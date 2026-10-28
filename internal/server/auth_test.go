package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rodericliu/backup/internal/store"
)

// login signs in and changes the initial password if required. It returns a
// browser client holding the session.
func (e *testEnv) login(username, password string) *http.Client {
	e.t.Helper()
	c := e.browser()
	var u store.User
	if code := do(e.t, c, "POST", e.api("/auth/login"), loginRequest{username, password}, &u); code != http.StatusOK {
		e.t.Fatalf("login %s = %d", username, code)
	}
	return c
}

// admin bootstraps the administrator, changes its initial password and
// returns a signed-in client.
func (e *testEnv) admin() *http.Client {
	e.t.Helper()
	initial, err := e.srv.bootstrap(e.t.Context())
	if err != nil || initial == "" {
		e.t.Fatalf("bootstrap: %q %v", initial, err)
	}
	c := e.login("admin", initial)
	if code := do(e.t, c, "POST", e.api("/auth/password"), changePasswordRequest{initial, "admin-password-1"}, nil); code != http.StatusNoContent {
		e.t.Fatalf("change initial password = %d", code)
	}
	return c
}

func TestBootstrapAndFirstLogin(t *testing.T) {
	e := newTestEnv(t)
	initial, err := e.srv.bootstrap(t.Context())
	if err != nil || len(initial) < 10 {
		t.Fatalf("bootstrap = %q, %v", initial, err)
	}
	if again, err := e.srv.bootstrap(t.Context()); err != nil || again != "" {
		t.Errorf("second bootstrap = %q, %v", again, err)
	}

	c := e.browser()
	var errBody errorBody
	if code := do(t, c, "GET", e.api("/auth/me"), nil, &errBody); code != http.StatusUnauthorized {
		t.Errorf("me without session = %d", code)
	}
	if code := do(t, c, "POST", e.api("/auth/login"), loginRequest{"admin", "wrong-password"}, &errBody); code != http.StatusUnauthorized ||
		errBody.Error.Code != "INVALID_CREDENTIALS" {
		t.Errorf("wrong password = %d %+v", code, errBody)
	}
	var u store.User
	if code := do(t, c, "POST", e.api("/auth/login"), loginRequest{"admin", initial}, &u); code != http.StatusOK || !u.MustChangePassword {
		t.Fatalf("login = %d %+v", code, u)
	}

	// The cookie is HttpOnly, Secure and SameSite=Strict.
	resp, err := c.Post(e.api("/auth/login"), "application/json", strings.NewReader(`{"username":"admin","password":"`+initial+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	ck := resp.Cookies()
	if len(ck) != 1 || !ck[0].HttpOnly || !ck[0].Secure || ck[0].SameSite != http.SameSiteStrictMode || !strings.HasPrefix(ck[0].Value, "bss_") {
		t.Errorf("cookie = %+v", ck)
	}

	if code := do(t, c, "GET", e.api("/users"), nil, &errBody); code != http.StatusForbidden || errBody.Error.Code != "PASSWORD_CHANGE_REQUIRED" {
		t.Errorf("users before password change = %d %+v", code, errBody)
	}
	if code := do(t, c, "POST", e.api("/auth/password"), changePasswordRequest{initial, "short"}, nil); code != http.StatusBadRequest {
		t.Errorf("short password = %d", code)
	}
	if code := do(t, c, "POST", e.api("/auth/password"), changePasswordRequest{"wrong", "long-enough-password"}, nil); code != http.StatusBadRequest {
		t.Errorf("wrong old password = %d", code)
	}
	if code := do(t, c, "POST", e.api("/auth/password"), changePasswordRequest{initial, "long-enough-password"}, nil); code != http.StatusNoContent {
		t.Fatalf("change password = %d", code)
	}
	var users []store.User
	if code := do(t, c, "GET", e.api("/users"), nil, &users); code != http.StatusOK || len(users) != 1 || users[0].MustChangePassword {
		t.Errorf("users = %d %+v", code, users)
	}
	if code := do(t, c, "POST", e.api("/auth/logout"), nil, nil); code != http.StatusNoContent {
		t.Errorf("logout = %d", code)
	}
	if code := do(t, c, "GET", e.api("/auth/me"), nil, nil); code != http.StatusUnauthorized {
		t.Errorf("me after logout = %d", code)
	}
	if code := do(t, e.browser(), "POST", e.api("/auth/login"), loginRequest{"admin", initial}, nil); code != http.StatusUnauthorized {
		t.Errorf("login with the old password = %d", code)
	}
}

func TestSessionExpiry(t *testing.T) {
	e := newTestEnv(t)
	c := e.admin()
	e.clock.Add(11 * time.Hour)
	if code := do(t, c, "GET", e.api("/auth/me"), nil, nil); code != http.StatusOK {
		t.Errorf("me before expiry = %d", code)
	}
	e.clock.Add(2 * time.Hour)
	if code := do(t, c, "GET", e.api("/auth/me"), nil, nil); code != http.StatusUnauthorized {
		t.Errorf("me after expiry = %d", code)
	}
	if err := e.srv.deleteExpiredSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newTestEnv(t)
	e.admin()
	c := e.browser()
	for range maxLoginFailures {
		if code := do(t, c, "POST", e.api("/auth/login"), loginRequest{"admin", "wrong-password"}, nil); code != http.StatusUnauthorized {
			t.Fatalf("failed login = %d", code)
		}
	}
	var errBody errorBody
	if code := do(t, c, "POST", e.api("/auth/login"), loginRequest{"admin", "admin-password-1"}, &errBody); code != http.StatusTooManyRequests {
		t.Errorf("login after %d failures = %d %+v", maxLoginFailures, code, errBody)
	}
	e.clock.Add(loginWindow)
	if code := do(t, c, "POST", e.api("/auth/login"), loginRequest{"admin", "admin-password-1"}, nil); code != http.StatusOK {
		t.Errorf("login after the window = %d", code)
	}
}

func TestCSRF(t *testing.T) {
	e := newTestEnv(t)
	c := e.admin()
	req, _ := http.NewRequest("POST", e.api("/users"), strings.NewReader(`{"username":"x","password":"long-enough-password"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin POST = %d", resp.StatusCode)
	}
	resp, err = c.Post(e.api("/users"), "text/plain", strings.NewReader(`{"username":"x","password":"long-enough-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain POST = %d", resp.StatusCode)
	}
}

func TestUserManagement(t *testing.T) {
	e := newTestEnv(t)
	admin := e.admin()
	var me store.User
	do(t, admin, "GET", e.api("/auth/me"), nil, &me)

	var bob store.User
	if code := do(t, admin, "POST", e.api("/users"), createUserRequest{"bob", "bob-initial-1"}, &bob); code != http.StatusCreated || !bob.MustChangePassword {
		t.Fatalf("create bob = %d %+v", code, bob)
	}
	if code := do(t, admin, "POST", e.api("/users"), createUserRequest{"bob", "bob-initial-1"}, nil); code != http.StatusConflict {
		t.Errorf("duplicate user = %d", code)
	}
	if code := do(t, admin, "POST", e.api("/users"), createUserRequest{"bad name", "bob-initial-1"}, nil); code != http.StatusBadRequest {
		t.Errorf("invalid username = %d", code)
	}

	bobClient := e.login("bob", "bob-initial-1")
	if code := do(t, bobClient, "POST", e.api("/auth/password"), changePasswordRequest{"bob-initial-1", "bob-password-2"}, nil); code != http.StatusNoContent {
		t.Fatalf("bob change password = %d", code)
	}

	disabled := true
	if code := do(t, admin, "PATCH", e.api("/users/"+itoa(me.ID)), updateUserRequest{Disabled: &disabled}, nil); code != http.StatusBadRequest {
		t.Errorf("disable self = %d", code)
	}
	var got store.User
	if code := do(t, admin, "PATCH", e.api("/users/"+itoa(bob.ID)), updateUserRequest{Disabled: &disabled}, &got); code != http.StatusOK || !got.Disabled {
		t.Errorf("disable bob = %d %+v", code, got)
	}
	if code := do(t, bobClient, "GET", e.api("/auth/me"), nil, nil); code != http.StatusUnauthorized {
		t.Errorf("disabled user session = %d", code)
	}
	if code := do(t, e.browser(), "POST", e.api("/auth/login"), loginRequest{"bob", "bob-password-2"}, nil); code != http.StatusUnauthorized {
		t.Errorf("disabled user login = %d", code)
	}
	enabled := false
	do(t, admin, "PATCH", e.api("/users/"+itoa(bob.ID)), updateUserRequest{Disabled: &enabled}, nil)

	if code := do(t, admin, "POST", e.api("/users/"+itoa(bob.ID)+"/password"), resetPasswordRequest{"bob-temporary-3"}, nil); code != http.StatusNoContent {
		t.Fatalf("reset password = %d", code)
	}
	var u store.User
	if code := do(t, e.browser(), "POST", e.api("/auth/login"), loginRequest{"bob", "bob-temporary-3"}, &u); code != http.StatusOK || !u.MustChangePassword {
		t.Errorf("login after reset = %d %+v", code, u)
	}
	if code := do(t, admin, "POST", e.api("/users/999/password"), resetPasswordRequest{"bob-temporary-3"}, nil); code != http.StatusNotFound {
		t.Errorf("reset unknown user = %d", code)
	}
}
