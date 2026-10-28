package agent

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/rodericliu/backup/internal/agent/agenttest"
	"github.com/rodericliu/backup/internal/agent/config"
	"github.com/rodericliu/backup/internal/api"
	"github.com/rodericliu/backup/internal/crypto"
	"github.com/rodericliu/backup/internal/server"
	serverconfig "github.com/rodericliu/backup/internal/server/config"
	"github.com/rodericliu/backup/internal/store"
	"github.com/rodericliu/backup/internal/store/storetest"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type linkEnv struct {
	t      *testing.T
	store  *store.Store
	sealer *crypto.Sealer
	down   atomic.Bool
	agent  *Agent
	hostID int64
}

// newLinkEnv starts backup-server over TLS and an agent registered with it.
func newLinkEnv(t *testing.T) *linkEnv {
	podman, _ := agenttest.Install(t)
	st := storetest.New(t)
	for _, v := range []*time.Duration{&heartbeatInterval, &reportInterval, &commandWait, &retryInterval} {
		old := *v
		*v = 100 * time.Millisecond
		t.Cleanup(func() { *v = old })
	}

	sealer, err := crypto.NewSealer(bytes.Repeat([]byte{3}, crypto.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	scfg, err := serverconfig.Parse([]byte("timezone: UTC"))
	if err != nil {
		t.Fatal(err)
	}
	e := &linkEnv{t: t, store: st, sealer: sealer}
	srv := server.New(server.Options{Store: st, Sealer: sealer, Config: scfg, Logger: slog.New(slog.DiscardHandler)})
	h := srv.AgentHandler()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if e.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	token := crypto.NewToken(crypto.PrefixRegister)
	now := time.Now()
	if err := st.CreateRegisterToken(t.Context(), &store.RegisterToken{ExpiresAt: now.Add(time.Hour), CreatedAt: now}, crypto.HashToken(token)); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(dir, "register.token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Local projects are ignored once server_url is set.
	cfg, err := config.Parse(fmt.Appendf(nil, `
server_url: %[3]s
server_ca: %[4]s
register_token_file: %[5]s
state_dir: %[1]s/state
backup_root: %[1]s/backup
lock_file: %[1]s/run/agent.lock
podman: %[2]s
timezone: UTC
zstd: {threads: 1, level: 1}
log: {dir: ""}
projects:
  - name: local
    container: local_slave
    mysql: {user: backup}
`, dir, podman, ts.URL, ca, tokenFile))
	if err != nil {
		t.Fatal(err)
	}
	e.agent, err = New(t.Context(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.agent.Close() })
	if ps := e.agent.Worker().Projects(); len(ps) != 0 {
		t.Errorf("local projects used with a server: %+v", ps)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.agent.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	waitFor(t, "registration and heartbeat", func() bool {
		hosts, err := st.Hosts(t.Context())
		if err != nil || len(hosts) != 1 || hosts[0].LastHeartbeatAt.IsZero() {
			return false
		}
		e.hostID = hosts[0].ID
		return true
	})
	return e
}

func (e *linkEnv) host() *store.Host {
	e.t.Helper()
	h, err := e.store.Host(e.t.Context(), e.hostID)
	if err != nil {
		e.t.Fatal(err)
	}
	return h
}

// addProject creates a daily project whose schedule stays clear of the test.
func (e *linkEnv) addProject(name, container string) *store.Project {
	e.t.Helper()
	p := &store.Project{ProjectConfig: api.Template(api.TierNormal), HostID: e.hostID}
	p.Name, p.Container, p.MySQL.User = name, container, "backup"
	now := time.Now().UTC()
	p.Offset = api.Duration((time.Duration(now.Hour()+12)%24)*time.Hour + time.Duration(now.Minute())*time.Minute)
	err := e.store.CreateProject(e.t.Context(), p, func(id int64) []byte {
		return e.sealer.Seal([]byte("s3cret"), fmt.Appendf(nil, "project:%d", id))
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

// command sends a command and waits for its result.
func (e *linkEnv) command(typ api.CommandType, payload any) *store.Command {
	e.t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		e.t.Fatal(err)
	}
	now := time.Now()
	c := &store.Command{HostID: e.hostID, Type: typ, Payload: b, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := e.store.CreateCommand(e.t.Context(), c); err != nil {
		e.t.Fatal(err)
	}
	waitFor(e.t, string(typ)+" command result", func() bool {
		c, err = e.store.Command(e.t.Context(), c.ID)
		return err == nil && (c.Status == store.CommandDone || c.Status == store.CommandFailed)
	})
	return c
}

func (e *linkEnv) serverJob(id uuid.UUID, terminal bool) *store.Job {
	e.t.Helper()
	var j *store.Job
	waitFor(e.t, "job "+id.String()+" on the server", func() bool {
		var err error
		j, err = e.store.Job(e.t.Context(), id)
		return err == nil && (!terminal || j.Status.Terminal())
	})
	return j
}

func TestLinkEndToEnd(t *testing.T) {
	e := newLinkEnv(t)
	ctx := t.Context()

	h := e.host()
	if len(h.Containers) != 1 || h.Containers[0].Name != "gays_maindb_slave" || h.BackupRoot == "" || len(h.Metrics.Disks) == 0 {
		t.Errorf("heartbeat = containers %+v, backup root %q, disks %+v", h.Containers, h.BackupRoot, h.Metrics.Disks)
	}

	// Configuration (PRD 5.3.4, P-10).
	p := e.addProject("gays", "gays_maindb_slave")
	waitFor(t, "configuration applied", func() bool {
		h := e.host()
		ps := e.agent.Worker().Projects()
		return h.AppliedConfigVersion == h.ConfigVersion && len(ps) == 1 && ps[0].MySQL.Password == "s3cret"
	})
	if v, cached, err := e.agent.db.LoadConfig(ctx); err != nil || len(cached) != 1 || v != e.host().ConfigVersion {
		t.Errorf("cached config = %d %+v %v", v, cached, err)
	}

	// Manual backup through the command channel (PRD P-8).
	c := e.command(api.CmdRun, api.RunPayload{Project: "gays", Type: api.JobFull})
	var res api.CommandResult
	var run api.RunResult
	if err := json.Unmarshal(c.Result, &res); err != nil || !res.OK || json.Unmarshal(res.Data, &run) != nil {
		t.Fatalf("run result = %s %v", c.Result, err)
	}
	j := e.serverJob(run.JobID, true)
	if j.Status != api.StatusSuccess || j.ProjectID != p.ID || j.Trigger != api.TriggerManual || j.CompressedSize == 0 || j.ToLSN == 0 {
		t.Fatalf("job on the server = %+v", j)
	}
	chain, err := e.store.Chain(ctx, j.ChainID)
	if err != nil || chain.Status != store.ChainActive {
		t.Errorf("chain = %+v %v", chain, err)
	}
	waitFor(t, "job events", func() bool {
		events, err := e.store.JobEvents(ctx, j.ID)
		return err == nil && len(events) >= 3 // pending, running, success
	})

	// Connection test (PRD P-9).
	tp := p.ProjectConfig
	tp.MySQL.Password = "s3cret"
	c = e.command(api.CmdTestConnection, api.TestConnectionPayload{Project: tp})
	var tr api.TestConnectionResult
	if err := json.Unmarshal(c.Result, &res); err != nil || !res.OK || json.Unmarshal(res.Data, &tr) != nil || len(tr.Checks) < 4 {
		t.Errorf("test connection = %s", c.Result)
	}

	// Log upload (PRD J-4) and cancel of a finished job (PRD 5.3.3).
	if c = e.command(api.CmdUploadLog, api.JobPayload{JobID: j.ID}); c.Status != store.CommandDone {
		t.Errorf("upload log = %+v %s", c, c.Result)
	}
	if log, _, err := e.store.JobLog(ctx, j.ID); err != nil || !bytes.Contains(log, []byte("completed OK!")) {
		t.Errorf("uploaded log = %q %v", log, err)
	}
	if c = e.command(api.CmdCancel, api.JobPayload{JobID: j.ID}); c.Status != store.CommandDone || !bytes.Contains(c.Result, []byte("already finished")) {
		t.Errorf("cancel finished job = %s", c.Result)
	}

	// The server goes away: backups continue and are replayed later (PRD 9).
	e.down.Store(true)
	offline, err := e.agent.Worker().Enqueue(ctx, "gays", api.JobIncr)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "offline backup", func() bool {
		j, err := e.agent.db.Job(ctx, offline)
		return err == nil && j.Status.Terminal()
	})
	if _, err := e.store.Job(ctx, offline); err == nil {
		t.Error("job reported while the server was down")
	}
	e.down.Store(false)
	j = e.serverJob(offline, true)
	if j.Status != api.StatusSuccess || j.Type != api.JobIncr || j.ChainID != chain.ID || j.BaseJobID != run.JobID {
		t.Errorf("replayed job = %+v", j)
	}

	// Pausing is applied by the agent (PRD S-9).
	if err := e.store.SetHostPaused(ctx, e.hostID, true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "pause applied", func() bool { return e.agent.Worker().Status().Paused && e.host().State.Paused })

	// Deleting the project with its files (PRD P-7).
	c = e.command(api.CmdPurge, api.PurgePayload{Project: "gays", Before: time.Now().Add(time.Minute)})
	if c.Status != store.CommandDone || !bytes.Contains(c.Result, []byte("deleted 1 chains")) {
		t.Errorf("purge = %s", c.Result)
	}
	waitFor(t, "chain deleted on the server", func() bool {
		ch, err := e.store.Chain(ctx, chain.ID)
		return err == nil && ch.Status == store.ChainDeleted
	})
}

func TestLinkStartsFromCache(t *testing.T) {
	podman, _ := agenttest.Install(t)
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.crt")
	// Any certificate will do: the server is never reached.
	if err := crypto.InitCA(filepath.Join(dir, "ca"), "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "ca", crypto.CACertFile), ca); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, podman)
	cfg.ServerURL, cfg.ServerCA = "https://127.0.0.1:1", ca
	log := slog.New(slog.DiscardHandler)

	// Without a cached configuration the agent is idle.
	a, err := New(t.Context(), cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	if ps := a.Worker().Projects(); len(ps) != 0 {
		t.Errorf("projects without cached configuration = %+v", ps)
	}
	if err := a.db.SaveConfig(t.Context(), 7, cfg.Projects); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetMeta(t.Context(), metaPaused, "true"); err != nil {
		t.Fatal(err)
	}
	a.Close()

	// With a cached configuration the agent schedules it while the server is down.
	a, err = New(t.Context(), cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if ps := a.Worker().Projects(); len(ps) != 1 || !a.Worker().Status().Paused || a.link.applied.Load() != 7 {
		t.Errorf("projects from cache = %+v, paused %v, applied %d", ps, a.Worker().Status().Paused, a.link.applied.Load())
	}
}
