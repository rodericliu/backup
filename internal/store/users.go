package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// User is a web user. All users are administrators (PRD U-4).
type User struct {
	ID                 int64     `json:"id"`
	Username           string    `json:"username"`
	PasswordHash       string    `json:"-"`
	MustChangePassword bool      `json:"must_change_password"`
	Disabled           bool      `json:"disabled"`
	LastLoginAt        time.Time `json:"last_login_at,omitzero"`
	CreatedAt          time.Time `json:"created_at"`
}

const userColumns = "id, username, password_hash, must_change_password, disabled, last_login_at, created_at"

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var (
		u    User
		last *time.Time
	)
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.MustChangePassword, &u.Disabled, &last, &u.CreatedAt); err != nil {
		return nil, err
	}
	u.LastLoginAt = timeOf(last)
	u.CreatedAt = u.CreatedAt.UTC()
	return &u, nil
}

// CountUsers returns the number of users.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&n)
	return n, dbError(err, "count users")
}

// CreateUser inserts a user and sets its ID. It returns ErrConflict when the
// username is taken.
func (s *Store) CreateUser(ctx context.Context, u *User) error {
	err := s.pool.QueryRow(ctx, `INSERT INTO users (username, password_hash, must_change_password, disabled)
		VALUES ($1, $2, $3, $4) RETURNING id, created_at`,
		u.Username, u.PasswordHash, u.MustChangePassword, u.Disabled).Scan(&u.ID, &u.CreatedAt)
	return dbError(err, "create user %s", u.Username)
}

// User returns a user by ID.
func (s *Store) User(ctx context.Context, id int64) (*User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, "SELECT "+userColumns+" FROM users WHERE id = $1", id))
	return u, dbError(err, "user %d", id)
}

// UserByName returns a user by username.
func (s *Store) UserByName(ctx context.Context, username string) (*User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, "SELECT "+userColumns+" FROM users WHERE username = $1", username))
	return u, dbError(err, "user %s", username)
}

// Users returns all users ordered by username.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+userColumns+" FROM users ORDER BY username")
	if err != nil {
		return nil, dbError(err, "list users")
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, dbError(err, "scan user")
		}
		users = append(users, *u)
	}
	return users, dbError(rows.Err(), "list users")
}

// SetPassword replaces a user's password hash and must-change flag.
func (s *Store) SetPassword(ctx context.Context, id int64, hash string, mustChange bool) error {
	return s.exec1(ctx, "UPDATE users SET password_hash = $2, must_change_password = $3 WHERE id = $1", id, hash, mustChange)
}

// SetUserDisabled enables or disables a user.
func (s *Store) SetUserDisabled(ctx context.Context, id int64, disabled bool) error {
	return s.exec1(ctx, "UPDATE users SET disabled = $2 WHERE id = $1", id, disabled)
}

// TouchLogin records a successful login.
func (s *Store) TouchLogin(ctx context.Context, id int64, at time.Time) error {
	return s.exec1(ctx, "UPDATE users SET last_login_at = $2 WHERE id = $1", id, at)
}

// exec1 runs a statement that must affect exactly one row.
func (s *Store) exec1(ctx context.Context, sql string, args ...any) error {
	tag, err := s.pool.Exec(ctx, sql, args...)
	if err != nil {
		return dbError(err, "update")
	}
	if tag.RowsAffected() == 0 {
		return dbError(pgx.ErrNoRows, "update")
	}
	return nil
}

// Session is a browser session. ID is the hash of the cookie token.
type Session struct {
	ID        string
	UserID    int64
	ExpiresAt time.Time
	IP        string
	UserAgent string
}

// CreateSession stores a session.
func (s *Store) CreateSession(ctx context.Context, sess *Session) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO sessions (id, user_id, expires_at, ip, user_agent) VALUES ($1, $2, $3, $4, $5)`,
		sess.ID, sess.UserID, sess.ExpiresAt, sess.IP, sess.UserAgent)
	return dbError(err, "create session")
}

// SessionUser returns the enabled user of an unexpired session.
func (s *Store) SessionUser(ctx context.Context, id string, now time.Time) (*User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, `SELECT u.id, u.username, u.password_hash, u.must_change_password, u.disabled,
			u.last_login_at, u.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id = $1 AND s.expires_at > $2 AND NOT u.disabled`, id, now))
	return u, dbError(err, "session")
}

// DeleteSession removes a session.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM sessions WHERE id = $1", id)
	return dbError(err, "delete session")
}

// DeleteUserSessions removes all sessions of a user except keep.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64, keep string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM sessions WHERE user_id = $1 AND id <> $2", userID, keep)
	return dbError(err, "delete sessions")
}

// DeleteExpiredSessions removes sessions that expired before now.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM sessions WHERE expires_at <= $1", now)
	return tag.RowsAffected(), dbError(err, "delete expired sessions")
}
