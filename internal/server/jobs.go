package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/rodericliu/backup/internal/api"
	"github.com/rodericliu/backup/internal/store"
)

// listJobs filters the job history (PRD J-2).
func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	f := store.JobFilter{Project: q.Get("project"), Type: api.JobType(q.Get("type"))}
	var err error
	for name, dst := range map[string]*int64{"host_id": &f.HostID, "project_id": &f.ProjectID} {
		if v := q.Get(name); v != "" {
			if *dst, err = strconv.ParseInt(v, 10, 64); err != nil {
				return badRequest("%s %q: not an integer", name, v)
			}
		}
	}
	if f.Type != "" && f.Type != api.JobFull && f.Type != api.JobIncr {
		return badRequest("type must be %q or %q", api.JobFull, api.JobIncr)
	}
	for _, st := range q["status"] {
		for v := range strings.SplitSeq(st, ",") {
			f.Statuses = append(f.Statuses, api.JobStatus(v))
		}
	}
	if f.Page, err = pageParams(q); err != nil {
		return err
	}
	if f.From, err = timeParam(q, "from"); err != nil {
		return err
	}
	if f.To, err = timeParam(q, "to"); err != nil {
		return err
	}
	jobs, total, err := s.store.Jobs(r.Context(), f)
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, List[store.Job]{Items: jobs, Total: total})
}

// JobDetail is the response of GET /jobs/{id} (PRD J-3, J-7).
type JobDetail struct {
	store.Job
	HostName string         `json:"host_name"`
	Chain    *store.Chain   `json:"chain,omitzero"`
	Base     *store.Job     `json:"base,omitzero"`
	Events   []api.JobEvent `json:"events"`
	// LogUploadedAt is set when the complete log is available (PRD J-4).
	LogUploadedAt time.Time `json:"log_uploaded_at,omitzero"`
}

func (s *Server) pathJob(r *http.Request) (*store.Job, error) {
	id, err := pathJobID(r)
	if err != nil {
		return nil, err
	}
	j, err := s.store.Job(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("job")
	}
	return j, err
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) error {
	j, err := s.pathJob(r)
	if err != nil {
		return err
	}
	d := JobDetail{Job: *j}
	h, err := s.store.Host(r.Context(), j.HostID)
	if err != nil {
		return err
	}
	d.HostName = h.Name
	if j.ChainID != uuid.Nil() {
		if d.Chain, err = s.store.Chain(r.Context(), j.ChainID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	if j.BaseJobID != uuid.Nil() {
		if d.Base, err = s.store.Job(r.Context(), j.BaseJobID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	if d.Events, err = s.store.JobEvents(r.Context(), j.ID); err != nil {
		return err
	}
	if _, at, err := s.store.JobLog(r.Context(), j.ID); err == nil {
		d.LogUploadedAt = at
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return writeJSON(w, http.StatusOK, d)
}

// getJobLog returns the uploaded complete log (PRD J-4).
func (s *Server) getJobLog(w http.ResponseWriter, r *http.Request) error {
	id, err := pathJobID(r)
	if err != nil {
		return err
	}
	content, _, err := s.store.JobLog(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		return newError(http.StatusNotFound, "NOT_FOUND", "the log of job %s has not been uploaded; request it first", id)
	}
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, err = w.Write(content)
	return err
}

// ChainView is a chain with its jobs (PRD J-5).
type ChainView struct {
	store.Chain
	Jobs []store.Job `json:"jobs"`
}

// RestorePoint is a time the project can be restored to: a successful
// backup whose bases all succeeded.
type RestorePoint struct {
	JobID uuid.UUID   `json:"job_id"`
	Type  api.JobType `json:"type"`
	// At is when the backup finished; the data is consistent as of then.
	At time.Time `json:"at"`
}

// ChainsResponse is the response of GET /projects/{id}/chains.
type ChainsResponse struct {
	Chains        []ChainView    `json:"chains"`
	RestorePoints []RestorePoint `json:"restore_points"`
}

// maxChains bounds the chains returned for a project.
const maxChains = 200

func (s *Server) projectChains(w http.ResponseWriter, r *http.Request) error {
	p, _, err := s.pathProject(r)
	if err != nil {
		return err
	}
	chains, err := s.store.ProjectChains(r.Context(), p.ID, r.URL.Query().Get("include_deleted") == "true", maxChains)
	if err != nil {
		return err
	}
	ids := make([]uuid.UUID, len(chains))
	for i := range chains {
		ids[i] = chains[i].ID
	}
	jobs, err := s.store.ChainJobs(r.Context(), ids)
	if err != nil {
		return err
	}
	byChain := make(map[uuid.UUID][]store.Job)
	for _, j := range jobs {
		byChain[j.ChainID] = append(byChain[j.ChainID], j)
	}
	resp := ChainsResponse{Chains: make([]ChainView, len(chains)), RestorePoints: []RestorePoint{}}
	for i, c := range chains {
		cj := byChain[c.ID]
		if cj == nil {
			cj = []store.Job{}
		}
		resp.Chains[i] = ChainView{Chain: c, Jobs: cj}
		if c.Status == store.ChainDeleted {
			continue
		}
		for _, j := range cj {
			if _, err := restoreSequence(cj, j.ID); err == nil {
				resp.RestorePoints = append(resp.RestorePoints, RestorePoint{JobID: j.ID, Type: j.Type, At: j.FinishedAt})
			}
		}
	}
	slices.SortFunc(resp.RestorePoints, func(a, b RestorePoint) int { return b.At.Compare(a.At) })
	return writeJSON(w, http.StatusOK, resp)
}

// restoreSequence returns the backups to apply to restore target, full
// first, by following base jobs back to the chain's full backup. It works
// for chained and differential incrementals alike.
func restoreSequence(chainJobs []store.Job, target uuid.UUID) ([]store.Job, error) {
	byID := make(map[uuid.UUID]*store.Job, len(chainJobs))
	for i := range chainJobs {
		byID[chainJobs[i].ID] = &chainJobs[i]
	}
	var seq []store.Job
	for id := target; ; {
		j, ok := byID[id]
		switch {
		case !ok:
			return nil, fmt.Errorf("backup %s is missing from the chain", id)
		case !j.Status.Succeeded():
			return nil, fmt.Errorf("backup %s did not succeed (%s)", id, j.Status)
		case len(seq) > len(chainJobs):
			return nil, errors.New("base jobs form a cycle")
		}
		seq = append(seq, *j)
		if j.Type == api.JobFull {
			break
		}
		if j.BaseJobID == uuid.Nil() {
			return nil, fmt.Errorf("incremental %s has no base", id)
		}
		id = j.BaseJobID
	}
	slices.Reverse(seq)
	return seq, nil
}

// RestoreStep is one step of a restore guide.
type RestoreStep struct {
	Title    string `json:"title"`
	Commands string `json:"commands"`
}

// RestoreEnv is where a restore guide runs: the host and the replica
// container of the project, as last reported by the agent.
type RestoreEnv struct {
	Host       string `json:"host"`
	BackupRoot string `json:"backup_root,omitzero"`
	Container  string `json:"container,omitzero"`
	Image      string `json:"image,omitzero"`
	// DataDir is the host directory mounted as the container's data
	// directory; empty when unknown.
	DataDir string `json:"data_dir,omitzero"`
	// Dir receives the extracted and prepared backups.
	Dir string `json:"dir"`
}

// RestoreGuide is the response of GET /jobs/{id}/restore (PRD J-6).
type RestoreGuide struct {
	Target   store.Job     `json:"target"`
	Sequence []store.Job   `json:"sequence"`
	Env      RestoreEnv    `json:"environment"`
	Steps    []RestoreStep `json:"steps"`
	// Notes are what the operator must check or adapt.
	Notes []string `json:"notes"`
}

// mysqlDataDir is the data directory inside replica containers.
const mysqlDataDir = "/var/lib/mysql"

func (s *Server) restoreGuide(w http.ResponseWriter, r *http.Request) error {
	j, err := s.pathJob(r)
	if err != nil {
		return err
	}
	g, err := s.restorePlan(r.Context(), j, requestLang(r))
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, g)
}

// restorePlan generates the commands that restore a backup point: extract
// every backup of the sequence, prepare the full backup and apply the
// incrementals in order, then copy back and point replication at the
// recorded master position. The commands use the project's container when
// its data directory mount is known. Titles and notes are in language l.
func (s *Server) restorePlan(ctx context.Context, j *store.Job, l lang) (*RestoreGuide, error) {
	if j.ChainID == uuid.Nil() {
		return nil, badRequest("job %s has no backup chain", j.ID)
	}
	chain, err := s.store.ChainJobs(ctx, []uuid.UUID{j.ChainID})
	if err != nil {
		return nil, err
	}
	seq, err := restoreSequence(chain, j.ID)
	if err != nil {
		return nil, newError(http.StatusConflict, "NOT_RESTORABLE", "%v", err)
	}
	env, err := s.restoreEnv(ctx, j)
	if err != nil {
		return nil, err
	}
	steps, notes := restoreSteps(seq, env, s.stamp(j.FinishedAt), l)
	return &RestoreGuide{Target: *j, Sequence: seq, Env: env, Steps: steps, Notes: notes}, nil
}

// stamp formats t for names of restore directories.
func (s *Server) stamp(t time.Time) string { return t.In(s.cfg.Location).Format("20060102-1504") }

func (s *Server) restoreEnv(ctx context.Context, j *store.Job) (RestoreEnv, error) {
	h, err := s.store.Host(ctx, j.HostID)
	if err != nil {
		return RestoreEnv{}, err
	}
	env := RestoreEnv{Host: h.Name, BackupRoot: h.BackupRoot, Dir: "/restore"}
	if h.BackupRoot != "" {
		// The backup root is mounted at the same path in every container.
		env.Dir = path.Join(h.BackupRoot, "restore", j.Project+"-"+s.stamp(j.FinishedAt))
	}
	if j.ProjectID == 0 {
		return env, nil
	}
	p, err := s.store.Project(ctx, j.ProjectID)
	if errors.Is(err, store.ErrNotFound) {
		return env, nil
	}
	if err != nil {
		return env, err
	}
	env.Container = p.Container
	if i := slices.IndexFunc(h.Containers, func(c api.Container) bool { return c.Name == p.Container }); i >= 0 {
		c := h.Containers[i]
		env.Image = c.Image
		for _, m := range c.Mounts {
			if path.Clean(m.Destination) == mysqlDataDir {
				env.DataDir = m.Source
			}
		}
	}
	return env, nil
}

func restoreSteps(seq []store.Job, env RestoreEnv, stamp string, l lang) ([]RestoreStep, []string) {
	inContainer := env.BackupRoot != "" && env.Container != "" && env.Image != "" && env.DataDir != ""
	q := shellQuote
	// run prefixes commands executed inside the running replica container,
	// which has xtrabackup, xbstream and zstd and sees the backup root.
	run := func(cmd string) string {
		if inContainer {
			return "podman exec " + q(env.Container) + " " + cmd
		}
		return cmd
	}
	full := env.Dir + "/full"
	var b strings.Builder
	dirs := []string{q(full)}
	for i := range seq[1:] {
		dirs = append(dirs, q(fmt.Sprintf("%s/incr%d", env.Dir, i+1)))
	}
	fmt.Fprintf(&b, "%s\n", run("mkdir -p "+strings.Join(dirs, " ")))
	extract := func(src, dst string) {
		pipe := fmt.Sprintf("zstd -dc %s | xbstream -x -C %s", q(src), q(dst))
		if inContainer {
			pipe = "sh -c " + q(pipe)
		}
		fmt.Fprintf(&b, "%s\n", run(pipe))
	}
	extract(seq[0].Path, full)
	for i, j := range seq[1:] {
		extract(j.Path, fmt.Sprintf("%s/incr%d", env.Dir, i+1))
	}
	title := l.pick("解压备份（需要 zstd 与 xtrabackup 2.4 的 xbstream）", "Extract the backups (requires zstd and the xbstream of xtrabackup 2.4)")
	if inContainer {
		title = fmt.Sprintf(l.pick("在容器 %s 内解压备份（使用容器内的 zstd 与 xbstream）", "Extract the backups in container %s (with its zstd and xbstream)"), env.Container)
	}
	steps := []RestoreStep{{Title: title, Commands: b.String()}}

	b.Reset()
	if len(seq) == 1 {
		fmt.Fprintf(&b, "%s\n", run("xtrabackup --prepare --target-dir="+q(full)))
	} else {
		fmt.Fprintf(&b, "%s\n", run("xtrabackup --prepare --apply-log-only --target-dir="+q(full)))
		for i := range seq[1:] {
			only := " --apply-log-only"
			if i == len(seq)-2 {
				only = "" // the last incremental also rolls back uncommitted transactions
			}
			fmt.Fprintf(&b, "%s\n", run(fmt.Sprintf("xtrabackup --prepare%s --target-dir=%s --incremental-dir=%s", only, q(full), q(fmt.Sprintf("%s/incr%d", env.Dir, i+1)))))
		}
	}
	steps = append(steps, RestoreStep{Title: l.pick("prepare 全量并按顺序应用增量", "Prepare the full backup and apply the incrementals in order"), Commands: b.String()})

	var notes []string
	if inContainer {
		old := env.DataDir + ".before-restore-" + stamp
		steps = append(steps,
			RestoreStep{
				Title: l.pick("停止容器，把原数据目录改名保留", "Stop the container and keep the original data directory under a new name"),
				Commands: fmt.Sprintf("podman stop %s\nmv %s %s\nmkdir %s\n",
					q(env.Container), q(env.DataDir), q(old), q(env.DataDir)),
			},
			RestoreStep{
				Title: l.pick("用同一镜像的临时容器拷回数据并修正属主", "Copy the data back with a temporary container of the same image and fix ownership"),
				Commands: fmt.Sprintf("podman run --rm --user 0 --entrypoint sh \\\n  -v %s:%s -v %s:%s \\\n  %s \\\n  -c %s\n",
					q(env.BackupRoot), q(env.BackupRoot), q(env.DataDir), mysqlDataDir, q(env.Image),
					q(fmt.Sprintf("xtrabackup --copy-back --target-dir=%s --datadir=%s && chown -R mysql:mysql %s", q(full), mysqlDataDir, mysqlDataDir))),
			},
			RestoreStep{
				Title:    l.pick("启动容器并检查日志", "Start the container and check its log"),
				Commands: fmt.Sprintf("podman start %s\npodman logs --tail 50 %s\n", q(env.Container), q(env.Container)),
			},
		)
		raw := l.pick("未知", "unknown")
		if seq[0].RawSize > 0 {
			raw = sizeText(seq[0].RawSize)
		}
		notes = append(notes,
			fmt.Sprintf(l.pick("恢复期间容器停止，该项目的计划备份会失败：开始前在“主机”页面暂停 %s 的调度，完成后恢复。",
				"The container is stopped during the restore, so scheduled backups of the project would fail: pause scheduling of %s on the Hosts page first and resume it afterwards."), env.Host),
			fmt.Sprintf(l.pick("恢复目录 %s 在备份盘上，需要约等于数据库原始大小的可用空间（全量原始大小 %s）。",
				"The restore directory %s is on the backup disk and needs about as much free space as the raw database (raw size of the full backup: %s)."), env.Dir, raw),
			fmt.Sprintf(l.pick("原数据目录改名为 %s 保留在数据盘上；如果 %s 本身是挂载点，mv 会失败，改为清空其内容。",
				"The original data directory is kept on the data disk as %s. If %s is itself a mount point, mv fails; empty its contents instead."), old, env.DataDir),
			l.pick("如果容器的挂载带 SELinux 标签（:Z），临时容器的 -v 参数也要加上相同的选项。",
				"If the container's mounts carry an SELinux label (:Z), add the same option to the -v arguments of the temporary container."),
		)
	} else {
		steps = append(steps, RestoreStep{
			Title:    l.pick("停止 MySQL，清空数据目录后拷回", "Stop MySQL, empty the data directory and copy back"),
			Commands: fmt.Sprintf("xtrabackup --copy-back --target-dir=%s --datadir=%s\nchown -R mysql:mysql %s\n", q(full), mysqlDataDir, mysqlDataDir),
		})
		switch {
		case env.Container == "":
			notes = append(notes, fmt.Sprintf(l.pick("找不到备份所属的项目，命令按在宿主机上直接执行生成：请把数据目录 %s 换成实际路径。",
				"The backup's project was not found, so the commands are for running directly on the host: replace the data directory %s with the actual path."), mysqlDataDir))
		default:
			notes = append(notes, fmt.Sprintf(l.pick("agent 未上报容器 %s 的数据目录挂载（%s），命令按在宿主机上执行生成："+
				"请在宿主机安装 xtrabackup 2.4，并把数据目录换成该挂载的宿主机路径。",
				"The agent has not reported the data directory mount (%[2]s) of container %[1]s, so the commands are for running on the host: "+
					"install xtrabackup 2.4 on the host and replace the data directory with the host path of that mount."), env.Container, mysqlDataDir))
		}
	}

	last := seq[len(seq)-1]
	if si := last.SlaveInfo; si != nil && si.MasterLogFile != "" {
		b.Reset()
		if inContainer {
			fmt.Fprintf(&b, "podman exec -it %s mysql -uroot -p\n", q(env.Container))
		}
		fmt.Fprintf(&b, "CHANGE MASTER TO MASTER_HOST='<master>', MASTER_USER='<user>', MASTER_PASSWORD='<password>',\n"+
			"  MASTER_LOG_FILE='%s', MASTER_LOG_POS=%d;\nSTART SLAVE;\nSHOW SLAVE STATUS\\G\n", si.MasterLogFile, si.MasterLogPos)
		steps = append(steps, RestoreStep{Title: l.pick("按备份记录的主库位点重建复制", "Rebuild replication from the master position recorded by the backup"), Commands: b.String()})
		notes = append(notes, l.pick("把 CHANGE MASTER 中的 <master>、<user>、<password> 换成主库地址和复制账号。",
			"Replace <master>, <user> and <password> in CHANGE MASTER with the master address and the replication account."))
		if si.GTIDExecuted != "" {
			notes = append(notes, fmt.Sprintf(l.pick("主库开启了 GTID：也可以改用 SET GLOBAL gtid_purged='%s' 加 MASTER_AUTO_POSITION=1 建立复制。",
				"The master uses GTID: you can instead set up replication with SET GLOBAL gtid_purged='%s' and MASTER_AUTO_POSITION=1."), si.GTIDExecuted))
		}
	}
	if inContainer {
		steps = append(steps, RestoreStep{
			Title:    l.pick("复制追上并确认数据无误后清理", "Clean up once replication has caught up and the data is verified"),
			Commands: fmt.Sprintf("rm -rf %s\nrm -rf %s\n", q(env.Dir), q(env.DataDir+".before-restore-"+stamp)),
		})
	}
	return steps, notes
}

// sizeText formats a byte count in binary units.
func sizeText(n int64) string {
	if n <= 0 {
		return "未知"
	}
	v, units := float64(n), []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// shellQuote quotes a path for a POSIX shell.
func shellQuote(p string) string {
	if p != "" && strings.IndexFunc(p, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-", r))
	}) < 0 {
		return filepath.Clean(p)
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}
