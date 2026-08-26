package proxy

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// UsageKind enumerates how a provider meters its usage.
type UsageKind string

const (
	UsageCredits UsageKind = "credits" // metered in dollars / credits
	UsageRequests UsageKind = "requests" // metered in request count
	UsageUnknown UsageKind = "unknown" // no live endpoint; requests are counted locally
)

// UsageWindow enumerates the reset cadence of a usage stat.
type UsageWindow string

const (
	WindowNone     UsageWindow = "none"
	WindowDaily    UsageWindow = "daily"
	WindowWeekly   UsageWindow = "weekly"
	WindowMonthly  UsageWindow = "monthly"
	Window5h       UsageWindow = "5h"
	WindowMinute   UsageWindow = "minute"
	WindowRolling  UsageWindow = "rolling"
)

// WindowStat is one named usage window's live status.
type WindowStat struct {
	Status   string `json:"status"`   // human-readable window label, e.g. "rolling"
	Percent  int    `json:"percent"`  // consumed percentage 0-100
	ResetsAt string `json:"resetsAt"` // ISO timestamp of the next reset
}

// ProviderUsage is the per-provider usage snapshot exposed at /v1/usage.
type ProviderUsage struct {
	Name        string      `json:"name"`        // provider name (e.g. "openrouter")
	Kind        UsageKind   `json:"kind"`        // credits | requests | unknown
	Window      UsageWindow `json:"window"`      // primary reset cadence (summary)
	Used        *float64    `json:"used,omitempty"`
	Limit       *float64    `json:"limit,omitempty"`
	Remaining   *float64    `json:"remaining,omitempty"`
	Percent     *int        `json:"percent,omitempty"`
	RequestsUsed   *int64    `json:"requestsUsed,omitempty"`
	RequestsLimit  *int64    `json:"requestsLimit,omitempty"`
	RequestsReset  *int64    `json:"requestsReset,omitempty"`
	Rolling    *WindowStat `json:"rolling,omitempty"`
	Weekly     *WindowStat `json:"weekly,omitempty"`
	Monthly    *WindowStat `json:"monthly,omitempty"`
	// FreeLimit is the periodic cap for a credits provider (e.g. OpenRouter's
	// free-tier daily limit, captured from the /key `limit` field). It is set
	// only when the provider exposes a finite periodic allowance.
	FreeLimit  *float64    `json:"freeLimit,omitempty"`
	// Daily is a daily reset window (e.g. OpenRouter free-tier limit_reset),
	// carrying the ISO timestamp of the next reset in ResetsAt.
	Daily      *WindowStat `json:"daily,omitempty"`
	Exhausted  bool        `json:"exhausted"`
	LastUpdated string     `json:"lastUpdated"` // ISO timestamp of the last good fetch
	Detail     string      `json:"detail,omitempty"`
}

// usageTracker is a concurrency-safe store of per-provider usage rows plus a
// per-provider request counter. The request counter lets providers without a
// live usage endpoint (cerebras/cohere/modelscope/huggingface/codex) at least
// report how many requests have been served this session.
type usageTracker struct {
	mu       sync.RWMutex
	rows     map[string]*ProviderUsage
	counters map[string]*atomic.Int64
}

// newUsageTracker constructs an empty tracker.
func newUsageTracker() *usageTracker {
	return &usageTracker{
		rows:     map[string]*ProviderUsage{},
		counters: map[string]*atomic.Int64{},
	}
}

// recordRequest increments the served-request counter for a provider. Used for
// providers whose live usage is only observable via response headers or not at
// all, so the statusline can still show activity.
func (t *usageTracker) recordRequest(provider string) {
	if provider == "" {
		return
	}
	t.mu.Lock()
	c, ok := t.counters[provider]
	if !ok {
		c = &atomic.Int64{}
		t.counters[provider] = c
	}
	t.mu.Unlock()
	c.Add(1)
}

// setExhausted flips the exhausted flag for a provider (e.g. on a 429 triage or
// a later 200 that clears it).
func (t *usageTracker) setExhausted(provider string, exhausted bool) {
	if provider == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	row := t.rows[provider]
	if row == nil {
		row = &ProviderUsage{Name: provider}
		t.rows[provider] = row
	}
	row.Exhausted = exhausted
}

// recordRateLimit folds rate-limit response headers into the provider's row. It
// tolerates both the OpenAI-style X-RateLimit-Remaining-Requests / -Limit-Requests
// / -Reset-Requests headers and the Kong IETF X-RateLimit-Remaining / -Reset
// variants (case-insensitive). A missing field is left untouched.
func (t *usageTracker) recordRateLimit(provider string, hdr map[string][]string) {
	if provider == "" || len(hdr) == 0 {
		return
	}
	get := func(name string) string {
		// http.Header keys are canonicalized (title-case) on read, but be
		// defensive about callers that pass raw textproto maps.
		if v := hdr[name]; len(v) > 0 {
			return v[0]
		}
		lower := map[string]string{}
		for k, vals := range hdr {
			lower[lowerKey(k)] = vals[0]
		}
		for _, cand := range []string{name, lowerKey(name)} {
			if v, ok := lower[cand]; ok {
				return v
			}
		}
		return ""
	}
	remaining := get("X-RateLimit-Remaining-Requests")
	if remaining == "" {
		remaining = get("X-RateLimit-Remaining")
	}
	limit := get("X-RateLimit-Limit-Requests")
	reset := get("X-RateLimit-Reset-Requests")
	if reset == "" {
		reset = get("X-RateLimit-Reset")
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	row := t.rows[provider]
	if row == nil {
		row = &ProviderUsage{Name: provider}
		t.rows[provider] = row
	}
	if row.Kind == "" {
		row.Kind = UsageRequests
	}
	row.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	if remaining != "" {
		if rem, err := parseInt64(remaining); err == nil {
			if limit != "" {
				if lv, err := parseInt64(limit); err == nil && lv > 0 {
					used := lv - rem
					row.RequestsLimit = &lv
					row.RequestsUsed = &used
					pct := int(100 * float64(used) / float64(lv))
					row.Percent = &pct
				}
			} else {
				// No limit header: report remaining requests as RequestsUsed so
				// the statusline can show "N req" even without a known ceiling.
				r := rem
				row.RequestsUsed = &r
			}
		}
	}
	if reset != "" {
		if v, err := parseInt64(reset); err == nil {
			rv := v
			row.RequestsReset = &rv
		} else if ts := parseISO(reset); ts != "" {
			row.Detail = "reset " + ts
		}
	}
}

// setRow replaces a provider's row under the lock (used by the poller after a
// successful live fetch). The request counter is preserved across the swap.
func (t *usageTracker) setRow(provider string, row *ProviderUsage) {
	if provider == "" || row == nil {
		return
	}
	t.mu.Lock()
	if c, ok := t.counters[provider]; ok {
		cnt := c.Load()
		if cnt > 0 {
			if row.RequestsUsed == nil {
				r := cnt
				row.RequestsUsed = &r
			}
		}
	}
	if row.Kind == "" {
		row.Kind = UsageUnknown
	}
	row.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	t.rows[provider] = row
	t.mu.Unlock()
}

// getRows returns a snapshot of all known provider rows. It is cheap (<1ms) and
// read-locked so it never blocks the request path.
func (t *usageTracker) getRows() []ProviderUsage {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]ProviderUsage, 0, len(t.rows))
	for _, row := range t.rows {
		out = append(out, *row)
	}
	return out
}

// lowerKey lowercases a header name for case-insensitive lookup.
func lowerKey(k string) string {
	b := []byte(k)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// parseInt64 parses a possibly-float rate-limit header value as int64.
func parseInt64(s string) (int64, error) {
	// Strip whitespace, then try int then float.
	var i int64
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &i); err == nil {
		return i, nil
	}
	var f float64
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%f", &f); err == nil {
		return int64(f), nil
	}
	return 0, fmt.Errorf("not a number: %q", s)
}

// parseISO normalizes a header reset value. If it looks like a Unix epoch it is
// returned as an ISO timestamp; if it already parses as an RFC3339 time it is
// returned verbatim; otherwise "" is returned.
func parseISO(s string) string {
	s = strings.TrimSpace(s)
	if v, err := parseInt64(s); err == nil {
		// Heuristic: a value > 1e10 is a Unix epoch in seconds/millis, not a
		// remaining-request count.
		if v > 1e10 {
			secs := v
			if v > 1e12 {
				secs = v / 1000
			}
			return time.Unix(secs, 0).UTC().Format(time.RFC3339)
		}
		return ""
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}
