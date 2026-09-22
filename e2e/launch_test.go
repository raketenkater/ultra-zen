package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const launchTimeout = 45 * time.Second

// fakeArgs points the binary at the local fake provider. --codex-url is the
// only provider base URL settable from outside the process, so it is the one
// way to drive catalog fetch and launch without a real gateway or a key.
func fakeArgs(f *fakeProvider, rest ...string) []string {
	return append([]string{"--provider", "codex", "--codex-url", f.base(), "--codex-key", "test-key"}, rest...)
}

// TestListShowsTheProvidersCatalog is the first half of every launch: the
// binary fetches the provider's catalog and reports it.
func TestListShowsTheProvidersCatalog(t *testing.T) {
	e := newEnv(t)
	fp := newFakeProvider(t, "fake-model-a", "fake-model-b")

	r := e.run(launchTimeout, fakeArgs(fp, "--list")...)
	if r.code != 0 {
		t.Fatalf("--list exited %d:\n%s", r.code, r.out())
	}
	for _, id := range fp.Models {
		requireContains(t, r.out(), id, "--list output")
	}
	// The context window must come from the catalog, not a hardcoded guess:
	// it is what the client's autocompaction threshold is derived from.
	requireContains(t, r.out(), "121k", "--list context window")
}

// TestProxyOnlyServesTheChosenModel covers the launch path up to the point a
// client would connect: resolve the model, start the proxy, advertise the
// catalog. --proxy-only is the documented way to run it without Claude Code.
func TestProxyOnlyServesTheChosenModel(t *testing.T) {
	e := newEnv(t)
	fp := newFakeProvider(t, "fake-model-a", "fake-model-b")

	_, buf, stop := e.startBackground(fakeArgs(fp, "fake-model-b", "--proxy-only")...)
	defer stop()

	out := waitFor(t, buf, "proxy ready", launchTimeout)
	requireContains(t, out, "model=fake-model-b", "proxy banner names the chosen model")

	base := proxyBaseFrom(t, out)

	// The proxy must advertise the catalog to the client, because that is how
	// Claude Code's /model picker is populated.
	resp, err := http.Get(base + "/v1/models")
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /v1/models = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var adv struct {
		Data []struct {
			ID       string `json:"id"`
			Disabled bool   `json:"disabled"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &adv); err != nil {
		t.Fatalf("advertised catalog is not JSON: %v\n%s", err, body)
	}

	var listed []string
	selectable := map[string]bool{}
	for _, m := range adv.Data {
		listed = append(listed, m.ID)
		// Every advertised id must carry the "claude" prefix. Claude Code's
		// /model picker filters ids on /(claude|anthropic)/i, so an id
		// without it is invisible to the user no matter how correct the rest
		// of the launch is.
		if !strings.Contains(strings.ToLower(m.ID), "claude") {
			t.Errorf("advertised id %q would be filtered out of the client's picker", m.ID)
		}
		if !m.Disabled {
			selectable[m.ID] = true
		}
	}
	for _, want := range fp.Models {
		found := false
		for id := range selectable {
			if strings.HasSuffix(id, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no selectable advertised id for model %q; catalog was: %v", want, listed)
		}
	}
	// The section header must be advertised as unselectable, or a user can
	// pick a group that routes nowhere.
	header := false
	for _, m := range adv.Data {
		if strings.Contains(m.ID, "claude-group-") {
			header = true
			if !m.Disabled {
				t.Errorf("group header %q is selectable", m.ID)
			}
		}
	}
	if !header {
		t.Errorf("no group header advertised; catalog was: %v", listed)
	}
}

// TestProxyWritesTheStatuslineHandshake closes the loop on `uz usage`: a
// running proxy records where it listens, and the statusline reads that file.
// The two halves live in different processes and only meet here.
func TestProxyWritesTheStatuslineHandshake(t *testing.T) {
	e := newEnv(t)
	fp := newFakeProvider(t)

	_, buf, stop := e.startBackground(fakeArgs(fp, "fake-model-a", "--proxy-only")...)
	defer stop()
	waitFor(t, buf, "proxy ready", launchTimeout)

	path := filepath.Join(e.cache, "ultra-zen", "proxy.json")
	deadline := time.Now().Add(10 * time.Second)
	var body []byte
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			body = b
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(body) == 0 {
		t.Fatalf("a running proxy wrote no %s", path)
	}

	// With that file in place the statusline must produce a line rather than
	// the "no running proxy" fallback.
	r := e.run(short, "usage")
	if r.code != 0 {
		t.Fatalf("usage exited %d against a live proxy: %s", r.code, r.out())
	}
	if strings.Contains(r.out(), "no running ultra-zen proxy") {
		t.Errorf("usage did not find the running proxy recorded at %s:\n%s\nproxy.json: %s",
			path, r.out(), body)
	}
}

// TestLaunchHandsClaudeCodeItsEnvironment is the whole point of the tool, and
// the one path with no coverage at all before this suite: start the proxy,
// then exec the client pointed at it. The fake `claude` records what it was
// given.
//
// A wrong value here does not crash — the client just quietly talks to
// Anthropic instead of the proxy, on a different model, and the user pays for
// it.
func TestLaunchHandsClaudeCodeItsEnvironment(t *testing.T) {
	e := newEnv(t)
	fp := newFakeProvider(t, "fake-model-a")
	record := e.fakeClaude()

	r := e.run(launchTimeout, fakeArgs(fp, "fake-model-a")...)
	if r.code != 0 {
		t.Fatalf("launch exited %d:\n%s", r.code, r.out())
	}

	lines := readFileLines(t, record)
	got := strings.Join(lines, "\n")

	// The client must be pointed at the local proxy, not at Anthropic.
	requireContains(t, got, "ANTHROPIC_BASE_URL=http://127.0.0.1:", "claude env")
	// And told which model to ask for.
	requireContains(t, got, "ANTHROPIC_MODEL=fake-model-a", "claude env")
	// Gateway model discovery is what makes /model list the real catalog.
	requireContains(t, got, "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1", "claude env")

	// A real key must never reach the client: it talks to the proxy, and the
	// proxy holds the credential.
	if strings.Contains(got, "test-key") {
		t.Errorf("the provider key was handed to the client:\n%s", got)
	}
}

// TestLaunchRecordsAResumableSession covers the handoff between a launch and
// `uz sessions`: the launch records what it ran so the next run can offer to
// resume it. The picker's resume rows are built from exactly this.
func TestLaunchRecordsAResumableSession(t *testing.T) {
	e := newEnv(t)
	fp := newFakeProvider(t, "fake-model-a")
	e.fakeClaude()

	if r := e.run(launchTimeout, fakeArgs(fp, "fake-model-a")...); r.code != 0 {
		t.Fatalf("launch exited %d:\n%s", r.code, r.out())
	}

	r := e.run(short, "sessions")
	if r.code != 0 {
		t.Fatalf("sessions exited %d after a launch: %s", r.code, r.out())
	}
	if strings.Contains(strings.ToLower(r.out()), "no recorded") {
		t.Fatalf("a launch left no resumable session:\n%s", r.out())
	}
	requireContains(t, r.out(), "fake-model-a", "recorded session names its model")
}

// TestLaunchRecordsTheModelAsRecentlyUsed covers the other thing a launch
// feeds: the MRU store behind the picker's recently-used section. The entry
// must carry the provider, because a bare id cannot be relaunched.
func TestLaunchRecordsTheModelAsRecentlyUsed(t *testing.T) {
	e := newEnv(t)
	fp := newFakeProvider(t, "fake-model-a")
	e.fakeClaude()

	if r := e.run(launchTimeout, fakeArgs(fp, "fake-model-a")...); r.code != 0 {
		t.Fatalf("launch exited %d:\n%s", r.code, r.out())
	}

	body, err := os.ReadFile(filepath.Join(e.cache, "ultra-zen", "recent-models.json"))
	if err != nil {
		t.Fatalf("launch recorded no recent model: %v", err)
	}
	var entries []struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := json.Unmarshal(body, &entries); err != nil {
		t.Fatalf("recent-models.json is not the provider-aware form: %v\n%s", err, body)
	}
	if len(entries) == 0 || entries[0].Model != "fake-model-a" {
		t.Fatalf("recent store = %s, want fake-model-a first", body)
	}
	if entries[0].Provider == "" {
		t.Errorf("recent entry carries no provider, so it cannot be relaunched: %s", body)
	}
}

// TestUnknownModelIsRefused is the negative half of model resolution. It
// guards the regression that shipped today: a model the launch could not find
// was treated as a dead route and silently replaced by a pool fallback, so a
// user asking for one model got a session on another. Refusing loudly is the
// only acceptable answer for a model that genuinely is not served.
func TestUnknownModelIsRefused(t *testing.T) {
	e := newEnv(t)
	fp := newFakeProvider(t, "fake-model-a")
	record := e.fakeClaude()

	r := e.run(launchTimeout, fakeArgs(fp, "definitely-not-served")...)
	if r.code == 0 {
		t.Fatalf("launching an unserved model exited 0:\n%s", r.out())
	}
	requireContains(t, r.out(), "definitely-not-served", "error names the model")

	// Crucially, it must not have quietly launched something else instead.
	if _, err := os.Stat(record); err == nil {
		got := strings.Join(readFileLines(t, record), "\n")
		t.Fatalf("an unserved model still started the client:\n%s", got)
	}
}

// proxyBaseFrom pulls the listen URL out of the "proxy ready on <url>" banner,
// which is the only place it is published.
func proxyBaseFrom(t *testing.T, out string) string {
	t.Helper()
	const marker = "proxy ready on "
	i := strings.Index(out, marker)
	if i < 0 {
		t.Fatalf("no proxy banner in output:\n%s", out)
	}
	rest := out[i+len(marker):]
	end := strings.IndexAny(rest, " \n\t")
	if end < 0 {
		end = len(rest)
	}
	base := strings.TrimSpace(rest[:end])
	if !strings.HasPrefix(base, "http") {
		t.Fatalf("unparseable proxy base %q in:\n%s", base, out)
	}
	return base
}
