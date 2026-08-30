package models

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestParseCodexModels(t *testing.T) {
	body := `{"models":[
		{"slug":"gpt-5.6-sol","display_name":"GPT-5.6-Sol","context_window":272000,"visibility":"list","supported_in_api":true},
		{"slug":"gpt-5.5","display_name":"GPT-5.5","context_window":400000,"visibility":"list","supported_in_api":true},
		{"slug":"gpt-5.6-sol-wm","display_name":"GPT-5.6-Sol WM","visibility":"hide","supported_in_api":true},
		{"slug":"codex-auto-review","display_name":"Codex Auto Review","visibility":"hide","supported_in_api":true},
		{"slug":"gpt-5.4","display_name":"GPT-5.4","visibility":"list","supported_in_api":false},
		{"slug":"","display_name":"Empty","visibility":"list","supported_in_api":true}
	]}`
	list, err := parseCodexModels([]byte(body), CodexSubBase)
	if err != nil {
		t.Fatalf("parseCodexModels: %v", err)
	}
	// Only gpt-5.6-sol and gpt-5.5 survive: gpt-5.4 is not API-served, the
	// hidden ones are skipped, and the empty slug is dropped.
	if len(list) != 2 {
		t.Fatalf("parseCodexModels returned %d models, want 2: %+v", len(list), list)
	}
	if list[0].ID != "gpt-5.5" || list[1].ID != "gpt-5.6-sol" {
		t.Fatalf("unexpected sort order: %+v", list)
	}
	if list[1].ContextLength != 272000 || list[1].Base != CodexSubBase {
		t.Fatalf("gpt-5.6-sol metadata wrong: %+v", list[1])
	}
	if list[1].Free {
		t.Fatalf("codex-sub model marked Free")
	}
}

func TestListCodexSubLive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok123" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("ChatGPT-Account-ID") != "acct456" {
			t.Errorf("ChatGPT-Account-ID = %q", r.Header.Get("ChatGPT-Account-ID"))
		}
		if r.URL.Query().Get("client_version") != "0.147.0" {
			t.Errorf("client_version = %q", r.URL.Query().Get("client_version"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"slug": "gpt-5.6-sol", "display_name": "GPT-5.6-Sol", "context_window": 272000, "visibility": "list", "supported_in_api": true},
			},
		})
	}))
	defer srv.Close()

	list, err := ListCodexSub(&http.Client{}, srv.URL, "tok123", "acct456", "0.147.0")
	if err != nil {
		t.Fatalf("ListCodexSub: %v", err)
	}
	if len(list) != 1 || list[0].ID != "gpt-5.6-sol" {
		t.Fatalf("ListCodexSub = %+v", list)
	}
}

func TestListCodexSubHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := ListCodexSub(&http.Client{}, srv.URL, "tok", "acct", "0.147.0")
	if err == nil {
		t.Fatalf("ListCodexSub with 401 succeeded")
	}
}

func TestListCodexModelsFromCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	cache := map[string]any{
		"fetched_at":     "2026-08-09T13:42:59Z",
		"client_version": "0.147.0",
		"models": []map[string]any{
			{"slug": "gpt-5.6-sol", "display_name": "GPT-5.6-Sol", "context_window": 272000, "visibility": "list", "supported_in_api": true},
			{"slug": "gpt-5.4-mini", "display_name": "GPT-5.4 mini", "visibility": "list", "supported_in_api": true},
		},
	}
	b, _ := json.Marshal(cache)
	if err := os.WriteFile(filepath.Join(home, "models_cache.json"), b, 0644); err != nil {
		t.Fatal(err)
	}
	list, err := ListCodexModelsFromCache(CodexSubBase)
	if err != nil {
		t.Fatalf("ListCodexModelsFromCache: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("cache list = %+v", list)
	}
}

func TestListCodexModelsFromCacheMissing(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	if _, err := ListCodexModelsFromCache(CodexSubBase); err == nil {
		t.Fatalf("ListCodexModelsFromCache with no cache succeeded")
	}
}
