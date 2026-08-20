package main

import (
	"testing"

	"github.com/raketenkater/ultra-zen/internal/models"
	"github.com/raketenkater/ultra-zen/internal/proxy"
)

// findUpstream returns the first selectable upstream matching provider+model,
// or nil.
func findUpstream(t *testing.T, ups []proxy.Upstream, provider, id string) *proxy.Upstream {
	t.Helper()
	for i := range ups {
		if ups[i].Provider == provider && ups[i].Model == id {
			return &ups[i]
		}
	}
	return nil
}

// countProvider returns how many modelInfos carry the given provider tag.
func countProvider(infos []proxy.ModelInfo, provider string) int {
	n := 0
	for _, m := range infos {
		if m.Provider == provider {
			n++
		}
	}
	return n
}

// TestBuildAdvertisedCatalog_OpenCodeGoPrimary covers assignment A:
// opencode-go is the primary; its paid and free models both surface exactly
// once in modelInfos even when zenList repeats them (the zen catalog is
// skipped for an opencode-go primary, so no duplicate "opencode-go" entries);
// selectable upstreams carry the right Base and the primary key; the primary's
// selected model is absent from selectable.
func TestBuildAdvertisedCatalog_OpenCodeGoPrimary(t *testing.T) {
	const goBase = "https://go.example"
	const mainBase = "https://main.example"

	paid := models.Model{ID: "glm-5.1", Name: "GLM-5.1", Base: goBase, Free: false, ContextLength: 128000}
	free := models.Model{ID: "glm-5.1-free", Name: "GLM-5.1 Free", Base: mainBase, Free: true, ContextLength: 64000}
	primaryList := []models.Model{paid, free}

	infos, selectable := buildAdvertisedCatalog(
		"opencode-go", "glm-5.1",
		primaryList, primaryList, nil, // zenList == primaryList (dedup target)
		nil, nil,
		"key-abc", "zenKey", "orKey",
		nil,
		"openai", "acc-1",
	)

	// Both models present exactly once, no duplicate "opencode-go" entries.
	if got := countProvider(infos, "opencode-go"); got != 2 {
		t.Fatalf("expected exactly 2 opencode-go modelInfos, got %d", got)
	}
	seenPaid, seenFree := false, false
	for _, m := range infos {
		if m.Provider != "opencode-go" {
			t.Fatalf("unexpected provider %q in output", m.Provider)
		}
		if m.ID == "glm-5.1" && m.ContextLength == 128000 {
			seenPaid = true
		}
		if m.ID == "glm-5.1-free" && m.ContextLength == 64000 {
			seenFree = true
		}
	}
	if !seenPaid || !seenFree {
		t.Fatalf("expected both paid and free opencode-go models in modelInfos (paid=%v free=%v)", seenPaid, seenFree)
	}

	// Selected model absent from selectable.
	if u := findUpstream(t, selectable, "opencode-go", "glm-5.1"); u != nil {
		t.Fatalf("selected primary model glm-5.1 should be absent from selectable, got %+v", *u)
	}
	// Non-selected primary model present with primary key + correct base.
	freeUp := findUpstream(t, selectable, "opencode-go", "glm-5.1-free")
	if freeUp == nil {
		t.Fatalf("expected glm-5.1-free selectable upstream")
	}
	if freeUp.BaseURL != mainBase {
		t.Errorf("glm-5.1-free BaseURL = %q, want %q", freeUp.BaseURL, mainBase)
	}
	if freeUp.APIKey != "key-abc" {
		t.Errorf("glm-5.1-free APIKey = %q, want primary key", freeUp.APIKey)
	}
	if freeUp.Kind != "openai" || freeUp.AccountID != "acc-1" {
		t.Errorf("primary selectable upstream should inherit Kind/AccountID, got kind=%q account=%q", freeUp.Kind, freeUp.AccountID)
	}
	if freeUp.Model != "glm-5.1-free" || freeUp.ContextLength != 64000 {
		t.Errorf("glm-5.1-free upstream Model/ContextLength mismatch: %+v", freeUp)
	}
}

// TestBuildAdvertisedCatalog_BYOPrimary covers assignment B: a BYO primary
// (saia) renders its models, a non-primary BYO provider (modelscope) is present
// and keys via freeTierKeys, the selected saia model is absent from selectable,
// and saia's selectable upstreams inherit primaryKind + accountID.
func TestBuildAdvertisedCatalog_BYOPrimary(t *testing.T) {
	saiaSelected := models.Model{ID: "saia-1", Name: "SAIA 1", Base: "https://saia.example", Free: false, ContextLength: 32000}
	saiaOther := models.Model{ID: "saia-2", Name: "SAIA 2", Base: "https://saia.example", Free: false, ContextLength: 16000}
	msModel := models.Model{ID: "ms-1", Name: "MS 1", Base: "https://modelscope.example", Free: true, ContextLength: 8000}

	infos, selectable := buildAdvertisedCatalog(
		"saia", "saia-1",
		[]models.Model{saiaSelected, saiaOther},
		[]models.Model{models.Model{ID: "zen-1", Base: "https://zen.example", Free: true}},
		[]models.Model{models.Model{ID: "or-1", Base: "https://or.example", Free: true}},
		map[string][]models.Model{"modelscope": {msModel}},
		[]string{"saia", "modelscope"},
		"key-saia", "zenKey", "orKey",
		map[string]string{"modelscope": "key-ms"},
		"responses", "acc-77",
	)

	if got := countProvider(infos, "saia"); got != 2 {
		t.Fatalf("expected 2 saia modelInfos, got %d", got)
	}
	if got := countProvider(infos, "modelscope"); got != 1 {
		t.Fatalf("expected 1 modelscope modelInfo, got %d", got)
	}

	// Selected saia model absent from selectable.
	if u := findUpstream(t, selectable, "saia", "saia-1"); u != nil {
		t.Fatalf("selected saia model saia-1 should be absent from selectable, got %+v", *u)
	}
	// Non-selected saia model inherits Kind/AccountID.
	saiaOtherUp := findUpstream(t, selectable, "saia", "saia-2")
	if saiaOtherUp == nil {
		t.Fatalf("expected saia-2 selectable upstream")
	}
	if saiaOtherUp.Kind != "responses" || saiaOtherUp.AccountID != "acc-77" {
		t.Errorf("saia selectable upstream should inherit Kind/AccountID, got kind=%q account=%q", saiaOtherUp.Kind, saiaOtherUp.AccountID)
	}
	if saiaOtherUp.APIKey != "key-saia" {
		t.Errorf("saia-2 APIKey = %q, want primary key", saiaOtherUp.APIKey)
	}
	// Modelscope model keys via freeTierKeys.
	msUp := findUpstream(t, selectable, "modelscope", "ms-1")
	if msUp == nil {
		t.Fatalf("expected modelscope selectable upstream")
	}
	if msUp.APIKey != "key-ms" {
		t.Errorf("modelscope APIKey = %q, want freeTierKeys value", msUp.APIKey)
	}
	if msUp.Kind != "" || msUp.AccountID != "" {
		t.Errorf("non-primary BYO upstream should not inherit Kind/AccountID, got kind=%q account=%q", msUp.Kind, msUp.AccountID)
	}
}

// TestBuildAdvertisedCatalog_KeyResolution covers assignment C: an openrouter
// model keys via openRouterKey, an opencode-go (non-primary) model via zenKey.
func TestBuildAdvertisedCatalog_KeyResolution(t *testing.T) {
	infos, selectable := buildAdvertisedCatalog(
		"saia", "saia-1",
		[]models.Model{models.Model{ID: "saia-1", Base: "https://saia.example", Free: false}},
		[]models.Model{models.Model{ID: "zen-1", Base: "https://zen.example", Free: true}},
		[]models.Model{models.Model{ID: "or-1", Base: "https://or.example", Free: true}},
		nil, nil,
		"key-saia", "key-zen", "key-or",
		nil,
		"openai", "acc-1",
	)

	zenUp := findUpstream(t, selectable, "opencode-go", "zen-1")
	if zenUp == nil {
		t.Fatalf("expected opencode-go zen-1 selectable upstream")
	}
	if zenUp.APIKey != "key-zen" {
		t.Errorf("opencode-go APIKey = %q, want zenKey", zenUp.APIKey)
	}
	orUp := findUpstream(t, selectable, "openrouter", "or-1")
	if orUp == nil {
		t.Fatalf("expected openrouter or-1 selectable upstream")
	}
	if orUp.APIKey != "key-or" {
		t.Errorf("openrouter APIKey = %q, want openRouterKey", orUp.APIKey)
	}
	// Sanity: both providers present in modelInfos.
	if countProvider(infos, "opencode-go") != 1 || countProvider(infos, "openrouter") != 1 {
		t.Fatalf("expected one opencode-go and one openrouter modelInfo, got %d/%d",
			countProvider(infos, "opencode-go"), countProvider(infos, "openrouter"))
	}
}

// TestBuildAdvertisedCatalog_DedupByOOverlap covers assignment D: the same
// (provider,id) listed twice from a BYO overlap is not double-added.
func TestBuildAdvertisedCatalog_DedupByOOverlap(t *testing.T) {
	m := models.Model{ID: "ms-1", Name: "MS 1", Base: "https://ms.example", Free: true, ContextLength: 8000}

	infos, selectable := buildAdvertisedCatalog(
		"saia", "saia-1",
		[]models.Model{models.Model{ID: "saia-1", Base: "https://saia.example", Free: false}},
		nil, nil,
		// same provider listed twice in byoProviders -> overlap
		map[string][]models.Model{"modelscope": {m, m}},
		[]string{"modelscope", "modelscope"},
		"key-saia", "zenKey", "orKey",
		map[string]string{"modelscope": "key-ms"},
		"openai", "acc-1",
	)

	if got := countProvider(infos, "modelscope"); got != 1 {
		t.Fatalf("expected exactly 1 modelscope modelInfo after dedup, got %d", got)
	}
	// The two identical models also dedup within a single list.
	count := 0
	for _, m2 := range infos {
		if m2.Provider == "modelscope" && m2.ID == "ms-1" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected ms-1 exactly once in modelInfos, got %d", count)
	}
	if len(selectable) != 1 || selectable[0].Model != "ms-1" {
		t.Fatalf("expected a single selectable ms-1 upstream, got %+v", selectable)
	}
}
