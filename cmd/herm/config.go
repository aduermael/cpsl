package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const configDir = ".herm"
const configFile = "config.json"

type Config struct {
	PasteCollapseMinChars int             `json:"paste_collapse_min_chars"`
	AnthropicAPIKey       string          `json:"anthropic_api_key,omitempty"`
	GrokAPIKey            string          `json:"grok_api_key,omitempty"`
	OpenAIAPIKey          string          `json:"openai_api_key,omitempty"`
	GeminiAPIKey          string          `json:"gemini_api_key,omitempty"`
	ActiveModel           string          `json:"active_model,omitempty"`
	ExplorationModel      string          `json:"exploration_model,omitempty"` // model for sub-agents; falls back to ActiveModel
	ModelSortCol          string          `json:"model_sort_col,omitempty"`   // "name","provider","price","context"
	ModelSortDirs         map[string]bool `json:"model_sort_dirs,omitempty"` // column name → ascending (per-column)
	DisplaySystemPrompts  bool            `json:"display_system_prompts,omitempty"`
	SubAgentMaxTurns      int             `json:"sub_agent_max_turns,omitempty"`
	Personality           string          `json:"personality,omitempty"` // optional agent personality/tone
	HistoryMaxEntries     int             `json:"history_max_entries,omitempty"`
}

func (c Config) effectiveMaxHistory() int {
	if c.HistoryMaxEntries > 0 {
		return c.HistoryMaxEntries
	}
	return 100
}

// configuredProviders returns a set of provider names that have API keys configured.
func (c Config) configuredProviders() map[string]bool {
	providers := make(map[string]bool)
	if c.AnthropicAPIKey != "" {
		providers[ProviderAnthropic] = true
	}
	if c.GrokAPIKey != "" {
		providers[ProviderGrok] = true
	}
	if c.OpenAIAPIKey != "" {
		providers[ProviderOpenAI] = true
	}
	if c.GeminiAPIKey != "" {
		providers[ProviderGemini] = true
	}
	return providers
}

// defaultLangdagProvider returns the provider that newLangdagClient will use.
func (c Config) defaultLangdagProvider() string {
	if c.AnthropicAPIKey != "" {
		return ProviderAnthropic
	}
	if c.OpenAIAPIKey != "" {
		return ProviderOpenAI
	}
	if c.GrokAPIKey != "" {
		return ProviderGrok
	}
	if c.GeminiAPIKey != "" {
		return ProviderGemini
	}
	return ""
}

// availableModels returns the models whose provider has a configured API key.
func (c Config) availableModels(models []ModelDef) []ModelDef {
	return filterModelsByProviders(models, c.configuredProviders())
}

// resolveActiveModel returns a valid active model ID. If the current ActiveModel
// is invalid or its provider has no key, it falls back to the first available
// model, or empty string if no keys are configured.
func (c Config) resolveActiveModel(models []ModelDef) string {
	available := c.availableModels(models)
	if len(available) == 0 {
		return ""
	}
	// Check if current active model is in the available list
	for _, m := range available {
		if m.ID == c.ActiveModel {
			return c.ActiveModel
		}
	}
	// Fall back to first available
	return available[0].ID
}

// resolveExplorationModel returns the model ID for sub-agents/exploration.
// Falls back to resolveActiveModel if ExplorationModel is unset or invalid.
func (c Config) resolveExplorationModel(models []ModelDef) string {
	if c.ExplorationModel == "" {
		return c.resolveActiveModel(models)
	}
	available := c.availableModels(models)
	for _, m := range available {
		if m.ID == c.ExplorationModel {
			return c.ExplorationModel
		}
	}
	// Configured but invalid — fall back.
	return c.resolveActiveModel(models)
}

// ProjectConfig holds per-project overrides loaded from <repo>/.herm/config.json.
// Fields use omitempty so zero values mean "not overridden" (fall back to global).
type ProjectConfig struct {
	ActiveModel      string `json:"active_model,omitempty"`
	ExplorationModel string `json:"exploration_model,omitempty"`
	Personality      string `json:"personality,omitempty"`
	SubAgentMaxTurns int    `json:"sub_agent_max_turns,omitempty"`
}

// mergeConfigs overlays non-zero ProjectConfig fields onto a global Config.
func mergeConfigs(global Config, project ProjectConfig) Config {
	merged := global
	if project.ActiveModel != "" {
		merged.ActiveModel = project.ActiveModel
	}
	if project.ExplorationModel != "" {
		merged.ExplorationModel = project.ExplorationModel
	}
	if project.Personality != "" {
		merged.Personality = project.Personality
	}
	if project.SubAgentMaxTurns != 0 {
		merged.SubAgentMaxTurns = project.SubAgentMaxTurns
	}
	return merged
}

const defaultContainerImage = "debian:bookworm-slim"

func defaultConfig() Config {
	return Config{
		PasteCollapseMinChars: 200,
	}
}

func configPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(configDir, configFile)
	}
	return filepath.Join(home, configDir, configFile)
}

// ensureConfigDir creates the ~/.herm/ directory if it doesn't exist.
func ensureConfigDir() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home dir: %w", err)
	}
	return os.MkdirAll(filepath.Join(home, configDir), 0o755)
}

// loadConfig reads config from ~/.herm/config.json.
// If the file doesn't exist, it creates it with defaults.
// If the file is malformed, it returns defaults.
// Merging: starts from defaults and overlays whatever the file contains,
// so new fields added later automatically get their default values.
func loadConfig() (Config, error) {
	cfg := defaultConfig()

	if err := ensureConfigDir(); err != nil {
		return cfg, fmt.Errorf("creating config dir: %w", err)
	}

	data, err := os.ReadFile(configPath())
	if os.IsNotExist(err) {
		// First run — write defaults
		if saveErr := saveConfig(cfg); saveErr != nil {
			return cfg, fmt.Errorf("writing default config: %w", saveErr)
		}
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("reading config: %w", err)
	}

	// Unmarshal on top of defaults — missing fields keep their default values
	if err := json.Unmarshal(data, &cfg); err != nil {
		// Malformed JSON — return defaults
		return defaultConfig(), nil
	}

	return cfg, nil
}

// loadConfigFrom reads config from a specific directory path.
// Used for testing and custom config locations.
func loadConfigFrom(dir string) (Config, error) {
	cfg := defaultConfig()

	cfgDir := filepath.Join(dir, configDir)
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return cfg, fmt.Errorf("creating config dir: %w", err)
	}

	cfgPath := filepath.Join(cfgDir, configFile)
	data, err := os.ReadFile(cfgPath)
	if os.IsNotExist(err) {
		if saveErr := saveConfigTo(dir, cfg); saveErr != nil {
			return cfg, fmt.Errorf("writing default config: %w", saveErr)
		}
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("reading config: %w", err)
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return defaultConfig(), nil
	}

	return cfg, nil
}

// saveConfig writes config to ~/.herm/config.json.
func saveConfig(cfg Config) error {
	if err := ensureConfigDir(); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	return os.WriteFile(configPath(), data, 0o644)
}

// saveConfigTo writes config to a specific directory path.
func saveConfigTo(dir string, cfg Config) error {
	cfgDir := filepath.Join(dir, configDir)
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	return os.WriteFile(filepath.Join(cfgDir, configFile), data, 0o644)
}

// loadProjectConfig reads project-level overrides from <repoRoot>/.herm/config.json.
// Returns an empty ProjectConfig if the file doesn't exist or is malformed.
func loadProjectConfig(repoRoot string) ProjectConfig {
	if repoRoot == "" {
		return ProjectConfig{}
	}
	data, err := os.ReadFile(filepath.Join(repoRoot, configDir, configFile))
	if err != nil {
		return ProjectConfig{}
	}
	var pc ProjectConfig
	if err := json.Unmarshal(data, &pc); err != nil {
		return ProjectConfig{}
	}
	return pc
}

// saveProjectConfig writes project-level overrides to <repoRoot>/.herm/config.json.
func saveProjectConfig(repoRoot string, pc ProjectConfig) error {
	cfgDir := filepath.Join(repoRoot, configDir)
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return fmt.Errorf("creating project config dir: %w", err)
	}
	data, err := json.MarshalIndent(pc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling project config: %w", err)
	}
	return os.WriteFile(filepath.Join(cfgDir, configFile), data, 0o644)
}
