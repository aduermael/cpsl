// models.go defines model definitions, catalog lookups, sorting, filtering,
// and formatting helpers for the AI provider model selection UI.
package main

import (
	"bytes"
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

// Provider constants for supported AI providers.
const (
	ProviderAnthropic = "anthropic"
	ProviderGrok      = "grok"
	ProviderOpenAI    = "openai"
	ProviderGemini    = "gemini"
	ProviderOllama    = "ollama"
)

// supportedProviders lists providers in display order.
var supportedProviders = []string{ProviderAnthropic, ProviderGrok, ProviderOpenAI, ProviderGemini, ProviderOllama}

// ModelDef describes a model available for selection.
// Models are derived from the langdag model catalog at runtime.
type ModelDef struct {
	Provider        string
	ID              string
	Label           string   // optional display name override (e.g. "model (offline)")
	PromptPrice     float64  // USD per million input tokens
	CompletionPrice float64  // USD per million output tokens
	ContextWindow   int      // tokens
	SWEScore        float64  // SWE-bench Verified score (0 = no data)
	ServerTools     []string // server-side tools supported by this model (e.g. "web_search")
}

// modelsFromCatalog builds the model list from the langdag catalog.
// Only models from supported providers are included.
func modelsFromCatalog(catalog *langdag.ModelCatalog) []ModelDef {
	if catalog == nil {
		return nil
	}
	var models []ModelDef
	for _, provider := range supportedProviders {
		// Ollama models are fetched separately via fetchOllamaModels
		if provider == ProviderOllama {
			continue
		}
		for _, p := range catalog.ForProvider(provider) {
			models = append(models, ModelDef{
				Provider:        provider,
				ID:              p.ID,
				PromptPrice:     p.InputPricePer1M,
				CompletionPrice: p.OutputPricePer1M,
				ContextWindow:   p.ContextWindow,
				ServerTools:     p.ServerTools,
			})
		}
	}
	return models
}

// supportsServerToolsOptions is the parameter bundle for supportsServerTools.
type supportsServerToolsOptions struct {
	provider string
	modelID  string
	models   []ModelDef
}

// supportsServerTools reports whether a model supports server-side tools
// (e.g. web search). Uses catalog metadata when available; falls back to
// provider-level heuristics for models not in the catalog (e.g. Ollama).
func supportsServerTools(opts supportsServerToolsOptions) bool {
	// Check catalog metadata first.
	if m := findModelByID(findModelByIDOptions{models: opts.models, id: opts.modelID}); m != nil {
		for _, st := range m.ServerTools {
			if st == "web_search" {
				return true
			}
		}
		// Model found in catalog but no web_search — not supported.
		return false
	}
	// Model not in catalog (e.g. Ollama local models) — no server tools.
	return false
}

// fetchOllamaModels fetches available models from an Ollama instance.
// Returns nil if the Ollama server is unreachable or no baseURL is configured.
func fetchOllamaModels(baseURL string) []ModelDef {
	if baseURL == "" {
		return nil
	}

	client := &http.Client{Timeout: 5 * time.Second}
	base := strings.TrimRight(baseURL, "/")

	resp, err := client.Get(base + "/api/tags")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var tagsResp struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tagsResp); err != nil {
		return nil
	}

	type result struct {
		idx   int
		model ModelDef
	}
	ch := make(chan result, len(tagsResp.Models))
	for i, m := range tagsResp.Models {
		i, m := i, m
		go func() {
			ch <- result{i, ModelDef{
				Provider:        ProviderOllama,
				ID:              m.Name,
				PromptPrice:     0,
				CompletionPrice: 0,
				ContextWindow:   ollamaContextWindow(ollamaContextWindowOptions{client: client, baseURL: base, modelName: m.Name}),
			}}
		}()
	}
	models := make([]ModelDef, len(tagsResp.Models))
	for range tagsResp.Models {
		r := <-ch
		models[r.idx] = r.model
	}
	return models
}

// ollamaContextWindowOptions is the parameter bundle for ollamaContextWindow.
type ollamaContextWindowOptions struct {
	client    *http.Client
	baseURL   string
	modelName string
}

// ollamaContextWindow queries /api/show for the model's actual context length.
// Returns 0 if the server doesn't provide it.
func ollamaContextWindow(opts ollamaContextWindowOptions) int {
	client, baseURL, modelName := opts.client, opts.baseURL, opts.modelName
	body, _ := json.Marshal(map[string]string{"model": modelName})
	resp, err := client.Post(baseURL+"/api/show", "application/json", bytes.NewReader(body))
	if err != nil || resp.StatusCode != http.StatusOK {
		return 0
	}
	defer resp.Body.Close()

	// model_info contains keys like "llama.context_length", "gemma3.context_length", etc.
	var showResp struct {
		ModelInfo map[string]json.RawMessage `json:"model_info"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&showResp); err != nil {
		return 0
	}
	for key, val := range showResp.ModelInfo {
		if strings.HasSuffix(key, ".context_length") {
			var n int
			if err := json.Unmarshal(val, &n); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

// filterModelsByProvidersOptions is the parameter bundle for filterModelsByProviders.
type filterModelsByProvidersOptions struct {
	models    []ModelDef
	providers map[string]bool
}

// filterModelsByProviders returns models whose provider is in the given set.
func filterModelsByProviders(opts filterModelsByProvidersOptions) []ModelDef {
	var result []ModelDef
	for _, m := range opts.models {
		if opts.providers[m.Provider] {
			result = append(result, m)
		}
	}
	return result
}

// findModelByIDOptions is the parameter bundle for findModelByID.
type findModelByIDOptions struct {
	models []ModelDef
	id     string
}

// findModelByID returns the model with the given ID, or nil if not found.
func findModelByID(opts findModelByIDOptions) *ModelDef {
	for i := range opts.models {
		if opts.models[i].ID == opts.id {
			return &opts.models[i]
		}
	}
	return nil
}

// sortModelsByColOptions is the parameter bundle for sortModelsByCol.
type sortModelsByColOptions struct {
	models []ModelDef
	col    int
	asc    bool
}

// sortModelsByCol sorts models in place by the given column.
// col: 0=Model(ID), 1=Provider, 2=Price(prompt), 3=ContextWindow.
func sortModelsByCol(opts sortModelsByColOptions) {
	models, col, asc := opts.models, opts.col, opts.asc
	sort.SliceStable(models, func(i, j int) bool {
		var less bool
		switch col {
		case 0:
			less = strings.ToLower(models[i].ID) < strings.ToLower(models[j].ID)
		case 1:
			less = strings.ToLower(models[i].Provider) < strings.ToLower(models[j].Provider)
		case 2:
			less = models[i].PromptPrice < models[j].PromptPrice
		case 3:
			less = models[i].ContextWindow < models[j].ContextWindow
		default:
			less = strings.ToLower(models[i].ID) < strings.ToLower(models[j].ID)
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

// formatPricePerMOptions is the parameter bundle for formatPricePerM.
type formatPricePerMOptions struct {
	promptPrice     float64
	completionPrice float64
}

// formatPricePerM formats input/output prices per million tokens as "$X/$Y/M".
func formatPricePerM(opts formatPricePerMOptions) string {
	return formatPriceCompact(opts.promptPrice) + "/" + formatPriceCompact(opts.completionPrice) + "/M"
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

// formatModelMenuLinesOptions is the parameter bundle for formatModelMenuLines.
type formatModelMenuLinesOptions struct {
	models   []ModelDef
	activeID string
	sortCol  int
	sortAsc  bool
}

// formatModelMenuLines formats models as aligned multi-column menu lines.
// Columns: Model (ID), Provider, Price (prompt), Context Window.
// Returns a header string and the data lines.
// The active model is marked with ● at the end.
// sortCol (0-3) determines which column header is highlighted.
func formatModelMenuLines(opts formatModelMenuLinesOptions) (string, []string) {
	models, activeID, sortCol, sortAsc := opts.models, opts.activeID, opts.sortCol, opts.sortAsc
	// Column headers
	headers := [4]string{"Model", "Provider", "Price", "Context"}

	// Compute column widths (at least as wide as headers)
	maxName := visibleWidth(headers[0])
	maxProv := visibleWidth(headers[1])
	maxPrice := visibleWidth(headers[2])
	maxCtx := visibleWidth(headers[3])

	type entry struct {
		name, prov, price, ctx string
		active                 bool
	}
	entries := make([]entry, len(models))
	for i, m := range models {
		displayName := m.ID
		if m.Label != "" {
			displayName = m.Label
		}
		e := entry{
			name:   displayName,
			prov:   m.Provider,
			price:  formatPricePerM(formatPricePerMOptions{promptPrice: m.PromptPrice, completionPrice: m.CompletionPrice}),
			ctx:    formatContextWindow(m.ContextWindow),
			active: m.ID == activeID,
		}
		if visibleWidth(e.name) > maxName {
			maxName = visibleWidth(e.name)
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
		pad := widths[j] - visibleWidth(label)
		if pad < 0 {
			pad = 0
		}
		if rightAlign[j] {
			hdrParts[j] = strings.Repeat(" ", pad) + label
		} else {
			hdrParts[j] = label + strings.Repeat(" ", pad)
		}
	}
	header := hdrParts[0] + "  " + hdrParts[1] + "  " + hdrParts[2] + "  " + hdrParts[3]

	lines := make([]string, len(entries))
	for i, e := range entries {
		marker := " "
		if e.active {
			marker = "●"
		}
		// Pad name manually to account for invisible ANSI escape bytes.
		namePad := maxName - visibleWidth(e.name)
		if namePad < 0 {
			namePad = 0
		}
		// ● is 3 bytes but 1 visible char; adjust ctx width so right-align stays correct.
		ctxWidth := maxCtx
		if e.active {
			ctxWidth -= 2 // compensate for 2 extra bytes in ●
		}
		lines[i] = fmt.Sprintf("%s%s  %-*s  %*s  %*s %s",
			e.name,
			strings.Repeat(" ", namePad),
			maxProv, e.prov,
			maxPrice, e.price,
			ctxWidth, e.ctx,
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
	return filepath.Join(home, ".herm", "model_catalog.json")
}

// computeCostOptions is the parameter bundle for computeCost.
type computeCostOptions struct {
	models  []ModelDef
	modelID string
	usage   types.Usage
}

// computeCost calculates the USD cost for a single LLM call based on token
// usage and model pricing. Prices are per million tokens. For Anthropic models,
// cache read tokens are charged at 10% of the input price.
// Returns 0 if the model is not found.
func computeCost(opts computeCostOptions) float64 {
	m := findModelByID(findModelByIDOptions{models: opts.models, id: opts.modelID})
	if m == nil || (m.PromptPrice == 0 && m.CompletionPrice == 0) {
		return 0
	}
	usage := opts.usage
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

// formatTokenCount formats a token count for compact display.
// Examples: 1234 → "1,234", 150000 → "150k", 1500000 → "1.5m".
func formatTokenCount(tokens int) string {
	switch {
	case tokens >= 1_000_000:
		v := float64(tokens) / 1_000_000
		if v == float64(int(v)) {
			return fmt.Sprintf("%dm", int(v))
		}
		return fmt.Sprintf("%.1fm", v)
	case tokens >= 10_000:
		return fmt.Sprintf("%dk", tokens/1000)
	default:
		return fmt.Sprintf("%d", tokens)
	}
}

// formatBytes formats a byte count for compact display.
// Examples: 500 → "500B", 15360 → "15KB", 1572864 → "1.5MB".
func formatBytes(bytes int) string {
	switch {
	case bytes >= 1_000_000:
		return fmt.Sprintf("%.1fMB", float64(bytes)/1_000_000)
	case bytes >= 1_000:
		return fmt.Sprintf("%dKB", bytes/1000)
	default:
		return fmt.Sprintf("%dB", bytes)
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

// matchSWEScoresOptions is the parameter bundle for matchSWEScores.
type matchSWEScoresOptions struct {
	models []ModelDef
	scores map[string]float64
}

// matchSWEScores enriches models with SWE-bench scores by fuzzy-matching
// model IDs against SWE-bench model tags.
func matchSWEScores(opts matchSWEScoresOptions) {
	models, scores := opts.models, opts.scores
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
