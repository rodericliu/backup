package agent

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"uuid"

	"golang.org/x/sys/unix"

	"github.com/rodericliu/backup/internal/agent/cache"
	"github.com/rodericliu/backup/internal/agent/client"
	"github.com/rodericliu/backup/internal/agent/executor"
	"github.com/rodericliu/backup/internal/agent/mysql"
	"github.com/rodericliu/backup/internal/api"
	"github.com/rodericliu/backup/internal/crypto"
	"github.com/rodericliu/backup/internal/version"
)

// Meta keys of the local cache used by the link.
const (
	metaAgentToken    = "agent_token"
	metaRegisterToken = "register_token_hash"
	metaPaused        = "paused"
)

// Link timings; variables so that tests can shorten them.
var (
	// heartbeatInterval is how often heartbeats are sent (PRD 5.3.1).
	heartbeatInterval = api.HeartbeatInterval
	// reportInterval is how often unreported events and results are sent
	// (PRD E-6: progress every 5s).
	reportInterval = 5 * time.Second
	// commandWait is the long poll duration.
	commandWait = 30 * time.Second
	// retryInterval is the delay after a failed request.
	retryInterval = 15 * time.Second
)

const (
	// containerRefresh is how often the container list is refreshed.
	containerRefresh = 5 * time.Minute
	// reportBatch bounds events and jobs sent per round.
	reportBatch = 500
	// dataDirMount is the data directory inside replica containers.
	dataDirMount = "/var/lib/mysql"
)

// link connects the agent to backup-server (PRD 5.3). It registers the
// agent, sends heartbeats, applies configuration, reports jobs and events,
// and executes commands. Backups never depend on it: when the server is
// unavailable the worker keeps running with the cached configuration and
// results are reported once the server is back.
type link struct {
	a   *Agent
	c   *client.Client
	log *slog.Logger

	registered   atomic.Bool
	incompatible atomic.Bool
	online       atomic.Bool
	applied      atomic.Int64

	reportWake    chan struct{}
	heartbeatWake chan struct{}

	mu             sync.Mutex
	containers     []api.Container
	containersAt   time.Time
	bitmaps        map[string]api.BitmapUsage
	lastRegisterID string
}

func newLink(a *Agent, c *client.Client) *link {
	return &link{
		a: a, c: c, log: a.log.With("component", "link"),
		reportWake:    make(chan struct{}, 1),
		heartbeatWake: make(chan struct{}, 1),
		bitmaps:       make(map[string]api.BitmapUsage),
	}
}

func wake(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// sleep waits for d, a wake-up or the end of ctx; it reports whether ctx
// is still active.
func sleep(ctx context.Context, d time.Duration, woken <-chan struct{}) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
	case <-woken:
	}
	return true
}

// run serves the link until ctx is done.
func (l *link) run(ctx context.Context) {
	if tok, err := l.a.db.Meta(ctx, metaAgentToken); err != nil {
		l.log.Error("read agent token", "err", err)
	} else if tok != "" {
		l.c.SetToken(tok)
		l.registered.Store(true)
	}
	var wg sync.WaitGroup
	wg.Go(func() { l.heartbeatLoop(ctx) })
	wg.Go(func() { l.reportLoop(ctx) })
	wg.Go(func() { l.commandLoop(ctx) })
	wg.Wait()
}

// ready reports whether other requests than heartbeats may be sent.
func (l *link) ready() bool { return l.registered.Load() && !l.incompatible.Load() }

// register exchanges the registration token for an agent token (PRD H-2).
// A registration token is used once; a new one is used when the file changes,
// for example after the host was revoked and a new token was issued.
func (l *link) register(ctx context.Context) error {
	b, err := os.ReadFile(l.a.cfg.RegisterTokenFile)
	if err != nil {
		return fmt.Errorf("registration token: %w", err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return fmt.Errorf("registration token file %s is empty", l.a.cfg.RegisterTokenFile)
	}
	hash := crypto.HashToken(token)
	if used, err := l.a.db.Meta(ctx, metaRegisterToken); err != nil {
		return err
	} else if used == hash {
		return fmt.Errorf("registration token in %s was already used; put a new token there", l.a.cfg.RegisterTokenFile)
	}
	hostname, err := os.Hostname()
	if err != nil {
		return err
	}
	resp, err := l.c.Register(ctx, &api.RegisterRequest{Token: token, Hostname: hostname, AgentVersion: version.Version})
	if err != nil {
		if client.IsTokenInvalid(err) {
			// Do not retry a token the server rejected until the file changes.
			_ = l.a.db.SetMeta(ctx, metaRegisterToken, hash)
		}
		return err
	}
	if err := errors.Join(l.a.db.SetMeta(ctx, metaAgentToken, resp.AgentToken), l.a.db.SetMeta(ctx, metaRegisterToken, hash)); err != nil {
		return err
	}
	l.c.SetToken(resp.AgentToken)
	l.registered.Store(true)
	l.log.Info("registered with backup-server", "host_id", resp.HostID, "name", resp.Name)
	return nil
}

func (l *link) heartbeatLoop(ctx context.Context) {
	var lastErr string
	for {
		err := l.heartbeat(ctx)
		switch {
		case err == nil:
			if !l.online.Swap(true) {
				l.log.Info("connected to backup-server")
			}
			lastErr = ""
		case ctx.Err() != nil:
			return
		default:
			l.online.Store(false)
			// Log each distinct error once instead of every 15 seconds.
			if msg := err.Error(); msg != lastErr {
				l.log.Warn("backup-server unavailable; backups continue with the cached configuration", "err", err)
				lastErr = msg
			}
		}
		if !sleep(ctx, heartbeatInterval, l.heartbeatWake) {
			return
		}
	}
}

func (l *link) heartbeat(ctx context.Context) error {
	if !l.registered.Load() {
		if err := l.register(ctx); err != nil {
			return fmt.Errorf("register: %w", err)
		}
	}
	resp, err := l.c.Heartbeat(ctx, l.heartbeatBody(ctx))
	switch {
	case client.IsAPIVersion(err):
		if !l.incompatible.Swap(true) {
			l.log.Error("backup-server speaks another agent API version; upgrade the agent or the server", "agent_api", api.AgentAPIVersion, "err", err)
		}
		return err
	case client.IsTokenInvalid(err):
		l.registered.Store(false)
		return fmt.Errorf("agent token rejected (host revoked?); a new registration token is needed: %w", err)
	case err != nil:
		return err
	}
	l.incompatible.Store(false)
	if resp.ConfigVersion != l.applied.Load() {
		if err := l.syncConfig(ctx); err != nil {
			return fmt.Errorf("sync configuration: %w", err)
		}
	}
	return nil
}

// syncConfig fetches, caches and applies the server configuration (PRD
// 5.3.4). Running jobs are not affected.
func (l *link) syncConfig(ctx context.Context) error {
	cfg, err := l.c.Config(ctx)
	if err != nil {
		return err
	}
	for i := range cfg.Projects {
		if err := cfg.Projects[i].Validate(); err != nil {
			return err
		}
	}
	if err := l.a.db.SaveConfig(ctx, cfg.Version, cfg.Projects); err != nil {
		return err
	}
	if err := l.a.db.SetMeta(ctx, metaPaused, fmt.Sprint(cfg.Paused)); err != nil {
		return err
	}
	l.a.worker.SetProjects(cfg.Projects)
	l.a.worker.SetPaused(cfg.Paused)
	l.applied.Store(cfg.Version)
	l.log.Info("applied configuration from backup-server", "version", cfg.Version, "projects", len(cfg.Projects), "paused", cfg.Paused)
	// Report the applied version right away (PRD P-10).
	wake(l.heartbeatWake)
	return nil
}

func (l *link) heartbeatBody(ctx context.Context) *api.Heartbeat {
	hostname, _ := os.Hostname()
	st := l.a.worker.Status()
	state := api.AgentState{Current: st.Current, Queue: st.Queue, Paused: st.Paused}
	if st.Current != nil {
		p := st.Progress
		state.Progress = &p
	}
	if state.Queue == nil {
		state.Queue = []api.Job{}
	}
	containers := l.containerList(ctx, false)
	return &api.Heartbeat{
		AgentVersion:  version.Version,
		Hostname:      hostname,
		BackupRoot:    l.a.cfg.BackupRoot,
		ConfigVersion: l.applied.Load(),
		Metrics:       api.HostMetrics{Disks: l.disks(containers), Bitmaps: l.bitmapList()},
		Containers:    containers,
		State:         state,
	}
}

// containerList returns the cached container list, refreshing it when it is
// old or refresh is set (PRD H-6).
func (l *link) containerList(ctx context.Context, refresh bool) []api.Container {
	l.mu.Lock()
	cached, at := l.containers, l.containersAt
	l.mu.Unlock()
	if !refresh && cached != nil && time.Since(at) < containerRefresh {
		return cached
	}
	cs, err := l.a.podman.List(ctx)
	if err != nil {
		l.log.Warn("list containers", "err", err)
		if cached == nil {
			return []api.Container{}
		}
		return cached
	}
	l.mu.Lock()
	l.containers, l.containersAt = cs, time.Now()
	l.mu.Unlock()
	return cs
}

// disks reports the backup root and the data directories of the configured
// projects' containers (PRD D-4).
func (l *link) disks(containers []api.Container) []api.DiskUsage {
	type target struct{ path, role string }
	targets := []target{{l.a.cfg.BackupRoot, "backup"}}
	for _, p := range l.a.worker.Projects() {
		i := slices.IndexFunc(containers, func(c api.Container) bool { return c.Name == p.Container })
		if i < 0 {
			continue
		}
		for _, m := range containers[i].Mounts {
			if m.Destination == dataDirMount {
				targets = append(targets, target{m.Source, "data"})
			}
		}
	}
	var (
		out  []api.DiskUsage
		seen = make(map[[2]int64]bool)
	)
	for _, t := range targets {
		var st unix.Statfs_t
		if err := statfs(t.path, &st); err != nil {
			l.log.Debug("statfs", "path", t.path, "err", err)
			continue
		}
		total := int64(uint64(st.Blocks) * uint64(st.Bsize))
		free := int64(uint64(st.Bavail) * uint64(st.Bsize))
		key := [2]int64{total, free}
		if t.role == "data" && seen[key] {
			continue // same filesystem as an earlier path
		}
		seen[key] = true
		out = append(out, api.DiskUsage{Path: t.path, Role: t.role, Total: total, Free: free})
	}
	return out
}

// statfs reports the filesystem of path or, when path does not exist yet,
// of its nearest existing parent.
func statfs(path string, st *unix.Statfs_t) error {
	for {
		err := unix.Statfs(path, st)
		if !errors.Is(err, unix.ENOENT) {
			return err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return err
		}
		path = parent
	}
}

func (l *link) setBitmap(project string, size int64) {
	l.mu.Lock()
	l.bitmaps[project] = api.BitmapUsage{Project: project, Bytes: size, CheckedAt: time.Now()}
	l.mu.Unlock()
}

func (l *link) bitmapList() []api.BitmapUsage {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]api.BitmapUsage, 0, len(l.bitmaps))
	for _, b := range l.bitmaps {
		out = append(out, b)
	}
	slices.SortFunc(out, func(a, b api.BitmapUsage) int { return strings.Compare(a.Project, b.Project) })
	return out
}

func (l *link) reportLoop(ctx context.Context) {
	for {
		if l.ready() {
			if err := l.report(ctx); err != nil && ctx.Err() == nil && l.online.Load() {
				l.log.Warn("report jobs", "err", err)
			}
		}
		if !sleep(ctx, reportInterval, l.reportWake) {
			return
		}
	}
}

// report sends pending events in order, each batch with its job's current
// state, and then the final results not yet reported (PRD 9: results are
// replayed in order after an outage). Requests the server rejects for good
// are logged and skipped so that they cannot block the queue.
func (l *link) report(ctx context.Context) error {
	db := l.a.db
	for {
		events, err := db.PendingEvents(ctx, reportBatch)
		if err != nil || len(events) == 0 {
			if err != nil {
				return err
			}
			break
		}
		for i := 0; i < len(events); {
			j := i + 1
			for j < len(events) && events[j].JobID == events[i].JobID {
				j++
			}
			if err := l.sendEvents(ctx, events[i:j]); err != nil {
				return err
			}
			if err := db.AckEvents(ctx, events[j-1].Seq); err != nil {
				return err
			}
			i = j
		}
		if len(events) < reportBatch {
			break
		}
	}
	for {
		jobs, err := db.UnreportedJobs(ctx, reportBatch)
		if err != nil || len(jobs) == 0 {
			return err
		}
		for _, u := range jobs {
			err := l.c.PutJob(ctx, &u.Job)
			if client.IsRejected(err) {
				l.log.Error("backup-server rejected job result; skipping it", "job", u.Job.ID.String(), "err", err)
			} else if err != nil {
				return err
			}
			if err := db.MarkReported(ctx, u.Job.ID, u.Rev); err != nil {
				return err
			}
		}
		if len(jobs) < reportBatch {
			return nil
		}
	}
}

func (l *link) sendEvents(ctx context.Context, events []cache.Event) error {
	id := events[0].JobID
	body := &api.JobEvents{Events: make([]api.JobEvent, len(events))}
	for i, e := range events {
		body.Events[i] = api.JobEvent{Seq: e.Seq, At: e.At, Type: e.Type, Payload: e.Payload}
	}
	job, err := l.a.db.Job(ctx, id)
	switch {
	case err == nil:
		body.Job = job
	case !errors.Is(err, cache.ErrNotFound):
		return err
	}
	err = l.c.JobEvents(ctx, id, body)
	if client.IsRejected(err) {
		l.log.Error("backup-server rejected job events; skipping them", "job", id.String(), "events", len(events), "err", err)
		return nil
	}
	return err
}

func (l *link) commandLoop(ctx context.Context) {
	for {
		if !l.ready() {
			if !sleep(ctx, retryInterval, nil) {
				return
			}
			continue
		}
		cmds, err := l.c.Commands(ctx, commandWait)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !sleep(ctx, retryInterval, nil) {
				return
			}
			continue
		}
		for _, cmd := range cmds {
			res := l.execute(ctx, &cmd)
			l.log.Info("command executed", "command", cmd.ID, "type", cmd.Type, "ok", res.OK, "message", res.Message)
			for attempt := 0; ; attempt++ {
				err := l.c.CommandResult(ctx, cmd.ID, res)
				if err == nil || client.IsRejected(err) || attempt == 3 || ctx.Err() != nil {
					if err != nil {
						l.log.Warn("report command result", "command", cmd.ID, "err", err)
					}
					break
				}
				if !sleep(ctx, time.Second<<attempt, nil) {
					return
				}
			}
		}
	}
}

func fail(format string, args ...any) *api.CommandResult {
	return &api.CommandResult{OK: false, Message: fmt.Sprintf(format, args...)}
}

func success(msg string, data any) *api.CommandResult {
	res := &api.CommandResult{OK: true, Message: msg}
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return fail("encode result: %v", err)
		}
		res.Data = b
	}
	return res
}

// execute runs one command (PRD 5.3.1).
func (l *link) execute(ctx context.Context, cmd *api.Command) *api.CommandResult {
	switch cmd.Type {
	case api.CmdRun:
		var p api.RunPayload
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return fail("decode payload: %v", err)
		}
		if p.Type != api.JobFull && p.Type != api.JobIncr {
			return fail("unknown job type %q", p.Type)
		}
		id, err := l.a.worker.Enqueue(ctx, p.Project, p.Type)
		if err != nil {
			return fail("%v", err)
		}
		return success("queued", api.RunResult{JobID: id})
	case api.CmdCancel:
		var p api.JobPayload
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return fail("decode payload: %v", err)
		}
		if !l.a.worker.Cancel(ctx, p.JobID) {
			// PRD 5.3.3: nothing to do when the job has already finished.
			return success("job already finished", nil)
		}
		return success("canceled", nil)
	case api.CmdUploadLog:
		var p api.JobPayload
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return fail("decode payload: %v", err)
		}
		return l.uploadLog(ctx, p.JobID)
	case api.CmdRescan:
		cs := l.containerList(ctx, true)
		wake(l.heartbeatWake)
		return success(fmt.Sprintf("%d containers", len(cs)), nil)
	case api.CmdTestConnection:
		var p api.TestConnectionPayload
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return fail("decode payload: %v", err)
		}
		checks := l.a.testConnection(ctx, &p.Project)
		res := success("all checks passed", api.TestConnectionResult{Checks: checks})
		for _, c := range checks {
			if !c.OK {
				res.OK, res.Message = false, c.Name+": "+c.Message
				break
			}
		}
		return res
	case api.CmdPurge:
		var p api.PurgePayload
		if err := json.Unmarshal(cmd.Payload, &p); err != nil {
			return fail("decode payload: %v", err)
		}
		if p.Project == "" || p.Before.IsZero() {
			return fail("project and before are required")
		}
		n, err := l.a.cleaner.Purge(ctx, p.Project, p.Before)
		if err != nil {
			return fail("deleted %d chains: %v", n, err)
		}
		return success(fmt.Sprintf("deleted %d chains", n), nil)
	default:
		return fail("unsupported command %q", cmd.Type)
	}
}

// uploadLog sends a job's backup.log, or its last api.MaxLogSize bytes.
func (l *link) uploadLog(ctx context.Context, id uuid.UUID) *api.CommandResult {
	job, err := l.a.db.Job(ctx, id)
	if err != nil {
		return fail("job %s: %v", id, err)
	}
	if job.Path == "" {
		return fail("job %s has no backup directory", id)
	}
	f, err := os.Open(filepath.Join(filepath.Dir(job.Path), executor.LogFile))
	if err != nil {
		return fail("open log: %v", err)
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > api.MaxLogSize {
		if _, err := f.Seek(-api.MaxLogSize, io.SeekEnd); err != nil {
			return fail("seek log: %v", err)
		}
	}
	b, err := io.ReadAll(io.LimitReader(f, api.MaxLogSize))
	if err != nil {
		return fail("read log: %v", err)
	}
	if err := l.c.UploadLog(ctx, id, b); err != nil {
		return fail("upload log: %v", err)
	}
	return success(fmt.Sprintf("uploaded %d bytes", len(b)), nil)
}

// testConnection checks that a project can be backed up (PRD P-9): the
// container runs, the account can connect, replication is healthy, the tools
// are installed and the backup root is mounted at the same path.
func (a *Agent) testConnection(ctx context.Context, p *api.ProjectConfig) []api.Check {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var checks []api.Check
	add := func(name string, err error, okMsg string) bool {
		c := api.Check{Name: name, OK: err == nil, Message: okMsg}
		if err != nil {
			c.Message = err.Error()
		}
		checks = append(checks, c)
		return err == nil
	}

	c, err := a.podman.Inspect(ctx, p.Container)
	if err == nil && !c.Running {
		err = fmt.Errorf("container %s is %s", p.Container, c.State)
	}
	if !add("container", err, "running") {
		return checks
	}
	_, err = a.podman.Output(ctx, p.Container, "sh", "-c", "command -v xtrabackup && command -v zstd && command -v mysql")
	add("tools", err, "xtrabackup, zstd and mysql found")
	_, err = a.podman.Output(ctx, p.Container, "test", "-d", a.cfg.BackupRoot)
	if err != nil {
		err = fmt.Errorf("%s is not mounted in the container: %w", a.cfg.BackupRoot, err)
	}
	add("backup_root", err, a.cfg.BackupRoot+" mounted")

	defaults, err := mysql.WriteDefaultsFile(filepath.Join(a.cfg.BackupRoot, executor.CredentialsDir), p.MySQL)
	if !add("credentials", err, "") {
		return checks
	}
	defer func() {
		if err := defaults.Remove(); err != nil {
			a.log.Error("remove credentials file", "err", err)
		}
	}()
	mc := &mysql.Client{Exec: a.podman, Container: p.Container, Defaults: defaults}
	err = mc.CheckReplication(ctx, p.LagThreshold.Std())
	add("replication", err, "IO and SQL threads running, lag below threshold")
	if p.HasIncr() {
		on, err := mc.TrackChangedPages(ctx)
		if err == nil && !on {
			err = errors.New("innodb_track_changed_pages is OFF; incremental backups will scan all pages")
		}
		add("changed_page_bitmap", err, "innodb_track_changed_pages is ON")
	}
	return checks
}
