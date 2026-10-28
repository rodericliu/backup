package store

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rodericliu/backup/internal/api"
)

// ErrTokenInvalid is returned when a registration token is unknown, used or
// expired.
var ErrTokenInvalid = errors.New("registration token is invalid, used or expired")

// Host is a backup server running backup-agent.
type Host struct {
	ID                   int64           `json:"id"`
	Name                 string          `json:"name"`
	Hostname             string          `json:"hostname"`
	IP                   string          `json:"ip"`
	AgentVersion         string          `json:"agent_version"`
	APIVersion           int             `json:"api_version"`
	BackupRoot           string          `json:"backup_root"`
	LastHeartbeatAt      time.Time       `json:"last_heartbeat_at,omitzero"`
	Metrics              api.HostMetrics `json:"metrics"`
	Containers           []api.Container `json:"containers"`
	State                api.AgentState  `json:"state"`
	ConfigVersion        int64           `json:"config_version"`
	AppliedConfigVersion int64           `json:"applied_config_version"`
	Paused               bool            `json:"paused"`
	RevokedAt            time.Time       `json:"revoked_at,omitzero"`
	CreatedAt            time.Time       `json:"created_at"`
}

const hostColumns = `id, name, hostname, ip, agent_version, api_version, backup_root, last_heartbeat_at,
	metrics, containers, state, config_version, applied_config_version, paused, revoked_at, created_at`

func scanHost(row pgx.Row) (*Host, error) {
	var (
		h                         Host
		last, revoked             *time.Time
		metrics, containers, stat []byte
	)
	err := row.Scan(&h.ID, &h.Name, &h.Hostname, &h.IP, &h.AgentVersion, &h.APIVersion, &h.BackupRoot, &last,
		&metrics, &containers, &stat, &h.ConfigVersion, &h.AppliedConfigVersion, &h.Paused, &revoked, &h.CreatedAt)
	if err != nil {
		return nil, err
	}
	h.LastHeartbeatAt, h.RevokedAt, h.CreatedAt = timeOf(last), timeOf(revoked), h.CreatedAt.UTC()
	if err := errors.Join(json.Unmarshal(metrics, &h.Metrics), json.Unmarshal(containers, &h.Containers), json.Unmarshal(stat, &h.State)); err != nil {
		return nil, fmt.Errorf("decode host %d: %w", h.ID, err)
	}
	if h.Containers == nil {
		h.Containers = []api.Container{}
	}
	return &h, nil
}

// Hosts returns all hosts ordered by name.
func (s *Store) Hosts(ctx context.Context) ([]Host, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+hostColumns+" FROM hosts ORDER BY name")
	if err != nil {
		return nil, dbError(err, "list hosts")
	}
	defer rows.Close()
	hosts := []Host{}
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, dbError(err, "scan host")
		}
		hosts = append(hosts, *h)
	}
	return hosts, dbError(rows.Err(), "list hosts")
}

// Host returns a host by ID.
func (s *Store) Host(ctx context.Context, id int64) (*Host, error) {
	h, err := scanHost(s.pool.QueryRow(ctx, "SELECT "+hostColumns+" FROM hosts WHERE id = $1", id))
	return h, dbError(err, "host %d", id)
}

// HostByToken returns the non-revoked host owning an agent token hash.
func (s *Store) HostByToken(ctx context.Context, tokenHash string) (*Host, error) {
	h, err := scanHost(s.pool.QueryRow(ctx, "SELECT "+hostColumns+" FROM hosts WHERE token_hash = $1 AND revoked_at IS NULL", tokenHash))
	return h, dbError(err, "host by token")
}

// RegisterToken is a one-time token that registers an agent (PRD H-1).
type RegisterToken struct {
	ID        int64     `json:"id"`
	Note      string    `json:"note"`
	ExpiresAt time.Time `json:"expires_at"`
	UsedAt    time.Time `json:"used_at,omitzero"`
	HostID    int64     `json:"host_id,omitzero"`
	CreatedBy int64     `json:"created_by,omitzero"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateRegisterToken stores the hash of a new registration token.
func (s *Store) CreateRegisterToken(ctx context.Context, t *RegisterToken, tokenHash string) error {
	var by *int64
	if t.CreatedBy != 0 {
		by = &t.CreatedBy
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO register_tokens (token_hash, note, expires_at, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`, tokenHash, t.Note, t.ExpiresAt, by, t.CreatedAt).Scan(&t.ID)
	return dbError(err, "create registration token")
}

// RegisterTokens returns unused tokens that have not expired at now.
func (s *Store) RegisterTokens(ctx context.Context, now time.Time) ([]RegisterToken, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, note, expires_at, coalesce(created_by, 0), created_at FROM register_tokens
		WHERE used_at IS NULL AND expires_at > $1 ORDER BY created_at DESC`, now)
	if err != nil {
		return nil, dbError(err, "list registration tokens")
	}
	defer rows.Close()
	tokens := []RegisterToken{}
	for rows.Next() {
		var t RegisterToken
		if err := rows.Scan(&t.ID, &t.Note, &t.ExpiresAt, &t.CreatedBy, &t.CreatedAt); err != nil {
			return nil, dbError(err, "scan registration token")
		}
		t.ExpiresAt, t.CreatedAt = t.ExpiresAt.UTC(), t.CreatedAt.UTC()
		tokens = append(tokens, t)
	}
	return tokens, dbError(rows.Err(), "list registration tokens")
}

// DeleteRegisterToken removes an unused registration token.
func (s *Store) DeleteRegisterToken(ctx context.Context, id int64) error {
	return s.exec1(ctx, "DELETE FROM register_tokens WHERE id = $1 AND used_at IS NULL", id)
}

// Registration is an agent registering with a registration token.
type Registration struct {
	RegisterTokenHash string
	AgentTokenHash    string
	Hostname          string
	IP                string
	AgentVersion      string
	Now               time.Time
}

// RegisterHost consumes a registration token and creates the host, or takes
// over the existing host of the same name (for example after a reinstall),
// replacing its agent token and lifting a revocation. It returns
// ErrTokenInvalid for unknown, used or expired tokens.
func (s *Store) RegisterHost(ctx context.Context, reg *Registration) (*Host, error) {
	var host *Host
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var tokenID int64
		err := tx.QueryRow(ctx, `SELECT id FROM register_tokens WHERE token_hash = $1 AND used_at IS NULL AND expires_at > $2
			FOR UPDATE`, reg.RegisterTokenHash, reg.Now).Scan(&tokenID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTokenInvalid
		}
		if err != nil {
			return err
		}
		host, err = scanHost(tx.QueryRow(ctx, `INSERT INTO hosts (name, hostname, ip, token_hash, agent_version, created_at)
			VALUES ($1, $1, $2, $3, $4, $5)
			ON CONFLICT (name) DO UPDATE SET hostname = excluded.hostname, ip = excluded.ip, token_hash = excluded.token_hash,
				agent_version = excluded.agent_version, revoked_at = NULL
			RETURNING `+hostColumns, reg.Hostname, reg.IP, reg.AgentTokenHash, reg.AgentVersion, reg.Now))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE register_tokens SET used_at = $2, host_id = $3 WHERE id = $1", tokenID, reg.Now, host.ID)
		return err
	})
	if errors.Is(err, ErrTokenInvalid) {
		return nil, err
	}
	return host, dbError(err, "register host %s", reg.Hostname)
}

// HostReport is the data of a heartbeat.
type HostReport struct {
	IP         string
	APIVersion int
	Heartbeat  *api.Heartbeat
	At         time.Time
}

// RecordHeartbeat stores a heartbeat and returns the host's newest
// configuration version.
func (s *Store) RecordHeartbeat(ctx context.Context, hostID int64, r *HostReport) (int64, error) {
	hb := r.Heartbeat
	containers := hb.Containers
	if containers == nil {
		containers = []api.Container{}
	}
	metrics, err := json.Marshal(hb.Metrics)
	if err != nil {
		return 0, err
	}
	cs, err := json.Marshal(containers)
	if err != nil {
		return 0, err
	}
	state, err := json.Marshal(hb.State)
	if err != nil {
		return 0, err
	}
	var version int64
	err = s.pool.QueryRow(ctx, `UPDATE hosts SET hostname = $2, ip = $3, agent_version = $4, api_version = $5, backup_root = $6,
			last_heartbeat_at = $7, metrics = $8, containers = $9, state = $10, applied_config_version = $11
		WHERE id = $1 AND revoked_at IS NULL RETURNING config_version`,
		hostID, hb.Hostname, r.IP, hb.AgentVersion, r.APIVersion, hb.BackupRoot, r.At,
		string(metrics), string(cs), string(state), hb.ConfigVersion).Scan(&version)
	return version, dbError(err, "record heartbeat of host %d", hostID)
}

// RecordAPIVersion stores the version of an agent whose API version is not
// supported, so that the mismatch is shown on the web.
func (s *Store) RecordAPIVersion(ctx context.Context, hostID int64, apiVersion int, agentVersion string, at time.Time) error {
	return s.exec1(ctx, `UPDATE hosts SET api_version = $2, agent_version = $3, last_heartbeat_at = $4 WHERE id = $1`,
		hostID, apiVersion, agentVersion, at)
}

// RevokeHost revokes a host's agent token (PRD H-5).
func (s *Store) RevokeHost(ctx context.Context, id int64, at time.Time) error {
	return s.exec1(ctx, "UPDATE hosts SET revoked_at = $2, token_hash = NULL WHERE id = $1 AND revoked_at IS NULL", id, at)
}

// SetHostPaused pauses or resumes scheduling on a host (PRD S-9) and bumps
// its configuration version.
func (s *Store) SetHostPaused(ctx context.Context, id int64, paused bool) error {
	return s.exec1(ctx, "UPDATE hosts SET paused = $2, config_version = config_version + 1 WHERE id = $1", id, paused)
}
