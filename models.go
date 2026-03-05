package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Provider constants for supported AI providers.
const (
	ProviderAnthropic = "anthropic"
	ProviderGrok      = "grok"
	ProviderOpenAI    = "openai"
	ProviderGemini    = "gemini"
)

// providerPrefixes maps OpenRouter ID prefixes to internal provider names.
var providerPrefixes = map[string]string{
	"anthropic/": ProviderAnthropic,
	"openai/":    ProviderOpenAI,
	"x-ai/":     ProviderGrok,
}

// ModelDef describes a model available for selection.
type ModelDef struct {
	Provider        string
	ID              string
	DisplayName     string
	PromptPrice     float64 // USD per million input tokens
	CompletionPrice float64 // USD per million output tokens
	SWEScore        float64 // SWE-bench Verified score (0 = no data)
}

// filterModelsByProviders returns models whose provider is in the given set.
func filterModelsByProviders(models []ModelDef, providers map[string]bool) []ModelDef {
	var result []ModelDef
	for _, m := range models {
		if providers[m.Provider] {
			result = append(result, m)
		}
	}
	return result
}

// findModelByID returns the model with the given ID, or nil if not found.
func findModelByID(models []ModelDef, id string) *ModelDef {
	for i := range models {
		if models[i].ID == id {
			return &models[i]
		}
	}
	return nil
}

// OpenRouter API types

type openRouterResponse struct {
	Data []openRouterModel `json:"data"`
}

type openRouterModel struct {
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Pricing openRouterPricing  `json:"pricing"`
}

type openRouterPricing struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
}

// formatPrice formats a per-million-token price as "$X.XX".
func formatPrice(price float64) string {
	return fmt.Sprintf("$%.2f", price)
}

// parsePrice converts an OpenRouter price string (USD per token) to USD per million tokens.
func parsePrice(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v * 1_000_000
}

// knownProviderNames lists display-name prefixes that OpenRouter prepends to
// model names. These are stripped by cleanDisplayName to avoid duplicating the
// PROVIDER column.
var knownProviderNames = []string{
	"Anthropic",
	"OpenAI",
	"xAI",
	"X AI",
}

// cleanDisplayName strips a leading provider prefix (e.g. "Anthropic: ") from
// an OpenRouter display name so it doesn't duplicate the PROVIDER column.
func cleanDisplayName(name, provider string) string {
	for _, prefix := range knownProviderNames {
		// Match "Provider: rest" (colon-space separator)
		if strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)+": ") {
			return name[len(prefix)+2:]
		}
	}
	return name
}

// parseOpenRouterModels converts raw OpenRouter API models into ModelDefs,
// filtering to only supported providers.
func parseOpenRouterModels(data []openRouterModel) []ModelDef {
	var result []ModelDef
	for _, m := range data {
		var provider string
		for prefix, prov := range providerPrefixes {
			if strings.HasPrefix(m.ID, prefix) {
				provider = prov
				break
			}
		}
		if provider == "" {
			continue
		}
		result = append(result, ModelDef{
			Provider:        provider,
			ID:              m.ID,
			DisplayName:     cleanDisplayName(m.Name, provider),
			PromptPrice:     parsePrice(m.Pricing.Prompt),
			CompletionPrice: parsePrice(m.Pricing.Completion),
		})
	}
	return result
}

// fetchModels fetches the model list from the OpenRouter API.
func fetchModels() ([]ModelDef, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("https://openrouter.ai/api/v1/models")
	if err != nil {
		return nil, fmt.Errorf("fetching models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenRouter API returned status %d", resp.StatusCode)
	}

	var body openRouterResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decoding models response: %w", err)
	}

	models := parseOpenRouterModels(body.Data)
	if len(models) == 0 {
		return nil, fmt.Errorf("no supported models found")
	}
	return models, nil
}

// SWE-bench leaderboard types

const sweBenchURL = "https://raw.githubusercontent.com/SWE-bench/swe-bench.github.io/master/data/leaderboards.json"

type sweBenchResponse struct {
	Leaderboards []sweBenchLeaderboard `json:"leaderboards"`
}

type sweBenchLeaderboard struct {
	Name    string           `json:"name"`
	Results []sweBenchResult `json:"results"`
}

type sweBenchResult struct {
	Name     string   `json:"name"`
	Resolved float64  `json:"resolved"`
	Tags     []string `json:"tags"`
}

// parseSWEScores extracts the highest SWE-bench Verified score per model tag
// from leaderboard results. Returns a map from model tag identifier (e.g.
// "claude-opus-4-5-20251101") to the best resolved score.
func parseSWEScores(resp sweBenchResponse) map[string]float64 {
	scores := make(map[string]float64)
	for _, lb := range resp.Leaderboards {
		if lb.Name != "Verified" {
			continue
		}
		for _, r := range lb.Results {
			var modelTags []string
			for _, tag := range r.Tags {
				if strings.HasPrefix(tag, "Model: ") {
					modelTags = append(modelTags, strings.TrimPrefix(tag, "Model: "))
				}
			}
			// Skip entries with multiple model tags (multi-model systems)
			if len(modelTags) != 1 {
				continue
			}
			tag := modelTags[0]
			if r.Resolved > scores[tag] {
				scores[tag] = r.Resolved
			}
		}
		break // only process "Verified"
	}
	return scores
}

// matchSWEScores enriches models with SWE-bench scores by fuzzy-matching
// OpenRouter model IDs against SWE-bench model tags.
func matchSWEScores(models []ModelDef, scores map[string]float64) {
	for i := range models {
		// Extract suffix after provider prefix (e.g. "anthropic/" → "claude-opus-4-5-20251101")
		suffix := models[i].ID
		for prefix := range providerPrefixes {
			if strings.HasPrefix(models[i].ID, prefix) {
				suffix = strings.TrimPrefix(models[i].ID, prefix)
				break
			}
		}
		// Try exact match on suffix first, then check if either contains the other
		for tag, score := range scores {
			if tag == suffix || strings.Contains(tag, suffix) || strings.Contains(suffix, tag) {
				if score > models[i].SWEScore {
					models[i].SWEScore = score
				}
			}
		}
	}
}

// fetchSWEScores fetches the SWE-bench Verified leaderboard and returns
// a map of model tag identifiers to their best scores.
func fetchSWEScores() (map[string]float64, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(sweBenchURL)
	if err != nil {
		return nil, fmt.Errorf("fetching SWE-bench scores: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SWE-bench API returned status %d", resp.StatusCode)
	}

	var body sweBenchResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decoding SWE-bench response: %w", err)
	}

	return parseSWEScores(body), nil
}
