package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRecordRateLimitSAIAWindowedHeaders pins the SAIA/Kong quota family:
// x-ratelimit-{limit,remaining}-{minute,hour,day,month}. The day window is
// the headline a free-tier user acts on, so it populates the row's
// RequestsLimit/RequestsUsed (verified against a live response captured
// 2026-08-31: remaining-day 944 of limit-day 1000).
func TestRecordRateLimitSAIAWindowedHeaders(t *testing.T) {
	s := New(Config{})
	hdr := map[string][]string{
		"Ratelimit-Reset":              {"48"},
		"X-Ratelimit-Limit-Minute":     {"30"},
		"X-Ratelimit-Limit-Hour":       {"200"},
		"X-Ratelimit-Limit-Day":        {"1000"},
		"X-Ratelimit-Limit-Month":      {"3000"},
		"Ratelimit-Remaining":          {"29"},
		"Ratelimit-Limit":              {"30"},
		"X-Ratelimit-Remaining-Minute": {"29"},
		"X-Ratelimit-Remaining-Hour":   {"173"},
		"X-Ratelimit-Remaining-Day":    {"944"},
		"X-Ratelimit-Remaining-Month":  {"2944"},
	}
	s.usage.recordRateLimit("saia", hdr)
	row := s.usage.getRowSnapshot("saia")
	if row == nil {
		t.Fatal("no row recorded for saia")
	}
	if row.RequestsLimit == nil || *row.RequestsLimit != 1000 {
		t.Fatalf("RequestsLimit = %v, want 1000 (day window)", row.RequestsLimit)
	}
	if row.RequestsUsed == nil || *row.RequestsUsed != 56 {
		t.Fatalf("RequestsUsed = %v, want 56 (1000-944)", row.RequestsUsed)
	}
}

// TestRecordRateLimitModelScopeAccountHeaders pins the documented
// modelscope-ratelimit-requests-{limit,remaining} pair (account-wide daily
// quota). Headers are only sent on some deployments; absent headers must
// leave the row untouched (requests-counted fallback).
func TestRecordRateLimitModelScopeAccountHeaders(t *testing.T) {
	s := New(Config{})
	s.usage.recordRateLimit("modelscope", map[string][]string{
		"Modelscope-Ratelimit-Requests-Limit":     {"2000"},
		"Modelscope-Ratelimit-Requests-Remaining": {"1850"},
	})
	row := s.usage.getRowSnapshot("modelscope")
	if row == nil {
		t.Fatal("no row recorded for modelscope")
	}
	if row.RequestsLimit == nil || *row.RequestsLimit != 2000 {
		t.Fatalf("RequestsLimit = %v, want 2000", row.RequestsLimit)
	}
	if row.RequestsUsed == nil || *row.RequestsUsed != 150 {
		t.Fatalf("RequestsUsed = %v, want 150", row.RequestsUsed)
	}

	// No quota headers: row stays a bare request counter.
	s2 := New(Config{})
	s2.usage.recordRateLimit("modelscope", map[string][]string{"Content-Type": {"application/json"}})
	row2 := s2.usage.getRowSnapshot("modelscope")
	if row2 == nil {
		t.Fatal("expected a placeholder row even without quota headers")
	}
	if row2.RequestsLimit != nil {
		t.Fatalf("RequestsLimit set without quota headers: %v", *row2.RequestsLimit)
	}
}

// TestRecordRateLimitLegacySingleWindowStillWorks guards the original
// X-RateLimit-Remaining/Limit path now that windowed families take priority.
func TestRecordRateLimitLegacySingleWindowStillWorks(t *testing.T) {
	s := New(Config{})
	s.usage.recordRateLimit("groq", map[string][]string{
		"X-RateLimit-Remaining-Requests": {"27"},
		"X-RateLimit-Limit-Requests":     {"30"},
	})
	row := s.usage.getRowSnapshot("groq")
	if row == nil || row.RequestsLimit == nil || *row.RequestsLimit != 30 {
		t.Fatalf("legacy path broken: %+v", row)
	}
	if row.RequestsUsed == nil || *row.RequestsUsed != 3 {
		t.Fatalf("RequestsUsed = %v, want 3", row.RequestsUsed)
	}
}

// TestSuccessfulResponseSeedsQuotaRow is the regression for the SAIA statusline
// sitting at "[saia —]" through heavy use. recordRateLimit was called ONLY on
// the 429 branch, so a provider that advertises its allowance on every 200 —
// SAIA publishes X-RateLimit-Remaining-{Minute,Hour,Day,Month} plus limits on
// each completed request, and nothing at all on an empty probe — could never
// populate its row. The 200 path must record too.
func TestSuccessfulResponseSeedsQuotaRow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A real success carrying SAIA's header family (shape captured live
		// 2026-09-23: remaining Day 900 of limit 1000).
		w.Header().Set("X-RateLimit-Limit-Minute", "30")
		w.Header().Set("X-RateLimit-Remaining-Minute", "27")
		w.Header().Set("X-RateLimit-Limit-Day", "1000")
		w.Header().Set("X-RateLimit-Remaining-Day", "900")
		w.Header().Set("X-RateLimit-Limit-Month", "3000")
		w.Header().Set("X-RateLimit-Remaining-Month", "2900")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	srv := New(Config{
		BaseURL:  upstream.URL,
		APIKey:   "saia-key",
		Provider: "saia",
		Model:    "meta-llama-3.1-8b-instruct",
		Port:     0,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Post(srv.BaseURL()+"/v1/messages", "application/json", strings.NewReader(
		`{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	row := srv.usage.getRowSnapshot("saia")
	if row == nil {
		t.Fatal("no saia row after a successful request")
	}
	if row.RequestsLimit == nil || *row.RequestsLimit != 1000 {
		t.Fatalf("RequestsLimit = %v, want 1000 (day window is the headline)", row.RequestsLimit)
	}
	if row.RequestsUsed == nil || *row.RequestsUsed != 100 {
		t.Fatalf("RequestsUsed = %v, want 100 (1000 limit - 900 remaining)", row.RequestsUsed)
	}
}
