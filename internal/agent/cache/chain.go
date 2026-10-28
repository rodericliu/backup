package cache

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"time"
	"uuid"

	"github.com/rodericliu/backup/internal/api"
)

// ChainStatus is the state of a backup chain.
type ChainStatus string

const (
	// ChainRunning: the full backup has not finished yet.
	ChainRunning ChainStatus = "running"
	// ChainActive: the full backup succeeded; incrementals may be added.
	ChainActive ChainStatus = "active"
	// ChainFailed: the full backup failed; the chain cannot be restored.
	ChainFailed ChainStatus = "failed"
	// ChainDeleted: the files were removed by retention.
	ChainDeleted ChainStatus = "deleted"
)

// Chain is a full backup and its incrementals (PRD 3, 6.4.3).
type Chain struct {
	ID        uuid.UUID    `json:"id"`
	Project   string       `json:"project"`
	Mode      api.IncrMode `json:"mode"`
	Dir       string       `json:"dir"`
	FullJobID uuid.UUID    `json:"full_job_id"`
	Status    ChainStatus  `json:"status"`
	StartedAt time.Time    `json:"started_at"`
	LastJobAt time.Time    `json:"last_job_at,omitzero"`
	DeletedAt time.Time    `json:"deleted_at,omitzero"`
}

// SaveChain inserts or replaces a chain.
func (d *DB) SaveChain(ctx context.Context, c *Chain) error {
	b, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode chain: %w", err)
	}
	_, err = d.db.ExecContext(ctx, `INSERT INTO chains (id, project, status, started_at, data) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET status = excluded.status, data = excluded.data`,
		c.ID.String(), c.Project, c.Status, unixMilli(c.StartedAt), b)
	if err != nil {
		return fmt.Errorf("save chain: %w", err)
	}
	return nil
}

// Chain returns a chain by ID, or ErrNotFound.
func (d *DB) Chain(ctx context.Context, id uuid.UUID) (*Chain, error) {
	chains, err := d.queryChains(ctx, "SELECT data FROM chains WHERE id = ?", id.String())
	if err != nil {
		return nil, err
	}
	if len(chains) == 0 {
		return nil, ErrNotFound
	}
	return &chains[0], nil
}

// Chains returns the project's chains that have not been deleted, oldest first.
func (d *DB) Chains(ctx context.Context, project string) ([]Chain, error) {
	return d.queryChains(ctx, "SELECT data FROM chains WHERE project = ? AND status != ? ORDER BY started_at", project, ChainDeleted)
}

// CurrentChain returns the project's newest chain whose full backup succeeded,
// or ErrNotFound.
func (d *DB) CurrentChain(ctx context.Context, project string) (*Chain, error) {
	chains, err := d.queryChains(ctx, "SELECT data FROM chains WHERE project = ? AND status = ? ORDER BY started_at DESC LIMIT 1", project, ChainActive)
	if err != nil {
		return nil, err
	}
	if len(chains) == 0 {
		return nil, ErrNotFound
	}
	return &chains[0], nil
}

func (d *DB) queryChains(ctx context.Context, query string, args ...any) ([]Chain, error) {
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query chains: %w", err)
	}
	defer rows.Close()
	var chains []Chain
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, fmt.Errorf("scan chain: %w", err)
		}
		var c Chain
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, fmt.Errorf("decode chain: %w", err)
		}
		chains = append(chains, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query chains: %w", err)
	}
	return chains, nil
}
