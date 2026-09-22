package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/raketenkater/ultra-zen/internal/models"
)

// searchTestRoutes is a cross-provider catalog: one model reachable three
// ways (priced OpenRouter, free OpenRouter, credit-billed Zen), one reachable
// twice, and an unrelated model to prove the query discriminates.
func searchTestRoutes() []models.Route {
	return []models.Route{
		{Provider: "openrouter", Model: models.Model{ID: "z-ai/glm-5.2", Name: "Z.AI: GLM 5.2", ContextLength: 204800,
			Price: models.Price{PromptUSD: 0.6, CompletionUSD: 2.2, Known: true}}},
		{Provider: "opencode-go", Model: models.Model{ID: "glm-5.2", Name: "GLM 5.2", ContextLength: 204800}},
		{Provider: "openrouter", Model: models.Model{ID: "z-ai/glm-5.2:free", Name: "Z.AI: GLM 5.2 (free)",
			Free: true, ContextLength: 131072, Price: models.Price{Known: true}}},
		{Provider: "openrouter", Model: models.Model{ID: "deepseek/deepseek-v4-flash", Name: "DeepSeek V4 Flash",
			ContextLength: 1048576, Price: models.Price{PromptUSD: 0.04, CompletionUSD: 0.5, Known: true}}},
		{Provider: "groq", Model: models.Model{ID: "llama-4-70b", Name: "Llama 4 70B", Free: true, ContextLength: 131072}},
	}
}

// typeQuery feeds a query into the screen one rune at a time, the way a user
// does — the rebuild runs per keystroke, so a bug that only shows on the
// second character would survive a single SetValue.
func typeQuery(t *testing.T, m *searchManager, q string) {
	t.Helper()
	for _, r := range q {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		*m = *(updated.(*searchManager))
	}
}

// settleCatalogs delivers every provider's full catalog, putting the screen
// in the state it reaches once the network has answered. Providers absent
// from the fixture report an empty catalog, which is what a real install with
// no key for them produces.
func settleCatalogs(m *searchManager, extra ...models.Route) {
	byProvider := map[string][]models.Model{}
	for _, r := range append(append([]models.Route{}, searchTestRoutes()...), extra...) {
		byProvider[r.Provider] = append(byProvider[r.Provider], r.Model)
	}
	for _, p := range poolProviders {
		updated, _ := m.Update(searchCatalogLoaded{provider: p, models: byProvider[p]})
		*m = *(updated.(*searchManager))
	}
}

func rowTitles(m *searchManager) []string {
	var out []string
	for _, item := range m.list.Items() {
		out = append(out, item.(tailer).Title())
	}
	return out
}

// TestSearchListsEveryProviderForATypedModel is the headline behaviour: type
// a name, get every route to it, grouped under one heading.
func TestSearchListsEveryProviderForATypedModel(t *testing.T) {
	m := newSearchManager(searchTestRoutes(), "")
	typeQuery(t, &m, "glm5")

	var heads, routes int
	providers := map[string]bool{}
	for _, item := range m.list.Items() {
		switch row := item.(type) {
		case searchGroupRow:
			heads++
		case searchRouteRow:
			routes++
			providers[row.provider+" "+row.model.ID] = true
		}
	}
	if heads != 1 {
		t.Fatalf("got %d result headings for \"glm5\", want 1: %v", heads, rowTitles(&m))
	}
	if routes != 3 {
		t.Fatalf("got %d routes, want 3: %v", routes, rowTitles(&m))
	}
	for _, want := range []string{"openrouter z-ai/glm-5.2", "openrouter z-ai/glm-5.2:free", "opencode-go glm-5.2"} {
		if !providers[want] {
			t.Errorf("route %q missing: %v", want, rowTitles(&m))
		}
	}
}

// TestSearchRouteRowsShowRealCosts checks the cost column carries the
// published rate for a priced route and the credits word for a gateway that
// publishes none — never a number ultra-zen made up.
func TestSearchRouteRowsShowRealCosts(t *testing.T) {
	m := newSearchManager(searchTestRoutes(), "")
	typeQuery(t, &m, "glm5")

	tails := map[string]string{}
	for _, item := range m.list.Items() {
		if row, ok := item.(searchRouteRow); ok {
			tails[row.provider+" "+row.model.ID] = strings.Join(row.tailParts(), " ")
		}
	}
	if got := tails["openrouter z-ai/glm-5.2"]; !strings.Contains(got, "$0.600/M") {
		t.Errorf("priced route tail = %q, want the published $/M rate", got)
	}
	if got := tails["opencode-go glm-5.2"]; !strings.Contains(got, "credits") {
		t.Errorf("unpriced route tail = %q, want \"credits\"", got)
	}
	if got := tails["opencode-go glm-5.2"]; strings.Contains(got, "$") {
		t.Errorf("unpriced route tail = %q, must not invent a price", got)
	}
	if got := tails["openrouter z-ai/glm-5.2:free"]; !strings.Contains(got, "free") {
		t.Errorf("free route tail = %q, want \"free\"", got)
	}
}

// TestSearchEnterLaunchesTheSelectedRoute covers the launchable criterion:
// Enter returns the exact provider and model id under the cursor.
func TestSearchEnterLaunchesTheSelectedRoute(t *testing.T) {
	m := newSearchManager(searchTestRoutes(), "")
	typeQuery(t, &m, "deepseek")

	row, ok := m.selectedRoute()
	if !ok {
		t.Fatalf("cursor is not on a route: %v", rowTitles(&m))
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	sm := updated.(*searchManager)
	if !sm.done {
		t.Fatal("Enter on a route did not finish the screen")
	}
	if sm.choice != row.model.ID || sm.provider != row.provider {
		t.Fatalf("Enter returned %s via %s, want %s via %s", sm.choice, sm.provider, row.model.ID, row.provider)
	}
	if sm.choice != "deepseek/deepseek-v4-flash" || sm.provider != "openrouter" {
		t.Errorf("chose %s via %s, want deepseek/deepseek-v4-flash via openrouter", sm.choice, sm.provider)
	}
}

// TestSearchCursorStartsAndStaysOnLaunchableRows proves headings and cost
// lines cannot be selected: Enter must never land on a row it cannot act on.
func TestSearchCursorStartsAndStaysOnLaunchableRows(t *testing.T) {
	m := newSearchManager(searchTestRoutes(), "")
	typeQuery(t, &m, "glm")

	if _, ok := m.selectedRoute(); !ok {
		t.Fatalf("initial cursor is inert (%T), want a route", m.list.SelectedItem())
	}
	// Walk the whole list downward; every stop must be launchable.
	for i := 0; i < len(m.list.Items())+2; i++ {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = *(updated.(*searchManager))
		if isInert(m.list.SelectedItem()) {
			t.Fatalf("cursor stopped on an inert %T after %d downs", m.list.SelectedItem(), i+1)
		}
	}
}

// TestSearchEnterOnHeadingDoesNothing is the other half: if the cursor is
// forced onto an inert row, Enter must not invent a choice.
func TestSearchEnterOnHeadingDoesNothing(t *testing.T) {
	m := newSearchManager(searchTestRoutes(), "")
	typeQuery(t, &m, "glm")

	idx := -1
	for i, item := range m.list.Items() {
		if _, ok := item.(searchGroupRow); ok {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("no heading row to test")
	}
	m.list.Select(idx)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	sm := updated.(*searchManager)
	if sm.choice != "" {
		t.Errorf("Enter on a heading chose %q, want nothing", sm.choice)
	}
	if sm.done {
		t.Error("Enter on a heading closed the screen")
	}
}

// TestSearchShowsUpstreamCostsCheapestFirst covers the per-provider breakdown:
// once the fetch lands, every upstream is listed under the OpenRouter route,
// cheapest first, with a summary naming the spread.
func TestSearchShowsUpstreamCostsCheapestFirst(t *testing.T) {
	m := newSearchManager(searchTestRoutes(), "")
	typeQuery(t, &m, "deepseek")
	m.syncExpansion()
	if m.expanded != "deepseek/deepseek-v4-flash" {
		t.Fatalf("expanded = %q, want the selected openrouter model", m.expanded)
	}

	updated, _ := m.Update(searchEndpointsLoaded{
		modelID: "deepseek/deepseek-v4-flash",
		eps: []models.Endpoint{
			{Provider: "OpenInference", ContextLength: 1048576, Quantization: "fp8", Uptime30m: 99.9,
				Price: models.Price{PromptUSD: 0.04, CompletionUSD: 0.5, Known: true}},
			{Provider: "DeepInfra", ContextLength: 1048576, Quantization: "fp8", Uptime30m: 99.6,
				Price: models.Price{PromptUSD: 0.09, CompletionUSD: 0.18, Known: true}},
			{Provider: "Azure", ContextLength: 1048576, Uptime30m: 98.2,
				Price: models.Price{PromptUSD: 0.21, CompletionUSD: 0.56, Known: true}},
		},
	})
	m = *(updated.(*searchManager))

	var eps []searchEndpointRow
	for _, item := range m.list.Items() {
		if row, ok := item.(searchEndpointRow); ok {
			eps = append(eps, row)
		}
	}
	if len(eps) != 3 {
		t.Fatalf("got %d upstream rows, want 3: %v", len(eps), rowTitles(&m))
	}
	want := []string{"OpenInference", "DeepInfra", "Azure"}
	for i, name := range want {
		if eps[i].ep.Provider != name {
			t.Fatalf("upstream order = %v, want %v", endpointNames(eps), want)
		}
	}
	tail := strings.Join(eps[0].tailParts(), " ")
	for _, want := range []string{"$0.040/M", "out $0.500/M", "1024k", "fp8", "100%"} {
		if !strings.Contains(tail, want) {
			t.Errorf("cheapest upstream tail = %q, missing %q", tail, want)
		}
	}
	if !hasNoteContaining(&m, "5.2x spread") {
		t.Errorf("no spread summary among rows: %v", rowTitles(&m))
	}
}

// TestSearchStatesAFailedCostLookup keeps a broken fetch visible. Silently
// dropping the breakdown would read as "this model has one provider", which
// is a claim about price the screen has not earned.
func TestSearchStatesAFailedCostLookup(t *testing.T) {
	m := newSearchManager(searchTestRoutes(), "")
	typeQuery(t, &m, "deepseek")
	m.syncExpansion()

	updated, _ := m.Update(searchEndpointsLoaded{
		modelID: "deepseek/deepseek-v4-flash",
		err:     errors.New("Get \"https://openrouter.ai/...\": dial tcp: no route to host"),
	})
	m = *(updated.(*searchManager))

	if !hasNoteContaining(&m, "unavailable") {
		t.Fatalf("a failed cost fetch left no trace: %v", rowTitles(&m))
	}
	for _, item := range m.list.Items() {
		if _, ok := item.(searchEndpointRow); ok {
			t.Fatal("a failed fetch produced upstream rows")
		}
	}
}

// TestSearchFetchesEachModelOnce pins the deduplication around the cost
// fetch. Typing issues it as soon as a result is selected — the breakdown
// loads while the user is still typing rather than after a second keypress —
// which makes the guards load-bearing: every further keystroke and cursor
// move re-enters syncExpansion, and without them a five-letter query would
// fire five identical requests at the endpoints API.
func TestSearchFetchesEachModelOnce(t *testing.T) {
	m := newSearchManager(searchTestRoutes(), "")
	typeQuery(t, &m, "deepseek")

	const id = "deepseek/deepseek-v4-flash"
	if !m.pending[id] {
		t.Fatalf("typing issued no cost fetch; pending = %v", m.pending)
	}
	// In flight: no duplicate, however often the selection is re-synced.
	if cmd := m.syncExpansion(); cmd != nil {
		t.Error("re-syncing an in-flight selection issued a second fetch")
	}
	// Landed: cached, and not re-fetched even when the row is re-expanded.
	updated, _ := m.Update(searchEndpointsLoaded{modelID: id, eps: []models.Endpoint{
		{Provider: "DeepInfra", Price: models.Price{PromptUSD: 0.09, Known: true}, Uptime30m: -1},
	}})
	m = *(updated.(*searchManager))
	if m.pending[id] {
		t.Error("pending flag survived the result landing")
	}
	m.expanded = ""
	if cmd := m.syncExpansion(); cmd != nil {
		t.Error("a cached model was fetched again")
	}
	if len(m.endpoints[id]) != 1 {
		t.Errorf("cached endpoints = %d, want 1", len(m.endpoints[id]))
	}
}

// TestSearchFailedFetchIsNotRetriedOnEveryKeystroke keeps a provider that is
// down from turning the screen into a retry loop: the failure is remembered,
// stated once, and not re-requested while the query stands.
func TestSearchFailedFetchIsNotRetriedOnEveryKeystroke(t *testing.T) {
	m := newSearchManager(searchTestRoutes(), "")
	typeQuery(t, &m, "deepseek")
	updated, _ := m.Update(searchEndpointsLoaded{
		modelID: "deepseek/deepseek-v4-flash",
		err:     errors.New("dial tcp: no route to host"),
	})
	m = *(updated.(*searchManager))
	m.expanded = ""
	if cmd := m.syncExpansion(); cmd != nil {
		t.Error("a failed cost lookup was retried on the next sync")
	}
}

// TestSearchNoMatchWhileLoadingDoesNotClaimAbsence covers the honesty rule on
// the empty state: with catalogs still arriving, ultra-zen has not
// established that nothing matches and must not say so.
func TestSearchNoMatchWhileLoadingDoesNotClaimAbsence(t *testing.T) {
	m := newSearchManager(searchTestRoutes(), "")
	// Deliberately do NOT settle: catalogs are still in flight.
	typeQuery(t, &m, "mistral")

	if hasNoteContaining(&m, "no model matches") {
		t.Error("claimed a definitive no-match while catalogs were still loading")
	}
	if !hasNoteContaining(&m, "still loading") {
		t.Errorf("did not say catalogs are still loading: %v", rowTitles(&m))
	}
}

// TestSearchNoMatchSaysSo keeps an empty result explicit rather than blank —
// but only once every catalog has answered, so the claim is earned.
func TestSearchNoMatchSaysSo(t *testing.T) {
	m := newSearchManager(searchTestRoutes(), "")
	settleCatalogs(&m)
	typeQuery(t, &m, "mistral")

	for _, item := range m.list.Items() {
		if _, ok := item.(searchRouteRow); ok {
			t.Fatal("\"mistral\" produced launchable routes")
		}
	}
	if !hasNoteContaining(&m, "mistral") {
		t.Errorf("no-match note does not name the query: %v", rowTitles(&m))
	}
}

// TestSearchEscReturnsWithoutAChoice covers backing out.
func TestSearchEscReturnsWithoutAChoice(t *testing.T) {
	m := newSearchManager(searchTestRoutes(), "")
	typeQuery(t, &m, "glm")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	sm := updated.(*searchManager)
	if !sm.done {
		t.Fatal("esc did not close the screen")
	}
	if sm.choice != "" || sm.quit {
		t.Errorf("esc returned choice=%q quit=%v, want a plain return", sm.choice, sm.quit)
	}
}

// TestSearchTypingLettersIsQueryNotBindings is the reason the screen owns all
// input: "s", "k", "f" and "p" are picker bindings on the main screen and
// must be plain text here.
func TestSearchTypingLettersIsQueryNotBindings(t *testing.T) {
	m := newSearchManager(searchTestRoutes(), "")
	typeQuery(t, &m, "kfps")
	if got := m.input.Value(); got != "kfps" {
		t.Fatalf("input = %q, want \"kfps\" — a binding swallowed a keystroke", got)
	}
}

func endpointNames(rows []searchEndpointRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ep.Provider
	}
	return out
}

func hasNoteContaining(m *searchManager, want string) bool {
	for _, item := range m.list.Items() {
		if row, ok := item.(searchNoteRow); ok {
			if strings.Contains(row.Title()+" "+row.tail, want) {
				return true
			}
		}
	}
	return false
}

// TestPickerSearchPickBecomesTheLaunch closes the loop the unit tests leave
// open: opening the search screen from the picker, choosing a route there and
// having that exact provider+model come back as the picker's decision. The
// screen-level tests prove the screen returns a choice; this proves the
// picker acts on it.
func TestPickerSearchPickBecomesTheLaunch(t *testing.T) {
	m := newCatalogTestModel()
	m.freePool = []FreeRoute{{Provider: "openrouter", Model: "vendor/model:free"}}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(model)
	if m.search == nil || m.step != stepSearch {
		t.Fatal("\"s\" did not open the search screen")
	}
	// The screen searches whatever the picker has discovered so far.
	if len(m.search.routes) == 0 {
		t.Fatal("search opened over an empty catalog")
	}

	// Drive a real pick through the screen, then hand the message to the
	// picker exactly as the event loop would.
	m.search.input.SetValue("model")
	m.search.rebuild()
	row, ok := m.search.selectedRoute()
	if !ok {
		t.Fatalf("no launchable route for \"model\": %v", rowTitles(m.search))
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)

	if m.choice != row.model.ID || m.choiceVia != row.provider {
		t.Fatalf("picker took %q via %q, want %q via %q", m.choice, m.choiceVia, row.model.ID, row.provider)
	}
	if cmd == nil {
		t.Error("a search pick did not quit the picker")
	}
	if m.search != nil {
		t.Error("the search screen stayed open after a pick")
	}
	// A searched model is a deliberate pick, so it must not silently inherit
	// the saved rotation pool as fallbacks — same rule as Enter on a catalog
	// row.
	if m.freePool != nil {
		t.Errorf("freePool survived a search pick: %v", m.freePool)
	}
}

// TestPickerSearchEscReturnsToTheCatalog covers backing out of the screen.
func TestPickerSearchEscReturnsToTheCatalog(t *testing.T) {
	m := newCatalogTestModel()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)

	if m.search != nil {
		t.Error("esc left the search screen open")
	}
	if m.step != stepCombo {
		t.Errorf("step = %v after esc, want the catalog screen", m.step)
	}
	if m.choice != "" {
		t.Errorf("esc produced a choice %q", m.choice)
	}
}

// TestSearchFindsModelsOutsideThePickersNarrowedList is the regression test
// for the bug that prompted the full-catalog load: a paid OpenRouter model
// outside the picker's top-hundred cap (xiaomi/mimo-v2.6-pro was the reported
// case) was unfindable, because the search reused the list the picker had
// already truncated for display. A search that answers "no model matches" for
// a model the provider plainly serves is worse than no search.
func TestSearchFindsModelsOutsideThePickersNarrowedList(t *testing.T) {
	// The seed is the picker's narrowed view: it does NOT contain mimo.
	m := newSearchManager(searchTestRoutes(), "")
	typeQuery(t, &m, "mimo")
	for _, item := range m.list.Items() {
		if _, ok := item.(searchRouteRow); ok {
			t.Fatal("fixture is wrong: mimo must not be in the seeded list")
		}
	}

	// The full catalog lands, carrying the model the picker had capped away.
	mimo := models.Route{Provider: "openrouter", Model: models.Model{
		ID: "xiaomi/mimo-v2.6-pro", Name: "Xiaomi: MiMo-V2.6-Pro", ContextLength: 262144,
		Price: models.Price{PromptUSD: 0.435, CompletionUSD: 1.3, Known: true}}}
	settleCatalogs(&m, mimo)

	var found *searchRouteRow
	for _, item := range m.list.Items() {
		if row, ok := item.(searchRouteRow); ok && row.model.ID == mimo.Model.ID {
			r := row
			found = &r
		}
	}
	if found == nil {
		t.Fatalf("mimo still not found after the full catalog landed: %v", rowTitles(&m))
	}
	if found.provider != "openrouter" {
		t.Errorf("found via %q, want openrouter", found.provider)
	}
}

// TestSearchFullCatalogReplacesTheSeed checks the merge rule: once a provider
// reports its real catalog, that catalog is authoritative. Keeping the seeded
// rows as well would resurrect models the full fetch deliberately dropped.
func TestSearchFullCatalogReplacesTheSeed(t *testing.T) {
	m := newSearchManager(searchTestRoutes(), "")
	// groq reports a catalog that no longer carries llama-4-70b.
	updated, _ := m.Update(searchCatalogLoaded{provider: "groq", models: []models.Model{
		{ID: "llama-5-8b", Name: "Llama 5 8B", Free: true},
	}})
	m = *(updated.(*searchManager))

	for _, r := range m.routes {
		if r.Provider == "groq" && r.Model.ID == "llama-4-70b" {
			t.Fatal("a seeded groq model survived its provider's full catalog")
		}
	}
	var haveNew bool
	for _, r := range m.routes {
		if r.Provider == "groq" && r.Model.ID == "llama-5-8b" {
			haveNew = true
		}
	}
	if !haveNew {
		t.Error("the freshly loaded groq model is missing from the routes")
	}
	// Providers that have not reported still contribute their seeded rows.
	var haveSeeded bool
	for _, r := range m.routes {
		if r.Provider == "openrouter" && r.Model.ID == "z-ai/glm-5.2" {
			haveSeeded = true
		}
	}
	if !haveSeeded {
		t.Error("an unreported provider lost its seeded rows")
	}
}

// TestSearchScopeLineStatesWhatWasNotSearched keeps the screen honest about
// its own coverage: a provider with no key or a failed fetch is named, so a
// missing model never silently reads as "not available anywhere".
func TestSearchScopeLineStatesWhatWasNotSearched(t *testing.T) {
	m := newSearchManager(searchTestRoutes(), "")
	if got := m.scopeLine(); !strings.Contains(got, "loading") {
		t.Errorf("scope line before any catalog = %q, want a loading state", got)
	}
	for _, p := range poolProviders {
		msg := searchCatalogLoaded{provider: p, models: nil}
		if p == "cohere" {
			msg.keyless = true
		}
		if p == "saia" {
			msg.err = errors.New("dial tcp: no route to host")
		}
		updated, _ := m.Update(msg)
		m = *(updated.(*searchManager))
	}
	got := m.scopeLine()
	if !strings.Contains(got, "not searched") {
		t.Fatalf("scope line = %q, want it to name the unsearched providers", got)
	}
	for _, want := range []string{"cohere", "saia"} {
		if !strings.Contains(got, want) {
			t.Errorf("scope line = %q, missing %q", got, want)
		}
	}
}
