package store_test

import (
	"errors"
	"testing"

	"github.com/rodericliu/backup/internal/store"
	"github.com/rodericliu/backup/internal/store/storetest"
)

func TestMigrate(t *testing.T) {
	s := storetest.New(t)
	v, err := s.SchemaVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if v != 2 {
		t.Errorf("schema version = %d, want 2", v)
	}
	// Migrating again is a no-op.
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := s.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestSettings(t *testing.T) {
	s := storetest.New(t)
	ctx := t.Context()
	type wecom struct {
		Webhook string `json:"webhook"`
		Hour    int    `json:"hour"`
	}
	var got wecom
	if err := s.Setting(ctx, "wecom", &got); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing setting: err = %v, want ErrNotFound", err)
	}
	for _, want := range []wecom{{"https://example.com/a", 9}, {"https://example.com/b", 10}} {
		if err := s.SetSetting(ctx, "wecom", want); err != nil {
			t.Fatal(err)
		}
		if err := s.Setting(ctx, "wecom", &got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("setting = %+v, want %+v", got, want)
		}
	}
}
