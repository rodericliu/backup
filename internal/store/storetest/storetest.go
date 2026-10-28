// Package storetest creates throwaway PostgreSQL databases for tests.
package storetest

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/rodericliu/backup/internal/store"
)

// EnvURL names the environment variable holding a PostgreSQL URL of a role
// allowed to create databases, for example
// postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable.
const EnvURL = "BACKUP_TEST_DATABASE_URL"

// New creates an empty, migrated database that is dropped when the test ends.
// The test is skipped when EnvURL is not set.
func New(t testing.TB) *store.Store {
	t.Helper()
	s, _ := NewWithURL(t)
	return s
}

// NewWithURL is like New and also returns the URL of the database.
func NewWithURL(t testing.TB) (*store.Store, string) {
	t.Helper()
	base := os.Getenv(EnvURL)
	if base == "" {
		t.Skipf("%s not set", EnvURL)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse %s: %v", EnvURL, err)
	}
	name := "backup_test_" + strings.ToLower(rand.Text()[:16])
	ctx := context.Background()

	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect %s: %v", EnvURL, err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %q", name)); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(ctx, base)
		if err != nil {
			t.Errorf("drop database: %v", err)
			return
		}
		defer c.Close(ctx)
		if _, err := c.Exec(ctx, fmt.Sprintf("DROP DATABASE %q WITH (FORCE)", name)); err != nil {
			t.Errorf("drop database: %v", err)
		}
	})

	u.Path = "/" + name
	dsn := u.String()
	s, err := store.Open(ctx, dsn, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	return s, dsn
}
