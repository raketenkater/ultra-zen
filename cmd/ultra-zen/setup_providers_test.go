package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raketenkater/ultra-zen/internal/keys"
	"github.com/raketenkater/ultra-zen/internal/models"
)

// setupTestEnv isolates every store setup touches: the per-user key store
// (XDG_CONFIG_HOME), the system store (ULTRA_ZEN_SYSTEM_KEYS), and opencode's
// auth.json (HOME drives auth.DefaultPath). Returns the fake HOME.
func setupTestEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ULTRA_ZEN_SYSTEM_KEYS", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	// opencode's canonical auth.json location: absent unless a test writes it.
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("MODELSCOPE_API_KEY", "")
	t.Setenv("GROQ_API_KEY", "")
	t.Setenv("CEREBRAS_API_KEY", "")
	t.Setenv("HF_TOKEN", "")
	t.Setenv("COHERE_API_KEY", "")
	t.Setenv("SAIA_API_KEY", "")
	return home
}

// captureStdout redirects the package-level stdout/stdin indirections for the
// duration of fn and returns what was printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	var buf strings.Builder
	oldOut, oldIn := stdout, stdin
	stdout, stdin = &buf, strings.NewReader("")
	defer func() { stdout, stdin = oldOut, oldIn }()
	fn()
	return buf.String()
}

// TestKeySourceLabelsMissing verifies the no-key path: with every source
// empty, every provider reports "not set".
func TestKeySourceLabelsMissing(t *testing.T) {
	setupTestEnv(t)
	for _, p := range setupProviderOrder {
		if got := keySourceLabel(p); got != "not set" {
			t.Errorf("%s = %q, want %q", p, got, "not set")
		}
	}
}

// TestKeySourceLabelsPrecedence verifies env > user store > system store >
// opencode auth.json, matching the launcher's resolution order.
func TestKeySourceLabelsPrecedence(t *testing.T) {
	home := setupTestEnv(t)

	// Seed opencode auth.json only: the opencode-go fallback source.
	authDir := filepath.Join(home, ".local", "share", "opencode")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	authJSON := `{"opencode-go":{"type":"api","key":"zen-from-auth"}}`
	if err := os.WriteFile(filepath.Join(authDir, "auth.json"), []byte(authJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := keySourceLabel("opencode-go"); got != "set (opencode auth)" {
		t.Fatalf("auth.json only: %q, want %q", got, "set (opencode auth)")
	}

	// A system key outranks auth.json; a user key outranks the system key.
	if err := keys.SaveSystem("opencode-go", "zen-system"); err != nil {
		t.Fatal(err)
	}
	if got := keySourceLabel("opencode-go"); got != "set (system store)" {
		t.Fatalf("system store: %q, want %q", got, "set (system store)")
	}
	if err := keys.Save("opencode-go", "zen-user"); err != nil {
		t.Fatal(err)
	}
	if got := keySourceLabel("opencode-go"); got != "set (user store)" {
		t.Fatalf("user store: %q, want %q", got, "set (user store)")
	}

	// An env var outranks every store.
	t.Setenv("MODELSCOPE_API_KEY", "ms-env")
	if err := keys.Save("modelscope", "ms-stored"); err != nil {
		t.Fatal(err)
	}
	if got := keySourceLabel("modelscope"); got != "set (env)" {
		t.Fatalf("env: %q, want %q", got, "set (env)")
	}
}

// TestSetupProvidersTable verifies the status table lists every provider with
// key source and tier, and that a non-interactive run prints recipes and never
// prompts.
func TestSetupProvidersTable(t *testing.T) {
	setupTestEnv(t)
	if err := keys.Save("openrouter", "or-secret"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		setupProvidersRun(http.DefaultClient, false)
	})
	for _, want := range []string{
		"PROVIDER", "opencode-go", "openrouter", "modelscope", "saia",
		"set (user store)", "not set", "credits (go tier)",
		"uz keys set <provider> -",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q:\n%s", want, out)
		}
	}
	// The secret must never be printed back.
	if strings.Contains(out, "or-secret") {
		t.Error("table printed the stored key")
	}
}

// TestSetupProvidersIdempotentRerun verifies that with every provider key
// present the run reports everything configured and does not offer the
// configure prompt.
func TestSetupProvidersIdempotentRerun(t *testing.T) {
	setupTestEnv(t)
	for _, p := range setupProviderOrder {
		if err := keys.Save(p, "k-"+p); err != nil {
			t.Fatal(err)
		}
	}
	out := captureStdout(t, func() {
		setupProvidersRun(http.DefaultClient, false)
	})
	if !strings.Contains(out, "All provider keys configured") {
		t.Fatalf("expected all-configured report, got:\n%s", out)
	}
	if strings.Contains(out, "No key yet") {
		t.Fatalf("re-run offered configure prompt:\n%s", out)
	}
}

// TestSetupValidateKey verifies the key is validated against the provider's
// cheapest endpoint before storing: a good key stores, a rejected one does
// not, and the probe hits the server with a Bearer header.
func TestSetupValidateKey(t *testing.T) {
	setupTestEnv(t)
	var gotAuth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") == "Bearer sk-good" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	client := srv.Client()

	// Retarget the free-tier probe through the fake server (vars, so tests can).
	oldBase := models.FreeTierProviders["modelscope"]
	models.FreeTierProviders["modelscope"] = models.FreeTierProvider{
		Base: srv.URL, EnvKey: oldBase.EnvKey, KeyHint: oldBase.KeyHint,
	}
	defer func() { models.FreeTierProviders["modelscope"] = oldBase }()
	oldOR := setupProbeOpenRouter
	setupProbeOpenRouter = srv.URL + "/key"
	defer func() { setupProbeOpenRouter = oldOR }()

	if err := setupValidateKey(client, "modelscope", "sk-good"); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	if err := setupValidateKey(client, "openrouter", "sk-bad"); err == nil {
		t.Fatal("invalid key accepted for openrouter probe (401 expected)")
	}
	if len(gotAuth) == 0 || gotAuth[0] != "Bearer sk-good" {
		t.Fatalf("probe Authorization = %v, want Bearer sk-good", gotAuth)
	}

	// setupStoreKey stores only after validation succeeds.
	if err := setupStoreKey(client, "modelscope", "sk-good"); err != nil {
		t.Fatalf("store: %v", err)
	}
	if got := keys.Load("modelscope"); got != "sk-good" {
		t.Fatalf("stored = %q, want sk-good", got)
	}
	if err := setupStoreKey(client, "modelscope", "sk-bad"); err == nil {
		t.Fatal("bad key stored")
	}
	if got := keys.Load("modelscope"); got != "sk-good" {
		t.Fatalf("bad key overwrote good one: %q", got)
	}
}

// TestSetupProvidersNonInteractiveNeverPrompts verifies that a run whose
// stdin is not a real terminal (pipe or /dev/null — /dev/null IS a character
// device, the classic false positive) prints the table and recipes without
// ever printing an interactive prompt or reading stdin.
func TestSetupProvidersNonInteractiveNeverPrompts(t *testing.T) {
	setupTestEnv(t)
	out := captureStdout(t, func() {
		cmdSetupProviders(nil)
	})
	if strings.Contains(out, "[y/N]") {
		t.Fatalf("non-interactive run printed an interactive prompt:\n%s", out)
	}
	if !strings.Contains(out, "uz keys set <provider> -") {
		t.Fatalf("non-interactive run missing recipes:\n%s", out)
	}
}

// TestStdinIsTerminalDevNull verifies the /dev/null exclusion directly.
func TestStdinIsTerminalDevNull(t *testing.T) {
	// The test binary's stdin is whatever `go test` provides; only assert the
	// /dev/null-detection branch by swapping the check through os.Stdin is
	// not possible in-process, so verify the helper agrees /dev/null is not a
	// terminal by opening it as stdin is left to the smoke test. Here we only
	// assert the function returns a bool without panicking when Stat fails:
	// temporarily point os.Stdin at a closed fd.
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skip("cannot open /dev/null")
	}
	defer f.Close()
	old := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = old }()
	if stdinIsTerminal() {
		t.Fatal("/dev/null stdin reported as interactive terminal")
	}
}

// TestSetupProbeOverrides verifies the opencode-go and OpenRouter probes can
// be retargeted at a fake server (the production endpoints have no entry in
// models.FreeTierProviders for tests to mutate).
func TestSetupProbeOverrides(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	oldOR, oldZen := setupProbeOpenRouter, setupProbeZen
	setupProbeOpenRouter, setupProbeZen = srv.URL+"/key", srv.URL+"/models"
	defer func() { setupProbeOpenRouter, setupProbeZen = oldOR, oldZen }()

	client := srv.Client()
	if err := setupValidateKey(client, "openrouter", "k1"); err != nil {
		t.Errorf("openrouter probe: %v", err)
	}
	if err := setupValidateKey(client, "opencode-go", "k2"); err != nil {
		t.Errorf("zen probe: %v", err)
	}
	if probeURL("codex") != "" {
		t.Error("codex should have no probe (no stored key to validate)")
	}
}
