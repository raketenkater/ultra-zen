// Package tui is the interactive model selector shown before Claude Code
// launches. It runs a two-step flow: first pick the orchestrator (main model),
// then optionally pick a cheaper worker for background sub-agents.
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

// step represents which selection screen is active.
type step int

const (
	stepOrchestrator step = iota
	stepWorker
)

type model struct {
	list     list.Model
	choice   string
	worker   string // set on second step; empty = none
	quit     bool
	subtitle string
	step     step
}

func (m model) Init() tea.Cmd { return nil }

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
				// Skip worker selection.
				m.worker = ""
				return m, tea.Quit
			}
		case "enter":
			if item, ok := m.list.SelectedItem().(modelItem); ok {
				if m.step == stepOrchestrator {
					m.choice = item.m.ID
					m.step = stepWorker
					m.list.ResetSelected()
					return m, nil
				}
				// Worker step: selected a worker.
				m.worker = item.m.ID
				return m, tea.Quit
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
	if m.step == stepOrchestrator {
		b += subtitleStyle.Render("  " + m.subtitle + " — pick orchestrator") + "\n\n"
	} else {
		b += subtitleStyle.Render("  orchestrator: " + m.choice) + "\n"
		b += subtitleStyle.Render("  pick worker (Esc to skip)") + "\n\n"
	}
	b += m.list.View() + "\n"
	b += mutedStyle.Render("  / filter · Enter select · Esc skip worker · Ctrl+C quit")
	return b
}

// buildItems creates list items from models, with recent ones first.
func buildItems(list_ []models.Model) []list.Item {
	recent := models.LoadRecent()
	ordered := models.SortByRecent(list_, recent)
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

func providerSubtitle(provider string) string {
	switch provider {
	case "openrouter":
		return "OpenRouter"
	default:
		return "opencode Zen"
	}
}

// Run shows a two-step selector:
//  1. Pick the orchestrator model (required).
//  2. Pick the worker model (Enter selects, Esc skips).
//
// Returns (orchestrator, worker, quit). worker is "" if skipped.
func Run(list_ []models.Model, provider string) (string, string, bool) {
	items := buildItems(list_)
	l := list.New(items, list.NewDefaultDelegate(), 60, 20)
	l.Title = ""
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)

	p := tea.NewProgram(model{list: l, subtitle: providerSubtitle(provider), step: stepOrchestrator})
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
