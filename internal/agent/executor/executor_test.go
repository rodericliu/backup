package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/rodericliu/backup/internal/agent/podman"
	"github.com/rodericliu/backup/internal/api"
)

// The fakes run every `podman exec` directly on the host with a fake bin
// directory first in PATH, so the real shell wrapper, nice and zstd pipeline
// are exercised.
const fakePodman = `#!/bin/sh
case "$1" in
container)
  [ "$FAKE_CONTAINER" = missing ] && { echo "Error: no such container $5" >&2; exit 125; }
  state=running; running=true
  [ "$FAKE_CONTAINER" = stopped ] && { state=exited; running=false; }
  printf '[{"Id":"id1","Name":"%s","State":{"Status":"%s","Running":%s}}]' "$5" "$state" "$running" ;;
exec)
  shift
  [ "$1" = --user ] && shift 2
  shift
  exec "$@" ;;
*) echo "unexpected podman $*" >&2; exit 2 ;;
esac
`

const fakeMySQL = `#!/bin/sh
cnf=${1#--defaults-extra-file=}
[ -f "$cnf" ] || { echo "ERROR 1045 (28000): Access denied: no defaults file" >&2; exit 1; }
echo "$4" >> "$FAKE_DIR/mysql.log"
case "$4" in
"SHOW SLAVE STATUS")
  sql=Yes; [ -f "$FAKE_DIR/sql_stopped" ] && sql=No
  io=Yes; [ "$FAKE_REPL" = broken ] && { io=No; sql=No; }
  printf 'Slave_IO_Running\tSlave_SQL_Running\tSeconds_Behind_Master\tLast_IO_Error\tLast_SQL_Error\n'
  printf '%s\t%s\t%s\t\t\n' "$io" "$sql" 0 ;;
"START SLAVE SQL_THREAD") rm -f "$FAKE_DIR/sql_stopped" ;;
*innodb_track_changed_pages*) printf 'Variable_name\tValue\ninnodb_track_changed_pages\tON\n' ;;
*datadir*) printf 'Variable_name\tValue\ndatadir\t/var/lib/mysql/\n' ;;
PURGE*) ;;
*) echo "ERROR 1064 (42000): unexpected $4" >&2; exit 1 ;;
esac
`

const fakeDu = `#!/bin/sh
printf '1024\t%s\n' "$2"
`

const fakeXtrabackup = `#!/bin/sh
echo "$@" > "$FAKE_DIR/xtrabackup.args"
for a in "$@"; do
  case "$a" in
  --defaults-extra-file=*) [ -f "${a#*=}" ] || { echo "no defaults file" >&2; exit 1; } ;;
  --extra-lsndir=*) lsndir=${a#*=} ;;
  --incremental-basedir=*) base=${a#*=} ;;
  esac
done
echo "xtrabackup version 2.4.28 based on MySQL server 5.7.40" >&2
type=full-backuped; from=0
if [ -n "$base" ]; then
  type=incremental
  from=$(sed -n 's/^to_lsn = //p' "$base/xtrabackup_checkpoints")
  if [ "$FAKE_XB" = nobitmap ]; then
    echo "xtrabackup: using the full scan for incremental backup" >&2
  else
    echo "xtrabackup: using the changed page bitmap" >&2
  fi
fi
case "$FAKE_XB" in
fail) echo "InnoDB: Error: log block numbers mismatch" >&2; exit 1 ;;
stopsql) touch "$FAKE_DIR/sql_stopped"; echo "xtrabackup: Error: failed after stopping SQL thread" >&2; exit 1 ;;
hang) head -c 65536 /dev/zero; exec sleep 60 ;;
esac
i=0
while [ $i -lt 20 ]; do
  head -c 16384 /dev/zero | tr '\0' x
  echo ">> log scanned up to ($i)" >&2
  [ "$FAKE_XB" = slow ] && sleep 0.05
  i=$((i+1))
done
to=$((from + 1000))
printf 'backup_type = %s\nfrom_lsn = %s\nto_lsn = %s\nlast_lsn = %s\ncompact = 0\n' "$type" "$from" "$to" "$to" > "$lsndir/xtrabackup_checkpoints"
echo "tool_name = xtrabackup" > "$lsndir/xtrabackup_info"
echo "MySQL slave binlog position: master host '10.0.0.1', filename 'mysql-bin.000042', position '4711'" >&2
[ "$FAKE_XB" = nook ] && exit 0
echo "xtrabackup: Transaction log of lsn ($from) to ($to) was copied." >&2
echo "$(date) completed OK!" >&2
`

type env struct {
	t    *testing.T
	dir  string
	root string
	exec *Executor
}

func setup(t *testing.T) *env {
	t.Helper()
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd not installed")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"podman": fakePodman, "mysql": fakeMySQL, "du": fakeDu, "xtrabackup": fakeXtrabackup,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DIR", dir)
	t.Setenv("FAKE_XB", "")
	t.Setenv("FAKE_CONTAINER", "")
	t.Setenv("FAKE_REPL", "")

	root := filepath.Join(dir, "backup")
	pc := podman.New(filepath.Join(bin, "podman"))
	pc.User = "root"
	return &env{
		t:    t,
		dir:  dir,
		root: root,
		exec: &Executor{
			Podman: pc,
			Opts: Options{
				BackupRoot: root, ZstdThreads: 1, ZstdLevel: 3, LowIOPriority: true,
				ProgressInterval: 20 * time.Millisecond,
			},
			FreeSpace: func(string) (int64, error) { return 1 << 40, nil },
			Logger:    slog.New(slog.DiscardHandler),
		},
	}
}

func project() api.ProjectConfig {
	p := api.Template(api.TierImportant)
	p.Name, p.Container = "gays", "gays_maindb_slave"
	p.MySQL = api.MySQLConfig{User: "backup", Password: "s3cret"}
	p.Throttle = 40
	return p
}

func (e *env) request(typ api.JobType, name string) *Request {
	return &Request{
		Project: project(),
		Job:     &api.Job{ID: uuid.NewV7(), Project: "gays", Type: typ, Trigger: api.TriggerSchedule, ScheduledAt: time.Now()},
		JobDir:  filepath.Join(e.root, "gays", "20261028-0000-chain", name),
	}
}

func (e *env) mysqlLog() string {
	b, _ := os.ReadFile(filepath.Join(e.dir, "mysql.log"))
	return string(b)
}

func (e *env) assertNoCredentials() {
	e.t.Helper()
	entries, _ := os.ReadDir(filepath.Join(e.root, CredentialsDir))
	if len(entries) != 0 {
		e.t.Errorf("credentials files left behind: %v", entries)
	}
}

func readMeta(t *testing.T, dir string) api.Job {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, MetaFile))
	if err != nil {
		t.Fatal(err)
	}
	var j api.Job
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatal(err)
	}
	return j
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestFullSuccess(t *testing.T) {
	e := setup(t)
	req := e.request(api.JobFull, "00-full")
	var samples int
	req.Progress = func(p Progress) { samples++ }
	t.Setenv("FAKE_XB", "slow")
	e.exec.Run(t.Context(), req)

	job := req.Job
	if job.Status != api.StatusSuccess {
		t.Fatalf("status = %s %s: %s", job.Status, job.ErrorCode, job.ErrorMessage)
	}
	if job.ToLSN != 1000 || job.FromLSN != 0 {
		t.Errorf("lsn = %d..%d, want 0..1000", job.FromLSN, job.ToLSN)
	}
	if job.SlaveInfo == nil || job.SlaveInfo.MasterLogFile != "mysql-bin.000042" || job.SlaveInfo.MasterLogPos != 4711 {
		t.Errorf("slave info = %+v", job.SlaveInfo)
	}
	if job.UsedBitmap {
		t.Error("full backup marked as using bitmap")
	}
	if job.RawSize != 20*16384 {
		t.Errorf("raw size = %d, want %d", job.RawSize, 20*16384)
	}
	if samples == 0 {
		t.Error("no progress samples")
	}

	data, err := os.ReadFile(job.Path)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != job.CompressedSize || job.CompressedSize >= job.RawSize {
		t.Errorf("compressed size = %d, file %d, raw %d", job.CompressedSize, len(data), job.RawSize)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != job.SHA256 {
		t.Error("sha256 does not match file")
	}
	raw, err := exec.Command("zstd", "-d", "-c", job.Path).Output()
	if err != nil || int64(len(raw)) != job.RawSize || !bytes.Equal(raw[:4], []byte("xxxx")) {
		t.Errorf("decompress: %d bytes, %v", len(raw), err)
	}

	for _, f := range []string{CheckpointsFile, InfoFile, SlaveInfoFile, LogFile, MetaFile} {
		if !exists(filepath.Join(req.JobDir, f)) {
			t.Errorf("%s missing", f)
		}
	}
	if exists(filepath.Join(req.JobDir, pidFile)) {
		t.Error("pid file left behind")
	}
	if fi, _ := os.Stat(job.Path); fi.Mode().Perm() != 0o600 {
		t.Errorf("data file mode %o", fi.Mode().Perm())
	}
	if si, _ := os.ReadFile(filepath.Join(req.JobDir, SlaveInfoFile)); !strings.Contains(string(si), "MASTER_LOG_POS=4711") {
		t.Errorf("slave info file = %q", si)
	}
	if meta := readMeta(t, req.JobDir); meta.ID != job.ID || meta.SHA256 != job.SHA256 || meta.Status != api.StatusSuccess {
		t.Errorf("meta = %+v", meta)
	}
	if !strings.Contains(job.LogTail, "completed OK!") {
		t.Errorf("log tail = %q", job.LogTail)
	}

	args, _ := os.ReadFile(filepath.Join(e.dir, "xtrabackup.args"))
	for _, want := range []string{"--backup", "--stream=xbstream", "--slave-info", "--safe-slave-backup", "--parallel=4", "--throttle=40", "--extra-lsndir=" + req.JobDir} {
		if !strings.Contains(string(args), want) {
			t.Errorf("xtrabackup args %q missing %s", args, want)
		}
	}
	if strings.Contains(string(args), "s3cret") || strings.Contains(string(args), "incremental") {
		t.Errorf("unexpected xtrabackup args %q", args)
	}
	if !strings.Contains(e.mysqlLog(), "PURGE CHANGED_PAGE_BITMAPS BEFORE 1000") {
		t.Errorf("bitmaps not purged before full to_lsn:\n%s", e.mysqlLog())
	}
	e.assertNoCredentials()
}

func (e *env) fullBackup() *Request {
	e.t.Helper()
	full := e.request(api.JobFull, "00-full")
	e.exec.Run(e.t.Context(), full)
	if full.Job.Status != api.StatusSuccess {
		e.t.Fatalf("full backup: %s %s", full.Job.Status, full.Job.ErrorMessage)
	}
	return full
}

func TestIncremental(t *testing.T) {
	tests := []struct {
		mode       string
		want       api.JobStatus
		usedBitmap bool
	}{
		{"", api.StatusSuccess, true},
		{"nobitmap", api.StatusWarning, false},
	}
	for _, tt := range tests {
		t.Run(cmpOr(tt.mode, "bitmap"), func(t *testing.T) {
			e := setup(t)
			full := e.fullBackup()
			t.Setenv("FAKE_XB", tt.mode)
			req := e.request(api.JobIncr, "01-incr")
			req.BaseDir = full.JobDir
			req.PurgeLSN = full.Job.ToLSN
			e.exec.Run(t.Context(), req)

			job := req.Job
			if job.Status != tt.want || job.UsedBitmap != tt.usedBitmap {
				t.Fatalf("status = %s used_bitmap=%v (%s)", job.Status, job.UsedBitmap, job.ErrorMessage)
			}
			if job.FromLSN != 1000 || job.ToLSN != 2000 {
				t.Errorf("lsn = %d..%d, want 1000..2000", job.FromLSN, job.ToLSN)
			}
			args, _ := os.ReadFile(filepath.Join(e.dir, "xtrabackup.args"))
			if !strings.Contains(string(args), "--incremental-basedir="+full.JobDir) {
				t.Errorf("args = %q", args)
			}
			if strings.Count(e.mysqlLog(), "PURGE CHANGED_PAGE_BITMAPS BEFORE 1000") != 2 {
				t.Errorf("incremental should purge before the chain's full to_lsn:\n%s", e.mysqlLog())
			}
		})
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func TestFailures(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		mutate  func(*env, *Request)
		code    api.ErrorCode
		started bool // xtrabackup ran, so the data file existed
	}{
		{name: "container missing", env: map[string]string{"FAKE_CONTAINER": "missing"}, code: api.ErrContainerNotRunning},
		{name: "container stopped", env: map[string]string{"FAKE_CONTAINER": "stopped"}, code: api.ErrContainerNotRunning},
		{name: "replication broken", env: map[string]string{"FAKE_REPL": "broken"}, code: api.ErrReplicationBroken},
		{name: "disk insufficient", mutate: func(e *env, _ *Request) {
			e.exec.FreeSpace = func(string) (int64, error) { return 1024*1024 + 1, nil } // datadir is 1 MiB
		}, code: api.ErrDiskInsufficient},
		{name: "disk from history", mutate: func(e *env, r *Request) {
			e.exec.FreeSpace = func(string) (int64, error) { return 1000, nil }
			r.EstimatedSize = 700
		}, code: api.ErrDiskInsufficient},
		{name: "no base", mutate: func(_ *env, r *Request) { r.Job.Type = api.JobIncr }, code: api.ErrNoBase},
		{name: "xtrabackup error", env: map[string]string{"FAKE_XB": "fail"}, code: api.ErrXtrabackup, started: true},
		{name: "no completed ok", env: map[string]string{"FAKE_XB": "nook"}, code: api.ErrXtrabackup, started: true},
		{name: "zstd missing", mutate: func(e *env, _ *Request) { e.exec.Opts.Zstd = "/nonexistent/zstd" }, code: api.ErrCompress},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := setup(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			req := e.request(api.JobFull, "00-full")
			if tt.mutate != nil {
				tt.mutate(e, req)
			}
			e.exec.Run(t.Context(), req)
			job := req.Job
			if job.Status != api.StatusFailed || job.ErrorCode != tt.code {
				t.Fatalf("status = %s %s (%s), want failed %s", job.Status, job.ErrorCode, job.ErrorMessage, tt.code)
			}
			if job.FinishedAt.IsZero() || job.StartedAt.IsZero() {
				t.Error("timestamps not set")
			}
			for _, f := range []string{DataFile, CheckpointsFile, InfoFile} {
				if exists(filepath.Join(req.JobDir, f)) {
					t.Errorf("%s not cleaned up", f)
				}
			}
			if meta := readMeta(t, req.JobDir); meta.ErrorCode != tt.code {
				t.Errorf("meta error code = %s", meta.ErrorCode)
			}
			if tt.started {
				if !exists(filepath.Join(req.JobDir, LogFile)) {
					t.Error("backup.log should be kept")
				}
				if job.LogTail == "" {
					t.Error("log tail empty")
				}
			}
			if strings.Contains(e.mysqlLog(), "PURGE") {
				t.Error("bitmaps purged after failure")
			}
			e.assertNoCredentials()
		})
	}
}

func TestXtrabackupErrorMessage(t *testing.T) {
	e := setup(t)
	t.Setenv("FAKE_XB", "fail")
	req := e.request(api.JobFull, "00-full")
	e.exec.Run(t.Context(), req)
	if !strings.Contains(req.Job.ErrorMessage, "log block numbers mismatch") {
		t.Errorf("error message should include the log tail: %q", req.Job.ErrorMessage)
	}
}

func TestRestartsSQLThread(t *testing.T) {
	e := setup(t)
	t.Setenv("FAKE_XB", "stopsql")
	req := e.request(api.JobFull, "00-full")
	e.exec.Run(t.Context(), req)
	if req.Job.ErrorCode != api.ErrXtrabackup {
		t.Fatalf("error code = %s", req.Job.ErrorCode)
	}
	if !strings.Contains(e.mysqlLog(), "START SLAVE SQL_THREAD") {
		t.Errorf("sql thread not restarted:\n%s", e.mysqlLog())
	}
	if exists(filepath.Join(e.dir, "sql_stopped")) {
		t.Error("sql thread still stopped")
	}
}

func TestReplicationBrokenDoesNotStartSQLThread(t *testing.T) {
	e := setup(t)
	t.Setenv("FAKE_REPL", "broken")
	e.exec.Run(t.Context(), e.request(api.JobFull, "00-full"))
	if strings.Contains(e.mysqlLog(), "START SLAVE") {
		t.Error("must not start replication that was already broken before the backup")
	}
}

func TestTimeoutAndCancel(t *testing.T) {
	tests := []struct {
		name   string
		run    func(e *env, req *Request)
		status api.JobStatus
		code   api.ErrorCode
	}{
		{"timeout", func(e *env, req *Request) {
			req.Project.Timeout = api.Duration(1500 * time.Millisecond)
			e.exec.Run(e.t.Context(), req)
		}, api.StatusFailed, api.ErrTimeout},
		{"cancel", func(e *env, req *Request) {
			ctx, cancel := context.WithCancelCause(e.t.Context())
			time.AfterFunc(1500*time.Millisecond, func() { cancel(ErrCanceled) })
			e.exec.Run(ctx, req)
		}, api.StatusCanceled, api.ErrCanceled},
		{"shutdown", func(e *env, req *Request) {
			ctx, cancel := context.WithCancelCause(e.t.Context())
			time.AfterFunc(1500*time.Millisecond, func() { cancel(ErrShutdown) })
			e.exec.Run(ctx, req)
		}, api.StatusFailed, api.ErrInterrupted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := setup(t)
			t.Setenv("FAKE_XB", "hang")
			req := e.request(api.JobFull, "00-full")
			start := time.Now()
			tt.run(e, req)
			if d := time.Since(start); d > 20*time.Second {
				t.Errorf("took %s; xtrabackup was not stopped", d)
			}
			if req.Job.Status != tt.status || req.Job.ErrorCode != tt.code {
				t.Errorf("status = %s %s, want %s %s", req.Job.Status, req.Job.ErrorCode, tt.status, tt.code)
			}
			if exists(filepath.Join(req.JobDir, DataFile)) {
				t.Error("incomplete data file left behind")
			}
			if strings.Count(e.mysqlLog(), "SHOW SLAVE STATUS") != 2 {
				t.Errorf("sql thread not checked after interruption:\n%s", e.mysqlLog())
			}
			e.assertNoCredentials()
		})
	}
}
