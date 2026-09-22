package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const short = 20 * time.Second

// TestVersionAndHelp is the smoke test everything else depends on: the built
// binary runs, prints its version to stdout, and its usage to stderr.
func TestVersionAndHelp(t *testing.T) {
	e := newEnv(t)

	v := e.run(short, "--version")
	if v.code != 0 {
		t.Fatalf("--version exited %d: %s", v.code, v.out())
	}
	if !strings.HasPrefix(strings.TrimSpace(v.stdout), "ultra-zen ") {
		t.Errorf("--version stdout = %q, want it to start with \"ultra-zen \"", v.stdout)
	}
	if strings.TrimSpace(v.stderr) != "" {
		t.Errorf("--version wrote to stderr: %q", v.stderr)
	}

	h := e.run(short, "-h")
	requireContains(t, h.out(), "--provider", "-h usage")
	requireContains(t, h.out(), "--free-model", "-h usage")
}

// TestUnknownFlagFails keeps a typo from silently launching something.
func TestUnknownFlagFails(t *testing.T) {
	e := newEnv(t)
	r := e.run(short, "--definitely-not-a-flag")
	if r.code == 0 {
		t.Fatalf("an unknown flag exited 0:\n%s", r.out())
	}
}

// TestKeysLifecycle walks the whole key story a user goes through: look at
// what is set, set one from stdin (the documented non-interactive form),
// confirm it is stored with private permissions and not echoed back, then
// clear it.
func TestKeysLifecycle(t *testing.T) {
	e := newEnv(t)

	before := e.run(short, "keys")
	requireContains(t, before.out(), "openrouter", "keys table")
	if strings.Contains(before.out(), "sk-secret") {
		t.Fatal("keys table printed a secret before anything was set")
	}

	set := e.runStdin(short, "sk-secret-value\n", "keys", "set", "openrouter", "-")
	if set.code != 0 {
		t.Fatalf("keys set exited %d: %s", set.code, set.out())
	}

	// The key must be on disk, private to the user.
	path := filepath.Join(e.config, "ultra-zen", "keys", "openrouter")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("key not written to %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file mode = %o, want 600", perm)
	}
	body, _ := os.ReadFile(path)
	if strings.TrimSpace(string(body)) != "sk-secret-value" {
		t.Errorf("stored key = %q, want the value fed on stdin", strings.TrimSpace(string(body)))
	}

	// Listing must report it as set without ever printing it.
	after := e.run(short, "keys")
	if strings.Contains(after.out(), "sk-secret-value") {
		t.Fatalf("keys table leaked the secret:\n%s", after.out())
	}
	requireContains(t, after.out(), "set", "keys table after set")

	if r := e.run(short, "keys", "clear", "openrouter"); r.code != 0 {
		t.Fatalf("keys clear exited %d: %s", r.code, r.out())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("key file survived `keys clear`: %v", err)
	}
}

// TestKeysRejectsUnknownProvider keeps a typo from silently storing a key
// under a name nothing will ever read.
func TestKeysRejectsUnknownProvider(t *testing.T) {
	e := newEnv(t)
	r := e.runStdin(short, "x\n", "keys", "set", "not-a-provider", "-")
	if r.code == 0 {
		t.Fatalf("storing a key for an unknown provider exited 0:\n%s", r.out())
	}
}

// TestSetupInstallsIntoADirectory covers `uz setup --dir`, the documented way
// to put the binary on PATH, including the `uz` shorthand symlink.
func TestSetupInstallsIntoADirectory(t *testing.T) {
	e := newEnv(t)
	target := filepath.Join(e.home, "bin-target")

	r := e.run(short, "setup", "--dir", target)
	if r.code != 0 {
		t.Fatalf("setup exited %d:\n%s", r.code, r.out())
	}

	installed := filepath.Join(target, "ultra-zen")
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatalf("setup did not install the binary: %v\n%s", err, r.out())
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed binary is not executable: mode %o", info.Mode().Perm())
	}
	// The installed copy must actually run.
	v := e.runCmd(short, installed, "--version")
	if v.code != 0 || !strings.Contains(v.stdout, "ultra-zen") {
		t.Errorf("installed binary --version = %q (exit %d)", v.out(), v.code)
	}
	// And the `uz` shorthand must point at it.
	link, err := os.Readlink(filepath.Join(target, "uz"))
	if err != nil {
		t.Fatalf("no uz symlink created: %v", err)
	}
	if link != "ultra-zen" {
		t.Errorf("uz -> %q, want a relative link to ultra-zen", link)
	}
	// The system key store must be the redirected one, never the real
	// /etc path. Asserting the real path is absent would test the machine
	// rather than the binary — it may well exist from a genuine install —
	// so the check is that the redirect was honoured and reported.
	if _, err := os.Stat(e.sysKeys); err != nil {
		t.Errorf("ULTRA_ZEN_SYSTEM_KEYS was not used as the system store: %v", err)
	}
	requireContains(t, r.out(), e.sysKeys, "reported system key store")
}

// TestSetupProvidersIsNonInteractive pins the behaviour a piped/CI run gets:
// the status table and the copy-paste recipes, never a prompt that would
// hang, and no keys written.
func TestSetupProvidersIsNonInteractive(t *testing.T) {
	e := newEnv(t)
	r := e.run(short, "setup", "providers")
	if r.code != 0 {
		t.Fatalf("setup providers exited %d:\n%s", r.code, r.out())
	}
	requireContains(t, r.out(), "openrouter", "providers table")
	requireContains(t, r.out(), "keys set", "non-interactive recipe")

	entries, err := os.ReadDir(filepath.Join(e.config, "ultra-zen", "keys"))
	if err == nil && len(entries) > 0 {
		t.Errorf("setup providers wrote %d key files in a non-interactive run", len(entries))
	}
}

// TestUsageWithoutAProxySaysSo covers the statusline a user wires into their
// shell: with nothing running it must say so and exit 0, never error out or
// hang, because it runs on every prompt.
func TestUsageWithoutAProxySaysSo(t *testing.T) {
	e := newEnv(t)
	r := e.run(short, "usage")
	if r.code != 0 {
		t.Fatalf("usage exited %d with no proxy running: %s", r.code, r.out())
	}
	requireContains(t, r.out(), "no running ultra-zen proxy", "usage with no proxy")
}

// TestWorkflowHookRewritesAgentTimeouts drives the Claude Code PreToolUse
// hook end to end over stdin/stdout, which is exactly how the client invokes
// it. It must always answer with valid JSON — the client parses stdout and a
// malformed answer breaks every tool call.
func TestWorkflowHookRewritesAgentTimeouts(t *testing.T) {
	e := newEnv(t)

	payload := `{"tool_name":"Workflow","tool_input":{"script":"await agent('do a thing', {label: 'x'})"}}`
	r := e.runStdin(short, payload, "workflow-hook")
	if r.code != 0 {
		t.Fatalf("workflow-hook exited %d: %s", r.code, r.out())
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatalf("workflow-hook stdout is not JSON: %v\n%s", err, r.stdout)
	}

	// Garbage in still has to produce valid JSON out.
	bad := e.runStdin(short, "not json at all", "workflow-hook")
	if bad.code != 0 {
		t.Errorf("workflow-hook exited %d on malformed input, want 0", bad.code)
	}
	if err := json.Unmarshal([]byte(bad.stdout), &out); err != nil {
		t.Fatalf("workflow-hook answered malformed input with non-JSON: %v\n%s", err, bad.stdout)
	}
}

// TestSessionsWithNoHistory covers the empty case of the resume feature: a
// fresh directory has nothing to resume and must say so rather than fail.
func TestSessionsWithNoHistory(t *testing.T) {
	e := newEnv(t)
	r := e.run(short, "sessions")
	if r.code != 0 {
		t.Fatalf("sessions exited %d in a fresh directory: %s", r.code, r.out())
	}
	requireContains(t, strings.ToLower(r.out()), "no recorded", "sessions with no history")
}
