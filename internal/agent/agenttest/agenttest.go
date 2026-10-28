// Package agenttest provides fake podman, mysql, du and xtrabackup commands
// for tests of the agent packages.
package agenttest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The fakes run every `podman exec` directly on the host with a fake bin
// directory first in PATH, so the real shell wrapper, nice and zstd pipeline
// are exercised.
const Podman = `#!/bin/sh
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

const MySQL = `#!/bin/sh
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

const Du = `#!/bin/sh
printf '1024\t%s\n' "$2"
`

const Xtrabackup = `#!/bin/sh
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
echo "MySQL slave binlog position: master host '10.0.0.1', filename 'mysql-bin.000042', position '4711', channel name: ''" >&2
[ "$FAKE_XB" = nook ] && exit 0
echo "xtrabackup: Transaction log of lsn ($from) to ($to) was copied." >&2
echo "$(date) completed OK!" >&2
`

// Install writes the fakes to a temporary bin directory placed first in PATH
// and returns the fake podman binary and the directory where the fakes record
// their calls (mysql.log, xtrabackup.args). Behavior is selected with the
// environment variables FAKE_XB (""|slow|nobitmap|fail|stopsql|hang|nook),
// FAKE_CONTAINER (""|missing|stopped) and FAKE_REPL (""|broken). It skips the
// test when zstd is not installed.
func Install(t *testing.T) (podman, dir string) {
	t.Helper()
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd not installed")
	}
	dir = t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"podman": Podman, "mysql": MySQL, "du": Du, "xtrabackup": Xtrabackup,
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
	return filepath.Join(bin, "podman"), dir
}
