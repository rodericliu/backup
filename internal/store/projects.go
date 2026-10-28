package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rodericliu/backup/internal/api"
)

// Delete policies of projects (PRD P-7).
const (
	// DeleteKeep keeps the backup files until the retention period ends.
	DeleteKeep = "keep"
	// DeletePurge deletes the backup files immediately.
	DeletePurge = "purge"
)

// Project is a project with its server-side metadata. Config.MySQL.Password
// is never filled; the encrypted password is in PasswordEnc.
type Project struct {
	api.ProjectConfig
	HostID int64 `json:"host_id"`
	// ConfigVersion is the host configuration version of the last change.
	ConfigVersion int64     `json:"config_version"`
	PasswordSet   bool      `json:"password_set"`
	PasswordEnc   []byte    `json:"-"`
	DeletePolicy  string    `json:"delete_policy,omitzero"`
	DeletedAt     time.Time `json:"deleted_at,omitzero"`
	PurgedAt      time.Time `json:"purged_at,omitzero"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

const projectColumns = `id, host_id, name, container, tier, full_interval, incr_interval, schedule_offset, incr_mode,
	retention_days, lag_threshold, parallel, throttle, timeout, retry_count, retry_interval, priority, enabled,
	mysql_user, mysql_password_enc, mysql_socket, mysql_host, mysql_port, config_version,
	coalesce(delete_policy, ''), deleted_at, purged_at, created_at, updated_at`

func seconds(d api.Duration) int64 { return int64(d.Std() / time.Second) }

func fromSeconds(s int64) api.Duration { return api.Duration(time.Duration(s) * time.Second) }

func scanProject(row pgx.Row) (*Project, error) {
	var (
		p                                    Project
		full, incr, off, lag, timeout, retry int64
		enabled                              bool
		deleted, purged                      *time.Time
	)
	err := row.Scan(&p.ID, &p.HostID, &p.Name, &p.Container, &p.Tier, &full, &incr, &off, &p.IncrMode,
		&p.RetentionDays, &lag, &p.Parallel, &p.Throttle, &timeout, &p.RetryCount, &retry, &p.Priority, &enabled,
		&p.MySQL.User, &p.PasswordEnc, &p.MySQL.Socket, &p.MySQL.Host, &p.MySQL.Port, &p.ConfigVersion,
		&p.DeletePolicy, &deleted, &purged, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	p.FullInterval, p.IncrInterval, p.Offset = fromSeconds(full), fromSeconds(incr), fromSeconds(off)
	p.LagThreshold, p.Timeout, p.RetryInterval = fromSeconds(lag), fromSeconds(timeout), fromSeconds(retry)
	p.Disabled = !enabled
	p.PasswordSet = len(p.PasswordEnc) > 0
	p.DeletedAt, p.PurgedAt = timeOf(deleted), timeOf(purged)
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	return &p, nil
}

// ProjectFilter selects projects.
type ProjectFilter struct {
	HostID         int64
	IncludeDeleted bool
}

// Projects returns matching projects ordered by host, priority and name.
func (s *Store) Projects(ctx context.Context, f ProjectFilter) ([]Project, error) {
	var w where
	if f.HostID != 0 {
		w.add("host_id = ?", f.HostID)
	}
	if !f.IncludeDeleted {
		w.conds = append(w.conds, "deleted_at IS NULL")
	}
	rows, err := s.pool.Query(ctx, "SELECT "+projectColumns+" FROM projects"+w.String()+" ORDER BY host_id, priority, name", w.args...)
	if err != nil {
		return nil, dbError(err, "list projects")
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
	return projects, dbError(rows.Err(), "list projects")
}

// Project returns a project by ID, including deleted projects.
func (s *Store) Project(ctx context.Context, id int64) (*Project, error) {
	p, err := scanProject(s.pool.QueryRow(ctx, "SELECT "+projectColumns+" FROM projects WHERE id = $1", id))
	return p, dbError(err, "project %d", id)
}

// bumpConfig increments a host's configuration version and returns it.
func bumpConfig(ctx context.Context, tx pgx.Tx, hostID int64) (int64, error) {
	var v int64
	err := tx.QueryRow(ctx, "UPDATE hosts SET config_version = config_version + 1 WHERE id = $1 RETURNING config_version", hostID).Scan(&v)
	return v, err
}

// Sealer encrypts a project's MySQL password bound to the project ID.
type Sealer func(projectID int64) []byte

// CreateProject inserts a project, encrypting its password with seal, and
// bumps the host's configuration version. It sets p.ID and the metadata.
func (s *Store) CreateProject(ctx context.Context, p *Project, seal Sealer) error {
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT nextval('projects_id_seq')").Scan(&p.ID); err != nil {
			return err
		}
		p.PasswordEnc = seal(p.ID)
		version, err := bumpConfig(ctx, tx, p.HostID)
		if err != nil {
			return err
		}
		created, err := scanProject(tx.QueryRow(ctx, `INSERT INTO projects (id, host_id, name, container, tier, full_interval, incr_interval,
				schedule_offset, incr_mode, retention_days, lag_threshold, parallel, throttle, timeout, retry_count,
				retry_interval, priority, enabled, mysql_user, mysql_password_enc, mysql_socket, mysql_host, mysql_port,
				config_version, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23,
				$24, now(), now())
			RETURNING `+projectColumns,
			p.ID, p.HostID, p.Name, p.Container, p.Tier, seconds(p.FullInterval), seconds(p.IncrInterval),
			seconds(p.Offset), p.IncrMode, p.RetentionDays, seconds(p.LagThreshold), p.Parallel, p.Throttle,
			seconds(p.Timeout), p.RetryCount, seconds(p.RetryInterval), p.Priority, !p.Disabled, p.MySQL.User,
			p.PasswordEnc, p.MySQL.Socket, p.MySQL.Host, p.MySQL.Port, version))
		if err != nil {
			return err
		}
		*p = *created
		return nil
	})
	if err != nil {
		return dbError(err, "create project %s", p.Name)
	}
	return nil
}

// UpdateProject replaces a live project's configuration and bumps the
// host's configuration version. A nil seal keeps the stored password.
func (s *Store) UpdateProject(ctx context.Context, p *Project, seal Sealer) error {
	err := s.tx(ctx, func(tx pgx.Tx) error {
		version, err := bumpConfig(ctx, tx, p.HostID)
		if err != nil {
			return err
		}
		var enc []byte
		if seal != nil {
			enc = seal(p.ID)
		}
		updated, err := scanProject(tx.QueryRow(ctx, `UPDATE projects SET container = $2, tier = $3, full_interval = $4,
				incr_interval = $5, schedule_offset = $6, incr_mode = $7, retention_days = $8, lag_threshold = $9,
				parallel = $10, throttle = $11, timeout = $12, retry_count = $13, retry_interval = $14, priority = $15,
				enabled = $16, mysql_user = $17, mysql_password_enc = coalesce($18, mysql_password_enc),
				mysql_socket = $19, mysql_host = $20, mysql_port = $21, config_version = $22, updated_at = now()
			WHERE id = $1 AND host_id = $23 AND deleted_at IS NULL
			RETURNING `+projectColumns,
			p.ID, p.Container, p.Tier, seconds(p.FullInterval), seconds(p.IncrInterval), seconds(p.Offset), p.IncrMode,
			p.RetentionDays, seconds(p.LagThreshold), p.Parallel, p.Throttle, seconds(p.Timeout), p.RetryCount,
			seconds(p.RetryInterval), p.Priority, !p.Disabled, p.MySQL.User, enc, p.MySQL.Socket, p.MySQL.Host,
			p.MySQL.Port, version, p.HostID))
		if err != nil {
			return err
		}
		*p = *updated
		return nil
	})
	return dbError(err, "update project %d", p.ID)
}

// SetProjectEnabled enables or disables a live project (PRD P-6).
func (s *Store) SetProjectEnabled(ctx context.Context, id, hostID int64, enabled bool) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		version, err := bumpConfig(ctx, tx, hostID)
		if err != nil {
			return dbError(err, "project %d", id)
		}
		tag, err := tx.Exec(ctx, `UPDATE projects SET enabled = $2, config_version = $3, updated_at = now()
			WHERE id = $1 AND deleted_at IS NULL`, id, enabled, version)
		if err == nil && tag.RowsAffected() == 0 {
			err = pgx.ErrNoRows
		}
		return dbError(err, "project %d", id)
	})
}

// DeleteProject soft-deletes a live project with a delete policy (PRD P-7).
// The project leaves the agent configuration, which stops its scheduling.
func (s *Store) DeleteProject(ctx context.Context, id, hostID int64, policy string, at time.Time) error {
	if policy != DeleteKeep && policy != DeletePurge {
		return fmt.Errorf("delete policy %q: must be %q or %q", policy, DeleteKeep, DeletePurge)
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		version, err := bumpConfig(ctx, tx, hostID)
		if err != nil {
			return dbError(err, "project %d", id)
		}
		tag, err := tx.Exec(ctx, `UPDATE projects SET deleted_at = $2, delete_policy = $3, config_version = $4, enabled = false,
				updated_at = now()
			WHERE id = $1 AND deleted_at IS NULL`, id, at, policy, version)
		if err == nil && tag.RowsAffected() == 0 {
			err = pgx.ErrNoRows
		}
		return dbError(err, "project %d", id)
	})
}

// MarkProjectPurged records that a deleted project's files were removed.
func (s *Store) MarkProjectPurged(ctx context.Context, id int64, at time.Time) error {
	return s.exec1(ctx, "UPDATE projects SET purged_at = $2 WHERE id = $1 AND deleted_at IS NOT NULL AND purged_at IS NULL", id, at)
}

// AvgDuration is the average duration of a project's successful jobs of one
// type.
type AvgDuration struct {
	Project  string
	Type     api.JobType
	Duration time.Duration
	Count    int
}

// AvgDurations returns, for each project and job type on a host, the average
// duration of successful jobs finished after since (PRD P-5, S-10).
func (s *Store) AvgDurations(ctx context.Context, hostID int64, since time.Time) ([]AvgDuration, error) {
	rows, err := s.pool.Query(ctx, `SELECT project, type, avg(extract(epoch FROM finished_at - started_at))::float8, count(*)
		FROM jobs
		WHERE host_id = $1 AND status IN ('success', 'warning') AND finished_at > $2 AND started_at IS NOT NULL
		GROUP BY project, type`, hostID, since)
	if err != nil {
		return nil, dbError(err, "average durations")
	}
	defer rows.Close()
	var out []AvgDuration
	for rows.Next() {
		var (
			a   AvgDuration
			sec float64
		)
		if err := rows.Scan(&a.Project, &a.Type, &sec, &a.Count); err != nil {
			return nil, dbError(err, "scan average duration")
		}
		a.Duration = time.Duration(sec * float64(time.Second))
		out = append(out, a)
	}
	return out, dbError(rows.Err(), "average durations")
}
