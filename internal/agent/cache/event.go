package cache

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"strconv"
	"time"
	"uuid"
)

// Event types recorded in a job's timeline (PRD J-7).
const (
	EventStatus   = "status"
	EventProgress = "progress"
	EventCleanup  = "cleanup"
	EventMessage  = "message"
)

// Event is one entry of a job's timeline.
type Event struct {
	Seq     int64
	JobID   uuid.UUID
	At      time.Time
	Type    string
	Payload []byte // JSON
}

const eventCursorKey = "event_cursor"

// AddEvent appends an event; payload is encoded as JSON.
func (d *DB) AddEvent(ctx context.Context, jobID uuid.UUID, typ string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	_, err = d.db.ExecContext(ctx, "INSERT INTO job_events (job_id, ts, type, payload) VALUES (?, ?, ?, ?)",
		jobID.String(), time.Now().UnixMilli(), typ, b)
	if err != nil {
		return fmt.Errorf("add event: %w", err)
	}
	return nil
}

// JobEvents returns a job's events in order.
func (d *DB) JobEvents(ctx context.Context, jobID uuid.UUID) ([]Event, error) {
	return d.queryEvents(ctx, "SELECT seq, job_id, ts, type, payload FROM job_events WHERE job_id = ? ORDER BY seq", jobID.String())
}

// PendingEvents returns up to limit events after the reported cursor.
func (d *DB) PendingEvents(ctx context.Context, limit int) ([]Event, error) {
	cursor, err := d.eventCursor(ctx)
	if err != nil {
		return nil, err
	}
	return d.queryEvents(ctx, "SELECT seq, job_id, ts, type, payload FROM job_events WHERE seq > ? ORDER BY seq LIMIT ?", cursor, limit)
}

// AckEvents advances the reported cursor to seq.
func (d *DB) AckEvents(ctx context.Context, seq int64) error {
	cursor, err := d.eventCursor(ctx)
	if err != nil {
		return err
	}
	return d.SetMeta(ctx, eventCursorKey, strconv.FormatInt(max(cursor, seq), 10))
}

// PruneEvents deletes reported events older than before.
func (d *DB) PruneEvents(ctx context.Context, before time.Time) error {
	cursor, err := d.eventCursor(ctx)
	if err != nil {
		return err
	}
	_, err = d.db.ExecContext(ctx, "DELETE FROM job_events WHERE seq <= ? AND ts < ?", cursor, before.UnixMilli())
	if err != nil {
		return fmt.Errorf("prune events: %w", err)
	}
	return nil
}

func (d *DB) eventCursor(ctx context.Context) (int64, error) {
	v, err := d.Meta(ctx, eventCursorKey)
	if err != nil || v == "" {
		return 0, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse event cursor: %w", err)
	}
	return n, nil
}

func (d *DB) queryEvents(ctx context.Context, query string, args ...any) ([]Event, error) {
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var (
			e     Event
			jobID string
			ts    int64
		)
		if err := rows.Scan(&e.Seq, &jobID, &ts, &e.Type, &e.Payload); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		e.JobID = parseID(jobID)
		e.At = time.UnixMilli(ts)
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	return events, nil
}

// LastEventSeq returns the sequence number of the newest event, or 0.
func (d *DB) LastEventSeq(ctx context.Context) (int64, error) {
	var seq int64
	if err := d.db.QueryRowContext(ctx, "SELECT coalesce(max(seq), 0) FROM job_events").Scan(&seq); err != nil {
		return 0, fmt.Errorf("last event seq: %w", err)
	}
	return seq, nil
}
