package main

import (
	"testing"

	"github.com/raketenkater/ultra-zen/internal/models"
)

func TestResolveFastModelZen(t *testing.T) {
	list := []models.Model{
		{ID: "glm-5.2", Name: "GLM 5.2"},
		{ID: "glm-5.3", Name: "GLM 5.3"},
		{ID: "glm-5.3-flash", Name: "GLM 5.3 Flash"},
	}
	primary := &models.Model{ID: "glm-5.2", Name: "GLM 5.2"}
	if got := resolveFastModel("", "opencode-go", primary, list); got != "glm-5.3-flash" {
		t.Fatalf("auto-pick = %q, want glm-5.3-flash", got)
	}
	if got := resolveFastModel("none", "opencode-go", primary, list); got != "" {
		t.Fatalf("none = %q, want empty", got)
	}
	if got := resolveFastModel("glm-5.3", "opencode-go", primary, list); got != "glm-5.3" {
		t.Fatalf("explicit = %q, want glm-5.3", got)
	}
	if got := resolveFastModel("glm-5.2", "opencode-go", primary, list); got != "glm-5.2" {
		t.Fatalf("explicit id resolves even if it is the primary: %q", got)
	}
}

func TestResolveFastModelNoMatch(t *testing.T) {
	list := []models.Model{
		{ID: "deepseek-v4-flash-0731", Name: "DeepSeek V4 Flash 0731"},
	}
	primary := &models.Model{ID: "deepseek-v4-flash-0731", Name: "DeepSeek V4 Flash 0731"}
	// The primary IS the only flash model → auto-pick must keep the primary
	// (never pick a different, slower model just for the sake of separation).
	if got := resolveFastModel("", "saia", primary, list); got != "" {
		t.Fatalf("auto-pick with primary-only flash = %q, want empty (keep primary)", got)
	}
}

// TestResolveFastModelOpenRouterFreeCatalog exercises OpenRouter's free-catalog
// naming (no "flash" at all): lightning/mini/small/xs tier names must be
// recognized, free variants preferred, and huge models never picked.
func TestResolveFastModelOpenRouterFreeCatalog(t *testing.T) {
	list := []models.Model{
		{ID: "nvidia/nemotron-3-ultra-550b-a55b:free", Name: "Nemotron 3 Ultra", Free: true},
		{ID: "nvidia/nemotron-3.5-lightning:free", Name: "Nemotron 3.5 Lightning", Free: true},
		{ID: "cohere/north-mini-code:free", Name: "North Mini Code", Free: true},
		{ID: "poolside/laguna-xs-2.1:free", Name: "Laguna XS 2.1", Free: true},
		{ID: "minimax/minimax-m3:free", Name: "MiniMax M3", Free: true},
	}
	primary := &models.Model{ID: "nvidia/nemotron-3-ultra-550b-a55b:free", Name: "Nemotron 3 Ultra"}
	// lightning (100) beats mini (80) and xs (70), and "minimax-m3" must NOT
	// match the "mini" substring trap.
	if got := resolveFastModel("", "openrouter", primary, list); got != "nvidia/nemotron-3.5-lightning:free" {
		t.Fatalf("auto-pick = %q, want nemotron-3.5-lightning (and never minimax)", got)
	}

	// Without a lightning model, mini wins; "minimax" must still be excluded.
	noLightning := []models.Model{
		{ID: "cohere/north-mini-code:free", Name: "North Mini Code", Free: true},
		{ID: "minimax/minimax-m3:free", Name: "MiniMax M3", Free: true},
		{ID: "minimax/minimax-m2.7:free", Name: "MiniMax M2.7", Free: true},
	}
	if got := resolveFastModel("", "openrouter", primary, noLightning); got != "cohere/north-mini-code:free" {
		t.Fatalf("auto-pick = %q, want north-mini-code (minimax excluded)", got)
	}

	// Free variant preferred over a paid one of the same tier.
	tiered := []models.Model{
		{ID: "vendor/inkling-small", Name: "Inkling Small", Free: false},
		{ID: "vendor/inkling-small:free", Name: "Inkling Small", Free: true},
	}
	if got := resolveFastModel("", "openrouter", primary, tiered); got != "vendor/inkling-small:free" {
		t.Fatalf("auto-pick = %q, want the free variant", got)
	}

	// Friendlier display names with spaces must match too (name-only match).
	display := []models.Model{
		{ID: "vendor/model-x", Name: "Model X Lightning"},
	}
	if got := resolveFastModel("", "openrouter", primary, display); got != "vendor/model-x" {
		t.Fatalf("auto-pick = %q, want vendor/model-x (name-based match)", got)
	}
}

// TestFastTierCollapsed covers the tier-collapse guard's three cases: an
// auto-pick that ends up sharing the primary (either nothing matched, or the
// explicit id names the primary) must warn; --fast-model none keeps legacy
// behavior without a warning; an explicit fast model different from the
// primary never warns.
func TestFastTierCollapsed(t *testing.T) {
	const primary = "glm-5.2"
	cases := []struct {
		name      string
		flagValue string
		fast      string
		want      bool
	}{
		// Collapse: auto-pick found no flash/mini/lite sibling → Env keeps
		// every tier on the primary. This is the 3-of-9-sessions case.
		{"auto no match warns", "", "", true},
		// Collapse: explicit --fast-model names the primary itself.
		{"explicit primary warns", "glm-5.2", primary, true},
		// Legacy: "none" is the deliberate choice, never a silent collapse.
		{"none no warning", "none", "", false},
		{"off no warning", "off", "", false},
		// Separated: explicit fast model different from primary.
		{"explicit sibling no warning", "glm-5.3-flash", "glm-5.3-flash", false},
		// Separated: auto-pick found a sibling.
		{"auto sibling no warning", "", "glm-5.3-flash", false},
	}
	for _, tc := range cases {
		if got := fastTierCollapsed(tc.flagValue, tc.fast, primary); got != tc.want {
			t.Errorf("%s: fastTierCollapsed(%q, %q, %q) = %v, want %v",
				tc.name, tc.flagValue, tc.fast, primary, got, tc.want)
		}
	}
}

// TestResolveFastModelCollapseFeedsGuard ties resolveFastModel to the guard:
// the resolution paths that collapse the tier are exactly the ones the banner
// then warns about.
func TestResolveFastModelCollapseFeedsGuard(t *testing.T) {
	list := []models.Model{
		{ID: "deepseek-v4-flash-0731", Name: "DeepSeek V4 Flash 0731"},
	}
	primary := &models.Model{ID: "deepseek-v4-flash-0731", Name: "DeepSeek V4 Flash 0731"}
	// Auto-pick with only the primary as a flash candidate returns ""...
	fast := resolveFastModel("", "saia", primary, list)
	if fast != "" {
		t.Fatalf("auto-pick = %q, want empty", fast)
	}
	if !fastTierCollapsed("", fast, primary.ID) {
		t.Error("auto-pick collapsed onto the primary but the guard stays silent")
	}
	// ...and an explicit none resolves to "" without tripping the guard.
	fast = resolveFastModel("none", "saia", primary, list)
	if fastTierCollapsed("none", fast, primary.ID) {
		t.Error(`--fast-model none must not warn`)
	}
}
