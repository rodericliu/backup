package server

import (
	"bufio"
	json "encoding/json/v2"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/rodericliu/backup/internal/api"
	"github.com/rodericliu/backup/internal/store"
)

// history reports a chain: a full backup, two chained incrementals, a failed
// incremental and a skipped job, and returns them.
func (e *testEnv) history(token string, t0 time.Time) []api.Job {
	e.t.Helper()
	chain := uuid.NewV7()
	mk := func(typ api.JobType, st api.JobStatus, at time.Duration, d time.Duration, base uuid.UUID) api.Job {
		j := api.Job{ID: uuid.NewV7(), Project: "gays", Type: typ, ChainID: chain, BaseJobID: base, Trigger: api.TriggerSchedule,
			Status: st, ScheduledAt: t0.Add(at), StartedAt: t0.Add(at), FinishedAt: t0.Add(at + d),
			Path: "/data/backup/gays/20261028-0300-chain/" + string(typ) + "/backup.xbstream.zst"}
		if st.Succeeded() {
			j.CompressedSize = 1 << 30
			j.SlaveInfo = &api.SlaveInfo{MasterLogFile: "mysql-bin.000042", MasterLogPos: uint64(at / time.Second)}
		}
		if st == api.StatusFailed {
			j.ErrorCode = api.ErrXtrabackup
		}
		return j
	}
	full := mk(api.JobFull, api.StatusSuccess, 0, 40*time.Minute, uuid.Nil())
	i1 := mk(api.JobIncr, api.StatusSuccess, time.Hour, 5*time.Minute, full.ID)
	i2 := mk(api.JobIncr, api.StatusWarning, 2*time.Hour, 5*time.Minute, i1.ID)
	bad := mk(api.JobIncr, api.StatusFailed, 3*time.Hour, time.Minute, i2.ID)
	skip := mk(api.JobIncr, api.StatusSkipped, 3*time.Hour+time.Minute, 0, uuid.Nil())
	skip.ChainID, skip.StartedAt = uuid.Nil(), time.Time{}
	jobs := []api.Job{full, i1, i2, bad, skip}
	for _, j := range jobs {
		if code := e.agentDo(token, "PUT", "/jobs/"+j.ID.String(), j, nil); code != http.StatusNoContent {
			e.t.Fatalf("put %s = %d", j.Type, code)
		}
	}
	return jobs
}

func TestJobsAndChains(t *testing.T) {
	e := newTestEnv(t)
	admin := e.admin()
	token, hostID := e.setupHost(admin)
	var p ProjectView
	do(t, admin, "POST", e.api("/projects"), projectBody(hostID, "gays", "gays_maindb_slave", nil), &p)
	t0 := e.clock.Now().Add(-6 * time.Hour)
	jobs := e.history(token, t0)
	full, i1, i2, bad := jobs[0], jobs[1], jobs[2], jobs[3]

	var list List[store.Job]
	for q, want := range map[string]int{
		"":                               5,
		"?project_id=" + itoa(p.ID):      5,
		"?type=full":                     1,
		"?status=failed,skipped":         2,
		"?status=success&status=warning": 3,
		"?from=" + url.QueryEscape(t0.Add(time.Hour).Format(time.RFC3339)): 4,
		"?host_id=999": 0,
	} {
		if code := do(t, admin, "GET", e.api("/jobs"+q), nil, &list); code != http.StatusOK || list.Total != want {
			t.Errorf("jobs%s = %d total %d, want %d", q, code, list.Total, want)
		}
	}
	do(t, admin, "GET", e.api("/jobs?page=2&page_size=2"), nil, &list)
	if len(list.Items) != 2 || list.Items[0].ID != i2.ID {
		t.Errorf("page 2 = %+v", list.Items)
	}
	if code := do(t, admin, "GET", e.api("/jobs?type=diff"), nil, nil); code != http.StatusBadRequest {
		t.Errorf("invalid type = %d", code)
	}

	var d JobDetail
	if code := do(t, admin, "GET", e.api("/jobs/"+i2.ID.String()), nil, &d); code != http.StatusOK {
		t.Fatalf("detail = %d", code)
	}
	if d.HostName != "backup-esh-01" || d.Chain == nil || d.Chain.FullJobID != full.ID || d.Base == nil || d.Base.ID != i1.ID ||
		d.SlaveInfo == nil || !d.LogUploadedAt.IsZero() {
		t.Errorf("detail = %+v", d)
	}

	// Complete log (PRD J-4).
	if code := do(t, admin, "GET", e.api("/jobs/"+i2.ID.String()+"/log"), nil, nil); code != http.StatusNotFound {
		t.Errorf("log before upload = %d", code)
	}
	if err := e.store.SaveJobLog(t.Context(), i2.ID, []byte("completed OK!\n"), e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	resp, err := admin.Get(e.api("/jobs/" + i2.ID.String() + "/log"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := bufio.NewReader(resp.Body).ReadString('\n')
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || body != "completed OK!\n" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Errorf("log = %d %q", resp.StatusCode, body)
	}

	// Chains and restore points (PRD J-5).
	var chains ChainsResponse
	do(t, admin, "GET", e.api("/projects/"+itoa(p.ID)+"/chains"), nil, &chains)
	if len(chains.Chains) != 1 || len(chains.Chains[0].Jobs) != 4 || len(chains.RestorePoints) != 3 || chains.RestorePoints[0].JobID != i2.ID {
		t.Errorf("chains = %+v", chains)
	}

	// Restore guide (PRD J-6).
	var g RestoreGuide
	if code := do(t, admin, "GET", e.api("/jobs/"+i2.ID.String()+"/restore"), nil, &g); code != http.StatusOK {
		t.Fatalf("restore = %d", code)
	}
	if len(g.Sequence) != 3 || g.Sequence[0].ID != full.ID || g.Sequence[2].ID != i2.ID || len(g.Steps) != 4 {
		t.Fatalf("guide = %+v", g)
	}
	// The host reported no data directory mount of the container: the
	// commands run on the host and the notes say what to adapt.
	dir := "/data/backup/restore/gays-" + i2.FinishedAt.Format("20060102-1504")
	if g.Env.Dir != dir || g.Env.Container != "gays_maindb_slave" || g.Env.DataDir != "" || len(g.Notes) == 0 {
		t.Errorf("environment = %+v, notes %q", g.Env, g.Notes)
	}
	prepare := g.Steps[1].Commands
	for _, want := range []string{
		"xtrabackup --prepare --apply-log-only --target-dir=" + dir + "/full\n",
		"--apply-log-only --target-dir=" + dir + "/full --incremental-dir=" + dir + "/incr1\n",
		"xtrabackup --prepare --target-dir=" + dir + "/full --incremental-dir=" + dir + "/incr2\n",
	} {
		if !strings.Contains(prepare, want) {
			t.Errorf("prepare step missing %q:\n%s", want, prepare)
		}
	}
	if !strings.Contains(g.Steps[3].Commands, "MASTER_LOG_FILE='mysql-bin.000042', MASTER_LOG_POS=7200") {
		t.Errorf("replication step = %s", g.Steps[3].Commands)
	}
	if code := do(t, admin, "GET", e.api("/jobs/"+bad.ID.String()+"/restore"), nil, nil); code != http.StatusConflict {
		t.Errorf("restore failed job = %d", code)
	}
	do(t, admin, "GET", e.api("/jobs/"+full.ID.String()+"/restore"), nil, &g)
	fullDir := "/data/backup/restore/gays-" + full.FinishedAt.Format("20060102-1504")
	if len(g.Sequence) != 1 || !strings.Contains(g.Steps[1].Commands, "xtrabackup --prepare --target-dir="+fullDir+"/full\n") ||
		strings.Contains(g.Steps[1].Commands, "apply-log-only") {
		t.Errorf("full-only guide = %+v", g.Steps)
	}

	// With the data directory mount known, every command uses the container.
	hb := sampleHeartbeat()
	hb.Containers = []api.Container{{Name: "gays_maindb_slave", Image: "percona:5.6", Running: true, State: "running",
		Mounts: []api.Mount{{Source: "/data/backup", Destination: "/data/backup"}, {Source: "/data/gays", Destination: "/var/lib/mysql/"}}}}
	if code := e.agentDo(token, "POST", "/heartbeat", hb, nil); code != http.StatusOK {
		t.Fatalf("heartbeat = %d", code)
	}
	do(t, admin, "GET", e.api("/jobs/"+i2.ID.String()+"/restore"), nil, &g)
	if g.Env.DataDir != "/data/gays" || g.Env.Image != "percona:5.6" || len(g.Steps) != 7 {
		t.Fatalf("container guide = %+v %+v", g.Env, g.Steps)
	}
	all := ""
	for _, st := range g.Steps {
		all += st.Commands
	}
	for _, want := range []string{
		"podman exec gays_maindb_slave sh -c 'zstd -dc /data/backup/gays/20261028-0300-chain/full/backup.xbstream.zst | xbstream -x -C " + dir + "/full'\n",
		"podman exec gays_maindb_slave xtrabackup --prepare --target-dir=" + dir + "/full --incremental-dir=" + dir + "/incr2\n",
		"podman stop gays_maindb_slave\nmv /data/gays /data/gays.before-restore-" + i2.FinishedAt.Format("20060102-1504") + "\nmkdir /data/gays\n",
		"-v /data/backup:/data/backup -v /data/gays:/var/lib/mysql",
		"-c 'xtrabackup --copy-back --target-dir=" + dir + "/full --datadir=/var/lib/mysql && chown -R mysql:mysql /var/lib/mysql'",
		"podman start gays_maindb_slave\n",
		"podman exec -it gays_maindb_slave mysql -uroot -p\nCHANGE MASTER TO",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("container guide lacks %q:\n%s", want, all)
		}
	}

	// The web UI asks for English: the same commands with English text.
	var en RestoreGuide
	do(t, admin, "GET", e.api("/jobs/"+i2.ID.String()+"/restore"), nil, &en, "Accept-Language", "en-US,en;q=0.9")
	if len(en.Steps) != len(g.Steps) || len(en.Notes) != len(g.Notes) {
		t.Fatalf("English guide = %+v", en)
	}
	texts := en.Notes
	for i := range en.Steps {
		if en.Steps[i].Commands != g.Steps[i].Commands {
			t.Errorf("English step %d commands = %q, want %q", i, en.Steps[i].Commands, g.Steps[i].Commands)
		}
		texts = append(texts, en.Steps[i].Title)
	}
	for _, text := range texts {
		if strings.ContainsFunc(text, func(r rune) bool { return r >= 0x4e00 && r <= 0x9fff }) {
			t.Errorf("English guide contains Chinese: %q", text)
		}
	}
	if !strings.HasPrefix(en.Steps[0].Title, "Extract the backups in container gays_maindb_slave") {
		t.Errorf("English extract title = %q", en.Steps[0].Title)
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/data/backup/gays/x-chain/00-full/backup.xbstream.zst": "/data/backup/gays/x-chain/00-full/backup.xbstream.zst",
		"/data/my dir/it's": `'/data/my dir/it'\''s'`,
		"":                  "''",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestScheduleAndDashboard(t *testing.T) {
	e := newTestEnv(t)
	admin := e.admin()
	token, hostID := e.setupHost(admin)
	var gays, poppen ProjectView
	do(t, admin, "POST", e.api("/projects"), projectBody(hostID, "gays", "gays_maindb_slave", map[string]any{"offset": "3h"}), &gays)
	do(t, admin, "POST", e.api("/projects"), projectBody(hostID, "poppen", "poppen_maindb_slave", map[string]any{"offset": "3h"}), &poppen)
	jobs := e.history(token, e.clock.Now().Add(-5*time.Hour))

	// Prediction (PRD S-10, S-11): both 40m fulls planned at 03:00 overload that hour.
	from := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	var sch ScheduleResponse
	q := url.Values{"host_id": {itoa(hostID)}, "from": {from.Format(time.RFC3339)}}
	if code := do(t, admin, "GET", e.api("/schedule?"+q.Encode()), nil, &sch); code != http.StatusOK {
		t.Fatalf("schedule = %d", code)
	}
	if len(sch.Runs) != 2*(6+18) || sch.Timezone != "UTC (UTC+00:00)" || !sch.To.Equal(from.Add(24*time.Hour)) {
		t.Errorf("runs = %d, schedule = %+v", len(sch.Runs), sch.Hours)
	}
	overloaded := 0
	for _, h := range sch.Hours {
		if h.Overloaded {
			overloaded++
			if h.Hour.Hour()%4 != 3 {
				t.Errorf("hour %s overloaded", h.Hour)
			}
		}
	}
	// gays uses its 40m history; poppen the 30m default: 70m > 60m.
	if overloaded != 6 {
		t.Errorf("overloaded hours = %d, want 6: %+v", overloaded, sch.Hours)
	}
	for _, r := range sch.Runs {
		if r.Type == api.JobFull && r.Project == "gays" && r.Estimate.Std() != 40*time.Minute {
			t.Errorf("gays full estimate = %s, want the 40m average", r.Estimate)
		}
	}
	// Actual jobs overlay (PRD S-12).
	q.Set("from", e.clock.Now().Add(-6*time.Hour).Format(time.RFC3339))
	do(t, admin, "GET", e.api("/schedule?"+q.Encode()), nil, &sch)
	if len(sch.Actual) != len(jobs) {
		t.Errorf("actual = %d, want %d", len(sch.Actual), len(jobs))
	}
	if code := do(t, admin, "GET", e.api("/schedule?host_id="+itoa(hostID)+"&hours=1000"), nil, nil); code != http.StatusBadRequest {
		t.Errorf("too many hours = %d", code)
	}

	// Dashboard (PRD 6.6).
	hb := sampleHeartbeat()
	hb.Containers = []api.Container{{Name: "gays_maindb_slave"}, {Name: "poppen_maindb_slave"}}
	hb.Metrics.Disks = append(hb.Metrics.Disks, api.DiskUsage{Path: "/nvme_data", Role: "data", Total: 1000, Free: 50})
	e.agentDo(token, "POST", "/heartbeat", hb, nil)
	// poppen, created at the test clock's start, never backs up: stale after
	// two 4h periods. gays has a recent backup.
	if _, err := e.storePool(t).Exec(t.Context(), "UPDATE projects SET created_at = $1", e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.clock.Add(9 * time.Hour)
	recent := api.Job{ID: uuid.NewV7(), Project: "gays", Type: api.JobIncr, Trigger: api.TriggerSchedule, Status: api.StatusSuccess,
		ScheduledAt: e.clock.Now().Add(-time.Hour), StartedAt: e.clock.Now().Add(-time.Hour), FinishedAt: e.clock.Now().Add(-55 * time.Minute)}
	e.agentDo(token, "PUT", "/jobs/"+recent.ID.String(), recent, nil)
	admin = e.login("admin", "admin-password-1")
	var d Dashboard
	if code := do(t, admin, "GET", e.api("/dashboard"), nil, &d); code != http.StatusOK {
		t.Fatalf("dashboard = %d", code)
	}
	if len(d.Hosts) != 1 || d.Hosts[0].Status != HostOffline {
		t.Errorf("hosts = %+v", d.Hosts)
	}
	if d.Today.Success != 3 || d.Today.Warning != 1 || d.Today.Failed != 1 || d.Today.Skipped != 1 || d.Today.Bytes != 3<<30 {
		t.Errorf("today = %+v (from %s)", d.Today, d.TodayFrom)
	}
	health := map[string]ProjectHealth{}
	for _, h := range d.Projects {
		health[h.Name] = h
	}
	if g := health["gays"]; g.LastFull.IsZero() || g.LastIncr.IsZero() || g.Stale {
		t.Errorf("gays health = %+v", g)
	}
	if p := health["poppen"]; !p.Stale {
		t.Errorf("poppen health = %+v", p)
	}
	if len(d.Disks) != 2 || d.Disks[0].Alert || !d.Disks[1].Alert {
		t.Errorf("disks = %+v", d.Disks)
	}
	if len(d.RecentFailures) != 1 || d.RecentFailures[0].ID != jobs[3].ID {
		t.Errorf("recent failures = %+v", d.RecentFailures)
	}
}

func TestEventStream(t *testing.T) {
	e := newTestEnv(t)
	admin := e.admin()
	token, hostID := e.setupHost(admin)
	do(t, admin, "POST", e.api("/projects"), projectBody(hostID, "gays", "gays_maindb_slave", nil), nil)

	req, _ := http.NewRequestWithContext(t.Context(), "GET", e.api("/events/stream"), nil)
	resp, err := admin.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	events := make(chan [2]string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		var typ string
		for sc.Scan() {
			line := sc.Text()
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				typ = v
			} else if v, ok := strings.CutPrefix(line, "data: "); ok {
				events <- [2]string{typ, v}
			}
		}
		close(events)
	}()
	waitSubscribed := time.Now().Add(5 * time.Second)
	for !e.srv.events.active() && time.Now().Before(waitSubscribed) {
		time.Sleep(10 * time.Millisecond)
	}

	e.agentDo(token, "POST", "/heartbeat", sampleHeartbeat(), nil)
	job := api.Job{ID: uuid.NewV7(), Project: "gays", Type: api.JobFull, Trigger: api.TriggerManual, Status: api.StatusRunning,
		ScheduledAt: e.clock.Now(), StartedAt: e.clock.Now()}
	e.agentDo(token, "POST", "/jobs/"+job.ID.String()+"/events", api.JobEvents{Job: &job, Events: []api.JobEvent{
		event(1, e.clock.Now(), api.EventProgress, api.Progress{CompressedBytes: 42 << 20, BytesPerSecond: 1 << 20}),
	}}, nil)

	var gotHost, gotJob bool
	timeout := time.After(5 * time.Second)
	for !gotHost || !gotJob {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("stream closed")
			}
			switch ev[0] {
			case EventHost:
				var h HostView
				gotHost = json.Unmarshal([]byte(ev[1]), &h) == nil && h.ID == hostID && h.Status == HostOnline
			case EventJob:
				var u JobUpdate
				gotJob = json.Unmarshal([]byte(ev[1]), &u) == nil && u.Job.ID == job.ID && u.Progress != nil && u.Progress.CompressedBytes == 42<<20
			}
		case <-timeout:
			t.Fatalf("events: host %v, job %v", gotHost, gotJob)
		}
	}
}

func TestJobListPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	e := newTestEnv(t)
	admin := e.admin()
	_, hostID := e.setupHost(admin)
	var p ProjectView
	do(t, admin, "POST", e.api("/projects"), projectBody(hostID, "gays", "gays_maindb_slave", nil), &p)
	// 100k jobs over ~3 years of hourly backups (PRD 9).
	_, err := e.storePool(t).Exec(t.Context(), `INSERT INTO jobs (id, host_id, project_id, project, type, trigger, status, scheduled_at,
			started_at, finished_at, compressed_size)
		SELECT gen_random_uuid(), $1, $2, 'gays', CASE WHEN i % 4 = 0 THEN 'full' ELSE 'incr' END, 'schedule',
			CASE WHEN i % 50 = 0 THEN 'failed' ELSE 'success' END,
			now() - make_interval(hours => i), now() - make_interval(hours => i), now() - make_interval(hours => i) + interval '5 min', 1000
		FROM generate_series(1, 100000) AS i`, hostID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.storePool(t).Exec(t.Context(), "ANALYZE jobs"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"", "?project_id=" + itoa(p.ID) + "&type=full", "?status=failed", "?host_id=" + itoa(hostID) + "&page=1000&page_size=50",
		"?from=" + url.QueryEscape(time.Now().Add(-30*24*time.Hour).Format(time.RFC3339)),
	} {
		start := time.Now()
		var list List[store.Job]
		if code := do(t, admin, "GET", e.api("/jobs"+q), nil, &list); code != http.StatusOK || list.Total == 0 {
			t.Errorf("jobs%s = %d %d", q, code, list.Total)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("jobs%s took %s, want < 1s", q, d)
		}
	}
}
