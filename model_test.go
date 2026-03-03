package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// keyPress creates a KeyPressMsg for a printable character.
func keyPress(key rune) tea.Msg {
	return tea.KeyPressMsg{Code: key, Text: string(key)}
}

// typeString feeds each rune of s into the model's Update loop.
func typeString(m model, s string) model {
	for _, r := range s {
		result, _ := m.Update(keyPress(r))
		m = result.(model)
	}
	return m
}

// sendKey feeds a single KeyPressMsg into the model.
func sendKey(m model, code rune, mods ...tea.KeyMod) model {
	msg := tea.KeyPressMsg{Code: code}
	for _, mod := range mods {
		msg.Mod |= mod
	}
	result, _ := m.Update(msg)
	return result.(model)
}

// resize sends a WindowSizeMsg and returns the updated model.
func resize(m model, w, h int) model {
	result, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return result.(model)
}

func TestWindowResize(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	if !m.ready {
		t.Fatal("model should be ready after first WindowSizeMsg")
	}
	if m.width != 80 {
		t.Errorf("width = %d, want 80", m.width)
	}
	if m.height != 24 {
		t.Errorf("height = %d, want 24", m.height)
	}

	// Textarea width should be window width minus 2 (border)
	if m.textarea.Width() != 78 {
		t.Errorf("textarea width = %d, want 78", m.textarea.Width())
	}

	// Viewport height = total height - input box height (textarea height + 2 for border)
	expectedVpHeight := 24 - (minInputHeight + 2)
	if m.viewport.Height() != expectedVpHeight {
		t.Errorf("viewport height = %d, want %d", m.viewport.Height(), expectedVpHeight)
	}
}

func TestWindowResizeMultiple(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)
	m = resize(m, 120, 40)

	if m.width != 120 {
		t.Errorf("width = %d, want 120", m.width)
	}
	if m.height != 40 {
		t.Errorf("height = %d, want 40", m.height)
	}
	if m.textarea.Width() != 118 {
		t.Errorf("textarea width = %d, want 118", m.textarea.Width())
	}
}

func TestWindowResizeSmall(t *testing.T) {
	m := initialModel()
	m = resize(m, 10, 5)

	if !m.ready {
		t.Fatal("model should be ready even at small sizes")
	}
	// Viewport height should be at least 1
	if m.viewport.Height() < 1 {
		t.Errorf("viewport height = %d, want >= 1", m.viewport.Height())
	}
}

func TestEnterSendsMessage(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	m = typeString(m, "hello world")
	m = sendKey(m, tea.KeyEnter)

	if len(m.messages) != 1 {
		t.Fatalf("messages count = %d, want 1", len(m.messages))
	}
	if m.messages[0].content != "hello world" {
		t.Errorf("message = %q, want %q", m.messages[0].content, "hello world")
	}
	// Textarea should be cleared after send
	if m.textarea.Value() != "" {
		t.Errorf("textarea should be empty after send, got %q", m.textarea.Value())
	}
}

func TestEnterEmptyDoesNotSend(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	m = sendKey(m, tea.KeyEnter)
	if len(m.messages) != 0 {
		t.Errorf("messages count = %d, want 0 (empty input should not send)", len(m.messages))
	}
}

func TestEnterWhitespaceOnlyDoesNotSend(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	m = typeString(m, "   ")
	m = sendKey(m, tea.KeyEnter)
	if len(m.messages) != 0 {
		t.Errorf("messages count = %d, want 0 (whitespace-only should not send)", len(m.messages))
	}
}

func TestMultipleMessages(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	m = typeString(m, "first")
	m = sendKey(m, tea.KeyEnter)
	m = typeString(m, "second")
	m = sendKey(m, tea.KeyEnter)
	m = typeString(m, "third")
	m = sendKey(m, tea.KeyEnter)

	if len(m.messages) != 3 {
		t.Fatalf("messages count = %d, want 3", len(m.messages))
	}
	if m.messages[0].content != "first" {
		t.Errorf("messages[0] = %q, want %q", m.messages[0].content, "first")
	}
	if m.messages[2].content != "third" {
		t.Errorf("messages[2] = %q, want %q", m.messages[2].content, "third")
	}
}

func TestTextareaHeightExpandsWithContent(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	if m.textarea.Height() != minInputHeight {
		t.Errorf("initial height = %d, want %d", m.textarea.Height(), minInputHeight)
	}

	// Type enough newlines to expand the textarea
	m = typeString(m, "line1")
	m = sendKey(m, tea.KeyEnter, tea.ModShift) // shift+enter = newline
	m = typeString(m, "line2")
	m = sendKey(m, tea.KeyEnter, tea.ModShift)
	m = typeString(m, "line3")

	if m.textarea.Height() < 3 {
		t.Errorf("textarea height = %d, want >= 3 with 3 lines of content", m.textarea.Height())
	}
}

func TestTextareaHeightCappedAtMax(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	// Type many newlines to try to exceed max
	for i := 0; i < maxInputHeight+5; i++ {
		m = typeString(m, "x")
		m = sendKey(m, tea.KeyEnter, tea.ModShift)
	}

	if m.textarea.Height() > maxInputHeight {
		t.Errorf("textarea height = %d, exceeds max %d", m.textarea.Height(), maxInputHeight)
	}
}

func TestTextareaHeightResetsAfterSend(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	// Type multiline content
	m = typeString(m, "line1")
	m = sendKey(m, tea.KeyEnter, tea.ModShift)
	m = typeString(m, "line2")
	m = sendKey(m, tea.KeyEnter, tea.ModShift)
	m = typeString(m, "line3")

	heightBefore := m.textarea.Height()
	if heightBefore < 2 {
		t.Fatalf("textarea should have expanded, got height %d", heightBefore)
	}

	// Send the message
	m = sendKey(m, tea.KeyEnter)

	if m.textarea.Height() != minInputHeight {
		t.Errorf("textarea height after send = %d, want %d", m.textarea.Height(), minInputHeight)
	}
}

func TestViewportHeightAdjustsWithTextarea(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	vpHeightEmpty := m.viewport.Height()

	// Expand textarea
	m = typeString(m, "line1")
	m = sendKey(m, tea.KeyEnter, tea.ModShift)
	m = typeString(m, "line2")
	m = sendKey(m, tea.KeyEnter, tea.ModShift)
	m = typeString(m, "line3")

	vpHeightExpanded := m.viewport.Height()

	// Viewport should shrink as textarea grows
	if vpHeightExpanded >= vpHeightEmpty {
		t.Errorf("viewport should shrink when textarea expands: empty=%d, expanded=%d",
			vpHeightEmpty, vpHeightExpanded)
	}
}

func TestTextareaExpandsWithWrapping(t *testing.T) {
	m := initialModel()
	m = resize(m, 30, 24) // narrow window to force wrapping

	// Type a long line that will wrap
	longText := strings.Repeat("word ", 20) // 100 chars, will wrap in 28-wide textarea
	m = typeString(m, longText)

	if m.textarea.Height() <= 1 {
		t.Errorf("textarea should expand for wrapped content, got height %d", m.textarea.Height())
	}
}

func TestDisplayLineCount(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	if m.displayLineCount() != 1 {
		t.Errorf("empty displayLineCount = %d, want 1", m.displayLineCount())
	}

	m = typeString(m, "hello")
	if m.displayLineCount() != 1 {
		t.Errorf("short text displayLineCount = %d, want 1", m.displayLineCount())
	}
}

func TestViewNotReadyShowsInitializing(t *testing.T) {
	m := initialModel()
	// Before any WindowSizeMsg, ready is false
	v := m.View()
	if !strings.Contains(v.Content, "Initializing") {
		t.Error("View() before ready should contain 'Initializing'")
	}
}

func TestViewAfterReady(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	v := m.View()

	if strings.Contains(v.Content, "Initializing") {
		t.Error("View() after ready should not contain 'Initializing'")
	}
	// Should render something non-empty
	if len(v.Content) == 0 {
		t.Error("View() after ready should not be empty")
	}
}

func TestInputBoxHeight(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	// Input box = textarea height + 2 (borders)
	expected := m.textarea.Height() + 2
	if m.inputBoxHeight() != expected {
		t.Errorf("inputBoxHeight = %d, want %d", m.inputBoxHeight(), expected)
	}
}

func TestViewportHeightMinimum(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 4) // very short terminal

	if m.viewportHeight() < 1 {
		t.Errorf("viewportHeight = %d, want >= 1", m.viewportHeight())
	}
}

func TestMessageTrimmed(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	m = typeString(m, "  hello  ")
	m = sendKey(m, tea.KeyEnter)

	if len(m.messages) != 1 {
		t.Fatalf("messages count = %d, want 1", len(m.messages))
	}
	if m.messages[0].content != "hello" {
		t.Errorf("message = %q, want %q (should be trimmed)", m.messages[0].content, "hello")
	}
}

func TestInputBoxFullWidth(t *testing.T) {
	widths := []int{40, 80, 120, 55, 100}
	for _, w := range widths {
		m := initialModel()
		m = resize(m, w, 24)

		v := m.View()
		lines := strings.Split(v.Content, "\n")

		// The input box is the last few lines of the view.
		// Find the border lines (they start with the rounded border character).
		// We check that every line of the rendered view has width <= m.width,
		// and specifically the input box lines should be exactly m.width.
		inputBoxLines := lines[m.viewport.Height():]
		for i, line := range inputBoxLines {
			lineWidth := lipgloss.Width(line)
			if lineWidth != w {
				t.Errorf("width=%d: input box line %d has width %d, want %d\n  line: %q",
					w, i, lineWidth, w, line)
			}
		}
	}
}

func TestInputBoxFullWidthAfterResize(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)
	// Resize to a different width
	m = resize(m, 60, 24)

	v := m.View()
	lines := strings.Split(v.Content, "\n")
	inputBoxLines := lines[m.viewport.Height():]
	for i, line := range inputBoxLines {
		lineWidth := lipgloss.Width(line)
		if lineWidth != 60 {
			t.Errorf("input box line %d after resize has width %d, want 60\n  line: %q",
				i, lineWidth, line)
		}
	}
}

func TestInputBoxFullWidthWithContent(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	// Type some multiline content
	m = typeString(m, "hello")
	m = sendKey(m, tea.KeyEnter, tea.ModShift)
	m = typeString(m, "world")

	v := m.View()
	lines := strings.Split(v.Content, "\n")
	inputBoxLines := lines[m.viewport.Height():]
	for i, line := range inputBoxLines {
		lineWidth := lipgloss.Width(line)
		if lineWidth != 80 {
			t.Errorf("input box line %d with content has width %d, want 80\n  line: %q",
				i, lineWidth, line)
		}
	}
}

func TestCtrlCReturnsQuit(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c should return a command")
	}
}
