package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raketenkater/ultra-zen/internal/session"
)

// postMessages drives one minimal non-streaming turn through the proxy and
// returns the upstream record of x-opencode-session for it.
func postMessages(t *testing.T, srv *Server) {
	t.Helper()
	resp, err := http.Post(srv.BaseURL()+"/v1/messages", "application/json",
		strings.NewReader(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func startTestProxy(t *testing.T, cfg Config) *Server {
	t.Helper()
	srv := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return srv
}

// TestZenSessionHeaderSendsConfiguredID pins the fix for Zen's 400
// MissingSessionID: completion requests routed to an opencode Zen gateway
// must carry x-opencode-session, forwarded verbatim from Config and
// identical across a conversation's requests so Zen can keep the routing
// (and its prompt cache) stable.
func TestZenSessionHeaderSendsConfiguredID(t *testing.T) {
	const want = "11111111-2222-4333-8444-555555555555"
	var got []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("x-opencode-session"))
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("Authorization = %q, want Bearer k", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer up.Close()

	srv := startTestProxy(t, Config{Provider: "opencode-go", BaseURL: up.URL, APIKey: "k", Model: "m", SessionID: want})
	postMessages(t, srv)
	postMessages(t, srv)

	if len(got) != 2 || got[0] != want || got[1] != want {
		t.Fatalf("x-opencode-session = %q, want %q on both requests", got, want)
	}
}

// TestZenSessionHeaderMintedWhenUnset verifies the default: with no
// configured id, the proxy mints a valid UUID at construction and reuses it
// for every request of the instance (one instance = one conversation).
func TestZenSessionHeaderMintedWhenUnset(t *testing.T) {
	var got []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("x-opencode-session"))
		w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer up.Close()

	srv := startTestProxy(t, Config{Provider: "opencode-go", BaseURL: up.URL, APIKey: "k", Model: "m"})
	postMessages(t, srv)
	postMessages(t, srv)

	if len(got) != 2 {
		t.Fatalf("upstream calls = %d, want 2", len(got))
	}
	if !session.ValidSessionID(got[0]) {
		t.Fatalf("minted session id %q is not a UUID", got[0])
	}
	if got[0] != got[1] {
		t.Fatalf("session id not stable across requests: %q then %q", got[0], got[1])
	}
}

// TestZenSessionHeaderOnlyForZenRoutes ensures non-Zen gateways (OpenRouter
// and friends) never see the header — it is Zen-specific routing metadata.
func TestZenSessionHeaderOnlyForZenRoutes(t *testing.T) {
	var seen string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("x-opencode-session")
		w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer up.Close()

	srv := startTestProxy(t, Config{Provider: "openrouter", BaseURL: up.URL, APIKey: "k", Model: "m"})
	postMessages(t, srv)

	if seen != "" {
		t.Fatalf("x-opencode-session = %q sent to a non-Zen gateway, want none", seen)
	}
}
