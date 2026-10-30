package store

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Roles of assistant messages.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// AssistantSession is a conversation of a user with the AI assistant.
type AssistantSession struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"-"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AssistantToolCall is a tool the assistant called while answering, with
// the result it was given.
type AssistantToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Result    string `json:"result"`
	Error     bool   `json:"error,omitzero"`
}

// AssistantMessage is a question or an answer.
type AssistantMessage struct {
	ID           int64               `json:"id"`
	SessionID    int64               `json:"session_id"`
	Role         string              `json:"role"`
	Content      string              `json:"content"`
	Reasoning    string              `json:"reasoning,omitzero"`
	ToolCalls    []AssistantToolCall `json:"tool_calls"`
	Model        string              `json:"model,omitzero"`
	Error        string              `json:"error,omitzero"`
	InputTokens  int64               `json:"input_tokens,omitzero"`
	OutputTokens int64               `json:"output_tokens,omitzero"`
	CreatedAt    time.Time           `json:"created_at"`
}

const sessionColumns = "id, user_id, title, created_at, updated_at"

func scanSession(row pgx.Row) (*AssistantSession, error) {
	var a AssistantSession
	if err := row.Scan(&a.ID, &a.UserID, &a.Title, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	a.CreatedAt, a.UpdatedAt = a.CreatedAt.UTC(), a.UpdatedAt.UTC()
	return &a, nil
}

// CreateAssistantSession inserts a session and sets its ID.
func (s *Store) CreateAssistantSession(ctx context.Context, a *AssistantSession) error {
	err := s.pool.QueryRow(ctx, `INSERT INTO assistant_sessions (user_id, title, created_at, updated_at)
		VALUES ($1, $2, $3, $3) RETURNING id`, a.UserID, a.Title, a.CreatedAt).Scan(&a.ID)
	a.UpdatedAt = a.CreatedAt
	return dbError(err, "create assistant session")
}

// AssistantSessions returns a user's sessions, most recently used first.
func (s *Store) AssistantSessions(ctx context.Context, userID int64, limit int) ([]AssistantSession, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+sessionColumns+` FROM assistant_sessions WHERE user_id = $1
		ORDER BY updated_at DESC, id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, dbError(err, "list assistant sessions")
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AssistantSession, error) {
		a, err := scanSession(row)
		if err != nil {
			return AssistantSession{}, err
		}
		return *a, nil
	})
	return out, dbError(err, "list assistant sessions")
}

// AssistantSession returns a session of a user; sessions of other users
// are reported as ErrNotFound.
func (s *Store) AssistantSession(ctx context.Context, id, userID int64) (*AssistantSession, error) {
	a, err := scanSession(s.pool.QueryRow(ctx, "SELECT "+sessionColumns+" FROM assistant_sessions WHERE id = $1 AND user_id = $2", id, userID))
	return a, dbError(err, "assistant session %d", id)
}

// RenameAssistantSession sets the title of a user's session.
func (s *Store) RenameAssistantSession(ctx context.Context, id, userID int64, title string) error {
	tag, err := s.pool.Exec(ctx, "UPDATE assistant_sessions SET title = $3 WHERE id = $1 AND user_id = $2", id, userID, title)
	if err == nil && tag.RowsAffected() == 0 {
		err = pgx.ErrNoRows
	}
	return dbError(err, "rename assistant session %d", id)
}

// DeleteAssistantSession deletes a user's session and its messages.
func (s *Store) DeleteAssistantSession(ctx context.Context, id, userID int64) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM assistant_sessions WHERE id = $1 AND user_id = $2", id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = pgx.ErrNoRows
	}
	return dbError(err, "delete assistant session %d", id)
}

// AddAssistantMessage appends a message to a session and marks the session
// used. title becomes the session title if it has none yet.
func (s *Store) AddAssistantMessage(ctx context.Context, m *AssistantMessage, title string) error {
	calls := m.ToolCalls
	if calls == nil {
		calls = []AssistantToolCall{}
	}
	b, err := json.Marshal(calls)
	if err != nil {
		return fmt.Errorf("encode tool calls: %w", err)
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO assistant_messages (session_id, role, content, reasoning, tool_calls, model, error,
				input_tokens, output_tokens, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
			m.SessionID, m.Role, m.Content, m.Reasoning, string(b), m.Model, m.Error, m.InputTokens, m.OutputTokens, m.CreatedAt).Scan(&m.ID)
		if err != nil {
			return dbError(err, "add assistant message")
		}
		_, err = tx.Exec(ctx, `UPDATE assistant_sessions SET updated_at = $2,
			title = CASE WHEN title = '' THEN $3 ELSE title END WHERE id = $1`, m.SessionID, m.CreatedAt, title)
		return dbError(err, "touch assistant session %d", m.SessionID)
	})
}

// AssistantMessages returns the newest limit messages of a session, oldest
// first.
func (s *Store) AssistantMessages(ctx context.Context, sessionID int64, limit int) ([]AssistantMessage, error) {
	rows, err := s.pool.Query(ctx, `SELECT * FROM (
			SELECT id, session_id, role, content, reasoning, tool_calls, model, error, input_tokens, output_tokens, created_at
			FROM assistant_messages WHERE session_id = $1 ORDER BY id DESC LIMIT $2
		) m ORDER BY id`, sessionID, limit)
	if err != nil {
		return nil, dbError(err, "list assistant messages")
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AssistantMessage, error) {
		var (
			m     AssistantMessage
			calls []byte
		)
		err := row.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.Reasoning, &calls, &m.Model, &m.Error,
			&m.InputTokens, &m.OutputTokens, &m.CreatedAt)
		if err != nil {
			return m, err
		}
		m.CreatedAt = m.CreatedAt.UTC()
		if err := json.Unmarshal(calls, &m.ToolCalls); err != nil {
			return m, fmt.Errorf("decode tool calls of message %d: %w", m.ID, err)
		}
		return m, nil
	})
	return out, dbError(err, "list assistant messages")
}
