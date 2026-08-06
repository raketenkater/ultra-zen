package tui

import (
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/raketenkater/ultra-zen/internal/auth"
	"github.com/raketenkater/ultra-zen/internal/keys"
	"github.com/raketenkater/ultra-zen/internal/models"
)

// FreeRoute is one user-configured rotation entry. Provider is a
// splitFreeModelSpec provider name ("openrouter", "opencode-go", "groq", ...);
// String() emits the provider:model form so main.go can treat a TUI-configured
// pool exactly like --free-model flags. The API key is resolved by main.go from
// the same sources the primary provider uses (flag/env/keys store), so the TUI
// does not need to carry secrets back.
type FreeRoute struct {
	Provider string
	Model    string
}

// String returns the provider:model spec consumed by splitFreeModelSpec.
func (r FreeRoute) String() string { return r.Provider + ":" + r.Model }

// poolProviders is the set of providers whose free models can rotate as
// fallbacks, in display order. codex is excluded: its models are
// subscription-backed (Free:false) and addRoute only accepts free models.
var poolProviders = []string{
	"openrouter",
	"opencode-go",
	"groq",
	"cerebras",
	"huggingface",
	"cohere",
	"modelscope",
}

// fallbackStatus tracks one provider's fetch state on the fallback screen.
type fallbackStatus int

const (
	statusLoading fallbackStatus = iota
	statusReady
	statusKeyless
	statusHidden // intentionally omitted from the list (primarily test setup)
	statusError
)

type providerState struct {
	status fallbackStatus
	key    string
	models []models.Model
	err    string
}

// fallbackManager is the rotation-config screen. Reached with 'f' from the
// model picker; owns all input while open, exactly like keyManager. It fetches
// each provider's free-model list asynchronously (never blocks the TUI), lets
// the user toggle models into an ordered pool, and exposes the pool via
// routes(). Selections are kept across fetches so re-toggling a provider never
// loses the user's choices.
type fallbackManager struct {
	list         list.Model
	states       map[string]*providerState
	selected     map[string]bool // "provider\x00model" -> in pool
	order        []string        // selection order, keyed as above
	primaryModel string          // the picker's already-chosen model (suppress dup)
	// allModelsProvider is set only on the background start-screen catalog.
	// Its primary provider loads every model; the pool UI still shows only Free.
	allModelsProvider string
	listReady         bool
	editor            *inlineKeyEditor
	editing           string
	done              bool
	quit              bool
}

// fallbackLoaded is sent when a provider's model fetch (or key resolution)
// completes. err != nil shows a retryable provider row; key == "" with nil
// err means no credential is available (show a key prompt row).
type fallbackLoaded struct {
	provider string
	models   []models.Model
	key      string
	err      error
}

// newFallbackManager builds the screen. primaryModel is the picker's current
// choice ("" if none), so a model that is already the primary is not offered
// as a redundant fallback.
func newFallbackManager(primaryModel string, existing ...[]FreeRoute) fallbackManager {
	states := make(map[string]*providerState, len(poolProviders))
	for _, p := range poolProviders {
		states[p] = &providerState{status: statusLoading}
	}
	m := fallbackManager{
		states:       states,
		selected:     map[string]bool{},
		primaryModel: primaryModel,
	}
	if len(existing) > 0 {
		for _, route := range existing[0] {
			if states[route.Provider] == nil || strings.TrimSpace(route.Model) == "" {
				continue
			}
			k := selKey(route.Provider, route.Model)
			if m.selected[k] {
				continue
			}
			m.selected[k] = true
			m.order = append(m.order, k)
		}
	}
	m.rebuildList()
	return m
}

// providerKey resolves the credential for a pool provider (flag/env/keys store).
// The TUI has no flag access, so only env + keys store apply here.
func providerKey(p string) string {
	if key := models.ProviderKey(p, "", ""); key != "" || p != "opencode-go" {
		return key
	}
	store, err := auth.Load("")
	if err != nil {
		return ""
	}
	key, err := auth.KeyFor(store, "opencode-go")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(key)
}

// Init kicks off every provider's async model fetch.
func (m *fallbackManager) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(poolProviders))
	for _, p := range poolProviders {
		if m.states[p].status == statusLoading {
			cmds = append(cmds, m.fetch(p))
		}
	}
	return tea.Batch(cmds...)
}

// seedProvider reuses the primary provider list that main already fetched, so
// the start screen can show it immediately without a duplicate request. An
// empty seed list is NOT silently hidden: it keeps the provider fetchable so
// Init/refresh can retry it, instead of leaving the provider permanently
// skipped (the statusHidden trap that hid every free model after a transient
// empty catalog).
func (m *fallbackManager) seedProvider(provider string, list []models.Model) {
	st := m.states[provider]
	if st == nil {
		return
	}
	st.status = statusReady
	st.key = "available"
	st.models = append(st.models[:0], list...)
	if len(st.models) == 0 {
		st.status = statusLoading
	}
	m.rebuildList()
}

type availableProviderModel struct {
	Provider string
	Model    models.Model
}

func (m *fallbackManager) availableModels() []availableProviderModel {
	var out []availableProviderModel
	for _, provider := range poolProviders {
		st := m.states[provider]
		if st == nil || st.status != statusReady {
			continue
		}
		for _, model := range st.models {
			out = append(out, availableProviderModel{Provider: provider, Model: model})
		}
	}
	return out
}

func (m *fallbackManager) loading() bool {
	for _, provider := range poolProviders {
		if st := m.states[provider]; st != nil && st.status == statusLoading {
			return true
		}
	}
	return false
}

// availableRoutes returns every discovered free model, not just selected pool
// members. The parent selector uses this catalog to put all configured
// providers on its opening screen.
func (m *fallbackManager) availableRoutes() []FreeRoute {
	var out []FreeRoute
	for _, provider := range poolProviders {
		st := m.states[provider]
		if st == nil || st.status != statusReady {
			continue
		}
		for _, model := range st.models {
			if model.Free {
				out = append(out, FreeRoute{Provider: provider, Model: model.ID})
			}
		}
	}
	return out
}

// statusRows returns an informative row for every pool provider that has not
// reached statusReady, so the start screen explains what is loading, which
// provider needs a key, and which errored — instead of silently omitting them.
func (m *fallbackManager) statusRows() []list.Item {
	var out []list.Item
	for _, provider := range poolProviders {
		st := m.states[provider]
		if st == nil {
			continue
		}
		switch st.status {
		case statusLoading:
			out = append(out, providerStatusItem{provider: provider, kind: "loading"})
		case statusKeyless:
			out = append(out, providerStatusItem{provider: provider, kind: "keyless"})
		case statusError:
			out = append(out, providerStatusItem{provider: provider, kind: "error", detail: st.err})
		}
	}
	return out
}

// refreshCredentials retries providers that were missing a key, failed to
// load, or were left statusLoading (e.g. an empty seed that got retried). It is
// called after the key manager closes so newly stored keys appear on the start
// screen immediately. statusLoading providers are re-fetched so a transient
// empty catalog recovers once credentials change.
func (m *fallbackManager) refreshCredentials() tea.Cmd {
	var cmds []tea.Cmd
	for _, provider := range poolProviders {
		st := m.states[provider]
		if st == nil || (st.status != statusKeyless && st.status != statusError && st.status != statusLoading) {
			continue
		}
		m.states[provider] = &providerState{status: statusLoading}
		cmds = append(cmds, m.fetch(provider))
	}
	m.rebuildList()
	return tea.Batch(cmds...)
}

// fetchProvider returns a cmd that resolves the provider's key and model list
// and reports back with a fallbackLoaded message. Always sends a message so the
// TUI never hangs on a slow or dead endpoint.
func fetchProvider(provider string) tea.Cmd {
	return func() tea.Msg {
		return loadProvider(provider)
	}
}

func (m *fallbackManager) fetch(provider string) tea.Cmd {
	if provider == m.allModelsProvider && provider == "opencode-go" {
		return func() tea.Msg {
			key := providerKey(provider)
			if key == "" {
				return fallbackLoaded{provider: provider}
			}
			list, err := models.List(&http.Client{Timeout: 4 * time.Second}, key)
			return fallbackLoaded{provider: provider, models: models.FilterUnavailable(provider, list), key: key, err: err}
		}
	}
	return fetchProvider(provider)
}

func loadProvider(provider string) fallbackLoaded {
	key := providerKey(provider)
	if key == "" {
		return fallbackLoaded{provider: provider, key: ""}
	}
	client := &http.Client{Timeout: 4 * time.Second}
	var (
		list []models.Model
		err  error
	)
	switch provider {
	case "openrouter":
		list, err = models.ListOpenRouter(client, key)
	case "opencode-go":
		list, err = models.ListZenFree(client, key)
	default:
		_, ok := models.FreeTierProviders[provider]
		if !ok {
			return fallbackLoaded{provider: provider, err: errUnknownProvider(provider)}
		}
		list, err = models.ListFreeTierProvider(client, provider, key)
	}
	list = models.FilterUnavailable(provider, list)
	return fallbackLoaded{provider: provider, models: list, key: key, err: err}
}

func errUnknownProvider(p string) error { return &unknownProviderError{p: p} }

type unknownProviderError struct{ p string }

func (e *unknownProviderError) Error() string { return "unknown free-tier provider " + e.p }

// key returns the selection key for a provider/model pair.
func selKey(provider, model string) string { return provider + "\x00" + model }

// rebuildList renders the current state as list rows.
func (m *fallbackManager) rebuildList() tea.Cmd {
	selected, hadSelection := fallbackRow{}, false
	if m.listReady {
		selected, hadSelection = m.list.SelectedItem().(fallbackRow)
	}
	var items []list.Item
	for _, p := range poolProviders {
		st := m.states[p]
		switch st.status {
		case statusLoading:
			items = append(items, fallbackRow{provider: p, kind: rowLoading})
		case statusKeyless:
			items = append(items, fallbackRow{provider: p, kind: rowNoKey})
		case statusReady:
			for _, model := range st.models {
				if !model.Free {
					continue
				}
				if model.ID == m.primaryModel {
					continue // already the primary; don't offer as fallback
				}
				items = append(items, fallbackRow{
					provider: p,
					modelID:  model.ID,
					kind:     rowModel,
					inPool:   m.selected[selKey(p, model.ID)],
				})
			}
		case statusHidden:
			// skip
		case statusError:
			items = append(items, fallbackRow{provider: p, kind: rowError, detail: st.err})
		}
	}
	if !m.listReady {
		l := list.New(items, list.NewDefaultDelegate(), 60, 20)
		l.Title = "Free rotation pool"
		l.SetShowStatusBar(false)
		l.SetFilteringEnabled(true)
		l.SetShowHelp(false)
		m.list = l
		m.listReady = true
		return nil
	}
	cmd := m.list.SetItems(items)
	if hadSelection {
		m.restoreSelection(selected)
	}
	return cmd
}

type rowKind int

const (
	rowLoading rowKind = iota
	rowNoKey
	rowModel
	rowError
)

// fallbackRow is one row in the fallback list: a loading placeholder, a
// key-prompt row, or a toggleable model.
type fallbackRow struct {
	provider string
	modelID  string
	kind     rowKind
	inPool   bool
	detail   string
}

func (r fallbackRow) Title() string {
	switch r.kind {
	case rowLoading:
		return "… loading " + r.provider + " models"
	case rowNoKey:
		return r.provider + " — no key, Enter to set"
	case rowError:
		return r.provider + " — unavailable, Enter to retry"
	default:
		mark := "[ ]"
		if r.inPool {
			mark = "[✓]"
		}
		return mark + " " + r.modelID
	}
}
func (r fallbackRow) Description() string {
	switch r.kind {
	case rowLoading:
		return "fetching free models"
	case rowNoKey:
		return "a credential is required to use " + r.provider + " as a fallback"
	case rowError:
		return r.detail
	default:
		if r.inPool {
			return r.provider + " · in pool — Enter to remove"
		}
		return r.provider + " · free — Enter to add to pool"
	}
}
func (r fallbackRow) FilterValue() string {
	return r.provider + " " + r.modelID
}

// selectedProvider returns the provider name for the currently selected row.
func (m *fallbackManager) selectedProvider() string {
	if item, ok := m.list.SelectedItem().(fallbackRow); ok {
		return item.provider
	}
	return ""
}

// toggle flips the selected model's pool membership.
func (m *fallbackManager) toggle() tea.Cmd {
	item, ok := m.list.SelectedItem().(fallbackRow)
	if !ok || item.kind != rowModel {
		return nil
	}
	k := selKey(item.provider, item.modelID)
	if m.selected[k] {
		m.selected[k] = false
		for i, s := range m.order {
			if s == k {
				m.order = append(m.order[:i], m.order[i+1:]...)
				break
			}
		}
	} else {
		m.selected[k] = true
		m.order = append(m.order, k)
	}
	return m.rebuildList()
}

// restoreSelection returns the cursor to an exact row when it still exists,
// falling back to the same provider group when a loading/error row was
// replaced by fetched models.
func (m *fallbackManager) restoreSelection(want fallbackRow) {
	providerIndex := -1
	for i := 0; i < len(m.list.Items()); i++ {
		item, ok := m.list.Items()[i].(fallbackRow)
		if !ok || item.provider != want.provider {
			continue
		}
		if providerIndex == -1 {
			providerIndex = i
		}
		if item.kind == want.kind && item.modelID == want.modelID {
			m.list.Select(i)
			return
		}
	}
	if providerIndex >= 0 {
		m.list.Select(providerIndex)
	}
}

// setKey opens an editor inside the existing Bubble Tea program. Starting a
// second program here would make two event loops compete for the terminal.
func (m *fallbackManager) setKey(provider string) tea.Cmd {
	m.editor = newInlineKeyEditor("Set "+provider+" API key", providerHint(provider), true)
	m.editing = provider
	return textinput.Blink
}

// applyLoad updates state for a completed provider fetch and rebuilds rows.
func (m *fallbackManager) applyLoad(msg fallbackLoaded) tea.Cmd {
	st := m.states[msg.provider]
	if st == nil {
		return nil
	}
	st.key = msg.key
	st.models = nil
	st.err = ""
	switch {
	case msg.err != nil:
		st.status = statusError
		st.err = compactError(msg.err.Error())
	case msg.key == "":
		st.status = statusKeyless
	case len(msg.models) == 0:
		st.status = statusError
		st.err = "no free models returned"
	default:
		st.status = statusReady
		st.models = msg.models
	}
	return m.rebuildList()
}

func compactError(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const limit = 160
	if len(s) > limit {
		return s[:limit-1] + "…"
	}
	return s
}

// routes returns the ordered pool as FreeRoute values.
func (m *fallbackManager) routes() []FreeRoute {
	var out []FreeRoute
	for _, k := range m.order {
		parts := strings.SplitN(k, "\x00", 2)
		if len(parts) != 2 {
			continue
		}
		out = append(out, FreeRoute{Provider: parts[0], Model: parts[1]})
	}
	return out
}

func (m *fallbackManager) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			m.states[provider] = &providerState{status: statusError, err: compactError(err.Error())}
			return m, tea.Batch(cmd, m.rebuildList())
		}
		_ = models.ClearUnavailable(provider)
		if editor.value == "" {
			m.states[provider] = &providerState{status: statusKeyless}
			return m, tea.Batch(cmd, m.rebuildList())
		}
		m.states[provider] = &providerState{status: statusLoading}
		return m, tea.Batch(cmd, m.rebuildList(), m.fetch(provider))
	}
	switch msg := msg.(type) {
	case fallbackLoaded:
		return m, m.applyLoad(msg)
	case tea.WindowSizeMsg:
		m.list.SetSize(max(msg.Width-4, 20), max(msg.Height-7, 8))
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.quit = true
			m.done = true
			return m, tea.Quit
		case "esc":
			m.done = true
			return m, nil
		case "r":
			m.selected = map[string]bool{}
			m.order = nil
			return m, m.rebuildList()
		case "x", "d":
			return m, m.toggle()
		case "enter":
			item, ok := m.list.SelectedItem().(fallbackRow)
			if !ok {
				return m, nil
			}
			switch item.kind {
			case rowNoKey:
				return m, m.setKey(item.provider)
			case rowModel:
				return m, m.toggle()
			case rowError:
				m.states[item.provider] = &providerState{status: statusLoading}
				return m, tea.Batch(m.rebuildList(), m.fetch(item.provider))
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m *fallbackManager) View() string {
	if m.editor != nil {
		return m.editor.View()
	}
	var b string
	b += titleStyle.Render("═══ ultra-zen ═══") + "\n"
	b += subtitleStyle.Render("  Free rotation pool — Enter toggle · x remove · r reset · Esc save & back") + "\n\n"
	b += m.list.View() + "\n"
	if len(m.order) > 0 {
		b += mutedStyle.Render("  pool: "+strings.Join(m.orderKeys(), " → ")) + "\n"
	}
	return b
}

// orderKeys renders the current pool for the footer (provider/model pairs).
func (m *fallbackManager) orderKeys() []string {
	out := make([]string, 0, len(m.order))
	for _, k := range m.order {
		parts := strings.SplitN(k, "\x00", 2)
		if len(parts) == 2 {
			out = append(out, parts[1])
		}
	}
	return out
}
