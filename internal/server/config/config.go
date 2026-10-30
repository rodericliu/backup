// Package config loads the backup-server configuration file (PRD 10.1).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/rodericliu/backup/internal/api"
)

// DefaultPath is the default location of the server configuration file.
const DefaultPath = "/etc/backup-server/server.yaml"

// EnvDBPassword overrides database.password.
const EnvDBPassword = "BACKUP_DB_PASSWORD"

// Config is the server configuration.
type Config struct {
	// Listen serves the web UI and management API.
	Listen string `yaml:"listen"`
	// AgentListen serves the agent API; empty shares Listen.
	AgentListen string          `yaml:"agent_listen"`
	TLS         TLSConfig       `yaml:"tls"`
	Database    DatabaseConfig  `yaml:"database"`
	Secrets     SecretsConfig   `yaml:"secrets"`
	Auth        AuthConfig      `yaml:"auth"`
	Agent       AgentConfig     `yaml:"agent"`
	Log         LogConfig       `yaml:"log"`
	Assistant   AssistantConfig `yaml:"assistant"`
	// Timezone is used to align predicted schedules; it must match the
	// agents' timezone. An IANA name or "Local".
	Timezone string `yaml:"timezone"`

	// Location is the parsed Timezone.
	Location *time.Location `yaml:"-"`
}

// TLSConfig is the server certificate issued by `backup-server cert issue`.
type TLSConfig struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

// DatabaseConfig locates the PostgreSQL database.
type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Name     string `yaml:"name"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	// SSLMode is disable, require or verify-full.
	SSLMode  string `yaml:"sslmode"`
	MaxConns int32  `yaml:"max_conns"`
}

// SecretsConfig locates the key that encrypts MySQL credentials.
type SecretsConfig struct {
	MasterKeyFile string `yaml:"master_key_file"`
}

// AuthConfig configures browser sessions.
type AuthConfig struct {
	SessionTTL api.Duration `yaml:"session_ttl"`
	// BootstrapAdmin is created with a random password when no user exists.
	BootstrapAdmin string `yaml:"bootstrap_admin"`
}

// AgentConfig configures agent supervision.
type AgentConfig struct {
	// HeartbeatTimeout marks a host offline (PRD H-4).
	HeartbeatTimeout api.Duration `yaml:"heartbeat_timeout"`
}

// LogConfig configures server logging.
type LogConfig struct {
	Level string `yaml:"level"`
	// Dir receives daily rotated JSON logs; empty logs to stderr.
	Dir string `yaml:"dir"`
}

// AssistantConfig configures the read-only AI assistant, which answers
// questions about backups with an OpenAI model through the Responses API.
type AssistantConfig struct {
	Enabled bool `yaml:"enabled"`
	// BaseURL is the API root, for a proxy or gateway.
	BaseURL string `yaml:"base_url"`
	// APIKeyFile holds the API key; EnvOpenAIKey overrides it.
	APIKeyFile string `yaml:"api_key_file"`
	// Models are offered in the chat window; the first is the default.
	Models []string `yaml:"models"`
	// ReasoningEffort is passed to reasoning models; empty leaves the
	// model default.
	ReasoningEffort string `yaml:"reasoning_effort"`
	// Timeout bounds one model request.
	Timeout api.Duration `yaml:"timeout"`
	// MaxToolRounds bounds the model requests of one answer.
	MaxToolRounds int `yaml:"max_tool_rounds"`

	// APIKey is read from APIKeyFile or EnvOpenAIKey by ReadAPIKey.
	APIKey string `yaml:"-"`
}

// EnvOpenAIKey overrides assistant.api_key_file.
const EnvOpenAIKey = "BACKUP_OPENAI_API_KEY"

// ReadAPIKey loads the API key of an enabled assistant.
func (a *AssistantConfig) ReadAPIKey() error {
	if !a.Enabled {
		return nil
	}
	if k, ok := os.LookupEnv(EnvOpenAIKey); ok && k != "" {
		a.APIKey = k
		return nil
	}
	if a.APIKeyFile == "" {
		return fmt.Errorf("assistant: set api_key_file or %s", EnvOpenAIKey)
	}
	b, err := os.ReadFile(a.APIKeyFile)
	if err != nil {
		return fmt.Errorf("assistant: read API key: %w", err)
	}
	a.APIKey = string(bytes.TrimSpace(b))
	if a.APIKey == "" {
		return fmt.Errorf("assistant: %s is empty", a.APIKeyFile)
	}
	return nil
}

// Default returns the configuration used for omitted fields.
func Default() Config {
	return Config{
		Listen: "0.0.0.0:8443",
		TLS: TLSConfig{
			Cert: "/etc/backup-server/tls/server.crt",
			Key:  "/etc/backup-server/tls/server.key",
		},
		Database: DatabaseConfig{Host: "localhost", Port: 5432, Name: "backup", User: "backup", SSLMode: "require", MaxConns: 10},
		Secrets:  SecretsConfig{MasterKeyFile: "/etc/backup-server/master.key"},
		Auth:     AuthConfig{SessionTTL: api.Duration(12 * time.Hour), BootstrapAdmin: "admin"},
		Agent:    AgentConfig{HeartbeatTimeout: api.Duration(60 * time.Second)},
		Log:      LogConfig{Level: "info", Dir: "/var/log/backup-server"},
		Assistant: AssistantConfig{
			BaseURL:       "https://api.openai.com/v1",
			Models:        []string{"gpt-6.1-sol"},
			Timeout:       api.Duration(3 * time.Minute),
			MaxToolRounds: 8,
		},
		Timezone: "Local",
	}
}

// Load reads and validates the configuration file at path.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// Parse decodes and validates a YAML document. The database password may be
// overridden by the BACKUP_DB_PASSWORD environment variable.
func Parse(b []byte) (*Config, error) {
	cfg := Default()
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if pw, ok := os.LookupEnv(EnvDBPassword); ok {
		cfg.Database.Password = pw
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}
	for name, addr := range map[string]string{"listen": c.Listen, "agent_listen": c.AgentListen} {
		if addr == "" && name == "agent_listen" {
			continue
		}
		if _, _, err := net.SplitHostPort(addr); err != nil {
			add("%s %q: %v", name, addr, err)
		}
	}
	if c.AgentListen == c.Listen {
		c.AgentListen = ""
	}
	for name, path := range map[string]string{
		"tls.cert":                c.TLS.Cert,
		"tls.key":                 c.TLS.Key,
		"secrets.master_key_file": c.Secrets.MasterKeyFile,
	} {
		if !filepath.IsAbs(path) {
			add("%s %q: must be an absolute path", name, path)
		}
	}
	if c.Log.Dir != "" && !filepath.IsAbs(c.Log.Dir) {
		add("log.dir %q: must be an absolute path", c.Log.Dir)
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		add("log.level %q: must be debug, info, warn or error", c.Log.Level)
	}
	d := &c.Database
	if d.Host == "" || d.Name == "" || d.User == "" {
		add("database: host, name and user are required")
	}
	if d.Port < 1 || d.Port > 65535 {
		add("database.port %d: out of range", d.Port)
	}
	switch d.SSLMode {
	case "disable", "require", "verify-full":
	default:
		add("database.sslmode %q: must be disable, require or verify-full", d.SSLMode)
	}
	if d.MaxConns < 1 {
		add("database.max_conns %d: must be at least 1", d.MaxConns)
	}
	if c.Auth.SessionTTL.Std() < time.Minute {
		add("auth.session_ttl %s: must be at least 1m", c.Auth.SessionTTL)
	}
	if c.Auth.BootstrapAdmin == "" {
		add("auth.bootstrap_admin: required")
	}
	if c.Agent.HeartbeatTimeout.Std() < 30*time.Second {
		add("agent.heartbeat_timeout %s: must be at least 30s (agents send a heartbeat every 15s)", c.Agent.HeartbeatTimeout)
	}
	if a := &c.Assistant; a.Enabled {
		if u, err := url.Parse(a.BaseURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			add("assistant.base_url %q: must be an http(s) URL", a.BaseURL)
		}
		if a.APIKeyFile != "" && !filepath.IsAbs(a.APIKeyFile) {
			add("assistant.api_key_file %q: must be an absolute path", a.APIKeyFile)
		}
		if len(a.Models) == 0 || slices.Contains(a.Models, "") {
			add("assistant.models: at least one model name is required")
		}
		switch a.ReasoningEffort {
		case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
		default:
			add("assistant.reasoning_effort %q: unknown effort", a.ReasoningEffort)
		}
		if a.Timeout.Std() < 10*time.Second {
			add("assistant.timeout %s: must be at least 10s", a.Timeout)
		}
		if a.MaxToolRounds < 1 || a.MaxToolRounds > 20 {
			add("assistant.max_tool_rounds %d: must be in [1, 20]", a.MaxToolRounds)
		}
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		add("timezone %q: %v", c.Timezone, err)
	}
	c.Location = loc
	return errors.Join(errs...)
}

// DSN returns the PostgreSQL connection URL.
func (d *DatabaseConfig) DSN() string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(d.User, d.Password),
		Host:   net.JoinHostPort(d.Host, strconv.Itoa(d.Port)),
		Path:   "/" + d.Name,
	}
	if d.Password == "" {
		u.User = url.User(d.User)
	}
	q := url.Values{}
	q.Set("sslmode", d.SSLMode)
	q.Set("application_name", "backup-server")
	u.RawQuery = q.Encode()
	return u.String()
}
