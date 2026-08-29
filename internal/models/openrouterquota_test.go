package models

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOpenRouterFreeDailyCap(t *testing.T) {
	if got := OpenRouterFreeDailyCap(0); got != 50 {
		t.Fatalf("cap($0) = %d, want 50", got)
	}
	if got := OpenRouterFreeDailyCap(9.99); got != 50 {
		t.Fatalf("cap($9.99) = %d, want 50", got)
	}
	if got := OpenRouterFreeDailyCap(10); got != 1000 {
		t.Fatalf("cap($10) = %d, want 1000", got)
	}
	if got := OpenRouterFreeDailyCap(20); got != 1000 {
		t.Fatalf("cap($20) = %d, want 1000", got)
	}
}

func TestORFreeRequestCounter(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if got := ORFreeRequests(); got != 0 {
		t.Fatalf("initial = %d, want 0", got)
	}
	RecordORFreeRequest()
	RecordORFreeRequest()
	if got := ORFreeRequests(); got != 2 {
		t.Fatalf("after 2 records = %d, want 2", got)
	}
}

func TestORFreeRequestCounterRollsOver(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	// Write a record for yesterday: today's read must be zero, and the next
	// increment must restart from 1.
	p := filepath.Join(dir, "ultra-zen", "openrouter-free-requests.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	b, _ := json.Marshal(orQuotaRecord{Day: yesterday, Count: 99})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ORFreeRequests(); got != 0 {
		t.Fatalf("stale-day read = %d, want 0", got)
	}
	RecordORFreeRequest()
	if got := ORFreeRequests(); got != 1 {
		t.Fatalf("after rollover increment = %d, want 1", got)
	}
}

func TestORFreeRequestCounterCorruptFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	p := filepath.Join(dir, "ultra-zen", "openrouter-free-requests.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ORFreeRequests(); got != 0 {
		t.Fatalf("corrupt file = %d, want 0", got)
	}
	RecordORFreeRequest()
	if got := ORFreeRequests(); got != 1 {
		t.Fatalf("after corrupt reset = %d, want 1", got)
	}
}

// TestORFreeRequestConcurrentNoLostUpdates hammers the read-modify-write from
// many goroutines: without the mutex+flock the rename-on-rename race would
// intermittently under-report.
func TestORFreeRequestConcurrentNoLostUpdates(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for k := 0; k < n; k++ {
		go func() { defer wg.Done(); RecordORFreeRequest() }()
	}
	wg.Wait()
	if got := ORFreeRequests(); got != n {
		t.Fatalf("concurrent tally = %d, want %d", got, n)
	}
}

func TestOpenRouterFreeModel(t *testing.T) {
	free := []string{"vendor/model:free", "openrouter/free", "z:free"}
	paid := []string{"vendor/model", "vendor/model-free", "openrouter/auto", ""}
	for _, id := range free {
		if !OpenRouterFreeModel(id) {
			t.Errorf("OpenRouterFreeModel(%q) = false, want true", id)
		}
	}
	for _, id := range paid {
		if OpenRouterFreeModel(id) {
			t.Errorf("OpenRouterFreeModel(%q) = true, want false", id)
		}
	}
}

// TestFetchOpenRouterCredits pins the /credits parsing (200 ok=true, non-200
// skip, garbage body skip) against the documented {data:{total_credits,
// total_usage}} shape.
func TestFetchOpenRouterCredits(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/credits" {
				http.NotFound(w, r)
				return
			}
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				t.Errorf("missing bearer header")
			}
			w.Write([]byte(`{"data":{"total_credits":100.5,"total_usage":25.75}}`))
		}))
		defer srv.Close()
		total, used, ok := FetchOpenRouterCredits(srv.Client(), srv.URL, "sk-or-x")
		if !ok || total != 100.5 || used != 25.75 {
			t.Fatalf("got (%v, %v, %v), want (100.5, 25.75, true)", total, used, ok)
		}
	})
	t.Run("forbidden-is-skip", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":{"code":403,"message":"Only management keys can perform this operation"}}`))
		}))
		defer srv.Close()
		if _, _, ok := FetchOpenRouterCredits(srv.Client(), srv.URL, "sk-or-x"); ok {
			t.Fatal("403 must read as ok=false")
		}
	})
	t.Run("garbage-is-skip", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("<html>cloudflare</html>"))
		}))
		defer srv.Close()
		if _, _, ok := FetchOpenRouterCredits(srv.Client(), srv.URL, "sk-or-x"); ok {
			t.Fatal("unparseable 200 must read as ok=false")
		}
	})
}
