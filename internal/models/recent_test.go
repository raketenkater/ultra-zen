package models

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSortByRecent(t *testing.T) {
	list := []Model{
		{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"},
	}
	got := SortByRecent(list, []string{"c", "a"})
	want := []string{"c", "a", "b", "d"}
	if len(got) != len(want) {
		t.Fatalf("len %d, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("got[%d]=%s want %s (full %v)", i, got[i].ID, id, got)
		}
	}
}

func TestSortByRecentEmpty(t *testing.T) {
	list := []Model{{ID: "a"}, {ID: "b"}}
	got := SortByRecent(list, nil)
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("order changed: %v", got)
	}
}

func TestRecordRecentRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)

	RecordRecent("x")
	RecordRecent("y")
	RecordRecent("x") // dedupe, moves to front
	got := LoadRecent()
	want := []string{"x", "y"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("LoadRecent=%v want %v", got, want)
	}

	// File actually landed under XDG_CACHE_HOME.
	if _, err := os.Stat(filepath.Join(dir, "ultra-zen", "recent-models.json")); err != nil {
		t.Fatalf("store file missing: %v", err)
	}
}

func TestLoadRecentMissing(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if got := LoadRecent(); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

// TestRecentRoutesRecordProvider covers the reason the store holds more than
// an id: a recently used model must be relaunchable, and that needs the
// gateway it was reached through.
func TestRecentRoutesRecordProvider(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	RecordRecentRoute("opencode-go", "glm-5.2")
	RecordRecentRoute("openrouter", "xiaomi/mimo-v2.6-pro")

	got := LoadRecentRoutes()
	if len(got) != 2 {
		t.Fatalf("LoadRecentRoutes = %d entries, want 2", len(got))
	}
	if got[0].Provider != "openrouter" || got[0].Model != "xiaomi/mimo-v2.6-pro" {
		t.Errorf("newest = %+v, want the openrouter mimo launch", got[0])
	}
	if got[1].Provider != "opencode-go" {
		t.Errorf("older entry lost its provider: %+v", got[1])
	}
}

// TestRecentRoutesDedupeByModelKeepsNewestProvider pins the dedupe rule: the
// list answers "what did I run recently", so one model is one answer, and the
// provider shown is the one most recently used.
func TestRecentRoutesDedupeByModelKeepsNewestProvider(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	RecordRecentRoute("opencode-go", "glm-5.2")
	RecordRecentRoute("openrouter", "z-ai/glm-5")
	RecordRecentRoute("openrouter", "glm-5.2") // same model, other gateway

	got := LoadRecentRoutes()
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 (glm-5.2 deduped)", len(got))
	}
	if got[0].Model != "glm-5.2" || got[0].Provider != "openrouter" {
		t.Errorf("newest = %+v, want glm-5.2 via openrouter", got[0])
	}
}

// TestLoadRecentRoutesReadsLegacyIDs is the upgrade path: the file on disk
// predates the provider field, and dropping those entries would silently
// empty every existing user's recents.
func TestLoadRecentRoutesReadsLegacyIDs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	p := recentPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`["glm-5.2","kimi-k3"]`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := LoadRecentRoutes()
	if len(got) != 2 || got[0].Model != "glm-5.2" || got[1].Model != "kimi-k3" {
		t.Fatalf("legacy file read as %+v, want both ids in order", got)
	}
	if got[0].Provider != "" {
		t.Errorf("legacy entry invented a provider: %q", got[0].Provider)
	}
	// The id-only view must keep working for the ordering helpers.
	if ids := LoadRecent(); len(ids) != 2 || ids[0] != "glm-5.2" {
		t.Errorf("LoadRecent = %v, want the legacy ids", ids)
	}
}

// TestLoadRecentRoutesMixedFormats covers a file written across an upgrade:
// new object entries in front of legacy strings.
func TestLoadRecentRoutesMixedFormats(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	p := recentPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	body := `[{"provider":"openrouter","model":"z-ai/glm-5"},"kimi-k3"]`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadRecentRoutes()
	if len(got) != 2 {
		t.Fatalf("mixed file read as %d entries, want 2", len(got))
	}
	if got[0].Provider != "openrouter" || got[1].Model != "kimi-k3" {
		t.Errorf("mixed file read as %+v", got)
	}
}
