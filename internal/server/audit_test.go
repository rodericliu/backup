package server

import (
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/rodericliu/backup/internal/store"
)

func TestAuditLogs(t *testing.T) {
	e := newTestEnv(t)
	admin := e.admin()
	do(t, e.browser(), "POST", e.api("/auth/login"), loginRequest{"admin", "wrong-password"}, nil)
	var bob store.User
	do(t, admin, "POST", e.api("/users"), createUserRequest{"bob", "bob-initial-1"}, &bob)
	disabled := true
	do(t, admin, "PATCH", e.api("/users/"+itoa(bob.ID)), updateUserRequest{Disabled: &disabled}, nil)
	do(t, admin, "POST", e.api("/users/"+itoa(bob.ID)+"/password"), resetPasswordRequest{"bob-temporary-2"}, nil)

	var logs List[store.AuditLog]
	if code := do(t, admin, "GET", e.api("/audit-logs"), nil, &logs); code != http.StatusOK {
		t.Fatalf("audit logs = %d", code)
	}
	var actions []string
	for _, a := range logs.Items {
		actions = append(actions, a.Action)
	}
	// Newest first.
	want := []string{auditUserResetPassword, auditUserDisable, auditUserCreate, auditLoginFailed, auditChangePassword, auditLogin}
	if !slices.Equal(actions, want) || logs.Total != len(want) {
		t.Errorf("actions = %v (total %d), want %v", actions, logs.Total, want)
	}
	create := logs.Items[2]
	if create.Username != "admin" || create.TargetType != "user" || create.TargetID != itoa(bob.ID) ||
		create.Detail["username"] != "bob" || create.IP == "" {
		t.Errorf("create entry = %+v", create)
	}
	if failed := logs.Items[3]; failed.UserID != 0 || failed.TargetID != "admin" {
		t.Errorf("failed login entry = %+v", failed)
	}

	q := url.Values{"action": {auditUserDisable}}
	do(t, admin, "GET", e.api("/audit-logs?"+q.Encode()), nil, &logs)
	if logs.Total != 1 || logs.Items[0].Action != auditUserDisable {
		t.Errorf("filtered = %+v", logs)
	}
	do(t, admin, "GET", e.api("/audit-logs?page=2&page_size=4"), nil, &logs)
	if logs.Total != len(want) || len(logs.Items) != 2 || logs.Items[0].Action != auditChangePassword {
		t.Errorf("page 2 = %+v", logs)
	}
	from := url.Values{"from": {e.clock.Now().Add(time.Second).Format(time.RFC3339)}}
	do(t, admin, "GET", e.api("/audit-logs?"+from.Encode()), nil, &logs)
	if logs.Total != 0 {
		t.Errorf("from the future = %d entries", logs.Total)
	}
	for _, bad := range []string{"page=0", "page_size=1000", "from=yesterday"} {
		if code := do(t, admin, "GET", e.api("/audit-logs?"+bad), nil, nil); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", bad, code)
		}
	}
}
