package executor

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestParseCheckpoints(t *testing.T) {
	cp, err := parseCheckpoints("backup_type = incremental\nfrom_lsn = 100\nto_lsn = 250\nlast_lsn = 260\ncompact = 0\nrecover_binlog_info = 0\n")
	if err != nil {
		t.Fatal(err)
	}
	if cp.BackupType != "incremental" || cp.FromLSN != 100 || cp.ToLSN != 250 || cp.LastLSN != 260 {
		t.Errorf("got %+v", cp)
	}
	for _, bad := range []string{"", "backup_type = full-backuped\n", "backup_type = x\nto_lsn = abc\n"} {
		if _, err := parseCheckpoints(bad); err == nil {
			t.Errorf("parseCheckpoints(%q): want error", bad)
		}
	}
}

func TestParseSlaveInfo(t *testing.T) {
	si := parseSlaveInfo("181028 01:02:03 MySQL slave binlog position: master host '10.20.0.1', filename 'mysql-bin.001234', position '98765'")
	if si == nil || si.MasterLogFile != "mysql-bin.001234" || si.MasterLogPos != 98765 ||
		si.Raw != "CHANGE MASTER TO MASTER_LOG_FILE='mysql-bin.001234', MASTER_LOG_POS=98765;" {
		t.Errorf("file position: %+v", si)
	}
	si = parseSlaveInfo("MySQL slave binlog position: master host '10.20.0.1', purge list 'uuid:1-100', channel name: ''")
	if si == nil || si.GTIDExecuted != "uuid:1-100" || !strings.Contains(si.Raw, "MASTER_AUTO_POSITION=1") {
		t.Errorf("gtid: %+v", si)
	}
	if parseSlaveInfo("something else") != nil {
		t.Error("unrelated line parsed")
	}
}

func TestLogScanner(t *testing.T) {
	var sb strings.Builder
	s := newLogScanner(&sb)
	for i := range LogTailLines + 10 {
		// Split writes across line boundaries.
		line := fmt.Sprintf("line %d\n", i)
		_, _ = io.WriteString(s, line[:3])
		_, _ = io.WriteString(s, line[3:])
	}
	_, _ = io.WriteString(s, "xtrabackup: using the changed page bitmap\n")
	_, _ = io.WriteString(s, "completed OK!")
	_ = s.Close()

	f := s.facts()
	if !f.completed || !f.usedBitmap {
		t.Errorf("facts = %+v", f)
	}
	tail := strings.Split(s.Tail(LogTailLines), "\n")
	if len(tail) != LogTailLines || tail[len(tail)-1] != "completed OK!" || tail[0] != "line 12" {
		t.Errorf("tail has %d lines, first %q last %q", len(tail), tail[0], tail[len(tail)-1])
	}
	if got := s.Tail(2); got != "xtrabackup: using the changed page bitmap\ncompleted OK!" {
		t.Errorf("Tail(2) = %q", got)
	}
	if !strings.HasPrefix(sb.String(), "line 0\nline 1\n") {
		t.Error("log not copied to writer")
	}
}

func TestLogScannerFullScanOverridesBitmap(t *testing.T) {
	s := newLogScanner(io.Discard)
	_, _ = io.WriteString(s, "xtrabackup: using the changed page bitmap\nxtrabackup: using the full scan for incremental backup\n")
	if s.facts().usedBitmap {
		t.Error("full scan fallback should not count as bitmap use")
	}
}
