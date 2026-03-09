package main

import (
	"testing"

	"langdag.com/langdag"
	"langdag.com/langdag/types"
)

// testModels returns a known model list for testing (native API IDs).
func testModels() []ModelDef {
	return []ModelDef{
		{Provider: ProviderAnthropic, ID: "claude-sonnet-4-0-20250514", DisplayName: "Claude Sonnet 4", PromptPrice: 3.0, CompletionPrice: 15.0},
		{Provider: ProviderAnthropic, ID: "claude-haiku-4-5-20250414", DisplayName: "Claude Haiku 4.5", PromptPrice: 0.8, CompletionPrice: 4.0},
		{Provider: ProviderAnthropic, ID: "claude-opus-4-0-20250514", DisplayName: "Claude Opus 4", PromptPrice: 15.0, CompletionPrice: 75.0},
		{Provider: ProviderGrok, ID: "grok-3", DisplayName: "Grok 3", PromptPrice: 3.0, CompletionPrice: 15.0},
		{Provider: ProviderGrok, ID: "grok-3-mini", DisplayName: "Grok 3 Mini", PromptPrice: 0.3, CompletionPrice: 0.5},
		{Provider: ProviderOpenAI, ID: "gpt-4o", DisplayName: "GPT-4o", PromptPrice: 2.5, CompletionPrice: 10.0},
		{Provider: ProviderOpenAI, ID: "gpt-4o-mini", DisplayName: "GPT-4o Mini", PromptPrice: 0.15, CompletionPrice: 0.6},
		{Provider: ProviderOpenAI, ID: "o3-mini", DisplayName: "o3-mini", PromptPrice: 1.1, CompletionPrice: 4.4},
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
	m := findModelByID(testModels(), "gpt-4o")
	if m == nil {
		t.Fatal("expected to find gpt-4o")
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
		ActiveModel:     "claude-sonnet-4-0-20250514",
	}
	resolved := cfg.resolveActiveModel(testModels())
	if resolved != "claude-sonnet-4-0-20250514" {
		t.Errorf("resolveActiveModel = %q, want claude-sonnet-4-0-20250514", resolved)
	}
}

func TestResolveActiveModelMissingKeyFallback(t *testing.T) {
	cfg := Config{
		GrokAPIKey:  "key",
		ActiveModel: "claude-sonnet-4-0-20250514",
	}
	models := testModels()
	resolved := cfg.resolveActiveModel(models)
	if resolved == "claude-sonnet-4-0-20250514" {
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

// --- builtinModels tests ---

func TestBuiltinModelsNotEmpty(t *testing.T) {
	models := builtinModels()
	if len(models) == 0 {
		t.Error("builtinModels should return at least one model")
	}
}

func TestBuiltinModelsHaveRequiredFields(t *testing.T) {
	for _, m := range builtinModels() {
		if m.Provider == "" {
			t.Errorf("model %q has empty provider", m.ID)
		}
		if m.ID == "" {
			t.Errorf("model %q has empty ID", m.DisplayName)
		}
		if m.DisplayName == "" {
			t.Errorf("model %q has empty display name", m.ID)
		}
	}
}

func TestBuiltinModelsHaveMultipleProviders(t *testing.T) {
	providers := make(map[string]bool)
	for _, m := range builtinModels() {
		providers[m.Provider] = true
	}
	if len(providers) < 2 {
		t.Errorf("expected at least 2 providers in builtin models, got %d", len(providers))
	}
}

func TestModelsJSONLoadsAllEntries(t *testing.T) {
	models := builtinModels()
	if len(models) < 20 {
		t.Errorf("expected at least 20 models from models.json, got %d", len(models))
	}
	providers := make(map[string]int)
	for _, m := range models {
		providers[m.Provider]++
	}
	for _, p := range []string{ProviderAnthropic, ProviderGrok, ProviderOpenAI, ProviderGemini} {
		if providers[p] == 0 {
			t.Errorf("expected at least one model for provider %q", p)
		}
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

func TestMatchSWEScoresExactMatch(t *testing.T) {
	models := []ModelDef{
		{Provider: ProviderAnthropic, ID: "claude-opus-4-5-20250620"},
		{Provider: ProviderOpenAI, ID: "gpt-4o"},
	}
	scores := map[string]float64{
		"claude-opus-4-5-20250620": 79.2,
		"gpt-4o":                   55.0,
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
		{Provider: ProviderOpenAI, ID: "o3-mini"},
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
		{Provider: ProviderGrok, ID: "grok-3"},
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
		{Provider: ProviderAnthropic, ID: "claude-sonnet-4-0-20250514"},
	}
	scores := map[string]float64{
		"claude-sonnet-4-0-20250514": 65.0,
		"claude-sonnet-4":            60.0,
	}

	matchSWEScores(models, scores)

	if models[0].SWEScore != 65.0 {
		t.Errorf("SWEScore = %f, want 65.0 (best match)", models[0].SWEScore)
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

// --- sortModelsByCol tests ---

func sortTestModels() []ModelDef {
	return []ModelDef{
		{Provider: "openai", ID: "gpt-4o", DisplayName: "GPT-4o", PromptPrice: 2.5, ContextWindow: 128000},
		{Provider: "anthropic", ID: "claude-opus", DisplayName: "Claude Opus", PromptPrice: 15.0, ContextWindow: 200000},
		{Provider: "gemini", ID: "gemini-pro", DisplayName: "Gemini Pro", PromptPrice: 1.25, ContextWindow: 1000000},
	}
}

func TestSortModelsByColNameAsc(t *testing.T) {
	m := sortTestModels()
	sortModelsByCol(m, 0, true)
	want := []string{"Claude Opus", "Gemini Pro", "GPT-4o"}
	for i, w := range want {
		if m[i].DisplayName != w {
			t.Errorf("index %d: got %s, want %s", i, m[i].DisplayName, w)
		}
	}
}

func TestSortModelsByColNameDesc(t *testing.T) {
	m := sortTestModels()
	sortModelsByCol(m, 0, false)
	want := []string{"GPT-4o", "Gemini Pro", "Claude Opus"}
	for i, w := range want {
		if m[i].DisplayName != w {
			t.Errorf("index %d: got %s, want %s", i, m[i].DisplayName, w)
		}
	}
}

func TestSortModelsByColProvider(t *testing.T) {
	m := sortTestModels()
	sortModelsByCol(m, 1, true)
	want := []string{"anthropic", "gemini", "openai"}
	for i, w := range want {
		if m[i].Provider != w {
			t.Errorf("index %d: got %s, want %s", i, m[i].Provider, w)
		}
	}
}

func TestSortModelsByColPrice(t *testing.T) {
	m := sortTestModels()
	sortModelsByCol(m, 2, true)
	want := []float64{1.25, 2.5, 15.0}
	for i, w := range want {
		if m[i].PromptPrice != w {
			t.Errorf("index %d: got %f, want %f", i, m[i].PromptPrice, w)
		}
	}
}

func TestSortModelsByColPriceDesc(t *testing.T) {
	m := sortTestModels()
	sortModelsByCol(m, 2, false)
	want := []float64{15.0, 2.5, 1.25}
	for i, w := range want {
		if m[i].PromptPrice != w {
			t.Errorf("index %d: got %f, want %f", i, m[i].PromptPrice, w)
		}
	}
}

func TestSortModelsByColContext(t *testing.T) {
	m := sortTestModels()
	sortModelsByCol(m, 3, true)
	want := []int{128000, 200000, 1000000}
	for i, w := range want {
		if m[i].ContextWindow != w {
			t.Errorf("index %d: got %d, want %d", i, m[i].ContextWindow, w)
		}
	}
}

func TestSortColFromName(t *testing.T) {
	tests := []struct {
		name string
		want int
	}{
		{"name", 0},
		{"provider", 1},
		{"price", 2},
		{"context", 3},
		{"unknown", 0},
		{"", 0},
	}
	for _, tt := range tests {
		if got := sortColFromName(tt.name); got != tt.want {
			t.Errorf("sortColFromName(%q) = %d, want %d", tt.name, got, tt.want)
		}
	}
}

// --- enrichModelsFromCatalog tests ---

func TestEnrichModelsFromCatalog(t *testing.T) {
	models := []ModelDef{
		{Provider: "anthropic", ID: "claude-sonnet-4-0-20250514", DisplayName: "Claude Sonnet 4"},
		{Provider: "openai", ID: "gpt-4o", DisplayName: "GPT-4o"},
		{Provider: "grok", ID: "unknown-model", DisplayName: "Unknown"},
	}

	catalog := &langdag.ModelCatalog{
		Providers: map[string][]langdag.ModelPricing{
			"anthropic": {
				{ID: "claude-sonnet-4-0-20250514", InputPricePer1M: 3.0, OutputPricePer1M: 15.0, ContextWindow: 200000},
			},
			"openai": {
				{ID: "gpt-4o", InputPricePer1M: 2.5, OutputPricePer1M: 10.0, ContextWindow: 128000},
			},
		},
	}

	enrichModelsFromCatalog(models, catalog)

	// Matched models get catalog pricing
	if models[0].PromptPrice != 3.0 {
		t.Errorf("claude sonnet input price: got %f, want 3.0", models[0].PromptPrice)
	}
	if models[0].CompletionPrice != 15.0 {
		t.Errorf("claude sonnet output price: got %f, want 15.0", models[0].CompletionPrice)
	}
	if models[0].ContextWindow != 200000 {
		t.Errorf("claude sonnet context: got %d, want 200000", models[0].ContextWindow)
	}
	if models[1].PromptPrice != 2.5 {
		t.Errorf("gpt-4o input price: got %f, want 2.5", models[1].PromptPrice)
	}

	// Unmatched model keeps zero values
	if models[2].PromptPrice != 0 {
		t.Errorf("unknown model should have zero price, got %f", models[2].PromptPrice)
	}
}

func TestEnrichModelsNilCatalog(t *testing.T) {
	models := []ModelDef{
		{Provider: "anthropic", ID: "test", DisplayName: "Test", PromptPrice: 5.0},
	}
	enrichModelsFromCatalog(models, nil)
	if models[0].PromptPrice != 5.0 {
		t.Errorf("nil catalog should not modify prices, got %f", models[0].PromptPrice)
	}
}

// --- computeCost tests ---

func TestComputeCostStandardTokens(t *testing.T) {
	models := []ModelDef{
		{Provider: ProviderOpenAI, ID: "gpt-4o", PromptPrice: 2.5, CompletionPrice: 10.0},
	}
	usage := types.Usage{InputTokens: 1000, OutputTokens: 500}
	got := computeCost(models, "gpt-4o", usage)
	// (1000 * 2.5 + 500 * 10.0) / 1_000_000 = (2500 + 5000) / 1_000_000 = 0.0075
	want := 0.0075
	if got != want {
		t.Errorf("computeCost = %f, want %f", got, want)
	}
}

func TestComputeCostAnthropicCacheRead(t *testing.T) {
	models := []ModelDef{
		{Provider: ProviderAnthropic, ID: "claude-sonnet-4-0-20250514", PromptPrice: 3.0, CompletionPrice: 15.0},
	}
	usage := types.Usage{
		InputTokens:          1000,
		OutputTokens:         500,
		CacheReadInputTokens: 10000,
	}
	got := computeCost(models, "claude-sonnet-4-0-20250514", usage)
	// input: 1000 * 3.0 / 1M = 0.003
	// output: 500 * 15.0 / 1M = 0.0075
	// cache read: 10000 * 3.0 * 0.1 / 1M = 0.003
	want := 0.003 + 0.0075 + 0.003
	if got != want {
		t.Errorf("computeCost = %f, want %f", got, want)
	}
}

func TestComputeCostNonAnthropicCacheReadIgnored(t *testing.T) {
	models := []ModelDef{
		{Provider: ProviderOpenAI, ID: "gpt-4o", PromptPrice: 2.5, CompletionPrice: 10.0},
	}
	usage := types.Usage{
		InputTokens:          1000,
		OutputTokens:         500,
		CacheReadInputTokens: 10000,
	}
	got := computeCost(models, "gpt-4o", usage)
	// Cache read tokens should not add cost for non-Anthropic
	want := (1000*2.5 + 500*10.0) / 1_000_000
	if got != want {
		t.Errorf("computeCost = %f, want %f", got, want)
	}
}

func TestComputeCostModelNotFound(t *testing.T) {
	models := []ModelDef{
		{Provider: ProviderOpenAI, ID: "gpt-4o", PromptPrice: 2.5, CompletionPrice: 10.0},
	}
	usage := types.Usage{InputTokens: 1000, OutputTokens: 500}
	got := computeCost(models, "nonexistent-model", usage)
	if got != 0 {
		t.Errorf("computeCost for unknown model = %f, want 0", got)
	}
}

func TestComputeCostZeroPricing(t *testing.T) {
	models := []ModelDef{
		{Provider: ProviderGrok, ID: "grok-free", PromptPrice: 0, CompletionPrice: 0},
	}
	usage := types.Usage{InputTokens: 5000, OutputTokens: 1000}
	got := computeCost(models, "grok-free", usage)
	if got != 0 {
		t.Errorf("computeCost with zero pricing = %f, want 0", got)
	}
}

// --- formatCost tests ---

func TestFormatCostSmall(t *testing.T) {
	got := formatCost(0.0075)
	if got != "$0.0075" {
		t.Errorf("formatCost(0.0075) = %q, want $0.0075", got)
	}
}

func TestFormatCostLarge(t *testing.T) {
	got := formatCost(1.23)
	if got != "$1.23" {
		t.Errorf("formatCost(1.23) = %q, want $1.23", got)
	}
}

func TestFormatCostBoundary(t *testing.T) {
	got := formatCost(0.01)
	if got != "$0.01" {
		t.Errorf("formatCost(0.01) = %q, want $0.01", got)
	}
}
