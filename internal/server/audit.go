package server

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/rodericliu/backup/internal/store"
)

// Audit actions (PRD U-5).
const (
	auditLogin             = "login"
	auditLoginFailed       = "login_failed"
	auditLoginBlocked      = "login_blocked"
	auditLogout            = "logout"
	auditChangePassword    = "change_password"
	auditUserCreate        = "user_create"
	auditUserDisable       = "user_disable"
	auditUserEnable        = "user_enable"
	auditUserResetPassword = "user_reset_password"
)

// audit records an action of user (nil for anonymous requests). Failures to
// write the audit log are logged but do not fail the request.
func (s *Server) audit(r *http.Request, user *store.User, action, targetType, targetID string, detail map[string]any) {
	a := &store.AuditLog{
		Action: action, TargetType: targetType, TargetID: targetID, Detail: detail,
		IP: clientIP(r), CreatedAt: s.now(),
	}
	if user != nil {
		a.UserID, a.Username = user.ID, user.Username
	}
	if err := s.store.AddAudit(r.Context(), a); err != nil {
		s.log.Error("write audit log", "action", action, "err", err)
	}
	s.log.Info("audit", "action", action, "user", a.Username, "target_type", targetType, "target_id", targetID, "ip", a.IP)
}

// List is a page of a list and the total number of matching items.
type List[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}

// pageParams reads page (1-based) and page_size query parameters.
func pageParams(q url.Values) (store.Page, error) {
	size, err := intParam(q, "page_size", 50)
	if err != nil {
		return store.Page{}, err
	}
	page, err := intParam(q, "page", 1)
	if err != nil {
		return store.Page{}, err
	}
	if size < 1 || size > 500 || page < 1 {
		return store.Page{}, badRequest("page must be >= 1 and page_size in [1, 500]")
	}
	return store.Page{Limit: size, Offset: (page - 1) * size}, nil
}

func intParam(q url.Values, name string, def int) (int, error) {
	v := q.Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, badRequest("%s %q: not an integer", name, v)
	}
	return n, nil
}

func timeParam(q url.Values, name string) (time.Time, error) {
	v := q.Get(name)
	if v == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, badRequest("%s %q: want RFC 3339 time", name, v)
	}
	return t, nil
}

func (s *Server) listAuditLogs(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	f := store.AuditFilter{
		Username:   q.Get("username"),
		Action:     q.Get("action"),
		TargetType: q.Get("target_type"),
		TargetID:   q.Get("target_id"),
	}
	var err error
	if f.Page, err = pageParams(q); err != nil {
		return err
	}
	if f.From, err = timeParam(q, "from"); err != nil {
		return err
	}
	if f.To, err = timeParam(q, "to"); err != nil {
		return err
	}
	logs, total, err := s.store.AuditLogs(r.Context(), f)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, List[store.AuditLog]{Items: logs, Total: total})
}
