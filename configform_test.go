package main

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/textinput"
)

func TestConfigFormApplyToWritesAPIKeys(t *testing.T) {
	cfg := Config{
		PasteCollapseMinChars: 200,
		AnthropicAPIKey:       "sk-ant-old",
	}
	form := newConfigForm(cfg, 80, 24)

	// Set new API key values
	form.fields[1].input.SetValue("sk-ant-new")
	form.fields[2].input.SetValue("xai-key-123")
	form.fields[3].input.SetValue("sk-openai-456")

	var result Config
	form.applyTo(&result)

	if result.AnthropicAPIKey != "sk-ant-new" {
		t.Errorf("AnthropicAPIKey = %q, want %q", result.AnthropicAPIKey, "sk-ant-new")
	}
	if result.GrokAPIKey != "xai-key-123" {
		t.Errorf("GrokAPIKey = %q, want %q", result.GrokAPIKey, "xai-key-123")
	}
	if result.OpenAIAPIKey != "sk-openai-456" {
		t.Errorf("OpenAIAPIKey = %q, want %q", result.OpenAIAPIKey, "sk-openai-456")
	}
}

func TestConfigFormApplyToTrimsKeys(t *testing.T) {
	form := newConfigForm(Config{}, 80, 24)
	form.fields[1].input.SetValue("  sk-ant-123  ")

	var cfg Config
	form.applyTo(&cfg)

	if cfg.AnthropicAPIKey != "sk-ant-123" {
		t.Errorf("AnthropicAPIKey = %q, want trimmed %q", cfg.AnthropicAPIKey, "sk-ant-123")
	}
}

func TestConfigFormAPIKeyFieldsMasked(t *testing.T) {
	form := newConfigForm(Config{}, 80, 24)

	// Fields 1, 2, 3 are the API key fields
	for i := 1; i <= 3; i++ {
		if form.fields[i].input.EchoMode != textinput.EchoPassword {
			t.Errorf("field %d EchoMode = %d, want EchoPassword (%d)",
				i, form.fields[i].input.EchoMode, textinput.EchoPassword)
		}
	}
}

func TestConfigFormPrePopulatesKeys(t *testing.T) {
	cfg := Config{
		AnthropicAPIKey: "ant-key",
		GrokAPIKey:      "grok-key",
		OpenAIAPIKey:    "openai-key",
	}
	form := newConfigForm(cfg, 80, 24)

	if form.fields[1].input.Value() != "ant-key" {
		t.Errorf("Anthropic field = %q, want %q", form.fields[1].input.Value(), "ant-key")
	}
	if form.fields[2].input.Value() != "grok-key" {
		t.Errorf("Grok field = %q, want %q", form.fields[2].input.Value(), "grok-key")
	}
	if form.fields[3].input.Value() != "openai-key" {
		t.Errorf("OpenAI field = %q, want %q", form.fields[3].input.Value(), "openai-key")
	}
}

func TestConfigFormTabCyclesThroughAllFields(t *testing.T) {
	form := newConfigForm(Config{}, 80, 24)

	if form.focused != 0 {
		t.Fatalf("initial focus = %d, want 0", form.focused)
	}

	totalFields := len(form.fields)
	if totalFields != 4 {
		t.Fatalf("expected 4 fields (paste + 3 keys), got %d", totalFields)
	}

	// Tab through all fields
	for i := 0; i < totalFields; i++ {
		if form.focused != i {
			t.Errorf("before tab %d: focused = %d, want %d", i, form.focused, i)
		}
		form, _ = form.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	}
	// Should wrap around to 0
	if form.focused != 0 {
		t.Errorf("after full cycle: focused = %d, want 0", form.focused)
	}
}

func TestConfigFormShiftTabCyclesBackward(t *testing.T) {
	form := newConfigForm(Config{}, 80, 24)

	// Shift+tab from field 0 should wrap to last field
	form, _ = form.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if form.focused != len(form.fields)-1 {
		t.Errorf("shift+tab from 0: focused = %d, want %d", form.focused, len(form.fields)-1)
	}
}

func TestConfigFormValidateAcceptsEmptyKeys(t *testing.T) {
	form := newConfigForm(Config{PasteCollapseMinChars: 100}, 80, 24)

	// API key fields are empty — should still validate
	valid := form.validate()
	if !valid {
		t.Error("form with valid paste threshold and empty keys should be valid")
	}
}

func TestConfigFormApplyToEmptyKeysStayEmpty(t *testing.T) {
	form := newConfigForm(Config{}, 80, 24)

	var cfg Config
	form.applyTo(&cfg)

	if cfg.AnthropicAPIKey != "" {
		t.Errorf("AnthropicAPIKey should be empty, got %q", cfg.AnthropicAPIKey)
	}
	if cfg.GrokAPIKey != "" {
		t.Errorf("GrokAPIKey should be empty, got %q", cfg.GrokAPIKey)
	}
	if cfg.OpenAIAPIKey != "" {
		t.Errorf("OpenAIAPIKey should be empty, got %q", cfg.OpenAIAPIKey)
	}
}
