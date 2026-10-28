package main

import (
	"strings"
	"testing"
)

func TestCheckExampleConfig(t *testing.T) {
	var out strings.Builder
	if err := run([]string{"check", "-config", "../../deploy/agent.yaml.example"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"5 projects", "fetisch_maindb_slave", "kmv2_message", "full  gays"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestCommands(t *testing.T) {
	var out strings.Builder
	if err := run([]string{"version"}, &out); err != nil || !strings.HasPrefix(out.String(), "backup-agent ") {
		t.Errorf("version: %q %v", out.String(), err)
	}
	if err := run(nil, &out); err == nil {
		t.Error("no command: want error")
	}
	if err := run([]string{"bogus"}, &out); err == nil {
		t.Error("unknown command: want error")
	}
}
