package server

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rodericliu/backup/internal/api"
	"github.com/rodericliu/backup/internal/store"
)

// agentDo sends an agent API request with the agent token and API version.
func (e *testEnv) agentDo(token, method, path string, body, out any) int {
	e.t.Helper()
	return e.agentDoVersion(api.AgentAPIVersion, token, method, path, body, out)
}

func (e *testEnv) agentDoVersion(version int, token, method, path string, body, out any) int {
	e.t.Helper()
	return do(e.t, e.agent.Client(), method, e.agent.URL+AgentAPIPrefix+path, body, out,
		"Authorization", "Bearer "+token, api.HeaderAPIVersion, strconv.Itoa(version))
}

// registerToken creates a registration token as the administrator.
func (e *testEnv) registerToken(admin *http.Client, ttl time.Duration) string {
	e.t.Helper()
	var resp RegisterTokenResponse
	if code := do(e.t, admin, "POST", e.api("/register-tokens"), createRegisterTokenRequest{Note: "test", TTL: api.Duration(ttl)}, &resp); code != http.StatusCreated {
		e.t.Fatalf("create registration token = %d", code)
	}
	return resp.Token
}

// registerHost registers an agent and returns its token and host ID.
func (e *testEnv) registerHost(admin *http.Client, hostname string) (string, int64) {
	e.t.Helper()
	var reg api.RegisterResponse
	if code := e.agentDo("", "POST", "/register", api.RegisterRequest{Token: e.registerToken(admin, 0), Hostname: hostname, AgentVersion: "v1.0.0"}, &reg); code != http.StatusOK {
		e.t.Fatalf("register %s = %d", hostname, code)
	}
	return reg.AgentToken, reg.HostID
}

func sampleHeartbeat() api.Heartbeat {
	return api.Heartbeat{
		AgentVersion: "v1.0.0", Hostname: "backup-esh-01", BackupRoot: "/data/backup", ConfigVersion: 0,
		Metrics: api.HostMetrics{
			Disks:   []api.DiskUsage{{Path: "/data/backup", Role: "backup", Total: 100 << 40, Free: 88 << 40}},
			Bitmaps: []api.BitmapUsage{{Project: "gays", Bytes: 1 << 30}},
		},
		Containers: []api.Container{{ID: "abc", Name: "gays_maindb_slave", Image: "percona:5.6", State: "running", Running: true,
			Ports: []string{"10.23.1.202:3306->3306/tcp"}, Mounts: []api.Mount{{Source: "/data/backup", Destination: "/data/backup"}}}},
		State: api.AgentState{Queue: []api.Job{{Project: "gays", Type: api.JobIncr, Status: api.StatusPending}}},
	}
}

func TestRegisterAndHeartbeat(t *testing.T) {
	e := newTestEnv(t)
	admin := e.admin()

	var tokens []store.RegisterToken
	token := e.registerToken(admin, 0)
	if !strings.HasPrefix(token, "brt_") {
		t.Errorf("token = %q", token)
	}
	do(t, admin, "GET", e.api("/register-tokens"), nil, &tokens)
	if len(tokens) != 1 || !tokens[0].ExpiresAt.Equal(e.clock.Now().Add(defaultRegisterTTL)) {
		t.Errorf("tokens = %+v", tokens)
	}

	req := api.RegisterRequest{Token: token, Hostname: "backup-esh-01", AgentVersion: "v1.0.0"}
	if code := e.agentDo("", "POST", "/register", api.RegisterRequest{Token: "brt_wrong", Hostname: "x"}, nil); code != http.StatusUnauthorized {
		t.Errorf("register with an unknown token = %d", code)
	}
	if code := e.agentDoVersion(2, "", "POST", "/register", req, nil); code != http.StatusConflict {
		t.Errorf("register with API version 2 = %d", code)
	}
	var reg api.RegisterResponse
	if code := e.agentDo("", "POST", "/register", req, &reg); code != http.StatusOK || !strings.HasPrefix(reg.AgentToken, "bat_") || reg.Name != "backup-esh-01" {
		t.Fatalf("register = %d %+v", code, reg)
	}
	if code := e.agentDo("", "POST", "/register", req, nil); code != http.StatusUnauthorized {
		t.Errorf("reusing a registration token = %d", code)
	}
	do(t, admin, "GET", e.api("/register-tokens"), nil, &tokens)
	if len(tokens) != 0 {
		t.Errorf("used token still listed: %+v", tokens)
	}

	var host HostView
	do(t, admin, "GET", e.api("/hosts/"+itoa(reg.HostID)), nil, &host)
	if host.Status != HostNever || host.ConfigVersion != 1 {
		t.Errorf("host before heartbeat = %+v", host)
	}

	if code := e.agentDo("bat_wrong", "POST", "/heartbeat", sampleHeartbeat(), nil); code != http.StatusUnauthorized {
		t.Errorf("heartbeat with a wrong token = %d", code)
	}
	var hbResp api.HeartbeatResponse
	if code := e.agentDo(reg.AgentToken, "POST", "/heartbeat", sampleHeartbeat(), &hbResp); code != http.StatusOK || hbResp.ConfigVersion != 1 {
		t.Fatalf("heartbeat = %d %+v", code, hbResp)
	}
	var hosts []HostView
	do(t, admin, "GET", e.api("/hosts"), nil, &hosts)
	if len(hosts) != 1 {
		t.Fatalf("hosts = %+v", hosts)
	}
	h := hosts[0]
	if h.Status != HostOnline || !h.Compatible || h.AgentVersion != "v1.0.0" || h.IP != "127.0.0.1" || h.BackupRoot != "/data/backup" ||
		len(h.Metrics.Disks) != 1 || h.Metrics.Bitmaps[0].Bytes != 1<<30 || len(h.State.Queue) != 1 || h.APIVersion != api.AgentAPIVersion {
		t.Errorf("host = %+v", h)
	}
	var containers []api.Container
	do(t, admin, "GET", e.api("/hosts/"+itoa(reg.HostID)+"/containers"), nil, &containers)
	if len(containers) != 1 || containers[0].Name != "gays_maindb_slave" || containers[0].Mounts[0].Source != "/data/backup" {
		t.Errorf("containers = %+v", containers)
	}

	// Offline after the heartbeat timeout (PRD H-4); the last state is kept.
	e.clock.Add(61 * time.Second)
	do(t, admin, "GET", e.api("/hosts/"+itoa(reg.HostID)), nil, &host)
	if host.Status != HostOffline || len(host.State.Queue) != 1 {
		t.Errorf("host after timeout = %s %+v", host.Status, host.State)
	}

	// Pausing bumps the configuration version (PRD S-9).
	paused := true
	do(t, admin, "PATCH", e.api("/hosts/"+itoa(reg.HostID)), updateHostRequest{Paused: &paused}, &host)
	if !host.Paused || host.ConfigVersion != 2 {
		t.Errorf("paused host = %+v", host)
	}
	e.agentDo(reg.AgentToken, "POST", "/heartbeat", sampleHeartbeat(), &hbResp)
	if hbResp.ConfigVersion != 2 {
		t.Errorf("config version after pause = %d", hbResp.ConfigVersion)
	}

	// Another API version is rejected but recorded.
	if code := e.agentDoVersion(2, reg.AgentToken, "POST", "/heartbeat", sampleHeartbeat(), nil); code != http.StatusConflict {
		t.Errorf("heartbeat with API version 2 = %d", code)
	}
	do(t, admin, "GET", e.api("/hosts/"+itoa(reg.HostID)), nil, &host)
	if host.Compatible || host.APIVersion != 2 {
		t.Errorf("incompatible host = %+v", host)
	}

	// Revocation (PRD H-5).
	if code := do(t, admin, "POST", e.api("/hosts/"+itoa(reg.HostID)+"/revoke"), nil, &host); code != http.StatusOK || host.Status != HostRevoked {
		t.Errorf("revoke = %d %+v", code, host)
	}
	if code := e.agentDo(reg.AgentToken, "POST", "/heartbeat", sampleHeartbeat(), nil); code != http.StatusUnauthorized {
		t.Errorf("heartbeat after revocation = %d", code)
	}
	if code := do(t, admin, "POST", e.api("/hosts/"+itoa(reg.HostID)+"/revoke"), nil, nil); code != http.StatusConflict {
		t.Errorf("revoke twice = %d", code)
	}

	// Registering again with a new token takes over the same host.
	token2, id2 := e.registerHost(admin, "backup-esh-01")
	if id2 != reg.HostID {
		t.Errorf("re-registration created host %d, want %d", id2, reg.HostID)
	}
	if code := e.agentDo(token2, "POST", "/heartbeat", sampleHeartbeat(), nil); code != http.StatusOK {
		t.Errorf("heartbeat after re-registration = %d", code)
	}
	if code := e.agentDo(reg.AgentToken, "POST", "/heartbeat", sampleHeartbeat(), nil); code != http.StatusUnauthorized {
		t.Errorf("old token after re-registration = %d", code)
	}

	var logs List[store.AuditLog]
	do(t, admin, "GET", e.api("/audit-logs?target_type=host"), nil, &logs)
	if logs.Total != 6 { // invalid token, register, reused token, pause, revoke, register
		t.Errorf("host audit logs = %d", logs.Total)
	}
}

func TestRegisterTokenExpiry(t *testing.T) {
	e := newTestEnv(t)
	admin := e.admin()
	if code := do(t, admin, "POST", e.api("/register-tokens"), createRegisterTokenRequest{TTL: api.Duration(time.Second)}, nil); code != http.StatusBadRequest {
		t.Errorf("1s ttl = %d", code)
	}
	expiring := e.registerToken(admin, time.Hour)
	var deleted RegisterTokenResponse
	do(t, admin, "POST", e.api("/register-tokens"), createRegisterTokenRequest{TTL: api.Duration(time.Hour)}, &deleted)
	if code := do(t, admin, "DELETE", e.api("/register-tokens/"+itoa(deleted.ID)), nil, nil); code != http.StatusNoContent {
		t.Errorf("delete token = %d", code)
	}
	var tokens []store.RegisterToken
	do(t, admin, "GET", e.api("/register-tokens"), nil, &tokens)
	if len(tokens) != 1 {
		t.Fatalf("tokens = %+v", tokens)
	}
	if code := e.agentDo("", "POST", "/register", api.RegisterRequest{Token: deleted.Token, Hostname: "h1"}, nil); code != http.StatusUnauthorized {
		t.Errorf("register with a deleted token = %d", code)
	}
	e.clock.Add(time.Hour)
	if code := e.agentDo("", "POST", "/register", api.RegisterRequest{Token: expiring, Hostname: "h2"}, nil); code != http.StatusUnauthorized {
		t.Errorf("register with an expired token = %d", code)
	}
}
