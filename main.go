package main

import (
	"fmt"
	"image/color"
	"log"
	"os"
	"regexp"
	"strconv"
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

type appMode int

const (
	modeChat appMode = iota
	modeConfig
	modeModel
)

type msgKind int

const (
	msgUser    msgKind = iota // normal user message
	msgError                  // error feedback (e.g. unknown command)
	msgSuccess                // success feedback (e.g. config saved)
	msgInfo                   // informational feedback (e.g. config discarded)
)

type message struct {
	content string
	kind    msgKind
}

// commands is the list of available slash commands.
var commands = []string{"/config"}

// filterCommands returns commands matching the given prefix.
func filterCommands(prefix string) []string {
	var matches []string
	for _, cmd := range commands {
		if strings.HasPrefix(cmd, prefix) {
			matches = append(matches, cmd)
		}
	}
	return matches
}

var pasteplaceholderRe = regexp.MustCompile(`\[pasted #(\d+) \| \d+ chars\]`)

// expandPastes replaces paste placeholders with actual content from the paste store.
func expandPastes(s string, store map[int]string) string {
	return pasteplaceholderRe.ReplaceAllStringFunc(s, func(match string) string {
		sub := pasteplaceholderRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		id, err := strconv.Atoi(sub[1])
		if err != nil {
			return match
		}
		if content, ok := store[id]; ok {
			return content
		}
		return match
	})
}

type model struct {
	textarea   textarea.Model
	viewport   viewport.Model
	width      int
	height     int
	messages   []message
	ready      bool
	config     Config
	pasteCount int
	pasteStore map[int]string // paste ID → actual content
	mode       appMode
	configForm configForm
	modelList  modelList
}

// autocompleteMatches returns matching commands for the current textarea input,
// or nil if autocomplete should not be shown.
func (m model) autocompleteMatches() []string {
	if m.mode != modeChat {
		return nil
	}
	val := m.textarea.Value()
	if !strings.HasPrefix(val, "/") {
		return nil
	}
	return filterCommands(val)
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
	// Config mode: delegate to config form
	if m.mode == modeConfig {
		return m.updateConfigMode(msg)
	}

	var cmds []tea.Cmd
	taMsg := tea.Msg(msg) // message forwarded to textarea (may be modified)

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
			m.pasteCount++
			if m.pasteStore == nil {
				m.pasteStore = make(map[int]string)
			}
			m.pasteStore[m.pasteCount] = msg.Content
			placeholder := fmt.Sprintf("[pasted #%d | %d chars]", m.pasteCount, len(msg.Content))
			taMsg = tea.PasteMsg{Content: placeholder}
		}

	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "tab":
			if matches := m.autocompleteMatches(); len(matches) > 0 {
				m.textarea.SetValue(matches[0])
				m.textarea.CursorEnd()
				m.recalcTextareaHeight()
				if m.ready {
					m.viewport.SetHeight(m.viewportHeight())
				}
			}
			return m, nil
		case "esc":
			if strings.HasPrefix(m.textarea.Value(), "/") {
				m.textarea.Reset()
				m.textarea.SetHeight(minInputHeight)
				if m.ready {
					m.viewport.SetHeight(m.viewportHeight())
				}
			}
			return m, nil
		case "enter":
			val := strings.TrimSpace(m.textarea.Value())
			if val != "" {
				if strings.HasPrefix(val, "/") {
					return m.handleCommand(val)
				}
				content := expandPastes(val, m.pasteStore)
				m.messages = append(m.messages, message{content: content})
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

	// Update textarea (may receive modified PasteMsg with placeholder)
	var taCmd tea.Cmd
	m.textarea, taCmd = m.textarea.Update(taMsg)
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

// enterConfigMode switches to the config editing mode.
func (m model) enterConfigMode() (tea.Model, tea.Cmd) {
	m.mode = modeConfig
	m.configForm = newConfigForm(m.config, m.width, m.height)
	m.textarea.Reset()
	m.textarea.SetHeight(minInputHeight)
	m.textarea.Blur()
	return m, nil
}

// exitConfigMode returns to chat mode, optionally saving config changes.
func (m model) exitConfigMode(save bool) (tea.Model, tea.Cmd) {
	m.mode = modeChat
	if save {
		m.configForm.applyTo(&m.config)
		if err := saveConfig(m.config); err != nil {
			m.messages = append(m.messages, message{
				content: fmt.Sprintf("Error saving config: %v", err),
				kind:    msgError,
			})
		} else {
			m.messages = append(m.messages, message{
				content: "Config saved.",
				kind:    msgSuccess,
			})
		}
	} else {
		m.messages = append(m.messages, message{
			content: "Config changes discarded.",
			kind:    msgInfo,
		})
	}
	if m.ready {
		m.viewport.SetHeight(m.viewportHeight())
		m.updateViewportContent()
		m.viewport.GotoBottom()
	}
	return m, m.textarea.Focus()
}

// updateConfigMode handles input while the config form is active.
func (m model) updateConfigMode(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.configForm.width = msg.Width
		m.configForm.height = msg.Height
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			return m.exitConfigMode(false)
		case "enter":
			if m.configForm.validate() {
				return m.exitConfigMode(true)
			}
			// validation failed — stay in config mode, errors shown
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.configForm, cmd = m.configForm.Update(msg)
	return m, cmd
}

// handleCommand processes slash commands and returns the updated model.
func (m model) handleCommand(input string) (tea.Model, tea.Cmd) {
	cmd := strings.Fields(input)[0] // e.g. "/config"

	switch cmd {
	case "/config":
		return m.enterConfigMode()
	default:
		m.messages = append(m.messages, message{
			content: fmt.Sprintf("Unknown command: %s", cmd),
			kind:    msgError,
		})
		m.textarea.Reset()
		m.textarea.SetHeight(minInputHeight)
		if m.ready {
			m.viewport.SetHeight(m.viewportHeight())
			m.updateViewportContent()
			m.viewport.GotoBottom()
		}
		return m, nil
	}
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

func (m model) autocompleteHeight() int {
	if matches := m.autocompleteMatches(); len(matches) > 0 {
		return len(matches)
	}
	return 0
}

func (m model) viewportHeight() int {
	h := m.height - m.inputBoxHeight() - m.autocompleteHeight()
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

		errorStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF6B6B")).
			PaddingLeft(2).
			Italic(true)

		successStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6FE7B8")).
			PaddingLeft(2).
			Italic(true)

		infoStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8B7BA8")).
			PaddingLeft(2).
			Italic(true)

		var parts []string
		parts = append(parts, centeredLogo, "")
		for _, msg := range m.messages {
			wrapped := lipgloss.NewStyle().Width(m.width - 4).Render(msg.content)
			var style lipgloss.Style
			switch msg.kind {
			case msgError:
				style = errorStyle
			case msgSuccess:
				style = successStyle
			case msgInfo:
				style = infoStyle
			default:
				style = msgStyle
			}
			parts = append(parts, style.Render(wrapped), "")
		}
		content = strings.Join(parts, "\n")
	}

	m.viewport.SetContent(content)
}

func (m model) renderAutocomplete() string {
	matches := m.autocompleteMatches()
	if len(matches) == 0 {
		return ""
	}
	highlightStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#B88AFF")).
		Background(lipgloss.Color("#2A1545")).
		Bold(true).
		PaddingLeft(1).
		PaddingRight(1).
		Width(m.width)
	normalStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#E0E0E0")).
		Background(lipgloss.Color("#2A1545")).
		PaddingLeft(1).
		PaddingRight(1).
		Width(m.width)
	var lines []string
	for i, cmd := range matches {
		if i == 0 {
			lines = append(lines, highlightStyle.Render(cmd))
		} else {
			lines = append(lines, normalStyle.Render(cmd))
		}
	}
	return strings.Join(lines, "\n")
}

func (m model) View() tea.View {
	if !m.ready {
		v := tea.NewView("Initializing...")
		v.AltScreen = true
		return v
	}

	if m.mode == modeConfig {
		return m.viewConfig()
	}

	inputBorderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForegroundBlend(borderGradientColors...).
		Width(m.width)

	inputBox := inputBorderStyle.Render(m.textarea.View())

	autocomplete := m.renderAutocomplete()
	acHeight := m.autocompleteHeight()

	var fullView string
	if autocomplete != "" {
		fullView = m.viewport.View() + "\n" + autocomplete + "\n" + inputBox
	} else {
		fullView = m.viewport.View() + "\n" + inputBox
	}

	v := tea.NewView(fullView)

	c := m.textarea.Cursor()
	if c != nil {
		c.Y += m.viewport.Height() + acHeight + 1 // +1 for top border
		c.X += 1                                    // +1 for left border
		v.Cursor = c
	}

	v.AltScreen = true
	return v
}

func (m model) viewConfig() tea.View {
	formBorder := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForegroundBlend(borderGradientColors...).
		Width(m.width).
		Padding(1, 0)

	formContent := m.configForm.View()
	rendered := formBorder.Render(formContent)

	// Center vertically
	formHeight := lipgloss.Height(rendered)
	padding := (m.height - formHeight) / 2
	if padding < 0 {
		padding = 0
	}

	v := tea.NewView(strings.Repeat("\n", padding) + rendered)
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
