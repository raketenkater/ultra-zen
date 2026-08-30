package tui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/raketenkater/ultra-zen/internal/keys"
	"github.com/raketenkater/ultra-zen/internal/models"
	"github.com/raketenkater/ultra-zen/internal/proxy"
)

// TestFilterStateKeystrokesGoToFilter pins the Column's Phase-3 gate: while
// the filter prompt is active, k/f/esc must reach the filter input (typing
// "sk", or canceling the query) instead of opening screens. Outside
// filtering the bindings behave exactly as before.
func TestFilterStateKeystrokesGoToFilter(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := newTestModel()

	// "/" enters filtering mode.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	mm := updated.(model)
	if mm.list.FilterState() != list.Filtering {
		t.Fatal("pressing / did not enter filtering mode")
	}

	// "k" while filtering is a typed rune, not the key-manager binding.
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	mm = updated.(model)
	if mm.keys != nil || mm.step == stepKeys {
		t.Fatal("k opened the key manager while filtering")
	}
	if got := mm.list.FilterValue(); !strings.HasSuffix(got, "k") {
		t.Fatalf("filter value = %q, want it to contain the typed k", got)
	}

	// "f" while filtering is a typed rune, not the pool binding.
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	mm = updated.(model)
	if mm.fallbacks != nil {
		t.Fatal("f opened the pool editor while filtering")
	}

	// "p" while filtering is a typed rune, not the provider-filter binding.
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	mm = updated.(model)
	if !mm.filter.off() {
		t.Fatalf("p cycled the provider filter while filtering: %+v", mm.filter)
	}
	if got := mm.list.FilterValue(); !strings.HasSuffix(got, "p") {
		t.Fatalf("filter value = %q, want it to contain the typed p", got)
	}

	// Esc while filtering cancels the query instead of quitting the picker.
	updated, cmd := mm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm = updated.(model)
	if cmd != nil && mm.quit {
		t.Fatal("esc quit the picker while filtering")
	}
	if mm.list.FilterState() == list.Filtering {
		t.Fatal("esc did not exit filtering mode")
	}
}

// TestModelItemTitleUsesFriendlyName verifies the picker row renders the
// friendly Name (falling back to the id), so users identify models at a
// glance. Under the Column contract the title is the bare identity; tier and
// recency are tail parts.
func TestModelItemTitleUsesFriendlyName(t *testing.T) {
	// Friendly name differs from the id -> show the name; free tier moves to
	// the tail column.
	item := modelItem{m: models.Model{ID: "zai-org/GLM-5.2", Name: "GLM 5.2", Free: true}}
	if got := item.Title(); got != "GLM 5.2" {
		t.Fatalf("Title = %q, want friendly name", got)
	}
	if got := strings.Join(item.tailParts(), "  "); got != "free" {
		t.Fatalf("tailParts = %q, want free", got)
	}
	// ctx and recency append after the tier word, in drop-priority order.
	rich := modelItem{m: models.Model{ID: "x", Name: "x", Free: true, ContextLength: 131072}, recent: true}
	if got := strings.Join(rich.tailParts(), "  "); got != "free  128k  recent" {
		t.Fatalf("tailParts = %q, want free 128k recent", got)
	}
	// No friendly name (Name == ID) -> fall back to the id.
	plain := modelItem{m: models.Model{ID: "glm-5.1", Name: "glm-5.1"}}
	if got := plain.Title(); got != "glm-5.1" {
		t.Fatalf("Title = %q, want id fallback", got)
	}
}

// TestProviderModelItemTitleUsesFriendlyName mirrors the above for the
// provider-discovered rows.
func TestProviderModelItemTitleUsesFriendlyName(t *testing.T) {
	item := providerModelItem{provider: "modelscope", model: models.Model{ID: "Qwen/Qwen3-235B-A22B", Name: "Qwen3 235B A22B"}}
	if got := item.Title(); got != "Qwen3 235B A22B" {
		t.Fatalf("Title = %q, want friendly name", got)
	}
}

// newTestModel returns a model in the combo step with a couple of fake models.
func newTestModel() model {
	ms := []models.Model{
		{ID: "test-model", Name: "test-model", Base: "https://example.com", Free: true},
	}
	items := buildModelItems(ms)
	l := list.New(items, list.NewDefaultDelegate(), 60, 20)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	return model{
		list:     l,
		all:      ms,
		subtitle: "opencode Zen",
		step:     stepCombo,
	}
}

// assertPickerRows renders every row of lm through the real columnDelegate
// and pins the two tier-hierarchy cues that the picker flow actually
// produces: an unselected model row keeps the blank two-column gutter (no
// "already picked" dot — nothing is picked yet, and Run never seeds a
// choice), names stay at col 2, and the free tier pops with the accent tail
// word while paid tails stay unaccented.
func assertPickerRows(t *testing.T, screen string, lm list.Model) {
	t.Helper()
	delegate := columnDelegate{}
	lm.Select(0) // cursor on row 0; every later row renders unselected
	rows := 0
	for idx, item := range lm.Items() {
		mi, ok := item.(modelItem)
		if !ok {
			continue
		}
		rows++
		// The cursor row renders as one all-bold-accent line (a different,
		// separately pinned branch); check the unselected rows only.
		if idx == lm.Index() {
			continue
		}
		var buf bytes.Buffer
		delegate.Render(&buf, lm, idx, item)
		plain := ansi.Strip(buf.String())
		if !strings.HasPrefix(plain, "  ") {
			t.Fatalf("%s: unselected row %d carries a gutter marker: %q", screen, idx, plain)
		}
		if !strings.HasPrefix(strings.TrimPrefix(plain, "  "), mi.Title()) {
			t.Fatalf("%s: row %d name not at col 2: %q", screen, idx, plain)
		}
		if mi.m.Free && !strings.Contains(buf.String(), accentStyle.Render("free")) {
			t.Fatalf("%s: free row %d lost the accent tail: %q", screen, idx, buf.String())
		}
		if !mi.m.Free && strings.Contains(buf.String(), accentStyle.Render("free")) {
			t.Fatalf("%s: paid row %d wrongly accented: %q", screen, idx, buf.String())
		}
	}
	if rows < 2 {
		t.Fatalf("%s: expected model rows from the real build, got %d", screen, rows)
	}
}

// TestPickerFlowHierarchyCues pins what the start screen and the manual
// orchestrator step actually render: Run only opens the picker when nothing
// is chosen, so no row may claim to be "the current pick" — the free accent
// and the name column are the surviving cues. Drives Update()/startItems/
// buildModelItems end to end; no hand-built row state.
func TestPickerFlowHierarchyCues(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ms := []models.Model{
		{ID: "free-model", Name: "free-model", Base: "https://example.com", Free: true},
		{ID: "paid-model", Name: "paid-model", Base: "https://example.com"},
	}
	m := model{all: ms, provider: "opencode-go", subtitle: "opencode Zen", step: stepCombo}
	l := list.New(m.startItems(), columnDelegate{}, 80, 30)
	configureList(&l)
	m.list = l
	assertPickerRows(t, "start", m.list)

	// Walk the real flow: Enter on the manual row advances to the
	// orchestrator step, which rebuilds items with m.choice still "".
	for i, item := range m.list.Items() {
		if ci, ok := item.(comboItem); ok && ci.manual {
			m.list.Select(i)
			break
		}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(model)
	if mm.step != stepOrchestrator {
		t.Fatalf("step = %v, want stepOrchestrator", mm.step)
	}
	if mm.choice != "" {
		t.Fatalf("choice = %q before any pick, want empty", mm.choice)
	}
	assertPickerRows(t, "orchestrator", mm.list)
}

// Opening the key manager with 'k' and closing it with Esc must not panic
// when the picker renders again. This is a regression test for a nil pointer
// dereference where m.step stayed stepKeys after the manager closed.
func TestKeyManagerOpenCloseDoesNotPanic(t *testing.T) {
	m := newTestModel()

	// Press 'k' to open the key manager.
	km, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	mm := km.(model)
	if mm.keys == nil {
		t.Fatal("pressing k did not open the key manager")
	}
	if mm.step != stepKeys {
		t.Fatalf("step = %v, want stepKeys", mm.step)
	}

	// Esc closes it and returns to the picker.
	updated, _ := mm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm = updated.(model)
	if mm.keys != nil {
		t.Fatal("Esc did not close the key manager")
	}
	// The picker step must be restored (no longer stepKeys with nil keys).
	if mm.step == stepKeys {
		t.Fatalf("step still stepKeys after close; View() would panic on nil keys")
	}
	// View must render without panicking.
	_ = mm.View()
}

// Esc on the key manager must not panic either (same nil path, direct close).
func TestKeyManagerEscFromCombo(t *testing.T) {
	m := newTestModel()
	km, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	mm := km.(model)

	// Send esc to the key manager itself.
	updated, _ := mm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm = updated.(model)
	if mm.keys != nil {
		t.Fatal("esc did not close the key manager")
	}
	_ = mm.View() // must not panic
}

// Opening the fallback pool screen with 'f' and closing it with Esc must not
// panic when the picker renders again — same nil-deref class as the key
// manager crash (step left at stepFallbacks with a nil manager).
func TestFallbackManagerOpenCloseDoesNotPanic(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := newTestModel()

	fm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	mm := fm.(model)
	if mm.fallbacks == nil {
		t.Fatal("pressing f did not open the fallback manager")
	}
	if mm.step != stepFallbacks {
		t.Fatalf("step = %v, want stepFallbacks", mm.step)
	}

	updated, _ := mm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm = updated.(model)
	if mm.fallbacks != nil {
		t.Fatal("Esc did not close the fallback manager")
	}
	if mm.step == stepFallbacks {
		t.Fatal("step still stepFallbacks after close; View() would panic on nil manager")
	}
	_ = mm.View() // must not panic
}

func TestFallbackManagerSavesPoolOnClose(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := newTestModel()
	m.freePool = []FreeRoute{{Provider: "openrouter", Model: "saved:free"}}
	fm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	mm := fm.(model)

	updated, _ := mm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm = updated.(model)
	if mm.poolErr != "" {
		t.Fatalf("save error = %q", mm.poolErr)
	}
	got := LoadFreePool()
	if len(got) != 1 || got[0] != m.freePool[0] {
		t.Fatalf("saved pool = %v, want %v", got, m.freePool)
	}
}

// Opening the fallback screen and pressing Enter (toggle) must not panic even
// with no fetch results yet (loading rows).
func TestFallbackManagerEnterNoPanic(t *testing.T) {
	m := newTestModel()
	fm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	mm := fm.(model)
	updated, _ := mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = updated.(model)
	_ = mm.View()
}

func TestKeyManagerUsesInlineEditor(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := newKeyManager()
	for i, item := range m.list.Items() {
		if row, ok := item.(keyRow); ok && row.p == "openrouter" {
			m.list.Select(i)
			break
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.editor == nil {
		t.Fatal("Enter did not open the inline key editor")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("stored-inline")})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.editor != nil {
		t.Fatal("inline key editor did not close")
	}
	if got := keys.Load("openrouter"); got != "stored-inline" {
		t.Fatalf("stored key = %q, want stored-inline", got)
	}
}

func TestFallbackManagerReopensWithExistingPool(t *testing.T) {
	m := newTestModel()
	m.freePool = []FreeRoute{{Provider: "openrouter", Model: "a:free"}}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	mm := updated.(model)
	if mm.fallbacks == nil {
		t.Fatal("fallback manager did not open")
	}
	routes := mm.fallbacks.routes()
	if len(routes) != 1 || routes[0] != m.freePool[0] {
		t.Fatalf("reopened routes = %v, want %v", routes, m.freePool)
	}
}

// TestPickingModelDoesNotCarrySavedPool is the regression test for the bug
// where a direct model pick in the launcher silently attached the saved
// free-pool.json routes as fallbacks (and wiped the worker), so a paid pick
// could run a -free variant for the whole session. A concrete model/combo pick
// must return a nil FreePool; only engaging the Free cycle or the f editor
// carries it.
func TestPickingModelDoesNotCarrySavedPool(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := newCatalogTestModel()
	// Simulate a saved free pool on disk.
	m.freePool = []FreeRoute{
		{Provider: "opencode-go", Model: "zen-free"},
		{Provider: "openrouter", Model: "vendor/router-model:free"},
	}
	// Find a modelItem row (a concrete model pick) in the list and select it.
	idx := -1
	for i, item := range m.list.Items() {
		if _, ok := item.(modelItem); ok {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("no modelItem in the start list")
	}
	m.list.Select(idx)
	mm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = cmd
	// A modelItem pick enters the worker step (not quit) — but freePool must
	// already be cleared so a subsequent quit never carries it.
	picked := mm.(model)
	if picked.freePool != nil {
		t.Fatalf("model pick kept the saved pool: %v", picked.freePool)
	}
}

// TestCycleLaunchCarriesSavedPool verifies that launching the Free cycle (the
// cycleItem row) DOES carry the saved pool — that's the intended engagement.
func TestCycleLaunchCarriesSavedPool(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := newCatalogTestModel()
	// Set the pool BEFORE the start items are built so the cycleItem carries
	// selected>0 (the launch condition). Rebuild the list.
	m.freePool = []FreeRoute{{Provider: "openrouter", Model: "vendor/router-model:free"}}
	l := list.New(m.startItems(), list.NewDefaultDelegate(), 80, 30)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	m.list = l
	// The cycleItem is the last selectable row (out of the main path).
	for i, item := range m.list.Items() {
		if _, ok := item.(cycleItem); ok {
			m.list.Select(i)
			break
		}
	}
	mm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = cmd
	picked := mm.(model)
	if !picked.poolTouched {
		t.Fatalf("Free cycle launch did not mark the pool as touched")
	}
	if len(picked.freePool) != 1 {
		t.Fatalf("Free cycle should carry the pool, got %v", picked.freePool)
	}
}

func newCatalogTestModel() model {
	ms := []models.Model{
		{ID: "zen-paid", Name: "zen-paid", Base: models.GoBase},
		{ID: "zen-free", Name: "zen-free", Base: models.MainBase, Free: true},
	}
	catalog := newFallbackManager("")
	catalog.seedProvider("opencode-go", ms)
	catalog.applyLoad(fallbackLoaded{
		provider: "openrouter",
		key:      "or-key",
		models: []models.Model{{
			ID: "vendor/router-model:free", Name: "router", Base: models.OpenRouterBase, Free: true,
		}},
	})
	m := model{
		all:      ms,
		provider: "opencode-go",
		subtitle: "opencode Zen",
		step:     stepCombo,
		catalog:  &catalog,
	}
	l := list.New(m.startItems(), list.NewDefaultDelegate(), 80, 30)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	m.list = l
	return m
}

func TestStartScreenShowsCycleAndAllConfiguredProviderModels(t *testing.T) {
	m := newCatalogTestModel()
	foundCycle := false
	modelsFound := map[string]bool{}
	for _, item := range m.list.Items() {
		switch item := item.(type) {
		case cycleItem:
			foundCycle = true
		case modelItem:
			modelsFound["opencode-go:"+item.m.ID] = true
		case providerModelItem:
			modelsFound[item.provider+":"+item.model.ID] = true
		}
	}
	if !foundCycle {
		t.Fatal("start screen has no Free cycle item")
	}
	for _, want := range []string{
		"opencode-go:zen-paid",
		"openrouter:vendor/router-model:free",
	} {
		if !modelsFound[want] {
			t.Errorf("start screen missing %s; found %v", want, modelsFound)
		}
	}
	// Free models must be visible on the Zen start screen even when paid
	// alternatives exist — the free tier is a first-class launch choice, not
	// something hidden behind the Free-cycle pool editor.
	if !modelsFound["opencode-go:zen-free"] {
		t.Error("free model rows must appear on the Zen start screen when paid models exist")
	}
}

func TestStartScreenShowsFreeModelsWhenNoPaidTier(t *testing.T) {
	ms := []models.Model{
		{ID: "only-free", Name: "only-free", Base: models.MainBase, Free: true},
	}
	catalog := newFallbackManager("")
	catalog.seedProvider("opencode-go", ms)
	m := model{
		all:      ms,
		provider: "opencode-go",
		subtitle: "opencode Zen",
		step:     stepCombo,
		catalog:  &catalog,
	}
	l := list.New(m.startItems(), list.NewDefaultDelegate(), 80, 30)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	m.list = l
	found := false
	for _, item := range m.list.Items() {
		if mi, ok := item.(modelItem); ok && mi.m.ID == "only-free" {
			found = true
		}
	}
	if !found {
		t.Fatal("free model must stay launchable when no paid tier exists")
	}
}

func TestSelectingDiscoveredModelReturnsItsProvider(t *testing.T) {
	m := newCatalogTestModel()
	for i, item := range m.list.Items() {
		if route, ok := item.(providerModelItem); ok && route.provider == "openrouter" {
			m.list.Select(i)
			break
		}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(model)
	if mm.choice != "vendor/router-model:free" || mm.choiceVia != "openrouter" {
		t.Fatalf("selection = %s via %s, want OpenRouter model", mm.choice, mm.choiceVia)
	}
}

func TestConfiguredCycleLaunchesItsFirstRoute(t *testing.T) {
	m := newCatalogTestModel()
	m.freePool = []FreeRoute{
		{Provider: "openrouter", Model: "vendor/router-model:free"},
		{Provider: "opencode-go", Model: "zen-free"},
	}
	m.list.SetItems(m.startItems())
	for i, item := range m.list.Items() {
		if _, ok := item.(cycleItem); ok {
			m.list.Select(i)
			break
		}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(model)
	if mm.choice != m.freePool[0].Model || mm.choiceVia != m.freePool[0].Provider {
		t.Fatalf("cycle launched %s via %s, want first route %+v", mm.choice, mm.choiceVia, m.freePool[0])
	}
	if mm.worker != "" {
		t.Fatalf("cycle unexpectedly selected legacy worker %q", mm.worker)
	}
}

func TestUnconfiguredCycleOpensPoolEditor(t *testing.T) {
	m := newCatalogTestModel()
	for i, item := range m.list.Items() {
		if _, ok := item.(cycleItem); ok {
			m.list.Select(i)
			break
		}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(model)
	if mm.fallbacks == nil || mm.step != stepFallbacks {
		t.Fatal("unconfigured Free cycle did not open the pool editor")
	}
}

func TestCatalogRefreshPreservesHighlightedStartItem(t *testing.T) {
	m := newCatalogTestModel()
	for i, item := range m.list.Items() {
		if combo, ok := item.(comboItem); ok && combo.manual {
			m.list.Select(i)
			break
		}
	}
	m.catalog.applyLoad(fallbackLoaded{
		provider: "groq",
		key:      "groq-key",
		models: []models.Model{{
			ID: "new-groq-model", Base: models.GroqBase, Free: true,
		}},
	})
	m.rebuildStart()
	combo, ok := m.list.SelectedItem().(comboItem)
	if !ok || !combo.manual {
		t.Fatalf("async provider load moved selection to %#v", m.list.SelectedItem())
	}
}

// TestDirectModelSelectionLaunches verifies the one-screen flow: Enter on a
// model row on the start screen launches immediately (quit with the choice
// set) — the worker/fast tiers are preset picks, not default questions. The
// orchestrator/worker walk remains reachable via the manual preset row.
func TestDirectModelSelectionLaunches(t *testing.T) {
	ms := []models.Model{
		{ID: "big-thinker", Name: "big-thinker"},
		{ID: "cheap-worker", Name: "cheap-worker"},
	}
	m := model{
		all:      ms,
		provider: "opencode-go",
		subtitle: "opencode Zen",
		step:     stepCombo,
	}
	l := list.New(m.startItems(), list.NewDefaultDelegate(), 80, 30)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	m.list = l

	// Select "big-thinker" from the start screen.
	for i, item := range m.list.Items() {
		if mi, ok := item.(modelItem); ok && mi.m.ID == "big-thinker" {
			m.list.Select(i)
			break
		}
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(model)
	if mm.choice != "big-thinker" {
		t.Fatalf("choice = %q, want big-thinker", mm.choice)
	}
	if mm.choiceVia != "opencode-go" {
		t.Fatalf("choiceVia = %q, want opencode-go", mm.choiceVia)
	}
	if cmd == nil {
		t.Fatal("Enter on a model must launch (return tea.Quit)")
	}
	if mm.step != stepCombo || mm.worker != "" {
		t.Fatalf("launch must not detour through worker picking: step=%v worker=%q", mm.step, mm.worker)
	}
}

// TestManualPresetWalksWorker verifies the manual preset row still walks the
// orchestrator → worker steps for the both-tiers-by-hand case, and that the
// worker list excludes the orchestrator itself.
func TestManualPresetWalksWorker(t *testing.T) {
	ms := []models.Model{
		{ID: "orchestrator", Name: "orchestrator"},
		{ID: "worker-model", Name: "worker-model"},
	}
	m := model{
		all:      ms,
		provider: "opencode-go",
		subtitle: "opencode Zen",
		step:     stepCombo,
	}
	l := list.New(m.startItems(), list.NewDefaultDelegate(), 80, 30)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	m.list = l

	// Enter on the manual preset -> orchestrator step.
	for i, item := range m.list.Items() {
		if ci, ok := item.(comboItem); ok && ci.manual {
			m.list.Select(i)
			break
		}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(model)
	if mm.step != stepOrchestrator {
		t.Fatalf("step = %v, want stepOrchestrator", mm.step)
	}

	// Select orchestrator -> worker step.
	for i, item := range mm.list.Items() {
		if mi, ok := item.(modelItem); ok && mi.m.ID == "orchestrator" {
			mm.list.Select(i)
			break
		}
	}
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = updated.(model)
	if mm.step != stepWorker {
		t.Fatalf("step = %v, want stepWorker", mm.step)
	}

	// The worker list must not include the orchestrator itself.
	for _, item := range mm.list.Items() {
		if mi, ok := item.(modelItem); ok && mi.m.ID == "orchestrator" {
			t.Fatal("worker list must exclude the orchestrator itself")
		}
	}

	// Esc skips the worker and launches with empty worker.
	updated, cmd := mm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm = updated.(model)
	if mm.worker != "" {
		t.Fatalf("Esc should clear worker, got %q", mm.worker)
	}
	if cmd == nil {
		t.Fatal("Esc should quit the picker (return tea.Quit)")
	}
}

// TestStartScreenGroupsModelsByProvider verifies the picker emits collapsible
// group headers (groupHeaderItem) so models read as categorized sections
// rather than one flat list, and that the OpenRouter group is titled "Most
// used" while secondary providers keep their friendly subtitle. Model groups
// open the list (the main path), ahead of presets and the free-cycle row.
func TestStartScreenGroupsModelsByProvider(t *testing.T) {
	m := newCatalogTestModel()
	// A model group header must open the list: the main path is model rows.
	if _, ok := m.list.Items()[0].(groupHeaderItem); !ok {
		t.Fatalf("first item = %T, want a provider group header", m.list.Items()[0])
	}
	var headers []groupHeaderItem
	for _, item := range m.list.Items() {
		if h, ok := item.(groupHeaderItem); ok {
			headers = append(headers, h)
		}
	}
	if len(headers) == 0 {
		t.Fatal("start screen has no group headers — models are not categorized")
	}
	var sawZen, sawMostUsed bool
	for _, h := range headers {
		switch h.label {
		case "opencode Zen":
			sawZen = true
		case "Most used":
			sawMostUsed = true
		}
	}
	if !sawZen {
		t.Error("missing 'opencode Zen' group header")
	}
	if !sawMostUsed {
		t.Error("missing 'Most used' (OpenRouter) group header")
	}
}

// TestGroupHeadersAreNotSelectable verifies that landing the cursor on a
// groupHeaderItem never chooses it as a model: Enter skips to the next real
// row, and ensureSelectable keeps the cursor off headers during navigation.
func TestGroupHeadersAreNotSelectable(t *testing.T) {
	m := newCatalogTestModel()
	hdrIdx := -1
	for i, item := range m.list.Items() {
		if _, ok := item.(groupHeaderItem); ok {
			hdrIdx = i
			break
		}
	}
	if hdrIdx < 0 {
		t.Fatal("no group header to test")
	}
	m.list.Select(hdrIdx)
	navd, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	nm := navd.(model)
	if _, ok := nm.list.SelectedItem().(groupHeaderItem); ok {
		t.Fatal("cursor landed on a group header after navigation")
	}
	m.list.Select(hdrIdx)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(model)
	if mm.choice != "" || mm.choiceVia != "" || mm.quit {
		t.Fatalf("header Enter produced a choice: choice=%q via=%q quit=%v", mm.choice, mm.choiceVia, mm.quit)
	}
}

// TestFastStepFlowAfterWorker verifies the manual flow still walks
// orchestrator -> worker -> fast, that Esc on the fast step means auto
// (empty Fast, quit), and that picking the auto/none rows records the
// corresponding mode.
func TestFastStepFlowAfterWorker(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ms := []models.Model{
		{ID: "orchestrator", Name: "orchestrator"},
		{ID: "worker-model", Name: "worker-model"},
		{ID: "flash-model", Name: "flash-model"},
	}
	m := model{
		all:      ms,
		provider: "opencode-go",
		subtitle: "opencode Zen",
		step:     stepCombo,
	}
	l := list.New(m.startItems(), list.NewDefaultDelegate(), 80, 30)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	m.list = l

	// The manual preset is the only door into the tier walk on the
	// one-screen flow.
	for i, item := range m.list.Items() {
		if ci, ok := item.(comboItem); ok && ci.manual {
			m.list.Select(i)
			break
		}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(model)
	if mm.step != stepOrchestrator {
		t.Fatalf("step = %v, want stepOrchestrator", mm.step)
	}

	// Pick orchestrator -> worker step.
	for i, item := range mm.list.Items() {
		if mi, ok := item.(modelItem); ok && mi.m.ID == "orchestrator" {
			mm.list.Select(i)
			break
		}
	}
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = updated.(model)
	if mm.step != stepWorker {
		t.Fatalf("step = %v, want stepWorker", mm.step)
	}

	// Pick worker -> fast step.
	for i, item := range mm.list.Items() {
		if mi, ok := item.(modelItem); ok && mi.m.ID == "worker-model" {
			mm.list.Select(i)
			break
		}
	}
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = updated.(model)
	if mm.step != stepFast {
		t.Fatalf("step = %v, want stepFast", mm.step)
	}
	// The fast list must start with the auto row and exclude the orchestrator.
	first, ok := mm.list.Items()[0].(fastItem)
	if !ok || first.mode != "auto" {
		t.Fatalf("first fast row = %#v, want auto", mm.list.Items()[0])
	}
	for _, item := range mm.list.Items() {
		if mi, ok := item.(modelItem); ok && mi.m.ID == "orchestrator" {
			t.Fatal("fast list must exclude the orchestrator")
		}
	}

	// Esc on the fast step = auto: empty Fast and quit.
	updated, escCmd := mm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm = updated.(model)
	if mm.fast != "" {
		t.Fatalf("Esc on fast step should leave fast empty (auto), got %q", mm.fast)
	}
	if escCmd == nil {
		t.Fatal("Esc on fast step should quit the picker")
	}
}

// TestFastStepAutoAndNoneRows verifies the auto and none rows set the
// corresponding fast mode and quit.
func TestFastStepAutoAndNoneRows(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ms := []models.Model{
		{ID: "orchestrator", Name: "orchestrator"},
		{ID: "flash-model", Name: "flash-model"},
	}
	m := model{
		all:      ms,
		provider: "opencode-go",
		subtitle: "opencode Zen",
		step:     stepCombo,
	}
	l := list.New(m.startItems(), list.NewDefaultDelegate(), 80, 30)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	m.list = l

	// Manual preset -> orchestrator step.
	for i, item := range m.list.Items() {
		if ci, ok := item.(comboItem); ok && ci.manual {
			m.list.Select(i)
			break
		}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(model)
	// Pick orchestrator; only one non-orchestrator model exists, so the worker
	// list has exactly one entry; picking it lands on the fast step.
	for i, item := range mm.list.Items() {
		if mi, ok := item.(modelItem); ok && mi.m.ID == "orchestrator" {
			mm.list.Select(i)
			break
		}
	}
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = updated.(model)
	for i, item := range mm.list.Items() {
		if mi, ok := item.(modelItem); ok && mi.m.ID == "flash-model" {
			mm.list.Select(i)
			break
		}
	}
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = updated.(model)
	if mm.step != stepFast {
		t.Fatalf("step = %v, want stepFast", mm.step)
	}

	// Pick the auto row.
	mm.list.Select(0)
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = updated.(model)
	if mm.fast != "auto" {
		t.Fatalf("auto row: fast = %q, want auto", mm.fast)
	}

	// Re-enter the step and pick none.
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEsc}) // quit
	mm = updated.(model)
	mm2 := model{all: ms, provider: "opencode-go", subtitle: "opencode Zen", step: stepCombo, choice: "orchestrator"}
	l2 := list.New(mm2.startItems(), list.NewDefaultDelegate(), 80, 30)
	l2.SetShowStatusBar(false)
	l2.SetFilteringEnabled(true)
	l2.SetShowHelp(false)
	mm2.list = l2
	st, _ := mm2.enterWorkerStep()
	mm3 := st.(model)
	for i, item := range mm3.list.Items() {
		if mi, ok := item.(modelItem); ok && mi.m.ID == "flash-model" {
			mm3.list.Select(i)
			break
		}
	}
	st2, _ := mm3.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm4 := st2.(model)
	if mm4.step != stepFast {
		t.Fatalf("step = %v, want stepFast", mm4.step)
	}
	mm4.list.Select(1) // the none row
	updated, _ = mm4.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm5 := updated.(model)
	if mm5.fast != "none" {
		t.Fatalf("none row: fast = %q, want none", mm5.fast)
	}
}

// TestFastStepExplicitModel verifies picking a concrete model row in the fast
// step records that id.
func TestFastStepExplicitModel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ms := []models.Model{
		{ID: "orchestrator", Name: "orchestrator"},
		{ID: "worker-model", Name: "worker-model"},
		{ID: "flash-model", Name: "flash-model"},
	}
	m := model{
		all:      ms,
		provider: "opencode-go",
		subtitle: "opencode Zen",
		step:     stepCombo,
	}
	l := list.New(m.startItems(), list.NewDefaultDelegate(), 80, 30)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	m.list = l

	// Manual preset -> orchestrator step.
	for i, item := range m.list.Items() {
		if ci, ok := item.(comboItem); ok && ci.manual {
			m.list.Select(i)
			break
		}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(model)
	// Pick orchestrator -> worker step.
	for i, item := range mm.list.Items() {
		if mi, ok := item.(modelItem); ok && mi.m.ID == "orchestrator" {
			mm.list.Select(i)
			break
		}
	}
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = updated.(model)
	// Pick worker -> fast step.
	for i, item := range mm.list.Items() {
		if mi, ok := item.(modelItem); ok && mi.m.ID == "worker-model" {
			mm.list.Select(i)
			break
		}
	}
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = updated.(model)
	// Pick the explicit fast model.
	for i, item := range mm.list.Items() {
		if mi, ok := item.(modelItem); ok && mi.m.ID == "flash-model" {
			mm.list.Select(i)
			break
		}
	}
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = updated.(model)
	if mm.fast != "flash-model" {
		t.Fatalf("explicit fast pick = %q, want flash-model", mm.fast)
	}
}

// TestDefaultSelectionIsLastUsed pins the one-screen flow's default: the
// cursor opens on the last-used model (the MRU store's newest id), so the
// common path is open → Enter. The default is resolved at Run time; the test
// drives the same selectDefault the real program runs.
func TestDefaultSelectionIsLastUsed(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	models.RecordRecent("second-choice")
	models.RecordRecent("first-choice") // newest
	ms := []models.Model{
		{ID: "first-choice", Name: "first-choice", Base: "https://example.com"},
		{ID: "second-choice", Name: "second-choice", Base: "https://example.com"},
		{ID: "other-model", Name: "other-model", Base: "https://example.com"},
	}
	m := model{all: ms, provider: "opencode-go", subtitle: "opencode Zen", step: stepCombo}
	l := list.New(m.startItems(), columnDelegate{}, 80, 30)
	configureList(&l)
	m.list = l
	m.selectDefault()
	mi, ok := m.list.SelectedItem().(modelItem)
	if !ok {
		t.Fatalf("default selection = %T, want a modelItem", m.list.SelectedItem())
	}
	if mi.m.ID != "first-choice" {
		t.Fatalf("default selection = %q, want the last-used model first-choice", mi.m.ID)
	}
}

// TestDefaultSelectionFallsBackToFirstModel pins the no-MRU fallback: with an
// empty recent store the cursor lands on the primary provider's first model —
// never on the resume row, a header, or the free-cycle row.
func TestDefaultSelectionFallsBackToFirstModel(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ms := []models.Model{
		{ID: "zen-free", Name: "zen-free", Base: "https://example.com", Free: true},
	}
	catalog := newFallbackManager("")
	catalog.seedProvider("opencode-go", ms)
	m := model{all: ms, provider: "opencode-go", subtitle: "opencode Zen", step: stepCombo, catalog: &catalog}
	l := list.New(m.startItems(), columnDelegate{}, 80, 30)
	configureList(&l)
	m.list = l
	m.selectDefault()
	switch item := m.list.SelectedItem().(type) {
	case modelItem:
		if item.m.ID != "zen-free" {
			t.Fatalf("fallback selection = %q, want zen-free", item.m.ID)
		}
	default:
		t.Fatalf("fallback selection = %T, want a modelItem", m.list.SelectedItem())
	}
}

// TestEnterOnResumeRowQuitsWithSessionID drives the real Update flow: the
// resume row is pinned at the top of the one-screen list and Enter on it quits
// with ResumeSessionID set and no model choice.
func TestEnterOnResumeRowQuitsWithSessionID(t *testing.T) {
	m := newCatalogTestModel()
	m.resume = &ResumeOption{SessionID: "abc-123", Label: "glm-5.2", Description: "2026-08-30 10:00"}
	l := list.New(m.startItems(), columnDelegate{}, 80, 30)
	configureList(&l)
	m.list = l
	// The resume row is the pinned FIRST row.
	if ri, ok := m.list.Items()[0].(resumeItem); !ok || ri.opt.SessionID != "abc-123" {
		t.Fatalf("first row = %#v, want the pinned resume row", m.list.Items()[0])
	}
	m.list.Select(0)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(model)
	if mm.resumeID != "abc-123" {
		t.Fatalf("resumeID = %q, want abc-123", mm.resumeID)
	}
	if mm.choice != "" {
		t.Fatalf("resume pick must not set a model choice, got %q", mm.choice)
	}
	if cmd == nil {
		t.Fatal("Enter on the resume row must quit the picker")
	}
}

// TestNoResumeRowWithoutResumeOption pins the absent half: with resume == nil
// (nothing resumable for this directory) no resume row appears anywhere.
func TestNoResumeRowWithoutResumeOption(t *testing.T) {
	m := newCatalogTestModel()
	for i, item := range m.list.Items() {
		if _, ok := item.(resumeItem); ok {
			t.Fatalf("resume row found at %d without a ResumeOption", i)
		}
	}
}

// TestProviderFilterCycles drives the 'p' binding through the real Update:
// all → primary → discovered provider → back to all. Under a filter only that
// provider's group remains (plus provider-unspecific rows), and cycling back
// restores every group.
func TestProviderFilterCycles(t *testing.T) {
	m := newCatalogTestModel()
	countProviders := func(mm model) map[string]bool {
		found := map[string]bool{}
		for _, item := range mm.list.Items() {
			switch it := item.(type) {
			case modelItem:
				found["opencode-go"] = true
			case providerModelItem:
				found[it.provider] = true
			}
		}
		return found
	}

	// Start: both providers visible.
	both := countProviders(m)
	if !both["opencode-go"] || !both["openrouter"] {
		t.Fatalf("unfiltered list missing providers: %v", both)
	}

	// p once: filter to the primary provider (opencode-go).
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	mm := updated.(model)
	if mm.filter.off() || mm.filter.provider != "opencode-go" {
		t.Fatalf("filter after first p = %+v, want opencode-go", mm.filter)
	}
	got := countProviders(mm)
	if got["openrouter"] || !got["opencode-go"] {
		t.Fatalf("filtered list = %v, want only opencode-go", got)
	}

	// p again: cycle to the discovered provider (openrouter).
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	mm = updated.(model)
	if mm.filter.provider != "openrouter" {
		t.Fatalf("filter after second p = %+v, want openrouter", mm.filter)
	}
	got = countProviders(mm)
	if got["opencode-go"] || !got["openrouter"] {
		t.Fatalf("filtered list = %v, want only openrouter", got)
	}

	// p a third time: back to all.
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	mm = updated.(model)
	if !mm.filter.off() {
		t.Fatalf("filter after third p = %+v, want all (off)", mm.filter)
	}
	got = countProviders(mm)
	if !got["opencode-go"] || !got["openrouter"] {
		t.Fatalf("cycled-back list = %v, want both providers", got)
	}
}

// TestProviderFilterKeepsCursorOnSameModel pins that cycling 'p' does not
// yank the cursor: a selected row that survives the refilter stays selected.
func TestProviderFilterKeepsCursorOnSameModel(t *testing.T) {
	m := newCatalogTestModel()
	// Select a zen model, then filter to openrouter and back.
	for i, item := range m.list.Items() {
		if mi, ok := item.(modelItem); ok && mi.m.ID == "zen-free" {
			m.list.Select(i)
			break
		}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	mm := updated.(model)
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	mm = updated.(model)
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	mm = updated.(model)
	mi, ok := mm.list.SelectedItem().(modelItem)
	if !ok || mi.m.ID != "zen-free" {
		t.Fatalf("cursor after filter cycle = %#v, want zen-free", mm.list.SelectedItem())
	}
}

// TestEnterLaunchesLastUsedModel is the end-to-end common path: last-used
// model preselected → Enter → quit with that choice and provider.
func TestEnterLaunchesLastUsedModel(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	models.RecordRecent("zen-paid")
	ms := []models.Model{
		{ID: "zen-paid", Name: "zen-paid", Base: "https://example.com"},
		{ID: "zen-free", Name: "zen-free", Base: "https://example.com", Free: true},
	}
	m := model{all: ms, provider: "opencode-go", subtitle: "opencode Zen", step: stepCombo}
	l := list.New(m.startItems(), columnDelegate{}, 80, 30)
	configureList(&l)
	m.list = l
	m.selectDefault()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(model)
	if mm.choice != "zen-paid" || mm.choiceVia != "opencode-go" {
		t.Fatalf("launch = %q via %q, want zen-paid via opencode-go", mm.choice, mm.choiceVia)
	}
	if cmd == nil {
		t.Fatal("Enter on the default selection must quit the picker")
	}
}

// TestGroupHeaderCarriesHealthToken pins the health-in-the-header contract:
// the group header of a provider whose usage row says drained renders
// "drained!" (in the alert style), a healthy provider renders "ok", and a
// provider without usage data renders no token.
func TestGroupHeaderCarriesHealthToken(t *testing.T) {
	pct := 10
	m := newCatalogTestModel()
	m.usage = map[string]usageSnapshot{
		"opencode-go": {Provider: "opencode-go", Usage: &proxy.ProviderUsage{
			Name: "opencode-go", Kind: proxy.UsageCredits,
			Rolling: &proxy.WindowStat{State: "ok", Percent: pct},
		}, Ready: true},
		"openrouter": {Provider: "openrouter", Usage: &proxy.ProviderUsage{
			Name: "openrouter", Kind: proxy.UsageCredits, Exhausted: true,
		}, Ready: true},
	}
	l := list.New(m.startItems(), columnDelegate{}, 80, 30)
	configureList(&l)
	seen := map[string]bool{}
	for _, item := range l.Items() {
		h, ok := item.(groupHeaderItem)
		if !ok || h.count == 0 {
			continue
		}
		seen[h.label] = true
		switch h.label {
		case "opencode Zen":
			if h.health != "ok" {
				t.Errorf("Zen health = %q, want ok", h.health)
			}
		case "Most used":
			if h.health != "drained!" {
				t.Errorf("OpenRouter health = %q, want drained!", h.health)
			}
		}
	}
	if !seen["opencode Zen"] || !seen["Most used"] {
		t.Fatalf("provider groups missing: %v", seen)
	}
	// A provider with no usage snapshot carries no token at all.
	m2 := newCatalogTestModel()
	for _, item := range m2.startItems() {
		if h, ok := item.(groupHeaderItem); ok && h.health != "" {
			t.Fatalf("header for %q has health %q without any usage snapshot", h.label, h.health)
		}
	}
}

// TestOneScreenRenderShape renders the real View() of the one-screen flow and
// pins its visual order: the resume row pinned above every group, the primary
// provider group (with health token) first among groups, the manual preset and
// free-cycle row reachable below, and the usage banner in the frame. This is
// the smoke check for the whole redesign.
func TestOneScreenRenderShape(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ms := []models.Model{
		{ID: "zen-paid", Name: "Zen Paid", Base: "https://example.com", ContextLength: 262144},
		{ID: "zen-free", Name: "Zen Free", Base: "https://example.com", Free: true, ContextLength: 131072},
	}
	catalog := newFallbackManager("")
	catalog.seedProvider("opencode-go", ms)
	catalog.applyLoad(fallbackLoaded{provider: "openrouter", key: "k", models: []models.Model{
		{ID: "vendor/model:free", Name: "Router Free", Free: true, ContextLength: 65536},
	}})
	m := model{
		all: ms, provider: "opencode-go", subtitle: "opencode Zen", step: stepCombo, catalog: &catalog,
		resume: &ResumeOption{SessionID: "abc", Label: "zen-paid", Description: "2026-08-30 09:12"},
		usage: map[string]usageSnapshot{
			"openrouter": {Provider: "openrouter", Usage: &proxy.ProviderUsage{
				Name: "openrouter", Kind: proxy.UsageCredits, Exhausted: true,
			}, Ready: true},
		},
	}
	l := list.New(m.startItems(), columnDelegate{}, 100, 40)
	configureList(&l)
	m.list = l
	m.selectDefault()
	upd, _ := m.Update(tea.WindowSizeMsg{Width: 104, Height: 30})
	mm := upd.(model)
	screen := mm.View()
	plain := ansi.Strip(screen)

	// Row order: resume pinned first, then the primary provider header, and
	// the manual preset + free-cycle rows below the model groups. The health
	// token rides one space after the header line ("most used · 1 drained!").
	resumeAt := strings.Index(plain, "resume · zen-paid")
	zenHeaderAt := strings.Index(plain, "opencode zen · 2")
	orHeaderAt := strings.Index(plain, "most used · 1 drained!")
	manualAt := strings.Index(plain, "manual")
	cycleAt := strings.Index(plain, "free-cycle")
	for name, at := range map[string]int{
		"resume row":                resumeAt,
		"zen header":                zenHeaderAt,
		"openrouter drained header": orHeaderAt,
		"manual preset row":         manualAt,
		"free-cycle row":            cycleAt,
	} {
		if at < 0 {
			t.Fatalf("one-screen render missing %s:\n%s", name, plain)
		}
	}
	if !(resumeAt < zenHeaderAt && zenHeaderAt < orHeaderAt && orHeaderAt < manualAt && manualAt < cycleAt) {
		t.Fatalf("one-screen row order wrong:\n%s", plain)
	}
	// Health token style: the drained header carries the alert-colored token.
	if !strings.Contains(screen, alertStyle.Render("drained!")) {
		t.Fatal("drained! token is not alert-styled")
	}
	// The footer names the new affordances.
	if !strings.Contains(plain, "p provider") || !strings.Contains(plain, "enter launch") {
		t.Fatalf("footer missing p/enter hints:\n%s", plain)
	}
}
