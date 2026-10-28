package store

import (
	"context"
	"encoding/json/jsontext"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rodericliu/backup/internal/api"
)

// Command statuses.
const (
	CommandPending = "pending" // waiting for the agent
	CommandSent    = "sent"    // delivered, waiting for the result
	CommandDone    = "done"
	CommandFailed  = "failed"
	CommandExpired = "expired" // not delivered or answered in time
)

// SecretKey is a payload member holding an encrypted secret. It is removed
// when the command finishes or expires and never returned by Command.
const SecretKey = "secret_enc"

// Command is a command for an agent.
type Command struct {
	ID         int64           `json:"id"`
	HostID     int64           `json:"host_id"`
	Type       api.CommandType `json:"type"`
	Payload    jsontext.Value  `json:"payload"`
	Status     string          `json:"status"`
	CreatedBy  int64           `json:"created_by,omitzero"`
	CreatedAt  time.Time       `json:"created_at"`
	ExpiresAt  time.Time       `json:"expires_at"`
	AckedAt    time.Time       `json:"acked_at,omitzero"`
	FinishedAt time.Time       `json:"finished_at,omitzero"`
	Result     jsontext.Value  `json:"result,omitzero"`
}

// commandColumns selects a command; payloadExpr chooses whether secrets are
// included.
func commandColumns(payloadExpr string) string {
	return "id, host_id, type, " + payloadExpr + ", status, coalesce(created_by, 0), created_at, expires_at, acked_at, finished_at, result"
}

func scanCommand(row pgx.Row) (*Command, error) {
	var (
		c               Command
		acked, finished *time.Time
		payload, result []byte
	)
	if err := row.Scan(&c.ID, &c.HostID, &c.Type, &payload, &c.Status, &c.CreatedBy, &c.CreatedAt, &c.ExpiresAt,
		&acked, &finished, &result); err != nil {
		return nil, err
	}
	c.Payload, c.Result = payload, result
	c.CreatedAt, c.ExpiresAt, c.AckedAt, c.FinishedAt = c.CreatedAt.UTC(), c.ExpiresAt.UTC(), timeOf(acked), timeOf(finished)
	return &c, nil
}

// CreateCommand queues a command and sets its ID.
func (s *Store) CreateCommand(ctx context.Context, c *Command) error {
	var by *int64
	if c.CreatedBy != 0 {
		by = &c.CreatedBy
	}
	payload := c.Payload
	if len(payload) == 0 {
		payload = jsontext.Value("{}")
	}
	c.Status = CommandPending
	err := s.pool.QueryRow(ctx, `INSERT INTO agent_commands (host_id, type, payload, status, created_by, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		c.HostID, c.Type, string(payload), c.Status, by, c.CreatedAt, c.ExpiresAt).Scan(&c.ID)
	return dbError(err, "create command")
}

// Command returns a command without its secrets.
func (s *Store) Command(ctx context.Context, id int64) (*Command, error) {
	c, err := scanCommand(s.pool.QueryRow(ctx, "SELECT "+commandColumns("payload - '"+SecretKey+"'")+" FROM agent_commands WHERE id = $1", id))
	return c, dbError(err, "command %d", id)
}

// TakeCommands marks a host's pending, unexpired commands as sent and returns
// them, with their secrets, in creation order.
func (s *Store) TakeCommands(ctx context.Context, hostID int64, now time.Time) ([]Command, error) {
	rows, err := s.pool.Query(ctx, `UPDATE agent_commands SET status = 'sent', acked_at = $2
		WHERE host_id = $1 AND status = 'pending' AND expires_at > $2
		RETURNING `+commandColumns("payload"), hostID, now)
	if err != nil {
		return nil, dbError(err, "take commands")
	}
	defer rows.Close()
	cmds := []Command{}
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, dbError(err, "scan command")
		}
		cmds = append(cmds, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, dbError(err, "take commands")
	}
	slices.SortFunc(cmds, func(a, b Command) int { return int(a.ID - b.ID) })
	return cmds, nil
}

// FinishCommand records the result of a sent command of a host and removes
// its secrets. It returns the finished command, or ErrNotFound when the
// command is unknown, belongs to another host or is not waiting for a result.
func (s *Store) FinishCommand(ctx context.Context, hostID, id int64, ok bool, result jsontext.Value, now time.Time) (*Command, error) {
	status := CommandFailed
	if ok {
		status = CommandDone
	}
	c, err := scanCommand(s.pool.QueryRow(ctx, `UPDATE agent_commands
		SET status = $3, result = $4, finished_at = $5, payload = payload - '`+SecretKey+`'
		WHERE id = $1 AND host_id = $2 AND status = 'sent'
		RETURNING `+commandColumns("payload"), id, hostID, status, string(result), now))
	return c, dbError(err, "finish command %d", id)
}

// ExpireCommands expires commands that were not answered before their
// deadline and removes their secrets.
func (s *Store) ExpireCommands(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE agent_commands SET status = 'expired', finished_at = $1, payload = payload - '`+SecretKey+`'
		WHERE status IN ('pending', 'sent') AND expires_at <= $1`, now)
	return tag.RowsAffected(), dbError(err, "expire commands")
}

// ProjectsToPurge returns deleted projects whose files should be deleted
// now: those deleted with the purge policy, and those deleted with the keep
// policy whose retention has ended. Projects of revoked hosts and projects
// with an active purge command are skipped.
func (s *Store) ProjectsToPurge(ctx context.Context, now time.Time) ([]Project, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+projectColumns+` FROM projects p
		WHERE deleted_at IS NOT NULL AND purged_at IS NULL
			AND EXISTS (SELECT 1 FROM hosts h WHERE h.id = p.host_id AND h.revoked_at IS NULL)
			AND (delete_policy = 'purge' OR deleted_at + make_interval(days => retention_days) <= $1)
			AND NOT EXISTS (SELECT 1 FROM agent_commands c WHERE c.host_id = p.host_id AND c.type = 'purge'
				AND c.status IN ('pending', 'sent') AND (c.payload->>'project_id')::bigint = p.id)`, now)
	if err != nil {
		return nil, dbError(err, "projects to purge")
	}
	defer rows.Close()
	projects := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, dbError(err, "scan project")
		}
		projects = append(projects, *p)
	}
	return projects, dbError(rows.Err(), "projects to purge")
}
