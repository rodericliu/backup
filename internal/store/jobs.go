package store

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/rodericliu/backup/internal/api"
)

// ErrJobHost is returned when a job is reported by a host other than the one
// that created it.
var ErrJobHost = errors.New("job belongs to another host")

func nullUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil() {
		return nil
	}
	return &id
}

// UpsertJob records a job reported by a host (PRD 5.3.2). Replays are
// idempotent: a job in a final state is only replaced by another final
// state, never by a stale pending or running snapshot. The job's chain is
// created or updated along with it.
func (s *Store) UpsertJob(ctx context.Context, hostID int64, j *api.Job) error {
	var slave []byte
	if j.SlaveInfo != nil {
		var err error
		if slave, err = json.Marshal(j.SlaveInfo); err != nil {
			return fmt.Errorf("encode slave info: %w", err)
		}
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var projectID *int64
		err := tx.QueryRow(ctx, `SELECT id FROM projects WHERE host_id = $1 AND name = $2
			ORDER BY deleted_at DESC NULLS FIRST, id DESC LIMIT 1`, hostID, j.Project).Scan(&projectID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var owner int64
		err = tx.QueryRow(ctx, `INSERT INTO jobs (id, host_id, project_id, project, type, chain_id, base_job_id, trigger, status,
				scheduled_at, started_at, finished_at, raw_size, compressed_size, from_lsn, to_lsn, slave_info, used_bitmap,
				error_code, error_message, log_tail, path, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, now())
			ON CONFLICT (id) DO UPDATE SET type = excluded.type, chain_id = excluded.chain_id, base_job_id = excluded.base_job_id,
				status = excluded.status, started_at = excluded.started_at, finished_at = excluded.finished_at,
				raw_size = excluded.raw_size, compressed_size = excluded.compressed_size, from_lsn = excluded.from_lsn,
				to_lsn = excluded.to_lsn, slave_info = excluded.slave_info, used_bitmap = excluded.used_bitmap,
				error_code = excluded.error_code, error_message = excluded.error_message, log_tail = excluded.log_tail,
				path = excluded.path, updated_at = now()
			WHERE jobs.host_id = excluded.host_id
				AND (jobs.status IN ('pending', 'running') OR excluded.status NOT IN ('pending', 'running'))
			RETURNING host_id`,
			j.ID, hostID, projectID, j.Project, j.Type, nullUUID(j.ChainID), nullUUID(j.BaseJobID), j.Trigger, j.Status,
			j.ScheduledAt, nullTime(j.StartedAt), nullTime(j.FinishedAt), j.RawSize, j.CompressedSize, j.FromLSN, j.ToLSN,
			slave, j.UsedBitmap, j.ErrorCode, j.ErrorMessage, j.LogTail, j.Path).Scan(&owner)
		if errors.Is(err, pgx.ErrNoRows) {
			// Either another host owns the job or a stale snapshot was ignored.
			if err := tx.QueryRow(ctx, "SELECT host_id FROM jobs WHERE id = $1", j.ID).Scan(&owner); err != nil {
				return err
			}
			if owner != hostID {
				return ErrJobHost
			}
			return nil
		}
		if err != nil {
			return err
		}
		if j.ChainID == uuid.Nil() {
			return nil
		}
		return upsertChain(ctx, tx, hostID, projectID, j)
	})
	if errors.Is(err, ErrJobHost) {
		return err
	}
	return dbError(err, "record job %s", j.ID)
}

// Chain statuses on the server mirror the agent's.
const (
	ChainRunning = "running"
	ChainActive  = "active"
	ChainFailed  = "failed"
	ChainDeleted = "deleted"
)

func upsertChain(ctx context.Context, tx pgx.Tx, hostID int64, projectID *int64, j *api.Job) error {
	started := j.StartedAt
	if started.IsZero() {
		started = j.ScheduledAt
	}
	var (
		fullJob *uuid.UUID
		status  *string
		last    *time.Time
	)
	if j.Type == api.JobFull {
		fullJob = &j.ID
		st := ChainRunning
		switch {
		case j.Status.Succeeded():
			st = ChainActive
		case j.Status.Terminal():
			st = ChainFailed
		}
		status = &st
	}
	if j.Status.Succeeded() && !j.FinishedAt.IsZero() {
		last = &j.FinishedAt
	}
	initial := ChainRunning
	if status != nil {
		initial = *status
	}
	_, err := tx.Exec(ctx, `INSERT INTO chains (id, host_id, project_id, project, full_job_id, started_at, last_job_at, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (id) DO UPDATE SET
			full_job_id = coalesce($5, chains.full_job_id),
			started_at = least(chains.started_at, excluded.started_at),
			last_job_at = greatest(chains.last_job_at, excluded.last_job_at),
			status = CASE WHEN chains.status = 'deleted' OR $9::text IS NULL THEN chains.status ELSE $9 END
		WHERE chains.host_id = excluded.host_id`,
		j.ChainID, hostID, projectID, j.Project, fullJob, started, last, initial, status)
	return err
}

// AddJobEvents stores a job's events, ignoring replays, and applies cleanup
// events to their chains (PRD R-3).
func (s *Store) AddJobEvents(ctx context.Context, hostID int64, jobID uuid.UUID, events []api.JobEvent) error {
	if len(events) == 0 {
		return nil
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for _, e := range events {
			payload := e.Payload
			if len(payload) == 0 {
				payload = []byte("{}")
			}
			batch.Queue(`INSERT INTO job_events (job_id, host_id, seq, ts, type, payload) VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (host_id, seq) DO NOTHING`, jobID, hostID, e.Seq, e.At, e.Type, string(payload))
			if e.Type == api.EventCleanup {
				var c api.CleanupEvent
				if err := json.Unmarshal(e.Payload, &c); err != nil {
					return fmt.Errorf("decode cleanup event %d: %w", e.Seq, err)
				}
				chainID, err := uuid.Parse(c.ChainID)
				if err != nil {
					return fmt.Errorf("cleanup event %d: chain id %q: %w", e.Seq, c.ChainID, err)
				}
				batch.Queue(`UPDATE chains SET status = 'deleted', deleted_at = coalesce(deleted_at, $3)
					WHERE id = $1 AND host_id = $2`, chainID, hostID, e.At)
			}
		}
		return tx.SendBatch(ctx, batch).Close()
	})
	return dbError(err, "add events of job %s", jobID)
}

// JobHostID returns the host that reported a job.
func (s *Store) JobHostID(ctx context.Context, id uuid.UUID) (int64, error) {
	var hostID int64
	err := s.pool.QueryRow(ctx, "SELECT host_id FROM jobs WHERE id = $1", id).Scan(&hostID)
	return hostID, dbError(err, "job %s", id)
}

// SaveJobLog stores the complete log of a job (PRD J-4).
func (s *Store) SaveJobLog(ctx context.Context, id uuid.UUID, content []byte, at time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO job_logs (job_id, content, uploaded_at) VALUES ($1, $2, $3)
		ON CONFLICT (job_id) DO UPDATE SET content = excluded.content, uploaded_at = excluded.uploaded_at`, id, content, at)
	return dbError(err, "save log of job %s", id)
}

// JobLog returns the uploaded log of a job and when it was uploaded.
func (s *Store) JobLog(ctx context.Context, id uuid.UUID) ([]byte, time.Time, error) {
	var (
		content []byte
		at      time.Time
	)
	err := s.pool.QueryRow(ctx, "SELECT content, uploaded_at FROM job_logs WHERE job_id = $1", id).Scan(&content, &at)
	return content, at.UTC(), dbError(err, "log of job %s", id)
}

// Job is a job with the host that ran it.
type Job struct {
	api.Job
	HostID    int64     `json:"host_id"`
	UpdatedAt time.Time `json:"updated_at"`
}

const jobColumns = `id, host_id, coalesce(project_id, 0), project, type, chain_id, base_job_id, trigger, status, scheduled_at,
	started_at, finished_at, raw_size, compressed_size, from_lsn, to_lsn, slave_info, used_bitmap, error_code,
	error_message, log_tail, path, updated_at`

func scanJob(row pgx.Row) (*Job, error) {
	var (
		j                 Job
		chain, base       *uuid.UUID
		started, finished *time.Time
		slave             []byte
	)
	err := row.Scan(&j.ID, &j.HostID, &j.ProjectID, &j.Project, &j.Type, &chain, &base, &j.Trigger, &j.Status, &j.ScheduledAt,
		&started, &finished, &j.RawSize, &j.CompressedSize, &j.FromLSN, &j.ToLSN, &slave, &j.UsedBitmap, &j.ErrorCode,
		&j.ErrorMessage, &j.LogTail, &j.Path, &j.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if chain != nil {
		j.ChainID = *chain
	}
	if base != nil {
		j.BaseJobID = *base
	}
	j.ScheduledAt, j.StartedAt, j.FinishedAt, j.UpdatedAt = j.ScheduledAt.UTC(), timeOf(started), timeOf(finished), j.UpdatedAt.UTC()
	if len(slave) > 0 {
		j.SlaveInfo = new(api.SlaveInfo)
		if err := json.Unmarshal(slave, j.SlaveInfo); err != nil {
			return nil, fmt.Errorf("decode slave info of job %s: %w", j.ID, err)
		}
	}
	return &j, nil
}

// Job returns a job by ID.
func (s *Store) Job(ctx context.Context, id uuid.UUID) (*Job, error) {
	j, err := scanJob(s.pool.QueryRow(ctx, "SELECT "+jobColumns+" FROM jobs WHERE id = $1", id))
	return j, dbError(err, "job %s", id)
}

// Chain is a full backup and its incrementals (PRD J-5).
type Chain struct {
	ID        uuid.UUID `json:"id"`
	HostID    int64     `json:"host_id"`
	ProjectID int64     `json:"project_id,omitzero"`
	Project   string    `json:"project"`
	FullJobID uuid.UUID `json:"full_job_id,omitzero"`
	StartedAt time.Time `json:"started_at"`
	LastJobAt time.Time `json:"last_job_at,omitzero"`
	Status    string    `json:"status"`
	DeletedAt time.Time `json:"deleted_at,omitzero"`
}

const chainColumns = "id, host_id, coalesce(project_id, 0), project, full_job_id, started_at, last_job_at, status, deleted_at"

func scanChain(row pgx.Row) (*Chain, error) {
	var (
		c             Chain
		full          *uuid.UUID
		last, deleted *time.Time
	)
	if err := row.Scan(&c.ID, &c.HostID, &c.ProjectID, &c.Project, &full, &c.StartedAt, &last, &c.Status, &deleted); err != nil {
		return nil, err
	}
	if full != nil {
		c.FullJobID = *full
	}
	c.StartedAt, c.LastJobAt, c.DeletedAt = c.StartedAt.UTC(), timeOf(last), timeOf(deleted)
	return &c, nil
}

// Chain returns a chain by ID.
func (s *Store) Chain(ctx context.Context, id uuid.UUID) (*Chain, error) {
	c, err := scanChain(s.pool.QueryRow(ctx, "SELECT "+chainColumns+" FROM chains WHERE id = $1", id))
	return c, dbError(err, "chain %s", id)
}

// JobEvents returns a job's events in order (PRD J-7).
func (s *Store) JobEvents(ctx context.Context, id uuid.UUID) ([]api.JobEvent, error) {
	rows, err := s.pool.Query(ctx, "SELECT seq, ts, type, payload FROM job_events WHERE job_id = $1 ORDER BY ts, seq", id)
	if err != nil {
		return nil, dbError(err, "events of job %s", id)
	}
	defer rows.Close()
	events := []api.JobEvent{}
	for rows.Next() {
		var (
			e       api.JobEvent
			payload []byte
		)
		if err := rows.Scan(&e.Seq, &e.At, &e.Type, &payload); err != nil {
			return nil, dbError(err, "scan event")
		}
		e.At, e.Payload = e.At.UTC(), payload
		events = append(events, e)
	}
	return events, dbError(rows.Err(), "events of job %s", id)
}
