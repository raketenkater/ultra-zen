package models

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnavailableModelsPersistAndFilter(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := MarkUnavailable("modelscope", "gated/model"); err != nil {
		t.Fatal(err)
	}
	if !IsUnavailable("modelscope", "gated/model") {
		t.Fatal("denial was not persisted")
	}
	list := []Model{{ID: "gated/model"}, {ID: "open/model"}}
	got := FilterUnavailable("modelscope", list)
	if len(got) != 1 || got[0].ID != "open/model" {
		t.Fatalf("filtered models = %+v", got)
	}
	info, err := os.Stat(unavailablePath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions = %v, want 0600", info.Mode().Perm())
	}
}

func TestClearUnavailableIsProviderScoped(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := MarkUnavailable("modelscope", "gated/model"); err != nil {
		t.Fatal(err)
	}
	if err := MarkUnavailable("groq", "retired/model"); err != nil {
		t.Fatal(err)
	}
	if err := ClearUnavailable("modelscope"); err != nil {
		t.Fatal(err)
	}
	if IsUnavailable("modelscope", "gated/model") {
		t.Fatal("ModelScope denial survived clear")
	}
	if !IsUnavailable("groq", "retired/model") {
		t.Fatal("clearing ModelScope removed Groq denial")
	}
}

func TestUnavailableExpiresAfterTTL(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// A fresh denial is active.
	if err := MarkUnavailable("modelscope", "gated/model"); err != nil {
		t.Fatal(err)
	}
	if !IsUnavailable("modelscope", "gated/model") {
		t.Fatal("fresh denial should be active")
	}

	// Rewrite the on-disk entry so its DeniedAt is older than the TTL.
	path := unavailablePath()
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	stale := `[
  {
    "provider": "modelscope",
    "model": "gated/model",
    "denied_at": "2020-01-01T00:00:00Z"
  }
]`
	if err := os.WriteFile(path, []byte(stale), 0600); err != nil {
		t.Fatal(err)
	}
	if IsUnavailable("modelscope", "gated/model") {
		t.Fatal("denial older than TTL should be ignored")
	}
	list := []Model{{ID: "gated/model"}}
	if got := FilterUnavailable("modelscope", list); len(got) != 1 {
		t.Fatalf("expired denial still filtered; got %+v", got)
	}

	// The expired entry should also be pruned from the file on the next load.
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("expired entry was not pruned; file still exists")
	}
}

func TestUnavailableZeroDeniedAtIsExpired(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := unavailablePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`[
  {"provider":"groq","model":"retired/model"}
]`), 0600); err != nil {
		t.Fatal(err)
	}
	if IsUnavailable("groq", "retired/model") {
		t.Fatal("entry with zero DeniedAt should be ignored")
	}
}
