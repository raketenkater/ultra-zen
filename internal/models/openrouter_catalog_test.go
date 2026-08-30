package models

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeOpenRouter serves the two endpoints ListOpenRouterRanked consumes: the
// OpenAI-shaped /models catalog and the rankings-daily dataset. Counts are
// exposed so tests can assert exactly which endpoints were hit.
type fakeOpenRouter struct {
	models    []map[string]any // raw /models entries
	rankRows  []map[string]any // rankings-daily rows
	catalog   int              // /models requests
	rankings  int              // /datasets/rankings-daily requests
	rankDown  bool             // serve 500 on the rankings endpoint
	modelDown bool             // serve 500 on the models endpoint
}

func (f *fakeOpenRouter) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/models":
			f.catalog++
			if f.modelDown {
				http.Error(w, "down", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"data": f.models})
		case r.URL.Path == "/datasets/rankings-daily":
			f.rankings++
			if f.rankDown {
				http.Error(w, "down", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"data": f.rankRows})
		default:
			http.NotFound(w, r)
		}
	}))
}

func freeEntry(id string) map[string]any {
	return map[string]any{"id": id, "canonical_slug": id}
}

func paidEntry(id, slug string) map[string]any {
	return map[string]any{"id": id, "canonical_slug": slug}
}

func rankRow(permaslug string, tokens int64) map[string]any {
	return map[string]any{"model_permaslug": permaslug, "total_tokens": fmt.Sprint(tokens)}
}

// TestCapOpenRouterPickerKeepsWholeFreeSet pins the user requirement: the
// default OpenRouter picker view must keep EVERY free model (:free suffix and
// openrouter/free), no matter how large the free set grows, while the paid
// weekly-top block is capped so the picker stays usable.
func TestCapOpenRouterPickerKeepsWholeFreeSet(t *testing.T) {
	const freeCount = 150 // larger than the old flat top-100 cap
	var ranked []Model
	for i := 0; i < freeCount; i++ {
		ranked = append(ranked, Model{ID: fmt.Sprintf("vendor/free-%03d", i), Name: fmt.Sprintf("Free %03d", i), Free: true})
	}
	for i := 0; i < freeCount+50; i++ { // 200 paid, well past the cap
		ranked = append(ranked, Model{ID: fmt.Sprintf("vendor/paid-%03d", i), Name: fmt.Sprintf("Paid %03d", i)})
	}

	capped := CapOpenRouterPicker(ranked)
	freeKept, paidKept := 0, 0
	for i, m := range capped {
		if m.Free {
			freeKept++
			// Free block must be a prefix: no paid model before a free one.
			if i >= freeKept {
				t.Fatalf("free model %q at index %d after paid models (free-first broken)", m.ID, i)
			}
		} else {
			paidKept++
		}
	}
	if freeKept != freeCount {
		t.Fatalf("free set truncated: kept %d of %d free models", freeKept, freeCount)
	}
	if paidKept != openRouterPickerCap {
		t.Fatalf("paid block = %d, want capped at %d", paidKept, openRouterPickerCap)
	}
}

// TestCapOpenRouterPickerPassthrough verifies small catalogs come back
// unchanged (no cap applied, same slice) and an already-conforming input is
// not re-sliced.
func TestCapOpenRouterPickerPassthrough(t *testing.T) {
	small := []Model{
		{ID: "a/free", Name: "A Free", Free: true},
		{ID: "a/paid", Name: "A Paid"},
	}
	if got := CapOpenRouterPicker(small); len(got) != 2 {
		t.Fatalf("small catalog changed: %+v", got)
	}
	// Free-only catalog never capped regardless of size.
	var freeOnly []Model
	for i := 0; i < 500; i++ {
		freeOnly = append(freeOnly, Model{ID: fmt.Sprintf("f/%d", i), Free: true})
	}
	if got := CapOpenRouterPicker(freeOnly); len(got) != 500 {
		t.Fatalf("free-only catalog capped: %d models", len(got))
	}
}

// TestListOpenRouterRankedFreeSetComplete drives the full ranked path against
// a fake gateway whose free set exceeds the paid cap, asserting (a) every
// :free model survives the picker cap, (b) the weekly ranking orders the free
// block by usage, and (c) the paid block follows, capped.
func TestListOpenRouterRankedFreeSetComplete(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	resetRankingClock := orRankingClock
	orRankingClock = func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) }
	defer func() { orRankingClock = resetRankingClock }()

	fake := &fakeOpenRouter{}
	// 120 free models (cap is 100) with usage ranks, 30 paid models with ranks.
	for i := 0; i < 120; i++ {
		id := fmt.Sprintf("vendor/free-%03d:free", i)
		fake.models = append(fake.models, freeEntry(id))
		fake.rankRows = append(fake.rankRows, rankRow(id, int64(1000-i)))
	}
	fake.models = append(fake.models, freeEntry("openrouter/free"))
	fake.rankRows = append(fake.rankRows, rankRow("openrouter/free", 99999))
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("vendor/paid-%03d", i)
		fake.models = append(fake.models, paidEntry(id, id))
		fake.rankRows = append(fake.rankRows, rankRow(id, int64(500-i)))
	}
	srv := fake.server()
	defer srv.Close()

	ranked, err := listOpenRouterRankedAt(srv.URL, &http.Client{Timeout: 5 * time.Second}, "key")
	if err != nil {
		t.Fatalf("listOpenRouterRankedAt: %v", err)
	}
	capped := CapOpenRouterPicker(ranked)

	// (a) every free model present, first in the list, usage-ordered.
	freeSeen := 0
	for i, m := range capped {
		if !m.Free {
			break
		}
		if i == 0 && m.ID != "openrouter/free" {
			t.Fatalf("top free model = %q, want the highest-volume openrouter/free", m.ID)
		}
		freeSeen++
	}
	if freeSeen != 121 { // 120 :free + the router
		t.Fatalf("free block = %d models, want all 121 (cap must not truncate the free tier)", freeSeen)
	}
	// First two free models are the top-two by weekly tokens.
	if capped[1].ID != "vendor/free-000:free" {
		t.Fatalf("second row = %q, want usage-ranked vendor/free-000:free", capped[1].ID)
	}
	// (b) paid block capped at 100 of the 30... (30 < 100, all kept) and
	// ordered by usage.
	firstPaid := -1
	for i, m := range capped {
		if !m.Free {
			firstPaid = i
			break
		}
	}
	if capped[firstPaid].ID != "vendor/paid-000" {
		t.Fatalf("top paid = %q, want usage-ranked vendor/paid-000", capped[firstPaid].ID)
	}
	if got := len(capped) - firstPaid; got != 30 {
		t.Fatalf("paid block = %d, want 30 (under cap, uncapped)", got)
	}
	// (c) the ranking fetch went through exactly once for this catalog (the
	// fake's counters double as a no-hidden-request check).
	if fake.rankings != 1 || fake.catalog != 1 {
		t.Fatalf("requests = catalog %d, rankings %d; want 1 each", fake.catalog, fake.rankings)
	}
}

// TestOpenRouterRankingTTLCache verifies the weekly-rank memo: a fresh fetch
// writes the cache, a call inside the TTL reads it without a second network
// request, and a call past the TTL re-fetches.
func TestOpenRouterRankingTTLCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	reset := orRankingClock
	orRankingClock = func() time.Time { return now }
	defer func() { orRankingClock = reset }()

	fake := &fakeOpenRouter{
		rankRows: []map[string]any{
			rankRow("vendor/a", 500),
			rankRow("vendor/b-20260731", 300), // dated permaslug normalizes
		},
	}
	srv := fake.server()
	defer srv.Close()
	client := &http.Client{Timeout: 5 * time.Second}

	// First call: live fetch (1 request), cached.
	r1 := fetchOpenRouterRankingAt(srv.URL, client, "key")
	if fake.rankings != 1 {
		t.Fatalf("rankings requests = %d, want 1", fake.rankings)
	}
	if len(r1) != 2 || r1["vendor/a"].tokens != 500 {
		t.Fatalf("rank = %+v", r1)
	}
	if r1["vendor/b"].tokens != 300 {
		t.Fatalf("dated permaslug not normalized: %+v", r1)
	}

	// Second call inside the TTL: served from cache, no new request.
	r2 := fetchOpenRouterRankingAt(srv.URL, client, "key")
	if fake.rankings != 1 {
		t.Fatalf("rankings requests = %d after cached read, want 1", fake.rankings)
	}
	if r2["vendor/a"].tokens != 500 {
		t.Fatalf("cached rank = %+v", r2)
	}

	// Third call past the TTL: re-fetches (2 requests).
	now = now.Add(openRouterRankingTTL + time.Minute)
	r3 := fetchOpenRouterRankingAt(srv.URL, client, "key")
	if fake.rankings != 2 {
		t.Fatalf("rankings requests = %d after TTL expiry, want 2", fake.rankings)
	}
	if r3["vendor/a"].tokens != 500 {
		t.Fatalf("refetched rank = %+v", r3)
	}
}

// TestOpenRouterRankingFallsBackToStaleCache pins the fallback-on-fetch-failure
// policy: when the live rankings endpoint fails, the last successful snapshot
// is served until it ages past the TTL (stale-but-ordered beats no order), and
// after the TTL the failure simply yields an unranked (empty) map.
func TestOpenRouterRankingFallsBackToStaleCache(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	reset := orRankingClock
	orRankingClock = func() time.Time { return now }
	defer func() { orRankingClock = reset }()

	fake := &fakeOpenRouter{
		rankRows: []map[string]any{rankRow("vendor/a", 500)},
	}
	srv := fake.server()
	defer srv.Close()
	client := &http.Client{Timeout: 5 * time.Second}

	// Seed the cache with a good snapshot.
	r1 := fetchOpenRouterRankingAt(srv.URL, client, "key")
	if len(r1) != 1 {
		t.Fatalf("seed rank = %+v", r1)
	}

	// Endpoint goes down; inside the TTL the stale snapshot is served.
	fake.rankDown = true
	r2 := fetchOpenRouterRankingAt(srv.URL, client, "key")
	if len(r2) != 1 || r2["vendor/a"].tokens != 500 {
		t.Fatalf("stale cache not served on fetch failure: %+v", r2)
	}

	// Past the TTL the stale copy expires: fetch fails, empty map (unranked).
	now = now.Add(openRouterRankingTTL + time.Minute)
	r3 := fetchOpenRouterRankingAt(srv.URL, client, "key")
	if len(r3) != 0 {
		t.Fatalf("expired cache must not be served: %+v", r3)
	}
}

// TestOpenRouterCatalogFetchFailureDoesNotReadStale verifies the /models
// catalog itself is never served stale from any cache (there is none): a
// failing catalog returns an error, so callers fall back to their own
// error paths instead of launching with a dead model list.
func TestOpenRouterCatalogFetchFailureDoesNotReadStale(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	fake := &fakeOpenRouter{modelDown: true}
	srv := fake.server()
	defer srv.Close()

	_, err := listOpenRouterRankedAt(srv.URL, &http.Client{Timeout: 5 * time.Second}, "key")
	if err == nil {
		t.Fatal("catalog fetch failure must surface an error, not an empty/stale list")
	}
	if !strings.Contains(err.Error(), "openrouter") {
		t.Fatalf("error should name the provider: %v", err)
	}
	// No rankings request may fire when the catalog itself failed.
	if fake.rankings != 0 {
		t.Fatalf("rankings requested %d times despite catalog failure", fake.rankings)
	}
}
