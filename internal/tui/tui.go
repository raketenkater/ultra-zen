// Package tui is the interactive model selector shown before Claude Code
// launches. It is a focused port of ggrun's Bubble Tea main list: a searchable
// list of Zen models grouped into the opencode-go tier and the free tier, with
// arrow-key navigation and a filter. Selecting a model returns its id.
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
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#7D56F4")).Bold(true)
	mutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#666666"))
)

// modelItem adapts models.Model to bubbles/list.Item.
type modelItem struct {
	m models.Model
}

func (i modelItem) Title() string {
	if i.m.Free {
		return i.m.ID + "  (free)"
	}
	return i.m.ID
}
func (i modelItem) Description() string {
	if i.m.Free {
		return "main tier · no credits"
	}
	return "opencode-go tier"
}
func (i modelItem) FilterValue() string { return i.m.ID }

type model struct {
	list   list.Model
	choice string
	quit   bool
}

type selectedMsg struct{ id string }
type quitMsg struct{}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetWidth(msg.Width - 4)
		m.list.SetHeight(msg.Height - 6)
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.quit = true
			return m, tea.Quit
		case "enter":
			if item, ok := m.list.SelectedItem().(modelItem); ok {
				m.choice = item.m.ID
				return m, tea.Quit
			}
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m model) View() string {
	var b = titleStyle.Render("═══ ultra-zen ═══") + "\n"
	b += subtitleStyle.Render("  Pick a Zen model for Claude Code") + "\n\n"
	b += m.list.View() + "\n"
	b += mutedStyle.Render("  ↑/↓ move · / filter · Enter select · q quit")
	return b
}

// Run shows the selector over the given models and returns the chosen model id,
// or "" if the user quit.
func Run(list_ []models.Model) (string, error) {
	items := make([]list.Item, 0, len(list_))
	for _, mdl := range list_ {
		items = append(items, modelItem{m: mdl})
	}
	l := list.New(items, list.NewDefaultDelegate(), 60, 20)
	l.Title = ""
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)

	p := tea.NewProgram(model{list: l})
	res, err := p.Run()
	if err != nil {
		return "", err
	}
	mm, ok := res.(model)
	if !ok {
		return "", nil
	}
	if mm.quit {
		return "", nil
	}
	return mm.choice, nil
}