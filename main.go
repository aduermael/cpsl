package main

import (
	"fmt"
	"image/color"
	"log"
	"os"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
	"github.com/rivo/uniseg"
)

const (
	minInputHeight = 1
	maxInputHeight = 12
)

// Purple gradient colors for the logo (smooth dark-to-light-to-dark)
var logoColors = []color.Color{
	lipgloss.Color("#3A0066"),
	lipgloss.Color("#5B1A99"),
	lipgloss.Color("#7B3EC7"),
	lipgloss.Color("#9B6ADE"),
	lipgloss.Color("#B88AFF"),
	lipgloss.Color("#9B6ADE"),
}

var logoLines = []string{
	" ██████╗ ██████╗  ███████╗██╗     ",
	"██╔════╝ ██╔══██╗ ██╔════╝██║     ",
	"██║      ██████╔╝ ███████╗██║     ",
	"██║      ██╔═══╝  ╚════██║██║     ",
	"╚██████╗ ██║      ███████║███████╗",
	" ╚═════╝ ╚═╝      ╚══════╝╚══════╝",
}

func renderLogo() string {
	var rendered []string
	for i, line := range logoLines {
		c := logoColors[i%len(logoColors)]
		style := lipgloss.NewStyle().Foreground(c).Bold(true)
		rendered = append(rendered, style.Render(line))
	}
	return strings.Join(rendered, "\n")
}

var borderGradientColors = []color.Color{
	lipgloss.Color("#6B34B0"),
	lipgloss.Color("#9B82F5"),
	lipgloss.Color("#B8A9FF"),
	lipgloss.Color("#9B82F5"),
	lipgloss.Color("#6B34B0"),
}

type message struct {
	content     string
	isPaste     bool
	charCount   int
	pasteNumber int
}

type model struct {
	textarea     textarea.Model
	viewport     viewport.Model
	width        int
	height       int
	messages     []message
	ready        bool
	config       Config
	pendingPaste bool
	pasteCount   int
}

func initialModel() model {
	ta := textarea.New()
	ta.Placeholder = "Type a message..."
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.SetHeight(minInputHeight)
	ta.CharLimit = 0
	ta.MaxHeight = 0 // no limit on content; we control visual height ourselves
	ta.SetVirtualCursor(false)
	ta.EndOfBufferCharacter = ' '

	// Enter sends the message. Shift+Enter or Alt+Enter inserts a newline.
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("shift+enter", "alt+enter"),
		key.WithHelp("shift+enter", "new line"),
	)

	s := ta.Styles()
	s.Focused.CursorLine = lipgloss.NewStyle()
	s.Focused.Base = lipgloss.NewStyle()
	s.Focused.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("#666666"))
	s.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color("#E0E0E0"))
	s.Blurred.CursorLine = lipgloss.NewStyle()
	s.Blurred.Base = lipgloss.NewStyle()
	ta.SetStyles(s)
	ta.Focus()

	cfg, err := loadConfig()
	if err != nil {
		log.Printf("warning: loading config: %v (using defaults)", err)
	}

	return model{
		textarea: ta,
		messages: []message{},
		config:   cfg,
	}
}

func (m model) Init() tea.Cmd {
	return textarea.Blink
}

// wrapLineCount reproduces the textarea's internal wrap() function exactly
// to count how many display lines a single logical line produces at the given width.
// This must stay in sync with charm.land/bubbles/v2/textarea wrap().
func wrapLineCount(line string, width int) int {
	if width <= 0 {
		return 1
	}
	runes := []rune(line)
	if len(runes) == 0 {
		return 1
	}

	var (
		lines  = [][]rune{{}}
		word   = []rune{}
		row    int
		spaces int
	)

	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}

		if spaces > 0 {
			if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces > width {
				row++
				lines = append(lines, []rune{})
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], []rune(strings.Repeat(" ", spaces))...)
				spaces = 0
				word = nil
			} else {
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], []rune(strings.Repeat(" ", spaces))...)
				spaces = 0
				word = nil
			}
		} else if len(word) > 0 {
			lastCharLen := uniseg.StringWidth(string(word[len(word)-1:]))
			if uniseg.StringWidth(string(word))+lastCharLen > width {
				if len(lines[row]) > 0 {
					row++
					lines = append(lines, []rune{})
				}
				lines[row] = append(lines[row], word...)
				word = nil
			}
		}
	}

	if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces >= width {
		lines = append(lines, []rune{})
	}

	return len(lines)
}

// displayLineCount calculates the total visual display lines for the textarea content.
func (m model) displayLineCount() int {
	val := m.textarea.Value()
	if val == "" {
		return 1
	}
	width := m.textarea.Width()
	if width <= 0 {
		return m.textarea.LineCount()
	}
	logicalLines := strings.Split(val, "\n")
	total := 0
	for _, line := range logicalLines {
		total += wrapLineCount(line, width)
	}
	return total
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		inputAreaWidth := m.width - 2 // border left + right
		if inputAreaWidth < 1 {
			inputAreaWidth = 1
		}
		m.textarea.SetWidth(inputAreaWidth)

		if !m.ready {
			m.viewport = viewport.New(
				viewport.WithWidth(m.width),
				viewport.WithHeight(m.viewportHeight()),
			)
			m.viewport.KeyMap.Up.SetEnabled(false)
			m.viewport.KeyMap.Down.SetEnabled(false)
			m.viewport.KeyMap.Left.SetEnabled(false)
			m.viewport.KeyMap.Right.SetEnabled(false)
			m.viewport.KeyMap.PageUp.SetEnabled(false)
			m.viewport.KeyMap.PageDown.SetEnabled(false)
			m.viewport.KeyMap.HalfPageUp.SetEnabled(false)
			m.viewport.KeyMap.HalfPageDown.SetEnabled(false)
			m.ready = true
		} else {
			m.viewport.SetWidth(m.width)
			m.viewport.SetHeight(m.viewportHeight())
		}
		m.recalcTextareaHeight()
		m.updateViewportContent()

	case tea.PasteMsg:
		if len(msg.Content) >= m.config.PasteCollapseMinChars {
			m.pendingPaste = true
		}

	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "enter":
			val := strings.TrimSpace(m.textarea.Value())
			if val != "" {
				msg := message{
					content:   val,
					isPaste:   m.pendingPaste,
					charCount: len(val),
				}
				if m.pendingPaste {
					m.pasteCount++
					msg.pasteNumber = m.pasteCount
				}
				m.pendingPaste = false
				m.messages = append(m.messages, msg)
				m.textarea.Reset()
				m.textarea.SetHeight(minInputHeight)
				if m.ready {
					m.viewport.SetHeight(m.viewportHeight())
					m.updateViewportContent()
					m.viewport.GotoBottom()
				}
			}
			return m, nil
		}
	}

	// Temporarily expand textarea to max height before Update so the
	// textarea's internal repositionView() doesn't scroll within a too-small
	// viewport. We'll shrink to the real height right after.
	m.textarea.SetHeight(maxInputHeight)

	// Update textarea
	var taCmd tea.Cmd
	m.textarea, taCmd = m.textarea.Update(msg)
	cmds = append(cmds, taCmd)

	// Now set the correct height based on actual content
	m.recalcTextareaHeight()

	// Update viewport
	if m.ready {
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		cmds = append(cmds, vpCmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *model) recalcTextareaHeight() {
	newHeight := m.displayLineCount()
	if newHeight < minInputHeight {
		newHeight = minInputHeight
	}
	if newHeight > maxInputHeight {
		newHeight = maxInputHeight
	}
	m.textarea.SetHeight(newHeight)
	if m.ready {
		m.viewport.SetHeight(m.viewportHeight())
	}
}

func (m model) inputBoxHeight() int {
	return m.textarea.Height() + 2 // top + bottom border
}

func (m model) viewportHeight() int {
	h := m.height - m.inputBoxHeight()
	if h < 1 {
		h = 1
	}
	return h
}

func (m *model) updateViewportContent() {
	if !m.ready {
		return
	}

	logo := renderLogo()
	logoHeight := lipgloss.Height(logo)

	var content string
	if len(m.messages) == 0 {
		vpHeight := m.viewportHeight()
		padding := (vpHeight - logoHeight) / 2
		if padding < 0 {
			padding = 0
		}
		centeredLogo := lipgloss.PlaceHorizontal(m.width, lipgloss.Center, logo)
		content = strings.Repeat("\n", padding) + centeredLogo
	} else {
		centeredLogo := lipgloss.PlaceHorizontal(m.width, lipgloss.Center, logo)

		msgStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E0E0E0")).
			PaddingLeft(2)

		pasteHeaderStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666")).
			PaddingLeft(2)

		var parts []string
		parts = append(parts, centeredLogo, "")
		for _, msg := range m.messages {
			if msg.isPaste && msg.charCount >= m.config.PasteCollapseMinChars {
				header := fmt.Sprintf("[pasted text #%d | %d chars]", msg.pasteNumber, msg.charCount)
				parts = append(parts, pasteHeaderStyle.Render(header))
			}
			wrapped := lipgloss.NewStyle().Width(m.width - 4).Render(msg.content)
			parts = append(parts, msgStyle.Render(wrapped), "")
		}
		content = strings.Join(parts, "\n")
	}

	m.viewport.SetContent(content)
}

func (m model) View() tea.View {
	if !m.ready {
		v := tea.NewView("Initializing...")
		v.AltScreen = true
		return v
	}

	inputBorderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForegroundBlend(borderGradientColors...).
		Width(m.width)

	inputBox := inputBorderStyle.Render(m.textarea.View())

	fullView := m.viewport.View() + "\n" + inputBox

	v := tea.NewView(fullView)

	c := m.textarea.Cursor()
	if c != nil {
		c.Y += m.viewport.Height() + 1 // +1 for top border
		c.X += 1                        // +1 for left border
		v.Cursor = c
	}

	v.AltScreen = true
	return v
}

func main() {
	p := tea.NewProgram(initialModel())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
