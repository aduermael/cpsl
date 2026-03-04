package main

import (
	"math"
	"testing"
)

// testModels returns a known model list for testing (OpenRouter-format IDs).
func testModels() []ModelDef {
	return []ModelDef{
		{Provider: ProviderAnthropic, ID: "anthropic/claude-sonnet-4-20250514", DisplayName: "Claude Sonnet 4", PromptPrice: 3.0, CompletionPrice: 15.0},
		{Provider: ProviderAnthropic, ID: "anthropic/claude-haiku-4-20250414", DisplayName: "Claude Haiku 4", PromptPrice: 0.8, CompletionPrice: 4.0},
		{Provider: ProviderAnthropic, ID: "anthropic/claude-opus-4-20250514", DisplayName: "Claude Opus 4", PromptPrice: 15.0, CompletionPrice: 75.0},
		{Provider: ProviderGrok, ID: "x-ai/grok-3", DisplayName: "Grok 3", PromptPrice: 3.0, CompletionPrice: 15.0},
		{Provider: ProviderGrok, ID: "x-ai/grok-3-mini", DisplayName: "Grok 3 Mini", PromptPrice: 0.3, CompletionPrice: 0.5},
		{Provider: ProviderOpenAI, ID: "openai/gpt-4o", DisplayName: "GPT-4o", PromptPrice: 2.5, CompletionPrice: 10.0},
		{Provider: ProviderOpenAI, ID: "openai/gpt-4o-mini", DisplayName: "GPT-4o Mini", PromptPrice: 0.15, CompletionPrice: 0.6},
		{Provider: ProviderOpenAI, ID: "openai/o3-mini", DisplayName: "o3-mini", PromptPrice: 1.1, CompletionPrice: 4.4},
	}
}

func TestFilterModelsByProvidersSingle(t *testing.T) {
	providers := map[string]bool{ProviderAnthropic: true}
	models := filterModelsByProviders(testModels(), providers)

	for _, m := range models {
		if m.Provider != ProviderAnthropic {
			t.Errorf("expected only anthropic models, got provider %q", m.Provider)
		}
	}
	if len(models) == 0 {
		t.Error("expected at least one anthropic model")
	}
}

func TestFilterModelsByProvidersMultiple(t *testing.T) {
	providers := map[string]bool{ProviderGrok: true, ProviderOpenAI: true}
	models := filterModelsByProviders(testModels(), providers)

	for _, m := range models {
		if m.Provider != ProviderGrok && m.Provider != ProviderOpenAI {
			t.Errorf("unexpected provider %q", m.Provider)
		}
	}
	if len(models) == 0 {
		t.Error("expected models for grok and openai")
	}
}

func TestFilterModelsByProvidersEmpty(t *testing.T) {
	models := filterModelsByProviders(testModels(), map[string]bool{})
	if len(models) != 0 {
		t.Errorf("expected no models, got %d", len(models))
	}
}

func TestFindModelByIDFound(t *testing.T) {
	m := findModelByID(testModels(), "openai/gpt-4o")
	if m == nil {
		t.Fatal("expected to find openai/gpt-4o")
	}
	if m.Provider != ProviderOpenAI {
		t.Errorf("provider = %q, want openai", m.Provider)
	}
}

func TestFindModelByIDNotFound(t *testing.T) {
	m := findModelByID(testModels(), "nonexistent-model")
	if m != nil {
		t.Errorf("expected nil for nonexistent model, got %+v", m)
	}
}

func TestConfiguredProvidersNone(t *testing.T) {
	cfg := Config{}
	p := cfg.configuredProviders()
	if len(p) != 0 {
		t.Errorf("expected no providers, got %v", p)
	}
}

func TestConfiguredProvidersSome(t *testing.T) {
	cfg := Config{AnthropicAPIKey: "sk-ant-123", OpenAIAPIKey: "sk-openai-456"}
	p := cfg.configuredProviders()

	if !p[ProviderAnthropic] {
		t.Error("expected anthropic to be configured")
	}
	if p[ProviderGrok] {
		t.Error("expected grok to NOT be configured")
	}
	if !p[ProviderOpenAI] {
		t.Error("expected openai to be configured")
	}
}

func TestConfiguredProvidersAll(t *testing.T) {
	cfg := Config{
		AnthropicAPIKey: "key1",
		GrokAPIKey:      "key2",
		OpenAIAPIKey:    "key3",
	}
	p := cfg.configuredProviders()
	if len(p) != 3 {
		t.Errorf("expected 3 providers, got %d", len(p))
	}
}

func TestAvailableModelsFilters(t *testing.T) {
	cfg := Config{GrokAPIKey: "xai-key"}
	models := cfg.availableModels(testModels())

	for _, m := range models {
		if m.Provider != ProviderGrok {
			t.Errorf("expected only grok models, got provider %q", m.Provider)
		}
	}
	if len(models) == 0 {
		t.Error("expected at least one grok model")
	}
}

func TestResolveActiveModelValid(t *testing.T) {
	cfg := Config{
		AnthropicAPIKey: "key",
		ActiveModel:     "anthropic/claude-sonnet-4-20250514",
	}
	resolved := cfg.resolveActiveModel(testModels())
	if resolved != "anthropic/claude-sonnet-4-20250514" {
		t.Errorf("resolveActiveModel = %q, want anthropic/claude-sonnet-4-20250514", resolved)
	}
}

func TestResolveActiveModelMissingKeyFallback(t *testing.T) {
	cfg := Config{
		GrokAPIKey:  "key",
		ActiveModel: "anthropic/claude-sonnet-4-20250514",
	}
	models := testModels()
	resolved := cfg.resolveActiveModel(models)
	if resolved == "anthropic/claude-sonnet-4-20250514" {
		t.Error("should not resolve to a model whose provider has no key")
	}
	if resolved == "" {
		t.Error("should fall back to first available model")
	}
	m := findModelByID(models, resolved)
	if m == nil || m.Provider != ProviderGrok {
		t.Errorf("fallback should be a grok model, got %q", resolved)
	}
}

func TestResolveActiveModelEmptyConfig(t *testing.T) {
	cfg := Config{}
	resolved := cfg.resolveActiveModel(testModels())
	if resolved != "" {
		t.Errorf("resolveActiveModel with no keys = %q, want empty", resolved)
	}
}

func TestResolveActiveModelInvalidID(t *testing.T) {
	cfg := Config{
		OpenAIAPIKey: "key",
		ActiveModel:  "nonexistent-model",
	}
	models := testModels()
	resolved := cfg.resolveActiveModel(models)
	if resolved == "nonexistent-model" {
		t.Error("should not resolve to invalid model ID")
	}
	m := findModelByID(models, resolved)
	if m == nil || m.Provider != ProviderOpenAI {
		t.Errorf("fallback should be an openai model, got %q", resolved)
	}
}

// --- parsePrice tests ---

func TestParsePriceNormal(t *testing.T) {
	// $3 per million tokens: "0.000003" per token * 1M = 3.0
	got := parsePrice("0.000003")
	if math.Abs(got-3.0) > 0.001 {
		t.Errorf("parsePrice(0.000003) = %f, want 3.0", got)
	}
}

func TestParsePriceZero(t *testing.T) {
	got := parsePrice("0")
	if got != 0 {
		t.Errorf("parsePrice(0) = %f, want 0", got)
	}
}

func TestParsePriceInvalid(t *testing.T) {
	got := parsePrice("not-a-number")
	if got != 0 {
		t.Errorf("parsePrice(not-a-number) = %f, want 0", got)
	}
}

func TestParsePriceEmpty(t *testing.T) {
	got := parsePrice("")
	if got != 0 {
		t.Errorf("parsePrice(\"\") = %f, want 0", got)
	}
}

// --- parseOpenRouterModels tests ---

func TestParseOpenRouterModelsFiltersProviders(t *testing.T) {
	data := []openRouterModel{
		{ID: "anthropic/claude-sonnet-4", Name: "Claude Sonnet 4", Pricing: openRouterPricing{Prompt: "0.000003", Completion: "0.000015"}},
		{ID: "openai/gpt-4o", Name: "GPT-4o", Pricing: openRouterPricing{Prompt: "0.0000025", Completion: "0.00001"}},
		{ID: "x-ai/grok-3", Name: "Grok 3", Pricing: openRouterPricing{Prompt: "0.000003", Completion: "0.000015"}},
		{ID: "google/gemini-pro", Name: "Gemini Pro", Pricing: openRouterPricing{Prompt: "0.000001", Completion: "0.000002"}},
		{ID: "meta-llama/llama-3", Name: "Llama 3", Pricing: openRouterPricing{Prompt: "0.000001", Completion: "0.000001"}},
	}

	models := parseOpenRouterModels(data)

	if len(models) != 3 {
		t.Fatalf("expected 3 models (anthropic, openai, x-ai), got %d", len(models))
	}

	providers := map[string]bool{}
	for _, m := range models {
		providers[m.Provider] = true
	}
	if !providers[ProviderAnthropic] || !providers[ProviderOpenAI] || !providers[ProviderGrok] {
		t.Errorf("expected all 3 supported providers, got %v", providers)
	}
}

func TestParseOpenRouterModelsPricing(t *testing.T) {
	data := []openRouterModel{
		{ID: "anthropic/claude-sonnet-4", Name: "Claude Sonnet 4", Pricing: openRouterPricing{Prompt: "0.000003", Completion: "0.000015"}},
	}

	models := parseOpenRouterModels(data)
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}

	m := models[0]
	if math.Abs(m.PromptPrice-3.0) > 0.001 {
		t.Errorf("PromptPrice = %f, want 3.0", m.PromptPrice)
	}
	if math.Abs(m.CompletionPrice-15.0) > 0.001 {
		t.Errorf("CompletionPrice = %f, want 15.0", m.CompletionPrice)
	}
}

func TestParseOpenRouterModelsEmpty(t *testing.T) {
	models := parseOpenRouterModels(nil)
	if len(models) != 0 {
		t.Errorf("expected 0 models for nil input, got %d", len(models))
	}
}

func TestParseOpenRouterModelsUnknownPrefixOnly(t *testing.T) {
	data := []openRouterModel{
		{ID: "google/gemini-pro", Name: "Gemini Pro", Pricing: openRouterPricing{Prompt: "0.000001", Completion: "0.000002"}},
	}
	models := parseOpenRouterModels(data)
	if len(models) != 0 {
		t.Errorf("expected 0 models for unsupported provider, got %d", len(models))
	}
}

// --- formatPrice tests ---

func TestFormatPriceWhole(t *testing.T) {
	got := formatPrice(3.0)
	if got != "$3.00" {
		t.Errorf("formatPrice(3.0) = %q, want $3.00", got)
	}
}

func TestFormatPriceFractional(t *testing.T) {
	got := formatPrice(0.15)
	if got != "$0.15" {
		t.Errorf("formatPrice(0.15) = %q, want $0.15", got)
	}
}

func TestFormatPriceZero(t *testing.T) {
	got := formatPrice(0)
	if got != "$0.00" {
		t.Errorf("formatPrice(0) = %q, want $0.00", got)
	}
}

// --- parseSWEScores tests ---

func TestParseSWEScoresBasic(t *testing.T) {
	resp := sweBenchResponse{
		Leaderboards: []sweBenchLeaderboard{
			{
				Name: "Verified",
				Results: []sweBenchResult{
					{Name: "Agent A + Claude Opus", Resolved: 72.5, Tags: []string{"Model: claude-opus-4-5-20251101"}},
					{Name: "Agent B + Claude Opus", Resolved: 79.2, Tags: []string{"Model: claude-opus-4-5-20251101"}},
					{Name: "Agent C + GPT-4o", Resolved: 55.0, Tags: []string{"Model: gpt-4o-2024-11-20"}},
				},
			},
		},
	}

	scores := parseSWEScores(resp)

	if scores["claude-opus-4-5-20251101"] != 79.2 {
		t.Errorf("claude-opus score = %f, want 79.2", scores["claude-opus-4-5-20251101"])
	}
	if scores["gpt-4o-2024-11-20"] != 55.0 {
		t.Errorf("gpt-4o score = %f, want 55.0", scores["gpt-4o-2024-11-20"])
	}
}

func TestParseSWEScoresSkipsMultiModelEntries(t *testing.T) {
	resp := sweBenchResponse{
		Leaderboards: []sweBenchLeaderboard{
			{
				Name: "Verified",
				Results: []sweBenchResult{
					{Name: "Multi-model agent", Resolved: 90.0, Tags: []string{"Model: claude-4-sonnet", "Model: o3-mini"}},
					{Name: "Single model agent", Resolved: 60.0, Tags: []string{"Model: gpt-4o"}},
				},
			},
		},
	}

	scores := parseSWEScores(resp)

	if _, ok := scores["claude-4-sonnet"]; ok {
		t.Error("should skip multi-model entries")
	}
	if _, ok := scores["o3-mini"]; ok {
		t.Error("should skip multi-model entries")
	}
	if scores["gpt-4o"] != 60.0 {
		t.Errorf("gpt-4o score = %f, want 60.0", scores["gpt-4o"])
	}
}

func TestParseSWEScoresOnlyVerified(t *testing.T) {
	resp := sweBenchResponse{
		Leaderboards: []sweBenchLeaderboard{
			{
				Name: "Lite",
				Results: []sweBenchResult{
					{Name: "Agent A", Resolved: 99.0, Tags: []string{"Model: should-not-appear"}},
				},
			},
			{
				Name: "Verified",
				Results: []sweBenchResult{
					{Name: "Agent B", Resolved: 50.0, Tags: []string{"Model: verified-model"}},
				},
			},
		},
	}

	scores := parseSWEScores(resp)

	if _, ok := scores["should-not-appear"]; ok {
		t.Error("should only parse Verified leaderboard")
	}
	if scores["verified-model"] != 50.0 {
		t.Errorf("verified-model score = %f, want 50.0", scores["verified-model"])
	}
}

func TestParseSWEScoresEmpty(t *testing.T) {
	scores := parseSWEScores(sweBenchResponse{})
	if len(scores) != 0 {
		t.Errorf("expected empty scores for empty response, got %d", len(scores))
	}
}

func TestParseSWEScoresNoModelTag(t *testing.T) {
	resp := sweBenchResponse{
		Leaderboards: []sweBenchLeaderboard{
			{
				Name: "Verified",
				Results: []sweBenchResult{
					{Name: "No model tag", Resolved: 70.0, Tags: []string{"Language: Python"}},
				},
			},
		},
	}

	scores := parseSWEScores(resp)
	if len(scores) != 0 {
		t.Errorf("expected empty scores when no Model tags, got %d", len(scores))
	}
}

// --- matchSWEScores tests ---

func TestMatchSWEScoresExactSuffix(t *testing.T) {
	models := []ModelDef{
		{Provider: ProviderAnthropic, ID: "anthropic/claude-opus-4-5-20251101"},
		{Provider: ProviderOpenAI, ID: "openai/gpt-4o-2024-11-20"},
	}
	scores := map[string]float64{
		"claude-opus-4-5-20251101": 79.2,
		"gpt-4o-2024-11-20":       55.0,
	}

	matchSWEScores(models, scores)

	if models[0].SWEScore != 79.2 {
		t.Errorf("claude-opus SWEScore = %f, want 79.2", models[0].SWEScore)
	}
	if models[1].SWEScore != 55.0 {
		t.Errorf("gpt-4o SWEScore = %f, want 55.0", models[1].SWEScore)
	}
}

func TestMatchSWEScoresSubstringMatch(t *testing.T) {
	models := []ModelDef{
		{Provider: ProviderOpenAI, ID: "openai/o3-mini"},
	}
	scores := map[string]float64{
		"o3-mini-2025-01-31": 49.3,
	}

	matchSWEScores(models, scores)

	if models[0].SWEScore != 49.3 {
		t.Errorf("o3-mini SWEScore = %f, want 49.3", models[0].SWEScore)
	}
}

func TestMatchSWEScoresNoMatch(t *testing.T) {
	models := []ModelDef{
		{Provider: ProviderGrok, ID: "x-ai/grok-3"},
	}
	scores := map[string]float64{
		"claude-opus-4-5": 79.2,
	}

	matchSWEScores(models, scores)

	if models[0].SWEScore != 0 {
		t.Errorf("grok SWEScore = %f, want 0 (no match)", models[0].SWEScore)
	}
}

func TestMatchSWEScoresTakesBestScore(t *testing.T) {
	models := []ModelDef{
		{Provider: ProviderAnthropic, ID: "anthropic/claude-sonnet-4"},
	}
	scores := map[string]float64{
		"claude-sonnet-4":            60.0,
		"claude-sonnet-4-20250514":   65.0,
	}

	matchSWEScores(models, scores)

	if models[0].SWEScore != 65.0 {
		t.Errorf("SWEScore = %f, want 65.0 (best match)", models[0].SWEScore)
	}
}

// --- cleanDisplayName tests ---

func TestCleanDisplayName(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		want     string
	}{
		{"Anthropic: Claude Opus 4", ProviderAnthropic, "Claude Opus 4"},
		{"OpenAI: GPT-4o", ProviderOpenAI, "GPT-4o"},
		{"xAI: Grok 3", ProviderGrok, "Grok 3"},
		{"X AI: Grok 3 Mini", ProviderGrok, "Grok 3 Mini"},
		{"GPT-4o", ProviderOpenAI, "GPT-4o"},           // no prefix
		{"Claude Sonnet 4", ProviderAnthropic, "Claude Sonnet 4"}, // no prefix
		{"anthropic: lower case", ProviderAnthropic, "lower case"}, // case-insensitive
	}
	for _, tt := range tests {
		got := cleanDisplayName(tt.name, tt.provider)
		if got != tt.want {
			t.Errorf("cleanDisplayName(%q, %q) = %q, want %q", tt.name, tt.provider, got, tt.want)
		}
	}
}

func TestMatchSWEScoresEmptyScores(t *testing.T) {
	models := testModels()
	matchSWEScores(models, map[string]float64{})

	for _, m := range models {
		if m.SWEScore != 0 {
			t.Errorf("model %s should have SWEScore 0 with empty scores, got %f", m.ID, m.SWEScore)
		}
	}
}
