package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rodericliu/backup/internal/crypto"
)

func TestCommands(t *testing.T) {
	var out strings.Builder
	if err := run([]string{"version"}, &out); err != nil || !strings.HasPrefix(out.String(), "backup-server ") {
		t.Errorf("version: %q %v", out.String(), err)
	}
	if err := run(nil, &out); err == nil {
		t.Error("no command: want error")
	}
	if err := run([]string{"bogus"}, &out); err == nil {
		t.Error("unknown command: want error")
	}
}

func TestCertAndKeygen(t *testing.T) {
	dir := t.TempDir()
	ca, tls := filepath.Join(dir, "ca"), filepath.Join(dir, "tls")
	var out strings.Builder
	steps := [][]string{
		{"cert", "init", "-dir", ca},
		{"cert", "issue", "-ca-dir", ca, "-out", tls, "-host", "backup.internal,10.0.0.1", "-host", "localhost"},
		{"keygen", "-out", filepath.Join(dir, "master.key")},
	}
	for _, args := range steps {
		if err := run(args, &out); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if !strings.Contains(out.String(), "backup.internal, 10.0.0.1, localhost") {
		t.Errorf("output:\n%s", out.String())
	}
	for _, f := range []string{filepath.Join(ca, crypto.CACertFile), filepath.Join(tls, crypto.ServerKeyFile)} {
		if _, err := os.Stat(f); err != nil {
			t.Error(err)
		}
	}
	if _, err := crypto.LoadKeyFile(filepath.Join(dir, "master.key")); err != nil {
		t.Error(err)
	}
	if err := run([]string{"cert", "issue", "-ca-dir", ca, "-out", tls}, &out); err == nil {
		t.Error("cert issue without -host: want error")
	}
}
