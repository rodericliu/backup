package server

import (
	json "encoding/json/v2"
	"encoding/json/jsontext"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/rodericliu/backup/internal/api"
	"github.com/rodericliu/backup/internal/store"
)

func TestAgentConfig(t *testing.T) {
	e := newTestEnv(t)
	admin := e.admin()
	token, hostID := e.setupHost(admin)
	var gays ProjectView
	do(t, admin, "POST", e.api("/projects"), projectBody(hostID, "gays", "gays_maindb_slave", map[string]any{"offset": "3h"}), &gays)
	do(t, admin, "POST", e.api("/projects"), projectBody(hostID, "poppen", "poppen_maindb_slave", nil), nil)
	do(t, admin, "DELETE", e.api("/projects/"+itoa(gays.ID)+"?files=keep"), nil, nil)
	paused := true
	do(t, admin, "PATCH", e.api("/hosts/"+itoa(hostID)), updateHostRequest{Paused: &paused}, nil)

	var cfg api.AgentConfig
	if code := e.agentDo(token, "GET", "/config", nil, &cfg); code != http.StatusOK {
		t.Fatalf("config = %d", code)
	}
	if cfg.Version != 5 || !cfg.Paused || len(cfg.Projects) != 1 {
		t.Fatalf("config = %+v", cfg)
	}
	p := cfg.Projects[0]
	if p.Name != "poppen" || p.MySQL.Password != "s3cret" || p.ID == 0 || p.FullInterval.Std() != 4*time.Hour {
		t.Errorf("project = %+v", p)
	}
	if code := e.agentDo("bat_wrong", "GET", "/config", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("config without token = %d", code)
	}
}

func event(seq int64, at time.Time, typ string, payload any) api.JobEvent {
	b, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return api.JobEvent{Seq: seq, At: at, Type: typ, Payload: jsontext.Value(b)}
}

func TestJobReports(t *testing.T) {
	e := newTestEnv(t)
	admin := e.admin()
	token, hostID := e.setupHost(admin)
	var gays ProjectView
	do(t, admin, "POST", e.api("/projects"), projectBody(hostID, "gays", "gays_maindb_slave", nil), &gays)
	ctx := t.Context()
	t0 := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)

	full := api.Job{
		ID: uuid.NewV7(), Project: "gays", Type: api.JobFull, Trigger: api.TriggerSchedule,
		Status: api.StatusPending, ScheduledAt: t0,
	}
	path := "/jobs/" + full.ID.String()
	// Queued: the job appears as soon as it is pending.
	if code := e.agentDo(token, "POST", path+"/events", api.JobEvents{Job: &full, Events: []api.JobEvent{
		event(1, t0, api.EventStatus, map[string]any{"status": "pending"}),
	}}, nil); code != http.StatusNoContent {
		t.Fatalf("pending events = %d", code)
	}
	j, err := e.store.Job(ctx, full.ID)
	if err != nil || j.Status != api.StatusPending || j.ProjectID != gays.ID || j.HostID != hostID {
		t.Fatalf("pending job = %+v, %v", j, err)
	}

	// Running with progress.
	running := full
	running.Status, running.StartedAt, running.ChainID = api.StatusRunning, t0.Add(time.Second), uuid.NewV7()
	progress := []api.JobEvent{
		event(2, t0.Add(time.Second), api.EventStatus, map[string]any{"status": "running"}),
		event(3, t0.Add(6*time.Second), api.EventProgress, api.Progress{CompressedBytes: 1 << 30, BytesPerSecond: 200 << 20}),
	}
	if code := e.agentDo(token, "POST", path+"/events", api.JobEvents{Job: &running, Events: progress}, nil); code != http.StatusNoContent {
		t.Fatalf("running events = %d", code)
	}
	if c, err := e.store.Chain(ctx, running.ChainID); err != nil || c.Status != store.ChainRunning || c.FullJobID != full.ID {
		t.Errorf("chain while running = %+v, %v", c, err)
	}

	// Final result.
	done := running
	done.Status, done.FinishedAt = api.StatusSuccess, t0.Add(30*time.Minute)
	done.CompressedSize, done.RawSize, done.FromLSN, done.ToLSN = 5<<30, 20<<30, 0, 18446744073709551000
	done.SlaveInfo = &api.SlaveInfo{MasterLogFile: "mysql-bin.000042", MasterLogPos: 4711}
	done.Path = "/data/backup/gays/20261028-0300-chain/00-full/backup.xbstream.zst"
	if code := e.agentDo(token, "PUT", path, done, nil); code != http.StatusNoContent {
		t.Fatalf("put = %d", code)
	}
	// Replays: the same events and a stale running snapshot change nothing.
	if code := e.agentDo(token, "POST", path+"/events", api.JobEvents{Job: &running, Events: progress}, nil); code != http.StatusNoContent {
		t.Fatalf("replayed events = %d", code)
	}
	if code := e.agentDo(token, "PUT", path, done, nil); code != http.StatusNoContent {
		t.Fatalf("replayed put = %d", code)
	}
	j, _ = e.store.Job(ctx, full.ID)
	if j.Status != api.StatusSuccess || j.ToLSN != done.ToLSN || j.SlaveInfo == nil || j.SlaveInfo.MasterLogPos != 4711 ||
		!j.FinishedAt.Equal(done.FinishedAt) || j.ChainID != done.ChainID || j.Path != done.Path {
		t.Errorf("final job = %+v", j)
	}
	events, err := e.store.JobEvents(ctx, full.ID)
	if err != nil || len(events) != 3 {
		t.Errorf("events = %d, %v", len(events), err)
	}
	c, _ := e.store.Chain(ctx, done.ChainID)
	if c.Status != store.ChainActive || !c.LastJobAt.Equal(done.FinishedAt) || c.ProjectID != gays.ID {
		t.Errorf("chain after full = %+v", c)
	}

	// An incremental extends the chain.
	incr := api.Job{
		ID: uuid.NewV7(), Project: "gays", Type: api.JobIncr, ChainID: done.ChainID, BaseJobID: full.ID,
		Trigger: api.TriggerSchedule, Status: api.StatusWarning, ScheduledAt: t0.Add(time.Hour),
		StartedAt: t0.Add(time.Hour), FinishedAt: t0.Add(65 * time.Minute),
	}
	if code := e.agentDo(token, "PUT", "/jobs/"+incr.ID.String(), incr, nil); code != http.StatusNoContent {
		t.Fatalf("put incr = %d", code)
	}
	c, _ = e.store.Chain(ctx, done.ChainID)
	if c.Status != store.ChainActive || !c.LastJobAt.Equal(incr.FinishedAt) {
		t.Errorf("chain after incr = %+v", c)
	}

	// Retention removes the chain (PRD R-3).
	cleanup := event(4, t0.Add(8*24*time.Hour), api.EventCleanup, api.CleanupEvent{ChainID: done.ChainID.String(), Reason: "expired"})
	if code := e.agentDo(token, "POST", path+"/events", api.JobEvents{Events: []api.JobEvent{cleanup}}, nil); code != http.StatusNoContent {
		t.Fatalf("cleanup event = %d", code)
	}
	c, _ = e.store.Chain(ctx, done.ChainID)
	if c.Status != store.ChainDeleted || !c.DeletedAt.Equal(cleanup.At) {
		t.Errorf("chain after cleanup = %+v", c)
	}
	// A late report of the full job does not resurrect the chain.
	e.agentDo(token, "PUT", path, done, nil)
	if c, _ = e.store.Chain(ctx, done.ChainID); c.Status != store.ChainDeleted {
		t.Errorf("chain resurrected: %+v", c)
	}

	// A failed full fails its chain.
	failed := api.Job{
		ID: uuid.NewV7(), Project: "gays", Type: api.JobFull, ChainID: uuid.NewV7(), Trigger: api.TriggerSchedule,
		Status: api.StatusFailed, ErrorCode: api.ErrXtrabackup, ScheduledAt: t0.Add(4 * time.Hour), StartedAt: t0.Add(4 * time.Hour),
		FinishedAt: t0.Add(4*time.Hour + time.Minute),
	}
	e.agentDo(token, "PUT", "/jobs/"+failed.ID.String(), failed, nil)
	if c, _ := e.store.Chain(ctx, failed.ChainID); c.Status != store.ChainFailed {
		t.Errorf("chain of failed full = %+v", c)
	}

	// Logs (PRD J-4).
	logReq, _ := http.NewRequest("POST", e.agent.URL+AgentAPIPrefix+path+"/log", strings.NewReader("xtrabackup log\ncompleted OK!\n"))
	logReq.Header.Set("Authorization", "Bearer "+token)
	logReq.Header.Set(api.HeaderAPIVersion, strconv.Itoa(api.AgentAPIVersion))
	logReq.Header.Set("Content-Type", "text/plain")
	resp, err := e.agent.Client().Do(logReq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("upload log = %d", resp.StatusCode)
	}
	if content, _, err := e.store.JobLog(ctx, full.ID); err != nil || !strings.Contains(string(content), "completed OK!") {
		t.Errorf("log = %q, %v", content, err)
	}

	// Errors.
	unknown := uuid.NewV7()
	if code := e.agentDo(token, "POST", "/jobs/"+unknown.String()+"/events", api.JobEvents{Events: []api.JobEvent{event(9, t0, "status", nil)}}, nil); code != http.StatusNotFound {
		t.Errorf("events of an unknown job without snapshot = %d", code)
	}
	if code := e.agentDo(token, "PUT", "/jobs/"+unknown.String(), running, nil); code != http.StatusBadRequest {
		t.Errorf("put with mismatched id = %d", code)
	}
	if code := e.agentDo(token, "PUT", path, running, nil); code != http.StatusBadRequest {
		t.Errorf("put of a non-final status = %d", code)
	}
	if code := e.agentDo(token, "PUT", "/jobs/not-a-uuid", done, nil); code != http.StatusBadRequest {
		t.Errorf("put with an invalid id = %d", code)
	}

	// Another host cannot report this host's jobs.
	other, _ := e.registerHost(admin, "backup-inx-01")
	if code := e.agentDo(other, "PUT", path, done, nil); code != http.StatusConflict {
		t.Errorf("put from another host = %d", code)
	}
	if code := e.agentDo(other, "POST", path+"/events", api.JobEvents{Events: []api.JobEvent{event(1, t0, "status", nil)}}, nil); code != http.StatusConflict {
		t.Errorf("events from another host = %d", code)
	}
}
