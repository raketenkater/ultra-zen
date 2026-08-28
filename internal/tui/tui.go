// Package tui is the interactive model selector shown before Claude Code
// launches. It opens on a list of recommended and recently used
// orchestrator/worker combos; choosing one launches immediately. Choosing
// "pick manually" falls through to a two-step flow: pick the orchestrator, then
// optionally pick a worker for background sub-agents.
package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/raketenkater/ultra-zen/internal/models"
)

var (
	// Brand accent: violet, used for the wordmark, selection, and section rules.
	accent = lipgloss.Color("#A78BFA")
	// Palette: one accent, then a quiet gray ramp. Nothing else competes.
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(accent)
	subtitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
	mutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	recentStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#5EEAD4"))
	// crumbStyle renders the already-chosen steps (orchestrator/worker) as
	// breadcrumbs on later steps.
	crumbStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#C4B5FD"))
	// usageBannerStyle renders the launch-time per-provider usage summary as a
	// status banner above the model list. It is informational, never selectable,
	// so it can never be mistaken for a model.
	usageBannerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#8B7BD8"))
	// groupHeaderStyle renders a section rule above a provider's model rows:
	// small caps-ish label, a count, and a thin rule — quieter than a filled
	// banner, so sections read as structure rather than as selectable rows.
	groupHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#C4B5FD")).
				Margin(1, 0, 0, 0)
	groupRuleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#3B3245"))
	// freeTagStyle / paidTagStyle add a subtle colored tier marker to a model
	// row's description so free vs paid stays readable at a glance without
	// being noisy. Free = green, paid = muted.
	freeTagStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#4ADE80"))
	paidTagStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
)

// selectedTitleStyle is the selected row's title: bold violet text instead of
// the stock white-on-default block. Padding matches NormalTitle (2 cols) so
// rows never shift horizontally when the cursor moves.
var selectedTitleStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(accent).
	PaddingLeft(2)

// rowIndentStyle indents descriptions to the titles' left edge (2 cols).
var rowIndentStyle = lipgloss.NewStyle().PaddingLeft(2)

// pickerDelegate renders the start-screen list like bubbles' DefaultDelegate,
// but (a) emits descriptions verbatim so the colored free/paid tier tags keep
// their ANSI, (b) renders groupHeaderItem rows as a single styled banner
// with no selection chrome, and (c) restyles the selected/normal rows: the
// selection is a violet bar + bold accent title instead of the stock white
// block, and the description is indented to align with the title text.
// It implements list.ItemDelegate.
type pickerDelegate struct {
	list.DefaultDelegate
}

func (d pickerDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	if header, ok := listItem.(groupHeaderItem); ok {
		fmt.Fprintf(w, "%s", header.Title())
		return
	}
	var (
		title, desc string
		s           = &d.Styles
	)
	if i, ok := listItem.(list.DefaultItem); ok {
		title = i.Title()
		desc = i.Description()
	} else {
		return
	}
	if m.Width() <= 0 {
		return
	}
	textwidth := m.Width() - s.NormalTitle.GetPaddingLeft() - s.NormalTitle.GetPaddingRight()
	isSelected := index == m.Index()
	emptyFilter := m.FilterState() == list.Filtering && m.FilterValue() == ""
	if emptyFilter {
		title = s.DimmedTitle.Render(title)
	} else if isSelected && m.FilterState() != list.Filtering {
		title = selectedTitleStyle.Render(title)
	} else {
		title = s.NormalTitle.Render(title)
	}
	title = ansiTruncate(title, textwidth)
	// The description is emitted verbatim (NOT wrapped in a delegate style): its
	// inner ANSI — the colored free/paid tier tag — must survive, and the muted
	// styling is already baked into Description() itself. Indented to the same
	// left edge as titles: the stock delegate leaves descriptions flush-left,
	// which broke the column alignment.
	desc = rowIndentStyle.Render(desc)
	desc = ansiTruncate(desc, textwidth)
	if d.ShowDescription {
		fmt.Fprintf(w, "%s\n%s", title, desc)
		return
	}
	fmt.Fprintf(w, "%s", title)
}

// ansiTruncate truncates a (possibly ANSI-styled) string to textwidth display
// columns, preserving embedded styling.
func ansiTruncate(s string, width int) string {
	if width <= 0 {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// modelItem is a single model row in the orchestrator/worker lists.
type modelItem struct {
	m      models.Model
	recent bool
}

func (i modelItem) Title() string {
	// Prefer the friendly Name when set; fall back to the id so the row is
	// never blank. The paid/free tier is shown in Description(), not here —
	// tagging the title would double-label rows and break the id-fallback test.
	t := i.m.Name
	if t == "" || t == i.m.ID {
		t = i.m.ID
	}
	if i.m.Free {
		t += "  (free)"
	}
	if i.recent {
		t += "  " + recentStyle.Render("recent")
	}
	return t
}

var (
	baseOpenRouter  = "https://openrouter.ai/api/v1"
	baseModelScope  = "https://api-inference.modelscope.cn/v1"
	baseGroq        = "https://api.groq.com/openai/v1"
	baseCerebras    = "https://api.cerebras.ai/v1"
	baseHuggingFace = "https://router.huggingface.co/v1"
	baseCohere      = "https://api.cohere.ai/compatibility/v1"
)

func (i modelItem) Description() string {
	var tier string
	switch i.m.Base {
	case baseOpenRouter:
		tier = "OpenRouter"
	case baseModelScope:
		tier = "ModelScope"
	case baseGroq:
		tier = "Groq"
	case baseCerebras:
		tier = "Cerebras"
	case baseHuggingFace:
		tier = "HuggingFace"
	case baseCohere:
		tier = "Cohere"
	case models.CodexSubBase:
		tier = "ChatGPT subscription"
	default:
		if i.m.Free {
			tier = "zen free"
		} else {
			tier = "opencode-go"
		}
	}
	if i.m.ContextLength > 0 {
		tier += fmt.Sprintf(" · %dk ctx", i.m.ContextLength/1024)
	}
	// A subtle colored tier tag (free = green, paid = muted) so the free/paid
	// split stays visible at a glance. The delegate renders this description
	// verbatim, preserving the inner ANSI.
	tag := paidTagStyle.Render("paid")
	if i.m.Free {
		tag = freeTagStyle.Render("free")
	}
	return tag + mutedStyle.Render("  "+tier)
}
func (i modelItem) FilterValue() string { return i.m.ID }

// comboItem is a row on the first screen: a preset pairing, a recent pairing,
// or the "pick manually" fall-through (when manual is true).
type comboItem struct {
	combo  models.Combo
	label  string // "recommended" or "recent"
	manual bool
}

// cycleItem makes provider-aware free rotation a first-class launch choice
// instead of a hidden hotkey. Enter opens the pool editor.
type cycleItem struct {
	available int
	selected  int
	first     string
	loading   bool
}

func (i cycleItem) Title() string {
	if i.selected > 0 {
		return fmt.Sprintf("Free cycle ready — %d routes", i.selected)
	}
	return "Free cycle — configure provider rotation →"
}
func (i cycleItem) Description() string {
	if i.selected > 0 {
		return "Enter launch from " + i.first + " · f edits the pool"
	}
	if i.available == 0 {
		if i.loading {
			return "loading configured free providers · Enter to view live status"
		}
		return "no provider models available yet · Enter to add keys"
	}
	return fmt.Sprintf("%d free models discovered · OpenRouter, OpenCode Zen, and BYO free tiers", i.available)
}
func (i cycleItem) FilterValue() string { return "free cycle pool rotation providers" }

// providerModelItem is a model discovered by the background provider catalog.
// Selecting it returns the provider as well as the ID, allowing main to switch
// primary backends without a CLI --provider flag.
type providerModelItem struct {
	provider string
	model    models.Model
}

func (i providerModelItem) Title() string {
	title := i.model.Name
	if title == "" || title == i.model.ID {
		title = i.model.ID
	}
	if i.model.Free {
		title += "  (free)"
	}
	// Paid cross-provider models are reachable via /model + the gateway cache,
	// but not from the TUI picker (which only shows the primary provider's paid
	// models under --all-models). Their tier shows in Description(), not here.
	return title
}
func (i providerModelItem) Description() string {
	// A subtle colored tier tag (free = green, paid = muted) so the free/paid
	// split stays visible at a glance; the delegate renders verbatim, keeping
	// the inner ANSI.
	tag := paidTagStyle.Render("paid")
	if i.model.Free {
		tag = freeTagStyle.Render("free")
	}
	if i.provider == "codex-sub" {
		return tag + mutedStyle.Render("  ChatGPT subscription · auto-detected from the codex CLI login")
	}
	return tag + mutedStyle.Render("  "+i.provider+" · provider discovered automatically")
}
func (i providerModelItem) FilterValue() string {
	return i.provider + " " + i.model.ID
}

// providerStatusItem is an informative, non-selectable row on the start screen
// for a free provider that hasn't produced a model list yet (loading, missing a
// key, or erroring). Selecting it opens the key manager so the user can fix the
// credential instead of seeing a blank list.
type providerStatusItem struct {
	provider string
	kind     string // "loading" | "keyless" | "error"
	detail   string
}

func (i providerStatusItem) Title() string {
	switch i.kind {
	case "loading":
		return i.provider + " — loading free models…"
	case "keyless":
		if i.provider == "codex-sub" {
			return "Codex (ChatGPT sub) — not logged in (run `codex login`)"
		}
		return i.provider + " — no key (Enter to set)"
	default:
		return i.provider + " — unavailable (Enter to retry)"
	}
}
func (i providerStatusItem) Description() string {
	switch i.kind {
	case "loading":
		return "fetching the free-model catalog"
	case "keyless":
		return "a credential is required to use " + i.provider + " free models"
	default:
		if i.detail != "" {
			return i.detail
		}
		return "free models could not be fetched"
	}
}
func (i providerStatusItem) FilterValue() string { return i.provider + " status" }

// usageStatusItem is a non-selectable row on the start screen showing the
// launch-time per-provider usage summary (OpenRouter credits, Zen 5h window,
// etc.). It complements the in-session /v1/usage statusline: the picker runs
// before the proxy exists, so it fetches the same upstream signals directly.
type usageStatusItem struct {
	rows map[string]usageSnapshot
}

func (i usageStatusItem) Title() string {
	if len(i.rows) == 0 {
		return "Usage — fetching…"
	}
	return "Usage — per provider"
}
func (i usageStatusItem) Description() string {
	if len(i.rows) == 0 {
		return "querying OpenRouter / Zen for remaining credits"
	}
	return usageSummaryText(i.rows)
}
func (i usageStatusItem) FilterValue() string { return "usage credits remaining providers" }

// groupHeaderItem is a non-selectable category banner on the start screen that
// labels a block of model rows belonging to one provider (e.g. "OpenRouter · 100").
// It is rendered with groupHeaderStyle and is skipped on Enter (see Update):
// landing the cursor on one moves to the next real row, so headers never get
// mistaken for models and never block selection.
type groupHeaderItem struct {
	label string // e.g. "OpenRouter" or "Most used"
	count int
}

func (i groupHeaderItem) Title() string {
	label := groupHeaderStyle.Render(strings.ToUpper(i.label))
	count := groupRuleStyle.Render(fmt.Sprintf(" %d ", i.count))
	return label + count + " " + groupRuleStyle.Render("────────────")
}
func (i groupHeaderItem) Description() string { return "" }
func (i groupHeaderItem) FilterValue() string { return "" }

func (i comboItem) Title() string {
	if i.manual {
		return "Pick models manually →"
	}
	if i.combo.Worker == "" || i.combo.Worker == i.combo.Orchestrator {
		return i.combo.Orchestrator
	}
	return i.combo.Orchestrator + "  +  " + i.combo.Worker
}
func (i comboItem) Description() string {
	if i.manual {
		return "choose orchestrator and worker yourself"
	}
	if i.combo.Worker == "" || i.combo.Worker == i.combo.Orchestrator {
		return i.label + " · free"
	}
	return i.label + " · orchestrator + worker"
}
func (i comboItem) FilterValue() string {
	return i.combo.Orchestrator + " " + i.combo.Worker
}

// ResumeOption describes a recorded, resumable Claude Code session for the
// current directory, so the picker's opening screen can offer to reopen it
// instead of starting a fresh one.
type ResumeOption struct {
	SessionID   string // session id to resume
	Label       string // e.g. the model it was recorded under
	Description string // e.g. recorded time and cached-agent count
}

// resumeItem is the picker row for a ResumeOption. It only ever appears on
// the opening screen (see Run) — once the user chooses to pick a model
// instead, it does not reappear on later steps.
type resumeItem struct{ opt ResumeOption }

func (i resumeItem) Title() string       { return "↻ Resume: " + i.opt.Label }
func (i resumeItem) Description() string { return i.opt.Description }
func (i resumeItem) FilterValue() string { return "resume " + i.opt.Label }

type step int

const (
	stepCombo step = iota
	stepOrchestrator
	stepWorker
	stepFast
	stepKeys
	stepFallbacks
)

type model struct {
	list        list.Model
	all         []models.Model // for rebuilding lists between steps
	choice      string
	provider    string
	choiceVia   string
	worker      string
	fast        string // chosen small-fast (classifier) model; "" = auto
	resumeID    string
	quit        bool
	subtitle    string
	step        step
	hasCombos   bool
	keys        *keyManager      // non-nil while the key manager screen is open
	fallbacks   *fallbackManager // non-nil while the fallback pool screen is open
	catalog     *fallbackManager // background free-provider discovery
	freePool    []FreeRoute      // configured rotation pool (nil = auto-discover)
	poolTouched bool             // true once the user engages the pool (free cycle / f editor)
	prevStep    step             // step to restore when a sub-screen closes
	resume      *ResumeOption
	poolErr     string
	usage       map[string]usageSnapshot // launch-time per-provider usage (set when usageLoaded arrives)
	allModels   bool                     // --all-models: show paid+free, grouped by tier
}

func (m model) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.catalog != nil {
		cmds = append(cmds, m.catalog.Init())
	}
	// Kick off the launch-time usage fetch (OpenRouter credits, Zen 5h window)
	// alongside catalog discovery. It emits usageLoaded when done, refreshing
	// the picker's usage row.
	cmds = append(cmds, fetchUsage(configuredProviderKeys(m.provider)))
	return tea.Batch(cmds...)
}

func (m *model) openFallbacks() tea.Cmd {
	if m.catalog != nil {
		m.fallbacks = m.catalog
		m.catalog = nil
		m.fallbacks.primaryModel = m.choice
		m.fallbacks.rebuildList()
	} else {
		fm := newFallbackManager(m.choice, m.freePool)
		m.fallbacks = &fm
	}
	m.prevStep = m.step
	m.step = stepFallbacks
	return m.fallbacks.Init()
}

func (m *model) startItems() []list.Item {
	var routes []FreeRoute
	var catalogModels []availableProviderModel
	loading := false
	if m.catalog != nil {
		routes = m.catalog.availableRoutes()
		catalogModels = m.catalog.availableModels()
		loading = m.catalog.loading()
	}
	cycle := cycleItem{available: len(routes), selected: len(m.freePool), loading: loading}
	if len(m.freePool) > 0 {
		cycle.first = m.freePool[0].String()
	}
	items := []list.Item{cycle}
	// Launch-time per-provider usage summary (OpenRouter credits, Zen 5h window)
	// is rendered as a status banner in View(), NOT as a list item — it is
	// informational, never selectable, so it can never be mistaken for a model.
	// Providers that haven't produced a model list yet are still surfaced as
	// status rows so the user sees free models exist but aren't ready — not a
	// blank screen that implies no free provider is configured.
	if m.catalog != nil {
		for _, row := range m.catalog.statusRows() {
			items = append(items, row)
		}
	}
	if m.resume != nil {
		items = append(items, resumeItem{opt: *m.resume})
	}
	// Presets group: recommended + recent combos, then the manual fall-through.
	combos := buildComboItems(m.all)
	var presets []list.Item
	for _, item := range combos {
		if combo, ok := item.(comboItem); ok {
			presets = append(presets, combo)
		}
	}
	if len(presets) > 0 {
		items = append(items, groupHeaderItem{label: "Presets", count: len(presets)})
		items = append(items, presets...)
	}
	// Every model from the initially selected provider is directly launchable;
	// the manual row remains for the legacy orchestrator/worker flow. The free
	// models on the primary provider are always shown (never hidden behind a
	// paid-availability filter) so the free tier is a first-class choice, not
	// something the user can only reach through the Free-cycle pool editor.
	local := make(map[string]bool, len(m.all))
	for _, model := range m.all {
		local[model.ID] = true
	}
	primaryModels := buildModelItems(m.all)
	if len(primaryModels) > 0 {
		items = append(items, groupHeaderItem{label: groupLabel(m.provider), count: len(primaryModels)})
		items = append(items, primaryModels...)
	}
	// Secondary providers are grouped into their own sections, rendered in the
	// stable poolProviders display order. OpenRouter is labeled "Most used"
	// because its catalog is usage-ranked. Provider models that duplicate the
	// primary catalog are skipped so each model shows once; the usage ranking
	// inside OpenRouter is preserved (it is just wrapped in a header).
	secondary := make(map[string][]list.Item)
	for _, option := range catalogModels {
		if option.Provider == m.provider && local[option.Model.ID] {
			continue
		}
		secondary[option.Provider] = append(secondary[option.Provider],
			providerModelItem{provider: option.Provider, model: option.Model})
	}
	for _, p := range poolProviders {
		rows, ok := secondary[p]
		if !ok {
			continue
		}
		items = append(items, groupHeaderItem{label: groupLabel(p), count: len(rows)})
		items = append(items, rows...)
	}
	return items
}

// groupLabel returns the display name for a provider section header. OpenRouter
// is usage-ranked, so its group reads "Most used" rather than just the name;
// every other provider uses its friendly subtitle.
func groupLabel(provider string) string {
	if provider == "openrouter" {
		return "Most used"
	}
	return providerSubtitle(provider)
}

// nextSelectable returns the index of the nearest non-header row at or after
// from+dir (dir +1 downward, -1 upward), or -1 if none exists. It lets the
// picker skip non-selectable groupHeaderItem rows during navigation/selection.
func (m *model) nextSelectable(from, dir int) int {
	items := m.list.Items()
	n := len(items)
	if n == 0 {
		return -1
	}
	for k := 1; k <= n; k++ {
		i := from + dir*k
		if i < 0 || i >= n {
			continue
		}
		if _, ok := items[i].(groupHeaderItem); !ok {
			return i
		}
	}
	return -1
}

// ensureSelectable nudges the cursor off a group header if one is currently
// selected, preferring the next row downward, then upward.
func (m *model) ensureSelectable() {
	items := m.list.Items()
	if len(items) == 0 {
		return
	}
	if _, ok := items[m.list.Index()].(groupHeaderItem); !ok {
		return
	}
	if i := m.nextSelectable(m.list.Index(), 1); i >= 0 {
		m.list.Select(i)
		return
	}
	if i := m.nextSelectable(m.list.Index(), -1); i >= 0 {
		m.list.Select(i)
	}
}

func (m *model) rebuildStart() tea.Cmd {
	if m.step != stepCombo {
		return nil
	}
	index := m.list.Index()
	want := startItemKey(m.list.SelectedItem())
	cmd := m.list.SetItems(m.startItems())
	if want != "" {
		for i, item := range m.list.Items() {
			if startItemKey(item) == want {
				m.list.Select(i)
				m.ensureSelectable()
				return cmd
			}
		}
	}
	if count := len(m.list.Items()); count > 0 {
		m.list.Select(min(index, count-1))
	}
	m.ensureSelectable()
	return cmd
}

func startItemKey(item list.Item) string {
	switch item := item.(type) {
	case cycleItem:
		return "cycle"
	case resumeItem:
		return "resume\x00" + item.opt.SessionID
	case comboItem:
		if item.manual {
			return "manual"
		}
		return "combo\x00" + item.combo.Orchestrator + "\x00" + item.combo.Worker
	case modelItem:
		return "model\x00" + item.m.Base + "\x00" + item.m.ID
	case providerModelItem:
		return "provider\x00" + item.provider + "\x00" + item.model.ID
	case providerStatusItem:
		return "status\x00" + item.provider
	case groupHeaderItem:
		return "header\x00" + item.label + "\x00" + fmt.Sprint(item.count)
	default:
		return ""
	}
}

func (m *model) enterOrchestratorStep() {
	m.step = stepOrchestrator
	m.list.SetItems(buildModelItems(m.all))
	m.list.ResetSelected()
	m.list.ResetFilter()
}

func (m model) enterWorkerStep() (tea.Model, tea.Cmd) {
	items := buildWorkerItems(m.all, m.choice)
	if len(items) == 0 {
		// The chosen orchestrator is the only model; there is no worker to
		// pick. Go straight to the fast step (which always offers auto).
		return m.enterFastStep()
	}
	m.step = stepWorker
	m.list.SetItems(items)
	m.list.ResetSelected()
	m.list.ResetFilter()
	return m, nil
}

// enterFastStep shows the small-fast (classifier) tier picker. The first row
// is always the auto option; the orchestrator itself is excluded (pinning the
// classifier to the main model is what the none row does).
func (m model) enterFastStep() (tea.Model, tea.Cmd) {
	items := buildFastItems(m.all, m.choice)
	m.step = stepFast
	m.list.SetItems(items)
	m.list.ResetSelected()
	m.list.ResetFilter()
	return m, nil
}

// buildFastItems lists candidate small-fast models for the chosen orchestrator.
// Row zero is the auto row (launch-time auto-pick); the "none" row pins every
// tier to the main model. The orchestrator itself is excluded from the model
// rows — that would duplicate "none"'s meaning.
func buildFastItems(ms []models.Model, orchestrator string) []list.Item {
	items := []list.Item{fastItem{mode: "auto"}, fastItem{mode: "none"}}
	recent := models.LoadRecent()
	ordered := models.SortByRecent(ms, recent)
	isRecent := make(map[string]bool, len(recent))
	for _, id := range recent {
		isRecent[id] = true
	}
	for _, mdl := range ordered {
		if mdl.ID == orchestrator {
			continue
		}
		items = append(items, modelItem{m: mdl, recent: isRecent[mdl.ID]})
	}
	return items
}

// fastItem is one of the two special rows at the top of the fast-step list.
type fastItem struct{ mode string }

func (i fastItem) Title() string {
	if i.mode == "auto" {
		return "auto — pick a flash-tier model at launch"
	}
	return "none — run the small-fast tier on the main model"
}

func (i fastItem) Description() string {
	if i.mode == "auto" {
		return "recommended: cheapest-looking model (flash/mini/lite) from the same provider, free variants first"
	}
	return "legacy behavior: every tier on the orchestrator"
}

func (i fastItem) FilterValue() string { return "fast " + i.mode }

// buildWorkerItems lists candidate worker models for the chosen orchestrator.
// The orchestrator itself is excluded (selecting it as its own worker is a
// no-op). Recently used models sort first, same as the main list.
func buildWorkerItems(ms []models.Model, orchestrator string) []list.Item {
	recent := models.LoadRecent()
	ordered := models.SortByRecent(ms, recent)
	isRecent := make(map[string]bool, len(recent))
	for _, id := range recent {
		isRecent[id] = true
	}
	items := make([]list.Item, 0, len(ordered))
	for _, mdl := range ordered {
		if mdl.ID == orchestrator {
			continue
		}
		items = append(items, modelItem{m: mdl, recent: isRecent[mdl.ID]})
	}
	return items
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Provider discovery continues while the start screen or key manager is
	// visible. When the pool editor is open it owns these messages instead.
	if loaded, ok := msg.(fallbackLoaded); ok && m.fallbacks == nil && m.catalog != nil {
		catalogCmd := m.catalog.applyLoad(loaded)
		return m, tea.Batch(catalogCmd, m.rebuildStart())
	}
	// Launch-time usage summary arrived; refresh the usage row in place.
	if ul, ok := msg.(usageLoaded); ok {
		m.usage = ul.rows
		return m, m.rebuildStart()
	}
	// While the key manager is open, it owns all input.
	if m.keys != nil {
		km, cmd := m.keys.Update(msg)
		m.keys = km.(*keyManager)
		if m.keys.done {
			m.quit = m.quit || m.keys.quit
			m.keys = nil
			m.step = m.prevStep // return to the picker screen
			if m.catalog != nil {
				cmd = tea.Batch(cmd, m.catalog.refreshCredentials(), m.rebuildStart())
			}
		}
		return m, cmd
	}
	// While the fallback pool screen is open, it owns all input.
	if m.fallbacks != nil {
		fm, cmd := m.fallbacks.Update(msg)
		m.fallbacks = fm.(*fallbackManager)
		if m.fallbacks.done {
			m.freePool = m.fallbacks.routes()
			m.poolTouched = true // the user edited the pool in the f screen
			m.quit = m.quit || m.fallbacks.quit
			if !m.fallbacks.quit {
				if err := SaveFreePool(m.freePool); err != nil {
					m.poolErr = err.Error()
				} else {
					m.poolErr = ""
				}
			}
			m.catalog = m.fallbacks
			m.fallbacks = nil
			m.step = m.prevStep
			cmd = tea.Batch(cmd, m.rebuildStart())
		}
		return m, cmd
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetWidth(msg.Width - 4)
		m.list.SetHeight(msg.Height - 6)
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "k":
			if m.step == stepCombo || m.step == stepOrchestrator || m.step == stepWorker || m.step == stepFast {
				km := newKeyManager()
				m.keys = &km
				m.prevStep = m.step
				m.step = stepKeys
				return m, nil
			}
		case "f":
			if m.step == stepCombo || m.step == stepOrchestrator || m.step == stepWorker || m.step == stepFast {
				return m, m.openFallbacks()
			}
		case "ctrl+c":
			m.quit = true
			return m, tea.Quit
		case "esc":
			if m.step == stepWorker {
				m.worker = ""
				m.fast = ""
				return m, tea.Quit
			}
			if m.step == stepFast {
				// Esc on the fast step = auto-pick: keep whatever the launch
				// resolves, do not pin "none".
				m.fast = ""
				return m, tea.Quit
			}
		case "enter":
			if item, ok := m.list.SelectedItem().(resumeItem); ok {
				m.resumeID = item.opt.SessionID
				return m, tea.Quit
			}
			// Group headers are non-selectable: landing on one (via keyboard
			// nav) is treated as "move to the next real row" rather than a
			// choice, so a header can never be mistaken for a model.
			if _, ok := m.list.SelectedItem().(groupHeaderItem); ok {
				if i := m.nextSelectable(m.list.Index(), 1); i >= 0 {
					m.list.Select(i)
				}
				return m, nil
			}
			switch m.step {
			case stepCombo:
				if item, ok := m.list.SelectedItem().(cycleItem); ok {
					if item.selected > 0 && len(m.freePool) > 0 {
						// Launching the Free cycle is an explicit engagement of the
						// saved pool — carry it.
						m.poolTouched = true
						m.choice = m.freePool[0].Model
						m.choiceVia = m.freePool[0].Provider
						m.worker = ""
						return m, tea.Quit
					}
					return m, m.openFallbacks()
				}
				if item, ok := m.list.SelectedItem().(modelItem); ok {
					// A concrete model pick must NOT carry the saved free pool —
					// it would silently attach -free fallbacks that get promoted
					// on the first hiccup, running a paid pick on free.
					m.freePool = nil
					m.choice = item.m.ID
					m.choiceVia = m.provider
					m.worker = ""
					return m.enterWorkerStep()
				}
				if item, ok := m.list.SelectedItem().(providerModelItem); ok {
					m.freePool = nil
					m.choice = item.model.ID
					m.choiceVia = item.provider
					m.worker = ""
					return m.enterWorkerStep()
				}
				if _, ok := m.list.SelectedItem().(providerStatusItem); ok {
					// Opening the key manager lets the user fix the credential;
					// a retry of a failed fetch happens via refreshCredentials
					// after the key manager closes.
					km := newKeyManager()
					m.keys = &km
					m.prevStep = m.step
					m.step = stepKeys
					return m, nil
				}
				if item, ok := m.list.SelectedItem().(comboItem); ok {
					if item.manual {
						// A manual pick walks the orchestrator/worker steps — no
						// saved pool fallbacks.
						m.freePool = nil
						m.enterOrchestratorStep()
						return m, nil
					}
					// A concrete combo pick also drops the saved pool: the combo
					// defines its own orchestrator + worker with no free fallbacks.
					m.freePool = nil
					m.choice = item.combo.Orchestrator
					m.choiceVia = m.provider
					m.worker = item.combo.Worker
					return m, tea.Quit
				}
			case stepOrchestrator:
				if item, ok := m.list.SelectedItem().(modelItem); ok {
					m.choice = item.m.ID
					m.choiceVia = m.provider
					return m.enterWorkerStep()
				}
			case stepWorker:
				if item, ok := m.list.SelectedItem().(modelItem); ok {
					m.worker = item.m.ID
					return m.enterFastStep()
				}
			case stepFast:
				if item, ok := m.list.SelectedItem().(fastItem); ok {
					m.fast = item.mode // "" is not stored: "auto"/"none" only
					return m, tea.Quit
				}
				if item, ok := m.list.SelectedItem().(modelItem); ok {
					m.fast = item.m.ID
					return m, tea.Quit
				}
			}
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	// Keep the cursor off non-selectable group headers: after the list has moved
	// the cursor, nudge it past any header it landed on so a section banner is
	// never shown as the "current" row. Runs after the list update, not before.
	if m.step == stepCombo {
		m.ensureSelectable()
	}
	return m, cmd
}

// pickerChrome renders the shared frame: wordmark, then the step-specific
// header block, then the caller's body. Keeping one renderer means every
// screen inherits the same proportions instead of hand-tuned spacing.
func pickerChrome(header string, body string, footer string) string {
	var b string
	b += titleStyle.Render("◆ ultra-zen") + "\n"
	if header != "" {
		b += header + "\n"
	}
	b += "\n"
	b += body
	b += "\n"
	if footer != "" {
		b += footer
	}
	return b
}

// stepFooter renders the key hints, dimming the ones that don't apply to the
// current step so the visible set stays short.
func stepFooter(step step) string {
	parts := []string{"/ filter", "Enter select"}
	switch step {
	case stepWorker:
		parts = append(parts, "Esc skip")
	case stepFast:
		parts = append(parts, "Esc auto")
	}
	parts = append(parts, "k keys", "f pool", "Ctrl+C quit")
	joined := make([]string, 0, len(parts))
	for _, p := range parts {
		joined = append(joined, mutedStyle.Render(p))
	}
	return "  " + strings.Join(joined, mutedStyle.Render(" · ")) + "\n"
}

func (m model) View() string {
	switch m.step {
	case stepCombo:
		var header string
		header += subtitleStyle.Render("  all configured providers — pick a model, combo, or free cycle") + "\n"
		// Launch-time per-provider usage banner (OpenRouter credits, Zen 5h
		// window). Informational only — never selectable, so it cannot be
		// mistaken for a model row. Refreshed when usageLoaded arrives.
		if m.usage != nil {
			header += usageBannerStyle.Render("  "+usageSummaryText(m.usage)) + "\n"
		}
		var body string
		body += m.list.View() + "\n"
		if m.poolErr != "" {
			body += mutedStyle.Render("  could not save free cycle: "+m.poolErr) + "\n"
		}
		return pickerChrome(header, body, stepFooter(stepCombo))
	case stepOrchestrator:
		header := subtitleStyle.Render("  "+m.subtitle) + "\n"
		header += crumbStyle.Render("  main model") + mutedStyle.Render(" › worker › fast") + "\n"
		return pickerChrome(header, m.list.View()+"\n", stepFooter(stepOrchestrator))
	case stepWorker:
		header := crumbStyle.Render("  "+m.choice) + mutedStyle.Render(" › ") + crumbStyle.Render("worker") + mutedStyle.Render(" › fast") + "\n"
		header += subtitleStyle.Render("  worker runs sub-agents in the background") + "\n"
		return pickerChrome(header, m.list.View()+"\n", stepFooter(stepWorker))
	case stepFast:
		header := crumbStyle.Render("  " + m.choice)
		if m.worker != "" {
			header += mutedStyle.Render(" › ") + crumbStyle.Render(m.worker)
		}
		header += mutedStyle.Render(" › ") + crumbStyle.Render("fast") + "\n"
		header += subtitleStyle.Render("  cheap tier for the permission classifier and background calls") + "\n"
		return pickerChrome(header, m.list.View()+"\n", stepFooter(stepFast))
	case stepKeys:
		if m.keys != nil {
			return m.keys.View()
		}
		// Fall through to a safe default if the manager is closed but the
		// step wasn't restored (shouldn't happen — see Update).
		return pickerChrome(subtitleStyle.Render("  "+m.subtitle)+"\n", m.list.View()+"\n", "")
	case stepFallbacks:
		if m.fallbacks != nil {
			return m.fallbacks.View()
		}
		// Fall through to a safe default (shouldn't happen — see Update).
		return pickerChrome(subtitleStyle.Render("  "+m.subtitle)+"\n", m.list.View()+"\n", "")
	}
	return ""
}

// buildModelItems returns model rows with recently used models first.
func buildModelItems(ms []models.Model) []list.Item {
	recent := models.LoadRecent()
	ordered := models.SortByRecent(ms, recent)
	isRecent := make(map[string]bool, len(recent))
	for _, id := range recent {
		isRecent[id] = true
	}
	items := make([]list.Item, 0, len(ordered))
	for _, mdl := range ordered {
		items = append(items, modelItem{m: mdl, recent: isRecent[mdl.ID]})
	}
	return items
}

// buildComboItems returns recent combos first, then recommended combos whose
// models are actually available, then the manual fall-through. Only combos
// whose orchestrator (and worker, if set) exist in ms are shown. A combo with
// an empty or self-paired worker renders identically to the model row itself,
// so only real orchestrator+worker pairings become rows.
func buildComboItems(ms []models.Model) []list.Item {
	avail := make(map[string]bool, len(ms))
	for _, m := range ms {
		avail[m.ID] = true
	}
	comboOK := func(c models.Combo) bool {
		if c.Worker == "" || c.Worker == c.Orchestrator {
			return false
		}
		if !avail[c.Orchestrator] {
			return false
		}
		return avail[c.Worker]
	}
	seen := make(map[string]bool)
	key := func(c models.Combo) string { return c.Orchestrator + "|" + c.Worker }

	var items []list.Item
	for _, c := range models.LoadCombos() {
		if comboOK(c) && !seen[key(c)] {
			seen[key(c)] = true
			items = append(items, comboItem{combo: c, label: "recent"})
		}
	}
	for _, c := range models.RecommendedCombos {
		if comboOK(c) && !seen[key(c)] {
			seen[key(c)] = true
			items = append(items, comboItem{combo: c, label: "recommended"})
		}
	}
	items = append(items, comboItem{manual: true})
	return items
}

func providerSubtitle(provider string) string {
	switch provider {
	case "openrouter":
		return "OpenRouter"
	case "codex":
		return "Codex endpoint"
	case "codex-sub":
		return "Codex (ChatGPT sub)"
	case "groq", "cerebras", "huggingface", "cohere", "modelscope":
		return provider
	case "saia":
		return "GWDG SAIA"
	default:
		return "opencode Zen"
	}
}

// Result is what the selector returns to the caller. Choice is the selected
// model id ("" when quitting/resuming/error). Worker is the chosen worker
// model id ("" if skipped). Fast is the chosen small-fast (classifier) model
// id ("" = auto-pick at launch; the special value "none" pins every tier to
// the main model). ResumeSessionID is non-empty when the user picked a resume
// row. Quit is true when the user Ctrl+C'd. FreePool holds any rotation pool
// configured via the 'f' screen (nil = auto-discover).
type Result struct {
	Choice          string
	Provider        string
	Worker          string
	Fast            string
	ResumeSessionID string
	Quit            bool
	FreePool        []FreeRoute
}

// Run shows the selector and returns the user's choices. The first screen
// lists combos; picking one returns immediately. "Pick manually" leads to
// orchestrator then optional worker selection. The 'f' screen configures the
// free-model rotation pool.
//
// If resume is non-nil, it is shown as an extra row on the opening screen
// only; choosing it sets ResumeSessionID and returns immediately with an
// empty Choice, so the caller can reopen that session instead of launching a
// fresh one.
//
// allModels gates the --all-models catalog: when true every model (paid+free)
// from the primary provider is shown, grouped into free/paid sub-sections with
// tier tags, matching the /model picker. When false only free models show.
func Run(ms []models.Model, provider string, resume *ResumeOption, allModels bool) Result {
	savedPool := LoadFreePool()
	catalog := newFallbackManager("", savedPool)
	catalog.allModelsProvider = provider
	catalog.showAll = allModels
	if len(ms) > 0 {
		catalog.seedProvider(provider, ms)
	}
	m := model{
		all:       ms,
		provider:  provider,
		subtitle:  providerSubtitle(provider),
		step:      stepCombo,
		hasCombos: true,
		catalog:   &catalog,
		resume:    resume,
		freePool:  savedPool,
		allModels: allModels,
	}
	items := m.startItems()

	l := list.New(items, pickerDelegate{DefaultDelegate: list.NewDefaultDelegate()}, 60, 20)
	l.Title = ""
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)

	m.list = l
	p := tea.NewProgram(m)
	res, err := p.Run()
	if err != nil {
		return Result{}
	}
	mm, ok := res.(model)
	if !ok {
		return Result{}
	}
	// The saved pool is only meaningful when the user actually engaged it (Free
	// cycle launch or the f pool editor). A concrete model/combo pick clears the
	// pool, so an explicit paid selection never silently gains -free fallbacks.
	pool := mm.freePool
	if !mm.poolTouched {
		pool = nil
	}
	if mm.resumeID != "" {
		return Result{ResumeSessionID: mm.resumeID, FreePool: pool}
	}
	if mm.quit || mm.choice == "" {
		return Result{Quit: mm.quit, FreePool: pool}
	}
	return Result{Choice: mm.choice, Provider: mm.choiceVia, Worker: mm.worker, Fast: mm.fast, FreePool: pool}
}
