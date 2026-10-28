package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/rodericliu/backup/internal/api"
	"github.com/rodericliu/backup/internal/schedule"
	"github.com/rodericliu/backup/internal/store"
)

// maxScheduleHours bounds the window of GET /schedule.
const maxScheduleHours = 7 * 24

// PredictedRun is a planned backup and its predicted execution (PRD S-10).
type PredictedRun struct {
	Project    string       `json:"project"`
	Type       api.JobType  `json:"type"`
	At         time.Time    `json:"at"`
	Start      time.Time    `json:"start,omitzero"`
	End        time.Time    `json:"end,omitzero"`
	Superseded bool         `json:"superseded"`
	Estimate   api.Duration `json:"estimate"`
}

// HourLoad is the predicted work planned in one hour (PRD S-11).
type HourLoad struct {
	Hour       time.Time    `json:"hour"`
	Total      api.Duration `json:"total"`
	Overloaded bool         `json:"overloaded"`
}

// ScheduleResponse is the response of GET /schedule.
type ScheduleResponse struct {
	HostID   int64          `json:"host_id"`
	From     time.Time      `json:"from"`
	To       time.Time      `json:"to"`
	Timezone string         `json:"timezone"`
	Runs     []PredictedRun `json:"runs"`
	Hours    []HourLoad     `json:"hours"`
	// Actual holds the jobs planned in the window, to overlay the prediction
	// with what happened (PRD S-12).
	Actual []store.Job `json:"actual"`
}

// hostSchedule predicts a host's serial execution with the same algorithm
// as the agent and the average durations of recent jobs (PRD 6.3.3).
// Parameters: host_id (required), from (RFC 3339, default the current
// hour) and hours (default 24).
func (s *Server) hostSchedule(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	hostID, err := strconv.ParseInt(q.Get("host_id"), 10, 64)
	if err != nil {
		return badRequest("host_id is required")
	}
	if _, err := s.store.Host(r.Context(), hostID); err != nil {
		return err
	}
	from, err := timeParam(q, "from")
	if err != nil {
		return err
	}
	if from.IsZero() {
		from = s.now().Truncate(time.Hour)
	}
	hours, err := intParam(q, "hours", 24)
	if err != nil {
		return err
	}
	if hours < 1 || hours > maxScheduleHours {
		return badRequest("hours must be in [1, %d]", maxScheduleHours)
	}
	to := from.Add(time.Duration(hours) * time.Hour)

	projects, err := s.store.Projects(r.Context(), store.ProjectFilter{HostID: hostID})
	if err != nil {
		return err
	}
	configs := make([]api.ProjectConfig, len(projects))
	for i := range projects {
		configs[i] = projects[i].ProjectConfig
	}
	est, err := s.estimator(r.Context(), hostID)
	if err != nil {
		return err
	}
	// Slots are planned in (from, to]; start just before from to include it.
	runs := schedule.Predict(configs, s.cfg.Location, from.Add(-time.Nanosecond), to.Add(-time.Nanosecond), est)
	resp := ScheduleResponse{
		HostID: hostID, From: from, To: to, Timezone: s.cfg.Location.String(),
		Runs: make([]PredictedRun, len(runs)), Hours: []HourLoad{},
	}
	for i, run := range runs {
		resp.Runs[i] = PredictedRun{
			Project: run.Project, Type: run.Type, At: run.At, Start: run.Start, End: run.End,
			Superseded: run.Superseded, Estimate: api.Duration(est(run.Project, run.Type)),
		}
	}
	for _, l := range schedule.HourLoads(runs) {
		resp.Hours = append(resp.Hours, HourLoad{Hour: l.Hour, Total: api.Duration(l.Total), Overloaded: l.Overloaded})
	}
	if resp.Actual, err = s.store.HostJobs(r.Context(), hostID, from, to); err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, resp)
}

// diskAlertRatio flags disks above 90% usage (PRD D-4).
const diskAlertRatio = 0.9

// ProjectHealth is the backup state of a project (PRD D-3).
type ProjectHealth struct {
	ID       int64     `json:"id"`
	Name     string    `json:"name"`
	HostID   int64     `json:"host_id"`
	HostName string    `json:"host_name"`
	Enabled  bool      `json:"enabled"`
	LastFull time.Time `json:"last_full,omitzero"`
	LastIncr time.Time `json:"last_incr,omitzero"`
	// Stale is set when no backup succeeded within two full intervals.
	Stale bool `json:"stale"`
}

// DiskView is a disk of a host (PRD D-4).
type DiskView struct {
	HostID   int64  `json:"host_id"`
	HostName string `json:"host_name"`
	api.DiskUsage
	UsedRatio float64 `json:"used_ratio"`
	Alert     bool    `json:"alert"`
}

// Dashboard is the response of GET /dashboard (PRD 6.6).
type Dashboard struct {
	Hosts          []HostView      `json:"hosts"`
	TodayFrom      time.Time       `json:"today_from"`
	Today          store.JobStats  `json:"today"`
	Projects       []ProjectHealth `json:"projects"`
	Disks          []DiskView      `json:"disks"`
	RecentFailures []store.Job     `json:"recent_failures"`
}

// recentFailures bounds the failures shown on the dashboard (PRD D-6).
const recentFailures = 10

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	now := s.now()
	hosts, err := s.store.Hosts(ctx)
	if err != nil {
		return err
	}
	d := Dashboard{Hosts: make([]HostView, 0, len(hosts)), Projects: []ProjectHealth{}, Disks: []DiskView{}}
	names := make(map[int64]string, len(hosts))
	for i := range hosts {
		h := &hosts[i]
		names[h.ID] = h.Name
		if !h.RevokedAt.IsZero() {
			continue
		}
		d.Hosts = append(d.Hosts, s.hostView(h))
		for _, disk := range h.Metrics.Disks {
			v := DiskView{HostID: h.ID, HostName: h.Name, DiskUsage: disk}
			if disk.Total > 0 {
				v.UsedRatio = float64(disk.Total-disk.Free) / float64(disk.Total)
			}
			v.Alert = v.UsedRatio > diskAlertRatio
			d.Disks = append(d.Disks, v)
		}
	}

	y, m, day := now.In(s.cfg.Location).Date()
	d.TodayFrom = time.Date(y, m, day, 0, 0, 0, 0, s.cfg.Location).UTC()
	stats, err := s.store.Stats(ctx, d.TodayFrom, now.Add(time.Second))
	if err != nil {
		return err
	}
	d.Today = *stats

	projects, err := s.store.Projects(ctx, store.ProjectFilter{})
	if err != nil {
		return err
	}
	last, err := s.store.LastSuccesses(ctx)
	if err != nil {
		return err
	}
	type key struct {
		id  int64
		typ api.JobType
	}
	lastAt := make(map[key]time.Time, len(last))
	for _, l := range last {
		lastAt[key{l.ProjectID, l.Type}] = l.At
	}
	for _, p := range projects {
		h := ProjectHealth{
			ID: p.ID, Name: p.Name, HostID: p.HostID, HostName: names[p.HostID], Enabled: p.Enabled(),
			LastFull: lastAt[key{p.ID, api.JobFull}], LastIncr: lastAt[key{p.ID, api.JobIncr}],
		}
		newest := h.LastFull
		if h.LastIncr.After(newest) {
			newest = h.LastIncr
		}
		window := 2 * p.FullInterval.Std()
		if newest.IsZero() {
			newest = p.CreatedAt // new projects get two periods for their first backup
		}
		h.Stale = p.Enabled() && now.Sub(newest) > window
		d.Projects = append(d.Projects, h)
	}

	d.RecentFailures, _, err = s.store.Jobs(ctx, store.JobFilter{
		Statuses: []api.JobStatus{api.StatusFailed},
		From:     now.Add(-7 * 24 * time.Hour),
		Page:     store.Page{Limit: recentFailures},
	})
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, d)
}
