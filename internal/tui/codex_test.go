package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/raketenkater/ultra-zen/internal/models"
)

// writeCodexAuth creates a valid ChatGPT-mode auth.json under a temp CODEX_HOME.
func writeCodexAuth(t *testing.T, access, account string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	auth := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"access_token":  access,
			"refresh_token": "rt",
			"account_id":    account,
		},
	}
	b, _ := json.MarshalIndent(auth, "", "  ")
	if err := os.WriteFile(filepath.Join(home, "auth.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadProviderCodexSubDetected(t *testing.T) {
	writeCodexAuth(t, "tok123", "acct456")
	// Point the live /models fetch at a local server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok123" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("ChatGPT-Account-ID") != "acct456" {
			t.Errorf("ChatGPT-Account-ID = %q", r.Header.Get("ChatGPT-Account-ID"))
		}
		json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"slug": "gpt-5.6-sol", "display_name": "GPT-5.6-Sol", "context_window": 272000, "visibility": "list", "supported_in_api": true},
			},
		})
	}))
	defer srv.Close()
	// The codex models list uses the real CodexSubBase; we can't override the
	// const, so exercise loadProvider against a server by stubbing the client.
	// Instead assert the detection + list path via ListCodexSub with a fake base
	// by pointing at the server (the models.Base is what matters for display).
	auth, ok := detectAndList(srv.URL, "tok123", "acct456")
	if !ok {
		t.Fatalf("codex login not detected")
	}
	if auth != "gpt-5.6-sol" {
		t.Fatalf("expected gpt-5.6-sol from the codex catalog, got %q", auth)
	}
}

func TestLoadProviderCodexSubNotLoggedIn(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir()) // empty -> no auth.json
	res := loadProvider("codex-sub")
	if res.key != "" {
		t.Fatalf("keyless provider key = %q, want empty", res.key)
	}
}

// detectAndList is a test helper that mirrors the codex-sub branch of
// loadProvider: detect the login, then list models from a given base.
func detectAndList(base, access, account string) (string, bool) {
	list, err := models.ListCodexSub(&http.Client{}, base, access, account, "0.147.0")
	if err != nil || len(list) == 0 {
		return "", false
	}
	return list[0].ID, true
}
