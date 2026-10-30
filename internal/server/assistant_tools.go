package server

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"uuid"

	"github.com/rodericliu/backup/internal/api"
	"github.com/rodericliu/backup/internal/server/assistant"
	"github.com/rodericliu/backup/internal/store"
)

// The assistant's tools only read. They return compact views with local
// times and human-readable sizes, and never include credentials.

// Limits of tool results.
const (
	aiDefaultJobs = 20
	aiMaxJobs     = 50
	aiFailures    = 20
	aiLogTail     = 3000
)

// Time periods understood by the tools.
var aiPeriods = []string{"today", "yesterday", "this_week", "last_week", "last_7_days", "last_30_days", "this_month", "last_month"}

func (s *Server) assistantTools() []assistant.Tool {
	rangeProps := map[string]any{
		"period": map[string]any{"type": "string", "enum": aiPeriods,
			"description": "相对时间段（按平台时区，周一为一周的第一天）。与 from/to 二选一。"},
		"from": map[string]any{"type": "string", "description": "开始时间（含），RFC 3339 或 YYYY-MM-DD"},
		"to":   map[string]any{"type": "string", "description": "结束时间（不含），RFC 3339 或 YYYY-MM-DD"},
	}
	projectProps := map[string]any{
		"project":    map[string]any{"type": "string", "description": "项目名或容器名的关键字，例如 fetisch"},
		"project_id": map[string]any{"type": "integer", "description": "find_projects 返回的项目 ID，优先于 project"},
		"host":       map[string]any{"type": "string", "description": "主机名或关键字，例如 backup-esh-01"},
	}
	obj := func(props ...map[string]any) map[string]any {
		all := map[string]any{}
		for _, p := range props {
			maps.Copy(all, p)
		}
		return map[string]any{"type": "object", "properties": all, "additionalProperties": false}
	}
	statusEnum := map[string]any{"type": "array", "items": map[string]any{"type": "string",
		"enum": []string{"pending", "running", "success", "warning", "failed", "skipped", "canceled"}}}

	return []assistant.Tool{
		{
			Name:        "list_hosts",
			Description: "列出所有备份主机：在线状态、最后心跳、是否暂停调度、备份根目录、磁盘使用率、当前任务与队列长度。",
			Parameters:  obj(),
			Call:        aiCall(s.aiListHosts),
		},
		{
			Name:        "get_host",
			Description: "查看一台主机的详情：容器（镜像、状态、挂载，可据此找到 MySQL 数据目录 /var/lib/mysql 对应的宿主机路径）、磁盘、changed page bitmap、当前任务、队列和其上的项目。",
			Parameters: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"host"},
				"properties": map[string]any{"host": projectProps["host"]}},
			Call: aiCall(s.aiGetHost),
		},
		{
			Name: "find_projects",
			Description: "按关键字模糊查找备份项目（一个项目是一台主机上的一个 MySQL 从库容器），返回所在主机、容器、排班、保留期、启用状态和最近一次成功的全量/增量。" +
				"用户提到的名字可能不完整，先用它确定项目。",
			Parameters: obj(map[string]any{
				"query":           map[string]any{"type": "string", "description": "关键字，匹配项目名和容器名；为空返回全部"},
				"host":            projectProps["host"],
				"include_deleted": map[string]any{"type": "boolean", "description": "是否包含已删除的项目"},
			}),
			Call: aiCall(s.aiFindProjects),
		},
		{
			Name:        "list_jobs",
			Description: "按项目、主机、时间段（按计划时间）、状态、类型查询备份任务，返回状态、开始结束时间、耗时、大小、错误和备份文件路径，最新的在前。",
			Parameters: obj(projectProps, rangeProps, map[string]any{
				"status": statusEnum,
				"type":   map[string]any{"type": "string", "enum": []string{"full", "incr"}},
				"limit":  map[string]any{"type": "integer", "description": fmt.Sprintf("最多返回条数，默认 %d，最大 %d", aiDefaultJobs, aiMaxJobs)},
			}),
			Call: aiCall(s.aiListJobs),
		},
		{
			Name: "backup_summary",
			Description: "统计一个时间段（按计划时间）内的备份情况：总数和按项目分的成功/警告/失败/跳过次数、备份量、平均耗时，最近的失败及原因，以及超过两个全量周期没有成功备份的项目。" +
				"适合“本周备份情况”这类总结；默认今天。",
			Parameters: obj(rangeProps, map[string]any{"host": projectProps["host"], "project": projectProps["project"]}),
			Call:       aiCall(s.aiBackupSummary),
		},
		{
			Name:        "get_job",
			Description: "查看一个备份任务的详情：备份文件路径和所在备份链目录、LSN、主库 binlog 位点（slave info）、所基于的备份、错误和日志末尾。",
			Parameters: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"job_id"},
				"properties": map[string]any{"job_id": map[string]any{"type": "string", "description": "任务 ID（UUID）"}}},
			Call: aiCall(s.aiGetJob),
		},
		{
			Name:        "latest_backups",
			Description: "查看一个项目最近一次成功的全量、最近一次成功的增量、最近一个任务和最新的可恢复时间点，包含备份文件在服务器上的路径。",
			Parameters:  obj(projectProps),
			Call:        aiCall(s.aiLatestBackups),
		},
		{
			Name: "restore_plan",
			Description: "生成恢复某个备份点的完整操作步骤：需要按顺序应用的备份、恢复所在主机、容器、镜像、数据目录、恢复目录、每一步的命令，以及需要人工确认或修改的事项。" +
				"给 job_id 恢复到该备份；或给项目，恢复到 at 之前（默认现在）最新的可恢复时间点。",
			Parameters: obj(projectProps, map[string]any{
				"job_id": map[string]any{"type": "string", "description": "要恢复到的备份任务 ID"},
				"at":     map[string]any{"type": "string", "description": "恢复到不晚于此时间的最新备份点，RFC 3339 或 YYYY-MM-DD HH:MM"},
			}),
			Call: aiCall(s.aiRestorePlan),
		},
	}
}

// aiCall adapts a typed tool function.
func aiCall[A any](fn func(context.Context, *A) (any, error)) func(context.Context, jsontext.Value) (any, error) {
	return func(ctx context.Context, raw jsontext.Value) (any, error) {
		var a A
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("invalid arguments: %v", err)
		}
		return fn(ctx, &a)
	}
}

// ---------- views ----------

type aiDisk struct {
	Path    string `json:"path"`
	Role    string `json:"role"`
	Used    string `json:"used"`
	Total   string `json:"total"`
	UsedPct string `json:"used_pct"`
	Alert   bool   `json:"alert,omitzero"`
}

type aiHost struct {
	Name          string   `json:"name"`
	URL           string   `json:"url"`
	Status        string   `json:"status"`
	IP            string   `json:"ip,omitzero"`
	AgentVersion  string   `json:"agent_version,omitzero"`
	LastHeartbeat string   `json:"last_heartbeat,omitzero"`
	Paused        bool     `json:"paused,omitzero"`
	BackupRoot    string   `json:"backup_root,omitzero"`
	ConfigApplied bool     `json:"config_applied"`
	Disks         []aiDisk `json:"disks"`
	Current       *aiJob   `json:"current_job,omitzero"`
	Progress      string   `json:"progress,omitzero"`
	QueueLength   int      `json:"queue_length"`
}

type aiJob struct {
	ID           string `json:"id"`
	URL          string `json:"url"`
	Project      string `json:"project"`
	Host         string `json:"host,omitzero"`
	Type         string `json:"type"`
	Trigger      string `json:"trigger,omitzero"`
	Status       string `json:"status"`
	ScheduledAt  string `json:"scheduled_at"`
	StartedAt    string `json:"started_at,omitzero"`
	FinishedAt   string `json:"finished_at,omitzero"`
	Duration     string `json:"duration,omitzero"`
	Size         string `json:"size,omitzero"`
	RawSize      string `json:"raw_size,omitzero"`
	UsedBitmap   bool   `json:"used_bitmap,omitzero"`
	ErrorCode    string `json:"error_code,omitzero"`
	ErrorMessage string `json:"error_message,omitzero"`
	Path         string `json:"path,omitzero"`
}

type aiProject struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Host          string `json:"host"`
	Container     string `json:"container"`
	Tier          string `json:"tier"`
	Schedule      string `json:"schedule"`
	IncrMode      string `json:"incr_mode"`
	RetentionDays int    `json:"retention_days"`
	Enabled       bool   `json:"enabled"`
	Deleted       bool   `json:"deleted,omitzero"`
	LastFullOK    string `json:"last_successful_full,omitzero"`
	LastIncrOK    string `json:"last_successful_incr,omitzero"`
	Stale         bool   `json:"stale,omitzero"`
	ChainsURL     string `json:"chains_url"`
}

func (s *Server) aiTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(s.cfg.Location).Format("2006-01-02 15:04:05 -07:00")
}

func aiDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return d.Round(time.Second).String()
}

func (s *Server) aiJob(j *store.Job, hosts map[int64]string) aiJob {
	v := aiJob{
		ID: j.ID.String(), URL: "/jobs?id=" + j.ID.String(), Project: j.Project, Host: hosts[j.HostID],
		Type: string(j.Type), Trigger: string(j.Trigger), Status: string(j.Status),
		ScheduledAt: s.aiTime(j.ScheduledAt), StartedAt: s.aiTime(j.StartedAt), FinishedAt: s.aiTime(j.FinishedAt),
		Duration: aiDuration(j.Duration()), UsedBitmap: j.Type == api.JobIncr && j.UsedBitmap,
		ErrorCode: string(j.ErrorCode), ErrorMessage: redact(j.ErrorMessage), Path: j.Path,
	}
	if j.CompressedSize > 0 {
		v.Size = sizeText(j.CompressedSize)
	}
	if j.RawSize > 0 {
		v.RawSize = sizeText(j.RawSize)
	}
	return v
}

func (s *Server) aiHost(h *store.Host) aiHost {
	hv := s.hostView(h)
	v := aiHost{
		Name: h.Name, URL: fmt.Sprintf("/hosts?id=%d", h.ID), Status: hv.Status, IP: h.IP, AgentVersion: h.AgentVersion,
		LastHeartbeat: s.aiTime(h.LastHeartbeatAt), Paused: h.Paused, BackupRoot: h.BackupRoot,
		ConfigApplied: h.AppliedConfigVersion >= h.ConfigVersion, Disks: []aiDisk{}, QueueLength: len(h.State.Queue),
	}
	for _, d := range h.Metrics.Disks {
		ratio := 0.0
		if d.Total > 0 {
			ratio = float64(d.Total-d.Free) / float64(d.Total)
		}
		v.Disks = append(v.Disks, aiDisk{Path: d.Path, Role: d.Role, Used: sizeText(d.Total - d.Free), Total: sizeText(d.Total),
			UsedPct: fmt.Sprintf("%.1f%%", ratio*100), Alert: ratio > diskAlertRatio})
	}
	if c := h.State.Current; c != nil {
		j := s.aiJob(&store.Job{Job: *c, HostID: h.ID}, map[int64]string{h.ID: h.Name})
		v.Current = &j
		if p := h.State.Progress; p != nil {
			v.Progress = fmt.Sprintf("已写入 %s，%s/s，已耗时 %s", sizeText(p.CompressedBytes), sizeText(int64(p.BytesPerSecond)), aiDuration(p.Elapsed.Std()))
		}
	}
	return v
}

func scheduleText(p *store.Project) string {
	s := "每 " + aiDuration(p.FullInterval.Std()) + " 全量"
	if p.IncrInterval.Std() > 0 {
		s += "，每 " + aiDuration(p.IncrInterval.Std()) + " 增量"
	}
	if off := p.Offset.Std(); off > 0 {
		s += "，偏移 " + aiDuration(off)
	}
	return s
}

// secretPattern matches credentials in free-form text from the hosts.
var secretPattern = regexp.MustCompile(`(?i)((?:password|passwd|pwd|secret|token|api[_-]?key)\s*(?:=|:|\s)\s*)('[^']*'|"[^"]*"|[^\s,;]+)`)

// redact hides credentials that may appear in logs and error messages.
func redact(s string) string { return secretPattern.ReplaceAllString(s, "${1}[REDACTED]") }

// ---------- time ranges ----------

type aiRange struct {
	Period string `json:"period"`
	From   string `json:"from"`
	To     string `json:"to"`
}

// resolve returns [from, to) for the range; def is the period used when
// none is given.
func (s *Server) resolve(r aiRange, def string) (from, to time.Time, err error) {
	loc := s.cfg.Location
	now := s.now().In(loc)
	day := func(t time.Time) time.Time { y, m, d := t.Date(); return time.Date(y, m, d, 0, 0, 0, 0, loc) }
	today := day(now)
	if r.From != "" || r.To != "" {
		if from, err = s.parseTime(r.From); err != nil {
			return from, to, err
		}
		if to, err = s.parseTime(r.To); err != nil {
			return from, to, err
		}
		if to.IsZero() {
			to = now.Add(time.Second)
		}
		if !from.Before(to) && !from.IsZero() {
			return from, to, errors.New("from must be before to")
		}
		return from, to, nil
	}
	if r.Period == "" {
		r.Period = def
	}
	monday := today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
	month := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, loc)
	switch r.Period {
	case "today":
		return today, today.AddDate(0, 0, 1), nil
	case "yesterday":
		return today.AddDate(0, 0, -1), today, nil
	case "this_week":
		return monday, monday.AddDate(0, 0, 7), nil
	case "last_week":
		return monday.AddDate(0, 0, -7), monday, nil
	case "last_7_days":
		return now.AddDate(0, 0, -7), now.Add(time.Second), nil
	case "last_30_days":
		return now.AddDate(0, 0, -30), now.Add(time.Second), nil
	case "this_month":
		return month, month.AddDate(0, 1, 0), nil
	case "last_month":
		return month.AddDate(0, -1, 0), month, nil
	}
	return from, to, fmt.Errorf("unknown period %q; use one of %s", r.Period, strings.Join(aiPeriods, ", "))
}

func (s *Server) parseTime(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, v, s.cfg.Location); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("time %q: use RFC 3339 or YYYY-MM-DD[ HH:MM]", v)
}

// ---------- name resolution ----------

// stopWords are ignored when matching project names: users say "fetisch
// mysql slave" for the container fetisch_maindb_slave.
var stopWords = map[string]bool{"mysql": true, "slave": true, "replica": true, "db": true, "database": true,
	"backup": true, "backups": true, "project": true, "the": true, "of": true,
	"备份": true, "从库": true, "数据库": true, "项目": true}

func keywords(q string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if !stopWords[w] {
			out = append(out, w)
		}
	}
	return out
}

// matchScore scores how well the keywords of q match the given names; 0
// means no match.
func matchScore(q string, names ...string) int {
	q = strings.ToLower(strings.TrimSpace(q))
	hay := strings.ToLower(strings.Join(names, " "))
	score := 0
	for _, n := range names {
		if strings.EqualFold(n, q) {
			score += 100
		}
	}
	for _, w := range keywords(q) {
		if strings.Contains(hay, w) {
			score += 10 + len(w)
		}
	}
	return score
}

func (s *Server) findHost(ctx context.Context, q string) (*store.Host, error) {
	hosts, err := s.store.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	var best []*store.Host
	top := 0
	for i := range hosts {
		h := &hosts[i]
		switch sc := matchScore(q, h.Name, h.Hostname); {
		case sc > top:
			top, best = sc, []*store.Host{h}
		case sc == top && sc > 0:
			best = append(best, h)
		}
	}
	switch len(best) {
	case 0:
		names := make([]string, len(hosts))
		for i := range hosts {
			names[i] = hosts[i].Name
		}
		return nil, fmt.Errorf("no host matches %q; hosts: %s", q, strings.Join(names, ", "))
	case 1:
		return best[0], nil
	}
	names := make([]string, len(best))
	for i, h := range best {
		names[i] = h.Name
	}
	return nil, fmt.Errorf("several hosts match %q: %s; use the full name", q, strings.Join(names, ", "))
}

type scoredProject struct {
	store.Project
	host  string
	score int
}

// matchProjects returns the projects matching the query and host, best
// first. An empty query matches every project.
func (s *Server) matchProjects(ctx context.Context, query, host string, includeDeleted bool) ([]scoredProject, map[int64]string, error) {
	hosts, err := s.store.Hosts(ctx)
	if err != nil {
		return nil, nil, err
	}
	names := make(map[int64]string, len(hosts))
	for _, h := range hosts {
		names[h.ID] = h.Name
	}
	f := store.ProjectFilter{IncludeDeleted: includeDeleted}
	if host != "" {
		h, err := s.findHost(ctx, host)
		if err != nil {
			return nil, nil, err
		}
		f.HostID = h.ID
	}
	projects, err := s.store.Projects(ctx, f)
	if err != nil {
		return nil, nil, err
	}
	var out []scoredProject
	for _, p := range projects {
		sc := 1
		if strings.TrimSpace(query) != "" {
			if sc = matchScore(query, p.Name, p.Container); sc == 0 {
				continue
			}
		}
		out = append(out, scoredProject{Project: p, host: names[p.HostID], score: sc})
	}
	slices.SortStableFunc(out, func(a, b scoredProject) int { return cmp.Compare(b.score, a.score) })
	return out, names, nil
}

type aiProjectRef struct {
	Project   string `json:"project"`
	ProjectID int64  `json:"project_id"`
	Host      string `json:"host"`
}

// project resolves a reference to exactly one project.
func (s *Server) project(ctx context.Context, ref aiProjectRef) (*store.Project, map[int64]string, error) {
	if ref.ProjectID != 0 {
		p, err := s.store.Project(ctx, ref.ProjectID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, fmt.Errorf("project %d does not exist", ref.ProjectID)
		}
		if err != nil {
			return nil, nil, err
		}
		_, names, err := s.matchProjects(ctx, "", "", false)
		return p, names, err
	}
	if strings.TrimSpace(ref.Project) == "" {
		return nil, nil, errors.New("project or project_id is required")
	}
	found, names, err := s.matchProjects(ctx, ref.Project, ref.Host, true)
	if err != nil {
		return nil, nil, err
	}
	// Prefer live projects over deleted ones of the same score.
	slices.SortStableFunc(found, func(a, b scoredProject) int {
		return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(boolInt(b.DeletedAt.IsZero()), boolInt(a.DeletedAt.IsZero())))
	})
	switch {
	case len(found) == 0:
		return nil, nil, fmt.Errorf("no project matches %q; call find_projects to list projects", ref.Project)
	case len(found) > 1 && found[1].score == found[0].score && found[1].DeletedAt.IsZero() == found[0].DeletedAt.IsZero():
		var c []string
		for _, p := range found[:min(len(found), 8)] {
			if p.score == found[0].score {
				c = append(c, fmt.Sprintf("%s (id %d, host %s)", p.Name, p.ID, p.host))
			}
		}
		return nil, nil, fmt.Errorf("several projects match %q: %s; pass project_id or host", ref.Project, strings.Join(c, ", "))
	}
	return &found[0].Project, names, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---------- tools ----------

func (s *Server) aiListHosts(ctx context.Context, _ *struct{}) (any, error) {
	hosts, err := s.store.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]aiHost, 0, len(hosts))
	for i := range hosts {
		out = append(out, s.aiHost(&hosts[i]))
	}
	return map[string]any{"hosts": out}, nil
}

type aiContainer struct {
	Name    string   `json:"name"`
	Image   string   `json:"image"`
	State   string   `json:"state"`
	Running bool     `json:"running"`
	Mounts  []string `json:"mounts"`
	DataDir string   `json:"mysql_data_dir_on_host,omitzero"`
}

func (s *Server) aiGetHost(ctx context.Context, a *struct {
	Host string `json:"host"`
}) (any, error) {
	h, err := s.findHost(ctx, a.Host)
	if err != nil {
		return nil, err
	}
	projects, err := s.store.Projects(ctx, store.ProjectFilter{HostID: h.ID})
	if err != nil {
		return nil, err
	}
	names := make([]string, len(projects))
	for i, p := range projects {
		names[i] = p.Name + "（容器 " + p.Container + "）"
	}
	var cs []aiContainer
	for _, c := range h.Containers {
		v := aiContainer{Name: c.Name, Image: c.Image, State: c.State, Running: c.Running, Mounts: []string{}}
		for _, m := range c.Mounts {
			v.Mounts = append(v.Mounts, m.Source+" -> "+m.Destination)
			if path.Clean(m.Destination) == mysqlDataDir {
				v.DataDir = m.Source
			}
		}
		cs = append(cs, v)
	}
	queue := make([]aiJob, 0, len(h.State.Queue))
	for _, j := range h.State.Queue {
		queue = append(queue, s.aiJob(&store.Job{Job: j, HostID: h.ID}, nil))
	}
	bitmaps := make([]map[string]string, 0, len(h.Metrics.Bitmaps))
	for _, b := range h.Metrics.Bitmaps {
		bitmaps = append(bitmaps, map[string]string{"project": b.Project, "size": sizeText(b.Bytes), "checked_at": s.aiTime(b.CheckedAt)})
	}
	return map[string]any{
		"host": s.aiHost(h), "hostname": h.Hostname, "projects": names, "containers": cs, "queue": queue, "bitmaps": bitmaps,
	}, nil
}

func (s *Server) aiFindProjects(ctx context.Context, a *struct {
	Query          string `json:"query"`
	Host           string `json:"host"`
	IncludeDeleted bool   `json:"include_deleted"`
}) (any, error) {
	found, names, err := s.matchProjects(ctx, a.Query, a.Host, a.IncludeDeleted)
	if err != nil {
		return nil, err
	}
	last, err := s.store.LastSuccesses(ctx)
	if err != nil {
		return nil, err
	}
	type key struct {
		id  int64
		typ api.JobType
	}
	lastAt := make(map[key]time.Time, len(last))
	for _, l := range last {
		lastAt[key{l.ProjectID, l.Type}] = l.At
	}
	out := make([]aiProject, 0, len(found))
	for _, p := range found {
		full, incr := lastAt[key{p.ID, api.JobFull}], lastAt[key{p.ID, api.JobIncr}]
		newest := p.CreatedAt // new projects get two periods for their first backup
		for _, t := range []time.Time{full, incr} {
			if t.After(newest) {
				newest = t
			}
		}
		out = append(out, aiProject{
			ID: p.ID, Name: p.Name, Host: names[p.HostID], Container: p.Container, Tier: string(p.Tier), Schedule: scheduleText(&p.Project),
			IncrMode: string(p.IncrMode), RetentionDays: p.RetentionDays, Enabled: p.Enabled(), Deleted: !p.DeletedAt.IsZero(),
			LastFullOK: s.aiTime(full), LastIncrOK: s.aiTime(incr),
			Stale:     p.Enabled() && s.now().Sub(newest) > 2*p.FullInterval.Std(),
			ChainsURL: fmt.Sprintf("/jobs?project_id=%d&tab=chains", p.ID),
		})
	}
	return map[string]any{"projects": out}, nil
}

func (s *Server) aiListJobs(ctx context.Context, a *struct {
	aiProjectRef
	aiRange
	Status []string `json:"status"`
	Type   string   `json:"type"`
	Limit  int      `json:"limit"`
}) (any, error) {
	f := store.JobFilter{Type: api.JobType(a.Type)}
	var names map[int64]string
	switch {
	case a.ProjectID != 0 || a.Project != "":
		p, n, err := s.project(ctx, a.aiProjectRef)
		if err != nil {
			return nil, err
		}
		f.ProjectID, names = p.ID, n
	case a.Host != "":
		h, err := s.findHost(ctx, a.Host)
		if err != nil {
			return nil, err
		}
		f.HostID, names = h.ID, map[int64]string{h.ID: h.Name}
	}
	if a.Period != "" || a.From != "" || a.To != "" {
		var err error
		if f.From, f.To, err = s.resolve(a.aiRange, ""); err != nil {
			return nil, err
		}
	}
	for _, st := range a.Status {
		f.Statuses = append(f.Statuses, api.JobStatus(st))
	}
	f.Page.Limit = cmp.Or(min(a.Limit, aiMaxJobs), aiDefaultJobs)
	jobs, total, err := s.store.Jobs(ctx, f)
	if err != nil {
		return nil, err
	}
	if names == nil {
		if names, err = s.hostNames(ctx); err != nil {
			return nil, err
		}
	}
	out := make([]aiJob, len(jobs))
	for i := range jobs {
		out[i] = s.aiJob(&jobs[i], names)
	}
	res := map[string]any{"total": total, "returned": len(out), "jobs": out}
	if !f.From.IsZero() || !f.To.IsZero() {
		res["range"] = s.aiTime(f.From) + " ~ " + s.aiTime(f.To)
	}
	return res, nil
}

func (s *Server) hostNames(ctx context.Context) (map[int64]string, error) {
	hosts, err := s.store.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(hosts))
	for _, h := range hosts {
		names[h.ID] = h.Name
	}
	return names, nil
}

type aiProjectStats struct {
	Project  string         `json:"project"`
	Host     string         `json:"host"`
	Counts   map[string]int `json:"counts"`
	Full     int            `json:"full_jobs"`
	Incr     int            `json:"incr_jobs"`
	Size     string         `json:"backup_size"`
	AvgFull  string         `json:"avg_full_duration,omitzero"`
	AvgIncr  string         `json:"avg_incr_duration,omitzero"`
	bytes    int64
	fullTime time.Duration
	incrTime time.Duration
	fullDone int
	incrDone int
}

func (s *Server) aiBackupSummary(ctx context.Context, a *struct {
	aiRange
	Host    string `json:"host"`
	Project string `json:"project"`
}) (any, error) {
	f := store.JobFilter{}
	var err error
	if f.From, f.To, err = s.resolve(a.aiRange, "today"); err != nil {
		return nil, err
	}
	if a.Project != "" {
		p, _, err := s.project(ctx, aiProjectRef{Project: a.Project, Host: a.Host})
		if err != nil {
			return nil, err
		}
		f.ProjectID = p.ID
	} else if a.Host != "" {
		h, err := s.findHost(ctx, a.Host)
		if err != nil {
			return nil, err
		}
		f.HostID = h.ID
	}
	counts, err := s.store.JobCounts(ctx, f)
	if err != nil {
		return nil, err
	}
	names, err := s.hostNames(ctx)
	if err != nil {
		return nil, err
	}
	totals := map[string]int{}
	var totalBytes int64
	byProject := map[[2]string]*aiProjectStats{}
	var order [][2]string
	for _, c := range counts {
		k := [2]string{c.Project, names[c.HostID]}
		ps := byProject[k]
		if ps == nil {
			ps = &aiProjectStats{Project: c.Project, Host: names[c.HostID], Counts: map[string]int{}}
			byProject[k] = ps
			order = append(order, k)
		}
		ps.Counts[string(c.Status)] += c.Count
		totals[string(c.Status)] += c.Count
		if c.Status.Succeeded() {
			ps.bytes += c.Bytes
			totalBytes += c.Bytes
		}
		switch c.Type {
		case api.JobFull:
			ps.Full += c.Count
			if c.Status.Succeeded() {
				ps.fullTime += c.AvgDuration * time.Duration(c.Count)
				ps.fullDone += c.Count
			}
		case api.JobIncr:
			ps.Incr += c.Count
			if c.Status.Succeeded() {
				ps.incrTime += c.AvgDuration * time.Duration(c.Count)
				ps.incrDone += c.Count
			}
		}
	}
	projects := make([]aiProjectStats, 0, len(order))
	for _, k := range order {
		ps := byProject[k]
		ps.Size = sizeText(ps.bytes)
		if ps.fullDone > 0 {
			ps.AvgFull = aiDuration(ps.fullTime / time.Duration(ps.fullDone))
		}
		if ps.incrDone > 0 {
			ps.AvgIncr = aiDuration(ps.incrTime / time.Duration(ps.incrDone))
		}
		projects = append(projects, *ps)
	}

	ff := f
	ff.Statuses = []api.JobStatus{api.StatusFailed, api.StatusCanceled}
	ff.Page.Limit = aiFailures
	failed, failedTotal, err := s.store.Jobs(ctx, ff)
	if err != nil {
		return nil, err
	}
	failures := make([]aiJob, len(failed))
	for i := range failed {
		failures[i] = s.aiJob(&failed[i], names)
		failures[i].Path = ""
	}

	health, err := s.aiFindProjects(ctx, &struct {
		Query          string `json:"query"`
		Host           string `json:"host"`
		IncludeDeleted bool   `json:"include_deleted"`
	}{Host: a.Host})
	if err != nil {
		return nil, err
	}
	var stale []string
	for _, p := range health.(map[string]any)["projects"].([]aiProject) {
		if p.Stale && (a.Project == "" || f.ProjectID == p.ID) {
			stale = append(stale, fmt.Sprintf("%s（%s，最近成功全量 %s）", p.Name, p.Host, cmp.Or(p.LastFullOK, "无")))
		}
	}
	return map[string]any{
		"range":                 s.aiTime(f.From) + " ~ " + s.aiTime(f.To),
		"timezone":              s.cfg.Location.String(),
		"totals":                totals,
		"total_backup_size":     sizeText(totalBytes),
		"projects":              projects,
		"failures":              failures,
		"failures_total":        failedTotal,
		"stale_projects":        stale,
		"note_on_counts":        "success 与 warning 都算备份成功；warning 表示成功但有告警；skipped 是因前一个任务未完成等原因跳过的计划。",
		"failures_list_limited": failedTotal > len(failures),
	}, nil
}

func (s *Server) jobByID(ctx context.Context, id string) (*store.Job, error) {
	u, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		return nil, fmt.Errorf("job_id %q is not a UUID", id)
	}
	j, err := s.store.Job(ctx, u)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("job %s does not exist", id)
	}
	return j, err
}

func (s *Server) aiGetJob(ctx context.Context, a *struct {
	JobID string `json:"job_id"`
}) (any, error) {
	j, err := s.jobByID(ctx, a.JobID)
	if err != nil {
		return nil, err
	}
	names, err := s.hostNames(ctx)
	if err != nil {
		return nil, err
	}
	res := map[string]any{"job": s.aiJob(j, names)}
	if j.Path != "" {
		res["backup_dir"] = path.Dir(j.Path)
		res["chain_dir"] = path.Dir(path.Dir(j.Path))
	}
	if j.FromLSN != 0 || j.ToLSN != 0 {
		res["lsn"] = map[string]uint64{"from": j.FromLSN, "to": j.ToLSN}
	}
	if si := j.SlaveInfo; si != nil {
		res["master_position"] = map[string]any{"log_file": si.MasterLogFile, "log_pos": si.MasterLogPos, "gtid_executed": si.GTIDExecuted}
	}
	if j.BaseJobID != uuid.Nil() {
		res["base_job_id"] = j.BaseJobID.String()
	}
	if j.ChainID != uuid.Nil() {
		if c, err := s.store.Chain(ctx, j.ChainID); err == nil {
			res["chain"] = map[string]string{"id": c.ID.String(), "status": c.Status, "started_at": s.aiTime(c.StartedAt)}
		}
	}
	if j.LogTail != "" {
		tail := j.LogTail
		if len(tail) > aiLogTail {
			tail = tail[len(tail)-aiLogTail:]
		}
		res["log_tail"] = redact(strings.ToValidUTF8(tail, ""))
	}
	return res, nil
}

// newestRestorable returns the newest backup of a project finished at or
// before at whose restore sequence is complete.
func (s *Server) newestRestorable(ctx context.Context, projectID int64, at time.Time) (*store.Job, error) {
	jobs, _, err := s.store.Jobs(ctx, store.JobFilter{
		ProjectID: projectID, Statuses: []api.JobStatus{api.StatusSuccess, api.StatusWarning}, To: at, Page: store.Page{Limit: 100},
	})
	if err != nil {
		return nil, err
	}
	chains := map[uuid.UUID][]store.Job{}
	for i := range jobs {
		j := &jobs[i]
		if j.ChainID == uuid.Nil() || j.FinishedAt.After(at) {
			continue
		}
		cj, ok := chains[j.ChainID]
		if !ok {
			if c, err := s.store.Chain(ctx, j.ChainID); err != nil || c.Status == store.ChainDeleted {
				chains[j.ChainID] = nil
				continue
			}
			if cj, err = s.store.ChainJobs(ctx, []uuid.UUID{j.ChainID}); err != nil {
				return nil, err
			}
			chains[j.ChainID] = cj
		}
		if cj == nil {
			continue
		}
		if _, err := restoreSequence(cj, j.ID); err == nil {
			return j, nil
		}
	}
	return nil, nil
}

func (s *Server) aiLatestBackups(ctx context.Context, a *aiProjectRef) (any, error) {
	p, names, err := s.project(ctx, *a)
	if err != nil {
		return nil, err
	}
	pick := func(f store.JobFilter) (*aiJob, error) {
		f.ProjectID, f.Page.Limit = p.ID, 1
		jobs, _, err := s.store.Jobs(ctx, f)
		if err != nil || len(jobs) == 0 {
			return nil, err
		}
		v := s.aiJob(&jobs[0], names)
		return &v, nil
	}
	ok := []api.JobStatus{api.StatusSuccess, api.StatusWarning}
	res := map[string]any{"project": p.Name, "host": names[p.HostID], "container": p.Container}
	for name, f := range map[string]store.JobFilter{
		"latest_job":             {},
		"latest_successful_full": {Type: api.JobFull, Statuses: ok},
		"latest_successful_incr": {Type: api.JobIncr, Statuses: ok},
	} {
		v, err := pick(f)
		if err != nil {
			return nil, err
		}
		if v != nil {
			res[name] = v
		}
	}
	j, err := s.newestRestorable(ctx, p.ID, s.now().Add(time.Second))
	if err != nil {
		return nil, err
	}
	if j != nil {
		res["newest_restore_point"] = map[string]string{"job_id": j.ID.String(), "type": string(j.Type), "consistent_as_of": s.aiTime(j.FinishedAt)}
	}
	return res, nil
}

func (s *Server) aiRestorePlan(ctx context.Context, a *struct {
	aiProjectRef
	JobID string `json:"job_id"`
	At    string `json:"at"`
}) (any, error) {
	var j *store.Job
	var err error
	if a.JobID != "" {
		if j, err = s.jobByID(ctx, a.JobID); err != nil {
			return nil, err
		}
	} else {
		p, _, err := s.project(ctx, a.aiProjectRef)
		if err != nil {
			return nil, err
		}
		at := s.now().Add(time.Second)
		if a.At != "" {
			if at, err = s.parseTime(a.At); err != nil {
				return nil, err
			}
		}
		if j, err = s.newestRestorable(ctx, p.ID, at); err != nil {
			return nil, err
		}
		if j == nil {
			return nil, fmt.Errorf("project %s has no restorable backup before %s", p.Name, s.aiTime(at))
		}
	}
	g, err := s.restorePlan(ctx, j, langZH)
	if err != nil {
		if ae, ok := errors.AsType[*apiError](err); ok {
			return nil, errors.New(ae.msg)
		}
		return nil, err
	}
	seq := make([]aiJob, len(g.Sequence))
	for i := range g.Sequence {
		seq[i] = s.aiJob(&g.Sequence[i], map[int64]string{j.HostID: g.Env.Host})
	}
	return map[string]any{
		"target":           s.aiJob(&g.Target, map[int64]string{j.HostID: g.Env.Host}),
		"consistent_as_of": s.aiTime(g.Target.FinishedAt),
		"sequence":         seq,
		"environment":      g.Env,
		"steps":            g.Steps,
		"notes":            g.Notes,
		"guide_url":        "/jobs?id=" + j.ID.String(),
	}, nil
}
