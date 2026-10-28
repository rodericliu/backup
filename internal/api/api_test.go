package api

import (
	json "encoding/json/v2"
	"strings"
	"testing"
	"time"
	"uuid"
)

func TestDurationJSON(t *testing.T) {
	type wrap struct {
		D Duration `json:"d"`
	}
	b, err := json.Marshal(wrap{D: Duration(90 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"d":"1h30m0s"}`; got != want {
		t.Errorf("marshal = %s, want %s", got, want)
	}

	var w wrap
	if err := json.Unmarshal([]byte(`{"d":"4h"}`), &w); err != nil {
		t.Fatal(err)
	}
	if w.D.Std() != 4*time.Hour {
		t.Errorf("unmarshal = %s, want 4h", w.D)
	}

	if err := json.Unmarshal([]byte(`{"d":"soon"}`), &w); err == nil {
		t.Error("unmarshal invalid duration: want error")
	}
}

func TestJobStatus(t *testing.T) {
	tests := []struct {
		s                   JobStatus
		terminal, succeeded bool
	}{
		{StatusPending, false, false},
		{StatusRunning, false, false},
		{StatusSuccess, true, true},
		{StatusWarning, true, true},
		{StatusFailed, true, false},
		{StatusSkipped, true, false},
		{StatusCanceled, true, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.s), func(t *testing.T) {
			if got := tt.s.Terminal(); got != tt.terminal {
				t.Errorf("Terminal() = %v, want %v", got, tt.terminal)
			}
			if got := tt.s.Succeeded(); got != tt.succeeded {
				t.Errorf("Succeeded() = %v, want %v", got, tt.succeeded)
			}
		})
	}
}

func TestJobJSONRoundTrip(t *testing.T) {
	start := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	in := Job{
		ID:          uuid.NewV7(),
		Project:     "gays",
		Type:        JobIncr,
		ChainID:     uuid.NewV7(),
		Trigger:     TriggerSchedule,
		Status:      StatusSuccess,
		ScheduledAt: start,
		StartedAt:   start,
		FinishedAt:  start.Add(3 * time.Minute),
		ToLSN:       123456789,
		SlaveInfo:   &SlaveInfo{MasterLogFile: "mysql-bin.000042", MasterLogPos: 4711},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "base_job_id") {
		t.Errorf("zero base_job_id should be omitted: %s", b)
	}
	var out Job
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != in.ID || out.ChainID != in.ChainID || !out.FinishedAt.Equal(in.FinishedAt) ||
		out.SlaveInfo == nil || *out.SlaveInfo != *in.SlaveInfo {
		t.Errorf("round trip mismatch:\n in=%+v\nout=%+v", in, out)
	}
	if got := out.Duration(); got != 3*time.Minute {
		t.Errorf("Duration() = %s, want 3m", got)
	}
}

func TestTemplate(t *testing.T) {
	imp := Template(TierImportant)
	if imp.FullInterval.Std() != 4*time.Hour || imp.IncrInterval.Std() != time.Hour {
		t.Errorf("important = %s/%s, want 4h/1h", imp.FullInterval, imp.IncrInterval)
	}
	norm := Template(TierNormal)
	if norm.FullInterval.Std() != 24*time.Hour || norm.HasIncr() {
		t.Errorf("normal = %s/%s, want 24h/0", norm.FullInterval, norm.IncrInterval)
	}
	if imp.RetryCount != 1 || imp.RetryInterval.Std() != 10*time.Minute {
		t.Errorf("retry = %d/%s, want 1/10m", imp.RetryCount, imp.RetryInterval)
	}
}

func validProject() ProjectConfig {
	p := Template(TierImportant)
	p.Name = "gays"
	p.Container = "gays_maindb_slave"
	p.MySQL.User = "backup"
	return p
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ProjectConfig)
		wantErr string
	}{
		{"valid", func(*ProjectConfig) {}, ""},
		{"normal tier", func(p *ProjectConfig) { *p = Template(TierNormal); p.Name, p.Container, p.MySQL.User = "x", "c", "u" }, ""},
		{"weekly full", func(p *ProjectConfig) { p.FullInterval = Duration(7 * day) }, ""},
		{"no retry", func(p *ProjectConfig) { p.RetryCount = 0; p.RetryInterval = 0 }, ""},
		{"bad name", func(p *ProjectConfig) { p.Name = "../etc" }, "name"},
		{"missing container", func(p *ProjectConfig) { p.Container = "" }, "container"},
		{"bad tier", func(p *ProjectConfig) { p.Tier = "gold" }, "tier"},
		{"full not dividing day", func(p *ProjectConfig) { p.FullInterval = Duration(5 * time.Hour) }, "must divide 24h"},
		{"full partial days", func(p *ProjectConfig) { p.FullInterval = Duration(36 * time.Hour) }, "whole number of days"},
		{"full seconds", func(p *ProjectConfig) { p.FullInterval = Duration(90 * time.Second) }, "whole number of minutes"},
		{"incr not dividing full", func(p *ProjectConfig) { p.IncrInterval = Duration(90 * time.Minute) }, "must divide full_interval"},
		{"incr equals full", func(p *ProjectConfig) { p.IncrInterval = p.FullInterval }, "shorter than"},
		{"offset too large", func(p *ProjectConfig) { p.Offset = p.FullInterval }, "offset"},
		{"negative offset", func(p *ProjectConfig) { p.Offset = Duration(-time.Minute) }, "offset"},
		{"bad incr mode", func(p *ProjectConfig) { p.IncrMode = "weird" }, "incr_mode"},
		{"retention zero", func(p *ProjectConfig) { p.RetentionDays = 0 }, "retention_days"},
		{"parallel zero", func(p *ProjectConfig) { p.Parallel = 0 }, "parallel"},
		{"timeout zero", func(p *ProjectConfig) { p.Timeout = 0 }, "timeout"},
		{"retry without interval", func(p *ProjectConfig) { p.RetryInterval = 0 }, "retry_interval"},
		{"missing mysql user", func(p *ProjectConfig) { p.MySQL.User = "" }, "mysql.user"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validProject()
			tt.mutate(&p)
			err := p.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Errorf("want error containing %q, got nil", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateReportsAllErrors(t *testing.T) {
	p := ProjectConfig{}
	err := p.Validate()
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"name", "container", "tier", "full_interval", "mysql.user"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}
