package mysql

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rodericliu/backup/internal/agent/podman"
	"github.com/rodericliu/backup/internal/api"
)

type response struct {
	out string
	err error
}

// fakeExec answers commands by the first matching substring of their joined
// arguments and records every call.
type fakeExec struct {
	responses map[string]response
	calls     []string
}

func (f *fakeExec) Output(_ context.Context, container string, args ...string) ([]byte, error) {
	call := container + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	for _, k := range slices.Sorted(maps.Keys(f.responses)) {
		if strings.Contains(call, k) {
			r := f.responses[k]
			return []byte(r.out), r.err
		}
	}
	return nil, errors.New("unexpected call: " + call)
}

func newClient(responses map[string]response) (*Client, *fakeExec) {
	f := &fakeExec{responses: responses}
	return &Client{Exec: f, Container: "c1", Defaults: &DefaultsFile{Path: "/data/backup/.agent/my.cnf"}}, f
}

func slaveStatus(io, sql, lag string) string {
	return "Slave_IO_State\tSlave_IO_Running\tSlave_SQL_Running\tSeconds_Behind_Master\tLast_IO_Error\tLast_SQL_Error\n" +
		"Waiting for master\t" + io + "\t" + sql + "\t" + lag + "\t\tduplicate\\tkey\n"
}

func exitErr(stderr string) error {
	return &podman.ExitError{Args: []string{"exec"}, ExitCode: 1, Stderr: stderr}
}

func TestQueryArgs(t *testing.T) {
	c, f := newClient(map[string]response{"SELECT": {out: "a\tb\n1\tNULL\n"}})
	rows, err := c.Query(t.Context(), "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	want := "c1 mysql --defaults-extra-file=/data/backup/.agent/my.cnf --batch --execute SELECT 1"
	if f.calls[0] != want {
		t.Errorf("call = %q, want %q", f.calls[0], want)
	}
	if len(rows) != 1 || rows[0].Get("a") != "1" || !rows[0].Null("b") || rows[0].Get("b") != "" {
		t.Errorf("rows = %q", rows)
	}
}

func TestParseBatch(t *testing.T) {
	rows := parseBatch([]byte("x\ty\na\\nb\tc\\\\d\n\t\n"))
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0]["x"] != "a\nb" || rows[0]["y"] != `c\d` {
		t.Errorf("unescape: %q", rows[0])
	}
	if rows[1].Null("x") || rows[1]["x"] != "" {
		t.Errorf("empty value treated as NULL: %q", rows[1])
	}
	if parseBatch(nil) != nil {
		t.Error("empty output should yield no rows")
	}
}

func TestQueryError(t *testing.T) {
	c, _ := newClient(map[string]response{
		"SHOW": {err: exitErr("ERROR 1045 (28000): Access denied for user 'backup'@'localhost' (using password: YES)")},
	})
	_, err := c.Query(t.Context(), "SHOW SLAVE STATUS")
	e, ok := errors.AsType[*Error](err)
	if !ok {
		t.Fatalf("err = %v, want *Error", err)
	}
	if e.Code != 1045 || !e.Auth() || !IsAuth(err) {
		t.Errorf("error = %+v", e)
	}
	if !strings.Contains(e.Message, "Access denied") {
		t.Errorf("message = %q", e.Message)
	}
}

func TestCheckReplication(t *testing.T) {
	tests := []struct {
		name string
		resp response
		want api.ErrorCode
	}{
		{"healthy", response{out: slaveStatus("Yes", "Yes", "3")}, ""},
		{"io stopped", response{out: slaveStatus("No", "Yes", "NULL")}, api.ErrReplicationBroken},
		{"sql stopped", response{out: slaveStatus("Yes", "No", "NULL")}, api.ErrReplicationBroken},
		{"lag null", response{out: slaveStatus("Yes", "Yes", "NULL")}, api.ErrReplicationBroken},
		{"lagging", response{out: slaveStatus("Yes", "Yes", "900")}, api.ErrReplicationLag},
		{"not replica", response{out: ""}, api.ErrReplicationBroken},
		{"auth", response{err: exitErr("ERROR 1227 (42000) at line 1: Access denied; you need (at least one of) the SUPER, REPLICATION CLIENT privilege(s)")}, api.ErrMySQLAuthFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newClient(map[string]response{"SHOW SLAVE STATUS": tt.resp})
			err := c.CheckReplication(t.Context(), 10*time.Minute)
			if tt.want == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			ce, ok := errors.AsType[*CheckError](err)
			if !ok {
				t.Fatalf("err = %v, want *CheckError", err)
			}
			if ce.Code != tt.want {
				t.Errorf("code = %s, want %s (%s)", ce.Code, tt.want, ce.Reason)
			}
		})
	}
}

func TestCheckReplicationReason(t *testing.T) {
	c, _ := newClient(map[string]response{"SHOW SLAVE STATUS": {out: slaveStatus("Yes", "No", "NULL")}})
	err := c.CheckReplication(t.Context(), 0)
	if err == nil || !strings.Contains(err.Error(), "duplicate\tkey") {
		t.Errorf("reason should include Last_SQL_Error: %v", err)
	}
}

func TestEnsureSQLThread(t *testing.T) {
	c, f := newClient(map[string]response{
		"SHOW SLAVE STATUS":      {out: slaveStatus("Yes", "No", "NULL")},
		"START SLAVE SQL_THREAD": {},
	})
	started, err := c.EnsureSQLThread(t.Context())
	if err != nil || !started {
		t.Fatalf("started=%v err=%v", started, err)
	}
	if !strings.HasSuffix(f.calls[1], "START SLAVE SQL_THREAD") {
		t.Errorf("calls = %q", f.calls)
	}

	c, f = newClient(map[string]response{"SHOW SLAVE STATUS": {out: slaveStatus("Yes", "Yes", "0")}})
	if started, err := c.EnsureSQLThread(t.Context()); err != nil || started {
		t.Errorf("running thread: started=%v err=%v", started, err)
	}
	if len(f.calls) != 1 {
		t.Errorf("unexpected calls: %q", f.calls)
	}
}

func TestPurgeBitmaps(t *testing.T) {
	c, f := newClient(map[string]response{
		"innodb_track_changed_pages": {out: "Variable_name\tValue\ninnodb_track_changed_pages\tON\n"},
		"PURGE":                      {},
	})
	if err := c.PurgeBitmaps(t.Context(), 123456); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(f.calls[1], "PURGE CHANGED_PAGE_BITMAPS BEFORE 123456") {
		t.Errorf("calls = %q", f.calls)
	}

	c, f = newClient(map[string]response{"innodb_track_changed_pages": {out: ""}})
	if err := c.PurgeBitmaps(t.Context(), 123456); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Errorf("purge should be skipped without bitmap support: %q", f.calls)
	}
}

func TestVariableRejectsInjection(t *testing.T) {
	c, f := newClient(nil)
	if _, _, err := c.Variable(t.Context(), "x' OR 1=1 -- "); err == nil {
		t.Error("want error")
	}
	if len(f.calls) != 0 {
		t.Errorf("query executed: %q", f.calls)
	}
}

func TestSizes(t *testing.T) {
	c, f := newClient(map[string]response{
		"'datadir'": {out: "Variable_name\tValue\ndatadir\t/var/lib/mysql/\n"},
		"wc -c":     {out: "100\n250\n"},
		"du -sk":    {out: "2048\t/var/lib/mysql\n"},
	})
	n, err := c.BitmapSize(t.Context())
	if err != nil || n != 350 {
		t.Errorf("BitmapSize = %d, %v; want 350", n, err)
	}
	if !strings.HasSuffix(f.calls[1], "sh /var/lib/mysql") {
		t.Errorf("datadir not passed as argument: %q", f.calls[1])
	}
	n, err = c.DataDirSize(t.Context())
	if err != nil || n != 2048*1024 {
		t.Errorf("DataDirSize = %d, %v; want %d", n, err, 2048*1024)
	}
}

func TestDefaultsFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "creds")
	d, err := WriteDefaultsFile(dir, api.MySQLConfig{User: "backup", Password: `p"a\ss`, Port: 3307})
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(d.Path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}
	if di, _ := os.Stat(dir); di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o, want 700", di.Mode().Perm())
	}
	b, _ := os.ReadFile(d.Path)
	want := "user=\"backup\"\npassword='p\"a\\\\ss'\nport=3307\n"
	if got := string(b); got != "[client]\n"+want+"[xtrabackup]\n"+want {
		t.Errorf("content =\n%s", got)
	}
	if !strings.HasPrefix(d.Arg(), "--defaults-extra-file="+dir) {
		t.Errorf("Arg() = %q", d.Arg())
	}
	if err := d.Remove(); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove(); err != nil {
		t.Errorf("second Remove: %v", err)
	}
	if _, err := os.Stat(d.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file still exists: %v", err)
	}
}

func TestDefaultsFileInvalid(t *testing.T) {
	for _, cfg := range []api.MySQLConfig{
		{},
		{User: "u", Password: "a\nb"},
		{User: "u", Password: `a"b'c`},
	} {
		if _, err := WriteDefaultsFile(t.TempDir(), cfg); err == nil {
			t.Errorf("WriteDefaultsFile(%+v): want error", cfg)
		}
	}
}
