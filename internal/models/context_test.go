package models

import "testing"

// TestFillContextWindowsFillsKnownZens verifies the curated context-window
// table fills models whose gateway reports no context (the opencode Zen and
// ModelScope gateways), and never overwrites a real gateway value.
func TestFillContextWindowsFillsKnownZens(t *testing.T) {
	entries := []apiModelEntry{
		{ID: "deepseek-v4-flash", ContextLength: 0},
		{ID: "glm-5.1", ContextLength: 0},
		{ID: "kimi-k2.6", ContextLength: 0},
		// A gateway-reported value must NOT be overwritten.
		{ID: "grok-4.5", ContextLength: 500_000},
		// Unknown model stays 0 (falls back to the caller's default).
		{ID: "brand-new-model", ContextLength: 0},
	}
	fillContextWindows(entries)
	got := map[string]int{}
	for _, e := range entries {
		got[e.ID] = e.ContextLength
	}
	if got["deepseek-v4-flash"] != 1_000_000 {
		t.Errorf("deepseek-v4-flash ctx = %d, want 1000000", got["deepseek-v4-flash"])
	}
	if got["glm-5.1"] != 200_000 {
		t.Errorf("glm-5.1 ctx = %d, want 200000", got["glm-5.1"])
	}
	if got["kimi-k2.6"] != 256_000 {
		t.Errorf("kimi-k2.6 ctx = %d, want 256000", got["kimi-k2.6"])
	}
	if got["brand-new-model"] != 0 {
		t.Errorf("unknown model ctx = %d, want 0 (unchanged)", got["brand-new-model"])
	}
	// A gateway-reported value is left alone (grok-4.5 has a real value).
	if got["grok-4.5"] != 500_000 {
		t.Errorf("grok-4.5 ctx = %d, want 500000 (gateway value preserved)", got["grok-4.5"])
	}
}

// TestKnownContextWindowsCoversZenCatalog verifies every currently-advertised
// Zen go-tier model has a curated window (so none silently falls back to the
// 200k default). This guards against a new Zen model shipping without a window
// and breaking autocompaction again.
func TestKnownContextWindowsCoversZenCatalog(t *testing.T) {
	zen := []string{
		"deepseek-v4-flash", "deepseek-v4-pro",
		"glm-5", "glm-5.1", "glm-5.2", "glm-5.3",
		"kimi-k2.5", "kimi-k2.6", "kimi-k2.7-code", "kimi-k3",
		"minimax-m2.5", "minimax-m2.7", "minimax-m3",
		"mimo-v2.5", "mimo-v2.5-pro", "mimo-v2-omni", "mimo-v2-pro",
		"gpt-5.6-luna", "gpt-5.6-terra",
		"grok-4.5", "hy3",
	}
	for _, id := range zen {
		if n, ok := knownContextWindows[id]; !ok || n <= 0 {
			t.Errorf("Zen model %q missing a curated context window (autocompaction would fall back to 200k)", id)
		}
	}
}
