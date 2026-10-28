package podman

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const inspectJSON = `[
 {
  "Id": "abc123",
  "Name": "gays_maindb_slave",
  "ImageName": "docker.io/percona/percona-server:5.6.51",
  "State": {"Status": "running", "Running": true},
  "Mounts": [
   {"Type": "bind", "Source": "/data/gays", "Destination": "/var/lib/mysql"},
   {"Type": "bind", "Source": "/data/backup", "Destination": "/data/backup"},
   {"Type": "tmpfs", "Source": "", "Destination": "/run"}
  ],
  "NetworkSettings": {"Ports": {"3306/tcp": [{"HostIp": "10.23.1.202", "HostPort": "3306"}]}}
 },
 {
  "Id": "def456",
  "Name": "fetisch_maindb_slave",
  "ImageName": "docker.io/percona/percona-server:5.6.51",
  "State": {"Status": "exited", "Running": false},
  "Mounts": [],
  "NetworkSettings": {"Ports": {}}
 }
]`

// fakePodman writes a podman stand-in that answers ps, inspect and exec.
func fakePodman(t *testing.T) *Client {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "inspect.json"), []byte(inspectJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
dir=$(dirname "$0")
case "$1" in
ps) printf 'abc123\ndef456\n' ;;
container)
  shift 4
  case "$1" in
  missing) echo "Error: no such container missing" >&2; exit 125 ;;
  gays_maindb_slave) sed -n '1,12p' "$dir/inspect.json"; echo ' }]' ;;
  *) cat "$dir/inspect.json" ;;
  esac ;;
exec)
  shift
  [ "$1" = --user ] && shift 2
  shift
  case "$1" in
  fail) echo "boom" >&2; exit 3 ;;
  sleep) exec sleep 30 ;;
  *) echo "$@" ;;
  esac ;;
esac
`
	bin := filepath.Join(dir, "podman")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return New(bin)
}

func TestList(t *testing.T) {
	c := fakePodman(t)
	got, err := c.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d containers, want 2", len(got))
	}
	if got[0].Name != "fetisch_maindb_slave" || got[0].Running {
		t.Errorf("containers not sorted by name or wrong state: %+v", got[0])
	}
	g := got[1]
	if !g.Running || g.State != "running" || g.Image != "docker.io/percona/percona-server:5.6.51" {
		t.Errorf("gays = %+v", g)
	}
	if want := []string{"10.23.1.202:3306->3306/tcp"}; !slices.Equal(g.Ports, want) {
		t.Errorf("ports = %v, want %v", g.Ports, want)
	}
	if len(g.Mounts) != 2 || g.Mounts[1] != (Mount{Source: "/data/backup", Destination: "/data/backup"}) {
		t.Errorf("mounts = %+v", g.Mounts)
	}
}

func TestInspect(t *testing.T) {
	c := fakePodman(t)
	got, err := c.Inspect(t.Context(), "gays_maindb_slave")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "gays_maindb_slave" || !got.Running {
		t.Errorf("got %+v", got)
	}

	_, err = c.Inspect(t.Context(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("missing container: err = %v, want ErrNotFound", err)
	}
}

func TestOutput(t *testing.T) {
	c := fakePodman(t)
	out, err := c.Output(t.Context(), "gays_maindb_slave", "echo", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "echo hello" {
		t.Errorf("output = %q", got)
	}

	c.User = "root"
	out, err = c.Output(t.Context(), "gays_maindb_slave", "echo", "as-root")
	if err != nil || strings.TrimSpace(string(out)) != "echo as-root" {
		t.Errorf("with user: out=%q err=%v", out, err)
	}

	_, err = c.Output(t.Context(), "gays_maindb_slave", "fail")
	exitErr, ok := errors.AsType[*ExitError](err)
	if !ok {
		t.Fatalf("err = %v, want *ExitError", err)
	}
	if exitErr.ExitCode != 3 || exitErr.Stderr != "boom" {
		t.Errorf("exit error = %+v", exitErr)
	}
}

func TestExecCanceled(t *testing.T) {
	c := fakePodman(t)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Output(ctx, "gays_maindb_slave", "sleep")
	if err == nil {
		t.Fatal("want error from canceled command")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("cancel took %s", d)
	}
}

func TestNotFoundBinary(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "nope"))
	if _, err := c.List(t.Context()); err == nil {
		t.Error("want error for missing binary")
	}
}
