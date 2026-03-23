package main

import (
	"testing"

	"langdag.com/langdag"
	"langdag.com/langdag/types"
)

// testModels returns a known model list for testing.
func testModels() []ModelDef {
	return []ModelDef{
		{Provider: ProviderAnthropic, ID: "claude-sonnet-4-0-20250514", PromptPrice: 3.0, CompletionPrice: 15.0},
		{Provider: ProviderAnthropic, ID: "claude-haiku-4-5-20250414", PromptPrice: 0.8, CompletionPrice: 4.0},
		{Provider: ProviderAnthropic, ID: "claude-opus-4-0-20250514", PromptPrice: 15.0, CompletionPrice: 75.0},
		{Provider: ProviderGrok, ID: "grok-4-1-fast-reasoning", PromptPrice: 3.0, CompletionPrice: 15.0},
		{Provider: ProviderGrok, ID: "grok-4-1-fast-non-reasoning", PromptPrice: 0.3, CompletionPrice: 0.5},
		{Provider: ProviderOpenAI, ID: "gpt-4o", PromptPrice: 2.5, CompletionPrice: 10.0},
		{Provider: ProviderOpenAI, ID: "gpt-4o-mini", PromptPrice: 0.15, CompletionPrice: 0.6},
		{Provider: ProviderOpenAI, ID: "o3-mini", PromptPrice: 1.1, CompletionPrice: 4.4},
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

// --- modelsFromCatalog tests ---

func TestModelsFromCatalog(t *testing.T) {
	catalog := &langdag.ModelCatalog{
		Providers: map[string][]langdag.ModelPricing{
			"anthropic": {
				{ID: "claude-opus-4-6", InputPricePer1M: 5, OutputPricePer1M: 25, ContextWindow: 200000},
			},
			"grok": {
				{ID: "grok-4-1-fast-reasoning", InputPricePer1M: 3, OutputPricePer1M: 15, ContextWindow: 131072},
			},
		},
	}
	models := modelsFromCatalog(catalog)
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	// anthropic comes first in supportedProviders order
	if models[0].ID != "claude-opus-4-6" || models[0].Provider != "anthropic" {
		t.Errorf("first model: got %s/%s, want claude-opus-4-6/anthropic", models[0].ID, models[0].Provider)
	}
	if models[0].PromptPrice != 5 || models[0].CompletionPrice != 25 {
		t.Errorf("pricing: got %f/%f, want 5/25", models[0].PromptPrice, models[0].CompletionPrice)
	}
	if models[0].ContextWindow != 200000 {
		t.Errorf("context: got %d, want 200000", models[0].ContextWindow)
	}
}

func TestModelsFromCatalogServerTools(t *testing.T) {
	catalog := &langdag.ModelCatalog{
		Providers: map[string][]langdag.ModelPricing{
			"anthropic": {
				{ID: "claude-sonnet-4", InputPricePer1M: 3, OutputPricePer1M: 15, ContextWindow: 200000, ServerTools: []string{"web_search"}},
			},
			"grok": {
				{ID: "grok-3", InputPricePer1M: 3, OutputPricePer1M: 15, ContextWindow: 131072},
			},
		},
	}
	models := modelsFromCatalog(catalog)
	// anthropic model should have server tools
	if m := findModelByID(models, "claude-sonnet-4"); m == nil {
		t.Fatal("claude-sonnet-4 not found")
	} else if len(m.ServerTools) != 1 || m.ServerTools[0] != "web_search" {
		t.Errorf("claude-sonnet-4 ServerTools = %v, want [web_search]", m.ServerTools)
	}
	// grok model should have no server tools
	if m := findModelByID(models, "grok-3"); m == nil {
		t.Fatal("grok-3 not found")
	} else if len(m.ServerTools) != 0 {
		t.Errorf("grok-3 ServerTools = %v, want []", m.ServerTools)
	}
}

func TestModelsFromCatalogNil(t *testing.T) {
	models := modelsFromCatalog(nil)
	if models != nil {
		t.Errorf("expected nil for nil catalog, got %d models", len(models))
	}
}

func TestModelsFromCatalogSkipsUnknownProviders(t *testing.T) {
	catalog := &langdag.ModelCatalog{
		Providers: map[string][]langdag.ModelPricing{
			"anthropic":        {{ID: "claude-opus-4-6", InputPricePer1M: 5, OutputPricePer1M: 25, ContextWindow: 200000}},
			"unknown-provider": {{ID: "mystery-model", InputPricePer1M: 1, OutputPricePer1M: 2, ContextWindow: 100000}},
		},
	}
	models := modelsFromCatalog(catalog)
	for _, m := range models {
		if m.Provider == "unknown-provider" {
			t.Errorf("should not include models from unknown providers")
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
		{Provider: "openai", ID: "gpt-4o", PromptPrice: 2.5, ContextWindow: 128000},
		{Provider: "anthropic", ID: "claude-opus", PromptPrice: 15.0, ContextWindow: 200000},
		{Provider: "gemini", ID: "gemini-pro", PromptPrice: 1.25, ContextWindow: 1000000},
	}
}

func TestSortModelsByColNameAsc(t *testing.T) {
	m := sortTestModels()
	sortModelsByCol(m, 0, true)
	want := []string{"claude-opus", "gemini-pro", "gpt-4o"}
	for i, w := range want {
		if m[i].ID != w {
			t.Errorf("index %d: got %s, want %s", i, m[i].ID, w)
		}
	}
}

func TestSortModelsByColNameDesc(t *testing.T) {
	m := sortTestModels()
	sortModelsByCol(m, 0, false)
	want := []string{"gpt-4o", "gemini-pro", "claude-opus"}
	for i, w := range want {
		if m[i].ID != w {
			t.Errorf("index %d: got %s, want %s", i, m[i].ID, w)
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


// --- formatTokenCount tests ---

func TestFormatTokenCountSmall(t *testing.T) {
	if got := formatTokenCount(500); got != "500" {
		t.Errorf("formatTokenCount(500) = %q, want 500", got)
	}
}

func TestFormatTokenCountThousands(t *testing.T) {
	if got := formatTokenCount(9999); got != "9999" {
		t.Errorf("formatTokenCount(9999) = %q, want 9999", got)
	}
}

func TestFormatTokenCountTenK(t *testing.T) {
	if got := formatTokenCount(15000); got != "15k" {
		t.Errorf("formatTokenCount(15000) = %q, want 15k", got)
	}
}

func TestFormatTokenCountHundredK(t *testing.T) {
	if got := formatTokenCount(150000); got != "150k" {
		t.Errorf("formatTokenCount(150000) = %q, want 150k", got)
	}
}

func TestFormatTokenCountMillions(t *testing.T) {
	if got := formatTokenCount(1500000); got != "1.5m" {
		t.Errorf("formatTokenCount(1500000) = %q, want 1.5m", got)
	}
}

func TestFormatTokenCountExactMillion(t *testing.T) {
	if got := formatTokenCount(2000000); got != "2m" {
		t.Errorf("formatTokenCount(2000000) = %q, want 2m", got)
	}
}

// --- formatBytes tests ---

func TestFormatBytesSmall(t *testing.T) {
	if got := formatBytes(500); got != "500B" {
		t.Errorf("formatBytes(500) = %q, want 500B", got)
	}
}

func TestFormatBytesKilobytes(t *testing.T) {
	if got := formatBytes(15000); got != "15KB" {
		t.Errorf("formatBytes(15000) = %q, want 15KB", got)
	}
}

func TestFormatBytesMegabytes(t *testing.T) {
	if got := formatBytes(1500000); got != "1.5MB" {
		t.Errorf("formatBytes(1500000) = %q, want 1.5MB", got)
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

func TestFormatCostVerySmall(t *testing.T) {
	got := formatCost(0.0001)
	if got != "$0.00010" {
		t.Errorf("formatCost(0.0001) = %q, want $0.00010", got)
	}
}

func TestFormatCostTiny(t *testing.T) {
	got := formatCost(0.00001)
	if got != "$0.000010" {
		t.Errorf("formatCost(0.00001) = %q, want $0.000010", got)
	}
}

// --- supportsServerTools tests ---

func TestSupportsServerToolsWithCapability(t *testing.T) {
	models := []ModelDef{
		{ID: "claude-sonnet-4-6", Provider: ProviderAnthropic, ServerTools: []string{"web_search"}},
		{ID: "gpt-4o", Provider: ProviderOpenAI, ServerTools: []string{"web_search"}},
		{ID: "grok-4-1-fast-reasoning", Provider: ProviderGrok, ServerTools: []string{"web_search"}},
	}
	for _, m := range models {
		if !supportsServerTools(models, m.ID) {
			t.Errorf("%s should support server tools", m.ID)
		}
	}
}

func TestSupportsServerToolsWithoutCapability(t *testing.T) {
	models := []ModelDef{
		{ID: "grok-3", Provider: ProviderGrok},
		{ID: "grok-code-fast-1", Provider: ProviderGrok},
	}
	for _, m := range models {
		if supportsServerTools(models, m.ID) {
			t.Errorf("%s should not support server tools", m.ID)
		}
	}
}

func TestSupportsServerToolsUnknownModel(t *testing.T) {
	models := []ModelDef{
		{ID: "claude-sonnet-4-6", Provider: ProviderAnthropic, ServerTools: []string{"web_search"}},
	}
	if supportsServerTools(models, "nonexistent-model") {
		t.Error("unknown model should not support server tools")
	}
}

