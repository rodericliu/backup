package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rodericliu/backup/internal/api"
)

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	def := Default()
	if cfg.BackupRoot != def.BackupRoot || cfg.LockFile != "/run/backup-agent.lock" ||
		cfg.Zstd != def.Zstd || cfg.Log != def.Log || !cfg.LowIOPriority {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if cfg.Location == nil {
		t.Error("location not set")
	}
	if len(cfg.Projects) != 0 {
		t.Errorf("projects = %d, want 0", len(cfg.Projects))
	}
}

const sample = `
server_url: "https://backup-server.internal:9443"
backup_root: /data/backup
timezone: UTC
zstd:
  threads: 4
log:
  level: debug
  dir: ""
projects:
  - name: gays
    container: gays_maindb_slave
    offset: 3h
    priority: 4
    retry_count: 0
    mysql:
      user: backup
      password: secret
  - name: kmv2_message
    container: kmv2_message_slave
    tier: normal
    offset: 3h
    incr_mode: differential
    mysql:
      user: backup
`

func TestParseSample(t *testing.T) {
	cfg, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Zstd.Threads != 4 || cfg.Zstd.Level != 3 {
		t.Errorf("zstd = %+v, want threads 4 level 3", cfg.Zstd)
	}
	if cfg.Log.Dir != "" || cfg.Log.Level != "debug" {
		t.Errorf("log = %+v", cfg.Log)
	}
	if cfg.Location != time.UTC {
		t.Errorf("location = %v, want UTC", cfg.Location)
	}
	if len(cfg.Projects) != 2 {
		t.Fatalf("projects = %d, want 2", len(cfg.Projects))
	}

	gays := cfg.Projects[0]
	if gays.Tier != api.TierImportant || gays.FullInterval.Std() != 4*time.Hour || gays.IncrInterval.Std() != time.Hour {
		t.Errorf("gays template not applied: %+v", gays)
	}
	if gays.Offset.Std() != 3*time.Hour || gays.Priority != 4 || gays.MySQL.Password != "secret" {
		t.Errorf("gays overrides not applied: %+v", gays)
	}
	if gays.RetryCount != 0 {
		t.Errorf("explicit retry_count 0 overwritten: %d", gays.RetryCount)
	}

	msg := cfg.Projects[1]
	if msg.FullInterval.Std() != 24*time.Hour || msg.HasIncr() || msg.IncrMode != api.IncrDifferential {
		t.Errorf("kmv2_message = %+v", msg)
	}
	if msg.RetryCount != 1 {
		t.Errorf("default retry_count = %d, want 1", msg.RetryCount)
	}
}

func TestParseErrors(t *testing.T) {
	const project = `
projects:
  - name: a
    container: a_slave
    mysql: {user: backup}
`
	tests := []struct {
		name, doc, want string
	}{
		{"unknown key", "backup_rot: /x", "backup_rot"},
		{"unknown project key", "projects:\n  - name: a\n    contaner: x", "contaner"},
		{"relative path", "backup_root: data", "backup_root"},
		{"bad level", "log: {level: loud}", "log.level"},
		{"bad zstd", "zstd: {level: 30}", "zstd.level"},
		{"bad timezone", "timezone: Mars/Base", "timezone"},
		{"bad duration", "projects:\n  - name: a\n    offset: soon", "soon"},
		{"invalid project", "projects:\n  - name: a", "container"},
		{"duplicate name", project + `  - name: a
    container: b_slave
    mysql: {user: backup}
`, "duplicate name"},
		{"duplicate container", project + `  - name: b
    container: a_slave
    mysql: {user: backup}
`, "already used"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.doc))
			if err == nil {
				t.Fatalf("want error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("missing file: want error")
	}
}

func TestServerURL(t *testing.T) {
	cfg, err := Parse([]byte(`server_url: "https://backup-server.internal:9443"`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerCA != "/etc/backup-agent/tls/ca.crt" {
		t.Errorf("server_ca = %q", cfg.ServerCA)
	}
	for _, doc := range []string{
		`server_url: "http://backup-server.internal:9443"`,
		`{server_url: "https://backup-server.internal:9443", server_ca: ca.crt}`,
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: want error", doc)
		}
	}
}
