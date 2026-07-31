package tui

import (
	"sort"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/raketenkater/ultra-zen/internal/keys"
)

// keyManager is the screen that lists every provider and lets the user set,
// view, or clear a stored API key. It is reached from the model picker with
// the 'k' hotkey. Each provider row shows whether a key is stored; selecting
// one prompts for a new value (or clears with an empty entry).
type keyManager struct {
	list      list.Model
	providers []string
	editor    *inlineKeyEditor
	editing   string
	err       string
	// exit tells the parent model to return to the model picker.
	done bool
	// quit is true when the user Ctrl+C'd out of the manager.
	quit bool
}

// providerDesc is the per-row description for the key manager list.
func providerKeyDesc(p string) string {
	if keys.Has(p) {
		return "key stored — Enter to change, x to clear"
	}
	return "no key — Enter to set"
}

func newKeyManager() keyManager {
	providers := []string{
		"openrouter",
		"modelscope",
		"groq",
		"cerebras",
		"huggingface",
		"cohere",
		"opencode-go",
	}
	// Keep a stable, readable order rather than map iteration order.
	sort.Strings(providers)

	items := make([]list.Item, 0, len(providers))
	for _, p := range providers {
		items = append(items, keyRow{p: p})
	}
	l := list.New(items, list.NewDefaultDelegate(), 60, 20)
	l.Title = "API keys"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	return keyManager{list: l, providers: providers}
}

// keyRow is one provider in the key manager.
type keyRow struct {
	p string
}

func (k keyRow) Title() string       { return k.p }
func (k keyRow) Description() string { return providerKeyDesc(k.p) }
func (k keyRow) FilterValue() string { return k.p }

func (m *keyManager) Init() tea.Cmd { return nil }

func (m *keyManager) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.editor != nil {
		cmd := m.editor.Update(msg)
		if !m.editor.done {
			return m, cmd
		}
		editor := m.editor
		provider := m.editing
		m.editor = nil
		m.editing = ""
		if editor.quit {
			m.quit = true
			m.done = true
			return m, tea.Quit
		}
		if editor.cancelled {
			return m, cmd
		}
		if err := keys.Save(provider, editor.value); err != nil {
			m.err = err.Error()
		} else {
			m.err = ""
		}
		refresh := m.list.SetItems(buildKeyItems())
		m.restoreSelection(provider)
		return m, tea.Batch(cmd, refresh)
	}
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.list.SetSize(max(size.Width-4, 20), max(size.Height-7, 8))
		return m, nil
	}
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c":
			m.quit = true
			m.done = true
			return m, tea.Quit
		case "esc":
			m.done = true
			return m, nil
		case "x", "d":
			// Clear the selected provider's stored key.
			if item, ok := m.list.SelectedItem().(keyRow); ok {
				if err := keys.Save(item.p, ""); err != nil {
					m.err = err.Error()
				} else {
					m.err = ""
				}
				cmd := m.list.SetItems(buildKeyItems())
				m.restoreSelection(item.p)
				return m, cmd
			}
			return m, nil
		case "enter":
			if item, ok := m.list.SelectedItem().(keyRow); ok {
				help := "paste a new API key, or leave empty to clear"
				if item.p == "opencode-go" {
					help = "opencode Zen key (opencode-go) — usually managed by `opencode auth login`"
				}
				m.editor = newInlineKeyEditor("Set "+item.p+" API key", help, true)
				m.editing = item.p
				m.err = ""
				return m, textinput.Blink
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m keyManager) View() string {
	if m.editor != nil {
		return m.editor.View()
	}
	var b string
	b += titleStyle.Render("═══ ultra-zen ═══") + "\n"
	b += subtitleStyle.Render("  API keys — Enter set/change · x clear · Esc back") + "\n\n"
	b += m.list.View() + "\n"
	if m.err != "" {
		b += mutedStyle.Render("  error: "+m.err) + "\n"
	}
	b += mutedStyle.Render("  Keys are stored at " + keys.Path())
	return b
}

func (m *keyManager) restoreSelection(provider string) {
	for i, item := range m.list.Items() {
		if row, ok := item.(keyRow); ok && row.p == provider {
			m.list.Select(i)
			return
		}
	}
}

// buildKeyItems rebuilds the key list after a change so the descriptions
// (key stored / no key) stay accurate.
func buildKeyItems() []list.Item {
	providers := []string{
		"openrouter",
		"modelscope",
		"groq",
		"cerebras",
		"huggingface",
		"cohere",
		"opencode-go",
	}
	sort.Strings(providers)
	items := make([]list.Item, 0, len(providers))
	for _, p := range providers {
		items = append(items, keyRow{p: p})
	}
	return items
}

// providerHint returns a short hint for a provider's key, used in prompts.
func providerHint(p string) string {
	switch p {
	case "openrouter":
		return "get one at https://openrouter.ai/keys"
	case "modelscope":
		return "get one at https://modelscope.cn/my/apiToken"
	case "groq":
		return "get one at https://console.groq.com/keys"
	case "cerebras":
		return "get one at https://cloud.cerebras.ai/platform/apikeys"
	case "huggingface":
		return "get one at https://huggingface.co/settings/tokens"
	case "cohere":
		return "get one at https://dashboard.cohere.com/api-keys"
	case "opencode-go":
		return "managed by `opencode auth login`; a saved key overrides it"
	default:
		return ""
	}
}
