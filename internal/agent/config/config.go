// Package config loads the backup-agent configuration file (PRD 10.2).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/rodericliu/backup/internal/api"
)

// DefaultPath is the default location of the agent configuration file.
const DefaultPath = "/etc/backup-agent/agent.yaml"

// Config is the agent configuration.
type Config struct {
	ServerURL         string `yaml:"server_url"`
	ServerCA          string `yaml:"server_ca"`
	RegisterTokenFile string `yaml:"register_token_file"`
	StateDir          string `yaml:"state_dir"`
	BackupRoot        string `yaml:"backup_root"`
	LockFile          string `yaml:"lock_file"`
	// Timezone aligns schedules to local midnight; an IANA name or "Local".
	Timezone string `yaml:"timezone"`
	// Podman is the podman binary.
	Podman string `yaml:"podman"`
	// LowIOPriority runs xtrabackup and zstd with idle IO and low CPU priority.
	LowIOPriority bool `yaml:"low_io_priority"`
	// BitmapAlertSize is the changed page bitmap size, in bytes, above which a
	// warning is logged (PRD R-5).
	BitmapAlertSize int64      `yaml:"bitmap_alert_size"`
	Zstd            ZstdConfig `yaml:"zstd"`
	Log             LogConfig  `yaml:"log"`

	// Projects are the locally configured projects, used when the agent runs
	// without a server.
	Projects []api.ProjectConfig `yaml:"-"`
	// Location is the parsed Timezone.
	Location *time.Location `yaml:"-"`
}

// ZstdConfig configures compression, which runs inside the container.
type ZstdConfig struct {
	Threads int `yaml:"threads"`
	Level   int `yaml:"level"`
}

// LogConfig configures agent logging.
type LogConfig struct {
	Level string `yaml:"level"`
	// Dir receives daily rotated JSON logs; empty logs to stderr.
	Dir string `yaml:"dir"`
}

// Default returns the configuration used for omitted fields.
func Default() Config {
	return Config{
		RegisterTokenFile: "/etc/backup-agent/register.token",
		StateDir:          "/var/lib/backup-agent",
		BackupRoot:        "/data/backup",
		LockFile:          "/run/backup-agent.lock",
		Timezone:          "Local",
		Podman:            "podman",
		LowIOPriority:     true,
		BitmapAlertSize:   10 << 30,
		Zstd:              ZstdConfig{Threads: 8, Level: 3},
		Log:               LogConfig{Level: "info", Dir: "/var/log/backup-agent"},
	}
}

// file mirrors the YAML document. Projects are decoded one by one on top of
// their tier template so that omitted fields take the template defaults.
type file struct {
	Config   `yaml:",inline"`
	Projects []yaml.Node `yaml:"projects"`
}

// strictFile is used only to reject unknown keys anywhere in the document.
type strictFile struct {
	Config   `yaml:",inline"`
	Projects []api.ProjectConfig `yaml:"projects"`
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

// Parse decodes and validates a YAML configuration document.
func Parse(b []byte) (*Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var strict strictFile
	if err := dec.Decode(&strict); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode: %w", err)
	}

	f := file{Config: Default()}
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	cfg := f.Config
	for i := range f.Projects {
		p, err := decodeProject(&f.Projects[i])
		if err != nil {
			return nil, fmt.Errorf("projects[%d]: %w", i, err)
		}
		cfg.Projects = append(cfg.Projects, p)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func decodeProject(n *yaml.Node) (api.ProjectConfig, error) {
	var head struct {
		Tier api.Tier `yaml:"tier"`
	}
	if err := n.Decode(&head); err != nil {
		return api.ProjectConfig{}, err
	}
	p := api.Template(api.TierImportant)
	if head.Tier != "" {
		p = api.Template(head.Tier)
	}
	if err := n.Decode(&p); err != nil {
		return api.ProjectConfig{}, err
	}
	return p, nil
}

func (c *Config) validate() error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	for name, path := range map[string]string{
		"state_dir":   c.StateDir,
		"backup_root": c.BackupRoot,
		"lock_file":   c.LockFile,
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
	if c.Zstd.Level < 1 || c.Zstd.Level > 19 {
		add("zstd.level %d: must be in [1, 19]", c.Zstd.Level)
	}
	if c.Zstd.Threads < 0 {
		add("zstd.threads %d: must not be negative", c.Zstd.Threads)
	}
	if c.Podman == "" {
		add("podman: required")
	}
	if c.BitmapAlertSize < 0 {
		add("bitmap_alert_size %d: must not be negative", c.BitmapAlertSize)
	}

	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		add("timezone %q: %v", c.Timezone, err)
	}
	c.Location = loc

	names := make(map[string]bool)
	containers := make(map[string]string)
	for i := range c.Projects {
		p := &c.Projects[i]
		if err := p.Validate(); err != nil {
			errs = append(errs, err)
		}
		if names[p.Name] {
			add("project %q: duplicate name", p.Name)
		}
		names[p.Name] = true
		if other, ok := containers[p.Container]; ok {
			add("project %q: container %q already used by %q", p.Name, p.Container, other)
		}
		containers[p.Container] = p.Name
	}
	return errors.Join(errs...)
}
