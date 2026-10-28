// Package mysql queries and controls a MySQL replica through the mysql client
// inside its container.
package mysql

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rodericliu/backup/internal/agent/podman"
	"github.com/rodericliu/backup/internal/api"
)

// Execer runs a command inside a container and returns its stdout.
// *podman.Client implements it.
type Execer interface {
	Output(ctx context.Context, container string, args ...string) ([]byte, error)
}

// ErrNotReplica is returned when SHOW SLAVE STATUS returns no row.
var ErrNotReplica = errors.New("server is not a replica")

// Error is a failure reported by the mysql client.
type Error struct {
	// Code is the MySQL error number, or 0 when it could not be parsed.
	Code    int
	Message string
}

func (e *Error) Error() string { return "mysql: " + e.Message }

// Auth reports whether the error is an authentication or privilege failure.
func (e *Error) Auth() bool {
	switch e.Code {
	case 1044, 1045, 1142, 1227, 1698:
		return true
	}
	return false
}

// IsAuth reports whether err is a MySQL authentication or privilege failure.
func IsAuth(err error) bool {
	e, ok := errors.AsType[*Error](err)
	return ok && e.Auth()
}

var errorRE = regexp.MustCompile(`ERROR (\d+) \([0-9A-Z]+\)[^:]*: (.*)`)

// Client runs SQL on one replica.
type Client struct {
	Exec      Execer
	Container string
	Defaults  *DefaultsFile
}

// Query runs sql and returns the result rows keyed by column name. Use
// Row.Null to tell NULL from an empty string. Server errors are returned as
// *Error.
func (c *Client) Query(ctx context.Context, sql string) ([]Row, error) {
	out, err := c.Exec.Output(ctx, c.Container, "mysql", c.Defaults.Arg(), "--batch", "--execute", sql)
	if err != nil {
		if exitErr, ok := errors.AsType[*podman.ExitError](err); ok {
			if m := errorRE.FindStringSubmatch(exitErr.Stderr); m != nil {
				code, _ := strconv.Atoi(m[1])
				return nil, &Error{Code: code, Message: m[2]}
			}
			if exitErr.Stderr != "" {
				return nil, &Error{Message: exitErr.Stderr}
			}
		}
		return nil, fmt.Errorf("mysql query: %w", err)
	}
	return parseBatch(out), nil
}

// Run runs sql that returns no rows.
func (c *Client) Run(ctx context.Context, sql string) error {
	_, err := c.Query(ctx, sql)
	return err
}

// Row is one result row. Values hold the unescaped column values.
type Row map[string]string

// nullValue marks NULL in a Row; the mysql client prints NULL unquoted, which
// is indistinguishable from the string "NULL", so the marker is an invalid
// UTF-8 sequence that cannot appear in server output.
const nullValue = "\xff\x00NULL"

// Null reports whether column is NULL or absent.
func (r Row) Null(column string) bool {
	v, ok := r[column]
	return !ok || v == nullValue
}

// Get returns the value of column, or "" when it is NULL.
func (r Row) Get(column string) string {
	if r.Null(column) {
		return ""
	}
	return r[column]
}

// parseBatch parses mysql --batch output: a header line followed by rows of
// tab separated, backslash escaped values.
func parseBatch(out []byte) []Row {
	lines := bytes.Split(bytes.TrimRight(out, "\n"), []byte("\n"))
	if len(lines) < 2 {
		return nil
	}
	header := strings.Split(string(lines[0]), "\t")
	rows := make([]Row, 0, len(lines)-1)
	for _, line := range lines[1:] {
		fields := strings.Split(string(line), "\t")
		row := make(Row, len(header))
		for i, col := range header {
			if i >= len(fields) {
				break
			}
			if fields[i] == "NULL" {
				row[col] = nullValue
				continue
			}
			row[col] = unescape(fields[i])
		}
		rows = append(rows, row)
	}
	return rows
}

func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i == len(s)-1 {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case '0':
			b.WriteByte(0)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// SlaveStatus is the subset of SHOW SLAVE STATUS used by prechecks.
type SlaveStatus struct {
	IORunning  bool
	SQLRunning bool
	// Lag is Seconds_Behind_Master; LagKnown is false when it is NULL.
	Lag          time.Duration
	LagKnown     bool
	LastIOError  string
	LastSQLError string
}

// SlaveStatus returns the replication state, or ErrNotReplica.
func (c *Client) SlaveStatus(ctx context.Context) (*SlaveStatus, error) {
	rows, err := c.Query(ctx, "SHOW SLAVE STATUS")
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNotReplica
	}
	r := rows[0]
	s := &SlaveStatus{
		IORunning:    r.Get("Slave_IO_Running") == "Yes",
		SQLRunning:   r.Get("Slave_SQL_Running") == "Yes",
		LastIOError:  r.Get("Last_IO_Error"),
		LastSQLError: r.Get("Last_SQL_Error"),
	}
	if !r.Null("Seconds_Behind_Master") {
		sec, err := strconv.ParseInt(r.Get("Seconds_Behind_Master"), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse Seconds_Behind_Master: %w", err)
		}
		s.Lag, s.LagKnown = time.Duration(sec)*time.Second, true
	}
	return s, nil
}

// CheckError is a failed precheck with its classification.
type CheckError struct {
	Code   api.ErrorCode
	Reason string
}

func (e *CheckError) Error() string { return string(e.Code) + ": " + e.Reason }

// CheckReplication verifies that both replication threads run and the lag is
// below threshold (PRD E-2). Failures are returned as *CheckError.
func (c *Client) CheckReplication(ctx context.Context, threshold time.Duration) error {
	s, err := c.SlaveStatus(ctx)
	switch {
	case IsAuth(err):
		return &CheckError{Code: api.ErrMySQLAuthFailed, Reason: err.Error()}
	case errors.Is(err, ErrNotReplica):
		return &CheckError{Code: api.ErrReplicationBroken, Reason: err.Error()}
	case err != nil:
		return fmt.Errorf("slave status: %w", err)
	}
	if !s.IORunning || !s.SQLRunning {
		parts := []string{fmt.Sprintf("Slave_IO_Running=%s Slave_SQL_Running=%s", yesNo(s.IORunning), yesNo(s.SQLRunning))}
		for _, e := range []string{s.LastIOError, s.LastSQLError} {
			if e != "" {
				parts = append(parts, e)
			}
		}
		return &CheckError{Code: api.ErrReplicationBroken, Reason: strings.Join(parts, ": ")}
	}
	if !s.LagKnown {
		return &CheckError{Code: api.ErrReplicationBroken, Reason: "Seconds_Behind_Master is NULL"}
	}
	if threshold > 0 && s.Lag >= threshold {
		return &CheckError{Code: api.ErrReplicationLag, Reason: fmt.Sprintf("Seconds_Behind_Master %s >= threshold %s", s.Lag, threshold)}
	}
	return nil
}

func yesNo(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

// EnsureSQLThread starts the SQL thread if it is stopped, which can happen when
// a backup using --safe-slave-backup is interrupted (PRD E-9). It reports
// whether the thread had to be started.
func (c *Client) EnsureSQLThread(ctx context.Context) (started bool, err error) {
	s, err := c.SlaveStatus(ctx)
	if err != nil {
		return false, err
	}
	if s.SQLRunning {
		return false, nil
	}
	if err := c.Run(ctx, "START SLAVE SQL_THREAD"); err != nil {
		return false, fmt.Errorf("start sql thread: %w", err)
	}
	return true, nil
}

// Variable returns a global variable, and false when it does not exist.
func (c *Client) Variable(ctx context.Context, name string) (string, bool, error) {
	if !validName(name) {
		return "", false, fmt.Errorf("invalid variable name %q", name)
	}
	rows, err := c.Query(ctx, "SHOW GLOBAL VARIABLES LIKE '"+name+"'")
	if err != nil {
		return "", false, err
	}
	if len(rows) == 0 {
		return "", false, nil
	}
	return rows[0].Get("Value"), true, nil
}

func validName(s string) bool {
	for _, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return s != ""
}

// TrackChangedPages reports whether the changed page bitmap is enabled.
func (c *Client) TrackChangedPages(ctx context.Context) (bool, error) {
	v, ok, err := c.Variable(ctx, "innodb_track_changed_pages")
	if err != nil {
		return false, err
	}
	return ok && (v == "ON" || v == "1"), nil
}

// PurgeBitmaps deletes changed page bitmaps before lsn (PRD R-4). It is a
// no-op when bitmap tracking is disabled or unsupported.
func (c *Client) PurgeBitmaps(ctx context.Context, lsn uint64) error {
	on, err := c.TrackChangedPages(ctx)
	if err != nil || !on || lsn == 0 {
		return err
	}
	if err := c.Run(ctx, "PURGE CHANGED_PAGE_BITMAPS BEFORE "+strconv.FormatUint(lsn, 10)); err != nil {
		return fmt.Errorf("purge bitmaps: %w", err)
	}
	return nil
}

// DataDir returns the server's data directory inside the container.
func (c *Client) DataDir(ctx context.Context) (string, error) {
	v, ok, err := c.Variable(ctx, "datadir")
	if err != nil {
		return "", err
	}
	if !ok || v == "" {
		return "", errors.New("datadir is not set")
	}
	return strings.TrimRight(v, "/"), nil
}

// BitmapSize returns the total size of the changed page bitmap files.
func (c *Client) BitmapSize(ctx context.Context) (int64, error) {
	dir, err := c.DataDir(ctx)
	if err != nil {
		return 0, err
	}
	const script = `for f in "$1"/ib_modified_log_*.xdb; do [ -f "$f" ] && wc -c < "$f"; done; true`
	out, err := c.Exec.Output(ctx, c.Container, "sh", "-c", script, "sh", dir)
	if err != nil {
		return 0, fmt.Errorf("bitmap size: %w", err)
	}
	return sumLines(out)
}

// DataDirSize returns the size of the data directory in bytes, used to
// estimate the first backup's size (PRD E-3).
func (c *Client) DataDirSize(ctx context.Context) (int64, error) {
	dir, err := c.DataDir(ctx)
	if err != nil {
		return 0, err
	}
	out, err := c.Exec.Output(ctx, c.Container, "du", "-sk", dir)
	if err != nil {
		return 0, fmt.Errorf("datadir size: %w", err)
	}
	kb, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\t")
	n, err := strconv.ParseInt(kb, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse du output %q: %w", out, err)
	}
	return n * 1024, nil
}

func sumLines(out []byte) (int64, error) {
	var total int64
	for f := range strings.FieldsSeq(string(out)) {
		n, err := strconv.ParseInt(f, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse size %q: %w", f, err)
		}
		total += n
	}
	return total, nil
}
