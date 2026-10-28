// Package worker schedules and runs backups on one host, strictly one at a
// time (PRD 6.3.1).
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"
	"uuid"

	"github.com/rodericliu/backup/internal/agent/cache"
	"github.com/rodericliu/backup/internal/agent/executor"
	"github.com/rodericliu/backup/internal/agent/retention"
	"github.com/rodericliu/backup/internal/api"
	"github.com/rodericliu/backup/internal/schedule"
)

// ErrUnknownProject is returned by Enqueue for projects not in the configuration.
var ErrUnknownProject = errors.New("unknown project")

// Runner executes one job. *executor.Executor implements it.
type Runner interface {
	Run(ctx context.Context, req *executor.Request)
}

// Options configures a Worker.
type Options struct {
	DB       *cache.DB
	Runner   Runner
	Cleaner  *retention.Cleaner
	Root     string // backup root
	Location *time.Location
	Logger   *slog.Logger
	// Tick is how often the schedule is evaluated; default 5s.
	Tick time.Duration
	// CleanInterval is how often retention runs for all projects; default 1h.
	CleanInterval time.Duration
	// Now returns the current time; default time.Now.
	Now func() time.Time
}

// Worker owns the host's job queue and its single execution slot.
type Worker struct {
	opts     Options
	log      *slog.Logger
	projects atomic.Pointer[[]api.ProjectConfig]
	wake     chan struct{}

	mu       sync.Mutex
	queue    []*api.Job
	current  *running
	attempts map[string]int // failed full attempts per project, for retries

	// lastSchedule and lastClean are only used by the loop goroutine.
	lastSchedule time.Time
	lastClean    time.Time
}

type running struct {
	job      api.Job
	cancel   context.CancelCauseFunc
	progress executor.Progress
}

// New returns a worker. Call SetProjects before Run.
func New(opts Options) *Worker {
	if opts.Tick <= 0 {
		opts.Tick = 5 * time.Second
	}
	if opts.CleanInterval <= 0 {
		opts.CleanInterval = time.Hour
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Location == nil {
		opts.Location = time.Local
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	w := &Worker{
		opts:     opts,
		log:      opts.Logger,
		wake:     make(chan struct{}, 1),
		attempts: make(map[string]int),
	}
	w.projects.Store(&[]api.ProjectConfig{})
	return w
}

// SetProjects replaces the project configuration. It takes effect at the
// next schedule evaluation; queued jobs of removed or disabled projects are
// skipped when they come up.
func (w *Worker) SetProjects(projects []api.ProjectConfig) {
	ps := slices.Clone(projects)
	w.projects.Store(&ps)
	w.signal()
}

func (w *Worker) project(name string) (*api.ProjectConfig, bool) {
	for _, p := range *w.projects.Load() {
		if p.Name == name {
			return &p, true
		}
	}
	return nil, false
}

func (w *Worker) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run recovers from a previous run and then schedules and executes jobs until
// ctx is done. A running job is interrupted on shutdown.
func (w *Worker) Run(ctx context.Context) error {
	if err := w.recover(ctx); err != nil {
		return fmt.Errorf("recover: %w", err)
	}
	w.lastSchedule = w.opts.Now()
	t := time.NewTicker(w.opts.Tick)
	defer t.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		if w.runOnce(ctx) {
			continue
		}
		w.maybeClean(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		case <-w.wake:
		}
	}
}

// runOnce enqueues due slots and runs the next ready job, if any.
func (w *Worker) runOnce(ctx context.Context) bool {
	now := w.opts.Now()
	w.schedule(ctx, now)
	job := w.next(now)
	if job == nil {
		return false
	}
	w.execute(ctx, job)
	return true
}

// schedule enqueues the slots planned in (lastSchedule, now]. Slots missed
// while the agent was down are never backfilled (PRD S-8).
func (w *Worker) schedule(ctx context.Context, now time.Time) {
	if !now.After(w.lastSchedule) {
		return
	}
	for _, s := range schedule.Slots(*w.projects.Load(), w.opts.Location, w.lastSchedule, now) {
		w.add(ctx, &api.Job{
			ID: uuid.NewV7(), Project: s.Project, Type: s.Type,
			Trigger: api.TriggerSchedule, Status: api.StatusPending, ScheduledAt: s.At,
		})
	}
	w.lastSchedule = now
}

// Enqueue adds a manual job (PRD P-8). It runs after the jobs already due and
// never preempts the running job.
func (w *Worker) Enqueue(ctx context.Context, project string, typ api.JobType) (uuid.UUID, error) {
	if _, ok := w.project(project); !ok {
		return uuid.Nil(), fmt.Errorf("%w: %s", ErrUnknownProject, project)
	}
	job := &api.Job{
		ID: uuid.NewV7(), Project: project, Type: typ,
		Trigger: api.TriggerManual, Status: api.StatusPending, ScheduledAt: w.opts.Now(),
	}
	w.add(ctx, job)
	w.signal()
	return job.ID, nil
}

// add queues a pending job, superseding a pending job of the same project and
// type (PRD S-4).
func (w *Worker) add(ctx context.Context, job *api.Job) {
	w.mu.Lock()
	var old *api.Job
	if i := slices.IndexFunc(w.queue, func(q *api.Job) bool {
		return q.Project == job.Project && q.Type == job.Type
	}); i >= 0 {
		old = w.queue[i]
		w.queue = slices.Delete(w.queue, i, i+1)
	}
	w.queue = append(w.queue, job)
	w.mu.Unlock()

	if old != nil {
		w.skip(ctx, old, api.ErrSuperseded, fmt.Sprintf("superseded by job %s scheduled at %s", job.ID, job.ScheduledAt.Format(time.RFC3339)))
	}
	w.save(ctx, job)
}

// next removes and returns the first ready job: planned time, then full
// before incremental, then project priority (PRD S-3).
func (w *Worker) next(now time.Time) *api.Job {
	w.mu.Lock()
	defer w.mu.Unlock()
	best := -1
	for i, j := range w.queue {
		if j.ScheduledAt.After(now) {
			continue
		}
		if best < 0 || schedule.Compare(w.slot(j), w.slot(w.queue[best])) < 0 {
			best = i
		}
	}
	if best < 0 {
		return nil
	}
	job := w.queue[best]
	w.queue = slices.Delete(w.queue, best, best+1)
	return job
}

func (w *Worker) slot(j *api.Job) schedule.Slot {
	s := schedule.Slot{Project: j.Project, Type: j.Type, At: j.ScheduledAt}
	if p, ok := w.project(j.Project); ok {
		s.Priority = p.Priority
	}
	return s
}

// Cancel cancels a pending or running job (PRD E-11). It reports whether the
// job was found.
func (w *Worker) Cancel(ctx context.Context, id uuid.UUID) bool {
	w.mu.Lock()
	if w.current != nil && w.current.job.ID == id {
		w.current.cancel(executor.ErrCanceled)
		w.mu.Unlock()
		return true
	}
	i := slices.IndexFunc(w.queue, func(j *api.Job) bool { return j.ID == id })
	if i < 0 {
		w.mu.Unlock()
		return false
	}
	job := w.queue[i]
	w.queue = slices.Delete(w.queue, i, i+1)
	w.mu.Unlock()

	job.Status, job.ErrorCode, job.ErrorMessage = api.StatusCanceled, api.ErrCanceled, "canceled before start"
	job.FinishedAt = w.opts.Now()
	w.save(ctx, job)
	return true
}

// Snapshot is the worker state shown on the dashboard (PRD D-1).
type Snapshot struct {
	Current  *api.Job
	Progress executor.Progress
	Queue    []api.Job
}

// Status returns a copy of the current job and queue.
func (w *Worker) Status() Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	var s Snapshot
	if w.current != nil {
		j := w.current.job
		s.Current, s.Progress = &j, w.current.progress
	}
	s.Queue = make([]api.Job, len(w.queue))
	for i, j := range w.queue {
		s.Queue[i] = *j
	}
	slices.SortFunc(s.Queue, func(a, b api.Job) int { return schedule.Compare(w.slot(&a), w.slot(&b)) })
	return s
}

// execute prepares the chain and base of a job, runs it and records the result.
func (w *Worker) execute(ctx context.Context, job *api.Job) {
	p, ok := w.project(job.Project)
	switch {
	case !ok:
		w.skip(ctx, job, "", "project no longer configured")
		return
	case !p.Enabled():
		w.skip(ctx, job, "", "project disabled")
		return
	}

	req, chain, skip := w.prepare(ctx, p, job)
	if skip != nil {
		w.skip(ctx, job, skip.code, skip.msg)
		return
	}

	// Shutdown interrupts the job with ErrShutdown; Cancel with ErrCanceled.
	jobCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, func() { cancel(executor.ErrShutdown) })
	defer stop()
	defer cancel(nil)

	job.Status = api.StatusRunning
	job.StartedAt = w.opts.Now()
	w.mu.Lock()
	w.current = &running{job: *job, cancel: cancel}
	w.mu.Unlock()
	w.save(ctx, job)

	req.Progress = func(pr executor.Progress) {
		w.mu.Lock()
		if w.current != nil {
			w.current.progress = pr
		}
		w.mu.Unlock()
		if err := w.opts.DB.AddEvent(context.WithoutCancel(ctx), job.ID, cache.EventProgress, pr); err != nil {
			w.log.Warn("record progress", "job", job.ID.String(), "err", err)
		}
	}
	w.opts.Runner.Run(jobCtx, req)

	w.mu.Lock()
	w.current = nil
	w.mu.Unlock()
	w.finish(context.WithoutCancel(ctx), p, job, chain)
}

type skipReason struct {
	code api.ErrorCode
	msg  string
}

// prepare chooses the chain, directories and base of a job. Incrementals
// without a usable full backup are converted to full backups (PRD S-7) unless
// the newest full failed, in which case they are skipped (PRD S-5).
func (w *Worker) prepare(ctx context.Context, p *api.ProjectConfig, job *api.Job) (*executor.Request, *cache.Chain, *skipReason) {
	db := w.opts.DB
	if job.Type == api.JobIncr {
		chains, err := db.Chains(ctx, p.Name)
		if err != nil {
			return nil, nil, &skipReason{api.ErrNoBase, err.Error()}
		}
		cur, err := db.CurrentChain(ctx, p.Name)
		switch {
		case errors.Is(err, cache.ErrNotFound) && (len(chains) == 0 || chains[len(chains)-1].Status != cache.ChainFailed):
			w.log.Info("no successful full backup; running full instead of incremental", "project", p.Name, "job", job.ID.String())
			job.Type = api.JobFull
		case err != nil && !errors.Is(err, cache.ErrNotFound):
			return nil, nil, &skipReason{api.ErrNoBase, err.Error()}
		case chains[len(chains)-1].Status == cache.ChainFailed:
			return nil, nil, &skipReason{api.ErrNoBase, "the newest full backup failed"}
		default:
			return w.prepareIncr(ctx, p, job, cur)
		}
	}
	return w.prepareFull(ctx, p, job)
}

func (w *Worker) prepareFull(ctx context.Context, p *api.ProjectConfig, job *api.Job) (*executor.Request, *cache.Chain, *skipReason) {
	now := w.opts.Now()
	chain := &cache.Chain{
		ID: uuid.NewV7(), Project: p.Name, Mode: p.IncrMode, FullJobID: job.ID,
		Status: cache.ChainRunning, StartedAt: now,
		Dir: w.chainDir(p.Name, now),
	}
	if err := w.opts.DB.SaveChain(ctx, chain); err != nil {
		return nil, nil, &skipReason{api.ErrCompress, err.Error()}
	}
	job.ChainID, job.BaseJobID = chain.ID, uuid.Nil()
	req := &executor.Request{
		Project: *p,
		Job:     job,
		JobDir:  filepath.Join(chain.Dir, "00-full"),
	}
	if last, err := w.opts.DB.LastSucceeded(ctx, p.Name, api.JobFull); err == nil {
		req.EstimatedSize = last.CompressedSize
	}
	return req, chain, nil
}

func (w *Worker) prepareIncr(ctx context.Context, p *api.ProjectConfig, job *api.Job, chain *cache.Chain) (*executor.Request, *cache.Chain, *skipReason) {
	db := w.opts.DB
	full, err := db.Job(ctx, chain.FullJobID)
	if err != nil {
		return nil, nil, &skipReason{api.ErrNoBase, fmt.Sprintf("full job of chain %s: %v", chain.ID, err)}
	}
	base := full
	if chain.Mode != api.IncrDifferential {
		if base, err = db.LastInChain(ctx, chain.ID); err != nil {
			return nil, nil, &skipReason{api.ErrNoBase, fmt.Sprintf("last backup of chain %s: %v", chain.ID, err)}
		}
	}
	jobs, err := db.ChainJobs(ctx, chain.ID)
	if err != nil {
		return nil, nil, &skipReason{api.ErrNoBase, err.Error()}
	}
	job.ChainID, job.BaseJobID = chain.ID, base.ID
	req := &executor.Request{
		Project:  *p,
		Job:      job,
		JobDir:   filepath.Join(chain.Dir, fmt.Sprintf("%02d-incr", len(jobs))),
		BaseDir:  filepath.Dir(base.Path),
		PurgeLSN: full.ToLSN,
	}
	if last, err := db.LastSucceeded(ctx, p.Name, api.JobIncr); err == nil {
		req.EstimatedSize = last.CompressedSize
	}
	return req, chain, nil
}

// chainDir returns <root>/<project>/<YYYYMMDD-HHMM>-chain, adding seconds if
// a chain already started in the same minute.
func (w *Worker) chainDir(project string, t time.Time) string {
	t = t.In(w.opts.Location)
	dir := filepath.Join(w.opts.Root, project, t.Format("20060102-1504")+"-chain")
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return dir
	}
	return filepath.Join(w.opts.Root, project, t.Format("20060102-150405.000")+"-chain")
}

// finish records the outcome, updates the chain, retries failed full backups
// (PRD S-5) and applies retention after successful backups.
func (w *Worker) finish(ctx context.Context, p *api.ProjectConfig, job *api.Job, chain *cache.Chain) {
	w.save(ctx, job)

	ok := job.Status.Succeeded()
	if ok {
		chain.LastJobAt = job.FinishedAt
	}
	if job.Type == api.JobFull {
		chain.Status = cache.ChainActive
		if !ok {
			chain.Status = cache.ChainFailed
		}
	}
	if err := w.opts.DB.SaveChain(ctx, chain); err != nil {
		w.log.Error("save chain", "chain", chain.ID.String(), "err", err)
	}

	if job.Type == api.JobFull {
		w.mu.Lock()
		if ok {
			delete(w.attempts, p.Name)
		} else if job.Trigger != api.TriggerRetry {
			w.attempts[p.Name] = 1
		} else {
			w.attempts[p.Name]++
		}
		attempts := w.attempts[p.Name]
		w.mu.Unlock()
		retryable := job.Status == api.StatusFailed && job.ErrorCode != api.ErrInterrupted
		if !ok && retryable && attempts <= p.RetryCount && ctx.Err() == nil {
			retry := &api.Job{
				ID: uuid.NewV7(), Project: p.Name, Type: api.JobFull, Trigger: api.TriggerRetry,
				Status: api.StatusPending, ScheduledAt: w.opts.Now().Add(p.RetryInterval.Std()),
			}
			w.log.Info("scheduling full backup retry", "project", p.Name, "attempt", attempts, "at", retry.ScheduledAt)
			w.add(ctx, retry)
		}
	}

	if ok && w.opts.Cleaner != nil {
		if _, err := w.opts.Cleaner.Clean(ctx, p); err != nil {
			w.log.Error("retention", "project", p.Name, "err", err)
		}
	}
}

func (w *Worker) maybeClean(ctx context.Context) {
	now := w.opts.Now()
	if w.opts.Cleaner == nil || now.Sub(w.lastClean) < w.opts.CleanInterval {
		return
	}
	w.lastClean = now
	for _, p := range *w.projects.Load() {
		if _, err := w.opts.Cleaner.Clean(ctx, &p); err != nil {
			w.log.Error("retention", "project", p.Name, "err", err)
		}
	}
}

func (w *Worker) skip(ctx context.Context, job *api.Job, code api.ErrorCode, msg string) {
	job.Status, job.ErrorCode, job.ErrorMessage = api.StatusSkipped, code, msg
	job.FinishedAt = w.opts.Now()
	w.log.Info("job skipped", "job", job.ID.String(), "project", job.Project, "type", job.Type, "error_code", code, "reason", msg)
	w.save(ctx, job)
}

func (w *Worker) save(ctx context.Context, job *api.Job) {
	if err := w.opts.DB.SaveJob(ctx, job); err != nil {
		w.log.Error("save job", "job", job.ID.String(), "err", err)
		return
	}
	ev := map[string]any{"status": job.Status}
	if job.ErrorCode != "" {
		ev["error_code"] = job.ErrorCode
	}
	if err := w.opts.DB.AddEvent(ctx, job.ID, cache.EventStatus, ev); err != nil {
		w.log.Error("record status event", "job", job.ID.String(), "err", err)
	}
}

// recover handles state left by a previous process (PRD S-8): running jobs are
// marked interrupted and their partial files removed, queued jobs are skipped,
// and stale credential files are deleted.
func (w *Worker) recover(ctx context.Context) error {
	db := w.opts.DB
	now := w.opts.Now()
	jobs, err := db.JobsWithStatus(ctx, api.StatusRunning, api.StatusPending)
	if err != nil {
		return err
	}
	for i := range jobs {
		job := &jobs[i]
		if job.Status == api.StatusRunning {
			job.Status, job.ErrorCode = api.StatusFailed, api.ErrInterrupted
			job.ErrorMessage = "agent restarted while the job was running"
			if job.Path != "" {
				if err := executor.RemoveIncomplete(filepath.Dir(job.Path)); err != nil {
					w.log.Error("remove incomplete backup", "job", job.ID.String(), "err", err)
				}
			}
			if job.Type == api.JobFull && job.ChainID != uuid.Nil() {
				if chain, err := db.Chain(ctx, job.ChainID); err == nil {
					chain.Status = cache.ChainFailed
					if err := db.SaveChain(ctx, chain); err != nil {
						return err
					}
				}
			}
		} else {
			job.Status, job.ErrorCode = api.StatusSkipped, api.ErrInterrupted
			job.ErrorMessage = "agent restarted before the job started"
		}
		job.FinishedAt = now
		w.log.Warn("recovered job from previous run", "job", job.ID.String(), "project", job.Project, "status", job.Status)
		w.save(ctx, job)
	}

	creds := filepath.Join(w.opts.Root, executor.CredentialsDir)
	entries, err := os.ReadDir(creds)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read credentials dir: %w", err)
	}
	for _, e := range entries {
		if err := os.Remove(filepath.Join(creds, e.Name())); err != nil {
			w.log.Error("remove stale credentials file", "file", e.Name(), "err", err)
		}
	}
	return nil
}

// Projects returns a copy of the current project configuration.
func (w *Worker) Projects() []api.ProjectConfig {
	return slices.Clone(*w.projects.Load())
}
