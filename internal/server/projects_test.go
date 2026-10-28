package server

import (
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rodericliu/backup/internal/api"
	"github.com/rodericliu/backup/internal/store"
)

// setupHost registers a host that reported the esh-01 containers.
func (e *testEnv) setupHost(admin *http.Client) (token string, hostID int64) {
	e.t.Helper()
	token, hostID = e.registerHost(admin, "backup-esh-01")
	hb := sampleHeartbeat()
	hb.Containers = nil
	for _, c := range []string{"fetisch_maindb_slave", "poppen_maindb_slave", "kmv2_maindb_slave", "gays_maindb_slave", "kmv2_message_slave"} {
		hb.Containers = append(hb.Containers, api.Container{Name: c, Running: true, State: "running"})
	}
	if code := e.agentDo(token, "POST", "/heartbeat", hb, nil); code != http.StatusOK {
		e.t.Fatalf("heartbeat = %d", code)
	}
	return token, hostID
}

func projectBody(hostID int64, name, container string, extra map[string]any) map[string]any {
	b := map[string]any{
		"host_id": hostID, "name": name, "container": container,
		"mysql": map[string]any{"user": "backup", "password": "s3cret"},
	}
	maps.Copy(b, extra)
	return b
}

func TestProjectCRUD(t *testing.T) {
	e := newTestEnv(t)
	admin := e.admin()
	token, hostID := e.setupHost(admin)

	var tmpl map[api.Tier]api.ProjectConfig
	do(t, admin, "GET", e.api("/project-templates"), nil, &tmpl)
	if tmpl[api.TierNormal].FullInterval.Std() != 24*time.Hour || tmpl[api.TierImportant].IncrInterval.Std() != time.Hour {
		t.Errorf("templates = %+v", tmpl)
	}

	var p ProjectView
	body := projectBody(hostID, "gays", "gays_maindb_slave", map[string]any{"offset": "3h", "priority": 4, "retention_days": 10})
	if code := do(t, admin, "POST", e.api("/projects"), body, &p); code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}
	if p.Tier != api.TierImportant || p.FullInterval.Std() != 4*time.Hour || p.Offset.Std() != 3*time.Hour ||
		p.RetentionDays != 10 || !p.PasswordSet || p.MySQL.Password != "" || p.ConfigVersion != 2 || p.Applied || p.HostName != "backup-esh-01" {
		t.Errorf("created = %+v", p)
	}
	stored, err := e.store.Project(t.Context(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored.PasswordEnc), "s3cret") {
		t.Error("password stored in plain text")
	}
	if pt, err := e.srv.sealer.Open(stored.PasswordEnc, projectAAD(p.ID)); err != nil || string(pt) != "s3cret" {
		t.Errorf("decrypt password = %q, %v", pt, err)
	}

	// Normal tier template: daily full without incrementals.
	var normal ProjectView
	if code := do(t, admin, "POST", e.api("/projects"), projectBody(hostID, "kmv2_message", "kmv2_message_slave", map[string]any{"tier": "normal"}), &normal); code != http.StatusCreated ||
		normal.FullInterval.Std() != 24*time.Hour || normal.HasIncr() {
		t.Errorf("normal = %d %+v", code, normal)
	}

	bad := []struct {
		name string
		body map[string]any
		code int
	}{
		{"duplicate name", projectBody(hostID, "gays", "poppen_maindb_slave", nil), http.StatusConflict},
		{"duplicate container", projectBody(hostID, "gays2", "gays_maindb_slave", nil), http.StatusConflict},
		{"unknown container", projectBody(hostID, "x", "nope", nil), http.StatusBadRequest},
		{"unknown host", projectBody(999, "x", "poppen_maindb_slave", nil), http.StatusBadRequest},
		{"invalid interval", projectBody(hostID, "x", "poppen_maindb_slave", map[string]any{"full_interval": "5h"}), http.StatusBadRequest},
		{"unknown field", projectBody(hostID, "x", "poppen_maindb_slave", map[string]any{"color": "red"}), http.StatusBadRequest},
		{"no password", map[string]any{"host_id": hostID, "name": "x", "container": "poppen_maindb_slave", "mysql": map[string]any{"user": "backup"}}, http.StatusBadRequest},
	}
	for _, tt := range bad {
		var eb errorBody
		if code := do(t, admin, "POST", e.api("/projects"), tt.body, &eb); code != tt.code {
			t.Errorf("%s = %d %+v, want %d", tt.name, code, eb, tt.code)
		}
	}

	// The agent applies version 3 (both projects).
	hb := sampleHeartbeat()
	hb.Containers = []api.Container{{Name: "gays_maindb_slave"}, {Name: "kmv2_message_slave"}, {Name: "poppen_maindb_slave"}}
	hb.ConfigVersion = 3
	e.agentDo(token, "POST", "/heartbeat", hb, nil)
	do(t, admin, "GET", e.api("/projects/"+itoa(p.ID)), nil, &p)
	if !p.Applied {
		t.Errorf("project not applied at version %d: %+v", hb.ConfigVersion, p)
	}

	// Update: keep the password, change the schedule.
	upd := projectBody(hostID, "gays", "gays_maindb_slave", map[string]any{"offset": "2h", "incr_mode": "differential"})
	upd["mysql"] = map[string]any{"user": "backup2"}
	if code := do(t, admin, "PUT", e.api("/projects/"+itoa(p.ID)), upd, &p); code != http.StatusOK {
		t.Fatalf("update = %d", code)
	}
	if p.Offset.Std() != 2*time.Hour || p.IncrMode != api.IncrDifferential || p.MySQL.User != "backup2" || p.RetentionDays != 7 ||
		!p.PasswordSet || p.ConfigVersion != 4 || p.Applied {
		t.Errorf("updated = %+v", p)
	}
	stored, _ = e.store.Project(t.Context(), p.ID)
	if pt, _ := e.srv.sealer.Open(stored.PasswordEnc, projectAAD(p.ID)); string(pt) != "s3cret" {
		t.Errorf("password after update = %q", pt)
	}
	rename := projectBody(hostID, "gays_new", "gays_maindb_slave", nil)
	if code := do(t, admin, "PUT", e.api("/projects/"+itoa(p.ID)), rename, nil); code != http.StatusBadRequest {
		t.Errorf("rename = %d", code)
	}

	// Disable and enable (PRD P-6).
	off := false
	do(t, admin, "PATCH", e.api("/projects/"+itoa(p.ID)), patchProjectRequest{Enabled: &off}, &p)
	if p.Enabled() || p.ConfigVersion != 5 {
		t.Errorf("disabled = %+v", p)
	}

	// Delete (PRD P-7).
	if code := do(t, admin, "DELETE", e.api("/projects/"+itoa(p.ID)), nil, nil); code != http.StatusBadRequest {
		t.Errorf("delete without policy = %d", code)
	}
	if code := do(t, admin, "DELETE", e.api("/projects/"+itoa(p.ID)+"?files=keep"), nil, nil); code != http.StatusNoContent {
		t.Errorf("delete = %d", code)
	}
	var list []ProjectView
	do(t, admin, "GET", e.api("/projects?host_id="+itoa(hostID)), nil, &list)
	if len(list) != 1 || list[0].Name != "kmv2_message" {
		t.Errorf("projects after delete = %+v", list)
	}
	do(t, admin, "GET", e.api("/projects?include_deleted=true"), nil, &list)
	if len(list) != 2 {
		t.Errorf("projects including deleted = %d", len(list))
	}
	if code := do(t, admin, "PUT", e.api("/projects/"+itoa(p.ID)), upd, nil); code != http.StatusConflict {
		t.Errorf("update deleted = %d", code)
	}
	// The name and container are free again.
	if code := do(t, admin, "POST", e.api("/projects"), projectBody(hostID, "gays", "gays_maindb_slave", nil), nil); code != http.StatusCreated {
		t.Errorf("recreate = %d", code)
	}

	var logs List[store.AuditLog]
	do(t, admin, "GET", e.api("/audit-logs?target_type=project"), nil, &logs)
	if logs.Total != 6 { // create ×2, update, disable, delete, create
		t.Errorf("project audit logs = %d", logs.Total)
	}
	for _, l := range logs.Items {
		for _, v := range l.Detail {
			if s, ok := v.(string); ok && strings.Contains(s, "s3cret") {
				t.Errorf("password in audit log %+v", l)
			}
		}
	}
}

func TestRecommendOffsetAPI(t *testing.T) {
	e := newTestEnv(t)
	admin := e.admin()
	_, hostID := e.setupHost(admin)
	for i, name := range []string{"fetisch", "poppen", "kmv2_maindb"} {
		body := projectBody(hostID, name, name+"_slave", map[string]any{"offset": (time.Duration(i) * time.Hour).String(), "priority": i + 1})
		body["container"] = map[string]string{"fetisch": "fetisch_maindb_slave", "poppen": "poppen_maindb_slave", "kmv2_maindb": "kmv2_maindb_slave"}[name]
		if code := do(t, admin, "POST", e.api("/projects"), body, nil); code != http.StatusCreated {
			t.Fatalf("create %s = %d", name, code)
		}
	}
	// Without history the defaults apply: 30m full, 5m incremental.
	var rec OffsetRecommendation
	if code := do(t, admin, "POST", e.api("/projects/recommend-offset"), map[string]any{"host_id": hostID, "tier": "important"}, &rec); code != http.StatusOK {
		t.Fatalf("recommend = %d", code)
	}
	if rec.Offset.Std() < 3*time.Hour || len(rec.Candidates) != 16 {
		t.Errorf("recommendation = %s, %d candidates", rec.Offset, len(rec.Candidates))
	}
	if code := do(t, admin, "POST", e.api("/projects/recommend-offset"), map[string]any{"host_id": hostID, "full_interval": "5h"}, nil); code != http.StatusBadRequest {
		t.Errorf("invalid interval = %d", code)
	}
}
