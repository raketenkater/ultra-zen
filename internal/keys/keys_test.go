package keys

import (
	"os"
	"path/filepath"
	"testing"
)

// withIsolatedStores redirects BOTH the per-user store (XDG_CONFIG_HOME) and
// the system store (ULTRA_ZEN_SYSTEM_KEYS) to temp dirs so tests never read a
// real /etc/ultra-zen or the developer's own ~/.config/ultra-zen.
func withIsolatedStores(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ULTRA_ZEN_SYSTEM_KEYS", t.TempDir())
}

func TestSaveLoadRoundTrip(t *testing.T) {
	withIsolatedStores(t)

	if err := Save("modelscope", "sk-secret"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := Load("modelscope"); got != "sk-secret" {
		t.Fatalf("Load = %q, want %q", got, "sk-secret")
	}

	// The user file must be 0600 so the secret isn't world-readable.
	info, err := os.Stat(filepath.Join(Path(), "modelscope"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("permissions = %v, want 0600", perm)
	}
}

func TestSaveEmptyRemoves(t *testing.T) {
	withIsolatedStores(t)

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
	withIsolatedStores(t)

	if Load("nope") != "" {
		t.Fatal("Load of missing key should be empty")
	}
	if Has("nope") {
		t.Fatal("Has of missing key should be false")
	}
}

func TestClearMissingIsIdempotent(t *testing.T) {
	withIsolatedStores(t)
	if err := Save("openrouter", ""); err != nil {
		t.Fatalf("clearing a missing key: %v", err)
	}
}

func TestSaveAndLoadTrimWhitespace(t *testing.T) {
	withIsolatedStores(t)
	if err := Save("openrouter", "  sk-or-secret\n"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := Load("openrouter"); got != "sk-or-secret" {
		t.Fatalf("Load = %q, want trimmed key", got)
	}
}

func TestSaveRepairsExistingPermissions(t *testing.T) {
	withIsolatedStores(t)
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
	withIsolatedStores(t)
	if err := Save("../escape", "secret"); err == nil {
		t.Fatal("Save accepted a provider path outside the key directory")
	}
	if got := Load("../escape"); got != "" {
		t.Fatalf("Load traversal = %q, want empty", got)
	}
}

// TestLoadUserOverridesSystem verifies precedence: a per-user key wins over a
// system key for the same provider, so any user can opt out of the shared
// credential for their own sessions.
func TestLoadUserOverridesSystem(t *testing.T) {
	withIsolatedStores(t)

	if err := SaveSystem("openrouter", "sk-system"); err != nil {
		t.Fatalf("SaveSystem: %v", err)
	}
	if err := Save("openrouter", "sk-user"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := Load("openrouter"); got != "sk-user" {
		t.Fatalf("Load = %q, want the per-user key", got)
	}
}

// TestLoadSystemFallback verifies a shared system key is used when the user
// has no personal key.
func TestLoadSystemFallback(t *testing.T) {
	withIsolatedStores(t)

	if err := SaveSystem("modelscope", "sk-system"); err != nil {
		t.Fatalf("SaveSystem: %v", err)
	}
	if got := Load("modelscope"); got != "sk-system" {
		t.Fatalf("Load = %q, want the system key", got)
	}
}

// TestSaveSystemIs0644ReadableByAll verifies system keys are world-readable
// (0644) — the deliberate trade-off of system-wide shared credentials — and
// that a per-user load can read them.
func TestSaveSystemIs0644ReadableByAll(t *testing.T) {
	withIsolatedStores(t)

	if err := SaveSystem("groq", "sk-groq"); err != nil {
		t.Fatalf("SaveSystem: %v", err)
	}
	p := filepath.Join(SystemDir(), "groq")
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0644 {
		t.Fatalf("system key permissions = %v, want 0644", perm)
	}
	if got := LoadFrom("groq", StoreSystem); got != "sk-groq" {
		t.Fatalf("LoadFrom(system) = %q, want %q", got, "sk-groq")
	}
}

// TestSaveSystemEmptyRemoves verifies an empty value deletes the system key.
func TestSaveSystemEmptyRemoves(t *testing.T) {
	withIsolatedStores(t)
	if err := SaveSystem("openrouter", "sk-or"); err != nil {
		t.Fatal(err)
	}
	if err := SaveSystem("openrouter", ""); err != nil {
		t.Fatal(err)
	}
	if HasIn("openrouter", StoreSystem) {
		t.Fatal("system key should be gone after clearing")
	}
}

// TestSystemRejectsTraversal verifies the system store keeps the same
// path-traversal guard as the user store.
func TestSystemRejectsTraversal(t *testing.T) {
	withIsolatedStores(t)
	if err := SaveSystem("../escape", "secret"); err == nil {
		t.Fatal("SaveSystem accepted a provider path outside the key directory")
	}
	if got := LoadFrom("../escape", StoreSystem); got != "" {
		t.Fatalf("LoadFrom system traversal = %q, want empty", got)
	}
}

// TestSystemDirOverride verifies ULTRA_ZEN_SYSTEM_KEYS redirects the store.
func TestSystemDirOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ULTRA_ZEN_SYSTEM_KEYS", dir)
	if got := SystemDir(); got != dir {
		t.Fatalf("SystemDir = %q, want %q", got, dir)
	}
}
