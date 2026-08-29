// Package models: OpenRouter free-tier request quota. OpenRouter meters free
// (:free) model requests with a per-UTC-day cap tied to lifetime credit
// purchases — 50/day under $10 bought, 1000/day at $10+. There is no readable
// "requests left today" endpoint for ordinary keys (/activity needs a
// management key and 403s otherwise), so ultra-zen keeps its own counter:
// every proxied :free request to openrouter increments a persisted daily
// tally (UTC day, matching OpenRouter's reset). The count is a floor —
// requests made through other apps or outside ultra-zen are invisible — but
// between ultra-zen sessions it tracks the real quota. Concurrent ultra-zen
// sessions share this one file, so the read-modify-write runs under an
// in-process mutex plus flock (see internal/flock): the tally is then exact
// across sessions, though still a floor overall. Cache-file style follows
// recent.go: best-effort, corrupt file means zero, never a launch blocker.
package models

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/raketenkater/ultra-zen/internal/flock"
)

func orQuotaPath() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".cache")
	}
	return filepath.Join(base, "ultra-zen", "openrouter-free-requests.json")
}

// orQuotaRecord is one UTC day's counted free-tier requests. The day string
// makes rollover implicit: a stale file simply reads as zero for today.
type orQuotaRecord struct {
	Day   string `json:"day"`
	Count int64  `json:"count"`
}

func todayUTC() string { return time.Now().UTC().Format("2006-01-02") }

// OpenRouterFreeModel reports whether an openrouter model id is metered
// against the :free daily request cap: the ":free" suffix variant or the
// openrouter/free auto-router. Matches the Free flag used for the picker
// (models.go), so the counter covers exactly the models the UI marks free.
func OpenRouterFreeModel(id string) bool {
	return id == "openrouter/free" || strings.HasSuffix(id, ":free")
}

// OpenRouterFreeDailyCap is OpenRouter's documented :free request cap per UTC
// day for an account with lifetimeCredits bought. $10+ lifts 50 to 1000.
func OpenRouterFreeDailyCap(lifetimeCredits float64) int64 {
	if lifetimeCredits >= 10 {
		return 1000
	}
	return 50
}

// orQuotaMu serializes the read-modify-write within this process; flock
// serializes it across concurrent ultra-zen sessions that share the cache
// file. Together they make the tally exact for :free requests routed through
// ultra-zen; it remains a floor for the account overall.
var orQuotaMu sync.Mutex

// ORFreeRequests returns today's counted free-tier openrouter requests, or 0
// if nothing is recorded for the current UTC day.
func ORFreeRequests() int64 {
	orQuotaMu.Lock()
	defer orQuotaMu.Unlock()
	return readORFreeCountLocked()
}

func readORFreeCountLocked() int64 {
	b, err := os.ReadFile(orQuotaPath())
	if err != nil {
		return 0
	}
	var rec orQuotaRecord
	if err := json.Unmarshal(b, &rec); err != nil || rec.Day != todayUTC() {
		return 0
	}
	return rec.Count
}

// RecordORFreeRequest increments today's counter and persists it. Best-effort:
// a lock or write failure loses one count, not a launch. Tests that drive the
// openrouter :free success path through the proxy should set XDG_CACHE_HOME to
// a temp dir (as the counter's own tests do) so the developer's real tally is
// not bumped by test traffic.
func RecordORFreeRequest() {
	orQuotaMu.Lock()
	defer orQuotaMu.Unlock()
	p := orQuotaPath()
	guard := flock.Lock(p)
	defer guard.Close()
	writeORFreeCountLocked(readORFreeCountLocked() + 1)
}

func writeORFreeCountLocked(n int64) {
	rec := orQuotaRecord{Day: todayUTC(), Count: n}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	p := orQuotaPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

// FetchOpenRouterCredits GETs {base}/credits and returns (total purchased,
// total used, ok). The docs say the endpoint needs a MANAGEMENT key ("Only
// management keys can perform this operation", 403), but in practice an
// ordinary inference key reads it fine (verified live); a non-200 just means
// "no richer data than /key", so callers treat ok=false as a skip. Both usage
// paths (poller, TUI banner) share this one fetch so they can never drift on
// what the credits numbers mean.
func FetchOpenRouterCredits(httpClient *http.Client, base, key string) (float64, float64, bool) {
	req, err := http.NewRequest(http.MethodGet, base+"/credits", nil)
	if err != nil {
		return 0, 0, false
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, false
	}
	var payload struct {
		Data struct {
			TotalCredits float64 `json:"total_credits"`
			TotalUsage   float64 `json:"total_usage"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, 0, false
	}
	return payload.Data.TotalCredits, payload.Data.TotalUsage, true
}
