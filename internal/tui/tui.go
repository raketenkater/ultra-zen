// Package tui is the interactive model selector shown before Claude Code
// launches. It opens on a list of recommended and recently used
// orchestrator/worker combos; choosing one launches immediately. Choosing
// "pick manually" falls through to a two-step flow: pick the orchestrator, then
// optionally pick a worker for background sub-agents.
package tui

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/raketenkater/ultra-zen/internal/models"
)

// "The Column" design tokens: a gray ramp, one cyan accent (the cursor and
// the selected line), one red reserved for errors. No background fills, and
// never Faint — SGR 2 vanishes on some SSH terminals, so dimness is a color
// token, not an attribute. AdaptiveColor pairs keep truecolor/256/16-color
// degradation correct; the ANSI-16 slots (white/black, bright-black,
// cyan/blue, bright-red/red) are position-stable across the ramp.
var (
	colFG     = lipgloss.AdaptiveColor{Light: "#1A1A1A", Dark: "#D6D6D6"}
	colMuted  = lipgloss.AdaptiveColor{Light: "#767676", Dark: "#6C6C6C"}
	colAccent = lipgloss.AdaptiveColor{Light: "#005F87", Dark: "#5FD7FF"}
	colAlert  = lipgloss.AdaptiveColor{Light: "#B00000", Dark: "#E05555"}

	fgStyle         = lipgloss.NewStyle().Foreground(colFG)
	mutedStyle      = lipgloss.NewStyle().Foreground(colMuted)
	accentStyle     = lipgloss.NewStyle().Foreground(colAccent)
	accentBoldStyle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	alertStyle      = lipgloss.NewStyle().Foreground(colAlert)
	matchStyle      = lipgloss.NewStyle().Underline(true)
)

// The glyph set: exactly one icon (the cursor) plus four data glyphs, each
// with a defined meaning. TERM=linux/dumb swaps to ASCII — meaning also
// lives in words and position, never in a glyph alone.
var (
	gCursor  = "❯"
	gDot     = "·"
	gMarkOn  = "×"
	gMarkOff = "·"
	gArrow   = "→"
	gEll     = "…"
)

func init() {
	switch os.Getenv("TERM") {
	case "linux", "dumb":
		gCursor, gDot, gMarkOn, gMarkOff, gArrow, gEll = ">", "-", "x", "-", "->", ".."
	}
}

// tailer is the row contract behind the column layout: Title() is the bare
// identity, tailParts() the facts shown right-aligned in the tail column.
// Parts are ordered by drop priority: when the row runs out of width the
// delegate sheds trailing parts (recent first, then ctx) — the first part,
// the tier word, is never dropped.
type tailer interface {
	list.Item
	Title() string
	tailParts() []string
}

// columnDelegate renders every item as exactly one physical line:
// [gutter][name left-aligned][tail right-aligned]. Height()==1 for all rows,
// headers included, which is what keeps the list's pagination arithmetic
// honest (two-line rows under a uniform-height paginator drift and
// overflow). showMark adds the pool screen's membership glyph, placing names
// at col 4 there and col 2 everywhere else.
type columnDelegate struct {
	showMark bool
}

func (columnDelegate) Height() int                         { return 1 }
func (columnDelegate) Spacing() int                        { return 0 }
func (columnDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (d columnDelegate) gutterWidth() int {
	if d.showMark {
		return 4
	}
	return 2
}

func (d columnDelegate) gutter(selected bool, item list.Item) string {
	if d.showMark {
		mark := gMarkOff
		if row, ok := item.(fallbackRow); ok {
			switch {
			case row.kind != rowModel:
				// Status rows keep the mark slot blank but still take the
				// cursor — Enter on them does something (set a key, retry).
				if selected {
					return gCursor + "   "
				}
				return "    "
			case row.inPool && row.pos >= 1 && row.pos <= 9:
				// Pool members carry their 1-based rotation rank as a digit
				// (the order routes() returns); beyond 9 the membership glyph
				// stands in — two digits would break the 4-col gutter.
				mark = strconv.Itoa(row.pos)
			case row.inPool:
				mark = gMarkOn
			}
		}
		if selected {
			return gCursor + " " + mark + " "
		}
		return "  " + mark + " "
	}
	if selected {
		return gCursor + " "
	}
	return "  "
}

// renderTail assembles the styled tail segment: pad, then the shed parts
// joined by two spaces. The first part is the tier word — "free" renders in
// accent cyan so the free tier pops at a glance, every other part (and the
// pad and joiners) stays muted. It runs after the shed arithmetic on the
// plain strings; ANSI carries zero display width, so per-part styling cannot
// shift a column.
func renderTail(pad string, parts []string) string {
	var b strings.Builder
	b.WriteString(mutedStyle.Render(pad))
	for i, part := range parts {
		if i > 0 {
			b.WriteString(mutedStyle.Render("  "))
		}
		if i == 0 && part == "free" {
			b.WriteString(accentStyle.Render(part))
			continue
		}
		b.WriteString(mutedStyle.Render(part))
	}
	return b.String()
}

func (d columnDelegate) nameCap(listWidth int) int {
	// Budget at 80 cols: gutter 2 + name 44 + gap 2 + tail 28 = listWidth 76.
	// Wider terminals relax the name cap toward 96; nothing drops at 120.
	c := 44 + (listWidth - 76)
	if c > 96 {
		c = 96
	}
	if c < 20 {
		c = 20
	}
	return c
}

func (d columnDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	lw := m.Width()
	if lw <= 0 {
		return
	}
	// Section headers: one muted line, flush col 0, no cursor. The indent
	// delta (col 0 vs col 2) is the only separation they need — no rules,
	// no pills, no blank line above.
	if header, ok := item.(groupHeaderItem); ok {
		fmt.Fprintf(w, "%s", mutedStyle.Render(header.line()))
		return
	}
	t, ok := item.(tailer)
	if !ok {
		return
	}
	isSelected := index == m.Index()
	emptyFilter := m.FilterState() == list.Filtering && m.FilterValue() == ""
	_, isStatus := item.(providerStatusItem)
	_, isFallbackStatus := item.(fallbackRow)
	if fr, ok := item.(fallbackRow); ok && fr.kind != rowModel {
		isFallbackStatus = true
	} else {
		isFallbackStatus = false
	}

	gutter := d.gutter(isSelected && !emptyFilter, item)
	name := ansiTruncate(t.Title(), d.nameCap(lw))
	parts := t.tailParts()
	tail := strings.Join(parts, "  ")

	// Right-align the tail, shedding parts (then the name) until the row
	// fits. The shedding arithmetic works on the plain join above: ANSI
	// sequences carry zero display width, so renderTail()'s per-part styling
	// (below) cannot shift a column.
	gw := d.gutterWidth()
	for len(parts) > 0 && gw+ansi.StringWidth(name)+2+ansi.StringWidth(tail) > lw && len(parts) > 1 {
		parts = parts[:len(parts)-1]
		tail = strings.Join(parts, "  ")
	}
	if nameW := lw - gw - 2 - ansi.StringWidth(tail); ansi.StringWidth(name) > nameW {
		name = ansiTruncate(name, max(nameW, 8))
	}
	pad := strings.Repeat(" ", max(lw-gw-ansi.StringWidth(name)-ansi.StringWidth(tail), 1))

	// Error-state rows (provider unreachable) render the name in red; other
	// non-selectable status rows sit one step dimmer than models. The tail
	// style no longer applies to the whole segment — renderTail colours the
	// tier word individually (free = accent cyan, everything else muted).
	nameStyle := fgStyle
	switch {
	case isStatus:
		nameStyle = mutedStyle
		if st := item.(providerStatusItem); st.kind == "error" {
			nameStyle = alertStyle
		}
	case isFallbackStatus:
		nameStyle = mutedStyle
		if fr := item.(fallbackRow); fr.kind == rowError {
			nameStyle = alertStyle
		}
	}

	var line string
	switch {
	case isSelected && !emptyFilter:
		if m.FilterState() == list.Filtering {
			// Mid-query: underline the matched runes of the name only
			// (stock DefaultDelegate semantics), rest of the line bold-accent.
			matched := m.MatchesForItem(index)
			unmatched := accentBoldStyle.Inline(true)
			matchedStyle := unmatched.Inherit(matchStyle)
			line = accentBoldStyle.Render(gutter) +
				lipgloss.StyleRunes(name, matched, matchedStyle, unmatched) +
				accentBoldStyle.Render(pad+tail)
		} else {
			line = accentBoldStyle.Render(gutter + name + pad + tail)
		}
	case emptyFilter:
		line = mutedStyle.Render(gutter+name) + mutedStyle.Render(pad+tail)
	default:
		line = nameStyle.Render(gutter+name) + renderTail(pad, parts)
	}
	// Exactly one write, no trailing newline: bubbles joins items itself.
	fmt.Fprintf(w, "%s", line)
}

// configureList applies the shared list chrome: the frame owns all titles,
// so the list renders no title bar (the filter prompt still swaps in while
// filtering because showFilter keeps that section live). FilterInput is
// configured after New because list.New bakes Styles.FilterPrompt into the
// textinput at construction.
func configureList(l *list.Model) {
	l.Title = ""
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)
	l.FilterInput.Prompt = "/ "
	l.FilterInput.PromptStyle = accentStyle
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
	// Bare identity: the friendly Name when set, the id otherwise. Tier and
	// recency live in the tail column, never in the title.
	if i.m.Name != "" && i.m.Name != i.m.ID {
		return i.m.Name
	}
	return i.m.ID
}

var (
	baseOpenRouter  = "https://openrouter.ai/api/v1"
	baseModelScope  = "https://api-inference.modelscope.cn/v1"
	baseGroq        = "https://api.groq.com/openai/v1"
	baseCerebras    = "https://api.cerebras.ai/v1"
	baseHuggingFace = "https://router.huggingface.co/v1"
	baseCohere      = "https://api.cohere.ai/compatibility/v1"
)

// tailParts renders the tier word first (it is never dropped), then context,
// then recency (shed first under width pressure).
func (i modelItem) tailParts() []string {
	parts := []string{"paid"}
	if i.m.Free {
		parts[0] = "free"
	}
	if i.m.ContextLength > 0 {
		parts = append(parts, fmt.Sprintf("%dk", i.m.ContextLength/1024))
	}
	if i.recent {
		parts = append(parts, "recent")
	}
	return parts
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

func (i cycleItem) Title() string { return "free-cycle" }

// tailParts: configured routes count, or the one-word ask ("setup · f") /
// in-progress state. The first route never fits the tail honestly; it lives
// in the pool footer chain instead.
func (i cycleItem) tailParts() []string {
	if i.selected > 0 {
		return []string{fmt.Sprintf("%d routes", i.selected)}
	}
	if i.available == 0 {
		if i.loading {
			return []string{"loading" + gEll}
		}
		return []string{"setup " + gDot + " f"}
	}
	return []string{fmt.Sprintf("%d models", i.available)}
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
	if i.model.Name != "" && i.model.Name != i.model.ID {
		return i.model.Name
	}
	return i.model.ID
}

// tailParts mirrors modelItem: tier first, then ctx. The provider never
// appears in the tail on the start screen — the section header above already
// names it (grafted rule: names are stated once).
func (i providerModelItem) tailParts() []string {
	parts := []string{"paid"}
	if i.model.Free {
		parts[0] = "free"
	}
	if i.model.ContextLength > 0 {
		parts = append(parts, fmt.Sprintf("%dk", i.model.ContextLength/1024))
	}
	return parts
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
	if i.provider == "codex-sub" {
		return "codex-sub"
	}
	return i.provider
}

// tailParts states the provider's state in one word; the footer/Enter
// affordance is shared across all status rows so it does not repeat.
func (i providerStatusItem) tailParts() []string {
	switch i.kind {
	case "loading":
		return []string{"loading" + gEll}
	case "keyless":
		return []string{"no key"}
	default:
		return []string{"unavailable"}
	}
}
func (i providerStatusItem) FilterValue() string { return i.provider + " status" }

// groupHeaderItem is a non-selectable one-line section label ("most used · 100")
// rendered flush col 0, muted. It is skipped on Enter (see Update): landing the
// cursor on one moves to the next real row, so headers never get mistaken for
// models and never block selection.
type groupHeaderItem struct {
	label string // e.g. "OpenRouter" or "Most used" (rendered lowercased)
	count int
}

// line is the rendered header text. The label field keeps its original case
// (startItemKey and the grouping tests read it); only the rendering lowercases.
// A zero count (pool-screen provider sections without model rows) renders as
// just the name — "groq · 0" states nothing.
func (i groupHeaderItem) line() string {
	if i.count == 0 {
		return strings.ToLower(i.label)
	}
	return strings.ToLower(i.label) + " " + gDot + " " + strconv.Itoa(i.count)
}
func (i groupHeaderItem) Title() string       { return i.line() }
func (i groupHeaderItem) FilterValue() string { return "" }

func (i comboItem) Title() string {
	if i.manual {
		return "manual"
	}
	if i.combo.Worker == "" || i.combo.Worker == i.combo.Orchestrator {
		return i.combo.Orchestrator
	}
	return i.combo.Orchestrator + " + " + i.combo.Worker
}
func (i comboItem) tailParts() []string {
	if i.manual {
		return nil
	}
	return []string{i.label} // "recent" / "recommended"
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

func (i resumeItem) Title() string { return "resume " + gDot + " " + i.opt.Label }

// tailParts passes the recorded time/agent summary through as-is: short
// data, not prose.
func (i resumeItem) tailParts() []string {
	if i.opt.Description == "" {
		return nil
	}
	return []string{i.opt.Description}
}
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
	// is rendered as a status line in the frame, NOT as a list item — it is
	// informational, never selectable, so it can never be mistaken for a model.
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
	// Providers without a usable model list are scattered nowhere: each kind
	// collapses into one trailing section, provider per row, state in the
	// tail. The user still sees free models exist (loading) or what to fix
	// (keyless/error) without a blank screen implying no providers at all.
	if m.catalog != nil {
		var loadingRows, keylessRows, errorRows []list.Item
		for _, row := range m.catalog.statusRows() {
			if st, ok := row.(providerStatusItem); ok {
				switch st.kind {
				case "loading":
					loadingRows = append(loadingRows, row)
				case "keyless":
					keylessRows = append(keylessRows, row)
				default:
					errorRows = append(errorRows, row)
				}
			}
		}
		for _, bucket := range []struct {
			label string
			rows  []list.Item
		}{
			{"loading providers", loadingRows},
			{"keyless providers", keylessRows},
			{"unavailable providers", errorRows},
		} {
			if len(bucket.rows) > 0 {
				items = append(items, groupHeaderItem{label: bucket.label, count: len(bucket.rows)})
				items = append(items, bucket.rows...)
			}
		}
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

func (i fastItem) Title() string { return i.mode }

// tailParts replaces the deleted sentence descriptions with the plain-spoken
// vocabulary: auto is the recommendation, none is what it does.
func (i fastItem) tailParts() []string {
	if i.mode == "auto" {
		return []string{"recommended"}
	}
	return []string{"all tiers"}
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
		// While the filter prompt is active, k/f/esc are TEXT the user is
		// typing (filtering for "sk-..." or a model with a k in it) or the
		// cancel-filter key — they must reach the list, not open screens.
		// Outside filtering the bindings behave exactly as before.
		filtering := m.list.FilterState() == list.Filtering
		switch msg.String() {
		case "k":
			if !filtering && (m.step == stepCombo || m.step == stepOrchestrator || m.step == stepWorker || m.step == stepFast) {
				km := newKeyManager()
				m.keys = &km
				m.prevStep = m.step
				m.step = stepKeys
				return m, nil
			}
		case "f":
			if !filtering && (m.step == stepCombo || m.step == stepOrchestrator || m.step == stepWorker || m.step == stepFast) {
				return m, m.openFallbacks()
			}
		case "ctrl+c":
			m.quit = true
			return m, tea.Quit
		case "esc":
			if !filtering && m.step == stepWorker {
				m.worker = ""
				m.fast = ""
				return m, tea.Quit
			}
			if !filtering && m.step == stepFast {
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

// frame renders the one-line chrome every screen shares: a wordmark line
// ("ultra-zen · <context>") with the usage summary right-aligned on it when
// the terminal is wide enough, the body, an optional red error line, and the
// key-hint footer. All explanatory sentences live in --help, not here.
func frame(ctx, usage, body, footer, errLine string, termWidth int) string {
	mark := fgStyle.Render("ultra-zen")
	if ctx != "" {
		mark += mutedStyle.Render(" " + gDot + " " + ctx)
	}
	folds := usage != "" && ansi.StringWidth(mark)+2+ansi.StringWidth(usage) <= termWidth
	line1 := mark
	if folds {
		pad := max(termWidth-ansi.StringWidth(mark)-ansi.StringWidth(usage), 2)
		line1 += strings.Repeat(" ", pad) + mutedStyle.Render(usage)
	}
	var b strings.Builder
	b.WriteString(line1 + "\n")
	if usage != "" && !folds {
		// Too wide to share the wordmark line: give the usage summary its own.
		b.WriteString(mutedStyle.Render(usage) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\n")
	}
	if errLine != "" {
		b.WriteString("\n" + alertStyle.Render("error: "+errLine) + "\n")
	}
	if footer != "" {
		b.WriteString("\n" + footer + "\n")
	}
	return b.String()
}

// stepFooter joins the key hints in muted gray, two spaces apart — no dot
// chain. The per-step Esc hint (skip/auto) is the only variation.
func stepFooter(step step) string {
	parts := []string{"/ filter", "enter select"}
	switch step {
	case stepWorker:
		parts = append(parts, "esc skip")
	case stepFast:
		parts = append(parts, "esc auto")
	}
	parts = append(parts, "k keys", "f pool", "ctrl+c quit")
	return mutedStyle.Render(strings.Join(parts, "  "))
}

// frameContext is the wordmark's suffix for the current step: where you are,
// in the terms of what you already picked.
func (m model) frameContext() string {
	switch m.step {
	case stepCombo:
		return m.provider
	case stepOrchestrator:
		return "orchestrator"
	case stepWorker:
		return "worker " + gDot + " " + m.choice
	case stepFast:
		ctx := "fast " + gDot + " " + m.choice
		if m.worker != "" {
			ctx += " + " + m.worker
		}
		return ctx
	}
	return m.subtitle
}

func (m model) View() string {
	width := m.list.Width() + 4
	usage := ""
	if m.usage != nil && m.list.FilterState() != list.Filtering {
		usage = usageSummaryText(m.usage)
	}
	switch m.step {
	case stepCombo, stepOrchestrator, stepWorker, stepFast:
		return frame(m.frameContext(), usage, m.list.View(), stepFooter(m.step), m.poolErr, width)
	case stepKeys:
		if m.keys != nil {
			return m.keys.View()
		}
		return frame(m.frameContext(), "", m.list.View(), stepFooter(m.step), "", width)
	case stepFallbacks:
		if m.fallbacks != nil {
			return m.fallbacks.View()
		}
		return frame(m.frameContext(), "", m.list.View(), stepFooter(m.step), "", width)
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

	l := list.New(items, columnDelegate{}, 60, 20)
	configureList(&l)

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
