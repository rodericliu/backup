package cache

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/rodericliu/backup/internal/api"
)

// SaveJob inserts or replaces a job and marks it as not yet reported.
func (d *DB) SaveJob(ctx context.Context, j *api.Job) error {
	b, err := json.Marshal(j)
	if err != nil {
		return fmt.Errorf("encode job: %w", err)
	}
	_, err = d.db.ExecContext(ctx, `INSERT INTO jobs (id, project, type, status, chain_id, scheduled_at, finished_at, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET status = excluded.status, chain_id = excluded.chain_id,
			finished_at = excluded.finished_at, data = excluded.data, rev = rev + 1`,
		j.ID.String(), j.Project, j.Type, j.Status, chainKey(j.ChainID), unixMilli(j.ScheduledAt), unixMilli(j.FinishedAt), b)
	if err != nil {
		return fmt.Errorf("save job: %w", err)
	}
	return nil
}

func chainKey(id uuid.UUID) string {
	if id == uuid.Nil() {
		return ""
	}
	return id.String()
}

// Job returns a job by ID, or ErrNotFound.
func (d *DB) Job(ctx context.Context, id uuid.UUID) (*api.Job, error) {
	return d.queryOne(ctx, "SELECT data FROM jobs WHERE id = ?", id.String())
}

// LastSucceeded returns the project's most recently finished successful job of
// the given type, or ErrNotFound. It is used to estimate backup size.
func (d *DB) LastSucceeded(ctx context.Context, project string, typ api.JobType) (*api.Job, error) {
	return d.queryOne(ctx, `SELECT data FROM jobs WHERE project = ? AND type = ? AND status IN (?, ?)
		ORDER BY finished_at DESC LIMIT 1`, project, typ, api.StatusSuccess, api.StatusWarning)
}

// LastInChain returns the chain's most recent successful job, which is the
// base of the next chained incremental, or ErrNotFound.
func (d *DB) LastInChain(ctx context.Context, chainID uuid.UUID) (*api.Job, error) {
	return d.queryOne(ctx, `SELECT data FROM jobs WHERE chain_id = ? AND status IN (?, ?)
		ORDER BY finished_at DESC LIMIT 1`, chainID.String(), api.StatusSuccess, api.StatusWarning)
}

// ChainJobs returns all jobs of a chain in scheduled order.
func (d *DB) ChainJobs(ctx context.Context, chainID uuid.UUID) ([]api.Job, error) {
	return d.queryJobs(ctx, "SELECT data FROM jobs WHERE chain_id = ? ORDER BY scheduled_at, id", chainID.String())
}

// JobsWithStatus returns jobs in any of the given states, oldest first.
func (d *DB) JobsWithStatus(ctx context.Context, statuses ...api.JobStatus) ([]api.Job, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	args := make([]any, len(statuses))
	for i, s := range statuses {
		args[i] = s
	}
	q := "SELECT data FROM jobs WHERE status IN (?" + strings.Repeat(", ?", len(statuses)-1) + ") ORDER BY scheduled_at, id"
	return d.queryJobs(ctx, q, args...)
}

// RecentJobs returns the project's jobs scheduled at or after since, oldest first.
func (d *DB) RecentJobs(ctx context.Context, project string, since time.Time) ([]api.Job, error) {
	return d.queryJobs(ctx, "SELECT data FROM jobs WHERE project = ? AND scheduled_at >= ? ORDER BY scheduled_at, id", project, unixMilli(since))
}

// Unreported is a job whose latest revision has not been sent to the server.
type Unreported struct {
	Job api.Job
	Rev int64
}

// UnreportedJobs returns up to limit terminal jobs not yet reported, in
// scheduled order.
func (d *DB) UnreportedJobs(ctx context.Context, limit int) ([]Unreported, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT data, rev FROM jobs
		WHERE rev > reported_rev AND status NOT IN (?, ?) ORDER BY scheduled_at, id LIMIT ?`,
		api.StatusPending, api.StatusRunning, limit)
	if err != nil {
		return nil, fmt.Errorf("query unreported jobs: %w", err)
	}
	defer rows.Close()
	var out []Unreported
	for rows.Next() {
		var (
			b []byte
			u Unreported
		)
		if err := rows.Scan(&b, &u.Rev); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		if err := json.Unmarshal(b, &u.Job); err != nil {
			return nil, fmt.Errorf("decode job: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// MarkReported records that revision rev of a job was accepted by the server.
// A newer local revision stays unreported.
func (d *DB) MarkReported(ctx context.Context, id uuid.UUID, rev int64) error {
	_, err := d.db.ExecContext(ctx, "UPDATE jobs SET reported_rev = max(reported_rev, ?) WHERE id = ?", rev, id.String())
	if err != nil {
		return fmt.Errorf("mark job reported: %w", err)
	}
	return nil
}

func (d *DB) queryOne(ctx context.Context, query string, args ...any) (*api.Job, error) {
	jobs, err := d.queryJobs(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	if len(jobs) == 0 {
		return nil, ErrNotFound
	}
	return &jobs[0], nil
}

func (d *DB) queryJobs(ctx context.Context, query string, args ...any) ([]api.Job, error) {
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query jobs: %w", err)
	}
	defer rows.Close()
	var jobs []api.Job
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		var j api.Job
		if err := json.Unmarshal(b, &j); err != nil {
			return nil, fmt.Errorf("decode job: %w", err)
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query jobs: %w", err)
	}
	return jobs, nil
}
