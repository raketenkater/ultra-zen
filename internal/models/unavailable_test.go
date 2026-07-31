package models

import (
	"os"
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
