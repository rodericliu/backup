// Package agent wires the backup-agent components together.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/rodericliu/backup/internal/agent/cache"
	"github.com/rodericliu/backup/internal/agent/config"
	"github.com/rodericliu/backup/internal/agent/executor"
	"github.com/rodericliu/backup/internal/agent/mysql"
	"github.com/rodericliu/backup/internal/agent/podman"
	"github.com/rodericliu/backup/internal/agent/retention"
	"github.com/rodericliu/backup/internal/agent/worker"
	"github.com/rodericliu/backup/internal/api"
)

const (
	// monitorInterval is how often bitmap sizes are checked and events pruned.
	monitorInterval = time.Hour
	// eventRetention is how long reported job events are kept locally.
	eventRetention = 30 * 24 * time.Hour
)

// Agent is a running backup agent.
type Agent struct {
	cfg    *config.Config
	log    *slog.Logger
	db     *cache.DB
	podman *podman.Client
	worker *worker.Worker
	unlock func() error
}

// New takes the host lock, opens the local cache and builds the worker.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (*Agent, error) {
	for _, bin := range []string{cfg.Podman, "zstd"} {
		if _, err := exec.LookPath(bin); err != nil {
			return nil, fmt.Errorf("required binary %s: %w", bin, err)
		}
	}
	unlock, err := worker.Lock(cfg.LockFile)
	if err != nil {
		return nil, err
	}
	db, err := cache.Open(ctx, filepath.Join(cfg.StateDir, "cache.db"))
	if err != nil {
		return nil, errors.Join(err, unlock())
	}

	pc := podman.New(cfg.Podman)
	pc.User = "root"
	exe := &executor.Executor{
		Podman: pc,
		Logger: log,
		Opts: executor.Options{
			BackupRoot:    cfg.BackupRoot,
			ZstdThreads:   cfg.Zstd.Threads,
			ZstdLevel:     cfg.Zstd.Level,
			LowIOPriority: cfg.LowIOPriority,
		},
	}
	w := worker.New(worker.Options{
		DB:       db,
		Runner:   exe,
		Cleaner:  &retention.Cleaner{Store: db, Root: cfg.BackupRoot, Logger: log},
		Root:     cfg.BackupRoot,
		Location: cfg.Location,
		Logger:   log,
	})
	a := &Agent{cfg: cfg, log: log, db: db, podman: pc, worker: w, unlock: unlock}
	projects, err := a.projects(ctx, cfg)
	if err != nil {
		return nil, errors.Join(err, a.Close())
	}
	w.SetProjects(projects)
	return a, nil
}

// projects returns the locally configured projects or, when there are none,
// the configuration cached from the server.
func (a *Agent) projects(ctx context.Context, cfg *config.Config) ([]api.ProjectConfig, error) {
	if len(cfg.Projects) > 0 {
		a.log.Info("using projects from configuration file", "projects", len(cfg.Projects))
		return cfg.Projects, nil
	}
	version, projects, err := a.db.LoadConfig(ctx)
	switch {
	case errors.Is(err, cache.ErrNotFound):
		a.log.Warn("no projects configured; the agent is idle")
		return nil, nil
	case err != nil:
		return nil, err
	}
	a.log.Info("using cached configuration", "version", version, "projects", len(projects))
	return projects, nil
}

// Reload applies a new configuration. Only projects are reloaded; other
// settings require a restart.
func (a *Agent) Reload(ctx context.Context, cfg *config.Config) error {
	if cfg.BackupRoot != a.cfg.BackupRoot || cfg.StateDir != a.cfg.StateDir || cfg.LockFile != a.cfg.LockFile ||
		cfg.Podman != a.cfg.Podman || cfg.Zstd != a.cfg.Zstd || cfg.Timezone != a.cfg.Timezone {
		a.log.Warn("only projects are reloaded; restart the agent to apply other changes")
	}
	projects, err := a.projects(ctx, cfg)
	if err != nil {
		return err
	}
	a.worker.SetProjects(projects)
	return nil
}

// Run executes backups until ctx is done.
func (a *Agent) Run(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.monitor(ctx)
	}()
	err := a.worker.Run(ctx)
	<-done
	return err
}

// Worker returns the agent's worker.
func (a *Agent) Worker() *worker.Worker { return a.worker }

// Close releases the cache and the host lock.
func (a *Agent) Close() error {
	return errors.Join(a.db.Close(), a.unlock())
}

func (a *Agent) monitor(ctx context.Context) {
	t := time.NewTicker(monitorInterval)
	defer t.Stop()
	for {
		a.checkBitmaps(ctx)
		a.pruneEvents(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// checkBitmaps warns about changed page bitmaps above the threshold (PRD R-5).
func (a *Agent) checkBitmaps(ctx context.Context) {
	for _, p := range a.worker.Projects() {
		if !p.Enabled() || !p.HasIncr() || ctx.Err() != nil {
			continue
		}
		if err := a.checkBitmap(ctx, &p); err != nil {
			a.log.Warn("check changed page bitmap", "project", p.Name, "err", err)
		}
	}
}

func (a *Agent) checkBitmap(ctx context.Context, p *api.ProjectConfig) error {
	c, err := a.podman.Inspect(ctx, p.Container)
	if err != nil || !c.Running {
		return err
	}
	defaults, err := mysql.WriteDefaultsFile(filepath.Join(a.cfg.BackupRoot, executor.CredentialsDir), p.MySQL)
	if err != nil {
		return err
	}
	defer func() {
		if err := defaults.Remove(); err != nil {
			a.log.Error("remove credentials file", "err", err)
		}
	}()
	client := &mysql.Client{Exec: a.podman, Container: p.Container, Defaults: defaults}
	on, err := client.TrackChangedPages(ctx)
	if err != nil || !on {
		return err
	}
	_, err = retention.CheckBitmap(ctx, client, a.cfg.BitmapAlertSize, a.log, p.Name)
	return err
}

// pruneEvents bounds the local event table. Without a server nothing reports
// events, so they are acknowledged locally first.
func (a *Agent) pruneEvents(ctx context.Context) {
	if a.cfg.ServerURL == "" {
		seq, err := a.db.LastEventSeq(ctx)
		if err == nil {
			err = a.db.AckEvents(ctx, seq)
		}
		if err != nil {
			a.log.Warn("acknowledge local events", "err", err)
			return
		}
	}
	if err := a.db.PruneEvents(ctx, time.Now().Add(-eventRetention)); err != nil {
		a.log.Warn("prune events", "err", err)
	}
}
