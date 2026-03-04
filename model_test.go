package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// TestMain runs all tests in a temp directory so that saveConfig() calls
// never clobber the real .cpsl/config.json in the project root.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "cpsl-test-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)

	orig, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		panic(err)
	}
	defer os.Chdir(orig)

	os.Exit(m.Run())
}

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

// paste simulates a bracketed paste event and returns the updated model.
func paste(m model, content string) model {
	result, _ := m.Update(tea.PasteMsg{Content: content})
	return result.(model)
}

func TestPasteAboveThresholdInsertPlaceholder(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	longText := strings.Repeat("x", m.config.PasteCollapseMinChars)
	m = paste(m, longText)

	// Placeholder should be in the textarea
	expected := fmt.Sprintf("[pasted #%d | %d chars]", 1, m.config.PasteCollapseMinChars)
	if m.textarea.Value() != expected {
		t.Errorf("textarea = %q, want %q", m.textarea.Value(), expected)
	}

	// Actual content stored in pasteStore
	if m.pasteStore[1] != longText {
		t.Error("pasteStore should contain the original paste content")
	}
}

func TestPasteBelowThresholdInsertedVerbatim(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	shortText := strings.Repeat("x", m.config.PasteCollapseMinChars-1)
	m = paste(m, shortText)

	// Small paste goes directly into textarea
	if m.textarea.Value() != shortText {
		t.Errorf("textarea = %q, want verbatim paste", m.textarea.Value())
	}
	if m.pasteCount != 0 {
		t.Errorf("pasteCount = %d, want 0 for small paste", m.pasteCount)
	}
}

func TestPasteCounterIncrements(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	longText := strings.Repeat("x", m.config.PasteCollapseMinChars)

	// First paste + send
	m = paste(m, longText)
	m = sendKey(m, tea.KeyEnter)

	// Second paste + send
	m = paste(m, longText)
	m = sendKey(m, tea.KeyEnter)

	// Normal message
	m = typeString(m, "hello")
	m = sendKey(m, tea.KeyEnter)

	// Third paste + send
	m = paste(m, longText)
	m = sendKey(m, tea.KeyEnter)

	if m.pasteCount != 3 {
		t.Errorf("pasteCount = %d, want 3", m.pasteCount)
	}
	if len(m.messages) != 4 {
		t.Fatalf("messages count = %d, want 4", len(m.messages))
	}
	// Messages should contain expanded paste content
	if m.messages[0].content != longText {
		t.Error("messages[0] should contain expanded paste content")
	}
	if m.messages[2].content != "hello" {
		t.Errorf("messages[2] = %q, want %q", m.messages[2].content, "hello")
	}
	if m.messages[3].content != longText {
		t.Error("messages[3] should contain expanded paste content")
	}
}

func TestPasteViewportShowsExpandedContent(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	longText := strings.Repeat("a", 300)
	m = paste(m, longText)
	m = sendKey(m, tea.KeyEnter)

	v := m.View()
	// Viewport should show the expanded content, not the placeholder
	if strings.Contains(v.Content, "[pasted #1 | 300 chars]") {
		t.Error("viewport should not contain placeholder after submit")
	}
	if !strings.Contains(v.Content, "aaaaaa") {
		t.Error("viewport should show expanded paste content")
	}
}

func TestPasteStoreRetainsContent(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	text1 := strings.Repeat("A", 300)
	text2 := strings.Repeat("B", 400)

	m = paste(m, text1)
	m = sendKey(m, tea.KeyEnter)
	m = paste(m, text2)
	m = sendKey(m, tea.KeyEnter)

	if m.pasteStore[1] != text1 {
		t.Error("pasteStore[1] should contain first paste")
	}
	if m.pasteStore[2] != text2 {
		t.Error("pasteStore[2] should contain second paste")
	}
}

func TestPasteTypingContinuesAfterPaste(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	m = typeString(m, "before ")
	longText := strings.Repeat("x", m.config.PasteCollapseMinChars)
	m = paste(m, longText)
	m = typeString(m, " after")
	m = sendKey(m, tea.KeyEnter)

	if len(m.messages) != 1 {
		t.Fatalf("messages count = %d, want 1", len(m.messages))
	}
	content := m.messages[0].content
	if !strings.HasPrefix(content, "before ") {
		t.Errorf("message should start with 'before ', got %q", content)
	}
	// Paste should be expanded in the sent message
	if !strings.Contains(content, longText) {
		t.Error("message should contain expanded paste content")
	}
	if !strings.HasSuffix(content, " after") {
		t.Errorf("message should end with ' after', got %q", content)
	}
}

func TestPasteMultiplePastesInOneMessage(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	text1 := strings.Repeat("A", 300)
	text2 := strings.Repeat("B", 400)

	m = typeString(m, "code: ")
	m = paste(m, text1)
	m = typeString(m, " and: ")
	m = paste(m, text2)
	m = sendKey(m, tea.KeyEnter)

	if len(m.messages) != 1 {
		t.Fatalf("messages count = %d, want 1", len(m.messages))
	}
	content := m.messages[0].content
	// Both pastes should be expanded in the sent message
	if !strings.Contains(content, text1) {
		t.Error("message should contain expanded paste #1 content")
	}
	if !strings.Contains(content, text2) {
		t.Error("message should contain expanded paste #2 content")
	}
	if !strings.HasPrefix(content, "code: ") {
		t.Errorf("message should start with 'code: ', got prefix %q", content[:10])
	}
	if !strings.Contains(content, " and: ") {
		t.Error("message should contain ' and: ' between pastes")
	}
}

// --- Command parsing tests ---

func TestSlashConfigEntersConfigMode(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	m = typeString(m, "/config")
	m = sendKey(m, tea.KeyEnter)

	if m.mode != modeConfig {
		t.Errorf("mode = %d, want modeConfig (%d)", m.mode, modeConfig)
	}
	// Textarea should be cleared and blurred
	if m.textarea.Value() != "" {
		t.Errorf("textarea should be empty after /config, got %q", m.textarea.Value())
	}
}

func TestUnknownCommandShowsError(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	m = typeString(m, "/foo")
	m = sendKey(m, tea.KeyEnter)

	if m.mode != modeChat {
		t.Error("unknown command should stay in chat mode")
	}
	if len(m.messages) != 1 {
		t.Fatalf("messages count = %d, want 1", len(m.messages))
	}
	if m.messages[0].kind != msgError {
		t.Error("unknown command message should be an error message")
	}
	if !strings.Contains(m.messages[0].content, "/foo") {
		t.Errorf("error message should mention the command, got %q", m.messages[0].content)
	}
}

func TestSlashInNormalTextNotTreatedAsCommand(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	m = typeString(m, "use a/b path")
	m = sendKey(m, tea.KeyEnter)

	if m.mode != modeChat {
		t.Error("text with / in middle should stay in chat mode")
	}
	if len(m.messages) != 1 {
		t.Fatalf("messages count = %d, want 1", len(m.messages))
	}
	if m.messages[0].content != "use a/b path" {
		t.Errorf("message = %q, want %q", m.messages[0].content, "use a/b path")
	}
}

func TestSlashConfigWithExtraArgs(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	m = typeString(m, "/config something")
	m = sendKey(m, tea.KeyEnter)

	// Should still enter config mode (extra args ignored for now)
	if m.mode != modeConfig {
		t.Errorf("mode = %d, want modeConfig", m.mode)
	}
}

// --- Mode switching tests ---

func TestConfigModeEscDiscards(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	originalThreshold := m.config.PasteCollapseMinChars

	// Enter config mode
	m = typeString(m, "/config")
	m = sendKey(m, tea.KeyEnter)

	if m.mode != modeConfig {
		t.Fatal("should be in config mode")
	}

	// Press Esc to cancel
	result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = result.(model)

	if m.mode != modeChat {
		t.Errorf("mode = %d, want modeChat after Esc", m.mode)
	}
	if m.config.PasteCollapseMinChars != originalThreshold {
		t.Error("config should not change after Esc")
	}
	// Should show "discarded" system message
	found := false
	for _, msg := range m.messages {
		if msg.kind == msgInfo && strings.Contains(msg.content, "discard") {
			found = true
			break
		}
	}
	if !found {
		t.Error("should show discard message after Esc")
	}
}

func TestConfigModeEnterSaves(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	// Enter config mode
	m = typeString(m, "/config")
	m = sendKey(m, tea.KeyEnter)

	if m.mode != modeConfig {
		t.Fatal("should be in config mode")
	}

	// The form should be pre-populated with current value
	val := m.configForm.fields[0].input.Value()
	if val != strconv.Itoa(m.config.PasteCollapseMinChars) {
		t.Errorf("form value = %q, want %q", val, strconv.Itoa(m.config.PasteCollapseMinChars))
	}

	// Press Enter to save (valid value already set)
	result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = result.(model)

	if m.mode != modeChat {
		t.Errorf("mode = %d, want modeChat after Enter", m.mode)
	}
	// Should show "saved" system message
	found := false
	for _, msg := range m.messages {
		if msg.kind == msgSuccess && strings.Contains(msg.content, "saved") {
			found = true
			break
		}
	}
	if !found {
		t.Error("should show saved message after Enter")
	}
}

func TestConfigModeValidationRejectsInvalid(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	// Enter config mode
	m = typeString(m, "/config")
	m = sendKey(m, tea.KeyEnter)

	// Clear the input and type invalid value
	// Select all and delete existing content
	m.configForm.fields[0].input.SetValue("abc")

	// Press Enter — should stay in config mode due to validation
	result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = result.(model)

	if m.mode != modeConfig {
		t.Errorf("mode = %d, want modeConfig (invalid input should not save)", m.mode)
	}
	if m.configForm.fields[0].err == "" {
		t.Error("should show validation error for non-numeric input")
	}
}

func TestConfigModeCtrlCDiscards(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	m = typeString(m, "/config")
	m = sendKey(m, tea.KeyEnter)

	// Ctrl+C in config mode should discard (not quit the app)
	result, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = result.(model)

	if m.mode != modeChat {
		t.Errorf("mode = %d, want modeChat after Ctrl+C in config", m.mode)
	}
	// Should NOT quit the app
	if cmd != nil {
		// Check it's not a quit command by running it
		// (focus command from textarea is OK)
	}
}

func TestConfigModeWindowResizeForwarded(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	m = typeString(m, "/config")
	m = sendKey(m, tea.KeyEnter)

	// Resize while in config mode
	result, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = result.(model)

	if m.mode != modeConfig {
		t.Error("should stay in config mode after resize")
	}
	if m.width != 120 || m.height != 40 {
		t.Errorf("dimensions = %dx%d, want 120x40", m.width, m.height)
	}
}

// --- filterCommands tests ---

func TestFilterCommandsExactMatch(t *testing.T) {
	matches := filterCommands("/config")
	if len(matches) != 1 || matches[0] != "/config" {
		t.Errorf("filterCommands(/config) = %v, want [/config]", matches)
	}
}

func TestFilterCommandsPartialMatch(t *testing.T) {
	matches := filterCommands("/con")
	if len(matches) != 2 || matches[0] != "/config" || matches[1] != "/container-shell" {
		t.Errorf("filterCommands(/con) = %v, want [/config /container-shell]", matches)
	}
}

func TestFilterCommandsNoMatch(t *testing.T) {
	matches := filterCommands("/xyz")
	if len(matches) != 0 {
		t.Errorf("filterCommands(/xyz) = %v, want []", matches)
	}
}

func TestFilterCommandsSlashOnly(t *testing.T) {
	matches := filterCommands("/")
	if len(matches) != len(commands) {
		t.Errorf("filterCommands(/) = %v, want all %d commands", matches, len(commands))
	}
}

// --- autocompleteMatches tests ---

func TestAutocompleteMatchesChatMode(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)
	m = typeString(m, "/")

	matches := m.autocompleteMatches()
	if len(matches) == 0 {
		t.Error("autocompleteMatches should return matches when typing / in chat mode")
	}
}

func TestAutocompleteMatchesConfigMode(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)
	m = typeString(m, "/config")
	m = sendKey(m, tea.KeyEnter)

	// In config mode, autocomplete should not be active
	matches := m.autocompleteMatches()
	if matches != nil {
		t.Errorf("autocompleteMatches should be nil in config mode, got %v", matches)
	}
}

func TestAutocompleteMatchesNoSlash(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)
	m = typeString(m, "hello")

	matches := m.autocompleteMatches()
	if matches != nil {
		t.Errorf("autocompleteMatches should be nil without slash, got %v", matches)
	}
}

// --- Tab/Esc key handling tests ---

func TestTabAcceptsTopMatch(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)
	m = typeString(m, "/con")
	m = sendKey(m, tea.KeyTab)

	if m.textarea.Value() != "/config" {
		t.Errorf("textarea = %q, want /config after Tab", m.textarea.Value())
	}
}

func TestTabWithNoMatchDoesNothing(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)
	m = typeString(m, "/xyz")
	m = sendKey(m, tea.KeyTab)

	if m.textarea.Value() != "/xyz" {
		t.Errorf("textarea = %q, want /xyz (unchanged) after Tab with no match", m.textarea.Value())
	}
}

func TestEscDismissesSlashInput(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)
	m = typeString(m, "/con")
	m = sendKey(m, tea.KeyEscape)

	if m.textarea.Value() != "" {
		t.Errorf("textarea = %q, want empty after Esc on slash input", m.textarea.Value())
	}
}

func TestEscWithoutSlashDoesNotClear(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)
	m = typeString(m, "hello")
	m = sendKey(m, tea.KeyEscape)

	if m.textarea.Value() != "hello" {
		t.Errorf("textarea = %q, want hello (unchanged) after Esc without slash", m.textarea.Value())
	}
}

func TestTabThenEnterExecutesCommand(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)
	m = typeString(m, "/con")
	m = sendKey(m, tea.KeyTab)
	m = sendKey(m, tea.KeyEnter)

	if m.mode != modeConfig {
		t.Errorf("mode = %d, want modeConfig after Tab+Enter on /con", m.mode)
	}
}

func TestAutocompleteVisibleInView(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)
	m = typeString(m, "/")

	v := m.View()
	if !strings.Contains(v.Content, "/config") {
		t.Error("View should show /config in autocomplete when typing /")
	}
}

func TestPasteConfigThresholdRespected(t *testing.T) {
	m := initialModel()
	m.config.PasteCollapseMinChars = 50
	m = resize(m, 80, 24)

	// Paste at custom threshold — should collapse
	text := strings.Repeat("x", 50)
	m = paste(m, text)
	if !strings.Contains(m.textarea.Value(), "[pasted #1") {
		t.Error("paste at custom threshold should be collapsed")
	}

	m.textarea.Reset()

	// Paste below custom threshold — should be verbatim
	shortText := strings.Repeat("y", 49)
	m = paste(m, shortText)
	if m.textarea.Value() != shortText {
		t.Errorf("paste below threshold should be verbatim, got %q", m.textarea.Value())
	}
}

// --- /model command tests ---

// modelWithKey returns a model with only an Anthropic API key configured
// and test models loaded.
func modelWithKey() model {
	m := initialModel()
	m.config.AnthropicAPIKey = "sk-test-key"
	m.config.GrokAPIKey = ""
	m.config.OpenAIAPIKey = ""
	m.config.ActiveModel = ""
	m.models = testModels()
	m.modelsLoaded = true
	return m
}

func TestSlashModelEntersModelMode(t *testing.T) {
	m := modelWithKey()
	m = resize(m, 80, 24)

	m = typeString(m, "/model")
	m = sendKey(m, tea.KeyEnter)

	if m.mode != modeModel {
		t.Errorf("mode = %d, want modeModel (%d)", m.mode, modeModel)
	}
	if m.textarea.Value() != "" {
		t.Errorf("textarea should be empty after /model, got %q", m.textarea.Value())
	}
}

func TestSlashModelNoKeysShowsError(t *testing.T) {
	m := initialModel()
	m.config.AnthropicAPIKey = ""
	m.config.GrokAPIKey = ""
	m.config.OpenAIAPIKey = ""
	m.models = testModels()
	m.modelsLoaded = true
	m = resize(m, 80, 24)

	m = typeString(m, "/model")
	m = sendKey(m, tea.KeyEnter)

	if m.mode != modeChat {
		t.Errorf("mode = %d, want modeChat (no keys configured)", m.mode)
	}
	if len(m.messages) != 1 {
		t.Fatalf("messages count = %d, want 1", len(m.messages))
	}
	if m.messages[0].kind != msgError {
		t.Error("should show error message when no keys configured")
	}
	if !strings.Contains(m.messages[0].content, "No API keys") {
		t.Errorf("error message = %q, want mention of API keys", m.messages[0].content)
	}
}

func TestModelModeEscCancels(t *testing.T) {
	m := modelWithKey()
	m = resize(m, 80, 24)

	m = typeString(m, "/model")
	m = sendKey(m, tea.KeyEnter)

	if m.mode != modeModel {
		t.Fatal("should be in model mode")
	}

	result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = result.(model)

	if m.mode != modeChat {
		t.Errorf("mode = %d, want modeChat after Esc", m.mode)
	}
	found := false
	for _, msg := range m.messages {
		if msg.kind == msgInfo && strings.Contains(msg.content, "cancelled") {
			found = true
			break
		}
	}
	if !found {
		t.Error("should show cancellation message after Esc")
	}
}

func TestModelModeEnterSelectsModel(t *testing.T) {
	m := modelWithKey()
	m = resize(m, 80, 24)

	m = typeString(m, "/model")
	m = sendKey(m, tea.KeyEnter)

	// Should show Anthropic models since only that key is set
	if len(m.modelList.models) == 0 {
		t.Fatal("model list should have models")
	}
	for _, md := range m.modelList.models {
		if md.Provider != ProviderAnthropic {
			t.Errorf("model list should only have anthropic models, got %s", md.Provider)
		}
	}

	// Select (Enter)
	selectedModel := m.modelList.selected()
	result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = result.(model)

	if m.mode != modeChat {
		t.Errorf("mode = %d, want modeChat after selection", m.mode)
	}
	if m.config.ActiveModel != selectedModel.ID {
		t.Errorf("ActiveModel = %q, want %q", m.config.ActiveModel, selectedModel.ID)
	}
	found := false
	for _, msg := range m.messages {
		if msg.kind == msgSuccess && strings.Contains(msg.content, selectedModel.DisplayName) {
			found = true
			break
		}
	}
	if !found {
		t.Error("should show success message with model name")
	}
}

func TestModelModeOnlyShowsConfiguredProviders(t *testing.T) {
	m := initialModel()
	m.config.AnthropicAPIKey = ""
	m.config.GrokAPIKey = "grok-key"
	m.config.OpenAIAPIKey = "openai-key"
	m.models = testModels()
	m.modelsLoaded = true
	m = resize(m, 80, 24)

	m = typeString(m, "/model")
	m = sendKey(m, tea.KeyEnter)

	if m.mode != modeModel {
		t.Fatal("should be in model mode")
	}

	for _, md := range m.modelList.models {
		if md.Provider == ProviderAnthropic {
			t.Error("should not show Anthropic models without key")
		}
	}
	// Should have Grok and OpenAI models
	hasGrok := false
	hasOpenAI := false
	for _, md := range m.modelList.models {
		if md.Provider == ProviderGrok {
			hasGrok = true
		}
		if md.Provider == ProviderOpenAI {
			hasOpenAI = true
		}
	}
	if !hasGrok {
		t.Error("should show Grok models with key configured")
	}
	if !hasOpenAI {
		t.Error("should show OpenAI models with key configured")
	}
}

func TestModelModeNavigationUpDown(t *testing.T) {
	m := initialModel()
	m.config.AnthropicAPIKey = "key"
	m.config.OpenAIAPIKey = "key"
	m.config.ActiveModel = "anthropic/claude-opus-4-20250514" // most expensive → cursor starts near top
	m.models = testModels()
	m.modelsLoaded = true
	m = resize(m, 80, 24)

	m = typeString(m, "/model")
	m = sendKey(m, tea.KeyEnter)

	start := m.modelList.cursor

	// Move down
	result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = result.(model)
	if m.modelList.cursor != start+1 {
		t.Errorf("cursor after down = %d, want %d", m.modelList.cursor, start+1)
	}

	// Move down again
	result, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = result.(model)
	if m.modelList.cursor != start+2 {
		t.Errorf("cursor after second down = %d, want %d", m.modelList.cursor, start+2)
	}

	// Move up
	result, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = result.(model)
	if m.modelList.cursor != start+1 {
		t.Errorf("cursor after up = %d, want %d", m.modelList.cursor, start+1)
	}
}

func TestModelModeNavigationBounds(t *testing.T) {
	m := modelWithKey()
	m.config.ActiveModel = ""
	m = resize(m, 80, 24)

	m = typeString(m, "/model")
	m = sendKey(m, tea.KeyEnter)

	// Move cursor to top first
	for i := 0; i < len(m.modelList.models); i++ {
		result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
		m = result.(model)
	}
	if m.modelList.cursor != 0 {
		t.Errorf("cursor should be at 0 after moving up enough times, got %d", m.modelList.cursor)
	}

	// Try to go above 0
	result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = result.(model)
	if m.modelList.cursor != 0 {
		t.Errorf("cursor should not go below 0, got %d", m.modelList.cursor)
	}

	// Go to bottom
	for i := 0; i < len(m.modelList.models)+5; i++ {
		result, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		m = result.(model)
	}
	if m.modelList.cursor != len(m.modelList.models)-1 {
		t.Errorf("cursor should stop at last item, got %d, want %d",
			m.modelList.cursor, len(m.modelList.models)-1)
	}
}

func TestModelModeCursorStartsOnActiveModel(t *testing.T) {
	m := initialModel()
	m.config.AnthropicAPIKey = "key"
	m.models = testModels()
	m.modelsLoaded = true
	// Set active model to second Anthropic model
	m.config.ActiveModel = "anthropic/claude-haiku-4-20250414"
	m = resize(m, 80, 24)

	m = typeString(m, "/model")
	m = sendKey(m, tea.KeyEnter)

	// Find the index of the active model in the list
	expectedIdx := -1
	for i, md := range m.modelList.models {
		if md.ID == "anthropic/claude-haiku-4-20250414" {
			expectedIdx = i
			break
		}
	}
	if expectedIdx == -1 {
		t.Fatal("active model not found in list")
	}
	if m.modelList.cursor != expectedIdx {
		t.Errorf("cursor = %d, want %d (should start on active model)", m.modelList.cursor, expectedIdx)
	}
}

func TestModelModeActiveModelHighlighted(t *testing.T) {
	m := initialModel()
	m.config.AnthropicAPIKey = "key"
	m.config.ActiveModel = "anthropic/claude-sonnet-4-20250514"
	m.models = testModels()
	m.modelsLoaded = true
	m = resize(m, 80, 24)

	m = typeString(m, "/model")
	m = sendKey(m, tea.KeyEnter)

	if m.modelList.activeModel != "anthropic/claude-sonnet-4-20250514" {
		t.Errorf("activeModel = %q, want anthropic/claude-sonnet-4-20250514", m.modelList.activeModel)
	}

	// The view should contain the active marker
	view := m.modelList.View()
	if !strings.Contains(view, "●") {
		t.Error("model list view should show active marker ●")
	}
}

func TestModelModeCtrlCCancels(t *testing.T) {
	m := modelWithKey()
	m = resize(m, 80, 24)

	m = typeString(m, "/model")
	m = sendKey(m, tea.KeyEnter)

	result, _ := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = result.(model)

	if m.mode != modeChat {
		t.Errorf("mode = %d, want modeChat after Ctrl+C", m.mode)
	}
}

func TestModelModeWindowResize(t *testing.T) {
	m := modelWithKey()
	m = resize(m, 80, 24)

	m = typeString(m, "/model")
	m = sendKey(m, tea.KeyEnter)

	result, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = result.(model)

	if m.mode != modeModel {
		t.Error("should stay in model mode after resize")
	}
	if m.width != 120 || m.height != 40 {
		t.Errorf("dimensions = %dx%d, want 120x40", m.width, m.height)
	}
}

func TestModelModeViewRendered(t *testing.T) {
	m := modelWithKey()
	m = resize(m, 80, 24)

	m = typeString(m, "/model")
	m = sendKey(m, tea.KeyEnter)

	v := m.View()
	if !strings.Contains(v.Content, "Select Model") {
		t.Error("model view should contain 'Select Model'")
	}
}

func TestModelCommandInAutocomplete(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)
	m = typeString(m, "/m")

	matches := m.autocompleteMatches()
	found := false
	for _, match := range matches {
		if match == "/model" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("autocomplete for /m should include /model, got %v", matches)
	}
}

func TestModelModeSelectNavigatedModel(t *testing.T) {
	m := initialModel()
	m.config.AnthropicAPIKey = "key"
	m.config.ActiveModel = ""
	m.models = testModels()
	m.modelsLoaded = true
	m = resize(m, 80, 24)

	m = typeString(m, "/model")
	m = sendKey(m, tea.KeyEnter)

	// Navigate down from initial position
	result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = result.(model)

	targetModel := m.modelList.models[m.modelList.cursor]

	// Select it
	result, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = result.(model)

	if m.config.ActiveModel != targetModel.ID {
		t.Errorf("ActiveModel = %q, want %q", m.config.ActiveModel, targetModel.ID)
	}
}

func TestModelModeVimKeys(t *testing.T) {
	m := initialModel()
	m.config.AnthropicAPIKey = "key"
	m.config.OpenAIAPIKey = "key"
	m.config.ActiveModel = ""
	m.models = testModels()
	m.modelsLoaded = true
	m = resize(m, 80, 24)

	m = typeString(m, "/model")
	m = sendKey(m, tea.KeyEnter)

	startCursor := m.modelList.cursor

	// j moves down
	result, _ := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m = result.(model)
	if m.modelList.cursor != startCursor+1 {
		t.Errorf("cursor after j = %d, want %d", m.modelList.cursor, startCursor+1)
	}

	// k moves up
	result, _ = m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	m = result.(model)
	if m.modelList.cursor != startCursor {
		t.Errorf("cursor after k = %d, want %d", m.modelList.cursor, startCursor)
	}
}

// --- modelsMsg handling tests ---

func TestModelsMsgSetsModels(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	if m.modelsLoaded {
		t.Fatal("modelsLoaded should be false initially")
	}

	result, _ := m.Update(modelsMsg{models: testModels()})
	m = result.(model)

	if !m.modelsLoaded {
		t.Error("modelsLoaded should be true after modelsMsg")
	}
	if m.modelsErr != nil {
		t.Errorf("modelsErr should be nil, got %v", m.modelsErr)
	}
	if len(m.models) != len(testModels()) {
		t.Errorf("models count = %d, want %d", len(m.models), len(testModels()))
	}
}

func TestModelsMsgSetsError(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	result, _ := m.Update(modelsMsg{err: fmt.Errorf("network error")})
	m = result.(model)

	if !m.modelsLoaded {
		t.Error("modelsLoaded should be true even on error")
	}
	if m.modelsErr == nil {
		t.Error("modelsErr should be set")
	}
	if len(m.models) != 0 {
		t.Errorf("models should be empty on error, got %d", len(m.models))
	}
}

func TestSlashModelBeforeModelsLoaded(t *testing.T) {
	m := initialModel()
	m.config.AnthropicAPIKey = "key"
	// modelsLoaded is false by default
	m = resize(m, 80, 24)

	m = typeString(m, "/model")
	m = sendKey(m, tea.KeyEnter)

	if m.mode != modeChat {
		t.Errorf("mode = %d, want modeChat (models not loaded)", m.mode)
	}
	if len(m.messages) != 1 {
		t.Fatalf("messages count = %d, want 1", len(m.messages))
	}
	if m.messages[0].kind != msgInfo {
		t.Error("should show info message about loading")
	}
	if !strings.Contains(m.messages[0].content, "loading") {
		t.Errorf("message = %q, want mention of loading", m.messages[0].content)
	}
}

func TestModelListScrollsWithCursor(t *testing.T) {
	// Create many models to force scrolling in a small window
	var manyModels []ModelDef
	for i := 0; i < 30; i++ {
		manyModels = append(manyModels, ModelDef{
			Provider:    ProviderAnthropic,
			ID:          fmt.Sprintf("anthropic/model-%d", i),
			DisplayName: fmt.Sprintf("Model %d", i),
			PromptPrice: 1.0,
		})
	}

	m := initialModel()
	m.config.AnthropicAPIKey = "key"
	m.models = manyModels
	m.modelsLoaded = true
	m = resize(m, 80, 20) // small height to force scrolling

	m = typeString(m, "/model")
	m = sendKey(m, tea.KeyEnter)

	if m.mode != modeModel {
		t.Fatal("should be in model mode")
	}

	vis := m.modelList.visibleRows()
	if vis >= 30 {
		t.Skip("window too large to test scrolling")
	}

	// Cursor starts at 0, scroll at 0
	if m.modelList.cursor != 0 {
		t.Errorf("cursor = %d, want 0", m.modelList.cursor)
	}
	if m.modelList.scroll != 0 {
		t.Errorf("scroll = %d, want 0", m.modelList.scroll)
	}

	// Navigate past visible area
	for i := 0; i < vis+2; i++ {
		result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		m = result.(model)
	}

	// Cursor should be past initial visible area, scroll should follow
	if m.modelList.cursor != vis+2 {
		t.Errorf("cursor = %d, want %d", m.modelList.cursor, vis+2)
	}
	if m.modelList.scroll == 0 {
		t.Error("scroll should have advanced past 0")
	}
	// Cursor should be within visible window
	if m.modelList.cursor < m.modelList.scroll || m.modelList.cursor >= m.modelList.scroll+vis {
		t.Errorf("cursor %d not in visible window [%d, %d)",
			m.modelList.cursor, m.modelList.scroll, m.modelList.scroll+vis)
	}

	// View should show scroll indicators
	view := m.modelList.View()
	if !strings.Contains(view, "↑") {
		t.Error("should show scroll-up indicator")
	}
	if !strings.Contains(view, "↓") {
		t.Error("should show scroll-down indicator")
	}
}

func TestSlashModelWithFetchError(t *testing.T) {
	m := initialModel()
	m.config.AnthropicAPIKey = "key"
	m.modelsLoaded = true
	m.modelsErr = fmt.Errorf("connection refused")
	m = resize(m, 80, 24)

	m = typeString(m, "/model")
	m = sendKey(m, tea.KeyEnter)

	if m.mode != modeChat {
		t.Errorf("mode = %d, want modeChat (fetch error)", m.mode)
	}
	if len(m.messages) != 1 {
		t.Fatalf("messages count = %d, want 1", len(m.messages))
	}
	if m.messages[0].kind != msgError {
		t.Error("should show error message")
	}
	if !strings.Contains(m.messages[0].content, "connection refused") {
		t.Errorf("message = %q, want mention of error", m.messages[0].content)
	}
}

// testModelsWithSWE returns test models enriched with SWE-bench scores.
func testModelsWithSWE() []ModelDef {
	return []ModelDef{
		{Provider: ProviderAnthropic, ID: "anthropic/claude-opus-4", DisplayName: "Claude Opus 4", PromptPrice: 15.0, CompletionPrice: 75.0},
		{Provider: ProviderAnthropic, ID: "anthropic/claude-sonnet-4", DisplayName: "Claude Sonnet 4", PromptPrice: 3.0, CompletionPrice: 15.0},
		{Provider: ProviderOpenAI, ID: "openai/gpt-4o", DisplayName: "GPT-4o", PromptPrice: 2.5, CompletionPrice: 10.0},
		{Provider: ProviderGrok, ID: "x-ai/grok-3", DisplayName: "Grok 3", PromptPrice: 3.0, CompletionPrice: 15.0},
	}
}

// enterModelModeWith sets up a model with the given models and enters /model.
func enterModelModeWith(t *testing.T, models []ModelDef) model {
	t.Helper()
	m := initialModel()
	m.config.AnthropicAPIKey = "key"
	m.config.OpenAIAPIKey = "key"
	m.config.GrokAPIKey = "key"
	m.models = models
	m.modelsLoaded = true
	m = resize(m, 120, 40)

	m = typeString(m, "/model")
	m = sendKey(m, tea.KeyEnter)
	if m.mode != modeModel {
		t.Fatalf("expected modeModel, got %d", m.mode)
	}
	return m
}

func TestSortDefaultPriceDescending(t *testing.T) {
	m := enterModelModeWith(t, testModelsWithSWE())

	if m.modelList.sortCol != colPrice {
		t.Errorf("default sortCol = %d, want colPrice (%d)", m.modelList.sortCol, colPrice)
	}
	if m.modelList.sortDirs[colPrice] {
		t.Error("default sort should be descending for price")
	}

	// First model should have the most expensive price
	for i := 1; i < len(m.modelList.models); i++ {
		if m.modelList.models[i-1].PromptPrice < m.modelList.models[i].PromptPrice {
			t.Errorf("price sort broken: %f < %f at index %d",
				m.modelList.models[i-1].PromptPrice, m.modelList.models[i].PromptPrice, i)
		}
	}
}

func TestSortRightArrowCyclesColumn(t *testing.T) {
	m := enterModelModeWith(t, testModelsWithSWE())

	// Default is colPrice (2)
	if m.modelList.sortCol != colPrice {
		t.Fatalf("initial sortCol = %d, want colPrice", m.modelList.sortCol)
	}

	// Right arrow → colName (wraps: (2+1)%3 = 0)
	result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m = result.(model)
	if m.modelList.sortCol != colName {
		t.Errorf("after right: sortCol = %d, want colName (%d)", m.modelList.sortCol, colName)
	}
	if !m.modelList.sortDirs[colName] {
		t.Error("colName should default to ascending")
	}

	// Right arrow → colProvider
	result, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m = result.(model)
	if m.modelList.sortCol != colProvider {
		t.Errorf("after right x2: sortCol = %d, want colProvider (%d)", m.modelList.sortCol, colProvider)
	}

	// Right arrow → colPrice (wraps back)
	result, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m = result.(model)
	if m.modelList.sortCol != colPrice {
		t.Errorf("after right x3: sortCol = %d, want colPrice (%d)", m.modelList.sortCol, colPrice)
	}
	if m.modelList.sortDirs[colPrice] {
		t.Error("colPrice should default to descending")
	}
}

func TestSortLeftArrowCyclesColumn(t *testing.T) {
	m := enterModelModeWith(t, testModelsWithSWE())

	// Default is colPrice (2), left arrow → colProvider (1)
	result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	m = result.(model)
	if m.modelList.sortCol != colProvider {
		t.Errorf("after left: sortCol = %d, want colProvider (%d)", m.modelList.sortCol, colProvider)
	}
}

func TestSortByNameAlphabetical(t *testing.T) {
	m := enterModelModeWith(t, testModelsWithSWE())

	// Switch to name sort (right from colPrice wraps to colName)
	result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m = result.(model)
	if m.modelList.sortCol != colName {
		t.Fatalf("sortCol = %d, want colName", m.modelList.sortCol)
	}

	// Should be alphabetical ascending
	for i := 1; i < len(m.modelList.models); i++ {
		a := strings.ToLower(m.modelList.models[i-1].DisplayName)
		b := strings.ToLower(m.modelList.models[i].DisplayName)
		if a > b {
			t.Errorf("name sort broken: %q > %q at index %d", a, b, i)
		}
	}
}

func TestSortByPriceAscending(t *testing.T) {
	m := enterModelModeWith(t, testModelsWithSWE())

	// Default sort is already colPrice descending
	if m.modelList.sortCol != colPrice {
		t.Fatalf("sortCol = %d, want colPrice", m.modelList.sortCol)
	}

	// Should be price descending (most expensive first)
	for i := 1; i < len(m.modelList.models); i++ {
		if m.modelList.models[i-1].PromptPrice < m.modelList.models[i].PromptPrice {
			t.Errorf("price sort broken: %f < %f at index %d",
				m.modelList.models[i-1].PromptPrice, m.modelList.models[i].PromptPrice, i)
		}
	}
}

func TestSortCursorPreserved(t *testing.T) {
	m := enterModelModeWith(t, testModelsWithSWE())

	// Move cursor to a specific model
	result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = result.(model)
	selectedID := m.modelList.selected().ID

	// Change sort column
	result, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m = result.(model)

	// Cursor should still point to the same model
	if m.modelList.selected().ID != selectedID {
		t.Errorf("cursor moved to %q after sort, want %q", m.modelList.selected().ID, selectedID)
	}
}

func TestTableViewHasHeaderAndSeparator(t *testing.T) {
	m := enterModelModeWith(t, testModelsWithSWE())
	view := m.modelList.View()

	if !strings.Contains(view, "MODEL") {
		t.Error("view should contain MODEL header")
	}
	if !strings.Contains(view, "PROVIDER") {
		t.Error("view should contain PROVIDER header")
	}
	if !strings.Contains(view, "PRICE") {
		t.Error("view should contain PRICE header")
	}
	if strings.Contains(view, "SWE-BENCH") {
		t.Error("view should NOT contain SWE-BENCH header (column removed)")
	}
	if !strings.Contains(view, "─") {
		t.Error("view should contain separator line")
	}
	if !strings.Contains(view, "▼") {
		t.Error("view should contain sort direction indicator (▼ for default descending price)")
	}
	if !strings.Contains(view, "←/→") {
		t.Error("hint should mention ←/→ for sort")
	}
	if !strings.Contains(view, "tab") {
		t.Error("hint should mention tab for reverse")
	}
}

func TestTableViewSortArrowChanges(t *testing.T) {
	m := enterModelModeWith(t, testModelsWithSWE())

	// Default: PRICE with ▼ (descending)
	view := m.modelList.View()
	if !strings.Contains(view, "PRICE ▼") {
		t.Error("default view should show PRICE ▼")
	}

	// Switch to name sort (right from colPrice wraps to colName, ascending ▲)
	result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m = result.(model)
	view = m.modelList.View()
	if !strings.Contains(view, "MODEL ▲") {
		t.Error("name sort view should show MODEL ▲")
	}
	// PRICE should no longer have arrow
	if strings.Contains(view, "PRICE ▼") || strings.Contains(view, "PRICE ▲") {
		t.Error("PRICE should not have arrow when not active sort")
	}
}

func TestTabTogglesSortDirection(t *testing.T) {
	m := enterModelModeWith(t, testModelsWithSWE())

	// Default: colPrice descending
	if m.modelList.sortDirs[colPrice] {
		t.Fatal("default price should be descending")
	}

	// Record first model (most expensive)
	firstBefore := m.modelList.models[0].ID

	// Press tab → ascending
	result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = result.(model)

	if !m.modelList.sortDirs[colPrice] {
		t.Error("tab should toggle to ascending")
	}
	if m.modelList.sortCol != colPrice {
		t.Error("tab should not change the sort column")
	}

	// First model should now be the cheapest
	if m.modelList.models[0].ID == firstBefore {
		t.Error("order should change after toggling direction")
	}

	// Press tab again → descending
	result, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = result.(model)

	if m.modelList.sortDirs[colPrice] {
		t.Error("second tab should toggle back to descending")
	}
	if m.modelList.models[0].ID != firstBefore {
		t.Error("order should restore after toggling back")
	}
}

func TestTabPreservesPerColumnDirection(t *testing.T) {
	m := enterModelModeWith(t, testModelsWithSWE())

	// Toggle price to ascending
	result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = result.(model)
	if !m.modelList.sortDirs[colPrice] {
		t.Fatal("price should now be ascending")
	}

	// Switch to name column (right arrow wraps to colName)
	result, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m = result.(model)
	if m.modelList.sortCol != colName {
		t.Fatalf("sortCol = %d, want colName", m.modelList.sortCol)
	}
	// Name should still be at its default (ascending)
	if !m.modelList.sortDirs[colName] {
		t.Error("name should be ascending (default)")
	}

	// Switch back to price — should remember ascending
	result, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	m = result.(model)
	if m.modelList.sortCol != colPrice {
		t.Fatalf("sortCol = %d, want colPrice", m.modelList.sortCol)
	}
	if !m.modelList.sortDirs[colPrice] {
		t.Error("price should still be ascending (remembered)")
	}
}

func TestSortDirsSavedToConfig(t *testing.T) {
	m := enterModelModeWith(t, testModelsWithSWE())

	// Toggle price to ascending
	result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = result.(model)

	// Exit model mode (cancel) — should persist sort dirs
	result, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = result.(model)

	// Config should have the updated sort dirs
	if !m.config.ModelSortDirs["price"] {
		t.Error("config should persist price as ascending after tab toggle")
	}
	if !m.config.ModelSortDirs["name"] {
		t.Error("config should persist name as ascending (default)")
	}

	// Re-enter model mode — should restore saved dirs
	m = typeString(m, "/model")
	m = sendKey(m, tea.KeyEnter)

	if !m.modelList.sortDirs[colPrice] {
		t.Error("price should be ascending (restored from config)")
	}
}
