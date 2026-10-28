package store

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"time"
)

// AuditLog records an administrative action (PRD U-5).
type AuditLog struct {
	ID         int64          `json:"id"`
	UserID     int64          `json:"user_id,omitzero"`
	Username   string         `json:"username"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	Detail     map[string]any `json:"detail"`
	IP         string         `json:"ip"`
	CreatedAt  time.Time      `json:"created_at"`
}

// AddAudit inserts an audit log entry.
func (s *Store) AddAudit(ctx context.Context, a *AuditLog) error {
	detail := a.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	b, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode audit detail: %w", err)
	}
	var userID *int64
	if a.UserID != 0 {
		userID = &a.UserID
	}
	err = s.pool.QueryRow(ctx, `INSERT INTO audit_logs (user_id, username, action, target_type, target_id, detail, ip, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		userID, a.Username, a.Action, a.TargetType, a.TargetID, string(b), a.IP, a.CreatedAt).Scan(&a.ID)
	return dbError(err, "add audit log")
}

// AuditFilter selects audit log entries; zero fields match everything.
type AuditFilter struct {
	Username   string
	Action     string
	TargetType string
	TargetID   string
	From, To   time.Time
	Page       Page
}

// Page selects a page of a list. Limit defaults to 50 and is at most 500.
type Page struct {
	Limit  int
	Offset int
}

func (p Page) limit() int {
	switch {
	case p.Limit <= 0:
		return 50
	case p.Limit > 500:
		return 500
	}
	return p.Limit
}

// where builds a WHERE clause from conditions with $n placeholders.
type where struct {
	conds []string
	args  []any
}

func (w *where) add(cond string, arg any) {
	w.args = append(w.args, arg)
	w.conds = append(w.conds, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(w.args))))
}

func (w *where) String() string {
	if len(w.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.conds, " AND ")
}

// next returns the placeholder of an additional argument.
func (w *where) next(arg any) string {
	w.args = append(w.args, arg)
	return fmt.Sprintf("$%d", len(w.args))
}

// AuditLogs returns matching entries, newest first, and the total count.
func (s *Store) AuditLogs(ctx context.Context, f AuditFilter) ([]AuditLog, int, error) {
	var w where
	if f.Username != "" {
		w.add("username = ?", f.Username)
	}
	if f.Action != "" {
		w.add("action = ?", f.Action)
	}
	if f.TargetType != "" {
		w.add("target_type = ?", f.TargetType)
	}
	if f.TargetID != "" {
		w.add("target_id = ?", f.TargetID)
	}
	if !f.From.IsZero() {
		w.add("created_at >= ?", f.From)
	}
	if !f.To.IsZero() {
		w.add("created_at < ?", f.To)
	}
	var total int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM audit_logs"+w.String(), w.args...).Scan(&total); err != nil {
		return nil, 0, dbError(err, "count audit logs")
	}
	q := `SELECT id, coalesce(user_id, 0), username, action, target_type, target_id, detail, ip, created_at
		FROM audit_logs` + w.String() + " ORDER BY created_at DESC, id DESC LIMIT " + w.next(f.Page.limit()) + " OFFSET " + w.next(max(f.Page.Offset, 0))
	rows, err := s.pool.Query(ctx, q, w.args...)
	if err != nil {
		return nil, 0, dbError(err, "list audit logs")
	}
	defer rows.Close()
	logs := []AuditLog{}
	for rows.Next() {
		var (
			a      AuditLog
			detail []byte
		)
		if err := rows.Scan(&a.ID, &a.UserID, &a.Username, &a.Action, &a.TargetType, &a.TargetID, &detail, &a.IP, &a.CreatedAt); err != nil {
			return nil, 0, dbError(err, "scan audit log")
		}
		if err := json.Unmarshal(detail, &a.Detail); err != nil {
			return nil, 0, fmt.Errorf("decode audit detail: %w", err)
		}
		a.CreatedAt = a.CreatedAt.UTC()
		logs = append(logs, a)
	}
	return logs, total, dbError(rows.Err(), "list audit logs")
}
