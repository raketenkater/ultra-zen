package keys

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	// Isolate from any real user config.
	dir := t.TempDir()
	prev := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", dir)
	t.Cleanup(func() { os.Setenv("XDG_CONFIG_HOME", prev) })

	if err := Save("modelscope", "sk-secret"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := Load("modelscope"); got != "sk-secret" {
		t.Fatalf("Load = %q, want %q", got, "sk-secret")
	}

	// The file must be 0600 so the secret isn't world-readable.
	info, err := os.Stat(filepath.Join(Path(), "modelscope"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("permissions = %v, want 0600", perm)
	}
}

func TestSaveEmptyRemoves(t *testing.T) {
	dir := t.TempDir()
	prev := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", dir)
	t.Cleanup(func() { os.Setenv("XDG_CONFIG_HOME", prev) })

	if err := Save("openrouter", "sk-or-1"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := Save("openrouter", ""); err != nil {
		t.Fatalf("Save empty: %v", err)
	}
	if Load("openrouter") != "" {
		t.Fatal("Load should be empty after clearing")
	}
	if Has("openrouter") {
		t.Fatal("Has should be false after clearing")
	}
}

func TestLoadMissing(t *testing.T) {
	dir := t.TempDir()
	prev := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", dir)
	t.Cleanup(func() { os.Setenv("XDG_CONFIG_HOME", prev) })

	if Load("nope") != "" {
		t.Fatal("Load of missing key should be empty")
	}
	if Has("nope") {
		t.Fatal("Has of missing key should be false")
	}
}

func TestClearMissingIsIdempotent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save("openrouter", ""); err != nil {
		t.Fatalf("clearing a missing key: %v", err)
	}
}

func TestSaveAndLoadTrimWhitespace(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save("openrouter", "  sk-or-secret\n"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := Load("openrouter"); got != "sk-or-secret" {
		t.Fatalf("Load = %q, want trimmed key", got)
	}
}

func TestSaveRepairsExistingPermissions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(Path(), 0700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(Path(), "openrouter")
	if err := os.WriteFile(p, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0644); err != nil {
		t.Fatal(err)
	}
	if err := Save("openrouter", "new"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("permissions = %v, want 0600", got)
	}
}

func TestRejectsProviderPathTraversal(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save("../escape", "secret"); err == nil {
		t.Fatal("Save accepted a provider path outside the key directory")
	}
	if got := Load("../escape"); got != "" {
		t.Fatalf("Load traversal = %q, want empty", got)
	}
}
