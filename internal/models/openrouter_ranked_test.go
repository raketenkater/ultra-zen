package models

import (
	"testing"
)

// TestOrderOpenRouterRankedGroupsFreeFirst pins the picker's OpenRouter order:
// the whole free tier forms the top block (usage-descending within it), paid
// models follow (usage-descending). Previously free and paid interleaved by raw
// tokens, so a :free model sat mid-list behind paid traffic and a TopN cap
// could truncate the free tier entirely.
func TestOrderOpenRouterRankedGroupsFreeFirst(t *testing.T) {
	out := []Model{
		{ID: "vendor/paid-big", Name: "Paid Big", Base: OpenRouterBase},
		{ID: "vendor/free-small", Name: "Free Small", Base: OpenRouterBase, Free: true},
		{ID: "vendor/paid-small", Name: "Paid Small", Base: OpenRouterBase},
		{ID: "vendor/free-big", Name: "Free Big", Base: OpenRouterBase, Free: true},
	}
	rank := map[string]rankingEntry{
		"vendor/paid-big":   {tokens: 900},
		"vendor/paid-small": {tokens: 10},
		"vendor/free-big":   {tokens: 500},
		"vendor/free-small": {tokens: 20},
	}
	orderOpenRouterRanked(out, rank)

	want := []string{"vendor/free-big", "vendor/free-small", "vendor/paid-big", "vendor/paid-small"}
	for i, id := range want {
		if out[i].ID != id {
			t.Fatalf("order = %v, want %v", ids(out), want)
		}
	}
}

// TestOrderOpenRouterRankedUnrankedFallToBlockEnd verifies unranked models sort
// last within their own block (alphabetically), not globally — a free model
// missing from the rankings still outranks any paid model.
func TestOrderOpenRouterRankedUnrankedFallToBlockEnd(t *testing.T) {
	out := []Model{
		{ID: "vendor/paid-ranked", Name: "Paid Ranked", Base: OpenRouterBase},
		{ID: "vendor/free-unranked", Name: "Free Unranked", Base: OpenRouterBase, Free: true},
	}
	rank := map[string]rankingEntry{"vendor/paid-ranked": {tokens: 1000}}
	orderOpenRouterRanked(out, rank)
	if out[0].ID != "vendor/free-unranked" {
		t.Fatalf("free-first broken: %v", ids(out))
	}
}

// TestOrderOpenRouterRankedDatedSlugMatch keeps the normalizeSlug join working:
// a model whose ranking row is a dated canonical slug still gets its real rank.
func TestOrderOpenRouterRankedDatedSlugMatch(t *testing.T) {
	out := []Model{
		{ID: "vendor/x", Name: "X", Base: OpenRouterBase, CanonicalSlug: "vendor/x-20260731"},
		{ID: "vendor/y", Name: "Y", Base: OpenRouterBase},
	}
	rank := map[string]rankingEntry{
		"vendor/x": {tokens: 50}, // stored under the normalized (undated) key
		"vendor/y": {tokens: 70},
	}
	orderOpenRouterRanked(out, rank)
	if out[0].ID != "vendor/y" {
		t.Fatalf("dated-slug ranking lost: %v", ids(out))
	}
}

func ids(ms []Model) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}
