package server

import (
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rodericliu/backup/internal/api"
	"github.com/rodericliu/backup/internal/store"
)

// Command lifetimes: commands not delivered and answered in time expire, so
// that a manual backup does not start hours later when an agent reconnects.
const (
	commandTTL        = 10 * time.Minute
	testConnectionTTL = 2 * time.Minute
	purgeTTL          = 24 * time.Hour
	defaultWait       = 30 * time.Second
)

// Audit actions of commands.
const (
	auditJobRun       = "job_run"
	auditJobCancel    = "job_cancel"
	auditProjectPurge = "project_purge"
)

// commandAAD binds encrypted command secrets to their purpose.
var commandAAD = []byte("command")

// hub wakes long polls of a host when a command is queued.
type hub struct {
	mu      sync.Mutex
	waiters map[int64]chan struct{}
}

// wait returns a channel that is closed by the next notify of the host.
func (h *hub) wait(hostID int64) <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.waiters == nil {
		h.waiters = make(map[int64]chan struct{})
	}
	ch, ok := h.waiters[hostID]
	if !ok {
		ch = make(chan struct{})
		h.waiters[hostID] = ch
	}
	return ch
}

func (h *hub) notify(hostID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.waiters[hostID]; ok {
		close(ch)
		delete(h.waiters, hostID)
	}
}

// sendCommand queues a command for a host and wakes its long poll.
func (s *Server) sendCommand(ctx context.Context, user *store.User, hostID int64, typ api.CommandType, payload any, ttl time.Duration) (*store.Command, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	now := s.now()
	c := &store.Command{HostID: hostID, Type: typ, Payload: b, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	if user != nil {
		c.CreatedBy = user.ID
	}
	if err := s.store.CreateCommand(ctx, c); err != nil {
		return nil, err
	}
	s.hub.notify(hostID)
	return s.store.Command(ctx, c.ID)
}

// pollCommands delivers a host's pending commands, waiting up to the wait
// query parameter (default 30s, at most 1m) for one to arrive.
func (s *Server) pollCommands(w http.ResponseWriter, r *http.Request) error {
	wait := defaultWait
	if v := r.URL.Query().Get("wait"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			return badRequest("wait %q: want a duration such as 30s", v)
		}
		wait = min(d, api.MaxCommandWait)
	}
	h := currentHost(r)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		// Subscribe before checking so that a command queued in between wakes us.
		woken := s.hub.wait(h.ID)
		cmds, err := s.store.TakeCommands(r.Context(), h.ID, s.now())
		if err != nil {
			return err
		}
		if len(cmds) > 0 {
			out := make([]api.Command, 0, len(cmds))
			for i := range cmds {
				c, err := s.deliverable(&cmds[i])
				if err != nil {
					return err
				}
				out = append(out, *c)
			}
			return writeJSON(w, http.StatusOK, out)
		}
		select {
		case <-woken:
		case <-timer.C:
			return writeJSON(w, http.StatusOK, []api.Command{})
		case <-r.Context().Done():
			return nil
		}
	}
}

// deliverable converts a stored command for the agent, decrypting secrets.
func (s *Server) deliverable(c *store.Command) (*api.Command, error) {
	out := &api.Command{ID: c.ID, Type: c.Type, Payload: c.Payload, CreatedAt: c.CreatedAt, ExpiresAt: c.ExpiresAt}
	if c.Type != api.CmdTestConnection {
		return out, nil
	}
	var stored struct {
		api.TestConnectionPayload
		Secret string `json:"secret_enc"`
	}
	if err := json.Unmarshal(c.Payload, &stored); err != nil {
		return nil, fmt.Errorf("decode command %d: %w", c.ID, err)
	}
	sealed, err := base64.StdEncoding.DecodeString(stored.Secret)
	if err != nil {
		return nil, fmt.Errorf("decode secret of command %d: %w", c.ID, err)
	}
	pw, err := s.sealer.Open(sealed, commandAAD)
	if err != nil {
		return nil, fmt.Errorf("decrypt secret of command %d: %w", c.ID, err)
	}
	stored.Project.MySQL.Password = string(pw)
	b, err := json.Marshal(stored.TestConnectionPayload)
	if err != nil {
		return nil, err
	}
	out.Payload = b
	return out, nil
}

// commandResult records the agent's result of a command.
func (s *Server) commandResult(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var res api.CommandResult
	if err := readJSON(r, &res); err != nil {
		return err
	}
	b, err := json.Marshal(res)
	if err != nil {
		return err
	}
	c, err := s.store.FinishCommand(r.Context(), currentHost(r).ID, id, res.OK, b, s.now())
	if errors.Is(err, store.ErrNotFound) {
		return newError(http.StatusConflict, "COMMAND_STATE", "command %d is unknown, expired or already answered", id)
	}
	if err != nil {
		return err
	}
	if c.Type == api.CmdPurge && res.OK {
		var p struct {
			ProjectID int64 `json:"project_id"`
		}
		if err := json.Unmarshal(c.Payload, &p); err != nil {
			return err
		}
		if err := s.store.MarkProjectPurged(r.Context(), p.ProjectID, s.now()); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		s.audit(r, nil, auditProjectPurge, "project", fmt.Sprint(p.ProjectID), map[string]any{"command_id": c.ID, "message": res.Message})
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// getCommand returns a command and its result, for the web to follow a
// command it sent.
func (s *Server) getCommand(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	c, err := s.store.Command(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		return notFound("command")
	}
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, c)
}

type runRequest struct {
	Type api.JobType `json:"type"`
}

// runProject queues a manual backup on the project's host (PRD P-8). The
// agent puts it in its serial queue; it never preempts the running job.
func (s *Server) runProject(w http.ResponseWriter, r *http.Request) error {
	p, h, err := s.pathProject(r)
	if err != nil {
		return err
	}
	var req runRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if req.Type != api.JobFull && req.Type != api.JobIncr {
		return badRequest("type must be %q or %q", api.JobFull, api.JobIncr)
	}
	if !p.DeletedAt.IsZero() || !h.RevokedAt.IsZero() {
		return conflict("project %s is deleted or its host is revoked", p.Name)
	}
	c, err := s.sendCommand(r.Context(), currentUser(r), h.ID, api.CmdRun, api.RunPayload{Project: p.Name, Type: req.Type}, commandTTL)
	if err != nil {
		return err
	}
	s.audit(r, currentUser(r), auditJobRun, "project", fmt.Sprint(p.ID), map[string]any{"name": p.Name, "type": req.Type, "command_id": c.ID})
	return writeJSON(w, http.StatusAccepted, c)
}

// cancelJob asks the agent to cancel a pending or running job (PRD E-11).
func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) error {
	id, err := pathJobID(r)
	if err != nil {
		return err
	}
	j, err := s.store.Job(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		return notFound("job")
	}
	if err != nil {
		return err
	}
	if j.Status.Terminal() {
		return conflict("job %s has already finished (%s)", id, j.Status)
	}
	c, err := s.sendCommand(r.Context(), currentUser(r), j.HostID, api.CmdCancel, api.JobPayload{JobID: id}, commandTTL)
	if err != nil {
		return err
	}
	s.audit(r, currentUser(r), auditJobCancel, "job", id.String(), map[string]any{"project": j.Project, "command_id": c.ID})
	return writeJSON(w, http.StatusAccepted, c)
}

// requestJobLog asks the agent to upload a job's complete log (PRD J-4).
func (s *Server) requestJobLog(w http.ResponseWriter, r *http.Request) error {
	id, err := pathJobID(r)
	if err != nil {
		return err
	}
	j, err := s.store.Job(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		return notFound("job")
	}
	if err != nil {
		return err
	}
	c, err := s.sendCommand(r.Context(), currentUser(r), j.HostID, api.CmdUploadLog, api.JobPayload{JobID: id}, commandTTL)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusAccepted, c)
}

// rescanHost asks the agent to refresh its container list (PRD H-6).
func (s *Server) rescanHost(w http.ResponseWriter, r *http.Request) error {
	h, err := s.pathHost(r)
	if err != nil {
		return err
	}
	if !h.RevokedAt.IsZero() {
		return conflict("host %s is revoked", h.Name)
	}
	c, err := s.sendCommand(r.Context(), currentUser(r), h.ID, api.CmdRescan, struct{}{}, commandTTL)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusAccepted, c)
}

// testConnection asks the agent to check a project configuration before it
// is saved (PRD P-9). An empty password uses the stored password of the
// project given by the project_id query parameter.
func (s *Server) testConnection(w http.ResponseWriter, r *http.Request) error {
	req, err := readProject(r)
	if err != nil {
		return err
	}
	h, err := s.store.Host(r.Context(), req.HostID)
	if errors.Is(err, store.ErrNotFound) {
		return badRequest("host %d not found", req.HostID)
	}
	if err != nil {
		return err
	}
	if !h.RevokedAt.IsZero() {
		return conflict("host %s is revoked", h.Name)
	}
	if req.Container == "" || req.MySQL.User == "" {
		return badRequest("container and mysql.user are required")
	}
	password := req.MySQL.Password
	if password == "" {
		if v := r.URL.Query().Get("project_id"); v != "" {
			id, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return badRequest("project_id %q: not an integer", v)
			}
			p, err := s.store.Project(r.Context(), id)
			if err != nil {
				return err
			}
			if p.HostID != h.ID {
				return badRequest("project %d is on another host", p.ID)
			}
			pw, err := s.sealer.Open(p.PasswordEnc, projectAAD(p.ID))
			if err != nil {
				return err
			}
			password = string(pw)
		}
	}
	req.MySQL.Password = ""
	payload := struct {
		api.TestConnectionPayload
		Secret string `json:"secret_enc"`
	}{
		TestConnectionPayload: api.TestConnectionPayload{Project: req.ProjectConfig},
		Secret:                base64.StdEncoding.EncodeToString(s.sealer.Seal([]byte(password), commandAAD)),
	}
	c, err := s.sendCommand(r.Context(), currentUser(r), h.ID, api.CmdTestConnection, payload, testConnectionTTL)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusAccepted, c)
}

// purgeDeleted sends purge commands for deleted projects whose files are due
// for deletion (PRD P-7). Expired purge commands are sent again.
func (s *Server) purgeDeleted(ctx context.Context) error {
	projects, err := s.store.ProjectsToPurge(ctx, s.now())
	if err != nil {
		return err
	}
	for _, p := range projects {
		payload := struct {
			api.PurgePayload
			ProjectID int64 `json:"project_id"`
		}{api.PurgePayload{Project: p.Name, Before: p.DeletedAt}, p.ID}
		c, err := s.sendCommand(ctx, nil, p.HostID, api.CmdPurge, payload, purgeTTL)
		if err != nil {
			return err
		}
		s.log.Info("purging the backups of a deleted project", "project", p.Name, "host_id", p.HostID, "command_id", c.ID)
	}
	return nil
}

// expireCommands is a background task.
func (s *Server) expireCommands(ctx context.Context) error {
	_, err := s.store.ExpireCommands(ctx, s.now())
	return err
}
