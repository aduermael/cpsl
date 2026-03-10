package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"langdag.com/langdag"
	"langdag.com/langdag/types"
)

//go:embed models.json
var modelsJSON []byte

// Provider constants for supported AI providers.
const (
	ProviderAnthropic = "anthropic"
	ProviderGrok      = "grok"
	ProviderOpenAI    = "openai"
	ProviderGemini    = "gemini"
)

// ModelDef describes a model available for selection.
// IDs are native API model identifiers (not OpenRouter format).
// models.json provides provider, id, and display_name; pricing and context
// window are populated at runtime from the langdag model catalog.
type ModelDef struct {
	Provider        string  `json:"provider"`
	ID              string  `json:"id"`
	DisplayName     string  `json:"display_name"`
	PromptPrice     float64 `json:"-"` // USD per million input tokens (from catalog)
	CompletionPrice float64 `json:"-"` // USD per million output tokens (from catalog)
	ContextWindow   int     `json:"-"` // tokens (from catalog)
	SWEScore        float64 `json:"-"` // SWE-bench Verified score (0 = no data)
}

// builtinModels returns the list of supported models loaded from the embedded models.json.
func builtinModels() []ModelDef {
	var models []ModelDef
	if err := json.Unmarshal(modelsJSON, &models); err != nil {
		panic(fmt.Sprintf("failed to parse embedded models.json: %v", err))
	}
	return models
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
// Falls back to longest-prefix match (e.g. "grok-4-0709" matches "grok-4")
// when no exact match exists, which handles API responses that return
// versioned model IDs.
func findModelByID(models []ModelDef, id string) *ModelDef {
	for i := range models {
		if models[i].ID == id {
			return &models[i]
		}
	}
	// Fallback: longest prefix match
	bestIdx := -1
	bestLen := 0
	for i := range models {
		mid := models[i].ID
		if strings.HasPrefix(id, mid+"-") && len(mid) > bestLen {
			bestIdx = i
			bestLen = len(mid)
		}
	}
	if bestIdx >= 0 {
		return &models[bestIdx]
	}
	return nil
}

// sortModelsByCol sorts models in place by the given column.
// col: 0=Name, 1=Provider, 2=Price(prompt), 3=ContextWindow.
func sortModelsByCol(models []ModelDef, col int, asc bool) {
	sort.SliceStable(models, func(i, j int) bool {
		var less bool
		switch col {
		case 0:
			less = strings.ToLower(models[i].DisplayName) < strings.ToLower(models[j].DisplayName)
		case 1:
			less = strings.ToLower(models[i].Provider) < strings.ToLower(models[j].Provider)
		case 2:
			less = models[i].PromptPrice < models[j].PromptPrice
		case 3:
			less = models[i].ContextWindow < models[j].ContextWindow
		default:
			less = strings.ToLower(models[i].DisplayName) < strings.ToLower(models[j].DisplayName)
		}
		if !asc {
			return !less
		}
		return less
	})
}

// sortColNames maps column indices to config-friendly names.
var sortColNames = [4]string{"name", "provider", "price", "context"}

// sortColFromName returns the column index for a name, defaulting to 0.
func sortColFromName(name string) int {
	for i, n := range sortColNames {
		if n == name {
			return i
		}
	}
	return 0
}

// sortAscFromMap converts a config map (column name → ascending) to a [4]bool array.
// Missing columns default to ascending (true).
func sortAscFromMap(m map[string]bool) [4]bool {
	var result [4]bool
	for i, name := range sortColNames {
		if asc, ok := m[name]; ok {
			result[i] = asc
		} else {
			result[i] = true
		}
	}
	return result
}

// sortAscToMap converts a [4]bool array to a config map (column name → ascending).
func sortAscToMap(arr [4]bool) map[string]bool {
	m := make(map[string]bool, 4)
	for i, name := range sortColNames {
		m[name] = arr[i]
	}
	return m
}

// formatPrice formats a per-million-token price as "$X.XX".
func formatPrice(price float64) string {
	return fmt.Sprintf("$%.2f", price)
}

// formatPriceCompact formats a price dropping unnecessary trailing zeros.
// 5.0 → "$5", 0.15 → "$0.15", 0.80 → "$0.80", 15.0 → "$15".
func formatPriceCompact(price float64) string {
	if price == float64(int(price)) {
		return fmt.Sprintf("$%d", int(price))
	}
	return fmt.Sprintf("$%.2f", price)
}

// formatPricePerM formats input/output prices per million tokens as "$X/$Y/M".
func formatPricePerM(promptPrice, completionPrice float64) string {
	return formatPriceCompact(promptPrice) + "/" + formatPriceCompact(completionPrice) + "/M"
}

// formatContextWindow formats a token count for display.
// Examples: 128000 → "128k", 200000 → "200k", 1048576 → "1.0m".
func formatContextWindow(tokens int) string {
	if tokens >= 1000000 {
		v := float64(tokens) / 1000000.0
		if v == float64(int(v)) {
			return fmt.Sprintf("%dm", int(v))
		}
		return fmt.Sprintf("%.1fm", v)
	}
	return fmt.Sprintf("%dk", tokens/1000)
}

// formatModelMenuLines formats models as aligned multi-column menu lines.
// Columns: Name, Provider, Price (prompt), Context Window.
// Returns a header string and the data lines.
// The active model is marked with ● at the end.
// sortCol (0-3) determines which column header is highlighted.
func formatModelMenuLines(models []ModelDef, activeID string, sortCol int, sortAsc bool) (string, []string) {
	// Column headers
	headers := [4]string{"Name", "Provider", "Price", "Context"}

	// Compute column widths (at least as wide as headers)
	maxName := len(headers[0])
	maxProv := len(headers[1])
	maxPrice := len(headers[2])
	maxCtx := len(headers[3])

	type entry struct {
		name, prov, price, ctx string
		active                 bool
	}
	entries := make([]entry, len(models))
	for i, m := range models {
		e := entry{
			name:   m.DisplayName,
			prov:   m.Provider,
			price:  formatPricePerM(m.PromptPrice, m.CompletionPrice),
			ctx:    formatContextWindow(m.ContextWindow),
			active: m.ID == activeID,
		}
		if len(e.name) > maxName {
			maxName = len(e.name)
		}
		if len(e.prov) > maxProv {
			maxProv = len(e.prov)
		}
		if len(e.price) > maxPrice {
			maxPrice = len(e.price)
		}
		if len(e.ctx) > maxCtx {
			maxCtx = len(e.ctx)
		}
		entries[i] = e
	}

	// Build header with sort indicator on active column
	// ▼ = list reads downward (A→Z / low→high), ▲ = list reads upward (Z→A / high→low)
	arrow := "▼"
	if !sortAsc {
		arrow = "▲"
	}
	hdrParts := make([]string, 4)
	widths := [4]int{maxName, maxProv, maxPrice, maxCtx}
	rightAlign := [4]bool{false, false, true, true}
	for j, h := range headers {
		label := h
		if j == sortCol {
			label = h + arrow
		}
		if rightAlign[j] {
			hdrParts[j] = fmt.Sprintf("%*s", widths[j], label)
		} else {
			hdrParts[j] = fmt.Sprintf("%-*s", widths[j], label)
		}
	}
	header := hdrParts[0] + "  " + hdrParts[1] + "  " + hdrParts[2] + "  " + hdrParts[3]

	lines := make([]string, len(entries))
	for i, e := range entries {
		marker := " "
		if e.active {
			marker = "●"
		}
		lines[i] = fmt.Sprintf("%-*s  %-*s  %*s  %*s %s",
			maxName, e.name,
			maxProv, e.prov,
			maxPrice, e.price,
			maxCtx, e.ctx,
			marker)
	}
	return header, lines
}

// catalogCachePath returns the path to the langdag model catalog cache file.
func catalogCachePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".cpsl", "model_catalog.json")
}

// enrichModelsFromCatalog updates model pricing and context window from the
// langdag catalog, falling back to models.json values when a model isn't found.
func enrichModelsFromCatalog(models []ModelDef, catalog *langdag.ModelCatalog) {
	if catalog == nil {
		return
	}
	for i := range models {
		pricing, _, found := catalog.LookupModel(models[i].ID)
		if !found {
			continue
		}
		if pricing.InputPricePer1M > 0 {
			models[i].PromptPrice = pricing.InputPricePer1M
		}
		if pricing.OutputPricePer1M > 0 {
			models[i].CompletionPrice = pricing.OutputPricePer1M
		}
		if pricing.ContextWindow > 0 {
			models[i].ContextWindow = pricing.ContextWindow
		}
	}
}

// computeCost calculates the USD cost for a single LLM call based on token
// usage and model pricing. Prices are per million tokens. For Anthropic models,
// cache read tokens are charged at 10% of the input price.
// Returns 0 if the model is not found.
func computeCost(models []ModelDef, modelID string, usage types.Usage) float64 {
	m := findModelByID(models, modelID)
	if m == nil || (m.PromptPrice == 0 && m.CompletionPrice == 0) {
		return 0
	}
	inputCost := float64(usage.InputTokens) * m.PromptPrice / 1_000_000
	outputCost := float64(usage.OutputTokens) * m.CompletionPrice / 1_000_000

	// Anthropic cache read tokens are 10% of input price
	if usage.CacheReadInputTokens > 0 && m.Provider == ProviderAnthropic {
		inputCost += float64(usage.CacheReadInputTokens) * m.PromptPrice * 0.1 / 1_000_000
	}

	return inputCost + outputCost
}

// formatCost formats a USD cost for display with enough precision to show
// at least one significant digit. Very small amounts get more decimal places.
func formatCost(cost float64) string {
	switch {
	case cost >= 0.01:
		return fmt.Sprintf("$%.2f", cost)
	case cost >= 0.001:
		return fmt.Sprintf("$%.4f", cost)
	case cost >= 0.0001:
		return fmt.Sprintf("$%.5f", cost)
	default:
		return fmt.Sprintf("$%.6f", cost)
	}
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
// model IDs against SWE-bench model tags.
func matchSWEScores(models []ModelDef, scores map[string]float64) {
	for i := range models {
		id := models[i].ID
		// Try exact match first, then check if either contains the other
		for tag, score := range scores {
			if tag == id || strings.Contains(tag, id) || strings.Contains(id, tag) {
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
