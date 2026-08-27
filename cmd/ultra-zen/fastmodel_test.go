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
