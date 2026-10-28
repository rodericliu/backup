// Package cache is the agent's local SQLite state: the last known
// configuration, backup chains, jobs and the queue of results not yet
// reported to the server.
package cache

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"
	"uuid"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/rodericliu/backup/internal/api"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

// migrations are applied in order; PRAGMA user_version records progress.
var migrations = []string{
	`CREATE TABLE config (
		id         INTEGER PRIMARY KEY CHECK (id = 1),
		version    INTEGER NOT NULL,
		projects   BLOB NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE TABLE chains (
		id         TEXT PRIMARY KEY,
		project    TEXT NOT NULL,
		status     TEXT NOT NULL,
		started_at INTEGER NOT NULL,
		data       BLOB NOT NULL
	);
	CREATE INDEX chains_project ON chains (project, started_at);
	CREATE TABLE jobs (
		id           TEXT PRIMARY KEY,
		project      TEXT NOT NULL,
		type         TEXT NOT NULL,
		status       TEXT NOT NULL,
		chain_id     TEXT NOT NULL,
		scheduled_at INTEGER NOT NULL,
		finished_at  INTEGER NOT NULL,
		data         BLOB NOT NULL,
		rev          INTEGER NOT NULL DEFAULT 1,
		reported_rev INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX jobs_project ON jobs (project, type, status, finished_at);
	CREATE INDEX jobs_chain ON jobs (chain_id, scheduled_at);
	CREATE INDEX jobs_status ON jobs (status);
	CREATE INDEX jobs_unreported ON jobs (reported_rev, rev);
	CREATE TABLE job_events (
		seq     INTEGER PRIMARY KEY AUTOINCREMENT,
		job_id  TEXT NOT NULL,
		ts      INTEGER NOT NULL,
		type    TEXT NOT NULL,
		payload BLOB NOT NULL
	);
	CREATE INDEX job_events_job ON job_events (job_id, seq);
	CREATE TABLE meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);`,
}

// DB is the local cache.
type DB struct {
	db *sql.DB
}

// Open opens or creates the cache database at path and applies migrations.
func Open(ctx context.Context, path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	q := url.Values{}
	for _, p := range []string{"journal_mode(WAL)", "busy_timeout(5000)", "synchronous(NORMAL)", "foreign_keys(1)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("open cache: %w", err)
	}
	// A single connection serializes writers and avoids SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	d := &DB{db: db}
	if err := d.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("chmod cache: %w", err)
	}
	return d, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

func (d *DB) migrate(ctx context.Context) error {
	var version int
	if err := d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("cache schema version %d is newer than supported %d", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		err := d.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1))
			return err
		})
		if err != nil {
			return fmt.Errorf("migrate cache to version %d: %w", i+1, err)
		}
	}
	return nil
}

func (d *DB) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// SaveConfig replaces the cached configuration.
func (d *DB) SaveConfig(ctx context.Context, version int64, projects []api.ProjectConfig) error {
	if projects == nil {
		projects = []api.ProjectConfig{}
	}
	b, err := json.Marshal(projects)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	_, err = d.db.ExecContext(ctx, `INSERT INTO config (id, version, projects, updated_at) VALUES (1, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET version = excluded.version, projects = excluded.projects, updated_at = excluded.updated_at`,
		version, b, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// LoadConfig returns the cached configuration, or ErrNotFound.
func (d *DB) LoadConfig(ctx context.Context) (version int64, projects []api.ProjectConfig, err error) {
	var b []byte
	err = d.db.QueryRowContext(ctx, "SELECT version, projects FROM config WHERE id = 1").Scan(&version, &b)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, ErrNotFound
	}
	if err != nil {
		return 0, nil, fmt.Errorf("load config: %w", err)
	}
	if err := json.Unmarshal(b, &projects); err != nil {
		return 0, nil, fmt.Errorf("decode config: %w", err)
	}
	return version, projects, nil
}

// Meta returns a value from the key-value table, or "" when absent.
func (d *DB) Meta(ctx context.Context, key string) (string, error) {
	var v string
	err := d.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read meta %s: %w", key, err)
	}
	return v, nil
}

// SetMeta stores a value in the key-value table.
func (d *DB) SetMeta(ctx context.Context, key, value string) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("write meta %s: %w", key, err)
	}
	return nil
}

func parseID(s string) uuid.UUID {
	id, _ := uuid.Parse(s)
	return id
}
