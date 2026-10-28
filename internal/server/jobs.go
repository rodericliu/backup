package server

import (
	"errors"
	"fmt"
	"net/http"
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

// RestoreGuide is the response of GET /jobs/{id}/restore (PRD J-6).
type RestoreGuide struct {
	Target   store.Job     `json:"target"`
	Sequence []store.Job   `json:"sequence"`
	Steps    []RestoreStep `json:"steps"`
}

// restoreGuide generates the commands that restore a backup point: extract
// every backup of the sequence, prepare the full backup and apply the
// incrementals in order, then copy back and point replication at the
// recorded master position.
func (s *Server) restoreGuide(w http.ResponseWriter, r *http.Request) error {
	j, err := s.pathJob(r)
	if err != nil {
		return err
	}
	if j.ChainID == uuid.Nil() {
		return badRequest("job %s has no backup chain", j.ID)
	}
	chain, err := s.store.ChainJobs(r.Context(), []uuid.UUID{j.ChainID})
	if err != nil {
		return err
	}
	seq, err := restoreSequence(chain, j.ID)
	if err != nil {
		return newError(http.StatusConflict, "NOT_RESTORABLE", "%v", err)
	}
	return writeJSON(w, http.StatusOK, RestoreGuide{Target: *j, Sequence: seq, Steps: restoreSteps(seq, "/restore")})
}

func restoreSteps(seq []store.Job, dir string) []RestoreStep {
	full := dir + "/full"
	var b strings.Builder
	fmt.Fprintf(&b, "mkdir -p %s\n", full)
	for i := range seq[1:] {
		fmt.Fprintf(&b, "mkdir -p %s/incr%d\n", dir, i+1)
	}
	fmt.Fprintf(&b, "zstd -dc %s | xbstream -x -C %s\n", shellQuote(seq[0].Path), full)
	for i, j := range seq[1:] {
		fmt.Fprintf(&b, "zstd -dc %s | xbstream -x -C %s/incr%d\n", shellQuote(j.Path), dir, i+1)
	}
	steps := []RestoreStep{{Title: "解压备份（需要 zstd 与 xtrabackup 2.4 的 xbstream）", Commands: b.String()}}

	b.Reset()
	if len(seq) == 1 {
		fmt.Fprintf(&b, "xtrabackup --prepare --target-dir=%s\n", full)
	} else {
		fmt.Fprintf(&b, "xtrabackup --prepare --apply-log-only --target-dir=%s\n", full)
		for i := range seq[1:] {
			only := " --apply-log-only"
			if i == len(seq)-2 {
				only = "" // the last incremental also rolls back uncommitted transactions
			}
			fmt.Fprintf(&b, "xtrabackup --prepare%s --target-dir=%s --incremental-dir=%s/incr%d\n", only, full, dir, i+1)
		}
	}
	steps = append(steps, RestoreStep{Title: "prepare 全量并按顺序应用增量", Commands: b.String()})

	steps = append(steps, RestoreStep{
		Title:    "停止 MySQL，清空数据目录后拷回",
		Commands: fmt.Sprintf("xtrabackup --copy-back --target-dir=%s --datadir=/var/lib/mysql\nchown -R mysql:mysql /var/lib/mysql\n", full),
	})
	last := seq[len(seq)-1]
	if si := last.SlaveInfo; si != nil && si.MasterLogFile != "" {
		steps = append(steps, RestoreStep{
			Title: "按备份记录的主库位点重建复制",
			Commands: fmt.Sprintf("CHANGE MASTER TO MASTER_HOST='<master>', MASTER_USER='<user>', MASTER_PASSWORD='<password>',\n"+
				"  MASTER_LOG_FILE='%s', MASTER_LOG_POS=%d;\nSTART SLAVE;\n", si.MasterLogFile, si.MasterLogPos),
		})
	}
	return steps
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
