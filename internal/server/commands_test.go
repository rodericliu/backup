package server

import (
	json "encoding/json/v2"
	"net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/rodericliu/backup/internal/api"
	"github.com/rodericliu/backup/internal/store"
)

// poll fetches commands as the agent.
func (e *testEnv) poll(token, wait string) []api.Command {
	e.t.Helper()
	var cmds []api.Command
	if code := e.agentDo(token, "GET", "/commands?wait="+wait, nil, &cmds); code != http.StatusOK {
		e.t.Fatalf("poll = %d", code)
	}
	return cmds
}

func TestLongPollAndResult(t *testing.T) {
	e := newTestEnv(t)
	admin := e.admin()
	token, hostID := e.setupHost(admin)
	var p ProjectView
	do(t, admin, "POST", e.api("/projects"), projectBody(hostID, "gays", "gays_maindb_slave", nil), &p)

	start := time.Now()
	if cmds := e.poll(token, "100ms"); len(cmds) != 0 || time.Since(start) < 100*time.Millisecond {
		t.Errorf("empty poll = %+v after %s", cmds, time.Since(start))
	}

	// A command queued while the agent waits wakes it immediately.
	got := make(chan []api.Command, 1)
	go func() {
		var cmds []api.Command
		do(t, e.agent.Client(), "GET", e.agent.URL+AgentAPIPrefix+"/commands?wait=20s", nil, &cmds,
			"Authorization", "Bearer "+token, api.HeaderAPIVersion, "1")
		got <- cmds
	}()
	time.Sleep(100 * time.Millisecond)
	var c store.Command
	start = time.Now()
	if code := do(t, admin, "POST", e.api("/projects/"+itoa(p.ID)+"/run"), runRequest{Type: api.JobFull}, &c); code != http.StatusAccepted {
		t.Fatalf("run = %d", code)
	}
	var cmds []api.Command
	select {
	case cmds = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("long poll not woken")
	}
	if time.Since(start) > 2*time.Second || len(cmds) != 1 || cmds[0].ID != c.ID || cmds[0].Type != api.CmdRun {
		t.Fatalf("delivered = %+v after %s", cmds, time.Since(start))
	}
	var run api.RunPayload
	if err := json.Unmarshal(cmds[0].Payload, &run); err != nil || run.Project != "gays" || run.Type != api.JobFull {
		t.Errorf("payload = %s %v", cmds[0].Payload, err)
	}
	// Delivered commands are not delivered again.
	if again := e.poll(token, "0s"); len(again) != 0 {
		t.Errorf("redelivered %+v", again)
	}
	do(t, admin, "GET", e.api("/commands/"+itoa(c.ID)), nil, &c)
	if c.Status != store.CommandSent || c.AckedAt.IsZero() {
		t.Errorf("command after delivery = %+v", c)
	}

	jobID := uuid.NewV7()
	data, _ := json.Marshal(api.RunResult{JobID: jobID})
	if code := e.agentDo(token, "POST", "/commands/"+itoa(c.ID)+"/result", api.CommandResult{OK: true, Data: data}, nil); code != http.StatusNoContent {
		t.Fatalf("result = %d", code)
	}
	if code := e.agentDo(token, "POST", "/commands/"+itoa(c.ID)+"/result", api.CommandResult{OK: true}, nil); code != http.StatusConflict {
		t.Errorf("second result = %d", code)
	}
	do(t, admin, "GET", e.api("/commands/"+itoa(c.ID)), nil, &c)
	var res api.CommandResult
	if err := json.Unmarshal(c.Result, &res); err != nil || c.Status != store.CommandDone || !res.OK || !strings.Contains(string(res.Data), jobID.String()) {
		t.Errorf("finished command = %+v %+v %v", c, res, err)
	}

	// Another host cannot answer this host's commands.
	do(t, admin, "POST", e.api("/hosts/"+itoa(hostID)+"/rescan"), nil, &c)
	e.poll(token, "0s")
	other, _ := e.registerHost(admin, "backup-inx-01")
	if code := e.agentDo(other, "POST", "/commands/"+itoa(c.ID)+"/result", api.CommandResult{OK: true}, nil); code != http.StatusConflict {
		t.Errorf("result from another host = %d", code)
	}

	if code := do(t, admin, "POST", e.api("/projects/"+itoa(p.ID)+"/run"), runRequest{Type: "diff"}, nil); code != http.StatusBadRequest {
		t.Errorf("invalid run type = %d", code)
	}
	var logs List[store.AuditLog]
	do(t, admin, "GET", e.api("/audit-logs?action="+auditJobRun), nil, &logs)
	if logs.Total != 1 {
		t.Errorf("run audit logs = %d", logs.Total)
	}
}

func TestCommandExpiry(t *testing.T) {
	e := newTestEnv(t)
	admin := e.admin()
	token, hostID := e.setupHost(admin)
	var c store.Command
	do(t, admin, "POST", e.api("/hosts/"+itoa(hostID)+"/rescan"), nil, &c)
	e.clock.Add(commandTTL)
	if cmds := e.poll(token, "0s"); len(cmds) != 0 {
		t.Errorf("expired command delivered: %+v", cmds)
	}
	if err := e.srv.expireCommands(t.Context()); err != nil {
		t.Fatal(err)
	}
	do(t, admin, "GET", e.api("/commands/"+itoa(c.ID)), nil, &c)
	if c.Status != store.CommandExpired {
		t.Errorf("status = %s", c.Status)
	}
}

func TestCancelAndLogRequest(t *testing.T) {
	e := newTestEnv(t)
	admin := e.admin()
	token, hostID := e.setupHost(admin)
	do(t, admin, "POST", e.api("/projects"), projectBody(hostID, "gays", "gays_maindb_slave", nil), nil)
	job := api.Job{ID: uuid.NewV7(), Project: "gays", Type: api.JobFull, Trigger: api.TriggerManual, Status: api.StatusRunning,
		ScheduledAt: e.clock.Now(), StartedAt: e.clock.Now()}
	e.agentDo(token, "POST", "/jobs/"+job.ID.String()+"/events", api.JobEvents{Job: &job}, nil)

	var c store.Command
	if code := do(t, admin, "POST", e.api("/jobs/"+job.ID.String()+"/cancel"), nil, &c); code != http.StatusAccepted || c.Type != api.CmdCancel {
		t.Fatalf("cancel = %d %+v", code, c)
	}
	if code := do(t, admin, "POST", e.api("/jobs/"+job.ID.String()+"/log/request"), nil, &c); code != http.StatusAccepted || c.Type != api.CmdUploadLog {
		t.Errorf("log request = %d %+v", code, c)
	}
	cmds := e.poll(token, "0s")
	if len(cmds) != 2 || cmds[0].Type != api.CmdCancel || cmds[1].Type != api.CmdUploadLog {
		t.Fatalf("commands = %+v", cmds)
	}
	var jp api.JobPayload
	if err := json.Unmarshal(cmds[0].Payload, &jp); err != nil || jp.JobID != job.ID {
		t.Errorf("cancel payload = %s", cmds[0].Payload)
	}

	job.Status, job.FinishedAt, job.ErrorCode = api.StatusCanceled, e.clock.Now(), api.ErrCanceled
	e.agentDo(token, "PUT", "/jobs/"+job.ID.String(), job, nil)
	if code := do(t, admin, "POST", e.api("/jobs/"+job.ID.String()+"/cancel"), nil, nil); code != http.StatusConflict {
		t.Errorf("cancel a finished job = %d", code)
	}
	if code := do(t, admin, "POST", e.api("/jobs/"+uuid.NewV7().String()+"/cancel"), nil, nil); code != http.StatusNotFound {
		t.Errorf("cancel an unknown job = %d", code)
	}
}

func TestTestConnection(t *testing.T) {
	e := newTestEnv(t)
	admin := e.admin()
	token, hostID := e.setupHost(admin)
	var p ProjectView
	do(t, admin, "POST", e.api("/projects"), projectBody(hostID, "gays", "gays_maindb_slave", nil), &p)

	// Unsaved configuration with a password.
	body := projectBody(hostID, "poppen", "poppen_maindb_slave", nil)
	body["mysql"] = map[string]any{"user": "backup", "password": "typed-in"}
	var c1, c2 store.Command
	if code := do(t, admin, "POST", e.api("/projects/test-connection"), body, &c1); code != http.StatusAccepted {
		t.Fatalf("test connection = %d", code)
	}
	// Saved project: the stored password is used.
	body = projectBody(hostID, "gays", "gays_maindb_slave", nil)
	body["mysql"] = map[string]any{"user": "backup"}
	if code := do(t, admin, "POST", e.api("/projects/test-connection?project_id="+itoa(p.ID)), body, &c2); code != http.StatusAccepted {
		t.Fatalf("test saved connection = %d", code)
	}
	for _, c := range []store.Command{c1, c2} {
		if strings.Contains(string(c.Payload), "secret") || strings.Contains(string(c.Payload), "typed-in") {
			t.Errorf("secret visible on the web: %s", c.Payload)
		}
	}

	cmds := e.poll(token, "0s")
	if len(cmds) != 2 {
		t.Fatalf("commands = %+v", cmds)
	}
	for i, want := range []string{"typed-in", "s3cret"} {
		var tp api.TestConnectionPayload
		if err := json.Unmarshal(cmds[i].Payload, &tp); err != nil || tp.Project.MySQL.Password != want || strings.Contains(string(cmds[i].Payload), "secret_enc") {
			t.Errorf("delivered payload %d = %s %v", i, cmds[i].Payload, err)
		}
	}
	res, _ := json.Marshal(api.TestConnectionResult{Checks: []api.Check{{Name: "container", OK: true}, {Name: "mysql", OK: false, Message: "Access denied"}}})
	e.agentDo(token, "POST", "/commands/"+itoa(c1.ID)+"/result", api.CommandResult{OK: false, Message: "mysql: Access denied", Data: res}, nil)
	stored, err := e.store.Command(t.Context(), c1.ID)
	if err != nil || stored.Status != store.CommandFailed {
		t.Errorf("command = %+v %v", stored, err)
	}
	var raw string
	if err := e.storePool(t).QueryRow(t.Context(), "SELECT payload::text FROM agent_commands WHERE id = $1", c1.ID).Scan(&raw); err != nil || strings.Contains(raw, "secret_enc") {
		t.Errorf("secret kept after the result: %s %v", raw, err)
	}
}

func TestPurge(t *testing.T) {
	e := newTestEnv(t)
	admin := e.admin()
	token, hostID := e.setupHost(admin)
	var gays, poppen ProjectView
	do(t, admin, "POST", e.api("/projects"), projectBody(hostID, "gays", "gays_maindb_slave", nil), &gays)
	do(t, admin, "POST", e.api("/projects"), projectBody(hostID, "poppen", "poppen_maindb_slave", map[string]any{"retention_days": 3}), &poppen)

	deletedAt := e.clock.Now()
	do(t, admin, "DELETE", e.api("/projects/"+itoa(gays.ID)+"?files=purge"), nil, nil)
	do(t, admin, "DELETE", e.api("/projects/"+itoa(poppen.ID)+"?files=keep"), nil, nil)
	cmds := e.poll(token, "0s")
	if len(cmds) != 1 || cmds[0].Type != api.CmdPurge {
		t.Fatalf("commands after delete = %+v", cmds)
	}
	var pp api.PurgePayload
	if err := json.Unmarshal(cmds[0].Payload, &pp); err != nil || pp.Project != "gays" || !pp.Before.Equal(deletedAt) {
		t.Errorf("purge payload = %s", cmds[0].Payload)
	}
	// No duplicate while the command is active.
	if err := e.srv.purgeDeleted(t.Context()); err != nil {
		t.Fatal(err)
	}
	if again := e.poll(token, "0s"); len(again) != 0 {
		t.Errorf("duplicate purge: %+v", again)
	}
	e.agentDo(token, "POST", "/commands/"+itoa(cmds[0].ID)+"/result", api.CommandResult{OK: true, Message: "deleted 3 chains"}, nil)
	if p, _ := e.store.Project(t.Context(), gays.ID); p.PurgedAt.IsZero() {
		t.Error("purged_at not set")
	}

	// The kept project is purged when its retention ends.
	e.clock.Add(3 * 24 * time.Hour)
	if err := e.srv.purgeDeleted(t.Context()); err != nil {
		t.Fatal(err)
	}
	cmds = e.poll(token, "0s")
	if len(cmds) != 1 || !strings.Contains(string(cmds[0].Payload), `"poppen"`) {
		t.Errorf("commands after retention = %+v", cmds)
	}
	var logs List[store.AuditLog]
	admin = e.login("admin", "admin-password-1") // the session expired meanwhile
	do(t, admin, "GET", e.api("/audit-logs?action="+auditProjectPurge), nil, &logs)
	if logs.Total != 1 {
		t.Errorf("purge audit logs = %d", logs.Total)
	}
}
