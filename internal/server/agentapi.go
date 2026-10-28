package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"uuid"

	"github.com/rodericliu/backup/internal/api"
	"github.com/rodericliu/backup/internal/store"
)

// maxEventsPerRequest bounds a batch of job events.
const maxEventsPerRequest = 1000

// agentConfig returns the host's projects with decrypted MySQL passwords
// (PRD 5.3.4). The version is read before the projects, so a concurrent
// change can only make the agent fetch again, never miss an update.
func (s *Server) agentConfig(w http.ResponseWriter, r *http.Request) error {
	h, err := s.store.Host(r.Context(), currentHost(r).ID)
	if err != nil {
		return err
	}
	projects, err := s.store.Projects(r.Context(), store.ProjectFilter{HostID: h.ID})
	if err != nil {
		return err
	}
	cfg := api.AgentConfig{Version: h.ConfigVersion, Paused: h.Paused, Projects: make([]api.ProjectConfig, 0, len(projects))}
	for i := range projects {
		p := projects[i].ProjectConfig
		pw, err := s.sealer.Open(projects[i].PasswordEnc, projectAAD(p.ID))
		if err != nil {
			return fmt.Errorf("decrypt MySQL password of project %d: %w", p.ID, err)
		}
		p.MySQL.Password = string(pw)
		cfg.Projects = append(cfg.Projects, p)
	}
	return writeJSON(w, http.StatusOK, cfg)
}

func pathJobID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil(), badRequest("invalid job id %q", r.PathValue("id"))
	}
	return id, nil
}

// checkJob validates a job reported by an agent.
func checkJob(id uuid.UUID, j *api.Job) error {
	if j.ID != id {
		return badRequest("job id %s does not match the path", j.ID)
	}
	if j.Project == "" || (j.Type != api.JobFull && j.Type != api.JobIncr) || j.ScheduledAt.IsZero() {
		return badRequest("job %s: project, type and scheduled_at are required", id)
	}
	switch j.Status {
	case api.StatusPending, api.StatusRunning, api.StatusSuccess, api.StatusWarning, api.StatusFailed, api.StatusSkipped, api.StatusCanceled:
	default:
		return badRequest("job %s: unknown status %q", id, j.Status)
	}
	return nil
}

func (s *Server) upsertJob(r *http.Request, j *api.Job) error {
	err := s.store.UpsertJob(r.Context(), currentHost(r).ID, j)
	if errors.Is(err, store.ErrJobHost) {
		return newError(http.StatusConflict, "JOB_HOST", "job %s belongs to another host", j.ID)
	}
	return err
}

// jobEvents records a job's state changes and progress (PRD 5.3.1). Replays
// are ignored by sequence number.
func (s *Server) jobEvents(w http.ResponseWriter, r *http.Request) error {
	id, err := pathJobID(r)
	if err != nil {
		return err
	}
	var req api.JobEvents
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if len(req.Events) > maxEventsPerRequest {
		return badRequest("at most %d events per request", maxEventsPerRequest)
	}
	if req.Job != nil {
		if err := checkJob(id, req.Job); err != nil {
			return err
		}
		if err := s.upsertJob(r, req.Job); err != nil {
			return err
		}
	} else if err := s.checkJobOwner(r, id); err != nil {
		return err
	}
	if err := s.store.AddJobEvents(r.Context(), currentHost(r).ID, id, req.Events); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// checkJobOwner returns 404 for unknown jobs and 409 for jobs of other hosts.
func (s *Server) checkJobOwner(r *http.Request, id uuid.UUID) error {
	hostID, err := s.store.JobHostID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		return notFound("job")
	}
	if err != nil {
		return err
	}
	if hostID != currentHost(r).ID {
		return newError(http.StatusConflict, "JOB_HOST", "job %s belongs to another host", id)
	}
	return nil
}

// putJob records a job's final result. It is idempotent (PRD 9).
func (s *Server) putJob(w http.ResponseWriter, r *http.Request) error {
	id, err := pathJobID(r)
	if err != nil {
		return err
	}
	var j api.Job
	if err := readJSON(r, &j); err != nil {
		return err
	}
	if err := checkJob(id, &j); err != nil {
		return err
	}
	if !j.Status.Terminal() {
		return badRequest("job %s: status %s is not final; report progress with POST /jobs/{id}/events", id, j.Status)
	}
	if err := s.upsertJob(r, &j); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// uploadJobLog stores a job's complete log (PRD J-4).
func (s *Server) uploadJobLog(w http.ResponseWriter, r *http.Request) error {
	id, err := pathJobID(r)
	if err != nil {
		return err
	}
	if err := s.checkJobOwner(r, id); err != nil {
		return err
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, api.MaxLogSize))
	if err != nil {
		return newError(http.StatusRequestEntityTooLarge, "TOO_LARGE", "log exceeds %d bytes: %v", api.MaxLogSize, err)
	}
	if err := s.store.SaveJobLog(r.Context(), id, b, s.now()); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
