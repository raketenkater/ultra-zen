package models

import "testing"

// searchCatalog is a cross-provider catalog shaped like the real one: the same
// model reachable through OpenRouter (priced), the Zen go tier (credit-billed,
// no published price) and a free variant, plus unrelated models to prove the
// matcher discriminates.
func searchCatalog() []Route {
	return []Route{
		{Provider: "openrouter", Model: Model{ID: "z-ai/glm-5.2", Name: "Z.AI: GLM 5.2", ContextLength: 200000,
			Price: Price{PromptUSD: 0.6, CompletionUSD: 2.2, Known: true}}},
		{Provider: "opencode-go", Model: Model{ID: "glm-5.2", Name: "GLM 5.2", ContextLength: 200000}},
		{Provider: "openrouter", Model: Model{ID: "z-ai/glm-5.2:free", Name: "Z.AI: GLM 5.2 (free)", Free: true,
			ContextLength: 131072, Price: Price{Known: true}}},
		{Provider: "openrouter", Model: Model{ID: "deepseek/deepseek-v4-flash", Name: "DeepSeek: DeepSeek V4 Flash",
			ContextLength: 1048576, Price: Price{PromptUSD: 0.04, CompletionUSD: 0.5, Known: true}}},
		{Provider: "opencode-go", Model: Model{ID: "deepseek-v4-flash", Name: "Deepseek V4 Flash", ContextLength: 1000000}},
		{Provider: "groq", Model: Model{ID: "llama-4-70b", Name: "Llama 4 70B", Free: true, ContextLength: 131072}},
	}
}

// TestSearchFindsModelAcrossProviders is the headline criterion: one typed
// query returns every provider that can serve the model, merged into a single
// group rather than scattered across unrelated hits.
func TestSearchFindsModelAcrossProviders(t *testing.T) {
	groups := Search("glm-5.2", searchCatalog(), 0)
	if len(groups) == 0 {
		t.Fatal("no groups for \"glm-5.2\"")
	}
	g := groups[0]
	if g.Title != "Z.AI: GLM 5.2" && g.Title != "GLM 5.2" {
		t.Errorf("top group title = %q, want a GLM 5.2 name", g.Title)
	}
	if len(g.Routes) != 3 {
		t.Fatalf("top group has %d routes, want 3 (openrouter paid, openrouter free, opencode-go)", len(g.Routes))
	}
	seen := map[string]bool{}
	for _, r := range g.Routes {
		seen[r.Provider+" "+r.Model.ID] = true
	}
	for _, want := range []string{"openrouter z-ai/glm-5.2", "openrouter z-ai/glm-5.2:free", "opencode-go glm-5.2"} {
		if !seen[want] {
			t.Errorf("route %q missing from the group", want)
		}
	}
}

// TestSearchOrdersRoutesFreeThenCheapest pins the ordering the user asked
// for: free first, then by published input price, with credit-billed routes
// (no published price) after the priced ones.
func TestSearchOrdersRoutesFreeThenCheapest(t *testing.T) {
	groups := Search("glm", searchCatalog(), 0)
	if len(groups) == 0 {
		t.Fatal("no groups for \"glm\"")
	}
	routes := groups[0].Routes
	if !routes[0].Model.Free {
		t.Errorf("first route = %s (%s), want the free one", routes[0].Model.ID, routes[0].Provider)
	}
	if routes[1].Model.ID != "z-ai/glm-5.2" {
		t.Errorf("second route = %s, want the priced openrouter route", routes[1].Model.ID)
	}
	if routes[2].Provider != "opencode-go" {
		t.Errorf("last route = %s, want the unpriced credit-billed route last", routes[2].Provider)
	}
	if routes[2].Model.Price.Known {
		t.Error("the Zen route reported a published price; it has none")
	}
}

// TestSearchFuzzyQueries covers the criterion the user named by example:
// "glm5", "glm 5" and "v4-flash" all have to find the right model without
// reproducing any gateway's exact id spelling.
func TestSearchFuzzyQueries(t *testing.T) {
	cases := map[string]string{
		"glm5":      "glm52",
		"glm 5":     "glm52",
		"GLM-5.2":   "glm52",
		"v4-flash":  "deepseekv4flash",
		"v4flash":   "deepseekv4flash",
		"deepseek":  "deepseekv4flash",
		"dsv4flash": "deepseekv4flash",
		"llama":     "llama470b",
	}
	cat := searchCatalog()
	for query, wantKey := range cases {
		groups := Search(query, cat, 0)
		if len(groups) == 0 {
			t.Errorf("%q matched nothing", query)
			continue
		}
		if groups[0].Key != wantKey {
			t.Errorf("%q -> top group %q, want %q", query, groups[0].Key, wantKey)
		}
	}
}

// TestSearchRejectsNonMatches proves the matcher discriminates: a loose
// subsequence tier that matched everything would make the search useless.
func TestSearchRejectsNonMatches(t *testing.T) {
	for _, query := range []string{"mistral", "zzzz", "gpt4o"} {
		if groups := Search(query, searchCatalog(), 0); len(groups) != 0 {
			t.Errorf("%q matched %d groups, want none (top: %q)", query, len(groups), groups[0].Key)
		}
	}
}

// TestSearchEmptyQueryListsEverything keeps a blank search box showing the
// catalog rather than an empty screen.
func TestSearchEmptyQueryListsEverything(t *testing.T) {
	groups := Search("", searchCatalog(), 0)
	if len(groups) != 3 {
		t.Fatalf("empty query -> %d groups, want 3 distinct models", len(groups))
	}
	total := 0
	for _, g := range groups {
		total += len(g.Routes)
	}
	if total != len(searchCatalog()) {
		t.Errorf("empty query covered %d routes, want all %d", total, len(searchCatalog()))
	}
}

// TestSearchLimitCaps checks the cap the picker applies so a two-letter query
// cannot flood the screen.
func TestSearchLimitCaps(t *testing.T) {
	if groups := Search("", searchCatalog(), 2); len(groups) != 2 {
		t.Fatalf("limit 2 -> %d groups, want 2", len(groups))
	}
}

// TestMatchRanksExactAboveFuzzy pins the score ordering, which is what puts
// the model the user actually typed at the top of the results.
func TestMatchRanksExactAboveFuzzy(t *testing.T) {
	exact := Match("z-ai/glm-5.2", "z-ai/glm-5.2", "Z.AI: GLM 5.2")
	bare := Match("glm-5.2", "z-ai/glm-5.2", "Z.AI: GLM 5.2")
	prefix := Match("glm", "z-ai/glm-5.2", "Z.AI: GLM 5.2")
	sub := Match("gl52", "z-ai/glm-5.2", "Z.AI: GLM 5.2")
	if !(exact > bare && bare > prefix && prefix > sub) {
		t.Errorf("score order broken: exact=%d bare=%d prefix=%d subsequence=%d", exact, bare, prefix, sub)
	}
	if sub < 0 {
		t.Error("subsequence query \"gl52\" did not match glm-5.2")
	}
	if got := Match("mistral", "z-ai/glm-5.2", "Z.AI: GLM 5.2"); got >= 0 {
		t.Errorf("Match(mistral) = %d, want negative", got)
	}
}

// TestGroupCheapestIgnoresUnpriced is the group-level half of the no-invented
// -costs rule: a credit-billed route must not be reported as the cheapest at
// $0.00.
func TestGroupCheapestIgnoresUnpriced(t *testing.T) {
	groups := Search("deepseek", searchCatalog(), 0)
	if len(groups) == 0 {
		t.Fatal("no deepseek group")
	}
	p, ok := groups[0].Cheapest()
	if !ok {
		t.Fatal("deepseek group reported no cheapest price; the openrouter route has one")
	}
	if p.PromptUSD != 0.04 {
		t.Errorf("cheapest = %v $/M, want 0.04 (not the unpriced Zen route at 0)", p.PromptUSD)
	}
}

// TestIdentityMergesFreeAndVendorVariants covers the merge key directly, since
// every grouping result depends on it.
func TestIdentityMergesFreeAndVendorVariants(t *testing.T) {
	want := identity("z-ai/glm-5.2")
	for _, id := range []string{"glm-5.2", "z-ai/glm-5.2:free", "GLM_5_2", "glm-5.2-free"} {
		if got := identity(id); got != want {
			t.Errorf("identity(%q) = %q, want %q", id, got, want)
		}
	}
	if identity("glm-5.1") == want {
		t.Error("identity merged glm-5.1 into glm-5.2")
	}
}
