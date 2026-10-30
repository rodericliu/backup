package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "0.0.0.0:8443" || cfg.AgentListen != "" || cfg.Auth.SessionTTL.Std() != 12*time.Hour ||
		cfg.Agent.HeartbeatTimeout.Std() != time.Minute || cfg.Location == nil {
		t.Errorf("defaults: %+v", cfg)
	}
}

func TestParseExample(t *testing.T) {
	cfg, err := Load("../../../deploy/server.yaml.example")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentListen != "0.0.0.0:9443" || cfg.Database.Host != "pg-host" || cfg.Database.SSLMode != "require" {
		t.Errorf("example: %+v", cfg)
	}
}

func TestPasswordFromEnv(t *testing.T) {
	t.Setenv(EnvDBPassword, "p@ss/word")
	cfg, err := Parse([]byte("database: {password: inline}"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Password != "p@ss/word" {
		t.Errorf("password = %q", cfg.Database.Password)
	}
	dsn := cfg.Database.DSN()
	want := "postgres://backup:p%40ss%2Fword@localhost:5432/backup?application_name=backup-server&sslmode=require"
	if dsn != want {
		t.Errorf("dsn = %s\nwant  %s", dsn, want)
	}
}

func TestSameListenSharesPort(t *testing.T) {
	cfg, err := Parse([]byte(`{listen: "127.0.0.1:8443", agent_listen: "127.0.0.1:8443"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentListen != "" {
		t.Errorf("agent_listen = %q, want shared", cfg.AgentListen)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct{ doc, want string }{
		{"unknown: 1", "unknown"},
		{"listen: nonsense", "listen"},
		{"tls: {cert: server.crt}", "tls.cert"},
		{"database: {sslmode: prefer}", "sslmode"},
		{"database: {port: 0}", "database.port"},
		{"auth: {session_ttl: 10s}", "session_ttl"},
		{"agent: {heartbeat_timeout: 15s}", "heartbeat_timeout"},
		{"timezone: Mars/Base", "timezone"},
		{"log: {level: trace}", "log.level"},
		{"assistant: {enabled: true, base_url: ftp://x}", "assistant.base_url"},
		{"assistant: {enabled: true, models: []}", "assistant.models"},
		{"assistant: {enabled: true, api_key_file: key}", "assistant.api_key_file"},
		{"assistant: {enabled: true, reasoning_effort: extreme}", "reasoning_effort"},
		{"assistant: {enabled: true, timeout: 1s}", "assistant.timeout"},
		{"assistant: {enabled: true, max_tool_rounds: 0}", "max_tool_rounds"},
	}
	for _, tt := range tests {
		_, err := Parse([]byte(tt.doc))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.doc, err, tt.want)
		}
	}
}

func TestAssistantAPIKey(t *testing.T) {
	cfg, err := Parse([]byte("assistant: {enabled: false, models: []}"))
	if err != nil || len(cfg.Assistant.Models) != 0 || cfg.Assistant.ReadAPIKey() != nil {
		t.Fatalf("a disabled assistant needs no models or key: %+v, %v", cfg.Assistant, err)
	}

	key := filepath.Join(t.TempDir(), "openai.key")
	if err := os.WriteFile(key, []byte("sk-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Parse([]byte("assistant: {enabled: true, api_key_file: " + key + "}"))
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Assistant
	if a.Models[0] != "gpt-6.1-sol" || a.BaseURL != "https://api.openai.com/v1" || a.MaxToolRounds != 8 {
		t.Errorf("assistant defaults = %+v", a)
	}
	if err := a.ReadAPIKey(); err != nil || a.APIKey != "sk-file" {
		t.Errorf("key from file = %q, %v", a.APIKey, err)
	}
	t.Setenv(EnvOpenAIKey, "sk-env")
	if err := a.ReadAPIKey(); err != nil || a.APIKey != "sk-env" {
		t.Errorf("key from environment = %q, %v", a.APIKey, err)
	}
	t.Setenv(EnvOpenAIKey, "")
	a.APIKeyFile = ""
	if err := a.ReadAPIKey(); err == nil {
		t.Error("no key: no error")
	}
}
