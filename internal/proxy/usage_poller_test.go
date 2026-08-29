package proxy

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raketenkater/ultra-zen/internal/models"
)

// seedORFreeCount writes the local :free tally for the current UTC day into
// the (test-isolated) XDG_CACHE_HOME so cap/exhaustion behavior is
// deterministic.
func seedORFreeCount(t *testing.T, n int64) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	p := filepath.Join(dir, "ultra-zen", "openrouter-free-requests.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{
		"day":   time.Now().UTC().Format("2006-01-02"),
		"count": n,
	})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeOpenRouter serves the documented /key and /credits payloads used by both
// usage paths (poller here, launch banner in internal/tui's parity test).
func fakeOpenRouter(t *testing.T, keyLimit *float64, credits bool, total, used float64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/key":
			data := map[string]any{
				"is_free_tier": false,
				"usage_daily":  0,
			}
			if keyLimit != nil {
				data["limit"] = *keyLimit
				data["limit_remaining"] = *keyLimit
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data})
		case "/credits":
			if !credits {
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`{"error":{"code":403}}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"total_credits": total,
				"total_usage":   used,
			}})
		default:
			http.NotFound(w, r)
		}
	}))
}

// TestFetchOpenRouterUsageCredits pins the poller row built from /key +
// /credits: balance headline, lifetime sets the daily :free cap, tally from
// the local counter. Golden values match internal/tui's parity test exactly —
// same fake payloads must produce the same canonical row on both paths.
func TestFetchOpenRouterUsageCredits(t *testing.T) {
	seedORFreeCount(t, 7)
	lim := 1.0
	srv := fakeOpenRouter(t, &lim, true, 20.0, 0.01)
	defer srv.Close()
	s := New(Config{})
	s.fetchOpenRouterUsageAt(srv.URL, srv.Client(), "sk-or-test")
	row := s.usage.getRowSnapshot("openrouter")
	if row == nil {
		t.Fatal("no row stored")
	}
	if row.Credits == nil || math.Abs(*row.Credits-19.99) > 1e-9 {
		t.Fatalf("Credits = %v, want 19.99", row.Credits)
	}
	if row.Limit == nil || *row.Limit != 20.0 {
		t.Fatalf("Limit = %v, want lifetime 20", row.Limit)
	}
	if row.FreeReqsUsed == nil || *row.FreeReqsUsed != 7 {
		t.Fatalf("FreeReqsUsed = %v, want 7", row.FreeReqsUsed)
	}
	if row.FreeReqsLimit == nil || *row.FreeReqsLimit != 1000 {
		t.Fatalf("FreeReqsLimit = %v, want 1000 (>= $10 lifetime)", row.FreeReqsLimit)
	}
	if row.Exhausted {
		t.Fatal("must not be exhausted at 7/1000")
	}
}

// TestFetchOpenRouterUsageAtCap pins the exhaustion flip shared with the
// launch banner: once the local tally reaches the documented daily cap the
// row reports exhausted (the statusline then shows "[openrouter hit]").
func TestFetchOpenRouterUsageAtCap(t *testing.T) {
	seedORFreeCount(t, 50)
	srv := fakeOpenRouter(t, nil, true, 5.0, 1.0) // under $10 → 50/day cap
	defer srv.Close()
	s := New(Config{})
	s.fetchOpenRouterUsageAt(srv.URL, srv.Client(), "sk-or-test")
	row := s.usage.getRowSnapshot("openrouter")
	if row == nil || !row.Exhausted {
		t.Fatalf("row = %+v, want Exhausted at 50/50", row)
	}
	if row.FreeReqsLimit == nil || *row.FreeReqsLimit != 50 {
		t.Fatalf("FreeReqsLimit = %v, want 50 (< $10 lifetime)", row.FreeReqsLimit)
	}
}

// TestFetchOpenRouterUsageCreditsSkip verifies the /credits 403 skip path:
// the row keeps only /key data, no Credits headline — matching what the
// launch banner falls back to with the same fake.
func TestFetchOpenRouterUsageCreditsSkip(t *testing.T) {
	seedORFreeCount(t, 3)
	lim := 0.5
	srv := fakeOpenRouter(t, &lim, false, 0, 0)
	defer srv.Close()
	s := New(Config{})
	s.fetchOpenRouterUsageAt(srv.URL, srv.Client(), "sk-or-test")
	row := s.usage.getRowSnapshot("openrouter")
	if row == nil {
		t.Fatal("no row stored")
	}
	if row.Credits != nil || row.FreeReqsLimit != nil {
		t.Fatalf("row leaked credit data on 403: %+v", row)
	}
	if row.Remaining == nil || *row.Remaining != 0.5 {
		t.Fatalf("Remaining = %v, want /key 0.5", row.Remaining)
	}
}

// TestHandleMessagesCountsOnly200Free pins the counter gating: an openrouter
// :free 401 is not a served request (OpenRouter meters nothing), a :free 200
// is counted once, a paid openrouter 200 is not counted, and a second :free
// 200 increments again.
func TestHandleMessagesCountsOnly200Free(t *testing.T) {
	seedORFreeCount(t, 0)
	ok := `{"id":"r","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
	var reject bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reject {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"bad key"}}`))
			return
		}
		w.Write([]byte(ok))
	}))
	defer up.Close()
	post := func(model string) {
		s := New(Config{
			Provider: "openrouter",
			BaseURL:  up.URL,
			APIKey:   "sk-or-test",
			Model:    model,
			Port:     0,
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		body := `{"model":"` + model + `","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		rec := httptest.NewRecorder()
		s.handleMessages(rec, req)
	}
	reject = true
	post("vendor/a:free")
	if got := models.ORFreeRequests(); got != 0 {
		t.Fatalf("401 counted: %d, want 0", got)
	}
	reject = false
	post("vendor/a:free")
	if got := models.ORFreeRequests(); got != 1 {
		t.Fatalf("first 200 tally = %d, want 1", got)
	}
	post("vendor/paid")
	if got := models.ORFreeRequests(); got != 1 {
		t.Fatalf("paid route tally = %d, want 1 (paid is uncapped)", got)
	}
	post("openrouter/free")
	if got := models.ORFreeRequests(); got != 2 {
		t.Fatalf("router-variant tally = %d, want 2 (openrouter/free is metered too)", got)
	}
}
