package store

import (
	"context"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/rodericliu/backup/internal/api"
)

// JobFilter selects jobs (PRD J-2); zero fields match everything.
type JobFilter struct {
	HostID    int64
	ProjectID int64
	Project   string
	Type      api.JobType
	Statuses  []api.JobStatus
	From, To  time.Time // scheduled_at in [From, To)
	Page      Page
}

func (f *JobFilter) where() *where {
	w := &where{}
	if f.HostID != 0 {
		w.add("host_id = ?", f.HostID)
	}
	if f.ProjectID != 0 {
		w.add("project_id = ?", f.ProjectID)
	}
	if f.Project != "" {
		w.add("project = ?", f.Project)
	}
	if f.Type != "" {
		w.add("type = ?", f.Type)
	}
	if len(f.Statuses) > 0 {
		s := make([]string, len(f.Statuses))
		for i, st := range f.Statuses {
			s[i] = string(st)
		}
		w.add("status = ANY(?)", s)
	}
	if !f.From.IsZero() {
		w.add("scheduled_at >= ?", f.From)
	}
	if !f.To.IsZero() {
		w.add("scheduled_at < ?", f.To)
	}
	return w
}

func (s *Store) queryJobs(ctx context.Context, sql string, args ...any) ([]Job, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, dbError(err, "list jobs")
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, dbError(err, "scan job")
		}
		jobs = append(jobs, *j)
	}
	return jobs, dbError(rows.Err(), "list jobs")
}

// Jobs returns matching jobs, newest planned first, and the total count.
// Log tails are omitted; use Job for the details.
func (s *Store) Jobs(ctx context.Context, f JobFilter) ([]Job, int, error) {
	w := f.where()
	var total int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM jobs"+w.String(), w.args...).Scan(&total); err != nil {
		return nil, 0, dbError(err, "count jobs")
	}
	cols := listJobColumns
	q := "SELECT " + cols + " FROM jobs" + w.String() + " ORDER BY scheduled_at DESC, id DESC LIMIT " + w.next(f.Page.limit()) +
		" OFFSET " + w.next(max(f.Page.Offset, 0))
	jobs, err := s.queryJobs(ctx, q, w.args...)
	return jobs, total, err
}

// listJobColumns is jobColumns without the log tail.
const listJobColumns = `id, host_id, coalesce(project_id, 0), project, type, chain_id, base_job_id, trigger, status, scheduled_at,
	started_at, finished_at, raw_size, compressed_size, from_lsn, to_lsn, slave_info, used_bitmap, error_code,
	error_message, '', path, updated_at`

// HostJobs returns a host's jobs planned in [from, to), oldest first.
func (s *Store) HostJobs(ctx context.Context, hostID int64, from, to time.Time) ([]Job, error) {
	return s.queryJobs(ctx, "SELECT "+listJobColumns+` FROM jobs WHERE host_id = $1 AND scheduled_at >= $2 AND scheduled_at < $3
		ORDER BY scheduled_at, id`, hostID, from, to)
}

// ChainJobs returns the jobs of chains, oldest first.
func (s *Store) ChainJobs(ctx context.Context, chainIDs []uuid.UUID) ([]Job, error) {
	return s.queryJobs(ctx, "SELECT "+listJobColumns+" FROM jobs WHERE chain_id = ANY($1) ORDER BY scheduled_at, id", chainIDs)
}

// ProjectChains returns a project's chains, newest first, limited to limit.
func (s *Store) ProjectChains(ctx context.Context, projectID int64, includeDeleted bool, limit int) ([]Chain, error) {
	q := "SELECT " + chainColumns + " FROM chains WHERE project_id = $1"
	if !includeDeleted {
		q += " AND status <> 'deleted'"
	}
	rows, err := s.pool.Query(ctx, q+" ORDER BY started_at DESC LIMIT $2", projectID, limit)
	if err != nil {
		return nil, dbError(err, "list chains")
	}
	defer rows.Close()
	chains := []Chain{}
	for rows.Next() {
		c, err := scanChain(rows)
		if err != nil {
			return nil, dbError(err, "scan chain")
		}
		chains = append(chains, *c)
	}
	return chains, dbError(rows.Err(), "list chains")
}

// JobStats counts jobs by status and sums the size of successful backups.
type JobStats struct {
	Success int   `json:"success"`
	Warning int   `json:"warning"`
	Failed  int   `json:"failed"`
	Skipped int   `json:"skipped"`
	Bytes   int64 `json:"bytes"`
}

// Stats summarizes the jobs that finished in [from, to) (PRD D-2).
func (s *Store) Stats(ctx context.Context, from, to time.Time) (*JobStats, error) {
	var st JobStats
	err := s.pool.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE status = 'success'),
			count(*) FILTER (WHERE status = 'warning'),
			count(*) FILTER (WHERE status IN ('failed', 'canceled')),
			count(*) FILTER (WHERE status = 'skipped'),
			coalesce(sum(compressed_size) FILTER (WHERE status IN ('success', 'warning')), 0)
		FROM jobs WHERE finished_at >= $1 AND finished_at < $2`, from, to).
		Scan(&st.Success, &st.Warning, &st.Failed, &st.Skipped, &st.Bytes)
	return &st, dbError(err, "job stats")
}

// LastSuccess is the newest successful backup of a project (PRD D-3).
type LastSuccess struct {
	ProjectID int64
	Type      api.JobType
	At        time.Time
}

// LastSuccesses returns, for every project and job type, when the newest
// successful backup finished.
func (s *Store) LastSuccesses(ctx context.Context) ([]LastSuccess, error) {
	rows, err := s.pool.Query(ctx, `SELECT project_id, type, max(finished_at) FROM jobs
		WHERE project_id IS NOT NULL AND status IN ('success', 'warning') GROUP BY project_id, type`)
	if err != nil {
		return nil, dbError(err, "last successes")
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (LastSuccess, error) {
		var l LastSuccess
		err := row.Scan(&l.ProjectID, &l.Type, &l.At)
		l.At = l.At.UTC()
		return l, err
	})
	return out, dbError(err, "last successes")
}
