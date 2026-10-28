package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/rodericliu/backup/internal/agent/cache"
	"github.com/rodericliu/backup/internal/agent/executor"
	"github.com/rodericliu/backup/internal/agent/retention"
	"github.com/rodericliu/backup/internal/api"
)

var t0 = time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

func (c *clock) Add(d time.Duration) { c.Set(c.Now().Add(d)) }

type outcome struct {
	status api.JobStatus
	code   api.ErrorCode
}

// fakeRunner simulates the executor: it advances the clock by the job's
// duration and records the request.
type fakeRunner struct {
	clock    *clock
	duration func(*executor.Request) time.Duration
	outcome  func(*executor.Request) outcome
	block    bool // wait for cancellation instead of finishing

	mu       sync.Mutex
	reqs     []*executor.Request
	active   atomic.Int32
	overlaps atomic.Int32
	lsn      uint64
	started  chan struct{}
}

func (f *fakeRunner) Run(ctx context.Context, req *executor.Request) {
	if f.active.Add(1) > 1 {
		f.overlaps.Add(1)
	}
	defer f.active.Add(-1)
	f.mu.Lock()
	cp := *req
	f.reqs = append(f.reqs, &cp)
	f.mu.Unlock()

	job := req.Job
	job.Status = api.StatusRunning
	job.StartedAt = f.clock.Now()
	job.Path = filepath.Join(req.JobDir, executor.DataFile)
	if err := os.MkdirAll(req.JobDir, 0o700); err != nil {
		panic(err)
	}
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.block {
		<-ctx.Done()
		job.Status, job.ErrorCode = api.StatusCanceled, api.ErrCanceled
		if !errors.Is(context.Cause(ctx), executor.ErrCanceled) {
			job.Status, job.ErrorCode = api.StatusFailed, api.ErrInterrupted
		}
		job.FinishedAt = f.clock.Now()
		return
	}

	d := time.Minute
	if f.duration != nil {
		d = f.duration(req)
	}
	f.clock.Add(d)
	o := outcome{status: api.StatusSuccess}
	if f.outcome != nil {
		o = f.outcome(req)
	}
	job.Status, job.ErrorCode = o.status, o.code
	if o.status.Succeeded() {
		f.mu.Lock()
		f.lsn += 100
		job.FromLSN, job.ToLSN = f.lsn-100, f.lsn
		f.mu.Unlock()
		job.CompressedSize = 1000
		if err := os.WriteFile(job.Path, []byte("data"), 0o600); err != nil {
			panic(err)
		}
	}
	job.FinishedAt = f.clock.Now()
}

func (f *fakeRunner) requests() []*executor.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

type env struct {
	t      *testing.T
	clock  *clock
	runner *fakeRunner
	db     *cache.DB
	root   string
	w      *Worker
}

func setup(t *testing.T, projects ...api.ProjectConfig) *env {
	t.Helper()
	dir := t.TempDir()
	db, err := cache.Open(t.Context(), filepath.Join(dir, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	c := &clock{now: t0.Add(-time.Second)}
	root := filepath.Join(dir, "backup")
	log := slog.New(slog.DiscardHandler)
	r := &fakeRunner{clock: c}
	w := New(Options{
		DB: db, Runner: r, Root: root, Location: time.UTC, Logger: log, Now: c.Now,
		Cleaner: &retention.Cleaner{Store: db, Root: root, Now: c.Now, Logger: log},
	})
	w.SetProjects(projects)
	w.lastSchedule = c.Now()
	return &env{t: t, clock: c, runner: r, db: db, root: root, w: w}
}

func project(name string, offset time.Duration, prio int) api.ProjectConfig {
	p := api.Template(api.TierImportant)
	p.Name, p.Container, p.Offset, p.Priority = name, name+"_slave", api.Duration(offset), prio
	p.MySQL.User = "backup"
	return p
}

// advance moves the clock forward in one-minute steps until end, running
// every ready job like the Run loop does.
func (e *env) advance(end time.Time) {
	e.t.Helper()
	for e.clock.Now().Before(end) {
		for e.w.runOnce(e.t.Context()) {
		}
		e.clock.Add(time.Minute)
	}
	for e.w.runOnce(e.t.Context()) {
	}
}

func (e *env) jobs(project string) []api.Job {
	e.t.Helper()
	jobs, err := e.db.RecentJobs(e.t.Context(), project, time.Time{})
	if err != nil {
		e.t.Fatal(err)
	}
	return jobs
}

func describe(jobs []api.Job) []string {
	var out []string
	for _, j := range jobs {
		s := fmt.Sprintf("%s %s %s", j.ScheduledAt.UTC().Format("15:04"), j.Type, j.Status)
		if j.ErrorCode != "" {
			s += " " + string(j.ErrorCode)
		}
		out = append(out, s)
	}
	return out
}

func TestChainedSchedule(t *testing.T) {
	e := setup(t, project("gays", 0, 1))
	e.runner.duration = func(r *executor.Request) time.Duration {
		if r.Job.Type == api.JobFull {
			return 10 * time.Minute
		}
		return 2 * time.Minute
	}
	e.advance(t0.Add(4*time.Hour + time.Minute))

	jobs := e.jobs("gays")
	want := []string{"00:00 full success", "01:00 incr success", "02:00 incr success", "03:00 incr success", "04:00 full success"}
	if got := describe(jobs); !slices.Equal(got, want) {
		t.Fatalf("jobs:\n got %q\nwant %q", got, want)
	}

	reqs := e.runner.requests()
	chain := jobs[0].ChainID
	for i, j := range jobs[:4] {
		if j.ChainID != chain {
			t.Errorf("job %d in chain %s, want %s", i, j.ChainID, chain)
		}
		wantDir := "00-full"
		if i > 0 {
			wantDir = fmt.Sprintf("%02d-incr", i)
			if j.BaseJobID != jobs[i-1].ID {
				t.Errorf("incr %d base = %s, want previous job (chained)", i, j.BaseJobID)
			}
			if reqs[i].BaseDir != filepath.Dir(jobs[i-1].Path) {
				t.Errorf("incr %d base dir = %s", i, reqs[i].BaseDir)
			}
			if reqs[i].PurgeLSN != jobs[0].ToLSN {
				t.Errorf("incr %d purge lsn = %d, want full to_lsn %d", i, reqs[i].PurgeLSN, jobs[0].ToLSN)
			}
		}
		if filepath.Base(reqs[i].JobDir) != wantDir {
			t.Errorf("job %d dir = %s, want %s", i, reqs[i].JobDir, wantDir)
		}
	}
	if jobs[4].ChainID == chain {
		t.Error("second full should start a new chain")
	}
	if !strings.HasSuffix(filepath.Dir(reqs[0].JobDir), filepath.Join("gays", "20261028-0000-chain")) {
		t.Errorf("chain dir = %s", filepath.Dir(reqs[0].JobDir))
	}
	if reqs[4].EstimatedSize != 1000 {
		t.Errorf("estimated size from history = %d, want 1000", reqs[4].EstimatedSize)
	}
	chains, _ := e.db.Chains(t.Context(), "gays")
	if len(chains) != 2 || chains[0].Status != cache.ChainActive || !chains[0].LastJobAt.Equal(jobs[3].FinishedAt) {
		t.Errorf("chains = %+v", chains)
	}
	if e.runner.overlaps.Load() != 0 {
		t.Error("jobs overlapped")
	}
}

func TestDifferentialBase(t *testing.T) {
	p := project("gays", 0, 1)
	p.IncrMode = api.IncrDifferential
	e := setup(t, p)
	e.advance(t0.Add(3*time.Hour + time.Minute))
	jobs := e.jobs("gays")
	if len(jobs) != 4 {
		t.Fatalf("jobs = %q", describe(jobs))
	}
	for _, j := range jobs[1:] {
		if j.BaseJobID != jobs[0].ID {
			t.Errorf("differential incr base = %s, want full %s", j.BaseJobID, jobs[0].ID)
		}
	}
}

func TestOrderAndSupersede(t *testing.T) {
	e := setup(t, project("slow", 0, 1), project("fast", 0, 2))
	e.runner.duration = func(r *executor.Request) time.Duration {
		if r.Job.Project == "slow" && r.Job.Type == api.JobFull {
			return 150 * time.Minute
		}
		return time.Minute
	}
	e.advance(t0.Add(3 * time.Hour))

	reqs := e.runner.requests()
	if reqs[0].Job.Project != "slow" || reqs[1].Job.Project != "fast" {
		t.Errorf("order: %s then %s; want priority order", reqs[0].Job.Project, reqs[1].Job.Project)
	}
	for _, name := range []string{"slow", "fast"} {
		var superseded int
		for _, j := range e.jobs(name) {
			if j.Status == api.StatusSkipped && j.ErrorCode == api.ErrSuperseded {
				superseded++
				if !j.ScheduledAt.Equal(t0.Add(time.Hour)) {
					t.Errorf("%s: superseded job scheduled at %s", name, j.ScheduledAt)
				}
			}
		}
		if superseded != 1 {
			t.Errorf("%s: %d superseded jobs, want 1:\n%q", name, superseded, describe(e.jobs(name)))
		}
	}
}

func TestFullFailureRetry(t *testing.T) {
	tests := []struct {
		name     string
		failures int
		want     []string
	}{
		{"retry succeeds", 1, []string{
			"00:00 full failed REPLICATION_LAG", "00:11 full success", "01:00 incr success",
		}},
		{"retry fails", 2, []string{
			"00:00 full failed REPLICATION_LAG", "00:11 full failed REPLICATION_LAG", "01:00 incr skipped NO_BASE",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := setup(t, project("gays", 0, 1))
			var fulls int
			e.runner.outcome = func(r *executor.Request) outcome {
				if r.Job.Type == api.JobFull {
					fulls++
					if fulls <= tt.failures {
						return outcome{api.StatusFailed, api.ErrReplicationLag}
					}
				}
				return outcome{status: api.StatusSuccess}
			}
			e.advance(t0.Add(time.Hour + time.Minute))
			jobs := e.jobs("gays")
			if got := describe(jobs); !slices.Equal(got, tt.want) {
				t.Errorf("jobs:\n got %q\nwant %q", got, tt.want)
			}
			if jobs[1].Trigger != api.TriggerRetry {
				t.Errorf("second job trigger = %s, want retry", jobs[1].Trigger)
			}
		})
	}
}

func TestIncrementalWithoutFullBecomesFull(t *testing.T) {
	e := setup(t, project("gays", 0, 1))
	e.clock.Set(t0.Add(30 * time.Minute))
	e.w.lastSchedule = e.clock.Now()
	e.advance(t0.Add(2*time.Hour + time.Minute))
	jobs := e.jobs("gays")
	if got, want := describe(jobs), []string{"01:00 full success", "02:00 incr success"}; !slices.Equal(got, want) {
		t.Errorf("jobs:\n got %q\nwant %q", got, want)
	}
}

func TestIncrementalFailureUsesLastSuccess(t *testing.T) {
	e := setup(t, project("gays", 0, 1))
	e.runner.outcome = func(r *executor.Request) outcome {
		if r.Job.ScheduledAt.Equal(t0.Add(2 * time.Hour)) {
			return outcome{api.StatusFailed, api.ErrXtrabackup}
		}
		return outcome{status: api.StatusSuccess}
	}
	e.advance(t0.Add(3*time.Hour + time.Minute))
	jobs := e.jobs("gays")
	if len(jobs) != 4 || jobs[2].Status != api.StatusFailed {
		t.Fatalf("jobs = %q", describe(jobs))
	}
	if jobs[3].BaseJobID != jobs[1].ID {
		t.Errorf("incr after failure based on %s, want last success %s", jobs[3].BaseJobID, jobs[1].ID)
	}
	if reqs := e.runner.requests(); filepath.Base(reqs[3].JobDir) != "03-incr" {
		t.Errorf("dir = %s, want 03-incr", reqs[3].JobDir)
	}
}

func TestDisabledAndRemovedProjects(t *testing.T) {
	e := setup(t, project("gays", 0, 1))
	id, err := e.w.Enqueue(t.Context(), "gays", api.JobFull)
	if err != nil {
		t.Fatal(err)
	}
	p := project("gays", 0, 1)
	p.Disabled = true
	e.w.SetProjects([]api.ProjectConfig{p})
	e.advance(t0.Add(2 * time.Hour))

	jobs := e.jobs("gays")
	if len(jobs) != 1 || jobs[0].ID != id || jobs[0].Status != api.StatusSkipped {
		t.Errorf("jobs = %q; disabled project should not be scheduled and queued job skipped", describe(jobs))
	}
	if len(e.runner.requests()) != 0 {
		t.Error("disabled project ran")
	}
	if _, err := e.w.Enqueue(t.Context(), "nope", api.JobFull); !errors.Is(err, ErrUnknownProject) {
		t.Errorf("unknown project: err = %v", err)
	}
}

func TestCancelPending(t *testing.T) {
	e := setup(t, project("gays", 0, 1))
	id, _ := e.w.Enqueue(t.Context(), "gays", api.JobFull)
	if s := e.w.Status(); len(s.Queue) != 1 || s.Queue[0].ID != id || s.Current != nil {
		t.Errorf("status = %+v", s)
	}
	if !e.w.Cancel(t.Context(), id) {
		t.Fatal("cancel returned false")
	}
	if e.w.Cancel(t.Context(), uuid.NewV7()) {
		t.Error("cancel of unknown job returned true")
	}
	job, _ := e.db.Job(t.Context(), id)
	if job.Status != api.StatusCanceled || job.ErrorCode != api.ErrCanceled {
		t.Errorf("job = %s %s", job.Status, job.ErrorCode)
	}
}

func TestRunCancelAndShutdown(t *testing.T) {
	e := setup(t, project("gays", 0, 1))
	e.clock.Set(t0.Add(10 * time.Minute)) // nothing scheduled soon
	e.runner.block = true
	e.runner.started = make(chan struct{}, 1)
	e.w.opts.Tick = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- e.w.Run(ctx) }()

	id, err := e.w.Enqueue(t.Context(), "gays", api.JobFull)
	if err != nil {
		t.Fatal(err)
	}
	waitStarted(t, e.runner.started)
	if s := e.w.Status(); s.Current == nil || s.Current.ID != id {
		t.Errorf("status current = %+v", s.Current)
	}
	if !e.w.Cancel(t.Context(), id) {
		t.Fatal("cancel running job returned false")
	}
	eventually(t, func() bool {
		j, err := e.db.Job(t.Context(), id)
		return err == nil && j.Status == api.StatusCanceled
	})

	id2, _ := e.w.Enqueue(t.Context(), "gays", api.JobFull)
	waitStarted(t, e.runner.started)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after shutdown")
	}
	j, _ := e.db.Job(t.Context(), id2)
	if j.Status != api.StatusFailed || j.ErrorCode != api.ErrInterrupted {
		t.Errorf("job interrupted by shutdown = %s %s", j.Status, j.ErrorCode)
	}
	if n := len(e.jobs("gays")); n != 2 {
		t.Errorf("%d jobs; interrupted full must not be retried during shutdown", n)
	}
}

func waitStarted(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("job did not start")
	}
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRecover(t *testing.T) {
	e := setup(t, project("gays", 0, 1))
	ctx := t.Context()
	chainDir := filepath.Join(e.root, "gays", "20261027-2000-chain")
	jobDir := filepath.Join(chainDir, "00-full")
	if err := os.MkdirAll(jobDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{executor.DataFile, executor.CheckpointsFile, executor.LogFile} {
		if err := os.WriteFile(filepath.Join(jobDir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	chain := &cache.Chain{ID: uuid.NewV7(), Project: "gays", Dir: chainDir, Status: cache.ChainRunning, StartedAt: t0.Add(-4 * time.Hour)}
	running := &api.Job{ID: uuid.NewV7(), Project: "gays", Type: api.JobFull, ChainID: chain.ID, Status: api.StatusRunning,
		ScheduledAt: chain.StartedAt, Path: filepath.Join(jobDir, executor.DataFile)}
	chain.FullJobID = running.ID
	pending := &api.Job{ID: uuid.NewV7(), Project: "gays", Type: api.JobIncr, Status: api.StatusPending, ScheduledAt: t0.Add(-3 * time.Hour)}
	if err := e.db.SaveChain(ctx, chain); err != nil {
		t.Fatal(err)
	}
	for _, j := range []*api.Job{running, pending} {
		if err := e.db.SaveJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	creds := filepath.Join(e.root, executor.CredentialsDir)
	if err := os.MkdirAll(creds, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(creds, "my-old.cnf"), []byte("password=x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := e.w.recover(ctx); err != nil {
		t.Fatal(err)
	}
	if j, _ := e.db.Job(ctx, running.ID); j.Status != api.StatusFailed || j.ErrorCode != api.ErrInterrupted {
		t.Errorf("running job = %s %s", j.Status, j.ErrorCode)
	}
	if j, _ := e.db.Job(ctx, pending.ID); j.Status != api.StatusSkipped || j.ErrorCode != api.ErrInterrupted {
		t.Errorf("pending job = %s %s", j.Status, j.ErrorCode)
	}
	if c, _ := e.db.Chain(ctx, chain.ID); c.Status != cache.ChainFailed {
		t.Errorf("chain = %s, want failed", c.Status)
	}
	for f, want := range map[string]bool{executor.DataFile: false, executor.CheckpointsFile: false, executor.LogFile: true} {
		if _, err := os.Stat(filepath.Join(jobDir, f)); (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", f, err == nil, want)
		}
	}
	if entries, _ := os.ReadDir(creds); len(entries) != 0 {
		t.Errorf("stale credentials left: %v", entries)
	}
}

func TestRetentionAfterSuccess(t *testing.T) {
	p := project("gays", 0, 1)
	p.RetentionDays = 1
	e := setup(t, p)
	old := &cache.Chain{
		ID: uuid.NewV7(), Project: "gays", Status: cache.ChainActive, StartedAt: t0.Add(-72 * time.Hour),
		Dir: filepath.Join(e.root, "gays", "20261025-0000-chain"),
	}
	if err := os.MkdirAll(old.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := e.db.SaveChain(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	e.advance(t0.Add(time.Minute))
	if _, err := os.Stat(old.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expired chain not deleted after successful full: %v", err)
	}
}

func TestLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "agent.lock")
	unlock, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(path); !errors.Is(err, ErrLocked) {
		t.Errorf("second lock: err = %v, want ErrLocked", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	unlock2, err := Lock(path)
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	_ = unlock2()
}

func TestRecoverKeepsJobsQueuedByThisProcess(t *testing.T) {
	e := setup(t, project("gays", 0, 1))
	id, err := e.w.Enqueue(t.Context(), "gays", api.JobFull)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.w.recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if j, _ := e.db.Job(t.Context(), id); j.Status != api.StatusPending {
		t.Errorf("job enqueued before Run = %s, want pending", j.Status)
	}
	if s := e.w.Status(); len(s.Queue) != 1 {
		t.Errorf("queue = %d, want 1", len(s.Queue))
	}
}
