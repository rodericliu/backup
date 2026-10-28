package api

import (
	"encoding/json/jsontext"
	"time"
	"uuid"
)

// CommandType is a command sent from the server to an agent (PRD 5.3.1).
type CommandType string

const (
	// CmdRun queues a manual backup (PRD P-8); payload RunPayload, result RunResult.
	CmdRun CommandType = "run"
	// CmdCancel cancels a pending or running job (PRD E-11); payload JobPayload.
	CmdCancel CommandType = "cancel"
	// CmdUploadLog uploads a job's complete log (PRD J-4); payload JobPayload.
	CmdUploadLog CommandType = "upload_log"
	// CmdRescan refreshes the container list sent with heartbeats (PRD H-6).
	CmdRescan CommandType = "rescan"
	// CmdTestConnection checks a project's container, account and replication
	// (PRD P-9); payload TestConnectionPayload, result TestConnectionResult.
	CmdTestConnection CommandType = "test_connection"
	// CmdPurge deletes the backups of a deleted project (PRD P-7); payload
	// PurgePayload.
	CmdPurge CommandType = "purge"
)

// Command is delivered by GET /commands?wait=30s.
type Command struct {
	ID        int64          `json:"id"`
	Type      CommandType    `json:"type"`
	Payload   jsontext.Value `json:"payload"`
	CreatedAt time.Time      `json:"created_at"`
	ExpiresAt time.Time      `json:"expires_at"`
}

// RunPayload asks for a manual backup of a project.
type RunPayload struct {
	Project string  `json:"project"`
	Type    JobType `json:"type"`
}

// RunResult identifies the queued job.
type RunResult struct {
	JobID uuid.UUID `json:"job_id"`
}

// JobPayload identifies a job.
type JobPayload struct {
	JobID uuid.UUID `json:"job_id"`
}

// TestConnectionPayload is the configuration to test, including the MySQL
// password.
type TestConnectionPayload struct {
	Project ProjectConfig `json:"project"`
}

// Check is one step of a connection test.
type Check struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitzero"`
}

// TestConnectionResult lists the checks performed.
type TestConnectionResult struct {
	Checks []Check `json:"checks"`
}

// PurgePayload deletes the chains of a project that started before Before,
// so that a new project reusing the name keeps its backups.
type PurgePayload struct {
	Project string    `json:"project"`
	Before  time.Time `json:"before"`
}

// CommandResult is the body of POST /commands/{id}/result.
type CommandResult struct {
	OK      bool           `json:"ok"`
	Message string         `json:"message,omitzero"`
	Data    jsontext.Value `json:"data,omitzero"`
}

// MaxCommandWait bounds the wait parameter of GET /commands.
const MaxCommandWait = time.Minute
