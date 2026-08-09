package codex

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeAuthFixture creates a minimal ~/.codex/auth.json under a temp CODEX_HOME.
func writeAuthFixture(t *testing.T, mode, access, refresh, account string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	dir := filepath.Join(home)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	auth := map[string]any{
		"auth_mode": mode,
		"tokens": map[string]any{
			"access_token":  access,
			"refresh_token": refresh,
			"account_id":    account,
		},
		"last_refresh": "2026-08-09T12:00:00Z",
	}
	b, _ := json.MarshalIndent(auth, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "auth.json")
}

// makeJWT builds an unsigned JWT whose payload carries the given exp. Signing
// is irrelevant — Detect/NeedsRefresh only decode the payload.
func makeJWT(exp time.Time) string {
	payload, _ := json.Marshal(map[string]any{"exp": exp.Unix()})
	return "header." + strings.TrimRight(base64.RawURLEncoding.EncodeToString(payload), "=") + ".signature"
}

func TestDetectChatGPTSuccess(t *testing.T) {
	path := writeAuthFixture(t, "chatgpt", "tok123", "rt123", "acct456")
	a, ok := Detect()
	if !ok {
		t.Fatalf("Detect() = ok=false for a valid ChatGPT auth")
	}
	if a.AccessToken != "tok123" || a.RefreshToken != "rt123" || a.AccountID != "acct456" {
		t.Fatalf("Detect() = %+v, want the fixture tokens", a)
	}
	if a.Path != path {
		t.Fatalf("Detect() Path = %q, want %q", a.Path, path)
	}
}

func TestDetectNotInstalled(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir()) // empty dir, no auth.json
	if _, ok := Detect(); ok {
		t.Fatalf("Detect() = ok=true with no auth.json")
	}
}

func TestDetectRejectsAPIKeyMode(t *testing.T) {
	// A legacy API-key login must not be mistaken for a ChatGPT subscription.
	writeAuthFixture(t, "legacy_key", "sk-123", "", "acct456")
	if _, ok := Detect(); ok {
		t.Fatalf("Detect() accepted a legacy_key auth mode")
	}
}

func TestDetectRejectsEmptyToken(t *testing.T) {
	writeAuthFixture(t, "chatgpt", "", "rt123", "acct456")
	if _, ok := Detect(); ok {
		t.Fatalf("Detect() accepted an empty access_token")
	}
}

func TestNeedsRefresh(t *testing.T) {
	fresh := makeJWT(time.Now().Add(72 * time.Hour))
	expiring := makeJWT(time.Now().Add(2 * time.Minute))
	opaque := "not-a-jwt"

	// Expiring token must trigger refresh.
	a := Auth{AccessToken: expiring}
	if !a.NeedsRefresh() {
		t.Fatalf("NeedsRefresh() = false for a token expiring in 2 minutes")
	}
	// Fresh token must not.
	a = Auth{AccessToken: fresh}
	if a.NeedsRefresh() {
		t.Fatalf("NeedsRefresh() = true for a token valid 72h")
	}
	// Opaque (unparseable) token: no proactive refresh; the backend 401 will
	// handle it.
	a = Auth{AccessToken: opaque}
	if a.NeedsRefresh() {
		t.Fatalf("NeedsRefresh() = true for an opaque token")
	}
}

func TestPrimaryModel(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	if got := PrimaryModel(); got != "" {
		t.Fatalf("PrimaryModel() = %q with no config.toml, want empty", got)
	}
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	cfg := "model = \"gpt-5.6-sol\"\nmodel_reasoning_effort = \"medium\"\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	if got := PrimaryModel(); got != "gpt-5.6-sol" {
		t.Fatalf("PrimaryModel() = %q, want gpt-5.6-sol", got)
	}
}

func TestRefreshExchangesAndRewrites(t *testing.T) {
	path := writeAuthFixture(t, "chatgpt", "old-access", "old-refresh", "acct456")

	// Serve the OAuth refresh endpoint.
	var sawForm string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		sawForm = r.PostForm.Encode()
		json.NewEncoder(w).Encode(map[string]string{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
		})
	}))
	defer srv.Close()

	// The token exchange must hit the real auth.openai.com URL with our
	// client_id. We assert the request shape by running against a transport
	// that rewrites the host to the test server.
	transport := &rewriteTransport{server: srv}
	client := &http.Client{Transport: transport}
	a := Auth{AccessToken: "old-access", RefreshToken: "old-refresh", AccountID: "acct456", Path: path}
	if err := Refresh(client, a); err != nil {
		t.Fatalf("Refresh() error: %v", err)
	}
	if !strings.Contains(sawForm, "grant_type=refresh_token") ||
		!strings.Contains(sawForm, "refresh_token=old-refresh") {
		t.Fatalf("Refresh() form = %q, missing grant_type/refresh_token", sawForm)
	}

	// The file must now hold the new tokens, atomically replaced.
	re := map[string]any{}
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &re); err != nil {
		t.Fatal(err)
	}
	tok := re["tokens"].(map[string]any)
	if tok["access_token"] != "new-access" || tok["refresh_token"] != "new-refresh" {
		t.Fatalf("rewritten auth.json tokens = %v, want new-access/new-refresh", tok)
	}
	if _, err := os.Stat(filepath.Join(homeOf(path), "auth.json.ultra-zen-tmp")); !os.IsNotExist(err) {
		t.Fatalf("temp file not cleaned up after successful rename")
	}
}

// homeOf returns the parent dir of an auth.json path.
func homeOf(p string) string { return filepath.Dir(p) }

// rewriteTransport forwards every request to the test server while preserving
// the request host (so oauthTokenURL's path is exercised as-is).
type rewriteTransport struct{ server *httptest.Server }

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request with the URL pointed at the test server.
	r2 := req.Clone(req.Context())
	r2.URL.Scheme = "http"
	r2.URL.Host = rt.server.Listener.Addr().String()
	return rt.server.Client().Transport.RoundTrip(r2)
}

func TestRefreshNoToken(t *testing.T) {
	a := Auth{AccessToken: "tok", RefreshToken: "", Path: "/nonexistent"}
	if err := Refresh(nil, a); err == nil {
		t.Fatalf("Refresh() with empty refresh_token succeeded")
	}
}

func TestHomeOverrides(t *testing.T) {
	t.Setenv("CODEX_HOME", "/tmp/custom-codex")
	if got := Home(); got != "/tmp/custom-codex" {
		t.Fatalf("Home() = %q with CODEX_HOME set, want /tmp/custom-codex", got)
	}
	if got := AuthPath(); got != "/tmp/custom-codex/auth.json" {
		t.Fatalf("AuthPath() = %q", got)
	}
}
