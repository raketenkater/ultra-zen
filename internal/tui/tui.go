// Package tui is the interactive model selector shown before Claude Code
// launches. It opens on a list of recommended and recently used
// orchestrator/worker combos; choosing one launches immediately. Choosing
// "pick manually" falls through to a two-step flow: pick the orchestrator, then
// optionally pick a worker for background sub-agents.
package tui

import (
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/raketenkater/ultra-zen/internal/models"
)

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4"))
	subtitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#A0A0A0"))
	mutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#666666"))
	recentStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#4EC9B0"))
)

// modelItem is a single model row in the orchestrator/worker lists.
type modelItem struct {
	m      models.Model
	recent bool
}

func (i modelItem) Title() string {
	t := i.m.ID
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
	switch i.m.Base {
	case baseOpenRouter:
		return "OpenRouter free"
	case baseModelScope:
		return "ModelScope free"
	case baseGroq:
		return "Groq free"
	case baseCerebras:
		return "Cerebras free"
	case baseHuggingFace:
		return "HuggingFace free"
	case baseCohere:
		return "Cohere free"
	}
	switch {
	case i.m.Free:
		return "zen free tier"
	default:
		return "opencode-go tier"
	}
}
func (i modelItem) FilterValue() string { return i.m.ID }

// comboItem is a row on the first screen: a preset pairing, a recent pairing,
// or the "pick manually" fall-through (when manual is true).
type comboItem struct {
	combo  models.Combo
	label  string // "recommended" or "recent"
	manual bool
}

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
	stepKeys
	stepFallbacks
)

type model struct {
	list      list.Model
	all       []models.Model // for rebuilding lists between steps
	choice    string
	worker    string
	resumeID  string
	quit      bool
	subtitle  string
	step      step
	hasCombos bool
	keys      *keyManager      // non-nil while the key manager screen is open
	fallbacks *fallbackManager // non-nil while the fallback pool screen is open
	freePool  []FreeRoute      // configured rotation pool (nil = auto-discover)
	prevStep  step             // step to restore when a sub-screen closes
}

func (m model) Init() tea.Cmd { return nil }

func (m *model) enterOrchestratorStep() {
	m.step = stepOrchestrator
	m.list.SetItems(buildModelItems(m.all))
	m.list.ResetSelected()
	m.list.ResetFilter()
}

func (m *model) enterWorkerStep() {
	m.step = stepWorker
	m.list.SetItems(buildModelItems(m.all))
	m.list.ResetSelected()
	m.list.ResetFilter()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// While the key manager is open, it owns all input.
	if m.keys != nil {
		km, cmd := m.keys.Update(msg)
		m.keys = km.(*keyManager)
		if m.keys.done {
			m.quit = m.quit || m.keys.quit
			m.keys = nil
			m.step = m.prevStep // return to the picker screen
		}
		return m, cmd
	}
	// While the fallback pool screen is open, it owns all input.
	if m.fallbacks != nil {
		fm, cmd := m.fallbacks.Update(msg)
		m.fallbacks = fm.(*fallbackManager)
		if m.fallbacks.done {
			m.freePool = m.fallbacks.routes()
			m.quit = m.quit || m.fallbacks.quit
			m.fallbacks = nil
			m.step = m.prevStep
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
			if m.step == stepCombo || m.step == stepOrchestrator || m.step == stepWorker {
				km := newKeyManager()
				m.keys = &km
				m.prevStep = m.step
				m.step = stepKeys
				return m, nil
			}
		case "f":
			if m.step == stepCombo || m.step == stepOrchestrator || m.step == stepWorker {
				fm := newFallbackManager(m.choice, m.freePool)
				m.fallbacks = &fm
				m.prevStep = m.step
				m.step = stepFallbacks
				return m, m.fallbacks.Init() // kick off async provider fetches
			}
		case "ctrl+c":
			m.quit = true
			return m, tea.Quit
		case "esc":
			if m.step == stepWorker {
				m.worker = ""
				return m, tea.Quit
			}
		case "enter":
			if item, ok := m.list.SelectedItem().(resumeItem); ok {
				m.resumeID = item.opt.SessionID
				return m, tea.Quit
			}
			switch m.step {
			case stepCombo:
				if item, ok := m.list.SelectedItem().(comboItem); ok {
					if item.manual {
						m.enterOrchestratorStep()
						return m, nil
					}
					m.choice = item.combo.Orchestrator
					m.worker = item.combo.Worker
					return m, tea.Quit
				}
			case stepOrchestrator:
				if item, ok := m.list.SelectedItem().(modelItem); ok {
					m.choice = item.m.ID
					m.enterWorkerStep()
					return m, nil
				}
			case stepWorker:
				if item, ok := m.list.SelectedItem().(modelItem); ok {
					m.worker = item.m.ID
					return m, tea.Quit
				}
			}
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m model) View() string {
	var b string
	b += titleStyle.Render("═══ ultra-zen ═══") + "\n"
	switch m.step {
	case stepCombo:
		b += subtitleStyle.Render("  "+m.subtitle+" — pick a combo, or choose manually") + "\n\n"
		b += m.list.View() + "\n"
		b += mutedStyle.Render("  / filter · Enter select · k keys · f pool · Ctrl+C quit")
	case stepOrchestrator:
		b += subtitleStyle.Render("  "+m.subtitle+" — pick orchestrator (main model)") + "\n\n"
		b += m.list.View() + "\n"
		b += mutedStyle.Render("  / filter · Enter select · k keys · f pool · Ctrl+C quit")
	case stepWorker:
		b += subtitleStyle.Render("  orchestrator: "+m.choice) + "\n"
		b += subtitleStyle.Render("  pick worker for sub-agents (Esc to skip)") + "\n\n"
		b += m.list.View() + "\n"
		b += mutedStyle.Render("  / filter · Enter select · Esc skip · k keys · f pool · Ctrl+C quit")
	case stepKeys:
		if m.keys != nil {
			return m.keys.View()
		}
		// Fall through to a safe default if the manager is closed but the
		// step wasn't restored (shouldn't happen — see Update).
		b += subtitleStyle.Render("  "+m.subtitle) + "\n\n"
		b += m.list.View() + "\n"
		return b
	case stepFallbacks:
		if m.fallbacks != nil {
			return m.fallbacks.View()
		}
		// Fall through to a safe default (shouldn't happen — see Update).
		b += subtitleStyle.Render("  "+m.subtitle) + "\n\n"
		b += m.list.View() + "\n"
		return b
	}
	return b
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
// whose orchestrator (and worker, if set) exist in ms are shown.
func buildComboItems(ms []models.Model) []list.Item {
	avail := make(map[string]bool, len(ms))
	for _, m := range ms {
		avail[m.ID] = true
	}
	comboOK := func(c models.Combo) bool {
		if !avail[c.Orchestrator] {
			return false
		}
		return c.Worker == "" || avail[c.Worker]
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
	case "groq", "cerebras", "huggingface", "cohere", "modelscope":
		return provider
	default:
		return "opencode Zen"
	}
}

// Result is what the selector returns to the caller. Choice is the selected
// model id ("" when quitting/resuming/error). Worker is the chosen worker
// model id ("" if skipped). ResumeSessionID is non-empty when the user picked
// a resume row. Quit is true when the user Ctrl+C'd. FreePool holds any
// rotation pool configured via the 'f' screen (nil = auto-discover).
type Result struct {
	Choice          string
	Worker          string
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
func Run(ms []models.Model, provider string, resume *ResumeOption) Result {
	comboItems := buildComboItems(ms)
	// If the only combo item is the manual fall-through, skip straight to
	// manual selection — there are no combos worth showing.
	startStep := stepCombo
	var items []list.Item
	if len(comboItems) <= 1 {
		startStep = stepOrchestrator
		items = buildModelItems(ms)
	} else {
		items = comboItems
	}
	if resume != nil {
		items = append([]list.Item{resumeItem{opt: *resume}}, items...)
	}

	l := list.New(items, list.NewDefaultDelegate(), 60, 20)
	l.Title = ""
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)

	p := tea.NewProgram(model{
		list:      l,
		all:       ms,
		subtitle:  providerSubtitle(provider),
		step:      startStep,
		hasCombos: startStep == stepCombo,
	})
	res, err := p.Run()
	if err != nil {
		return Result{}
	}
	mm, ok := res.(model)
	if !ok {
		return Result{}
	}
	if mm.resumeID != "" {
		return Result{ResumeSessionID: mm.resumeID, FreePool: mm.freePool}
	}
	if mm.quit || mm.choice == "" {
		return Result{Quit: mm.quit, FreePool: mm.freePool}
	}
	return Result{Choice: mm.choice, Worker: mm.worker, FreePool: mm.freePool}
}
