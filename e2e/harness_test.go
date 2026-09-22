// Package e2e drives the built ultra-zen binary as a subprocess, the way a
// user does: install it, set a key, list models, launch one, resume it.
//
// Every other test in this repo calls Go functions. That leaves the seams
// between them untested, and the seams are where the bugs have been: a model
// the picker offered but the launch could not resolve, a cursor that could
// not climb back to a section, a catalog narrowed for display and then reused
// as though it were complete. None of those are visible from inside a single
// package, and CI never ran the binary at all — it built it and threw it
// away.
//
// So these tests own no internals. They exec the binary, read its stdout and
// stderr, and look at the files it wrote. Everything it would reach over the
// network is a local fake, and everything it would write outside the project
// is redirected into a temp dir, so the suite is hermetic: no API keys, no
// network, no root, nothing left behind.
package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// uzBin is the binary under test, built once by TestMain.
var uzBin string

// repoRoot is the module root, resolved once so tests can reach install.sh.
var repoRoot string

func TestMain(m *testing.M) {
	// testing registers its flags in the generated main but does not parse
	// them until m.Run, and testing.Short panics on an unparsed flag set.
	flag.Parse()
	if testing.Short() {
		// -short skips the build; each test skips itself via newEnv, so the
		// run reports them as skipped rather than passing vacuously.
		os.Exit(m.Run())
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: getwd:", err)
		os.Exit(1)
	}
	repoRoot = filepath.Dir(wd)

	// Build into its own directory. The binary creates a `uz` symlink next to
	// itself on every launch, so it must not sit in the repo root or in a
	// directory shared with anything that matters.
	dir, err := os.MkdirTemp("", "uz-e2e-bin-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: tempdir:", err)
		os.Exit(1)
	}
	uzBin = filepath.Join(dir, "ultra-zen")
	build := exec.Command("go", "build", "-o", uzBin, "./cmd/ultra-zen")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build failed: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// env is a hermetic environment for one test: a private HOME, private config
// and cache dirs, a private system key store, and a PATH that resolves
// `claude` to a recorder script instead of the real client.
type env struct {
	t         *testing.T
	home      string // fake HOME
	config    string // XDG_CONFIG_HOME
	cache     string // XDG_CACHE_HOME
	sysKeys   string // ULTRA_ZEN_SYSTEM_KEYS
	claudeCfg string // CLAUDE_CONFIG_DIR
	bin       string // a PATH dir this test owns (fake claude, shims)
	vars      map[string]string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	if testing.Short() {
		// TestMain skips the build under -short, so there is no binary to
		// drive. Skipping here rather than in TestMain keeps `go test -short`
		// honest: it reports these as skipped instead of silently passing.
		t.Skip("e2e drives the built binary; skipped under -short")
	}
	root := t.TempDir()
	e := &env{
		t:         t,
		home:      filepath.Join(root, "home"),
		config:    filepath.Join(root, "config"),
		cache:     filepath.Join(root, "cache"),
		sysKeys:   filepath.Join(root, "etc-keys"),
		claudeCfg: filepath.Join(root, "claude"),
		bin:       filepath.Join(root, "bin"),
		vars:      map[string]string{},
	}
	for _, d := range []string{e.home, e.config, e.cache, e.sysKeys, e.claudeCfg, e.bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

// set adds or overrides one variable for this environment.
func (e *env) set(k, v string) *env { e.vars[k] = v; return e }

// environ renders the process environment. Provider key variables are blanked
// explicitly: a developer with OPENROUTER_API_KEY exported must not get a
// different result from CI, and must never have a real key reach a fake
// server.
func (e *env) environ() []string {
	out := []string{
		"HOME=" + e.home,
		"XDG_CONFIG_HOME=" + e.config,
		"XDG_CACHE_HOME=" + e.cache,
		"ULTRA_ZEN_SYSTEM_KEYS=" + e.sysKeys,
		"CLAUDE_CONFIG_DIR=" + e.claudeCfg,
		"PATH=" + e.bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		// TERM=dumb pins the ASCII glyph set so assertions on output do not
		// depend on the terminal the suite happens to run under.
		"TERM=dumb",
		"NO_COLOR=1",
	}
	for _, k := range []string{
		"OPENROUTER_API_KEY", "MODELSCOPE_API_KEY", "GROQ_API_KEY", "CEREBRAS_API_KEY",
		"HF_TOKEN", "COHERE_API_KEY", "SAIA_API_KEY", "CODEX_API_KEY", "CODEX_BASE_URL",
		"CODEX_HOME",
	} {
		out = append(out, k+"=")
	}
	for k, v := range e.vars {
		out = append(out, k+"="+v)
	}
	return out
}

// result is one finished subprocess run.
type result struct {
	stdout, stderr string
	code           int
}

// out is stdout and stderr together, for assertions that do not care which
// stream carried the line.
func (r result) out() string { return r.stdout + r.stderr }

// run executes the binary with args and waits for it to finish. A run that
// outlives the timeout is killed and fails the test: a hung launch is a bug,
// not a slow one.
func (e *env) run(timeout time.Duration, args ...string) result {
	e.t.Helper()
	return e.runCmd(timeout, uzBin, args...)
}

// runCmd is run for an arbitrary executable (install.sh, a stub binary).
func (e *env) runCmd(timeout time.Duration, name string, args ...string) result {
	e.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = e.environ()
	cmd.Dir = e.home
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// Stdin must never be a terminal: the binary gates its sudo prompt and
	// its interactive key prompts on a char device, and a test that tripped
	// those would hang.
	cmd.Stdin = strings.NewReader("")
	if err := cmd.Start(); err != nil {
		e.t.Fatalf("start %s: %v", name, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		code := 0
		var ee *exec.ExitError
		if err != nil {
			if !asExitError(err, &ee) {
				e.t.Fatalf("run %s: %v", name, err)
			}
			code = ee.ExitCode()
		}
		return result{stdout: stdout.String(), stderr: stderr.String(), code: code}
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		e.t.Fatalf("%s %v did not finish within %s\nstdout:\n%s\nstderr:\n%s",
			name, args, timeout, stdout.String(), stderr.String())
		return result{}
	}
}

// runStdin is run with something on stdin, for `keys set <p> -` and the
// workflow hook.
func (e *env) runStdin(timeout time.Duration, stdin string, args ...string) result {
	e.t.Helper()
	cmd := exec.Command(uzBin, args...)
	cmd.Env = e.environ()
	cmd.Dir = e.home
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		e.t.Fatalf("start: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		code := 0
		var ee *exec.ExitError
		if err != nil && asExitError(err, &ee) {
			code = ee.ExitCode()
		}
		return result{stdout: stdout.String(), stderr: stderr.String(), code: code}
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		e.t.Fatalf("stdin run %v timed out", args)
		return result{}
	}
}

func asExitError(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*target = ee
	}
	return ok
}

// startBackground launches the binary and returns it plus a func that stops
// it. Used for --proxy-only, which blocks by design.
func (e *env) startBackground(args ...string) (*exec.Cmd, *syncBuffer, func()) {
	e.t.Helper()
	cmd := exec.Command(uzBin, args...)
	cmd.Env = e.environ()
	cmd.Dir = e.home
	buf := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = buf, buf
	cmd.Stdin = strings.NewReader("")
	if err := cmd.Start(); err != nil {
		e.t.Fatalf("start background: %v", err)
	}
	stop := func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	return cmd, buf, stop
}

// fakeClaude installs a `claude` on PATH that records its argv and the
// ANTHROPIC_*/CLAUDE_* environment it was handed, then exits. It is how the
// launch path is observed without running the real client.
func (e *env) fakeClaude() string {
	e.t.Helper()
	outFile := filepath.Join(e.bin, "claude-invocation.txt")
	script := "#!/bin/sh\n" +
		"{\n" +
		"  echo \"ARGV\"\n" +
		"  for a in \"$@\"; do echo \"  $a\"; done\n" +
		"  echo \"ENV\"\n" +
		"  env | grep -E '^(ANTHROPIC_|CLAUDE_)' | sort | sed 's/^/  /'\n" +
		"} > " + shellQuote(outFile) + "\n" +
		"exit 0\n"
	path := filepath.Join(e.bin, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		e.t.Fatal(err)
	}
	return outFile
}

// shellQuote wraps s for safe use inside a generated /bin/sh script.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// fakeProvider serves an OpenAI-shaped model catalog. The binary is pointed
// at it with --codex-url, which is the one provider base URL settable from
// outside the process, and therefore the only way to exercise catalog fetch
// and launch without touching a real gateway.
type fakeProvider struct {
	*httptest.Server
	Models []string
}

func newFakeProvider(t *testing.T, models ...string) *fakeProvider {
	t.Helper()
	if len(models) == 0 {
		models = []string{"fake-model-a", "fake-model-b"}
	}
	fp := &fakeProvider{Models: models}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		type entry struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
		}
		payload := struct {
			Data []entry `json:"data"`
		}{}
		for _, id := range fp.Models {
			payload.Data = append(payload.Data, entry{ID: id, ContextLength: 123904})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	})
	fp.Server = httptest.NewServer(mux)
	t.Cleanup(fp.Close)
	return fp
}

// base is the value to pass to --codex-url.
func (f *fakeProvider) base() string { return f.URL + "/v1" }

// waitFor polls until the buffer contains want, and fails with everything
// captured so far if it never does. Polling beats a fixed sleep: the launch
// path does real work (catalog fetch, proxy start, health check) whose timing
// varies with the machine.
func waitFor(t *testing.T, buf *syncBuffer, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s := buf.snapshot(); strings.Contains(s, want) {
			return s
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("never saw %q within %s; output was:\n%s", want, timeout, buf.snapshot())
	return ""
}

// readFileLines is a small helper for asserting on recorder output.
func readFileLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

// requireContains fails with the full output rather than a bare boolean, so a
// failing e2e test says what the binary actually printed.
func requireContains(t *testing.T, got, want, what string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%s: missing %q\n--- actual output ---\n%s", what, want, got)
	}
}

// syncBuffer is a bytes.Buffer safe for the reader goroutine to poll while
// the subprocess writes into it. exec wires the child's stdout to this from
// its own goroutine, so waitFor would otherwise race the writer.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

// snapshot copies what has been written so far under the lock.
func (s *syncBuffer) snapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
