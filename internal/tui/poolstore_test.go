package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFreePoolStoreRoundTripPreservesOrder(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	want := []FreeRoute{
		{Provider: "openrouter", Model: "vendor/a:free"},
		{Provider: "opencode-go", Model: "deepseek-free"},
		{Provider: "modelscope", Model: "zai/GLM"},
	}
	if err := SaveFreePool(want); err != nil {
		t.Fatal(err)
	}
	got := LoadFreePool()
	if len(got) != len(want) {
		t.Fatalf("LoadFreePool = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("route %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	info, err := os.Stat(freePoolPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("permissions = %v, want 0600", got)
	}
}

func TestFreePoolStoreNormalizesInvalidAndDuplicateRoutes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	routes := []FreeRoute{
		{Provider: " openrouter ", Model: " a:free "},
		{Provider: "openrouter", Model: "a:free"},
		{Provider: "unknown", Model: "bad"},
		{Provider: "groq", Model: ""},
	}
	if err := SaveFreePool(routes); err != nil {
		t.Fatal(err)
	}
	got := LoadFreePool()
	if len(got) != 1 || got[0] != (FreeRoute{Provider: "openrouter", Model: "a:free"}) {
		t.Fatalf("normalized pool = %v", got)
	}
}

func TestFreePoolStoreClearIsIdempotent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := SaveFreePool(nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveFreePool([]FreeRoute{{Provider: "groq", Model: "model"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveFreePool(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(freePoolPath()); !os.IsNotExist(err) {
		t.Fatalf("pool file still exists: %v", err)
	}
}

func TestFreePoolStoreIgnoresCorruptFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(freePoolPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(freePoolPath(), []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := LoadFreePool(); got != nil {
		t.Fatalf("corrupt pool loaded as %v", got)
	}
}
