package cache

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"uuid"

	"github.com/rodericliu/backup/internal/api"
)

func openTest(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "cache.db")
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func TestOpenReopen(t *testing.T) {
	db, path := openTest(t)
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("cache file mode: %v %v", fi.Mode(), err)
	}
	if err := db.SetMeta(t.Context(), "k", "v"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db2, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if v, err := db2.Meta(t.Context(), "k"); err != nil || v != "v" {
		t.Errorf("meta after reopen = %q, %v", v, err)
	}
	if v, err := db2.Meta(t.Context(), "missing"); err != nil || v != "" {
		t.Errorf("missing meta = %q, %v", v, err)
	}
}

func TestConfig(t *testing.T) {
	db, _ := openTest(t)
	if _, _, err := db.LoadConfig(t.Context()); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty config: err = %v, want ErrNotFound", err)
	}
	p := api.Template(api.TierImportant)
	p.Name, p.Container, p.MySQL = "gays", "gays_slave", api.MySQLConfig{User: "backup", Password: "secret"}
	for _, v := range []int64{3, 4} {
		if err := db.SaveConfig(t.Context(), v, []api.ProjectConfig{p}); err != nil {
			t.Fatal(err)
		}
	}
	v, got, err := db.LoadConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if v != 4 || len(got) != 1 || got[0].Name != "gays" || got[0].MySQL.Password != "secret" || got[0].FullInterval != p.FullInterval {
		t.Errorf("config = %d %+v", v, got)
	}
}

func newJob(project string, typ api.JobType, chain uuid.UUID, at time.Time, status api.JobStatus) *api.Job {
	j := &api.Job{
		ID: uuid.NewV7(), Project: project, Type: typ, ChainID: chain, Trigger: api.TriggerSchedule,
		Status: status, ScheduledAt: at, StartedAt: at,
	}
	if status.Terminal() {
		j.FinishedAt = at.Add(time.Minute)
	}
	return j
}

var t0 = time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)

func TestChains(t *testing.T) {
	db, _ := openTest(t)
	ctx := t.Context()
	if _, err := db.CurrentChain(ctx, "gays"); !errors.Is(err, ErrNotFound) {
		t.Errorf("no chain: err = %v", err)
	}
	old := &Chain{ID: uuid.NewV7(), Project: "gays", Mode: api.IncrChained, Status: ChainActive, StartedAt: t0}
	failed := &Chain{ID: uuid.NewV7(), Project: "gays", Status: ChainFailed, StartedAt: t0.Add(4 * time.Hour)}
	gone := &Chain{ID: uuid.NewV7(), Project: "gays", Status: ChainDeleted, StartedAt: t0.Add(-4 * time.Hour)}
	other := &Chain{ID: uuid.NewV7(), Project: "poppen", Status: ChainActive, StartedAt: t0.Add(8 * time.Hour)}
	for _, c := range []*Chain{old, failed, gone, other} {
		if err := db.SaveChain(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	cur, err := db.CurrentChain(ctx, "gays")
	if err != nil || cur.ID != old.ID || cur.Mode != api.IncrChained {
		t.Errorf("current = %+v, %v; want %s", cur, err, old.ID)
	}
	chains, err := db.Chains(ctx, "gays")
	if err != nil || len(chains) != 2 || chains[0].ID != old.ID || chains[1].ID != failed.ID {
		t.Errorf("chains = %+v, %v", chains, err)
	}

	old.Status, old.DeletedAt = ChainDeleted, t0.Add(time.Hour)
	if err := db.SaveChain(ctx, old); err != nil {
		t.Fatal(err)
	}
	got, err := db.Chain(ctx, old.ID)
	if err != nil || got.Status != ChainDeleted || !got.DeletedAt.Equal(old.DeletedAt) {
		t.Errorf("updated chain = %+v, %v", got, err)
	}
	if _, err := db.CurrentChain(ctx, "gays"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: err = %v", err)
	}
}

func TestJobs(t *testing.T) {
	db, _ := openTest(t)
	ctx := t.Context()
	chain := uuid.NewV7()
	full := newJob("gays", api.JobFull, chain, t0, api.StatusSuccess)
	full.RawSize = 1000
	incr1 := newJob("gays", api.JobIncr, chain, t0.Add(time.Hour), api.StatusWarning)
	incr2 := newJob("gays", api.JobIncr, chain, t0.Add(2*time.Hour), api.StatusFailed)
	running := newJob("gays", api.JobIncr, chain, t0.Add(3*time.Hour), api.StatusRunning)
	other := newJob("poppen", api.JobFull, uuid.NewV7(), t0, api.StatusSuccess)
	for _, j := range []*api.Job{full, incr1, incr2, running, other} {
		if err := db.SaveJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}

	if got, err := db.Job(ctx, full.ID); err != nil || got.RawSize != 1000 || !got.ScheduledAt.Equal(t0) {
		t.Errorf("Job = %+v, %v", got, err)
	}
	if _, err := db.Job(ctx, uuid.NewV7()); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing job: %v", err)
	}
	if got, err := db.LastSucceeded(ctx, "gays", api.JobFull); err != nil || got.ID != full.ID {
		t.Errorf("LastSucceeded full = %v, %v", got, err)
	}
	if got, err := db.LastSucceeded(ctx, "gays", api.JobIncr); err != nil || got.ID != incr1.ID {
		t.Errorf("LastSucceeded incr = %v, %v", got, err)
	}
	if got, err := db.LastInChain(ctx, chain); err != nil || got.ID != incr1.ID {
		t.Errorf("LastInChain = %v, %v; want warning incr", got, err)
	}
	if jobs, err := db.ChainJobs(ctx, chain); err != nil || len(jobs) != 4 || jobs[0].ID != full.ID {
		t.Errorf("ChainJobs = %d, %v", len(jobs), err)
	}
	if jobs, err := db.JobsWithStatus(ctx, api.StatusRunning, api.StatusPending); err != nil || len(jobs) != 1 || jobs[0].ID != running.ID {
		t.Errorf("JobsWithStatus = %v, %v", jobs, err)
	}
	if jobs, err := db.RecentJobs(ctx, "gays", t0.Add(time.Hour)); err != nil || len(jobs) != 3 {
		t.Errorf("RecentJobs = %d, %v", len(jobs), err)
	}
}

func TestUnreported(t *testing.T) {
	db, _ := openTest(t)
	ctx := t.Context()
	done := newJob("gays", api.JobFull, uuid.NewV7(), t0, api.StatusSuccess)
	running := newJob("gays", api.JobIncr, uuid.NewV7(), t0.Add(time.Hour), api.StatusRunning)
	for _, j := range []*api.Job{done, running} {
		if err := db.SaveJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	un, err := db.UnreportedJobs(ctx, 10)
	if err != nil || len(un) != 1 || un[0].Job.ID != done.ID {
		t.Fatalf("unreported = %+v, %v", un, err)
	}
	rev := un[0].Rev

	// A local update after reading must not be lost by the acknowledgement.
	done.SHA256 = "abc"
	if err := db.SaveJob(ctx, done); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkReported(ctx, done.ID, rev); err != nil {
		t.Fatal(err)
	}
	un, err = db.UnreportedJobs(ctx, 10)
	if err != nil || len(un) != 1 || un[0].Job.SHA256 != "abc" {
		t.Fatalf("after stale ack = %+v, %v", un, err)
	}
	if err := db.MarkReported(ctx, done.ID, un[0].Rev); err != nil {
		t.Fatal(err)
	}
	if un, err = db.UnreportedJobs(ctx, 10); err != nil || len(un) != 0 {
		t.Errorf("after ack = %+v, %v", un, err)
	}
}

func TestEvents(t *testing.T) {
	db, _ := openTest(t)
	ctx := t.Context()
	job := uuid.NewV7()
	for i := range 3 {
		if err := db.AddEvent(ctx, job, EventProgress, map[string]int{"written": i}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := db.JobEvents(ctx, job)
	if err != nil || len(events) != 3 || events[0].JobID != job || string(events[2].Payload) != `{"written":2}` {
		t.Fatalf("events = %+v, %v", events, err)
	}

	if seq, err := db.LastEventSeq(ctx); err != nil || seq != events[2].Seq {
		t.Errorf("LastEventSeq = %d, %v", seq, err)
	}

	pending, err := db.PendingEvents(ctx, 2)
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending = %d, %v", len(pending), err)
	}
	if err := db.AckEvents(ctx, pending[1].Seq); err != nil {
		t.Fatal(err)
	}
	if err := db.AckEvents(ctx, pending[0].Seq); err != nil { // cursor never moves back
		t.Fatal(err)
	}
	pending, err = db.PendingEvents(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].Seq != events[2].Seq {
		t.Fatalf("pending after ack = %+v, %v", pending, err)
	}

	if err := db.PruneEvents(ctx, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	events, err = db.JobEvents(ctx, job)
	if err != nil || len(events) != 1 {
		t.Errorf("after prune = %d events, %v; want only the unreported one", len(events), err)
	}
}
