package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDailyWriterRotatesAndPrunes(t *testing.T) {
	dir := t.TempDir()
	day := time.Date(2026, 9, 30, 23, 0, 0, 0, time.Local)
	w, err := newDailyWriter(dir, "agent", 3, func() time.Time { return day })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	for i := range 5 {
		day = time.Date(2026, 10, 1+i, 12, 0, 0, 0, time.Local)
		if _, err := w.Write([]byte("line\n")); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "agent-*.log"))
	if len(files) != 3 {
		t.Fatalf("files = %v, want 3 newest", files)
	}
	if filepath.Base(files[0]) != "agent-2026-10-03.log" || filepath.Base(files[2]) != "agent-2026-10-05.log" {
		t.Errorf("files = %v", files)
	}
}

func TestNewRedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	log, closer, err := New("info", dir, "agent")
	if err != nil {
		t.Fatal(err)
	}
	log.Info("connect", "mysql_password", "s3cret", "agent_token", "tok", "user", "backup")
	log.Debug("hidden")
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "agent-*.log"))
	if len(files) != 1 {
		t.Fatalf("files = %v", files)
	}
	b, _ := os.ReadFile(files[0])
	s := string(b)
	if strings.Contains(s, "s3cret") || strings.Contains(s, "tok\"") || !strings.Contains(s, `"user":"backup"`) {
		t.Errorf("log = %s", s)
	}
	if strings.Contains(s, "hidden") {
		t.Error("debug message logged at info level")
	}
	if !strings.HasPrefix(s, "{") {
		t.Error("log is not JSON")
	}
}

func TestNewInvalidLevel(t *testing.T) {
	if _, _, err := New("loud", "", "x"); err == nil {
		t.Error("want error")
	}
}
