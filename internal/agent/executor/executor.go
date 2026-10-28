// Package executor runs one xtrabackup job: prechecks, streaming backup with
// zstd compression inside the container, result validation, metadata
// extraction and cleanup (PRD 6.4).
package executor

import (
	"bufio"
	"cmp"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rodericliu/backup/internal/agent/mysql"
	"github.com/rodericliu/backup/internal/agent/podman"
	"github.com/rodericliu/backup/internal/api"
)

// File names inside a job directory (PRD 6.4.3).
const (
	DataFile        = "backup.xbstream.zst"
	CheckpointsFile = "xtrabackup_checkpoints"
	InfoFile        = "xtrabackup_info"
	SlaveInfoFile   = "xtrabackup_slave_info"
	LogFile         = "backup.log"
	MetaFile        = "meta.json"
	pidFile         = "xtrabackup.pid"
)

// CredentialsDir is the directory below the backup root that holds temporary
// MySQL option files. The backup root is mounted into every container.
const CredentialsDir = ".agent"

// Cancellation causes. The worker cancels a job's context with ErrCanceled
// for a user request and with ErrShutdown when the agent stops.
var (
	ErrCanceled = errors.New("job canceled")
	ErrShutdown = errors.New("agent shutting down")
	errTimeout  = errors.New("job timed out")
)

// spaceFactor is the free space required relative to the estimated size (PRD E-3).
const spaceFactor = 1.5

// cleanupTimeout bounds finalization steps that run after cancellation.
const cleanupTimeout = 2 * time.Minute

// Podman is the subset of *podman.Client used by the executor.
type Podman interface {
	Inspect(ctx context.Context, name string) (*podman.Container, error)
	Exec(ctx context.Context, container string, args ...string) *exec.Cmd
	Output(ctx context.Context, container string, args ...string) ([]byte, error)
}

// Options configures an Executor.
type Options struct {
	BackupRoot    string
	Zstd          string // zstd binary inside the container; default "zstd"
	ZstdThreads   int
	ZstdLevel     int
	LowIOPriority bool
	// ProgressInterval is how often Request.Progress is called; default 5s (PRD E-6).
	ProgressInterval time.Duration
}

// Executor runs backup jobs. It is safe for use by one job at a time.
type Executor struct {
	Podman Podman
	Opts   Options
	Logger *slog.Logger
	// FreeSpace reports available bytes at a path; defaults to the statfs-based
	// FreeSpace function.
	FreeSpace func(path string) (int64, error)
	// Now returns the current time; defaults to time.Now.
	Now func() time.Time
}

// Request describes one job. The worker chooses the chain, directories and
// base; the executor fills in the outcome.
type Request struct {
	Project api.ProjectConfig
	// Job has ID, Type, ChainID, BaseJobID, Trigger and ScheduledAt set.
	Job *api.Job
	// JobDir is the job's directory, for example <root>/<project>/<chain>/00-full.
	JobDir string
	// BaseDir is the base backup's directory, required for incrementals.
	BaseDir string
	// EstimatedSize is the expected on-disk size; zero estimates from the data
	// directory size.
	EstimatedSize int64
	// PurgeLSN is the to_lsn of the current chain's full backup. Bitmaps before
	// it are purged after a successful incremental; a successful full purges
	// before its own to_lsn (PRD R-4).
	PurgeLSN uint64
	// Progress, if set, receives periodic progress samples.
	Progress func(Progress)
}

// Progress is a sample of a running backup (PRD E-6).
type Progress = api.Progress

// failure is a classified job failure.
type failure struct {
	code api.ErrorCode
	msg  string
}

func (f *failure) Error() string { return string(f.code) + ": " + f.msg }

func fail(code api.ErrorCode, format string, args ...any) *failure {
	return &failure{code: code, msg: fmt.Sprintf(format, args...)}
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Executor) logger() *slog.Logger {
	if e.Logger != nil {
		return e.Logger
	}
	return slog.Default()
}

// Run executes the job and records the outcome in req.Job. It returns when the
// job has finished and all cleanup is done; the outcome is never an error but
// the job's status and error code.
func (e *Executor) Run(ctx context.Context, req *Request) {
	job := req.Job
	log := e.logger().With("job", job.ID.String(), "project", req.Project.Name, "type", job.Type)
	job.Status = api.StatusRunning
	job.StartedAt = e.now()
	job.Path = filepath.Join(req.JobDir, DataFile)

	ctx, cancel := context.WithTimeoutCause(ctx, req.Project.Timeout.Std(), errTimeout)
	defer cancel()

	r := &run{e: e, req: req, log: log}
	err := r.execute(ctx)
	r.finish(ctx, err)
}

// run holds the state of one execution.
type run struct {
	e        *Executor
	req      *Request
	log      *slog.Logger
	sql      *mysql.Client
	defaults *mysql.DefaultsFile
	scan     *logScanner
	// started is set once xtrabackup runs and may have stopped the SQL thread.
	started bool
}

func (r *run) execute(ctx context.Context) error {
	p := &r.req.Project
	job := r.req.Job

	if err := os.MkdirAll(r.req.JobDir, 0o700); err != nil {
		return fail(api.ErrCompress, "create job dir: %v", err)
	}
	if job.Type == api.JobIncr && r.req.BaseDir == "" {
		return fail(api.ErrNoBase, "incremental backup without base directory")
	}

	// E-1: container running.
	c, err := r.e.Podman.Inspect(ctx, p.Container)
	switch {
	case errors.Is(err, podman.ErrNotFound):
		return fail(api.ErrContainerNotRunning, "container %s does not exist", p.Container)
	case err != nil:
		return r.ctxErr(ctx, fail(api.ErrContainerNotRunning, "inspect container: %v", err))
	case !c.Running:
		return fail(api.ErrContainerNotRunning, "container %s is %s", p.Container, c.State)
	}

	// E-4: credentials only in a short-lived 0600 file.
	r.defaults, err = mysql.WriteDefaultsFile(filepath.Join(r.e.Opts.BackupRoot, CredentialsDir), p.MySQL)
	if err != nil {
		return fail(api.ErrMySQLAuthFailed, "%v", err)
	}
	r.sql = &mysql.Client{Exec: r.e.Podman, Container: p.Container, Defaults: r.defaults}

	// E-2: replication healthy.
	if err := r.sql.CheckReplication(ctx, p.LagThreshold.Std()); err != nil {
		if ce, ok := errors.AsType[*mysql.CheckError](err); ok {
			return fail(ce.Code, "%s", ce.Reason)
		}
		return r.ctxErr(ctx, fail(api.ErrReplicationBroken, "check replication: %v", err))
	}

	// E-3: enough disk space.
	if err := r.checkSpace(ctx); err != nil {
		return err
	}

	return r.backup(ctx)
}

func (r *run) checkSpace(ctx context.Context) error {
	need := r.req.EstimatedSize
	if need <= 0 {
		size, err := r.sql.DataDirSize(ctx)
		if err != nil {
			return r.ctxErr(ctx, fail(api.ErrDiskInsufficient, "estimate size: %v", err))
		}
		need = size
	}
	need = int64(float64(need) * spaceFactor)
	freeSpace := r.e.FreeSpace
	if freeSpace == nil {
		freeSpace = FreeSpace
	}
	free, err := freeSpace(r.req.JobDir)
	if err != nil {
		return fail(api.ErrDiskInsufficient, "%v", err)
	}
	if free < need {
		return fail(api.ErrDiskInsufficient, "%d bytes free, %d required", free, need)
	}
	return nil
}

// ctxErr prefers the context's cancellation cause over a derived error, so
// that a timeout during a precheck is reported as TIMEOUT.
func (r *run) ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return err
}

func (r *run) xtrabackupArgs() []string {
	p := &r.req.Project
	dir := r.req.JobDir
	args := []string{
		r.defaults.Arg(),
		"--backup",
		"--stream=xbstream",
		"--slave-info",
		"--safe-slave-backup",
		"--parallel=" + strconv.Itoa(p.Parallel),
		"--target-dir=" + dir,
		"--extra-lsndir=" + dir,
	}
	if p.Throttle > 0 {
		args = append(args, "--throttle="+strconv.Itoa(p.Throttle))
	}
	if r.req.Job.Type == api.JobIncr {
		args = append(args, "--incremental-basedir="+r.req.BaseDir)
	}
	return args
}

// containerScript runs xtrabackup piped into zstd inside the container. Its
// arguments are the PID file, the data file, the zstd binary and xtrabackup's
// arguments. The inner shell records its PID, which becomes xtrabackup's PID
// after exec, so that xtrabackup can be killed inside the container; zstd then
// reads EOF and exits. The exit codes of both are printed to stdout as
// "xtrabackup N" and "zstd N" (see exitCodes).
func (r *run) containerScript() string {
	var b strings.Builder
	b.WriteString(`pid=$0 out=$1 zstd=$2; shift 2; prio=; `)
	if r.e.Opts.LowIOPriority {
		b.WriteString(`prio="nice -n 19"; command -v ionice >/dev/null 2>&1 && prio="ionice -c3 $prio"; `)
	}
	b.WriteString(`command -v "$zstd" >/dev/null 2>&1 || { echo "$zstd: command not found" >&2; echo "zstd 127"; exit 0; }; `)
	b.WriteString(`umask 077; exec 3>&1; `)
	b.WriteString(`{ $prio sh -c 'echo $$ > "$0"; exec xtrabackup "$@"' "$pid" "$@" 3>&-; echo "xtrabackup $?" >&3; } | `)
	fmt.Fprintf(&b, `$prio "$zstd" -q -c -T%d -%d > "$out" 3>&-; echo "zstd $?"`, r.e.Opts.ZstdThreads, r.e.Opts.ZstdLevel)
	return b.String()
}

// exitCodes parses the output of containerScript. A missing code is -1.
func exitCodes(out string) (xtrabackup, zstd int) {
	xtrabackup, zstd = -1, -1
	for line := range strings.Lines(out) {
		name, code, _ := strings.Cut(strings.TrimSpace(line), " ")
		n, err := strconv.Atoi(code)
		if err != nil {
			continue
		}
		switch name {
		case "xtrabackup":
			xtrabackup = n
		case "zstd":
			zstd = n
		}
	}
	return xtrabackup, zstd
}

func (r *run) backup(ctx context.Context) error {
	dir := r.req.JobDir
	pidPath := filepath.Join(dir, pidFile)
	dataPath := filepath.Join(dir, DataFile)

	logf, err := os.OpenFile(filepath.Join(dir, LogFile), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fail(api.ErrCompress, "create log: %v", err)
	}
	defer logf.Close()
	logBuf := bufio.NewWriter(logf)
	defer logBuf.Flush()
	r.scan = newLogScanner(logBuf)
	defer r.scan.Close()

	// The shell in the container creates the data file under umask 077; a
	// stale file would keep its old mode.
	if err := os.Remove(dataPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(api.ErrCompress, "remove stale data file: %v", err)
	}

	zstd := cmp.Or(r.e.Opts.Zstd, "zstd")
	args := append([]string{"sh", "-c", r.containerScript(), pidPath, dataPath, zstd}, r.xtrabackupArgs()...)
	xb := r.e.Podman.Exec(ctx, r.req.Project.Container, args...)
	xb.Stderr = r.scan
	var status limitedBuffer
	xb.Stdout = &status

	if err := xb.Start(); err != nil {
		return fail(api.ErrXtrabackup, "start xtrabackup: %v", err)
	}
	r.started = true
	stopKill := context.AfterFunc(ctx, func() { r.killInContainer(pidPath) })
	defer stopKill()
	stopProgress := r.reportProgress(dataPath)

	waitErr := xb.Wait()
	stopKill()
	stopProgress()
	_ = r.scan.Close()
	_ = os.Remove(pidPath)

	job := r.req.Job
	job.LogTail = r.scan.Tail(LogTailLines)
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}

	xbCode, zstdCode := exitCodes(status.String())
	// A failing zstd makes xtrabackup fail with a broken pipe, so check it first.
	if zstdCode > 0 {
		return fail(api.ErrCompress, "zstd exited with %d\n%s", zstdCode, r.scan.Tail(errorTailLines))
	}
	facts := r.scan.facts()
	if waitErr != nil || xbCode != 0 || !facts.completed {
		var reason string
		switch {
		case waitErr != nil:
			reason = waitErr.Error()
		case xbCode < 0:
			reason = "no exit code from xtrabackup"
		case xbCode > 0:
			reason = fmt.Sprintf("xtrabackup exited with %d", xbCode)
		default:
			reason = "log does not contain \"completed OK!\""
		}
		return fail(api.ErrXtrabackup, "%s\n%s", reason, r.scan.Tail(errorTailLines))
	}
	if zstdCode < 0 {
		return fail(api.ErrCompress, "no exit code from zstd\n%s", r.scan.Tail(errorTailLines))
	}

	size, err := syncFile(dataPath)
	if err != nil {
		return fail(api.ErrCompress, "%v", err)
	}
	job.CompressedSize = size
	if size == 0 {
		return fail(api.ErrXtrabackup, "backup stream is empty")
	}
	return r.collect(facts)
}

// syncFile flushes the data file written inside the container to disk and
// returns its size.
func syncFile(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open data file: %w", err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return 0, fmt.Errorf("sync data file: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat data file: %w", err)
	}
	return fi.Size(), nil
}

// collect records LSNs, slave position and bitmap use (PRD E-8).
func (r *run) collect(facts logFacts) error {
	job := r.req.Job
	cp, err := readCheckpoints(filepath.Join(r.req.JobDir, CheckpointsFile))
	if err != nil {
		return fail(api.ErrXtrabackup, "%v", err)
	}
	job.FromLSN, job.ToLSN = cp.FromLSN, cp.ToLSN
	job.UsedBitmap = facts.usedBitmap && job.Type == api.JobIncr
	job.SlaveInfo = facts.slave
	if facts.slave != nil {
		if err := os.WriteFile(filepath.Join(r.req.JobDir, SlaveInfoFile), []byte(facts.slave.Raw+"\n"), 0o600); err != nil {
			return fail(api.ErrCompress, "write slave info: %v", err)
		}
	}
	return nil
}

// killInContainer terminates xtrabackup inside the container. Killing only
// the local podman client may leave it running.
func (r *run) killInContainer(pidPath string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const script = `[ -f "$1" ] && kill -TERM "$(cat "$1")" 2>/dev/null; true`
	if _, err := r.e.Podman.Output(ctx, r.req.Project.Container, "sh", "-c", script, "sh", pidPath); err != nil {
		r.log.Warn("kill xtrabackup in container", "err", err)
	}
}

// reportProgress samples the size of the data file at path.
func (r *run) reportProgress(path string) (stop func()) {
	if r.req.Progress == nil {
		return func() {}
	}
	interval := r.e.Opts.ProgressInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(interval)
		defer t.Stop()
		start := time.Now()
		last, lastAt := int64(0), start
		for {
			select {
			case <-done:
				return
			case now := <-t.C:
				var n int64
				if fi, err := os.Stat(path); err == nil {
					n = fi.Size()
				}
				rate := float64(n-last) / now.Sub(lastAt).Seconds()
				last, lastAt = n, now
				r.req.Progress(Progress{
					CompressedBytes: n,
					BytesPerSecond:  rate,
					Elapsed:         api.Duration(now.Sub(start).Round(time.Millisecond)),
				})
			}
		}
	}()
	return func() {
		select {
		case <-done:
		default:
			close(done)
		}
		<-finished
	}
}

// finish classifies the outcome, runs cleanup that must happen regardless of
// cancellation, and writes meta.json.
func (r *run) finish(ctx context.Context, err error) {
	job := r.req.Job
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()

	// E-9: --safe-slave-backup stops the SQL thread; make sure it runs again.
	// Prechecks verified it was running, so only a backup can have stopped it.
	if r.started {
		started, serr := r.sql.EnsureSQLThread(cctx)
		switch {
		case serr != nil:
			r.log.Warn("ensure sql thread", "err", serr)
		case started:
			r.log.Warn("sql thread was stopped after backup and has been started")
		}
	}

	job.Status = api.StatusSuccess
	if err != nil {
		job.Status, job.ErrorCode, job.ErrorMessage = classify(err)
	} else if job.Type == api.JobIncr && !job.UsedBitmap {
		// E-12.
		job.Status = api.StatusWarning
		job.ErrorMessage = "incremental backup did not use the changed page bitmap"
	}

	if job.Status.Succeeded() && r.sql != nil {
		lsn := r.req.PurgeLSN
		if job.Type == api.JobFull {
			lsn = job.ToLSN
		}
		if perr := r.sql.PurgeBitmaps(cctx, lsn); perr != nil {
			r.log.Warn("purge changed page bitmaps", "lsn", lsn, "err", perr)
		}
	}

	if r.defaults != nil {
		if rerr := r.defaults.Remove(); rerr != nil {
			r.log.Error("remove credentials file", "err", rerr)
		}
	}
	if !job.Status.Succeeded() {
		r.removeIncomplete()
	}

	job.FinishedAt = r.e.now()
	if merr := writeMeta(r.req.JobDir, job); merr != nil {
		r.log.Error("write meta.json", "err", merr)
	}
	r.log.Info("job finished", "status", job.Status, "error_code", job.ErrorCode,
		"duration", job.Duration(), "compressed_size", job.CompressedSize)
}

func classify(err error) (api.JobStatus, api.ErrorCode, string) {
	switch {
	case errors.Is(err, ErrCanceled):
		return api.StatusCanceled, api.ErrCanceled, err.Error()
	case errors.Is(err, errTimeout):
		return api.StatusFailed, api.ErrTimeout, err.Error()
	case errors.Is(err, ErrShutdown), errors.Is(err, context.Canceled):
		return api.StatusFailed, api.ErrInterrupted, err.Error()
	}
	if f, ok := errors.AsType[*failure](err); ok {
		return api.StatusFailed, f.code, f.msg
	}
	return api.StatusFailed, api.ErrXtrabackup, err.Error()
}

// RemoveIncomplete deletes backup artifacts of a failed job, keeping the log
// and metadata for diagnosis.
func RemoveIncomplete(jobDir string) error {
	var errs []error
	for _, name := range []string{DataFile, CheckpointsFile, InfoFile, SlaveInfoFile, pidFile} {
		if err := os.Remove(filepath.Join(jobDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *run) removeIncomplete() {
	if err := RemoveIncomplete(r.req.JobDir); err != nil {
		r.log.Error("remove incomplete backup", "err", err)
	}
}

func writeMeta(dir string, job *api.Job) error {
	b, err := json.Marshal(job, json.Deterministic(true))
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, MetaFile+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, MetaFile))
}

// limitedBuffer keeps the first 4 KiB written to it.
type limitedBuffer struct{ b []byte }

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := 4096 - len(l.b); room > 0 {
		l.b = append(l.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string { return string(l.b) }
