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
