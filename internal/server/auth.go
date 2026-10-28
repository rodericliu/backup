package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rodericliu/backup/internal/crypto"
	"github.com/rodericliu/backup/internal/store"
)

// SessionCookie is the name of the browser session cookie (PRD U-1).
const SessionCookie = "backup_session"

// Login rate limit (PRD U-3).
const (
	maxLoginFailures = 5
	loginWindow      = 15 * time.Minute
)

// maxPasswordLength is bcrypt's input limit.
const maxPasswordLength = 72

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,63}$`)

type ctxKey int

const (
	userKey ctxKey = iota
	sessionKey
)

// currentUser returns the authenticated user of a request.
func currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey).(*store.User)
	return u
}

// Bootstrap creates the bootstrap administrator when there are no users and
// logs its random initial password, which must be changed at first login
// (PRD U-2).
func (s *Server) Bootstrap(ctx context.Context) error {
	_, err := s.bootstrap(ctx)
	return err
}

// bootstrap returns the initial password, or "" when users already exist.
func (s *Server) bootstrap(ctx context.Context) (string, error) {
	n, err := s.store.CountUsers(ctx)
	if err != nil || n > 0 {
		return "", err
	}
	password := crypto.RandomPassword()
	hash, err := crypto.HashPassword(password)
	if err != nil {
		return "", err
	}
	u := &store.User{Username: s.cfg.Auth.BootstrapAdmin, PasswordHash: hash, MustChangePassword: true}
	if err := s.store.CreateUser(ctx, u); err != nil {
		return "", err
	}
	s.log.Warn(fmt.Sprintf("created administrator %q with initial password %s; it must be changed at first login", u.Username, password))
	return password, nil
}

// loginLimiter counts failed logins per username and per client IP.
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

func (l *loginLimiter) keys(username, ip string) []string {
	return []string{"u:" + strings.ToLower(username), "ip:" + ip}
}

// blocked reports whether too many recent failures were recorded.
func (l *loginLimiter) blocked(now time.Time, username, ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range l.keys(username, ip) {
		if len(l.recent(k, now)) >= maxLoginFailures {
			return true
		}
	}
	return false
}

func (l *loginLimiter) fail(now time.Time, username, ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failures == nil {
		l.failures = make(map[string][]time.Time)
	}
	for _, k := range l.keys(username, ip) {
		l.failures[k] = append(l.recent(k, now), now)
	}
}

func (l *loginLimiter) reset(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, "u:"+strings.ToLower(username))
}

// recent drops failures outside the window; l.mu must be held.
func (l *loginLimiter) recent(key string, now time.Time) []time.Time {
	ts := l.failures[key]
	i := 0
	for i < len(ts) && now.Sub(ts[i]) >= loginWindow {
		i++
	}
	if i == len(ts) {
		delete(l.failures, key)
		return nil
	}
	ts = ts[i:]
	l.failures[key] = ts
	return ts
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) error {
	var req loginRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	now, ip := s.now(), clientIP(r)
	if s.limiter.blocked(now, req.Username, ip) {
		return newError(http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS", "too many failed logins; try again in %s", loginWindow)
	}
	u, err := s.store.UserByName(r.Context(), req.Username)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Spend the same time as a real check so that usernames cannot be probed.
		crypto.CheckPassword(dummyHash(), req.Password)
	case err != nil:
		return err
	}
	if u == nil || u.Disabled || !crypto.CheckPassword(u.PasswordHash, req.Password) {
		s.limiter.fail(now, req.Username, ip)
		return newError(http.StatusUnauthorized, "INVALID_CREDENTIALS", "invalid username or password")
	}
	s.limiter.reset(req.Username)

	token := crypto.NewToken(crypto.PrefixSession)
	sess := &store.Session{
		ID: crypto.HashToken(token), UserID: u.ID, ExpiresAt: now.Add(s.cfg.Auth.SessionTTL.Std()),
		IP: ip, UserAgent: truncate(r.UserAgent(), 256),
	}
	if err := s.store.CreateSession(r.Context(), sess); err != nil {
		return err
	}
	if err := s.store.TouchLogin(r.Context(), u.ID, now); err != nil {
		return err
	}
	u.LastLoginAt = now
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/", Expires: sess.ExpiresAt,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	return writeJSON(w, http.StatusOK, u)
}

// dummyHash is a bcrypt hash compared against for unknown users.
var dummyHash = sync.OnceValue(func() string {
	h, _ := crypto.HashPassword("unknown user placeholder")
	return h
})

func (s *Server) logout(w http.ResponseWriter, r *http.Request) error {
	if id, ok := r.Context().Value(sessionKey).(string); ok {
		if err := s.store.DeleteSession(r.Context(), id); err != nil {
			return err
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) error {
	return writeJSON(w, http.StatusOK, currentUser(r))
}

type changePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) error {
	var req changePasswordRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	u := currentUser(r)
	if !crypto.CheckPassword(u.PasswordHash, req.OldPassword) {
		return newError(http.StatusBadRequest, "INVALID_CREDENTIALS", "the current password is wrong")
	}
	if req.NewPassword == req.OldPassword {
		return badRequest("the new password must differ from the current password")
	}
	if err := s.setPassword(r.Context(), u.ID, req.NewPassword, false); err != nil {
		return err
	}
	sess, _ := r.Context().Value(sessionKey).(string)
	if err := s.store.DeleteUserSessions(r.Context(), u.ID, sess); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func validatePassword(p string) error {
	switch {
	case len(p) < crypto.MinPasswordLength:
		return badRequest("password must be at least %d characters", crypto.MinPasswordLength)
	case len(p) > maxPasswordLength:
		return badRequest("password must be at most %d bytes", maxPasswordLength)
	}
	return nil
}

func (s *Server) setPassword(ctx context.Context, userID int64, password string, mustChange bool) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	hash, err := crypto.HashPassword(password)
	if err != nil {
		return err
	}
	return s.store.SetPassword(ctx, userID, hash, mustChange)
}

// requireUser authenticates the session cookie. Users that must change
// their password may only use the endpoints in allowed (PRD U-2).
func (s *Server) requireUser(next http.Handler) http.Handler {
	allowed := map[string]bool{
		WebAPIPrefix + "/auth/me":       true,
		WebAPIPrefix + "/auth/password": true,
		WebAPIPrefix + "/auth/logout":   true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(SessionCookie)
		if err != nil || c.Value == "" {
			s.writeError(w, r, errUnauthorized)
			return
		}
		id := crypto.HashToken(c.Value)
		u, err := s.store.SessionUser(r.Context(), id, s.now())
		if errors.Is(err, store.ErrNotFound) {
			s.writeError(w, r, errUnauthorized)
			return
		}
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if u.MustChangePassword && !allowed[r.URL.Path] {
			s.writeError(w, r, newError(http.StatusForbidden, "PASSWORD_CHANGE_REQUIRED", "change the initial password first"))
			return
		}
		ctx := context.WithValue(r.Context(), userKey, u)
		ctx = context.WithValue(ctx, sessionKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// sameOrigin rejects state-changing requests that are not JSON or that come
// from another origin. Together with SameSite=Strict cookies this prevents
// cross-site request forgery.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host {
				writeJSON(w, http.StatusForbidden, errorBody{Error{Code: "FORBIDDEN", Message: "cross-origin request"}})
				return
			}
		}
		if r.ContentLength != 0 && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeJSON(w, http.StatusUnsupportedMediaType, errorBody{Error{Code: "UNSUPPORTED_MEDIA_TYPE", Message: "requests must be application/json"}})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// deleteExpiredSessions is a background task.
func (s *Server) deleteExpiredSessions(ctx context.Context) error {
	_, err := s.store.DeleteExpiredSessions(ctx, s.now())
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
