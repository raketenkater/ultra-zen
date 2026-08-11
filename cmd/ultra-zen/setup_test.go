package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raketenkater/ultra-zen/internal/keys"
)

// TestSetupFindBindir verifies the bin-dir resolution mirrors install.sh:
// explicit override wins, /usr/local/bin when writable, /usr/local/bin with
// needsSudo when not writable, else ~/.local/bin.
func TestSetupFindBindir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if dir, sudo := setupFindBindir("/opt/custom"); dir != "/opt/custom" || sudo {
		t.Fatalf("override = (%q, %v), want (/opt/custom, false)", dir, sudo)
	}

	// /usr/local/bin may or may not be writable; assert we get SOME dir and the
	// needsSudo flag matches writability.
	dir, sudo := setupFindBindir("")
	if dir == "" {
		t.Fatal("empty bindir")
	}
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		if w, _ := isWritable(dir); w && sudo {
			t.Fatalf("writable dir %s marked needsSudo", dir)
		}
	}

	// When /usr/local/bin is not a dir (rare), fall back to ~/.local/bin.
	if _, err := os.Stat("/usr/local/bin"); err != nil && !os.IsNotExist(err) {
		// Can't reliably simulate; skip.
	} else if _, err := os.Stat("/usr/local/bin"); os.IsNotExist(err) {
		if dir != filepath.Join(home, ".local", "bin") {
			t.Fatalf("fallback = %q, want %q", dir, filepath.Join(home, ".local", "bin"))
		}
	}
}

// TestSetupCreateSymlink verifies a symlink is created and an existing
// unrelated file is not clobbered.
func TestSetupCreateSymlink(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "ultra-zen")
	link := filepath.Join(dir, "uz")

	if err := setupCreateSymlink(bin, link); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got, err := os.Readlink(link); err != nil || got != bin {
		t.Fatalf("Readlink = %q, err %v; want %q", got, err, bin)
	}

	// Idempotent when already pointing at the same binary.
	if err := setupCreateSymlink(bin, link); err != nil {
		t.Fatalf("re-create: %v", err)
	}

	// Refuses to clobber an unrelated file.
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := setupCreateSymlink(bin, other); err == nil {
		t.Fatal("should refuse to clobber unrelated file")
	}
}

// TestSetupInstallBinary verifies the binary is copied and made executable.
func TestSetupInstallBinary(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("binary"), 0644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "bin", "ultra-zen")
	if err := setupInstallBinary(src, dst); err != nil {
		t.Fatalf("install: %v", err)
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat dst: %v", err)
	}
	if st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("dst not executable: %v", st.Mode())
	}
	b, _ := os.ReadFile(dst)
	if string(b) != "binary" {
		t.Fatalf("dst content = %q", b)
	}
}

// TestSetupCopyUserKeys verifies per-user keys are copied into the system
// store and the user store is left untouched.
func TestSetupCopyUserKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ULTRA_ZEN_SYSTEM_KEYS", t.TempDir())

	// Seed a user key for one provider.
	if err := keys.Save("openrouter", "sk-or-secret"); err != nil {
		t.Fatal(err)
	}

	copied := setupCopyUserKeys()
	if len(copied) != 1 || copied[0] != "openrouter" {
		t.Fatalf("copied = %v, want [openrouter]", copied)
	}
	// System store now has it, user store still has it.
	if got := keys.LoadFrom("openrouter", keys.StoreSystem); got != "sk-or-secret" {
		t.Fatalf("system key = %q, want %q", got, "sk-or-secret")
	}
	if got := keys.Load("openrouter"); got != "sk-or-secret" {
		t.Fatalf("user key = %q, want %q", got, "sk-or-secret")
	}
}

// TestSetupCopyUserKeysSkipsEmpty verifies providers without a user key are
// not copied.
func TestSetupCopyUserKeysSkipsEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ULTRA_ZEN_SYSTEM_KEYS", t.TempDir())

	if copied := setupCopyUserKeys(); len(copied) != 0 {
		t.Fatalf("copied = %v, want empty (no user keys)", copied)
	}
}

// TestSetupReport uses the stdout capture convention to check the report.
func TestSetupReport(t *testing.T) {
	var buf strings.Builder
	old := stdout
	stdout = &buf
	defer func() { stdout = old }()

	reportSetup("/opt/bin", "/opt/bin/uz", false, false)
	out := buf.String()
	if !strings.Contains(out, "uz ->") {
		t.Fatalf("report missing uz line: %q", out)
	}
	if !strings.Contains(out, "/opt/bin/ultra-zen") {
		t.Fatalf("report missing bin path: %q", out)
	}
}
