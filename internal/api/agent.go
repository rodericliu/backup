package api

import (
	"encoding/json/jsontext"
	"time"
)

// AgentAPIVersion is the version of the agent API (/api/agent/v1). The agent
// sends it in HeaderAPIVersion; the server rejects other versions with 409
// and code CodeAPIVersion but still records heartbeats so that the mismatch
// is visible on the web (PRD 9).
const AgentAPIVersion = 1

// HeaderAPIVersion carries AgentAPIVersion on every agent request.
const HeaderAPIVersion = "X-Backup-Api-Version"

// Error codes of the agent API.
const (
	CodeAPIVersion   = "API_VERSION_MISMATCH"
	CodeTokenInvalid = "TOKEN_INVALID"
)

// RegisterRequest exchanges a one-time registration token for a long-lived
// agent token (PRD H-2).
type RegisterRequest struct {
	Token        string `json:"token"`
	Hostname     string `json:"hostname"`
	AgentVersion string `json:"agent_version"`
}

// RegisterResponse returns the agent token, shown only once.
type RegisterResponse struct {
	HostID     int64  `json:"host_id"`
	Name       string `json:"name"`
	AgentToken string `json:"agent_token"`
}

// Mount is a bind mount or volume of a container.
type Mount struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// Container describes a container on a host (PRD H-6).
type Container struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Image   string   `json:"image"`
	State   string   `json:"state"`
	Running bool     `json:"running"`
	Ports   []string `json:"ports"`
	Mounts  []Mount  `json:"mounts"`
}

// DiskUsage is the usage of the filesystem holding Path.
type DiskUsage struct {
	Path string `json:"path"`
	// Role is "backup" for the backup root or "data" for a container's data
	// directory mount.
	Role  string `json:"role"`
	Total int64  `json:"total"`
	Free  int64  `json:"free"`
}

// BitmapUsage is the size of a project's changed page bitmap files (PRD R-5).
type BitmapUsage struct {
	Project   string    `json:"project"`
	Bytes     int64     `json:"bytes"`
	CheckedAt time.Time `json:"checked_at"`
}

// HostMetrics are the host measurements sent with every heartbeat.
type HostMetrics struct {
	Disks   []DiskUsage   `json:"disks"`
	Bitmaps []BitmapUsage `json:"bitmaps"`
}

// Progress is a sample of a running backup (PRD E-6). Compression runs inside
// the container, so only the size of the data file is known.
type Progress struct {
	CompressedBytes int64    `json:"compressed_bytes"`
	BytesPerSecond  float64  `json:"bytes_per_second"`
	Elapsed         Duration `json:"elapsed"`
}

// AgentState is what the agent is doing (PRD 5.3.2, D-1).
type AgentState struct {
	Current  *Job      `json:"current,omitzero"`
	Progress *Progress `json:"progress,omitzero"`
	// Queue holds the pending jobs in execution order.
	Queue  []Job `json:"queue"`
	Paused bool  `json:"paused"`
}

// Heartbeat is sent every HeartbeatInterval (PRD 5.3.1).
type Heartbeat struct {
	AgentVersion string `json:"agent_version"`
	Hostname     string `json:"hostname"`
	BackupRoot   string `json:"backup_root"`
	// ConfigVersion is the configuration version the agent applies.
	ConfigVersion int64       `json:"config_version"`
	Metrics       HostMetrics `json:"metrics"`
	Containers    []Container `json:"containers"`
	State         AgentState  `json:"state"`
}

// HeartbeatResponse tells the agent the newest configuration version; the
// agent fetches GET /config when it differs from the applied one.
type HeartbeatResponse struct {
	ConfigVersion int64 `json:"config_version"`
}

// HeartbeatInterval is how often agents send heartbeats.
const HeartbeatInterval = 15 * time.Second

// AgentConfig is the configuration of one host returned by GET /config
// (PRD 5.3.4). Projects carry their decrypted MySQL passwords.
type AgentConfig struct {
	Version int64 `json:"version"`
	// Paused stops scheduling on the host; manual jobs still run (PRD S-9).
	Paused   bool            `json:"paused"`
	Projects []ProjectConfig `json:"projects"`
}

// Job event types recorded in a job's timeline (PRD J-7).
const (
	EventStatus   = "status"
	EventProgress = "progress"
	EventCleanup  = "cleanup"
	EventMessage  = "message"
)

// JobEvent is one entry of a job's timeline. Seq is the agent's sequence
// number, unique per host, so replayed events are ignored.
type JobEvent struct {
	Seq     int64          `json:"seq"`
	At      time.Time      `json:"at"`
	Type    string         `json:"type"`
	Payload jsontext.Value `json:"payload"`
}

// JobEvents is the body of POST /jobs/{id}/events. Job, when set, is the
// job's current state; it creates the job on the server as soon as it is
// queued and never overrides a reported final result.
type JobEvents struct {
	Job    *Job       `json:"job,omitzero"`
	Events []JobEvent `json:"events"`
}

// CleanupEvent is the payload of an EventCleanup event, recorded on the full
// job of a chain removed by retention (PRD R-3).
type CleanupEvent struct {
	ChainID string    `json:"chain_id"`
	Dir     string    `json:"dir"`
	LastAt  time.Time `json:"last_at"`
	Reason  string    `json:"reason"`
}

// MaxLogSize bounds an uploaded job log (POST /jobs/{id}/log).
const MaxLogSize = 64 << 20
