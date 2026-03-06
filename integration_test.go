package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestFullFlowPasteAndConfigThreshold exercises the end-to-end flow:
// start app → paste long text → verify collapsed display → send →
// /config → change threshold → verify new threshold applies.
func TestFullFlowPasteAndConfigThreshold(t *testing.T) {
	a := newTestApp(80, 24)

	// --- Step 1: Paste long text above default threshold (200 chars) ---
	longText := strings.Repeat("X", 300)
	simPaste(a, longText)

	// Textarea should show the placeholder, not the raw text
	if !strings.Contains(a.textarea.Value(), "[pasted #1 | 300 chars]") {
		t.Fatalf("textarea should contain paste placeholder, got %q", a.textarea.Value())
	}
	if a.pasteCount != 1 {
		t.Fatalf("pasteCount = %d, want 1", a.pasteCount)
	}

	// --- Step 2: Send the pasted message ---
	simKey(a, KeyEnter)

	if len(a.messages) != 2 {
		t.Fatalf("messages count = %d, want 2", len(a.messages))
	}
	// Sent message should have expanded content, not the placeholder
	if a.messages[0].content != longText {
		t.Errorf("sent message should be expanded paste content")
	}

	// --- Step 3: Open /config and change threshold ---
	simType(a, "/config")
	simKey(a, KeyEnter)

	if a.mode != modeConfig {
		t.Fatalf("mode = %d, want modeConfig", a.mode)
	}

	// Change threshold to 500
	a.configForm.fields[0].input.SetValue("500")

	// Save config
	simKey(a, KeyEnter)

	if a.mode != modeChat {
		t.Fatalf("mode = %d, want modeChat after saving config", a.mode)
	}
	if a.config.PasteCollapseMinChars != 500 {
		t.Fatalf("threshold = %d, want 500", a.config.PasteCollapseMinChars)
	}

	// --- Step 4: Paste text between old and new threshold ---
	// 300 chars is above old threshold (200) but below new threshold (500)
	mediumText := strings.Repeat("Y", 300)
	simPaste(a, mediumText)

	// Should NOT be collapsed — it's below the new threshold of 500
	if a.textarea.Value() != mediumText {
		t.Errorf("paste below new threshold should be verbatim, got %q", a.textarea.Value())
	}
	if a.pasteCount != 1 {
		t.Errorf("pasteCount should still be 1, got %d", a.pasteCount)
	}

	// --- Step 5: Paste text above the new threshold ---
	a.textarea.Reset()
	hugeText := strings.Repeat("Z", 600)
	simPaste(a, hugeText)

	if !strings.Contains(a.textarea.Value(), "[pasted #2 | 600 chars]") {
		t.Errorf("paste above new threshold should be collapsed, got %q", a.textarea.Value())
	}
	if a.pasteCount != 2 {
		t.Errorf("pasteCount = %d, want 2", a.pasteCount)
	}
}

// TestFullFlowConfigDiscardPreservesThreshold verifies that Esc in /config
// does not change the active threshold.
func TestFullFlowConfigDiscardPreservesThreshold(t *testing.T) {
	a := newTestApp(80, 24)

	originalThreshold := a.config.PasteCollapseMinChars

	// Open /config
	simType(a, "/config")
	simKey(a, KeyEnter)

	// Change value but discard
	a.configForm.fields[0].input.SetValue("999")
	simKey(a, KeyEscape)

	if a.config.PasteCollapseMinChars != originalThreshold {
		t.Errorf("threshold = %d, want %d (should be unchanged after discard)",
			a.config.PasteCollapseMinChars, originalThreshold)
	}

	// Paste at original threshold should still collapse
	longText := strings.Repeat("x", originalThreshold)
	simPaste(a, longText)
	if !strings.Contains(a.textarea.Value(), "[pasted #1") {
		t.Error("paste at original threshold should still collapse after config discard")
	}
}

// TestFullFlowMultiplePastesThenMessages exercises sending a mix of
// normal messages and paste messages, verifying the message feed integrity.
func TestFullFlowMultiplePastesThenMessages(t *testing.T) {
	a := newTestApp(80, 200)

	// Send a normal message
	simType(a, "hello everyone")
	simKey(a, KeyEnter)

	// Paste a long message
	paste1 := strings.Repeat("A", 300)
	simPaste(a, paste1)
	simKey(a, KeyEnter)

	// Send another normal message
	simType(a, "that was a big paste")
	simKey(a, KeyEnter)

	// Paste another long message
	paste2 := strings.Repeat("B", 500)
	simPaste(a, paste2)
	simKey(a, KeyEnter)

	// Verify all messages present and in order
	if len(a.messages) != 8 {
		t.Fatalf("messages count = %d, want 8", len(a.messages))
	}
	if a.messages[0].content != "hello everyone" {
		t.Errorf("messages[0] = %q, want %q", a.messages[0].content, "hello everyone")
	}
	if a.messages[2].content != paste1 {
		t.Error("messages[2] should contain expanded paste #1")
	}
	if a.messages[4].content != "that was a big paste" {
		t.Errorf("messages[4] = %q, want %q", a.messages[4].content, "that was a big paste")
	}
	if a.messages[6].content != paste2 {
		t.Error("messages[6] should contain expanded paste #2")
	}

	// Messages should contain all expanded content
	allContent := ""
	for _, msg := range a.messages {
		allContent += msg.content + "\n"
	}
	if !strings.Contains(allContent, "hello everyone") {
		t.Error("messages missing normal message 1")
	}
	if !strings.Contains(allContent, "AAAA") {
		t.Error("messages missing expanded paste 1")
	}
	if !strings.Contains(allContent, "that was a big paste") {
		t.Error("messages missing normal message 2")
	}
	if !strings.Contains(allContent, "BBBB") {
		t.Error("messages missing expanded paste 2")
	}
}

// TestFullFlowUnknownCommandThenValidCommand verifies error recovery:
// unknown command shows error, then /config works normally.
func TestFullFlowUnknownCommandThenValidCommand(t *testing.T) {
	a := newTestApp(80, 24)

	// Try an unknown command
	simType(a, "/help")
	simKey(a, KeyEnter)

	if a.mode != modeChat {
		t.Error("should stay in chat mode after unknown command")
	}
	if len(a.messages) != 1 || a.messages[0].kind != msgError {
		t.Fatal("should have one error message")
	}
	if !strings.Contains(a.messages[0].content, "/help") {
		t.Errorf("error should mention /help, got %q", a.messages[0].content)
	}

	// Now use /config — should work fine
	simType(a, "/config")
	simKey(a, KeyEnter)

	if a.mode != modeConfig {
		t.Error("should enter config mode after /config")
	}

	// Cancel and return to chat
	simKey(a, KeyEscape)

	if a.mode != modeChat {
		t.Error("should return to chat mode after Esc")
	}
	// Should have the error message + the discard message
	if len(a.messages) != 2 {
		t.Errorf("messages count = %d, want 2", len(a.messages))
	}
}

// TestFullFlowConfigSaveAndVerifyWithPaste verifies the complete cycle:
// change config → save → paste at boundary → verify threshold applies.
func TestFullFlowConfigSaveAndVerifyWithPaste(t *testing.T) {
	a := newTestApp(80, 24)

	// Lower threshold to 50
	simType(a, "/config")
	simKey(a, KeyEnter)
	a.configForm.fields[0].input.SetValue("50")
	simKey(a, KeyEnter)

	if a.config.PasteCollapseMinChars != 50 {
		t.Fatalf("threshold = %d, want 50", a.config.PasteCollapseMinChars)
	}

	// Paste exactly at threshold (50 chars) — should collapse
	exactText := strings.Repeat("Q", 50)
	simPaste(a, exactText)
	if !strings.Contains(a.textarea.Value(), "[pasted #1 | 50 chars]") {
		t.Errorf("paste at exact threshold should collapse, got %q", a.textarea.Value())
	}
	simKey(a, KeyEnter)

	// Paste below threshold (49 chars) — should be verbatim
	belowText := strings.Repeat("R", 49)
	simPaste(a, belowText)
	if a.textarea.Value() != belowText {
		t.Errorf("paste below threshold should be verbatim, got %q", a.textarea.Value())
	}
	simKey(a, KeyEnter)

	// Verify messages
	// messages[0] = "Config saved." (system)
	// messages[1] = expanded paste (50 Q's) (user)
	// messages[2] = error (no API keys)
	// messages[3] = verbatim paste (49 R's) (user)
	// messages[4] = error (no API keys)
	if len(a.messages) != 5 {
		t.Fatalf("messages count = %d, want 5", len(a.messages))
	}
	if a.messages[1].content != exactText {
		t.Error("message[1] should contain expanded paste content")
	}
	if a.messages[3].content != belowText {
		t.Error("message[3] should contain verbatim paste content")
	}
}

// TestFullFlowConfigValidationThenSave tests that invalid config input
// is rejected, then corrected input saves successfully.
func TestFullFlowConfigValidationThenSave(t *testing.T) {
	a := newTestApp(80, 24)

	simType(a, "/config")
	simKey(a, KeyEnter)

	// Try invalid value
	a.configForm.fields[0].input.SetValue("not-a-number")
	simKey(a, KeyEnter)

	// Should stay in config mode
	if a.mode != modeConfig {
		t.Fatal("should stay in config mode with invalid input")
	}
	if a.configForm.fields[0].err == "" {
		t.Error("should show validation error")
	}

	// Fix the value and save
	a.configForm.fields[0].input.SetValue("100")
	simKey(a, KeyEnter)

	if a.mode != modeChat {
		t.Fatal("should return to chat mode after valid save")
	}
	if a.config.PasteCollapseMinChars != 100 {
		t.Errorf("threshold = %d, want 100", a.config.PasteCollapseMinChars)
	}
}

// TestFullFlowPasteWithSurroundingText tests paste collapsing when
// the user types text before and after a paste, then sends.
func TestFullFlowPasteWithSurroundingText(t *testing.T) {
	a := newTestApp(80, 24)

	// Type prefix, paste, type suffix, send
	simType(a, "see: ")
	longText := strings.Repeat("C", 250)
	simPaste(a, longText)
	simType(a, " done")
	simKey(a, KeyEnter)

	if len(a.messages) != 2 {
		t.Fatalf("messages count = %d, want 2", len(a.messages))
	}

	content := a.messages[0].content
	if !strings.HasPrefix(content, "see: ") {
		t.Errorf("message should start with 'see: ', got %q", content[:20])
	}
	if !strings.Contains(content, longText) {
		t.Error("message should contain the expanded paste content")
	}
	if !strings.HasSuffix(content, " done") {
		t.Errorf("message should end with ' done', got suffix %q", content[len(content)-10:])
	}
}

// TestFullFlowResizeDuringPasteAndConfig tests window resizing during
// paste collapsing and config mode doesn't break the UI.
func TestFullFlowResizeDuringPasteAndConfig(t *testing.T) {
	a := newTestApp(80, 24)

	// Paste and resize before sending
	longText := strings.Repeat("D", 300)
	simPaste(a, longText)
	simResize(a, 120, 40)

	// Textarea should still have the placeholder
	if !strings.Contains(a.textarea.Value(), "[pasted #1") {
		t.Error("resize should not lose paste placeholder")
	}

	simKey(a, KeyEnter)

	// Messages should contain expanded paste content
	if a.messages[0].content != longText {
		t.Error("message should contain expanded paste after resize")
	}

	// Enter config mode and resize
	simType(a, "/config")
	simKey(a, KeyEnter)

	simResize(a, 60, 20)
	if a.mode != modeConfig {
		t.Error("resize should not exit config mode")
	}
	if a.width != 60 || a.height != 20 {
		t.Errorf("dimensions = %dx%d, want 60x20", a.width, a.height)
	}
}

// TestFullFlowPasteCounterPersistsAcrossConfigVisits verifies the paste
// counter doesn't reset when entering and exiting config mode.
func TestFullFlowPasteCounterPersistsAcrossConfigVisits(t *testing.T) {
	a := newTestApp(80, 24)

	// Paste twice
	longText := strings.Repeat("E", 300)
	simPaste(a, longText)
	simKey(a, KeyEnter)
	simPaste(a, longText)
	simKey(a, KeyEnter)

	if a.pasteCount != 2 {
		t.Fatalf("pasteCount = %d, want 2", a.pasteCount)
	}

	// Enter and exit config mode
	simType(a, "/config")
	simKey(a, KeyEnter)
	simKey(a, KeyEscape)

	// Paste again — counter should continue from 2
	simPaste(a, longText)
	simKey(a, KeyEnter)

	if a.pasteCount != 3 {
		t.Errorf("pasteCount = %d, want 3 (should persist across config visits)", a.pasteCount)
	}

	// Verify correct paste ID in store
	expected := fmt.Sprintf("[pasted #3 | %d chars]", len(longText))
	_ = expected // used by paste detection logic
	if _, ok := a.pasteStore[3]; !ok {
		t.Error("pasteStore should have entry for paste #3")
	}
}

// TestFullFlowTabEnterExecutesCommand verifies the autocomplete flow:
// type partial command → Tab completes → Enter executes.
func TestFullFlowTabEnterExecutesCommand(t *testing.T) {
	a := newTestApp(80, 24)

	// Type partial command
	simType(a, "/con")

	// Autocomplete should show /config
	matches := a.autocompleteMatches()
	if len(matches) != 1 || matches[0] != "/config" {
		t.Fatalf("autocompleteMatches = %v, want [/config]", matches)
	}

	// Tab accepts the top match (/config)
	simKey(a, KeyTab)
	if a.textarea.Value() != "/config" {
		t.Fatalf("textarea = %q, want /config after Tab", a.textarea.Value())
	}

	// Enter executes the command
	simKey(a, KeyEnter)
	if a.mode != modeConfig {
		t.Errorf("mode = %d, want modeConfig after Tab+Enter", a.mode)
	}

	// Save and return to chat
	simKey(a, KeyEnter)

	if a.mode != modeChat {
		t.Error("should return to chat mode after saving config")
	}
	// Should show success message
	found := false
	for _, msg := range a.messages {
		if msg.kind == msgSuccess && strings.Contains(msg.content, "saved") {
			found = true
			break
		}
	}
	if !found {
		t.Error("should show saved message")
	}
}

// TestFullFlowEscDismissesAutocomplete verifies that Esc clears the
// slash input and returns to normal typing.
func TestFullFlowEscDismissesAutocomplete(t *testing.T) {
	a := newTestApp(80, 24)

	// Type partial command — autocomplete should be visible
	simType(a, "/co")
	if len(a.autocompleteMatches()) == 0 {
		t.Fatal("autocomplete should show matches for /co")
	}

	// Esc dismisses
	simKey(a, KeyEscape)
	if a.textarea.Value() != "" {
		t.Errorf("textarea = %q, want empty after Esc", a.textarea.Value())
	}
	if len(a.autocompleteMatches()) != 0 {
		t.Error("autocomplete should not show after Esc clears input")
	}

	// Should still be in chat mode and able to type normally
	simType(a, "hello")
	simKey(a, KeyEnter)

	if len(a.messages) != 2 {
		t.Fatalf("messages count = %d, want 2", len(a.messages))
	}
	if a.messages[0].content != "hello" {
		t.Errorf("message = %q, want hello", a.messages[0].content)
	}
}

// TestFullFlowAsyncModelsLoadAndSelect exercises the full async pattern:
// startup → modelsMsg arrives → /model → navigate → select → persists.
func TestFullFlowAsyncModelsLoadAndSelect(t *testing.T) {
	a := newTestApp(80, 24)
	a.config.AnthropicAPIKey = "sk-test"
	a.config.GrokAPIKey = ""
	a.config.OpenAIAPIKey = ""

	// Models not loaded yet
	if a.modelsLoaded {
		t.Fatal("models should not be loaded initially")
	}

	// Try /model before models arrive — should show loading message
	simType(a, "/model")
	simKey(a, KeyEnter)
	if a.mode != modeChat {
		t.Error("should stay in chat mode when models not loaded")
	}
	if len(a.messages) != 1 || a.messages[0].kind != msgInfo {
		t.Fatal("should show loading info message")
	}

	// Simulate modelsMsg arriving
	simResult(a, modelsMsg{models: testModels()})

	if !a.modelsLoaded {
		t.Fatal("modelsLoaded should be true after modelsMsg")
	}

	// Now /model should work
	simType(a, "/model")
	simKey(a, KeyEnter)
	if a.mode != modeModel {
		t.Fatalf("mode = %d, want modeModel", a.mode)
	}

	// Should only show Anthropic models
	for _, md := range a.modelList.models {
		if md.Provider != ProviderAnthropic {
			t.Errorf("should only show anthropic models, got %s", md.Provider)
		}
	}

	// Navigate down and select
	simKey(a, KeyDown)
	selected := a.modelList.selected()

	simKey(a, KeyEnter)

	if a.mode != modeChat {
		t.Error("should return to chat after selecting model")
	}
	if a.config.ActiveModel != selected.ID {
		t.Errorf("ActiveModel = %q, want %q", a.config.ActiveModel, selected.ID)
	}
}

// TestFullFlowAsyncModelsError exercises the error path:
// modelsMsg with error → /model shows error message.
func TestFullFlowAsyncModelsError(t *testing.T) {
	a := newTestApp(80, 24)
	a.config.AnthropicAPIKey = "key"

	// Simulate fetch error
	simResult(a, modelsMsg{err: errors.New("network timeout")})

	if !a.modelsLoaded {
		t.Fatal("modelsLoaded should be true even on error")
	}

	// /model should show the error
	simType(a, "/model")
	simKey(a, KeyEnter)

	if a.mode != modeChat {
		t.Error("should stay in chat mode on error")
	}
	found := false
	for _, msg := range a.messages {
		if msg.kind == msgError && strings.Contains(msg.content, "network timeout") {
			found = true
			break
		}
	}
	if !found {
		t.Error("should show fetch error message")
	}
}

// TestFullFlowProviderFilteringAfterConfigChange verifies that changing
// API keys in config correctly filters the model list on next /model.
func TestFullFlowProviderFilteringAfterConfigChange(t *testing.T) {
	a := newTestApp(80, 24)
	a.config.AnthropicAPIKey = "key"
	a.config.GrokAPIKey = ""
	a.config.OpenAIAPIKey = ""
	a.models = testModels()
	a.modelsLoaded = true

	// /model shows only Anthropic
	simType(a, "/model")
	simKey(a, KeyEnter)
	for _, md := range a.modelList.models {
		if md.Provider != ProviderAnthropic {
			t.Errorf("should only show anthropic, got %s", md.Provider)
		}
	}
	// Cancel
	simKey(a, KeyEscape)

	// Add OpenAI key
	a.config.OpenAIAPIKey = "openai-key"

	// /model now shows Anthropic + OpenAI
	simType(a, "/model")
	simKey(a, KeyEnter)

	hasAnthropic := false
	hasOpenAI := false
	for _, md := range a.modelList.models {
		if md.Provider == ProviderAnthropic {
			hasAnthropic = true
		}
		if md.Provider == ProviderOpenAI {
			hasOpenAI = true
		}
		if md.Provider == ProviderGrok {
			t.Error("should not show Grok models without key")
		}
	}
	if !hasAnthropic {
		t.Error("should show Anthropic models")
	}
	if !hasOpenAI {
		t.Error("should show OpenAI models after adding key")
	}
}

// TestFullFlowModelPricingInView verifies pricing is displayed in the model list.
func TestFullFlowModelPricingInView(t *testing.T) {
	a := newTestApp(80, 24)
	a.config.AnthropicAPIKey = "key"
	a.models = testModels()
	a.modelsLoaded = true

	simType(a, "/model")
	simKey(a, KeyEnter)

	view := a.modelList.View()
	// Should contain pricing info
	if !strings.Contains(view, "$") {
		t.Error("model list should show pricing with $ symbol")
	}
	if !strings.Contains(view, "1M tokens") {
		t.Error("model list should mention per 1M tokens in hint")
	}
}
