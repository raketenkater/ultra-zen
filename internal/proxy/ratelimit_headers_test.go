package proxy

import (
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
