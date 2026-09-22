// Model search: type a name, see every route to that model across every
// provider ultra-zen can reach, with what each one costs.
//
// The picker's main screen answers "what can I launch"; this screen answers
// the question that comes after it — "this model, where from, and for how
// much". Those are different questions because a model is not one thing: GLM
// 5.2 is reachable through OpenRouter (which itself fans out to thirty
// upstream providers priced from $0.56/M to $1.40/M for the same weights),
// through the Zen go tier against credits, and as a free variant. The main
// list flattens that into one row per provider catalog; this one unflattens
// it.
package tui

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/raketenkater/ultra-zen/internal/models"
)

// searchResultLimit caps how many models one query returns. A two-letter
// query matches most of a 400-model catalog, and a list that long is not a
// result — it is the catalog again, which the user already has one keypress
// away.
const searchResultLimit = 12

// searchEndpointTimeout bounds the per-upstream cost fetch. It is a detail
// panel on an interactive screen: late data is worth less than a screen that
// keeps responding, and the row says "unavailable" rather than hanging.
const searchEndpointTimeout = 6 * time.Second

// searchGroupRow is the heading for one matched model: its name, how many
// routes reach it, and the cheapest published price among them. Inert — the
// routes underneath are the things you can launch.
type searchGroupRow struct{ group models.Group }

func (searchGroupRow) inertRow()             {}
func (i searchGroupRow) FilterValue() string { return "" }
func (i searchGroupRow) Title() string       { return i.group.Title }

// tailParts states how many ways there are to reach this model and what the
// best of them costs. A group with a free route says so outright rather than
// rendering "from $0.000/M": Cheapest() legitimately returns zero there, and
// a price of zero is a fact about the free tier, not a headline rate.
func (i searchGroupRow) tailParts() []string {
	parts := []string{fmt.Sprintf("%d routes", len(i.group.Routes))}
	if len(i.group.Routes) == 1 {
		parts[0] = "1 route"
	}
	switch p, ok := i.group.Cheapest(); {
	case i.group.HasFree():
		parts = append(parts, "free available")
	case ok:
		parts = append(parts, "from "+p.Short(""))
	}
	return parts
}

// searchRouteRow is one launchable route: a provider plus the model id as
// that provider spells it. Both halves matter — the id differs per gateway
// ("z-ai/glm-5.2" vs "glm-5.2"), and it is the id a request must name.
type searchRouteRow struct {
	provider string
	model    models.Model
}

func (i searchRouteRow) Title() string {
	return "  " + i.provider + " " + gDot + " " + i.model.ID
}

func (i searchRouteRow) tailParts() []string {
	return append(tierParts(i.model), ctxPart(i.model.ContextLength)...)
}

func (i searchRouteRow) FilterValue() string { return i.provider + " " + i.model.ID }

// searchEndpointRow is one upstream provider behind an OpenRouter route.
//
// It is inert on purpose. ultra-zen sends its traffic to OpenRouter and
// OpenRouter picks the upstream; the proxy has no provider-pinning field in
// its request path, so a row that took Enter would promise routing control
// this build does not have. The rows are here to answer "what does this
// actually cost and who serves it" — the spread is routinely 5x — not to be
// chosen.
type searchEndpointRow struct{ ep models.Endpoint }

func (searchEndpointRow) inertRow()             {}
func (i searchEndpointRow) FilterValue() string { return "" }
func (i searchEndpointRow) Title() string       { return "    " + gArrow + " " + i.ep.Provider }

// tailParts orders by drop priority: the input rate survives longest because
// it is what the rows are ranked on, then output, then the physical details.
func (i searchEndpointRow) tailParts() []string {
	parts := []string{i.ep.Price.Short("no price")}
	if i.ep.Price.Known && !i.ep.Price.Free() {
		parts = append(parts, "out "+models.Price{PromptUSD: i.ep.Price.CompletionUSD, Known: true}.Short(""))
	}
	if i.ep.ContextLength > 0 {
		parts = append(parts, fmt.Sprintf("%dk", i.ep.ContextLength/1024))
	}
	if i.ep.Quantization != "" {
		parts = append(parts, i.ep.Quantization)
	}
	if i.ep.Uptime30m >= 0 {
		parts = append(parts, fmt.Sprintf("%.0f%%", i.ep.Uptime30m))
	}
	return parts
}

// searchNoteRow is an inert one-line note under a route: the upstream summary
// ("30 providers · 2.5x spread"), a loading marker, or why the fetch failed.
// A failed cost lookup is stated, never silently omitted — an absent
// breakdown would otherwise read as "this model has only one provider".
type searchNoteRow struct {
	text string
	tail string
}

func (searchNoteRow) inertRow()             {}
func (i searchNoteRow) FilterValue() string { return "" }
func (i searchNoteRow) Title() string       { return "    " + i.text }
func (i searchNoteRow) tailParts() []string {
	if i.tail == "" {
		return nil
	}
	return []string{i.tail}
}

// searchCatalogLoaded reports a finished full-catalog load for one provider.
// keyless distinguishes "this provider has no credential" from "the fetch
// failed", because only the first is the user's to fix.
type searchCatalogLoaded struct {
	provider string
	models   []models.Model
	keyless  bool
	err      error
}

// searchEndpointsLoaded reports a finished per-upstream cost fetch.
type searchEndpointsLoaded struct {
	modelID string
	eps     []models.Endpoint
	err     error
}

// searchManager is the search screen. Like keyManager and fallbackManager it
// owns all input while open.
type searchManager struct {
	input  textinput.Model
	list   list.Model
	routes []models.Route // the cross-provider catalog snapshot it searches
	groups []models.Group // results for the current query

	// endpoints caches the per-upstream breakdown by base model id, so
	// re-selecting a result (or retyping a query that matches it) costs
	// nothing. pending guards against firing a second request for a fetch
	// already in flight.
	endpoints map[string][]models.Endpoint
	pending   map[string]bool
	failed    map[string]string
	apiKey    string

	// expanded is the base model id whose upstream breakdown is shown. Only
	// one at a time: every OpenRouter model has tens of endpoints, and
	// expanding them all would bury the results they belong to.
	expanded string

	// catalogs tracks the full per-provider catalogs the screen loads for
	// itself, keyed by provider. The picker's own lists are narrowed for
	// display (free-only tiers, OpenRouter capped to the top hundred paid),
	// so searching them silently misses models the provider serves; these
	// replace them as they arrive.
	catalogs    map[string][]models.Model
	loadingFull map[string]bool
	catalogErrs map[string]string

	// seed is the picker's already-discovered list, kept so the screen is
	// usable on the first keystroke instead of blank until the network
	// answers. A provider's full catalog replaces its seeded rows entirely
	// once it lands.
	seed []models.Route

	choice   string // chosen model id ("" until Enter on a route)
	provider string // provider the chosen model is reached through
	done     bool
	quit     bool
}

func newSearchManager(routes []models.Route, apiKey string) searchManager {
	in := textinput.New()
	in.Prompt = "search " + gDot + " "
	in.PromptStyle = accentStyle
	in.Placeholder = "model name"
	in.CharLimit = 64
	in.Focus()

	// Every provider starts marked loading, before Init has even fired, so
	// the first frame cannot claim a model is missing from a catalog the
	// screen has not read yet.
	loading := make(map[string]bool, len(poolProviders))
	for _, p := range poolProviders {
		loading[p] = true
	}
	m := searchManager{
		input:       in,
		routes:      routes,
		seed:        routes,
		endpoints:   map[string][]models.Endpoint{},
		pending:     map[string]bool{},
		failed:      map[string]string{},
		catalogs:    map[string][]models.Model{},
		loadingFull: loading,
		catalogErrs: map[string]string{},
		apiKey:      apiKey,
	}
	l := list.New(nil, columnDelegate{}, 60, 20)
	configureList(&l)
	// The screen has its own always-on text input, so the list's own filter
	// would be a second, conflicting prompt competing for the same keystrokes.
	l.SetFilteringEnabled(false)
	m.list = l
	m.rebuild()
	return m
}

// Init starts the full-catalog load for every provider. The screen is
// already usable from the seeded list while these run.
func (m *searchManager) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(poolProviders)+1)
	cmds = append(cmds, textinput.Blink)
	for _, p := range poolProviders {
		cmds = append(cmds, fetchFullCatalog(p))
	}
	return tea.Batch(cmds...)
}

// mergeRoutes rebuilds the searchable set: every provider that has reported a
// full catalog contributes that, and the picker's seeded rows stand in for the
// providers still loading. Distinct by provider and model id, which is the
// whole of what a pick carries back.
func (m *searchManager) mergeRoutes() {
	var out []models.Route
	seen := map[string]bool{}
	add := func(provider string, mdl models.Model) {
		key := provider + "\x00" + mdl.ID
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, models.Route{Provider: provider, Model: mdl})
	}
	for _, p := range poolProviders {
		for _, mdl := range m.catalogs[p] {
			add(p, mdl)
		}
	}
	for _, r := range m.seed {
		// A provider whose full catalog arrived is authoritative; its seeded
		// rows are a strict subset and must not resurrect a model the full
		// fetch dropped (an unavailable route, say).
		if _, done := m.catalogs[r.Provider]; done {
			continue
		}
		add(r.Provider, r.Model)
	}
	m.routes = out
}

// stillLoading reports how many provider catalogs have yet to answer.
func (m *searchManager) stillLoading() int { return len(m.loadingFull) }

// rebuild re-runs the query and regenerates the rows. It preserves nothing
// across queries by design: a result list that kept stale rows from a
// previous query would show prices for a model the user is no longer asking
// about.
func (m *searchManager) rebuild() {
	m.groups = models.Search(m.input.Value(), m.routes, searchResultLimit)
	m.setItemsFromGroups()
	m.selectFirstRoute()
}

// upstreamRows renders an OpenRouter route's per-provider cost breakdown,
// when that route is the expanded one. Every other route contributes nothing:
// the breakdown only exists for OpenRouter (it is the only gateway that
// publishes one) and only for the route under the cursor.
func (m *searchManager) upstreamRows(r models.Route) []list.Item {
	if r.Provider != "openrouter" {
		return nil
	}
	id := models.BaseModelID(r.Model.ID)
	if id != m.expanded {
		return nil
	}
	if msg, bad := m.failed[id]; bad {
		return []list.Item{searchNoteRow{text: "upstream costs unavailable", tail: shortErr(msg)}}
	}
	eps, ok := m.endpoints[id]
	if !ok {
		return []list.Item{searchNoteRow{text: "loading upstream costs" + gEll}}
	}
	if len(eps) == 0 {
		return []list.Item{searchNoteRow{text: "no upstream providers reported"}}
	}
	rows := make([]list.Item, 0, len(eps)+1)
	summary := fmt.Sprintf("%d providers", len(eps))
	if len(eps) == 1 {
		summary = "1 provider"
	}
	// The spread rides in the tail, not the label: the name column is capped
	// and truncates from the right, which was eating the number this line
	// exists to deliver ("8 providers · 1.7x spre…").
	spreadText := ""
	if spread, ok := models.PriceSpread(eps); ok {
		spreadText = fmt.Sprintf("%.1fx spread", spread)
	}
	rows = append(rows, searchNoteRow{text: summary, tail: spreadText})
	for _, ep := range eps {
		rows = append(rows, searchEndpointRow{ep: ep})
	}
	return rows
}

// noResultsText distinguishes three different empty states, because they are
// three different problems: nothing to search yet, nothing matched, and
// nothing matched *so far* while catalogs are still arriving. Only the middle
// one can honestly be called a no-match — the others would be ultra-zen
// reporting an absence it has not established.
func (m *searchManager) noResultsText() string {
	query := strings.TrimSpace(m.input.Value())
	switch {
	case len(m.routes) == 0:
		return "no provider catalogs loaded yet" + gEll
	case query == "":
		return "type a model name"
	case m.stillLoading() > 0:
		return fmt.Sprintf("no match yet %s %d catalogs still loading%s", gDot, m.stillLoading(), gEll)
	}
	return "no model matches " + strconv.Quote(query)
}

// shortErr trims a transport error down to something that fits a tail column
// without carrying a URL and a stack of wrapping prefixes.
func shortErr(s string) string {
	if i := strings.LastIndex(s, ": "); i >= 0 && i+2 < len(s) {
		s = s[i+2:]
	}
	// Truncate by runes, not bytes: a byte slice through a multi-byte rune
	// emits invalid UTF-8, which terminals render as a replacement glyph in
	// the middle of an error message.
	if r := []rune(s); len(r) > 40 {
		s = string(r[:39]) + gEll
	}
	return s
}

// selectFirstRoute puts the cursor on the first launchable row, so Enter on a
// fresh query always has a sensible target.
func (m *searchManager) selectFirstRoute() {
	for i, item := range m.list.Items() {
		if !isInert(item) {
			m.list.Select(i)
			return
		}
	}
	m.list.Select(0)
}

// selectedRoute returns the route under the cursor, if the cursor is on one.
func (m *searchManager) selectedRoute() (searchRouteRow, bool) {
	row, ok := m.list.SelectedItem().(searchRouteRow)
	return row, ok
}

// syncExpansion points the upstream breakdown at whatever OpenRouter route
// the cursor is on and, when that model's costs are not cached yet, returns
// the command that fetches them. Tying the fetch to the cursor keeps it to
// one request at a time: fetching every result's breakdown up front would be
// a dozen requests for a list the user scrolls past.
func (m *searchManager) syncExpansion() tea.Cmd {
	row, ok := m.selectedRoute()
	if !ok || row.provider != "openrouter" {
		if m.expanded == "" {
			return nil
		}
		m.expanded = ""
		m.rebuildKeepingCursor()
		return nil
	}
	id := models.BaseModelID(row.model.ID)
	if id == m.expanded {
		return nil
	}
	m.expanded = id
	m.rebuildKeepingCursor()
	if _, done := m.endpoints[id]; done {
		return nil
	}
	if _, bad := m.failed[id]; bad {
		return nil
	}
	if m.pending[id] {
		return nil
	}
	m.pending[id] = true
	return fetchEndpoints(id, m.apiKey)
}

// rebuildKeepingCursor regenerates the rows and restores the cursor to the
// same route. Expanding a breakdown inserts rows above nothing and below the
// cursor, but re-selecting by identity keeps the screen still even if the
// result order ever changes underneath.
func (m *searchManager) rebuildKeepingCursor() {
	row, had := m.selectedRoute()
	m.setItemsFromGroups()
	if !had {
		return
	}
	m.restoreCursor(row)
}

// restoreCursor puts the cursor back on the same route by identity, so rows
// appearing above it (an expanded breakdown, a catalog that just landed)
// never drag the selection somewhere the user did not put it.
func (m *searchManager) restoreCursor(row searchRouteRow) {
	for i, item := range m.list.Items() {
		if r, ok := item.(searchRouteRow); ok && r.provider == row.provider && r.model.ID == row.model.ID {
			m.list.Select(i)
			return
		}
	}
	m.selectFirstRoute()
}

// setItemsFromGroups rebuilds the rows from the current results without
// re-running the query.
func (m *searchManager) setItemsFromGroups() {
	var items []list.Item
	for _, g := range m.groups {
		items = append(items, searchGroupRow{group: g})
		for _, r := range g.Routes {
			items = append(items, searchRouteRow{provider: r.Provider, model: r.Model})
			items = append(items, m.upstreamRows(r)...)
		}
	}
	if len(items) == 0 {
		items = append(items, searchNoteRow{text: m.noResultsText()})
	}
	m.list.SetItems(items)
}

// fetchEndpoints asks OpenRouter which upstreams serve a model and at what
// price. Errors are carried in the message rather than dropped: the screen
// states an unavailable breakdown instead of rendering an empty one.
func fetchEndpoints(modelID, apiKey string) tea.Cmd {
	return func() tea.Msg {
		eps, err := models.ListOpenRouterEndpoints(&http.Client{Timeout: searchEndpointTimeout}, apiKey, modelID)
		return searchEndpointsLoaded{modelID: modelID, eps: eps, err: err}
	}
}

func (m *searchManager) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetWidth(msg.Width - 4)
		m.list.SetHeight(msg.Height - 8)
		return m, nil
	case searchCatalogLoaded:
		delete(m.loadingFull, msg.provider)
		switch {
		case msg.err != nil:
			m.catalogErrs[msg.provider] = shortErr(msg.err.Error())
		case msg.keyless:
			// No credential: not an error, just a provider this install
			// cannot see into. Recorded so the footer can say the search is
			// narrower than "everywhere" rather than implying it is complete.
			m.catalogErrs[msg.provider] = "no key"
		default:
			m.catalogs[msg.provider] = msg.models
		}
		m.mergeRoutes()
		row, had := m.selectedRoute()
		m.groups = models.Search(m.input.Value(), m.routes, searchResultLimit)
		m.setItemsFromGroups()
		if had {
			m.restoreCursor(row)
		} else {
			m.selectFirstRoute()
		}
		return m, m.syncExpansion()
	case searchEndpointsLoaded:
		delete(m.pending, msg.modelID)
		if msg.err != nil {
			m.failed[msg.modelID] = msg.err.Error()
		} else {
			m.endpoints[msg.modelID] = msg.eps
		}
		m.rebuildKeepingCursor()
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.quit, m.done = true, true
			return m, tea.Quit
		case "esc":
			m.done = true
			return m, nil
		case "enter":
			if row, ok := m.selectedRoute(); ok {
				m.choice = row.model.ID
				m.provider = row.provider
				m.done = true
			}
			return m, nil
		case "up", "down", "pgup", "pgdown", "ctrl+u", "ctrl+d":
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			m.skipInert(msg.String())
			return m, tea.Batch(cmd, m.syncExpansion())
		}
		// Everything else is text: the input is the point of the screen.
		before := m.input.Value()
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		if m.input.Value() != before {
			m.rebuild()
			return m, tea.Batch(cmd, m.syncExpansion())
		}
		return m, cmd
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// skipInert moves the cursor off a heading or a cost line in the direction it
// was already travelling, so arrow keys step between launchable routes rather
// than stopping on rows Enter cannot act on.
func (m *searchManager) skipInert(key string) {
	items := m.list.Items()
	if len(items) == 0 || !isInert(items[m.list.Index()]) {
		return
	}
	dir := 1
	if key == "up" || key == "pgup" || key == "ctrl+u" {
		dir = -1
	}
	n := len(items)
	for k := 1; k <= n; k++ {
		i := m.list.Index() + dir*k
		if i < 0 || i >= n {
			continue
		}
		if !isInert(items[i]) {
			m.list.Select(i)
			return
		}
	}
}

func (m searchManager) View() string {
	body := m.input.View() + "\n\n" + m.list.View()
	footer := mutedStyle.Render(strings.Join([]string{
		"type to search", "enter launch", "esc back", "ctrl+c quit",
	}, "  "))
	return frame("search", m.scopeLine(), body, footer, "", m.list.Width()+4)
}

// scopeLine states what the search actually covered, next to the wordmark.
// A search is a claim about absence as much as presence, so the screen says
// how many models it read and names the providers it could not reach —
// otherwise a missing model reads as "not available anywhere" when it really
// means "that provider needs a key" or "that catalog failed to load".
func (m searchManager) scopeLine() string {
	if n := m.stillLoading(); n > 0 {
		return fmt.Sprintf("loading %d catalogs%s", n, gEll)
	}
	line := fmt.Sprintf("%d models", len(m.routes))
	if len(m.catalogErrs) == 0 {
		return line
	}
	missing := make([]string, 0, len(m.catalogErrs))
	for _, p := range poolProviders {
		if _, bad := m.catalogErrs[p]; bad {
			missing = append(missing, p)
		}
	}
	return line + " " + gDot + " not searched: " + strings.Join(missing, ", ")
}
