// Package logging builds the structured JSON loggers used by both binaries
// (PRD 9: JSON logs, rotated daily, never containing passwords).
package logging

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// KeepDays is how many daily log files are kept.
const KeepDays = 30

// New returns a logger writing JSON to daily files <dir>/<name>-YYYY-MM-DD.log,
// or text to stderr when dir is empty. Close the returned io.Closer on exit.
func New(level, dir, name string) (*slog.Logger, io.Closer, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, nil, fmt.Errorf("log level %q: %w", level, err)
	}
	opts := &slog.HandlerOptions{Level: lvl, ReplaceAttr: redact}
	if dir == "" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts)), io.NopCloser(nil), nil
	}
	w, err := NewDailyWriter(dir, name, KeepDays)
	if err != nil {
		return nil, nil, err
	}
	return slog.New(slog.NewJSONHandler(w, opts)), w, nil
}

// redact hides attributes whose key looks like a secret.
func redact(_ []string, a slog.Attr) slog.Attr {
	k := strings.ToLower(a.Key)
	if strings.Contains(k, "password") || strings.Contains(k, "token") || strings.Contains(k, "secret") {
		return slog.String(a.Key, "[REDACTED]")
	}
	return a
}

// DailyWriter writes to a file per local day and deletes old files.
type DailyWriter struct {
	dir, name string
	keep      int
	now       func() time.Time

	mu   sync.Mutex
	day  string
	file *os.File
}

// NewDailyWriter creates dir if needed and opens today's file.
func NewDailyWriter(dir, name string, keep int) (*DailyWriter, error) {
	return newDailyWriter(dir, name, keep, time.Now)
}

func newDailyWriter(dir, name string, keep int, now func() time.Time) (*DailyWriter, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	w := &DailyWriter{dir: dir, name: name, keep: keep, now: now}
	if err := w.rotate(w.now().Format(time.DateOnly)); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *DailyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if day := w.now().Format(time.DateOnly); day != w.day {
		if err := w.rotate(day); err != nil {
			return 0, err
		}
	}
	return w.file.Write(p)
}

// Close closes the current file.
func (w *DailyWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *DailyWriter) path(day string) string {
	return filepath.Join(w.dir, w.name+"-"+day+".log")
}

func (w *DailyWriter) rotate(day string) error {
	f, err := os.OpenFile(w.path(day), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o640)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	var closeErr error
	if w.file != nil {
		closeErr = w.file.Close()
	}
	w.file, w.day = f, day
	return errors.Join(closeErr, w.prune())
}

// prune removes all but the newest keep files.
func (w *DailyWriter) prune() error {
	matches, err := filepath.Glob(filepath.Join(w.dir, w.name+"-????-??-??.log"))
	if err != nil || len(matches) <= w.keep {
		return err
	}
	slices.Sort(matches)
	var errs []error
	for _, m := range matches[:len(matches)-w.keep] {
		if err := os.Remove(m); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
