package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rodericliu/backup/internal/agent/agenttest"
	"github.com/rodericliu/backup/internal/agent/config"
	"github.com/rodericliu/backup/internal/agent/executor"
	"github.com/rodericliu/backup/internal/agent/worker"
	"github.com/rodericliu/backup/internal/api"
)

func testConfig(t *testing.T, podman string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	doc := fmt.Sprintf(`
state_dir: %[1]s/state
backup_root: %[1]s/backup
lock_file: %[1]s/run/agent.lock
podman: %[2]s
timezone: UTC
zstd: {threads: 1, level: 1}
log: {dir: ""}
projects:
  - name: gays
    container: gays_maindb_slave
    mysql: {user: backup, password: s3cret}
`, dir, podman)
	cfg, err := config.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestAgentEndToEnd(t *testing.T) {
	podman, _ := agenttest.Install(t)
	cfg := testConfig(t, podman)
	log := slog.New(slog.DiscardHandler)

	a, err := New(t.Context(), cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if _, err := New(t.Context(), cfg, log); !errors.Is(err, worker.ErrLocked) {
		t.Errorf("second agent: err = %v, want ErrLocked", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	id, err := a.Worker().Enqueue(t.Context(), "gays", api.JobFull)
	if err != nil {
		t.Fatal(err)
	}
	var job *api.Job
	deadline := time.Now().Add(30 * time.Second)
	for {
		job, err = a.db.Job(t.Context(), id)
		if err == nil && job.Status.Terminal() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not finish: %+v %v", job, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if job.Status != api.StatusSuccess {
		t.Fatalf("job = %s %s: %s", job.Status, job.ErrorCode, job.ErrorMessage)
	}
	if fi, err := os.Stat(job.Path); err != nil || fi.Size() == 0 {
		t.Errorf("backup file: %v", err)
	}
	if want := filepath.Join(cfg.BackupRoot, "gays"); filepath.Dir(filepath.Dir(filepath.Dir(job.Path))) != want {
		t.Errorf("path = %s, want below %s", job.Path, want)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(job.Path), executor.MetaFile)); err != nil {
		t.Errorf("meta.json: %v", err)
	}
}

func TestReloadKeepsCachedFallback(t *testing.T) {
	podman, _ := agenttest.Install(t)
	cfg := testConfig(t, podman)
	a, err := New(t.Context(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	next := *cfg
	next.Projects = append([]api.ProjectConfig(nil), cfg.Projects...)
	next.Projects[0].Disabled = true
	if err := a.Reload(t.Context(), &next); err != nil {
		t.Fatal(err)
	}
	if ps := a.Worker().Projects(); len(ps) != 1 || ps[0].Enabled() {
		t.Errorf("projects after reload = %+v", ps)
	}

	// Without local projects the cached server configuration is used.
	if err := a.db.SaveConfig(t.Context(), 7, cfg.Projects); err != nil {
		t.Fatal(err)
	}
	next.Projects = nil
	if err := a.Reload(t.Context(), &next); err != nil {
		t.Fatal(err)
	}
	if ps := a.Worker().Projects(); len(ps) != 1 || !ps[0].Enabled() {
		t.Errorf("projects from cache = %+v", ps)
	}
}
