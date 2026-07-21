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

var baseOpenRouter = "https://openrouter.ai/api/v1"

func (i modelItem) Description() string {
	switch {
	case i.m.Base == baseOpenRouter:
		return "OpenRouter free"
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

type step int

const (
	stepCombo step = iota
	stepOrchestrator
	stepWorker
)

type model struct {
	list      list.Model
	all       []models.Model // for rebuilding lists between steps
	choice    string
	worker    string
	quit      bool
	subtitle  string
	step      step
	hasCombos bool
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
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetWidth(msg.Width - 4)
		m.list.SetHeight(msg.Height - 6)
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.quit = true
			return m, tea.Quit
		case "esc":
			if m.step == stepWorker {
				m.worker = ""
				return m, tea.Quit
			}
		case "enter":
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
		b += mutedStyle.Render("  / filter · Enter select · Ctrl+C quit")
	case stepOrchestrator:
		b += subtitleStyle.Render("  "+m.subtitle+" — pick orchestrator (main model)") + "\n\n"
		b += m.list.View() + "\n"
		b += mutedStyle.Render("  / filter · Enter select · Ctrl+C quit")
	case stepWorker:
		b += subtitleStyle.Render("  orchestrator: "+m.choice) + "\n"
		b += subtitleStyle.Render("  pick worker for sub-agents (Esc to skip)") + "\n\n"
		b += m.list.View() + "\n"
		b += mutedStyle.Render("  / filter · Enter select · Esc skip · Ctrl+C quit")
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
	default:
		return "opencode Zen"
	}
}

// Run shows the selector and returns (orchestrator, worker, quit). The first
// screen lists combos; picking one returns immediately. "Pick manually" leads
// to orchestrator then optional worker selection. worker is "" if none chosen.
func Run(ms []models.Model, provider string) (string, string, bool) {
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
		return "", "", false
	}
	mm, ok := res.(model)
	if !ok || mm.quit || mm.choice == "" {
		return "", "", mm.quit
	}
	return mm.choice, mm.worker, false
}
