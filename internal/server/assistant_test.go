package server

import (
	"bufio"
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rodericliu/backup/internal/api"
	"github.com/rodericliu/backup/internal/server/assistant"
	"github.com/rodericliu/backup/internal/store"
)

// fakeModel plays a model that calls the given tools in its first round
// and answers in its second, recording the input of every request.
type fakeModel struct {
	mu     sync.Mutex
	rounds [][]string
	inputs []string
}

func (f *fakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Input []any `json:"input"`
	}
	b, _ := io.ReadAll(r.Body)
	json.Unmarshal(b, &req)
	in, _ := json.Marshal(req.Input)
	f.mu.Lock()
	f.inputs = append(f.inputs, string(in))
	var events []string
	if len(f.rounds) > 0 {
		events, f.rounds = f.rounds[0], f.rounds[1:]
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range append(events, `{"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":5}}}`) {
		fmt.Fprintf(w, "data: %s\n\n", e)
	}
}

func call(id, name, args string) string {
	b, _ := json.Marshal(map[string]any{"type": "response.output_item.done",
		"item": map[string]string{"type": "function_call", "call_id": id, "name": name, "arguments": args}})
	return string(b)
}

func text(t string) string {
	b, _ := json.Marshal(map[string]string{"type": "response.output_text.delta", "delta": t})
	return string(b)
}

func (e *testEnv) enableAssistant(f *fakeModel) {
	ts := httptest.NewServer(f)
	e.t.Cleanup(ts.Close)
	e.srv.ai = &assistant.Client{BaseURL: ts.URL, APIKey: "sk-test", HTTP: ts.Client()}
	e.srv.cfg.Assistant.Models = []string{"gpt-test", "gpt-other"}
	e.srv.cfg.Assistant.MaxToolRounds = 4
	e.srv.cfg.Assistant.Timeout = api.Duration(30 * time.Second)
}

// chat posts a question and returns the server-sent events.
func chat(t *testing.T, c *http.Client, url string, body any) []ChatEvent {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := c.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("chat = %d %s", resp.StatusCode, b)
	}
	var events []ChatEvent
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			var ev ChatEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				t.Fatalf("event %s: %v", data, err)
			}
			events = append(events, ev)
		}
	}
	return events
}

func TestAssistantChat(t *testing.T) {
	e := newTestEnv(t)
	admin := e.admin()
	token, hostID := e.setupHost(admin)
	var p ProjectView
	do(t, admin, "POST", e.api("/projects"), projectBody(hostID, "gays", "gays_maindb_slave", nil), &p)
	jobs := e.history(token, e.clock.Now().Add(-6*time.Hour))

	var info AssistantInfo
	do(t, admin, "GET", e.api("/assistant/config"), nil, &info)
	if info.Enabled {
		t.Errorf("assistant enabled without configuration")
	}
	if code := do(t, admin, "POST", e.api("/assistant/sessions"), nil, nil); code != http.StatusNotFound {
		t.Errorf("create session while disabled = %d", code)
	}

	f := &fakeModel{rounds: [][]string{
		{text("我查一下。"), call("c1", "find_projects", `{"query":"gays mysql slave"}`), call("c2", "latest_backups", `{"project":"gays"}`)},
		{text("gays 最近一次成功的增量在 `/data/backup/gays/...`。")},
		{call("c3", "restore_plan", `{"project":"gays"}`)},
		{text("按以下步骤恢复。")},
	}}
	e.enableAssistant(f)
	do(t, admin, "GET", e.api("/assistant/config"), nil, &info)
	if !info.Enabled || len(info.Models) != 2 {
		t.Errorf("config = %+v", info)
	}

	var sess store.AssistantSession
	if code := do(t, admin, "POST", e.api("/assistant/sessions"), nil, &sess); code != http.StatusCreated || sess.ID == 0 {
		t.Fatalf("create session = %d %+v", code, sess)
	}
	url := e.api(fmt.Sprintf("/assistant/sessions/%d/messages", sess.ID))
	if code := do(t, admin, "POST", url, chatRequest{Content: "x", Model: "gpt-unknown"}, nil); code != http.StatusBadRequest {
		t.Errorf("unknown model = %d", code)
	}
	if code := do(t, admin, "POST", url, chatRequest{Content: "  "}, nil); code != http.StatusBadRequest {
		t.Errorf("empty question = %d", code)
	}

	events := chat(t, admin, url, chatRequest{Content: "gays 的备份在服务器上哪个目录？"})
	var types []string
	results := map[string]string{}
	for _, ev := range events {
		types = append(types, ev.Type)
		if ev.ToolResult != nil {
			results[ev.ToolResult.Name] = ev.ToolResult.Content
			if ev.ToolResult.Error {
				t.Errorf("tool %s failed: %s", ev.ToolResult.Name, ev.ToolResult.Content)
			}
		}
	}
	want := "user_message content tool_call tool_result tool_call tool_result content done"
	if strings.Join(types, " ") != want {
		t.Errorf("events = %v, want %s", types, want)
	}
	if r := results["find_projects"]; !strings.Contains(r, `"container":"gays_maindb_slave"`) || !strings.Contains(r, `"host":"backup-esh-01"`) {
		t.Errorf("find_projects = %s", r)
	}
	i2 := jobs[2]
	if r := results["latest_backups"]; !strings.Contains(r, i2.Path) || !strings.Contains(r, `"newest_restore_point":{`) ||
		!strings.Contains(r, i2.ID.String()) {
		t.Errorf("latest_backups = %s", r)
	}
	done := events[len(events)-1].Message
	if done == nil || done.Content != "我查一下。\n\ngays 最近一次成功的增量在 `/data/backup/gays/...`。" || len(done.ToolCalls) != 2 ||
		done.Model != "gpt-test" || done.InputTokens != 20 {
		t.Errorf("answer = %+v", done)
	}
	if !strings.Contains(f.inputs[1], `"call_id":"c2","output":`) {
		t.Errorf("tool output not sent back: %s", f.inputs[1])
	}

	// A follow-up question replays the conversation with its tool calls,
	// and carries what the user is looking at.
	events = chat(t, admin, url, chatRequest{Content: "这个备份怎么恢复？", Model: "gpt-other", Context: ChatContext{JobID: i2.ID.String()}})
	if !strings.Contains(f.inputs[2], `"call_id":"c1","name":"find_projects"`) || !strings.Contains(f.inputs[2], "gays 最近一次成功的增量") ||
		!strings.Contains(f.inputs[2], "用户正在页面上查看：备份任务 "+i2.ID.String()) {
		t.Errorf("follow-up input = %s", f.inputs[2])
	}
	var plan string
	for _, ev := range events {
		if ev.ToolResult != nil && ev.ToolResult.Name == "restore_plan" {
			plan = ev.ToolResult.Content
		}
	}
	if !strings.Contains(plan, `"steps":[`) || !strings.Contains(plan, i2.ID.String()) || !strings.Contains(plan, "mysql-bin.000042") {
		t.Errorf("restore_plan = %s", plan)
	}

	var detail SessionDetail
	do(t, admin, "GET", e.api(fmt.Sprintf("/assistant/sessions/%d", sess.ID)), nil, &detail)
	if detail.Title != "gays 的备份在服务器上哪个目录？" || len(detail.Messages) != 4 || detail.Messages[1].ToolCalls[1].Name != "latest_backups" {
		t.Errorf("session = %+v", detail)
	}
	var list []store.AssistantSession
	do(t, admin, "GET", e.api("/assistant/sessions"), nil, &list)
	if len(list) != 1 || list[0].ID != sess.ID {
		t.Errorf("sessions = %+v", list)
	}

	var logs List[store.AuditLog]
	do(t, admin, "GET", e.api("/audit-logs?action="+auditAssistantChat), nil, &logs)
	if logs.Total != 2 || logs.Items[1].Detail["question"] != "gays 的备份在服务器上哪个目录？" {
		t.Errorf("audit = %+v", logs.Items)
	}

	// Sessions are private.
	if code := do(t, admin, "GET", e.api("/assistant/sessions/999999"), nil, nil); code != http.StatusNotFound {
		t.Errorf("unknown session = %d", code)
	}
	if code := do(t, admin, "PATCH", e.api(fmt.Sprintf("/assistant/sessions/%d", sess.ID)), sessionRequest{Title: "恢复 gays"}, &sess); code != http.StatusOK || sess.Title != "恢复 gays" {
		t.Errorf("rename = %d %+v", code, sess)
	}
	if code := do(t, admin, "DELETE", e.api(fmt.Sprintf("/assistant/sessions/%d", sess.ID)), nil, nil); code != http.StatusNoContent {
		t.Errorf("delete = %d", code)
	}
	if code := do(t, admin, "GET", e.api(fmt.Sprintf("/assistant/sessions/%d", sess.ID)), nil, nil); code != http.StatusNotFound {
		t.Errorf("deleted session = %d", code)
	}
}

func TestAssistantModelError(t *testing.T) {
	e := newTestEnv(t)
	admin := e.admin()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"code":"invalid_api_key","message":"bad key"}}`)
	}))
	defer ts.Close()
	e.enableAssistant(&fakeModel{})
	e.srv.ai.BaseURL, e.srv.ai.HTTP = ts.URL, ts.Client()
	var sess store.AssistantSession
	do(t, admin, "POST", e.api("/assistant/sessions"), sessionRequest{Title: "test"}, &sess)
	events := chat(t, admin, e.api(fmt.Sprintf("/assistant/sessions/%d/messages", sess.ID)), chatRequest{Content: "你好"})
	last := events[len(events)-1]
	if len(events) != 3 || events[1].Type != "error" || !strings.Contains(events[1].Error, "API key") || last.Message == nil || last.Message.Error == "" {
		t.Errorf("events = %+v", events)
	}
}

func TestAssistantTools(t *testing.T) {
	e := newTestEnv(t)
	admin := e.admin()
	token, hostID := e.setupHost(admin)
	var gays, fetisch ProjectView
	do(t, admin, "POST", e.api("/projects"), projectBody(hostID, "gays", "gays_maindb_slave", nil), &gays)
	do(t, admin, "POST", e.api("/projects"), projectBody(hostID, "fetisch", "fetisch_maindb_slave", nil), &fetisch)
	jobs := e.history(token, e.clock.Now().Add(-6*time.Hour))
	ctx := t.Context()
	s := e.srv

	// "fetisch mysql slave" names the container fetisch_maindb_slave.
	p, _, err := s.project(ctx, aiProjectRef{Project: "fetisch mysql slave"})
	if err != nil || p.ID != fetisch.ID {
		t.Errorf("project(fetisch mysql slave) = %v, %v", p, err)
	}
	if _, _, err := s.project(ctx, aiProjectRef{Project: "maindb"}); err == nil || !strings.Contains(err.Error(), "several projects") {
		t.Errorf("ambiguous project: %v", err)
	}
	if _, _, err := s.project(ctx, aiProjectRef{Project: "nosuch"}); err == nil {
		t.Error("unknown project: no error")
	}
	if h, err := s.findHost(ctx, "esh-01"); err != nil || h.ID != hostID {
		t.Errorf("findHost = %v, %v", h, err)
	}

	res, err := s.aiBackupSummary(ctx, &struct {
		aiRange
		Host    string `json:"host"`
		Project string `json:"project"`
	}{Period: "today", Host: "backup-esh-01"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	for _, want := range []string{`"success":2`, `"warning":1`, `"failed":1`, `"skipped":1`, `"failures_total":1`, `"total_backup_size":"3.0 GiB"`, `"error_code":"XTRABACKUP_ERROR"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("summary lacks %s: %s", want, b)
		}
	}

	res, err = s.aiListJobs(ctx, &struct {
		aiProjectRef
		aiRange
		Status []string `json:"status"`
		Type   string   `json:"type"`
		Limit  int      `json:"limit"`
	}{Project: "gays", Status: []string{"failed"}})
	if err != nil {
		t.Fatal(err)
	}
	if m := res.(map[string]any); m["total"] != 1 || m["jobs"].([]aiJob)[0].ID != jobs[3].ID.String() {
		t.Errorf("failed jobs = %+v", m)
	}

	res, err = s.aiGetJob(ctx, &struct {
		JobID string `json:"job_id"`
	}{jobs[1].ID.String()})
	if err != nil {
		t.Fatal(err)
	}
	if m := res.(map[string]any); m["chain_dir"] != "/data/backup/gays/20261028-0300-chain" || m["base_job_id"] != jobs[0].ID.String() {
		t.Errorf("job = %+v", m)
	}

	// The newest restore point before a time.
	j, err := s.newestRestorable(ctx, gays.ID, jobs[1].FinishedAt.Add(time.Minute))
	if err != nil || j == nil || j.ID != jobs[1].ID {
		t.Errorf("newestRestorable = %v, %v", j, err)
	}
}

func TestResolvePeriods(t *testing.T) {
	e := newTestEnv(t)
	s := e.srv
	loc, _ := time.LoadLocation("Asia/Shanghai")
	s.cfg.Location = loc
	// 2026-10-29 is a Thursday.
	e.clock.now = time.Date(2026, 10, 29, 2, 30, 0, 0, time.UTC) // 10:30 in Shanghai
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, loc) }
	for period, want := range map[string][2]time.Time{
		"today":      {day(10, 29), day(10, 30)},
		"yesterday":  {day(10, 28), day(10, 29)},
		"this_week":  {day(10, 26), day(11, 2)},
		"last_week":  {day(10, 19), day(10, 26)},
		"this_month": {day(10, 1), day(11, 1)},
		"last_month": {day(9, 1), day(10, 1)},
	} {
		from, to, err := s.resolve(aiRange{Period: period}, "")
		if err != nil || !from.Equal(want[0]) || !to.Equal(want[1]) {
			t.Errorf("%s = %s ~ %s, %v; want %s ~ %s", period, from, to, err, want[0], want[1])
		}
	}
	from, to, err := s.resolve(aiRange{From: "2026-10-01", To: "2026-10-08 12:00"}, "")
	if err != nil || !from.Equal(day(10, 1)) || !to.Equal(day(10, 8).Add(12*time.Hour)) {
		t.Errorf("explicit range = %s ~ %s, %v", from, to, err)
	}
	if _, _, err := s.resolve(aiRange{Period: "fortnight"}, ""); err == nil {
		t.Error("unknown period: no error")
	}
}

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{
		"Access denied; password=s3cret for backup": "Access denied; password=[REDACTED] for backup",
		"--password='a b' --user=x":                 "--password=[REDACTED] --user=x",
		"token: abc,":                               "token: [REDACTED],",
		"no secrets here":                           "no secrets here",
	} {
		if got := redact(in); got != want {
			t.Errorf("redact(%q) = %q, want %q", in, got, want)
		}
	}
}
