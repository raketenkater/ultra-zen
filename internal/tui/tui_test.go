package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/raketenkater/ultra-zen/internal/keys"
	"github.com/raketenkater/ultra-zen/internal/models"
)

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
