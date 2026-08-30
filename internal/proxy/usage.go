package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// UsageKind enumerates how a provider meters its usage.
type UsageKind string

const (
	UsageCredits  UsageKind = "credits"  // metered in dollars / credits
	UsageRequests UsageKind = "requests" // metered in request count
	UsageUnknown  UsageKind = "unknown"  // no live endpoint; requests are counted locally
)

// UsageWindow enumerates the reset cadence of a usage stat.
type UsageWindow string

const (
	WindowNone    UsageWindow = "none"
	WindowDaily   UsageWindow = "daily"
	WindowWeekly  UsageWindow = "weekly"
	WindowMonthly UsageWindow = "monthly"
	Window5h      UsageWindow = "5h"
	WindowMinute  UsageWindow = "minute"
	WindowRolling UsageWindow = "rolling"
)

// WindowStat is one named usage window's live status.
type WindowStat struct {
	Status   string `json:"status"`   // human-readable window label, e.g. "rolling"
	Percent  int    `json:"percent"`  // consumed percentage 0-100
	ResetsAt string `json:"resetsAt"` // ISO timestamp of the next reset
	// State is the gateway's per-window health string ("ok",
	// "rate-limited"). Only the Zen /usage endpoint reports it; empty means
	// the endpoint did not send one. This is distinct from Status, which has
	// always carried the window's own label.
	State string `json:"state,omitempty"`
}

// ProviderUsage is the per-provider usage snapshot exposed at /v1/usage.
type ProviderUsage struct {
	Name          string      `json:"name"`   // provider name (e.g. "openrouter")
	Kind          UsageKind   `json:"kind"`   // credits | requests | unknown
	Window        UsageWindow `json:"window"` // primary reset cadence (summary)
	Used          *float64    `json:"used,omitempty"`
	Limit         *float64    `json:"limit,omitempty"`
	Remaining     *float64    `json:"remaining,omitempty"`
	Percent       *int        `json:"percent,omitempty"`
	RequestsUsed  *int64      `json:"requestsUsed,omitempty"`
	RequestsLimit *int64      `json:"requestsLimit,omitempty"`
	RequestsReset *int64      `json:"requestsReset,omitempty"`
	Rolling       *WindowStat `json:"rolling,omitempty"`
	Weekly        *WindowStat `json:"weekly,omitempty"`
	Monthly       *WindowStat `json:"monthly,omitempty"`
	// FreeLimit is the periodic cap for a credits provider (e.g. OpenRouter's
	// free-tier daily limit, captured from the /key `limit` field). It is set
	// only when the provider exposes a finite periodic allowance.
	FreeLimit *float64 `json:"freeLimit,omitempty"`
	// Daily is a daily reset window (e.g. OpenRouter free-tier limit_reset),
	// carrying the ISO timestamp of the next reset in ResetsAt.
	Daily *WindowStat `json:"daily,omitempty"`
	// Credits is the account balance (total purchased minus total spent),
	// from /credits — distinct from Remaining, which /key reports as the
	// per-key cap when the account has no credit data.
	Credits *float64 `json:"credits,omitempty"`
	// FreeReqsUsed/FreeReqsLimit are OpenRouter's :free request tally against
	// the daily cap. No public API exposes this, so the tally is ultra-zen's
	// own persisted per-UTC-day counter (a floor: requests made outside
	// ultra-zen are invisible).
	FreeReqsUsed  *int64 `json:"freeReqsUsed,omitempty"`
	FreeReqsLimit *int64 `json:"freeReqsLimit,omitempty"`
	Exhausted     bool   `json:"exhausted"`
	LastUpdated   string `json:"lastUpdated"` // ISO timestamp of the last good fetch
	Detail        string `json:"detail,omitempty"`
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

// IsCreditsExhausted reports whether an upstream error body is a credits/balance
// exhausted response — the live "drained" signal from opencode Zen (GoUsageLimitError
// at the request path) and the OpenAI/OpenRouter-style 401 with a CreditsError
// envelope. It is the single predicate the live usage poller and the launch
// fetch use to flip ProviderUsage.Exhausted, so the statusline and launch
// banner render the same drained token for the same data.
//
// We do NOT invent a dollar figure here: balance is console-only on the opencode
// side, and the upstream error string is the only ground truth we have. The
// returned reason is the first matching substring (lowercased) and is what the
// formatter surfaces as the "drained" message — a stable, user-readable
// excerpt, not a parse.
func IsCreditsExhausted(body []byte) (bool, string) {
	msg := strings.ToLower(string(body))
	// Order matters: more-specific phrases first so the surfaced reason is the
	// most informative. The exact tokens seen in the live incident
	// (2026-08-30) and in the documented upstream payloads:
	//   - {"error":{"type":"credits_error","message":"Insufficient balance.
	//     Manage your billing here..."}}
	//   - opencode zen: {"error":{"type":"error","message":"GoUsageLimitError:
	//     Credit balance too low..."}}
	//   - openai: {"error":{"type":"insufficient_quota","message":"..."}}
	candidates := []string{
		"insufficient balance",
		"manage your billing",
		"credit balance too low",
		"credits_error",
		"insufficient_quota",
		"gousagelimiterror",
	}
	for _, c := range candidates {
		if strings.Contains(msg, c) {
			return true, c
		}
	}
	return false, ""
}

// MarkExhaustedFromBody flips a provider's Exhausted flag and stores a
// balance-aware Detail when the body is a credits-exhausted response. It is
// the single point where the "drained" signal is set, so the statusline and
// the launch banner cannot drift apart on the same upstream evidence. The
// returned bool is true when the body was classified as a credits-exhausted
// response, so callers can decide whether to keep rotating (false) or stop
// retrying the provider (true) on this turn.
func (t *usageTracker) MarkExhaustedFromBody(provider string, body []byte) bool {
	if provider == "" {
		return false
	}
	exhausted, reason := IsCreditsExhausted(body)
	if !exhausted {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	row := t.rows[provider]
	if row == nil {
		row = &ProviderUsage{Name: provider}
		t.rows[provider] = row
	}
	row.Exhausted = true
	// Balance is console-only — we cannot invent a number. The Detail carries
	// the upstream phrase (e.g. "Insufficient balance. Manage your billing")
	// so the statusline and the launch banner surface the same words the user
	// will see in the provider's own dashboard. Truncated to keep the line
	// short beside the [Zen …] / [OR …] token.
	detail := reason
	if len(body) > 0 {
		// Prefer a slightly longer human-readable excerpt over the bare
		// marker so the statusline explains WHY, not just THAT. Strip
		// everything but the message field when it is a JSON envelope.
		detail = extractDrainedDetail(body, reason)
	}
	row.Detail = detail
	return true
}

// extractDrainedDetail pulls the message field out of a {error:{message:..}}
// envelope (or returns a trimmed plain-text body) so the statusline shows
// the upstream's own explanation, not a guess. Falls back to the lowercased
// marker if the body is not a JSON envelope.
func extractDrainedDetail(body []byte, fallback string) string {
	var env struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Error != nil && env.Error.Message != "" {
		msg := strings.TrimSpace(env.Error.Message)
		// Cap to 80 chars so the statusline stays one line beside the [..]
		// token; the full message is in the provider dashboard if needed.
		if len(msg) > 80 {
			msg = msg[:80] + "…"
		}
		return msg
	}
	return fallback
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
