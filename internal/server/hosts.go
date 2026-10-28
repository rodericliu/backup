package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rodericliu/backup/internal/api"
	"github.com/rodericliu/backup/internal/crypto"
	"github.com/rodericliu/backup/internal/store"
)

// Registration token lifetimes (PRD H-1).
const (
	defaultRegisterTTL = 24 * time.Hour
	maxRegisterTTL     = 30 * 24 * time.Hour
)

// Host statuses shown on the web (PRD H-3, H-4).
const (
	HostOnline  = "online"
	HostOffline = "offline"
	HostNever   = "never" // registered but no heartbeat yet
	HostRevoked = "revoked"
)

// Audit actions of hosts.
const (
	auditRegisterTokenCreate = "register_token_create"
	auditRegisterTokenDelete = "register_token_delete"
	auditHostRegister        = "host_register"
	auditHostRevoke          = "host_revoke"
	auditHostPause           = "host_pause"
	auditHostResume          = "host_resume"
)

// HostView is a host with its derived status.
type HostView struct {
	store.Host
	Status string `json:"status"`
	// Compatible is false when the agent speaks another API version.
	Compatible bool `json:"compatible"`
}

func (s *Server) hostView(h *store.Host) HostView {
	v := HostView{Host: *h, Compatible: h.APIVersion == 0 || h.APIVersion == api.AgentAPIVersion}
	switch {
	case !h.RevokedAt.IsZero():
		v.Status = HostRevoked
	case h.LastHeartbeatAt.IsZero():
		v.Status = HostNever
	case s.now().Sub(h.LastHeartbeatAt) > s.cfg.Agent.HeartbeatTimeout.Std():
		v.Status = HostOffline
	default:
		v.Status = HostOnline
	}
	return v
}

func (s *Server) listHosts(w http.ResponseWriter, r *http.Request) error {
	hosts, err := s.store.Hosts(r.Context())
	if err != nil {
		return err
	}
	views := make([]HostView, len(hosts))
	for i := range hosts {
		views[i] = s.hostView(&hosts[i])
	}
	return writeJSON(w, http.StatusOK, views)
}

func (s *Server) pathHost(r *http.Request) (*store.Host, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	h, err := s.store.Host(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("host")
	}
	return h, err
}

func (s *Server) getHost(w http.ResponseWriter, r *http.Request) error {
	h, err := s.pathHost(r)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, s.hostView(h))
}

func (s *Server) hostContainers(w http.ResponseWriter, r *http.Request) error {
	h, err := s.pathHost(r)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, h.Containers)
}

func (s *Server) revokeHost(w http.ResponseWriter, r *http.Request) error {
	h, err := s.pathHost(r)
	if err != nil {
		return err
	}
	if !h.RevokedAt.IsZero() {
		return conflict("host %s is already revoked", h.Name)
	}
	if err := s.store.RevokeHost(r.Context(), h.ID, s.now()); err != nil {
		return err
	}
	s.audit(r, currentUser(r), auditHostRevoke, "host", fmt.Sprint(h.ID), map[string]any{"name": h.Name})
	if h, err = s.store.Host(r.Context(), h.ID); err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, s.hostView(h))
}

type updateHostRequest struct {
	Paused *bool `json:"paused"`
}

// updateHost pauses or resumes scheduling on a host (PRD S-9).
func (s *Server) updateHost(w http.ResponseWriter, r *http.Request) error {
	h, err := s.pathHost(r)
	if err != nil {
		return err
	}
	var req updateHostRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if req.Paused != nil && *req.Paused != h.Paused {
		if err := s.store.SetHostPaused(r.Context(), h.ID, *req.Paused); err != nil {
			return err
		}
		action := auditHostResume
		if *req.Paused {
			action = auditHostPause
		}
		s.audit(r, currentUser(r), action, "host", fmt.Sprint(h.ID), map[string]any{"name": h.Name})
	}
	if h, err = s.store.Host(r.Context(), h.ID); err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, s.hostView(h))
}

type createRegisterTokenRequest struct {
	Note string       `json:"note"`
	TTL  api.Duration `json:"ttl"`
}

// RegisterTokenResponse returns a new registration token; the token itself
// is shown only once.
type RegisterTokenResponse struct {
	store.RegisterToken
	Token string `json:"token"`
}

func (s *Server) createRegisterToken(w http.ResponseWriter, r *http.Request) error {
	var req createRegisterTokenRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	ttl := req.TTL.Std()
	if ttl == 0 {
		ttl = defaultRegisterTTL
	}
	if ttl < time.Minute || ttl > maxRegisterTTL {
		return badRequest("ttl must be between 1m and %s", maxRegisterTTL)
	}
	now := s.now()
	token := crypto.NewToken(crypto.PrefixRegister)
	t := store.RegisterToken{Note: truncate(req.Note, 200), ExpiresAt: now.Add(ttl), CreatedBy: currentUser(r).ID, CreatedAt: now}
	if err := s.store.CreateRegisterToken(r.Context(), &t, crypto.HashToken(token)); err != nil {
		return err
	}
	s.audit(r, currentUser(r), auditRegisterTokenCreate, "register_token", fmt.Sprint(t.ID),
		map[string]any{"note": t.Note, "expires_at": t.ExpiresAt})
	return writeJSON(w, http.StatusCreated, RegisterTokenResponse{RegisterToken: t, Token: token})
}

func (s *Server) listRegisterTokens(w http.ResponseWriter, r *http.Request) error {
	tokens, err := s.store.RegisterTokens(r.Context(), s.now())
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, tokens)
}

func (s *Server) deleteRegisterToken(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := s.store.DeleteRegisterToken(r.Context(), id); err != nil {
		return err
	}
	s.audit(r, currentUser(r), auditRegisterTokenDelete, "register_token", fmt.Sprint(id), nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// Agent API.

const hostKey ctxKey = 100

// currentHost returns the authenticated host of an agent request.
func currentHost(r *http.Request) *store.Host {
	h, _ := r.Context().Value(hostKey).(*store.Host)
	return h
}

func errTokenInvalid() *apiError {
	return newError(http.StatusUnauthorized, api.CodeTokenInvalid, "agent token is invalid or revoked")
}

// requireAgent authenticates the agent token and checks the API version.
// Heartbeats of agents with another API version are still recorded, so the
// web can show the mismatch, before the request is rejected.
func (s *Server) requireAgent(next handlerFunc) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			return errTokenInvalid()
		}
		h, err := s.store.HostByToken(r.Context(), crypto.HashToken(token))
		if errors.Is(err, store.ErrNotFound) {
			return errTokenInvalid()
		}
		if err != nil {
			return err
		}
		if v, _ := strconv.Atoi(r.Header.Get(api.HeaderAPIVersion)); v != api.AgentAPIVersion {
			if strings.HasSuffix(r.URL.Path, "/heartbeat") {
				var hb api.Heartbeat
				if err := readJSON(r, &hb); err == nil {
					if err := s.store.RecordAPIVersion(r.Context(), h.ID, v, hb.AgentVersion, s.now()); err != nil {
						return err
					}
				}
			}
			return newError(http.StatusConflict, api.CodeAPIVersion, "agent API version %d is not supported; the server speaks version %d", v, api.AgentAPIVersion)
		}
		return next(w, r.WithContext(context.WithValue(r.Context(), hostKey, h)))
	}
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) error {
	if v, _ := strconv.Atoi(r.Header.Get(api.HeaderAPIVersion)); v != api.AgentAPIVersion {
		return newError(http.StatusConflict, api.CodeAPIVersion, "agent API version %d is not supported; the server speaks version %d", v, api.AgentAPIVersion)
	}
	var req api.RegisterRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if req.Hostname == "" || len(req.Hostname) > 253 {
		return badRequest("hostname is required")
	}
	token := crypto.NewToken(crypto.PrefixAgent)
	h, err := s.store.RegisterHost(r.Context(), &store.Registration{
		RegisterTokenHash: crypto.HashToken(req.Token),
		AgentTokenHash:    crypto.HashToken(token),
		Hostname:          req.Hostname,
		IP:                clientIP(r),
		AgentVersion:      req.AgentVersion,
		Now:               s.now(),
	})
	if errors.Is(err, store.ErrTokenInvalid) {
		s.audit(r, nil, auditHostRegister, "host", req.Hostname, map[string]any{"result": "invalid token"})
		return newError(http.StatusUnauthorized, api.CodeTokenInvalid, "%v", err)
	}
	if err != nil {
		return err
	}
	s.audit(r, nil, auditHostRegister, "host", fmt.Sprint(h.ID), map[string]any{"name": h.Name, "agent_version": req.AgentVersion})
	return writeJSON(w, http.StatusOK, api.RegisterResponse{HostID: h.ID, Name: h.Name, AgentToken: token})
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) error {
	var hb api.Heartbeat
	if err := readJSON(r, &hb); err != nil {
		return err
	}
	h := currentHost(r)
	version, err := s.store.RecordHeartbeat(r.Context(), h.ID, &store.HostReport{
		IP: clientIP(r), APIVersion: api.AgentAPIVersion, Heartbeat: &hb, At: s.now(),
	})
	if errors.Is(err, store.ErrNotFound) {
		return errTokenInvalid()
	}
	if err != nil {
		return err
	}
	if s.events.active() {
		if updated, err := s.store.Host(r.Context(), h.ID); err == nil {
			s.publish(EventHost, s.hostView(updated))
		}
	}
	return writeJSON(w, http.StatusOK, api.HeartbeatResponse{ConfigVersion: version})
}
