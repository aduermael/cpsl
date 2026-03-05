package main

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
)

type configField struct {
	label string
	desc  string
	input textinput.Model
	err   string
}

type configForm struct {
	fields  []configField
	focused int
	width   int
	height  int
}

// purpleInputStyles returns the shared purple-themed textinput styles.
func purpleInputStyles() textinput.Styles {
	var s textinput.Styles
	s.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color("#E0E0E0"))
	s.Focused.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("#5B1A99"))
	s.Focused.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color("#9B82F5"))
	s.Blurred.Text = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
	s.Blurred.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("#444444"))
	s.Blurred.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color("#555555"))
	s.Cursor.Color = lipgloss.Color("#B88AFF")
	return s
}

// newAPIKeyInput creates a masked textinput for an API key field.
func newAPIKeyInput(value string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "sk-..."
	ti.SetValue(value)
	ti.Prompt = "  "
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '*'
	ti.CharLimit = 256
	ti.SetWidth(40)
	ti.SetStyles(purpleInputStyles())
	return ti
}

func newConfigForm(cfg Config, width, height int) configForm {
	pasteInput := textinput.New()
	pasteInput.Placeholder = "200"
	pasteInput.SetValue(strconv.Itoa(cfg.PasteCollapseMinChars))
	pasteInput.Prompt = "  "
	pasteInput.Focus()
	pasteInput.CharLimit = 10
	pasteInput.SetWidth(20)
	pasteInput.SetStyles(purpleInputStyles())

	return configForm{
		fields: []configField{
			{label: "Paste collapse min chars", desc: "minimum characters to trigger paste collapsing", input: pasteInput},
			{label: "Anthropic API Key", desc: "key for Claude models", input: newAPIKeyInput(cfg.AnthropicAPIKey)},
			{label: "Grok API Key", desc: "key for Grok models", input: newAPIKeyInput(cfg.GrokAPIKey)},
			{label: "OpenAI API Key", desc: "key for GPT models", input: newAPIKeyInput(cfg.OpenAIAPIKey)},
			{label: "Gemini API Key", desc: "key for Gemini models", input: newAPIKeyInput(cfg.GeminiAPIKey)},
		},
		width:  width,
		height: height,
	}
}

func (f configForm) Update(msg tea.Msg) (configForm, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		f.width = msg.Width
		f.height = msg.Height

	case tea.KeyPressMsg:
		switch msg.String() {
		case "tab", "down":
			f.fields[f.focused].input.Blur()
			f.focused = (f.focused + 1) % len(f.fields)
			return f, f.fields[f.focused].input.Focus()
		case "shift+tab", "up":
			f.fields[f.focused].input.Blur()
			f.focused = (f.focused - 1 + len(f.fields)) % len(f.fields)
			return f, f.fields[f.focused].input.Focus()
		}
	}

	var cmd tea.Cmd
	f.fields[f.focused].input, cmd = f.fields[f.focused].input.Update(msg)
	return f, cmd
}

// validate checks all fields and returns true if valid.
func (f *configForm) validate() bool {
	valid := true
	val := strings.TrimSpace(f.fields[0].input.Value())
	n, err := strconv.Atoi(val)
	if err != nil || n < 1 {
		f.fields[0].err = "Must be a positive integer"
		valid = false
	} else {
		f.fields[0].err = ""
	}
	return valid
}

// applyTo writes the form values into the given config.
func (f configForm) applyTo(cfg *Config) {
	n, _ := strconv.Atoi(strings.TrimSpace(f.fields[0].input.Value()))
	cfg.PasteCollapseMinChars = n
	cfg.AnthropicAPIKey = strings.TrimSpace(f.fields[1].input.Value())
	cfg.GrokAPIKey = strings.TrimSpace(f.fields[2].input.Value())
	cfg.OpenAIAPIKey = strings.TrimSpace(f.fields[3].input.Value())
	cfg.GeminiAPIKey = strings.TrimSpace(f.fields[4].input.Value())
}

func (f configForm) View() string {
	titleStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#B88AFF")).
		Bold(true).
		PaddingLeft(2).
		PaddingBottom(1)

	labelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#9B6ADE")).
		Bold(true)

	descStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#666666")).
		Italic(true)

	errStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FF6B6B")).
		PaddingLeft(4)

	hintStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#555555")).
		PaddingLeft(2).
		PaddingTop(1)

	hintKeyStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#7B3EC7"))

	var b strings.Builder
	b.WriteString(titleStyle.Render("⚙ Configuration"))
	b.WriteString("\n")

	for i, field := range f.fields {
		cursor := lipgloss.NewStyle().Foreground(lipgloss.Color("#3A0066")).Render("  ")
		if i == f.focused {
			cursor = lipgloss.NewStyle().Foreground(lipgloss.Color("#B88AFF")).Render("▸ ")
		}
		b.WriteString(cursor)
		b.WriteString(labelStyle.Render(field.label))
		if field.desc != "" {
			b.WriteString("  ")
			b.WriteString(descStyle.Render(field.desc))
		}
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("    %s\n", field.input.View()))
		if field.err != "" {
			b.WriteString(errStyle.Render(field.err))
			b.WriteString("\n")
		}
	}

	hint := fmt.Sprintf(
		"  %s save  %s cancel",
		hintKeyStyle.Render("enter"),
		hintKeyStyle.Render("esc"),
	)
	b.WriteString(hintStyle.Render(hint))
	b.WriteString("\n")

	return b.String()
}
