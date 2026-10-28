package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"

	json "encoding/json/v2"

	"github.com/rodericliu/backup/internal/api"
	"github.com/rodericliu/backup/internal/schedule"
	"github.com/rodericliu/backup/internal/store"
)

// Audit actions of projects.
const (
	auditProjectCreate  = "project_create"
	auditProjectUpdate  = "project_update"
	auditProjectEnable  = "project_enable"
	auditProjectDisable = "project_disable"
	auditProjectDelete  = "project_delete"
)

// ProjectView is a project as shown on the web.
type ProjectView struct {
	store.Project
	HostName string `json:"host_name"`
	// Applied reports whether the host's agent runs this configuration (PRD P-10).
	Applied bool `json:"applied"`
}

func projectView(p *store.Project, h *store.Host) ProjectView {
	return ProjectView{Project: *p, HostName: h.Name, Applied: h.AppliedConfigVersion >= p.ConfigVersion}
}

// projectRequest is the body of create, update and offset recommendation
// requests. Omitted parameters take the defaults of the tier (PRD P-2).
type projectRequest struct {
	HostID int64 `json:"host_id"`
	api.ProjectConfig
}

func readProject(r *http.Request) (*projectRequest, error) {
	b, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBody))
	if err != nil {
		return nil, badRequest("read body: %v", err)
	}
	var head struct {
		Tier api.Tier `json:"tier"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return nil, badRequest("invalid JSON: %v", err)
	}
	req := &projectRequest{ProjectConfig: api.Template(api.TierImportant)}
	if head.Tier != "" {
		req.ProjectConfig = api.Template(head.Tier)
	}
	if err := json.Unmarshal(b, req, json.RejectUnknownMembers(true)); err != nil {
		return nil, badRequest("invalid JSON: %v", err)
	}
	req.ID = 0
	return req, nil
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	f := store.ProjectFilter{IncludeDeleted: q.Get("include_deleted") == "true"}
	if v := q.Get("host_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return badRequest("host_id %q: not an integer", v)
		}
		f.HostID = id
	}
	projects, err := s.store.Projects(r.Context(), f)
	if err != nil {
		return err
	}
	hosts, err := s.hostsByID(r)
	if err != nil {
		return err
	}
	views := make([]ProjectView, 0, len(projects))
	for i := range projects {
		if h, ok := hosts[projects[i].HostID]; ok {
			views = append(views, projectView(&projects[i], h))
		}
	}
	return writeJSON(w, http.StatusOK, views)
}

func (s *Server) hostsByID(r *http.Request) (map[int64]*store.Host, error) {
	hosts, err := s.store.Hosts(r.Context())
	if err != nil {
		return nil, err
	}
	m := make(map[int64]*store.Host, len(hosts))
	for i := range hosts {
		m[hosts[i].ID] = &hosts[i]
	}
	return m, nil
}

// pathProject returns the project of the {id} path parameter and its host.
func (s *Server) pathProject(r *http.Request) (*store.Project, *store.Host, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, nil, err
	}
	p, err := s.store.Project(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, notFound("project")
	}
	if err != nil {
		return nil, nil, err
	}
	h, err := s.store.Host(r.Context(), p.HostID)
	if err != nil {
		return nil, nil, err
	}
	return p, h, nil
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) error {
	p, h, err := s.pathProject(r)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, projectView(p, h))
}

// checkProject validates a project for a host: the configuration must be
// consistent and the container must be one the agent reported (PRD P-1).
func (s *Server) checkProject(p *api.ProjectConfig, h *store.Host) error {
	if !h.RevokedAt.IsZero() {
		return badRequest("host %s is revoked", h.Name)
	}
	if err := p.Validate(); err != nil {
		return newError(http.StatusBadRequest, "INVALID_PROJECT", "%v", err)
	}
	if len(h.Containers) > 0 && !slices.ContainsFunc(h.Containers, func(c api.Container) bool { return c.Name == p.Container }) {
		return newError(http.StatusBadRequest, "INVALID_PROJECT", "container %q is not on host %s", p.Container, h.Name)
	}
	return nil
}

// sealPassword returns a store.Sealer encrypting password bound to the
// project ID.
func (s *Server) sealPassword(password string) store.Sealer {
	return func(id int64) []byte { return s.sealer.Seal([]byte(password), projectAAD(id)) }
}

func projectAAD(id int64) []byte { return fmt.Appendf(nil, "project:%d", id) }

// auditDetail describes a project in audit logs, without the password.
func auditDetail(p *store.Project) map[string]any {
	return map[string]any{
		"name": p.Name, "host_id": p.HostID, "container": p.Container, "tier": p.Tier,
		"full_interval": p.FullInterval.String(), "incr_interval": p.IncrInterval.String(), "offset": p.Offset.String(),
		"incr_mode": p.IncrMode, "retention_days": p.RetentionDays, "enabled": p.Enabled(),
	}
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) error {
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
	if err := s.checkProject(&req.ProjectConfig, h); err != nil {
		return err
	}
	if req.MySQL.Password == "" {
		return newError(http.StatusBadRequest, "INVALID_PROJECT", "mysql.password: required")
	}
	p := &store.Project{ProjectConfig: req.ProjectConfig, HostID: h.ID}
	password := p.MySQL.Password
	p.MySQL.Password = ""
	if err := s.store.CreateProject(r.Context(), p, s.sealPassword(password)); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return conflict("host %s already has a project named %q or using container %q", h.Name, p.Name, p.Container)
		}
		return err
	}
	s.audit(r, currentUser(r), auditProjectCreate, "project", fmt.Sprint(p.ID), auditDetail(p))
	h.ConfigVersion = p.ConfigVersion
	return writeJSON(w, http.StatusCreated, projectView(p, h))
}

// updateProject replaces a project's configuration. The name identifies the
// project's backups on the agent and cannot change; an empty password keeps
// the stored one.
func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) error {
	old, h, err := s.pathProject(r)
	if err != nil {
		return err
	}
	if !old.DeletedAt.IsZero() {
		return conflict("project %s is deleted", old.Name)
	}
	req, err := readProject(r)
	if err != nil {
		return err
	}
	if req.Name != old.Name {
		return badRequest("the project name cannot change")
	}
	if req.HostID != 0 && req.HostID != old.HostID {
		return badRequest("a project cannot move to another host")
	}
	if err := s.checkProject(&req.ProjectConfig, h); err != nil {
		return err
	}
	p := &store.Project{ProjectConfig: req.ProjectConfig, HostID: old.HostID}
	p.ID = old.ID
	var seal store.Sealer
	if p.MySQL.Password != "" {
		seal = s.sealPassword(p.MySQL.Password)
		p.MySQL.Password = ""
	}
	if err := s.store.UpdateProject(r.Context(), p, seal); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return conflict("host %s already has a project using container %q", h.Name, p.Container)
		}
		return err
	}
	detail := auditDetail(p)
	detail["password_changed"] = seal != nil
	s.audit(r, currentUser(r), auditProjectUpdate, "project", fmt.Sprint(p.ID), detail)
	h.ConfigVersion = p.ConfigVersion
	return writeJSON(w, http.StatusOK, projectView(p, h))
}

type patchProjectRequest struct {
	Enabled *bool `json:"enabled"`
}

// patchProject enables or disables a project (PRD P-6).
func (s *Server) patchProject(w http.ResponseWriter, r *http.Request) error {
	p, _, err := s.pathProject(r)
	if err != nil {
		return err
	}
	if !p.DeletedAt.IsZero() {
		return conflict("project %s is deleted", p.Name)
	}
	var req patchProjectRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if req.Enabled != nil && *req.Enabled != p.Enabled() {
		if err := s.store.SetProjectEnabled(r.Context(), p.ID, p.HostID, *req.Enabled); err != nil {
			return err
		}
		action := auditProjectDisable
		if *req.Enabled {
			action = auditProjectEnable
		}
		s.audit(r, currentUser(r), action, "project", fmt.Sprint(p.ID), map[string]any{"name": p.Name})
	}
	p, h, err := s.pathProject(r)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, projectView(p, h))
}

// deleteProject stops scheduling a project. files=keep keeps the backups
// until their retention ends; files=purge deletes them now (PRD P-7).
func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) error {
	p, _, err := s.pathProject(r)
	if err != nil {
		return err
	}
	if !p.DeletedAt.IsZero() {
		return conflict("project %s is already deleted", p.Name)
	}
	policy := r.URL.Query().Get("files")
	if policy != store.DeleteKeep && policy != store.DeletePurge {
		return badRequest("files must be %q or %q", store.DeleteKeep, store.DeletePurge)
	}
	if err := s.store.DeleteProject(r.Context(), p.ID, p.HostID, policy, s.now()); err != nil {
		return err
	}
	s.audit(r, currentUser(r), auditProjectDelete, "project", fmt.Sprint(p.ID), map[string]any{"name": p.Name, "files": policy})
	if policy == store.DeletePurge {
		if err := s.purgeDeleted(r.Context()); err != nil {
			return err
		}
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) projectTemplates(w http.ResponseWriter, r *http.Request) error {
	return writeJSON(w, http.StatusOK, map[api.Tier]api.ProjectConfig{
		api.TierImportant: api.Template(api.TierImportant),
		api.TierNormal:    api.Template(api.TierNormal),
	})
}

// OffsetRecommendation is the response of POST /projects/recommend-offset.
type OffsetRecommendation struct {
	Offset     api.Duration           `json:"offset"`
	Candidates []schedule.OffsetScore `json:"candidates"`
}

// recommendOffset predicts the host's load for every offset of the project
// in the body and recommends the least loaded one (PRD P-5).
func (s *Server) recommendOffset(w http.ResponseWriter, r *http.Request) error {
	req, err := readProject(r)
	if err != nil {
		return err
	}
	// Only the schedule matters; fill in what the form may not have yet.
	check := req.ProjectConfig
	check.Name, check.Container, check.MySQL.User, check.Offset = "candidate", "candidate", "candidate", 0
	if err := check.Validate(); err != nil {
		return newError(http.StatusBadRequest, "INVALID_PROJECT", "%v", err)
	}
	projects, err := s.store.Projects(r.Context(), store.ProjectFilter{HostID: req.HostID})
	if err != nil {
		return err
	}
	others := make([]api.ProjectConfig, len(projects))
	for i := range projects {
		others[i] = projects[i].ProjectConfig
	}
	est, err := s.estimator(r.Context(), req.HostID)
	if err != nil {
		return err
	}
	best, scores := schedule.RecommendOffset(others, req.ProjectConfig, s.cfg.Location, s.now(), est)
	return writeJSON(w, http.StatusOK, OffsetRecommendation{Offset: best, Candidates: scores})
}
