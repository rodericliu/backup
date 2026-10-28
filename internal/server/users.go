package server

import (
	"net/http"

	"github.com/rodericliu/backup/internal/crypto"
	"github.com/rodericliu/backup/internal/store"
)

// User management (PRD U-4). All users are administrators.

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) error {
	users, err := s.store.Users(r.Context())
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, users)
}

type createUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// createUser adds a user who must change the given password at first login.
func (s *Server) createUser(w http.ResponseWriter, r *http.Request) error {
	var req createUserRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if !usernameRE.MatchString(req.Username) {
		return badRequest("username must match %s", usernameRE)
	}
	if err := validatePassword(req.Password); err != nil {
		return err
	}
	hash, err := crypto.HashPassword(req.Password)
	if err != nil {
		return err
	}
	u := &store.User{Username: req.Username, PasswordHash: hash, MustChangePassword: true}
	if err := s.store.CreateUser(r.Context(), u); err != nil {
		return err
	}
	return writeJSON(w, http.StatusCreated, u)
}

type updateUserRequest struct {
	Disabled *bool `json:"disabled"`
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var req updateUserRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if req.Disabled != nil {
		if *req.Disabled && id == currentUser(r).ID {
			return badRequest("you cannot disable yourself")
		}
		if err := s.store.SetUserDisabled(r.Context(), id, *req.Disabled); err != nil {
			return err
		}
		if *req.Disabled {
			if err := s.store.DeleteUserSessions(r.Context(), id, ""); err != nil {
				return err
			}
		}
	}
	u, err := s.store.User(r.Context(), id)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, u)
}

type resetPasswordRequest struct {
	Password string `json:"password"`
}

// resetPassword sets a temporary password that must be changed at the next
// login and signs the user out everywhere.
func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var req resetPasswordRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if _, err := s.store.User(r.Context(), id); err != nil {
		return err
	}
	if err := s.setPassword(r.Context(), id, req.Password, true); err != nil {
		return err
	}
	if err := s.store.DeleteUserSessions(r.Context(), id, ""); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
