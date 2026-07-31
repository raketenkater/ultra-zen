package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/raketenkater/ultra-zen/internal/keys"
	"github.com/raketenkater/ultra-zen/internal/models"
)

// feedProvider marks a provider ready with the given models and hides every
// other provider, so the list contains only that provider's model rows.
func feedProvider(m *fallbackManager, provider string, ids ...string) {
	for p, st := range m.states {
		if p == provider {
			st.status = statusReady
			st.key = "key-" + provider
			for _, id := range ids {
				st.models = append(st.models, models.Model{ID: id, Base: "https://example.com", Free: true})
			}
		} else {
			st.status = statusHidden
		}
	}
	m.rebuildList()
}

// toggleRow presses Enter on the row at index i (which toggles a model).
func toggleRow(t *testing.T, m *fallbackManager, i int) {
	m.list.Select(i)
	m.toggle()
}

func TestRoutesEmptyByDefault(t *testing.T) {
	m := newFallbackManager("")
	if got := m.routes(); len(got) != 0 {
		t.Fatalf("routes() = %v, want empty", got)
	}
}

func TestToggleAddsAndRemovesFromOrder(t *testing.T) {
	m := newFallbackManager("")
	feedProvider(&m, "modelscope", "deepseek-ai/DeepSeek-V4-Flash", "ZhipuAI/GLM-5.2")

	// Toggle the first model (index 0).
	m.list.Select(0)
	m.toggle()
	routes := m.routes()
	if len(routes) != 1 {
		t.Fatalf("routes() after toggle = %v, want 1 entry", routes)
	}
	if routes[0].Provider != "modelscope" || routes[0].Model != "deepseek-ai/DeepSeek-V4-Flash" {
		t.Fatalf("routes()[0] = %+v, want modelscope/deepseek-ai/DeepSeek-V4-Flash", routes[0])
	}

	// Toggle it off again — order should be empty.
	m.list.Select(0)
	m.toggle()
	if got := m.routes(); len(got) != 0 {
		t.Fatalf("routes() after untoggle = %v, want empty", got)
	}
}

func TestToggleOrderIsStable(t *testing.T) {
	m := newFallbackManager("")
	feedProvider(&m, "openrouter", "a:free", "b:free", "c:free")

	// Select and toggle 3 models in order.
	toggleRow(t, &m, 0)
	toggleRow(t, &m, 1)
	toggleRow(t, &m, 2)

	routes := m.routes()
	if len(routes) != 3 {
		t.Fatalf("routes() = %v, want 3 entries", routes)
	}
	// Feed order was a, b, c (feedProvider appends in given order).
	want := []string{"a:free", "b:free", "c:free"}
	for i, w := range want {
		if routes[i].Model != w {
			t.Fatalf("routes()[%d].Model = %q, want %q", i, routes[i].Model, w)
		}
	}
}

func TestPrimaryModelSuppressed(t *testing.T) {
	m := newFallbackManager("primary-model")
	feedProvider(&m, "openrouter", "primary-model", "fallback:free")

	// Only the non-primary model should appear as a toggleable row.
	found := 0
	for _, item := range m.list.Items() {
		if row, ok := item.(fallbackRow); ok && row.kind == rowModel {
			found++
			if row.modelID == "primary-model" {
				t.Fatalf("primary model %q should not be offered as a fallback", row.modelID)
			}
		}
	}
	if found != 1 {
		t.Fatalf("expected 1 model row, got %d", found)
	}
}

func TestResetClearsPool(t *testing.T) {
	m := newFallbackManager("")
	feedProvider(&m, "groq", "llama-3.3-70b")
	toggleRow(t, &m, 0)
	if len(m.routes()) != 1 {
		t.Fatalf("expected 1 route before reset, got %v", m.routes())
	}
	m.selected = map[string]bool{}
	m.order = nil
	if got := m.routes(); len(got) != 0 {
		t.Fatalf("routes() after reset = %v, want empty", got)
	}
}

func TestApplyLoadShowsRetryableError(t *testing.T) {
	m := newFallbackManager("")
	m.applyLoad(fallbackLoaded{provider: "openrouter", err: errUnknownProvider("openrouter")})
	if m.states["openrouter"].status != statusError {
		t.Fatalf("errored provider status = %v, want visible error", m.states["openrouter"].status)
	}
	found := false
	for _, item := range m.list.Items() {
		if row, ok := item.(fallbackRow); ok && row.provider == "openrouter" && row.kind == rowError {
			found = true
		}
	}
	if !found {
		t.Fatal("errored provider has no retry row")
	}
}

func TestApplyLoadShowsMissingKey(t *testing.T) {
	m := newFallbackManager("")
	m.applyLoad(fallbackLoaded{provider: "openrouter"})
	if m.states["openrouter"].status != statusKeyless {
		t.Fatalf("keyless provider status = %v, want statusKeyless", m.states["openrouter"].status)
	}
	for _, item := range m.list.Items() {
		if row, ok := item.(fallbackRow); ok && row.provider == "openrouter" && row.kind == rowNoKey {
			return
		}
	}
	t.Fatal("keyless provider has no key-entry row")
}

func TestToggleKeepsExactCursorAndFooterOrder(t *testing.T) {
	m := newFallbackManager("")
	feedProvider(&m, "openrouter", "a:free", "b:free", "c:free")
	m.list.Select(2)
	m.toggle()
	row, ok := m.list.SelectedItem().(fallbackRow)
	if !ok || row.modelID != "c:free" {
		t.Fatalf("cursor moved to %#v after toggle, want c:free", m.list.SelectedItem())
	}
	m.list.Select(0)
	m.toggle()
	got := m.orderKeys()
	if len(got) != 2 || got[0] != "c:free" || got[1] != "a:free" {
		t.Fatalf("footer order = %v, want [c:free a:free]", got)
	}
}

func TestExistingPoolRestoredWhenReopened(t *testing.T) {
	want := []FreeRoute{
		{Provider: "openrouter", Model: "b:free"},
		{Provider: "openrouter", Model: "a:free"},
	}
	m := newFallbackManager("", want)
	feedProvider(&m, "openrouter", "a:free", "b:free")
	got := m.routes()
	if len(got) != len(want) {
		t.Fatalf("routes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("routes[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestFallbackKeyEntryIsInline(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := newFallbackManager("")
	for p, st := range m.states {
		if p == "openrouter" {
			st.status = statusKeyless
		} else {
			st.status = statusHidden
		}
	}
	m.rebuildList()
	m.list.Select(0)
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.editor == nil {
		t.Fatal("Enter did not open an inline editor")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sk-or-test")})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.editor != nil {
		t.Fatal("editor still open after Enter")
	}
	if got := keys.Load("openrouter"); got != "sk-or-test" {
		t.Fatalf("saved key = %q, want sk-or-test", got)
	}
	if m.states["openrouter"].status != statusLoading {
		t.Fatalf("provider status = %v, want loading refetch", m.states["openrouter"].status)
	}
}
