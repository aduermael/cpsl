package main

import "testing"

// testOpenRouterModels returns a model list that includes OpenRouter models.
func testOpenRouterModels() []ModelDef {
	return append(testModels(), []ModelDef{
		{Provider: ProviderOpenRouter, ID: "z-ai/glm-4.5-air:free", PromptPrice: 0, CompletionPrice: 0},
		{Provider: ProviderOpenRouter, ID: "deepseek/deepseek-r1:free", PromptPrice: 0, CompletionPrice: 0},
		{Provider: ProviderOpenRouter, ID: "google/gemini-2.0-flash-exp:free", PromptPrice: 0, CompletionPrice: 0},
		{Provider: ProviderOpenRouter, ID: "anthropic/claude-sonnet-4-5", PromptPrice: 3.0, CompletionPrice: 15.0},
	}...)
}

// --- configuredProviders ---

func TestConfiguredProvidersOpenRouter(t *testing.T) {
	cfg := Config{OpenRouterAPIKey: "sk-or-test"}
	p := cfg.configuredProviders()
	if !p[ProviderOpenRouter] {
		t.Error("expected openrouter to be configured when OpenRouterAPIKey is set")
	}
}

func TestConfiguredProvidersOpenRouterEmpty(t *testing.T) {
	cfg := Config{}
	p := cfg.configuredProviders()
	if p[ProviderOpenRouter] {
		t.Error("expected openrouter to NOT be configured when OpenRouterAPIKey is empty")
	}
}

func TestConfiguredProvidersOpenRouterAmongOthers(t *testing.T) {
	cfg := Config{AnthropicAPIKey: "ant-key", OpenRouterAPIKey: "sk-or-test"}
	p := cfg.configuredProviders()
	if !p[ProviderAnthropic] {
		t.Error("expected anthropic to be configured")
	}
	if !p[ProviderOpenRouter] {
		t.Error("expected openrouter to be configured")
	}
}

// --- defaultLangdagProvider ---

func TestDefaultLangdagProviderOpenRouter(t *testing.T) {
	cfg := Config{OpenRouterAPIKey: "sk-or-test"}
	got := cfg.defaultLangdagProvider()
	if got != ProviderOpenRouter {
		t.Errorf("defaultLangdagProvider = %q, want %q", got, ProviderOpenRouter)
	}
}

func TestDefaultLangdagProviderOpenRouterAfterGrok(t *testing.T) {
	// Grok should take priority over OpenRouter (comes first in fallback chain)
	cfg := Config{GrokAPIKey: "grok-key", OpenRouterAPIKey: "sk-or-test"}
	got := cfg.defaultLangdagProvider()
	if got != ProviderGrok {
		t.Errorf("defaultLangdagProvider = %q, want %q (grok before openrouter)", got, ProviderGrok)
	}
}

func TestDefaultLangdagProviderOpenRouterBeforeGemini(t *testing.T) {
	// OpenRouter should take priority over Gemini
	cfg := Config{OpenRouterAPIKey: "sk-or-test", GeminiAPIKey: "gemini-key"}
	got := cfg.defaultLangdagProvider()
	if got != ProviderOpenRouter {
		t.Errorf("defaultLangdagProvider = %q, want %q (openrouter before gemini)", got, ProviderOpenRouter)
	}
}

// --- defaultActiveModels / defaultExplorationModels ---

func TestDefaultActiveModelOpenRouterExists(t *testing.T) {
	if _, ok := defaultActiveModels[ProviderOpenRouter]; !ok {
		t.Error("defaultActiveModels should have an entry for ProviderOpenRouter")
	}
}

func TestDefaultExplorationModelOpenRouterExists(t *testing.T) {
	if _, ok := defaultExplorationModels[ProviderOpenRouter]; !ok {
		t.Error("defaultExplorationModels should have an entry for ProviderOpenRouter")
	}
}

func TestDefaultActiveModelOpenRouterIsFree(t *testing.T) {
	id := defaultActiveModels[ProviderOpenRouter]
	// OpenRouter free models use the ":free" suffix
	if len(id) == 0 {
		t.Fatal("default active model for openrouter is empty")
	}
	// We expect a free-tier model as the default
	if id[len(id)-5:] != ":free" {
		t.Errorf("default active model for openrouter %q should be a free model (ending in :free)", id)
	}
}

func TestDefaultExplorationModelOpenRouterIsFree(t *testing.T) {
	id := defaultExplorationModels[ProviderOpenRouter]
	if len(id) == 0 {
		t.Fatal("default exploration model for openrouter is empty")
	}
	if id[len(id)-5:] != ":free" {
		t.Errorf("default exploration model for openrouter %q should be a free model (ending in :free)", id)
	}
}

// --- resolveActiveModel with OpenRouter ---

func TestResolveActiveModelOpenRouter(t *testing.T) {
	models := testOpenRouterModels()
	cfg := Config{
		OpenRouterAPIKey: "sk-or-test",
		ActiveModel:      "deepseek/deepseek-r1:free",
	}
	resolved := cfg.resolveActiveModel(models)
	if resolved != "deepseek/deepseek-r1:free" {
		t.Errorf("resolveActiveModel = %q, want deepseek/deepseek-r1:free", resolved)
	}
}

func TestResolveActiveModelOpenRouterFallsBackToDefault(t *testing.T) {
	models := testOpenRouterModels()
	cfg := Config{
		OpenRouterAPIKey: "sk-or-test",
		// No ActiveModel set — should fall back to defaultActiveModels
	}
	resolved := cfg.resolveActiveModel(models)
	if resolved == "" {
		t.Error("expected a fallback model, got empty string")
	}
	m := findModelByID(models, resolved)
	if m == nil || m.Provider != ProviderOpenRouter {
		t.Errorf("fallback model %q should be an openrouter model", resolved)
	}
}

// --- availableModels ---

func TestAvailableModelsOpenRouter(t *testing.T) {
	models := testOpenRouterModels()
	cfg := Config{OpenRouterAPIKey: "sk-or-test"}
	available := cfg.availableModels(models)
	for _, m := range available {
		if m.Provider != ProviderOpenRouter {
			t.Errorf("expected only openrouter models, got provider %q", m.Provider)
		}
	}
	if len(available) == 0 {
		t.Error("expected at least one openrouter model")
	}
}

// --- resolveActiveModel / resolveExplorationModel defaults (mirrors config_test.go pattern) ---

// openRouterDefaultTestModels returns a model list that includes the exact
// default model IDs from defaultActiveModels and defaultExplorationModels
// for OpenRouter, so preferredDefault can find them.
func openRouterDefaultTestModels() []ModelDef {
	return []ModelDef{
		{Provider: ProviderOpenRouter, ID: defaultActiveModels[ProviderOpenRouter]},
		{Provider: ProviderOpenRouter, ID: defaultExplorationModels[ProviderOpenRouter]},
	}
}

func TestResolveActiveModel_OpenRouterDefaults(t *testing.T) {
	cfg := Config{OpenRouterAPIKey: "sk-or-test"} // no ActiveModel set
	got := cfg.resolveActiveModel(openRouterDefaultTestModels())
	want := defaultActiveModels[ProviderOpenRouter]
	if got != want {
		t.Errorf("resolveActiveModel = %q, want %q", got, want)
	}
}

func TestResolveExplorationModel_OpenRouterDefaults(t *testing.T) {
	cfg := Config{OpenRouterAPIKey: "sk-or-test"} // no ExplorationModel set
	got := cfg.resolveExplorationModel(openRouterDefaultTestModels())
	want := defaultExplorationModels[ProviderOpenRouter]
	if got != want {
		t.Errorf("resolveExplorationModel = %q, want %q", got, want)
	}
}

// --- config round-trip (mirrors TestLoadConfigRoundTripWithOllamaURL) ---

func TestLoadConfigRoundTripWithOpenRouterKey(t *testing.T) {
	dir := t.TempDir()

	original := Config{OpenRouterAPIKey: "sk-or-v1-test123"}
	if err := saveConfigTo(dir, original); err != nil {
		t.Fatalf("saveConfigTo: %v", err)
	}

	loaded, err := loadConfigFrom(dir)
	if err != nil {
		t.Fatalf("loadConfigFrom: %v", err)
	}

	if loaded.OpenRouterAPIKey != original.OpenRouterAPIKey {
		t.Errorf("OpenRouterAPIKey = %q, want %q", loaded.OpenRouterAPIKey, original.OpenRouterAPIKey)
	}
}

// --- defaultLangdagProvider priority ordering ---

func TestDefaultLangdagProviderOpenRouterAfterAnthropic(t *testing.T) {
	cfg := Config{AnthropicAPIKey: "ant-key", OpenRouterAPIKey: "sk-or-test"}
	got := cfg.defaultLangdagProvider()
	if got != ProviderAnthropic {
		t.Errorf("defaultLangdagProvider = %q, want %q (anthropic beats openrouter)", got, ProviderAnthropic)
	}
}

func TestDefaultLangdagProviderOpenRouterAfterOpenAI(t *testing.T) {
	cfg := Config{OpenAIAPIKey: "oai-key", OpenRouterAPIKey: "sk-or-test"}
	got := cfg.defaultLangdagProvider()
	if got != ProviderOpenAI {
		t.Errorf("defaultLangdagProvider = %q, want %q (openai beats openrouter)", got, ProviderOpenAI)
	}
}

func TestDefaultLangdagProviderOpenRouterBeforeOllama(t *testing.T) {
	cfg := Config{OpenRouterAPIKey: "sk-or-test", OllamaBaseURL: "http://localhost:11434"}
	got := cfg.defaultLangdagProvider()
	if got != ProviderOpenRouter {
		t.Errorf("defaultLangdagProvider = %q, want %q (openrouter before ollama)", got, ProviderOpenRouter)
	}
}

// --- availableModels filtering ---

func TestAvailableModelsOpenRouterFiltersOthers(t *testing.T) {
	// Mix of OpenRouter + Anthropic models; only OpenRouter key configured.
	models := testOpenRouterModels() // includes Anthropic models from testModels()
	cfg := Config{OpenRouterAPIKey: "sk-or-test"}
	available := cfg.availableModels(models)

	for _, m := range available {
		if m.Provider != ProviderOpenRouter {
			t.Errorf("expected only openrouter models, got provider %q for model %q", m.Provider, m.ID)
		}
	}
}

func TestAvailableModelsOpenRouterEmptyKey(t *testing.T) {
	models := testOpenRouterModels()
	cfg := Config{} // no OpenRouterAPIKey
	available := cfg.availableModels(models)

	for _, m := range available {
		if m.Provider == ProviderOpenRouter {
			t.Errorf("expected no openrouter models when key is empty, got %q", m.ID)
		}
	}
}

// --- resolveExplorationModel ---

func TestResolveExplorationModel_OpenRouterFallsBackToDefault(t *testing.T) {
	cfg := Config{OpenRouterAPIKey: "sk-or-test"} // no ExplorationModel set
	got := cfg.resolveExplorationModel(openRouterDefaultTestModels())
	if got == "" {
		t.Error("expected a fallback exploration model, got empty string")
	}
	m := findModelByID(openRouterDefaultTestModels(), got)
	if m == nil || m.Provider != ProviderOpenRouter {
		t.Errorf("fallback exploration model %q should be an openrouter model", got)
	}
}
