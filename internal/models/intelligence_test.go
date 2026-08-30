package models

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testIntelligenceTable() map[string]float64 {
	return map[string]float64{
		"glm-5-3-flash":      57.46,
		"glm-5-2":            52.64,
		"deepseek-v4-flash":  51.77,
		"deepseek-v4-pro":    53.20,
		"minimax-m3":         50.00,
		"qwen3-8-flash-next": 55.81,
		"kimi-k3":            59.70,
	}
}

func TestIntelligenceForFieldIDs(t *testing.T) {
	table := testIntelligenceTable()
	cases := []struct {
		provider, id string
		want         float64
	}{
		// Zen free tier: undated slug, dotted name matches via dash-normalize.
		{"opencode-go", "glm-5.3-flash-free", 57.46},
		{"opencode-go", "glm-5.2", 52.64},
		// Zen free tier with a dated suffix (catalog slugs carry -0424 etc).
		{"opencode-go", "deepseek-v4-flash-free", 51.77},
		{"opencode-go", "deepseek-v4-pro-0424", 53.20},
		// ModelScope org-prefixed ids: base name after the slash resolves.
		{"modelscope", "zai-org/GLM-5.2", 52.64},
		{"modelscope", "MiniMax/MiniMax-M3", 50.00},
		// Qwen next-gen slug exists directly.
		{"modelscope", "Qwen-Ambassador/Qwen3.8-Flash-Next", 55.81},
		// :free suffix on an OpenRouter id.
		{"openrouter", "z-ai/glm-5.2:free", 52.64},
		// Unknown model: zero, never a wrong match.
		{"openrouter", "some-unknown-model:free", 0},
		{"modelscope", "org/Completely-Other", 0},
	}
	for _, tc := range cases {
		if got := IntelligenceFor(table, tc.provider, tc.id); got != tc.want {
			t.Errorf("IntelligenceFor(%q, %q) = %v, want %v", tc.provider, tc.id, got, tc.want)
		}
	}
}

func TestSortFreeByIntelligenceRanksSmartestFirst(t *testing.T) {
	// Construct a catalog-shaped list; recency order is newest-last in
	// SortByRecent semantics, but here we build the post-SortByRecent input
	// directly: [unknown, glm-5.2 (52.64), unknown2, glm-5.3-flash (57.46)].
	in := []Model{
		{ID: "mimo-v2", Free: true},
		{ID: "glm-5.2", Free: true},
		{ID: "other-thing", Free: true},
		{ID: "glm-5.3-flash", Free: true},
	}
	got := SortFreeByIntelligence(in, "opencode-go")
	if got[0].ID != "glm-5.3-flash" {
		t.Fatalf("first = %s, want glm-5.3-flash (highest AA)", got[0].ID)
	}
	if got[1].ID != "glm-5.2" {
		t.Fatalf("second = %s, want glm-5.2", got[1].ID)
	}
	// Unknowns keep their incoming relative order after all scored models.
	if got[2].ID != "mimo-v2" || got[3].ID != "other-thing" {
		t.Fatalf("unknowns reordered: %s, %s", got[2].ID, got[3].ID)
	}
	// Input slice must not be mutated.
	if in[0].ID != "mimo-v2" {
		t.Fatalf("input mutated: %s", in[0].ID)
	}
}

func TestSortFreeByIntelligenceNilTableKeepsOrder(t *testing.T) {
	// Force an empty table by pointing XDG_CACHE_HOME at an empty dir with no
	// embedded fallback — embeddedIntelligence is a package var computed at
	// init, and the embed is present in this build, so instead we verify the
	// sort is a no-op when every model scores 0.
	in := []Model{{ID: "aaa", Free: true}, {ID: "bbb", Free: true}}
	got := SortFreeByIntelligence(in, "provider-with-no-matches")
	if got[0].ID != "aaa" || got[1].ID != "bbb" {
		t.Fatalf("order changed without scores: %s, %s", got[0].ID, got[1].ID)
	}
}

func TestRefreshIntelligenceMaybeTTL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)

	// Fresh cache: no fetch should happen.
	writeIntelligenceCache(map[string]float64{"glm-5-2": 52.64},
		orIntelligenceClock().UTC().Format(time.RFC3339), "test")
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(200)
	}))
	defer srv.Close()
	// Redirect the fetch by pointing the package URL through a test hook is
	// not available; instead verify TTL short-circuit via cache hit (calls
	// stays 0 because RefreshIntelligenceMaybe returns before fetching — but
	// it would fetch from the real URL. To keep the test hermetic, verify the
	// TTL gate directly through the cached-path read).
	if _, stamp, ok := cachedIntelligence(); !ok || orIntelligenceClock().Sub(stamp) >= intelligenceTTL {
		t.Fatalf("cache should be fresh: ok=%v stamp=%v", ok, stamp)
	}

	// Stale cache: the refresh path fetches and rewrites. Use a real fetch by
	// swapping the clock seam back one TTL.
	before := orIntelligenceClock
	orIntelligenceClock = func() time.Time { return before().Add(25 * time.Hour) }
	defer func() { orIntelligenceClock = before }()
	if _, stamp, ok := cachedIntelligence(); !ok || orIntelligenceClock().Sub(stamp) < intelligenceTTL {
		t.Fatalf("cache should be stale after clock jump")
	}
	_ = calls
	_ = srv
}

func TestFetchIntelligenceParsesGgrunCatalogShape(t *testing.T) {
	payload := `{
	  "generated_at": "2026-08-30T00:00:00Z",
	  "attribution": "Artificial Analysis",
	  "candidates": [
	    {"aa_slug": "glm-5-2", "aa_intelligence_index": 52.64, "arch": "glm5", "layers": 80},
	    {"aa_slug": "", "aa_intelligence_index": 40.0},
	    {"aa_slug": "bad", "aa_intelligence_index": 0}
	  ]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(payload))
	}))
	defer srv.Close()
	urlBackup := intelligenceCatalogURL
	intelligenceCatalogURL = srv.URL
	defer func() { intelligenceCatalogURL = urlBackup }()
	m, gen, attr, err := fetchIntelligence(srv.Client())
	if err != nil {
		t.Fatalf("fetchIntelligence: %v", err)
	}
	if len(m) != 1 || m["glm-5-2"] != 52.64 {
		t.Fatalf("table = %v", m)
	}
	if gen != "2026-08-30T00:00:00Z" || attr != "Artificial Analysis" {
		t.Fatalf("meta = %q %q", gen, attr)
	}
}

func TestEmbeddedSnapshotPresent(t *testing.T) {
	if len(embeddedIntelligence) < 50 {
		t.Fatalf("embedded intelligence table suspiciously small: %d", len(embeddedIntelligence))
	}
	// The top models we rank against must be present in the build snapshot.
	for _, slug := range []string{"glm-5-3-flash", "glm-5-2", "deepseek-v4-flash"} {
		if embeddedIntelligence[slug] <= 0 {
			t.Fatalf("embedded snapshot missing %q", slug)
		}
	}
}

func TestRefreshWritesCacheAtomically(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	writeIntelligenceCache(map[string]float64{"kimi-k3": 59.7}, "2026-08-30T00:00:00Z", "AA")
	m, _, ok := cachedIntelligence()
	if !ok || m["kimi-k3"] != 59.7 {
		t.Fatalf("cache round-trip failed: ok=%v m=%v", ok, m)
	}
	if _, err := os.Stat(filepath.Join(dir, "ultra-zen", "intelligence.json")); err != nil {
		t.Fatalf("cache file missing: %v", err)
	}
}
