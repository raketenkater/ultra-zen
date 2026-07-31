package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// inlineKeyEditor is the embeddable form of the key prompt. Sub-screens use
// it instead of starting another Bubble Tea program while the picker already
// owns the terminal; nested programs fight over raw mode and can hang or
// corrupt the screen.
type inlineKeyEditor struct {
	input     textinput.Model
	prompt    string
	help      string
	value     string
	done      bool
	cancelled bool
	quit      bool
}

func newInlineKeyEditor(prompt, help string, secret bool) *inlineKeyEditor {
	in := textinput.New()
	in.Focus()
	in.CharLimit = 512
	in.Width = 56
	if secret {
		in.EchoMode = textinput.EchoPassword
		in.EchoCharacter = '•'
	}
	return &inlineKeyEditor{input: in, prompt: prompt, help: help}
}

func (m *inlineKeyEditor) Update(msg tea.Msg) tea.Cmd {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "ctrl+c":
			m.quit = true
			m.cancelled = true
			m.done = true
			return nil
		case "esc":
			m.cancelled = true
			m.done = true
			return nil
		case "enter":
			m.value = strings.TrimSpace(m.input.Value())
			m.done = true
			return nil
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return cmd
}

func (m inlineKeyEditor) View() string {
	b := titleStyle.Render("═══ ultra-zen ═══") + "\n"
	b += subtitleStyle.Render("  "+m.prompt) + "\n\n"
	b += "  " + m.input.View() + "\n\n"
	if m.help != "" {
		b += mutedStyle.Render("  "+m.help) + "\n"
	}
	b += mutedStyle.Render("  Enter to confirm · Esc to cancel")
	return b
}

// keyModel is a single-line prompt for pasting an API key or base URL.
type keyModel struct {
	input  textinput.Model
	prompt string
	help   string
	value  string
	quit   bool
}

func (m keyModel) Init() tea.Cmd { return textinput.Blink }

func (m keyModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "ctrl+c", "esc":
			m.quit = true
			return m, tea.Quit
		case "enter":
			m.value = m.input.Value()
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m keyModel) View() string {
	return inlineKeyEditor{input: m.input, prompt: m.prompt, help: m.help}.View()
}

// PromptKey shows a single-line input and returns what the user typed, or ""
// if they cancelled. secret masks the input (for API keys). help is an
// optional hint line (e.g. where to get a key).
func PromptKey(prompt, help string, secret bool) string {
	in := textinput.New()
	in.Focus()
	in.CharLimit = 512
	in.Width = 56
	if secret {
		in.EchoMode = textinput.EchoPassword
		in.EchoCharacter = '•'
	}
	p := tea.NewProgram(keyModel{input: in, prompt: prompt, help: help})
	res, err := p.Run()
	if err != nil {
		return ""
	}
	mm, ok := res.(keyModel)
	if !ok || mm.quit {
		return ""
	}
	return mm.value
}
