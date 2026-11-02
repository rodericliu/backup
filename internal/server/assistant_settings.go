package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/rodericliu/backup/internal/server/assistant"
	"github.com/rodericliu/backup/internal/store"
)

// The assistant API key can be set on the web. It is encrypted with the
// master key and stored in the settings table; it takes precedence over the
// environment variable and the key file of server.yaml.

const (
	auditAssistantKeyUpdate = "assistant_key_update"
	auditAssistantKeyDelete = "assistant_key_delete"

	settingAssistantKey = "assistant_api_key"
	// keySourceDatabase is the source of a key set on the web; see also
	// config.KeySourceEnv and config.KeySourceFile.
	keySourceDatabase = "database"

	maxAPIKey = 1024
	// keyTestTimeout bounds the model request of a key test.
	keyTestTimeout = 30 * time.Second
)

// assistantKeyAAD binds the encrypted key to its setting.
var assistantKeyAAD = []byte("setting:" + settingAssistantKey)

// storedAPIKey is the value of the assistant_api_key setting.
type storedAPIKey struct {
	Sealed    []byte    `json:"sealed"`
	Hint      string    `json:"hint"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// apiKeyState describes the API key in use.
type apiKeyState struct {
	// Source is keySourceDatabase, config.KeySourceEnv, config.KeySourceFile
	// or empty without a key.
	Source    string
	Hint      string
	UpdatedAt time.Time
	UpdatedBy string
	// Error explains why a key saved on the web is not used.
	Error string
}

// assistantClient returns the model client, or nil when the assistant is
// disabled or has no API key.
func (s *Server) assistantClient() *assistant.Client {
	s.aiMu.RLock()
	defer s.aiMu.RUnlock()
	return s.ai
}

func (s *Server) newAssistantClient(key string) *assistant.Client {
	a := &s.cfg.Assistant
	return &assistant.Client{BaseURL: a.BaseURL, APIKey: key, ReasoningEffort: a.ReasoningEffort, HTTP: &http.Client{}}
}

// setAssistant switches to key; an empty key or a disabled assistant leaves
// no client.
func (s *Server) setAssistant(key string, st apiKeyState) {
	var c *assistant.Client
	if key != "" && s.cfg.Assistant.Enabled {
		c = s.newAssistantClient(key)
	}
	s.aiMu.Lock()
	defer s.aiMu.Unlock()
	s.ai, s.aiKey = c, st
}

// loadAssistant selects the API key: the one set on the web, else the
// environment variable or key file of server.yaml. A missing key leaves the
// assistant hidden instead of failing.
func (s *Server) loadAssistant(ctx context.Context) error {
	var st apiKeyState
	var stored storedAPIKey
	err := s.store.Setting(ctx, settingAssistantKey, &stored)
	switch {
	case err == nil:
		key, err := s.sealer.Open(stored.Sealed, assistantKeyAAD)
		if err == nil {
			s.setAssistant(string(key), apiKeyState{Source: keySourceDatabase, Hint: stored.Hint, UpdatedAt: stored.UpdatedAt, UpdatedBy: stored.UpdatedBy})
			return nil
		}
		s.log.Error("cannot decrypt the assistant API key set on the web; was master.key replaced? Set the key again", "err", err)
		st.Error = "the saved API key cannot be decrypted with the current master key; set it again"
	case !errors.Is(err, store.ErrNotFound):
		return err
	}

	a := &s.cfg.Assistant
	if err := a.ReadAPIKey(); err != nil {
		s.log.Warn("assistant API key from server.yaml", "err", err)
	}
	if a.APIKey != "" {
		st.Source, st.Hint = a.APIKeySource, keyHint(a.APIKey)
	} else if a.Enabled {
		s.log.Warn("the assistant is enabled but has no API key; set it on the web under Settings")
	}
	s.setAssistant(a.APIKey, st)
	return nil
}

// keyHint shows the last characters of a long enough key.
func keyHint(key string) string {
	if len(key) < 12 {
		return "****"
	}
	return "…" + key[len(key)-4:]
}

// AssistantSettings is the response of the /settings/assistant endpoints.
// The API key itself is never returned.
type AssistantSettings struct {
	// Enabled is assistant.enabled of server.yaml.
	Enabled bool `json:"enabled"`
	// Active is true when the assistant is shown: enabled with a key.
	Active bool `json:"active"`
	// Source is database, env, file or empty without a key.
	Source    string    `json:"source"`
	Hint      string    `json:"hint,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	UpdatedBy string    `json:"updated_by,omitempty"`
	Error     string    `json:"error,omitempty"`
	BaseURL   string    `json:"base_url"`
	Models    []string  `json:"models"`
}

func (s *Server) assistantSettings() AssistantSettings {
	s.aiMu.RLock()
	defer s.aiMu.RUnlock()
	a := &s.cfg.Assistant
	k := s.aiKey
	return AssistantSettings{
		Enabled: a.Enabled, Active: s.ai != nil, Source: k.Source, Hint: k.Hint, UpdatedAt: k.UpdatedAt, UpdatedBy: k.UpdatedBy,
		Error: k.Error, BaseURL: a.BaseURL, Models: append([]string{}, a.Models...),
	}
}

func (s *Server) getAssistantSettings(w http.ResponseWriter, r *http.Request) error {
	return writeJSON(w, http.StatusOK, s.assistantSettings())
}

type apiKeyRequest struct {
	APIKey string `json:"api_key"`
}

// cleanAPIKey trims and checks a key entered on the web.
func cleanAPIKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	switch {
	case key == "":
		return "", badRequest("api_key is required")
	case len(key) > maxAPIKey:
		return "", badRequest("api_key is longer than %d bytes", maxAPIKey)
	case strings.ContainsFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r > unicode.MaxASCII }):
		return "", badRequest("api_key must be printable ASCII without spaces")
	}
	return key, nil
}

func (s *Server) putAssistantKey(w http.ResponseWriter, r *http.Request) error {
	var req apiKeyRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	key, err := cleanAPIKey(req.APIKey)
	if err != nil {
		return err
	}
	u := currentUser(r)
	stored := storedAPIKey{
		Sealed: s.sealer.Seal([]byte(key), assistantKeyAAD), Hint: keyHint(key), UpdatedAt: s.now(), UpdatedBy: u.Username,
	}
	if err := s.store.SetSetting(r.Context(), settingAssistantKey, stored); err != nil {
		return err
	}
	s.setAssistant(key, apiKeyState{Source: keySourceDatabase, Hint: stored.Hint, UpdatedAt: stored.UpdatedAt, UpdatedBy: stored.UpdatedBy})
	s.audit(r, u, auditAssistantKeyUpdate, "setting", settingAssistantKey, map[string]any{"hint": stored.Hint})
	return writeJSON(w, http.StatusOK, s.assistantSettings())
}

// deleteAssistantKey removes the key set on the web; the key of server.yaml,
// if any, applies again.
func (s *Server) deleteAssistantKey(w http.ResponseWriter, r *http.Request) error {
	if err := s.store.DeleteSetting(r.Context(), settingAssistantKey); err != nil {
		return err
	}
	if err := s.loadAssistant(r.Context()); err != nil {
		return err
	}
	s.audit(r, currentUser(r), auditAssistantKeyDelete, "setting", settingAssistantKey, nil)
	return writeJSON(w, http.StatusOK, s.assistantSettings())
}

type keyTestRequest struct {
	// APIKey is tested instead of the current key when set.
	APIKey string `json:"api_key"`
	Model  string `json:"model"`
}

// KeyTestResult is the response of POST /settings/assistant/test.
type KeyTestResult struct {
	OK        bool   `json:"ok"`
	Model     string `json:"model"`
	Error     string `json:"error,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
}

// testAssistantKey sends a minimal request to the first configured model to
// check the key, the base URL and the network path.
func (s *Server) testAssistantKey(w http.ResponseWriter, r *http.Request) error {
	var req keyTestRequest
	if r.ContentLength != 0 {
		if err := readJSON(r, &req); err != nil {
			return err
		}
	}
	var c *assistant.Client
	if strings.TrimSpace(req.APIKey) != "" {
		key, err := cleanAPIKey(req.APIKey)
		if err != nil {
			return err
		}
		c = s.newAssistantClient(key)
	} else if c = s.assistantClient(); c == nil {
		return badRequest("no API key to test")
	}
	models := s.cfg.Assistant.Models
	if req.Model == "" && len(models) > 0 {
		req.Model = models[0]
	}
	if req.Model == "" {
		return badRequest("no model configured")
	}
	timeout := min(s.cfg.Assistant.Timeout.Std(), keyTestTimeout)
	if timeout <= 0 {
		timeout = keyTestTimeout
	}
	start := time.Now()
	_, err := c.Run(r.Context(), assistant.Request{
		Model: req.Model, Instructions: "Reply with OK.", Input: []assistant.Item{assistant.UserMessage("ping")},
		MaxRounds: 1, Timeout: timeout,
	}, func(assistant.Event) {})
	res := KeyTestResult{OK: err == nil, Model: req.Model, LatencyMS: time.Since(start).Milliseconds()}
	if err != nil {
		res.Error = err.Error()
	}
	return writeJSON(w, http.StatusOK, res)
}
