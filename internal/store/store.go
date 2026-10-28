// Package store is the PostgreSQL storage of backup-server (PRD 7). Only
// backup-server accesses the database; the schema is migrated at startup.
package store

import (
	"context"
	"embed"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a write violates a uniqueness constraint.
var ErrConflict = errors.New("conflict")

// Store is the server database.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to the database described by dsn (a PostgreSQL URL or
// key=value connection string) and checks the connection.
func Open(ctx context.Context, dsn string, maxConns int32) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database config: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect database: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close closes all connections.
func (s *Store) Close() { s.pool.Close() }

// Ping checks the connection, for health checks.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Migrate applies all pending schema migrations. Concurrent servers are
// serialized by an advisory lock taken by the migration driver.
func (s *Store) Migrate() error {
	src, err := iofs.New(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	db := stdlib.OpenDBFromPool(s.pool)
	defer db.Close()
	drv, err := migratepgx.WithInstance(db, &migratepgx.Config{})
	if err != nil {
		return fmt.Errorf("migration driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "pgx5", drv)
	if err != nil {
		return errors.Join(fmt.Errorf("migrate: %w", err), drv.Close())
	}
	err = m.Up()
	if errors.Is(err, migrate.ErrNoChange) {
		err = nil
	}
	if err != nil {
		err = fmt.Errorf("migrate: %w", err)
	}
	// Close releases the connection held by the driver for its advisory lock.
	srcErr, dbErr := m.Close()
	return errors.Join(err, srcErr, dbErr)
}

// SchemaVersion returns the applied migration version.
func (s *Store) SchemaVersion(ctx context.Context) (uint, error) {
	var (
		v     int64
		dirty bool
	)
	err := s.pool.QueryRow(ctx, "SELECT version, dirty FROM schema_migrations").Scan(&v, &dirty)
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if dirty {
		return uint(v), fmt.Errorf("schema version %d is dirty", v)
	}
	return uint(v), nil
}

// tx runs fn in a transaction, committing when it returns nil.
func (s *Store) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.pool, fn)
}

// dbError maps driver errors to ErrNotFound and ErrConflict.
func dbError(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	msg := fmt.Sprintf(format, args...)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", msg, ErrNotFound)
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" {
		return fmt.Errorf("%s: %w: %s", msg, ErrConflict, pgErr.ConstraintName)
	}
	return fmt.Errorf("%s: %w", msg, err)
}

// Setting returns the JSON value of a setting decoded into v, or ErrNotFound.
func (s *Store) Setting(ctx context.Context, key string, v any) error {
	var b []byte
	if err := s.pool.QueryRow(ctx, "SELECT value FROM settings WHERE key = $1", key).Scan(&b); err != nil {
		return dbError(err, "read setting %s", key)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("decode setting %s: %w", key, err)
	}
	return nil
}

// SetSetting stores v, encoded as JSON, under key.
func (s *Store) SetSetting(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode setting %s: %w", key, err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, now())
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, string(b))
	return dbError(err, "write setting %s", key)
}

// nullTime converts a zero time to NULL.
func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// timeOf converts NULL to the zero time.
func timeOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}
