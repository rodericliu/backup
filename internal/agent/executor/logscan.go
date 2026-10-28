package executor

import (
	"bytes"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/rodericliu/backup/internal/api"
)

// LogTailLines is the number of log lines kept for job details (PRD J-3).
const LogTailLines = 200

// errorTailLines is the number of log lines included in XTRABACKUP_ERROR
// messages (PRD 6.4.5).
const errorTailLines = 50

var (
	slaveFileRE = regexp.MustCompile(`MySQL slave binlog position: master host '([^']*)', filename '([^']*)', position '?(\d+)'?`)
	slaveGTIDRE = regexp.MustCompile(`MySQL slave binlog position: master host '([^']*)', purge list '([^']*)'`)
)

// logScanner receives xtrabackup's stderr. It copies every byte to w and
// extracts the facts needed to judge and describe the backup.
type logScanner struct {
	w io.Writer

	mu         sync.Mutex
	partial    []byte
	tail       []string // ring buffer of the last LogTailLines lines
	next       int
	completed  bool
	usedBitmap bool
	fullScan   bool
	slave      *api.SlaveInfo
}

func newLogScanner(w io.Writer) *logScanner {
	return &logScanner{w: w, tail: make([]string, 0, LogTailLines)}
}

func (s *logScanner) Write(p []byte) (int, error) {
	n, err := s.w.Write(p)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partial = append(s.partial, p...)
	for {
		i := bytes.IndexByte(s.partial, '\n')
		if i < 0 {
			break
		}
		s.line(string(bytes.TrimRight(s.partial[:i], "\r")))
		s.partial = s.partial[i+1:]
	}
	return n, err
}

// Close flushes a final line without a trailing newline.
func (s *logScanner) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.partial) > 0 {
		s.line(string(s.partial))
		s.partial = nil
	}
	return nil
}

func (s *logScanner) line(l string) {
	if len(s.tail) < LogTailLines {
		s.tail = append(s.tail, l)
	} else {
		s.tail[s.next] = l
		s.next = (s.next + 1) % LogTailLines
	}
	switch {
	case strings.Contains(l, "completed OK!"):
		s.completed = true
	case strings.Contains(l, "using the changed page bitmap"):
		s.usedBitmap = true
	case strings.Contains(l, "using the full scan for incremental backup"):
		s.fullScan = true
	case strings.Contains(l, "MySQL slave binlog position:"):
		s.slave = parseSlaveInfo(l)
	}
}

// Tail returns up to n of the most recent lines.
func (s *logScanner) Tail(n int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	lines := make([]string, 0, len(s.tail))
	lines = append(lines, s.tail[s.next:]...)
	lines = append(lines, s.tail[:s.next]...)
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

type logFacts struct {
	completed  bool
	usedBitmap bool
	slave      *api.SlaveInfo
}

func (s *logScanner) facts() logFacts {
	s.mu.Lock()
	defer s.mu.Unlock()
	return logFacts{
		completed:  s.completed,
		usedBitmap: s.usedBitmap && !s.fullScan,
		slave:      s.slave,
	}
}

// parseSlaveInfo parses the slave position line printed by xtrabackup 2.4 for
// --slave-info and rebuilds the statement it writes to xtrabackup_slave_info.
func parseSlaveInfo(l string) *api.SlaveInfo {
	if m := slaveFileRE.FindStringSubmatch(l); m != nil {
		pos, _ := strconv.ParseUint(m[3], 10, 64)
		return &api.SlaveInfo{
			MasterLogFile: m[2],
			MasterLogPos:  pos,
			Raw:           "CHANGE MASTER TO MASTER_LOG_FILE='" + m[2] + "', MASTER_LOG_POS=" + m[3] + ";",
		}
	}
	if m := slaveGTIDRE.FindStringSubmatch(l); m != nil {
		return &api.SlaveInfo{
			GTIDExecuted: m[2],
			Raw:          "SET GLOBAL gtid_purged='" + m[2] + "';\nCHANGE MASTER TO MASTER_AUTO_POSITION=1;",
		}
	}
	return nil
}
