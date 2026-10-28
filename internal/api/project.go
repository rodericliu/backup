package api

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Tier selects a default parameter template for a project (PRD P-2).
type Tier string

const (
	// TierImportant takes a full backup every 4h and an incremental every hour.
	TierImportant Tier = "important"
	// TierNormal takes a daily full backup without incrementals.
	TierNormal Tier = "normal"
)

// IncrMode selects the base of incremental backups (PRD 6.4.6).
type IncrMode string

const (
	// IncrChained bases each incremental on the previous backup in the chain.
	IncrChained IncrMode = "chained"
	// IncrDifferential bases each incremental on the chain's full backup.
	IncrDifferential IncrMode = "differential"
)

const day = 24 * time.Hour

// MySQLConfig holds the backup account used inside the container.
type MySQLConfig struct {
	User     string `json:"user" yaml:"user"`
	Password string `json:"password,omitzero" yaml:"password"`
	// Socket, Host and Port are optional; the mysql client defaults apply when empty.
	Socket string `json:"socket,omitzero" yaml:"socket"`
	Host   string `json:"host,omitzero" yaml:"host"`
	Port   int    `json:"port,omitzero" yaml:"port"`
}

// ProjectConfig is the backup configuration of one MySQL replica container
// (PRD P-3). Decode user input on top of Template so that omitted fields take
// the tier defaults while explicit zero values are preserved.
type ProjectConfig struct {
	ID            int64       `json:"id,omitzero" yaml:"id"`
	Name          string      `json:"name" yaml:"name"`
	Container     string      `json:"container" yaml:"container"`
	Tier          Tier        `json:"tier" yaml:"tier"`
	FullInterval  Duration    `json:"full_interval" yaml:"full_interval"`
	IncrInterval  Duration    `json:"incr_interval" yaml:"incr_interval"`
	Offset        Duration    `json:"offset" yaml:"offset"`
	IncrMode      IncrMode    `json:"incr_mode" yaml:"incr_mode"`
	RetentionDays int         `json:"retention_days" yaml:"retention_days"`
	LagThreshold  Duration    `json:"lag_threshold" yaml:"lag_threshold"`
	Parallel      int         `json:"parallel" yaml:"parallel"`
	Throttle      int         `json:"throttle,omitzero" yaml:"throttle"`
	Timeout       Duration    `json:"timeout" yaml:"timeout"`
	RetryCount    int         `json:"retry_count" yaml:"retry_count"`
	RetryInterval Duration    `json:"retry_interval" yaml:"retry_interval"`
	Priority      int         `json:"priority" yaml:"priority"`
	Disabled      bool        `json:"disabled" yaml:"disabled"`
	MySQL         MySQLConfig `json:"mysql" yaml:"mysql"`
}

// Template returns the default parameters of a tier.
func Template(t Tier) ProjectConfig {
	p := ProjectConfig{
		Tier:          t,
		IncrMode:      IncrChained,
		RetentionDays: 7,
		LagThreshold:  Duration(10 * time.Minute),
		Parallel:      4,
		Timeout:       Duration(6 * time.Hour),
		RetryCount:    1,
		RetryInterval: Duration(10 * time.Minute),
	}
	switch t {
	case TierNormal:
		p.FullInterval = Duration(day)
	default:
		p.Tier = TierImportant
		p.FullInterval = Duration(4 * time.Hour)
		p.IncrInterval = Duration(time.Hour)
	}
	return p
}

// Enabled reports whether the project should be scheduled.
func (p *ProjectConfig) Enabled() bool { return !p.Disabled }

// HasIncr reports whether the project takes incremental backups.
func (p *ProjectConfig) HasIncr() bool { return p.IncrInterval > 0 }

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// Validate checks that the configuration is internally consistent. Full
// intervals must divide a day or be a whole number of days so that schedules
// align to midnight.
func (p *ProjectConfig) Validate() error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if !nameRE.MatchString(p.Name) {
		add("name %q: must match %s", p.Name, nameRE)
	}
	if p.Container == "" {
		add("container: required")
	}
	switch p.Tier {
	case TierImportant, TierNormal:
	default:
		add("tier %q: must be %q or %q", p.Tier, TierImportant, TierNormal)
	}

	full := p.FullInterval.Std()
	switch {
	case full < time.Minute || full%time.Minute != 0:
		add("full_interval %s: must be a positive whole number of minutes", full)
	case full <= day && day%full != 0:
		add("full_interval %s: must divide 24h", full)
	case full > day && full%day != 0:
		add("full_interval %s: must be a whole number of days", full)
	}

	if incr := p.IncrInterval.Std(); incr != 0 {
		switch {
		case incr < time.Minute || incr%time.Minute != 0:
			add("incr_interval %s: must be a positive whole number of minutes", incr)
		case incr >= full:
			add("incr_interval %s: must be shorter than full_interval %s", incr, full)
		case full%incr != 0:
			add("incr_interval %s: must divide full_interval %s", incr, full)
		}
	}

	if off := p.Offset.Std(); off < 0 || (full > 0 && off >= full) {
		add("offset %s: must be in [0, full_interval)", off)
	}
	switch p.IncrMode {
	case IncrChained, IncrDifferential:
	default:
		add("incr_mode %q: must be %q or %q", p.IncrMode, IncrChained, IncrDifferential)
	}
	if p.RetentionDays < 1 {
		add("retention_days %d: must be at least 1", p.RetentionDays)
	}
	if p.LagThreshold < 0 {
		add("lag_threshold %s: must not be negative", p.LagThreshold)
	}
	if p.Parallel < 1 {
		add("parallel %d: must be at least 1", p.Parallel)
	}
	if p.Throttle < 0 {
		add("throttle %d: must not be negative", p.Throttle)
	}
	if p.Timeout <= 0 {
		add("timeout %s: must be positive", p.Timeout)
	}
	if p.RetryCount < 0 {
		add("retry_count %d: must not be negative", p.RetryCount)
	}
	if p.RetryCount > 0 && p.RetryInterval <= 0 {
		add("retry_interval %s: must be positive when retry_count > 0", p.RetryInterval)
	}
	if p.MySQL.User == "" {
		add("mysql.user: required")
	}

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("project %q: %w", p.Name, err)
	}
	return nil
}
