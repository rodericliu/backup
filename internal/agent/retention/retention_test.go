package retention

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/rodericliu/backup/internal/agent/cache"
	"github.com/rodericliu/backup/internal/api"
)

var now = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

type env struct {
	t     *testing.T
	root  string
	db    *cache.DB
	clean *Cleaner
}

func setup(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	db, err := cache.Open(t.Context(), filepath.Join(dir, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	root := filepath.Join(dir, "backup")
	return &env{t: t, root: root, db: db, clean: &Cleaner{
		Store: db, Root: root, Now: func() time.Time { return now },
		Logger: slog.New(slog.DiscardHandler),
	}}
}

// chain creates a chain directory whose newest backup is age old.
func (e *env) chain(project string, status cache.ChainStatus, age time.Duration) *cache.Chain {
	e.t.Helper()
	started := now.Add(-age - 3*time.Hour)
	c := &cache.Chain{
		ID: uuid.NewV7(), Project: project, Status: status, FullJobID: uuid.NewV7(),
		Dir:       filepath.Join(e.root, project, started.Format("20060102-1504")+"-chain"),
		StartedAt: started, LastJobAt: now.Add(-age),
	}
	if err := os.MkdirAll(filepath.Join(c.Dir, "00-full"), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := e.db.SaveChain(e.t.Context(), c); err != nil {
		e.t.Fatal(err)
	}
	return c
}

func project() *api.ProjectConfig {
	p := api.Template(api.TierImportant)
	p.Name = "gays"
	return &p
}

func ids(chains []cache.Chain) map[uuid.UUID]bool {
	m := make(map[uuid.UUID]bool)
	for _, c := range chains {
		m[c.ID] = true
	}
	return m
}

func TestClean(t *testing.T) {
	e := setup(t)
	day := 24 * time.Hour
	expired := e.chain("gays", cache.ChainActive, 9*day)
	expiredFailed := e.chain("gays", cache.ChainFailed, 8*day)
	boundary := e.chain("gays", cache.ChainActive, 7*day-time.Minute)
	recent := e.chain("gays", cache.ChainActive, day)
	other := e.chain("poppen", cache.ChainActive, 30*day)

	deleted, err := e.clean.Clean(t.Context(), project())
	if err != nil {
		t.Fatal(err)
	}
	got := ids(deleted)
	if len(got) != 2 || !got[expired.ID] || !got[expiredFailed.ID] {
		t.Errorf("deleted = %v, want expired chains", got)
	}
	for _, c := range []*cache.Chain{expired, expiredFailed} {
		if _, err := os.Stat(c.Dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists", c.Dir)
		}
		stored, _ := e.db.Chain(t.Context(), c.ID)
		if stored.Status != cache.ChainDeleted || !stored.DeletedAt.Equal(now) {
			t.Errorf("chain %s not marked deleted: %+v", c.ID, stored)
		}
		events, _ := e.db.JobEvents(t.Context(), c.FullJobID)
		if len(events) != 1 || events[0].Type != cache.EventCleanup || !strings.Contains(string(events[0].Payload), "older than 7 days") {
			t.Errorf("cleanup event = %+v", events)
		}
	}
	for _, c := range []*cache.Chain{boundary, recent, other} {
		if _, err := os.Stat(c.Dir); err != nil {
			t.Errorf("%s should be kept: %v", c.Dir, err)
		}
	}
}

func TestCleanProtectsLatestChains(t *testing.T) {
	day := 24 * time.Hour
	tests := []struct {
		name string
		// statuses from oldest to newest; all are expired
		statuses []cache.ChainStatus
		kept     []int
	}{
		{"only chain", []cache.ChainStatus{cache.ChainActive}, []int{0}},
		{"newest active", []cache.ChainStatus{cache.ChainActive, cache.ChainActive}, []int{1}},
		{"newest failed keeps last good", []cache.ChainStatus{cache.ChainActive, cache.ChainActive, cache.ChainFailed}, []int{1, 2}},
		{"running", []cache.ChainStatus{cache.ChainActive, cache.ChainRunning, cache.ChainFailed}, []int{0, 1, 2}},
		{"no good chain", []cache.ChainStatus{cache.ChainFailed, cache.ChainFailed}, []int{1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := setup(t)
			var chains []*cache.Chain
			for i, s := range tt.statuses {
				chains = append(chains, e.chain("gays", s, time.Duration(30-i)*day))
			}
			if _, err := e.clean.Clean(t.Context(), project()); err != nil {
				t.Fatal(err)
			}
			for i, c := range chains {
				_, err := os.Stat(c.Dir)
				kept := err == nil
				want := false
				for _, k := range tt.kept {
					want = want || k == i
				}
				if kept != want {
					t.Errorf("chain %d (%s): kept=%v, want %v", i, c.Status, kept, want)
				}
			}
		})
	}
}

func TestCleanRefusesOutsideRoot(t *testing.T) {
	e := setup(t)
	outside := t.TempDir()
	bad := &cache.Chain{
		ID: uuid.NewV7(), Project: "gays", Status: cache.ChainActive, Dir: outside,
		StartedAt: now.Add(-40 * 24 * time.Hour),
	}
	if err := e.db.SaveChain(t.Context(), bad); err != nil {
		t.Fatal(err)
	}
	e.chain("gays", cache.ChainActive, time.Hour)

	_, err := e.clean.Clean(t.Context(), project())
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Errorf("err = %v, want refusal", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("directory outside root was deleted: %v", err)
	}
}

func TestDeleteAll(t *testing.T) {
	e := setup(t)
	a := e.chain("gays", cache.ChainActive, time.Hour)
	b := e.chain("gays", cache.ChainRunning, 0)
	if err := e.clean.DeleteAll(t.Context(), "gays"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*cache.Chain{a, b} {
		if _, err := os.Stat(c.Dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists", c.Dir)
		}
	}
	if chains, _ := e.db.Chains(t.Context(), "gays"); len(chains) != 0 {
		t.Errorf("chains left: %d", len(chains))
	}
}

type sizer int64

func (s sizer) BitmapSize(context.Context) (int64, error) { return int64(s), nil }

func TestCheckBitmap(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	if size, err := CheckBitmap(t.Context(), sizer(5), 10, log, "gays"); err != nil || size != 5 || buf.Len() != 0 {
		t.Errorf("below threshold: size=%d err=%v log=%q", size, err, buf.String())
	}
	if _, err := CheckBitmap(t.Context(), sizer(11), 10, log, "gays"); err != nil || !strings.Contains(buf.String(), "exceeds threshold") {
		t.Errorf("above threshold: err=%v log=%q", err, buf.String())
	}
}
