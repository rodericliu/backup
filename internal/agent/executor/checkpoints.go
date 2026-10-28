package executor

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// checkpoints is the content of xtrabackup_checkpoints.
type checkpoints struct {
	BackupType string
	FromLSN    uint64
	ToLSN      uint64
	LastLSN    uint64
}

func readCheckpoints(path string) (*checkpoints, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read checkpoints: %w", err)
	}
	return parseCheckpoints(string(b))
}

func parseCheckpoints(s string) (*checkpoints, error) {
	var c checkpoints
	seen := make(map[string]bool)
	for line := range strings.Lines(s) {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		var dst *uint64
		switch k {
		case "backup_type":
			c.BackupType = v
		case "from_lsn":
			dst = &c.FromLSN
		case "to_lsn":
			dst = &c.ToLSN
		case "last_lsn":
			dst = &c.LastLSN
		default:
			continue
		}
		seen[k] = true
		if dst != nil {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse checkpoints %s: %w", k, err)
			}
			*dst = n
		}
	}
	if !seen["to_lsn"] || !seen["backup_type"] {
		return nil, fmt.Errorf("checkpoints missing backup_type or to_lsn")
	}
	return &c, nil
}
