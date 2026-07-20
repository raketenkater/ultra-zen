// Package tui is the interactive model selector shown before Claude Code
// launches. It presents a searchable Bubble Tea list of Zen models grouped
// into the opencode-go tier and the free tier, with arrow-key navigation and
// a filter. Selecting a model returns its id.
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
	recentStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#4EC9B0"))
)

// modelItem adapts models.Model to bubbles/list.Item.
type modelItem struct {
	m      models.Model
	recent bool // in the MRU list; shown with a marker
}

func (i modelItem) Title() string {
	t := i.m.ID
	if i.m.Free {
		t += "  (free)"
	}
	if i.recent {
		t += "  " + recentStyle.Render("● recent")
	}
	return t
}
var (
	baseOpenRouter = "https://openrouter.ai/api/v1"
)

func (i modelItem) Description() string {
	switch {
	case i.m.Base == baseOpenRouter:
		return "OpenRouter · free"
	case i.m.Free:
		return "zen main tier · no credits"
	default:
		return "opencode-go tier"
	}
}
func (i modelItem) FilterValue() string { return i.m.ID }

type model struct {
	list     list.Model
	choice   string
	quit     bool
	subtitle string // provider name for the header
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
	b += subtitleStyle.Render("  " + m.subtitle + " models") + "\n\n"
	b += m.list.View() + "\n"
	b += mutedStyle.Render("  ↑/↓ move · / filter · Enter select · q quit")
	return b
}

// Run shows the selector and returns the chosen model id, or "" if the user
// quit. provider is "opencode-go", "openrouter", or similar — used for the
// subtitle only.
func Run(list_ []models.Model, provider string) (string, error) {
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
	l := list.New(items, list.NewDefaultDelegate(), 60, 20)
	l.Title = ""
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)

	subtitle := "opencode Zen"
	switch provider {
	case "openrouter":
		subtitle = "OpenRouter"
	}

	p := tea.NewProgram(model{list: l, subtitle: subtitle})
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