// Package retention deletes expired backup chains (PRD 6.4.4).
package retention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"uuid"

	"github.com/rodericliu/backup/internal/agent/cache"
	"github.com/rodericliu/backup/internal/api"
)

// Store is the subset of *cache.DB used by the cleaner.
type Store interface {
	Chains(ctx context.Context, project string) ([]cache.Chain, error)
	SaveChain(ctx context.Context, c *cache.Chain) error
	AddEvent(ctx context.Context, jobID uuid.UUID, typ string, payload any) error
}

// Cleaner applies the retention policy of a project.
type Cleaner struct {
	Store  Store
	Root   string // backup root; chains outside it are never deleted
	Logger *slog.Logger
	Now    func() time.Time
}

// CleanupEvent is recorded on a deleted chain's full job (PRD R-3).
type CleanupEvent = api.CleanupEvent

// Clean deletes chains whose newest backup is older than the project's
// retention, never touching the newest chain or the newest chain whose full
// backup succeeded (PRD R-1, R-2). It returns the deleted chains.
func (c *Cleaner) Clean(ctx context.Context, p *api.ProjectConfig) ([]cache.Chain, error) {
	chains, err := c.Store.Chains(ctx, p.Name)
	if err != nil {
		return nil, fmt.Errorf("list chains: %w", err)
	}
	keep := protected(chains)
	cutoff := c.now().Add(-time.Duration(p.RetentionDays) * 24 * time.Hour)

	var (
		deleted []cache.Chain
		errs    []error
	)
	for i := range chains {
		ch := &chains[i]
		last := lastBackupAt(ch)
		if keep[i] || !last.Before(cutoff) {
			continue
		}
		reason := fmt.Sprintf("newest backup %s is older than %d days", last.UTC().Format(time.RFC3339), p.RetentionDays)
		if err := c.delete(ctx, p.Name, ch, last, reason); err != nil {
			errs = append(errs, err)
			continue
		}
		deleted = append(deleted, *ch)
	}
	return deleted, errors.Join(errs...)
}

// Purge removes the chains of a deleted project that started before before
// (PRD P-7). Chains started later belong to a new project of the same name
// and are kept. It returns the number of deleted chains.
func (c *Cleaner) Purge(ctx context.Context, project string, before time.Time) (int, error) {
	chains, err := c.Store.Chains(ctx, project)
	if err != nil {
		return 0, fmt.Errorf("list chains: %w", err)
	}
	var (
		n    int
		errs []error
	)
	for i := range chains {
		if !chains[i].StartedAt.Before(before) {
			continue
		}
		if err := c.delete(ctx, project, &chains[i], lastBackupAt(&chains[i]), "project deleted"); err != nil {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// protected marks the newest chain, which may still receive backups, and the
// newest chain whose full backup succeeded, which is the latest restorable one.
func protected(chains []cache.Chain) []bool {
	keep := make([]bool, len(chains))
	if len(chains) == 0 {
		return keep
	}
	newest, newestActive := 0, -1
	for i := range chains {
		if chains[i].StartedAt.After(chains[newest].StartedAt) {
			newest = i
		}
		if chains[i].Status == cache.ChainActive &&
			(newestActive < 0 || chains[i].StartedAt.After(chains[newestActive].StartedAt)) {
			newestActive = i
		}
	}
	keep[newest] = true
	if newestActive >= 0 {
		keep[newestActive] = true
	}
	for i := range chains {
		if chains[i].Status == cache.ChainRunning {
			keep[i] = true
		}
	}
	return keep
}

func lastBackupAt(ch *cache.Chain) time.Time {
	if ch.LastJobAt.After(ch.StartedAt) {
		return ch.LastJobAt
	}
	return ch.StartedAt
}

func (c *Cleaner) delete(ctx context.Context, project string, ch *cache.Chain, last time.Time, reason string) error {
	if err := c.checkDir(project, ch.Dir); err != nil {
		return fmt.Errorf("chain %s: %w", ch.ID, err)
	}
	if err := os.RemoveAll(ch.Dir); err != nil {
		return fmt.Errorf("delete chain %s: %w", ch.ID, err)
	}
	ch.Status = cache.ChainDeleted
	ch.DeletedAt = c.now()
	if err := c.Store.SaveChain(ctx, ch); err != nil {
		return fmt.Errorf("mark chain %s deleted: %w", ch.ID, err)
	}
	ev := CleanupEvent{ChainID: ch.ID.String(), Dir: ch.Dir, LastAt: last, Reason: reason}
	if err := c.Store.AddEvent(ctx, ch.FullJobID, cache.EventCleanup, ev); err != nil {
		return fmt.Errorf("record cleanup of chain %s: %w", ch.ID, err)
	}
	c.logger().Info("deleted backup chain", "project", project, "chain", ch.ID.String(), "dir", ch.Dir, "reason", reason)
	return nil
}

// checkDir refuses to delete anything that is not a chain directory directly
// below <root>/<project>.
func (c *Cleaner) checkDir(project, dir string) error {
	if c.Root == "" || dir == "" {
		return errors.New("backup root and chain dir are required")
	}
	want := filepath.Join(c.Root, project)
	if filepath.Dir(filepath.Clean(dir)) != want || !strings.HasSuffix(dir, "-chain") {
		return fmt.Errorf("refusing to delete %q: not a chain directory in %q", dir, want)
	}
	return nil
}

func (c *Cleaner) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Cleaner) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

// BitmapSizer reports the size of a replica's changed page bitmap files.
type BitmapSizer interface {
	BitmapSize(ctx context.Context) (int64, error)
}

// CheckBitmap logs a warning when the bitmap files exceed threshold bytes
// (PRD R-5). It returns the measured size.
func CheckBitmap(ctx context.Context, s BitmapSizer, threshold int64, log *slog.Logger, project string) (int64, error) {
	size, err := s.BitmapSize(ctx)
	if err != nil {
		return 0, fmt.Errorf("bitmap size: %w", err)
	}
	if threshold > 0 && size > threshold {
		log.Warn("changed page bitmap exceeds threshold", "project", project, "size", size, "threshold", threshold)
	}
	return size, nil
}
