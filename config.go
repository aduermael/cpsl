package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const configDir = ".cpsl"
const configFile = "config.json"

type Config struct {
	PasteCollapseMinChars int             `json:"paste_collapse_min_chars"`
	AnthropicAPIKey       string          `json:"anthropic_api_key,omitempty"`
	GrokAPIKey            string          `json:"grok_api_key,omitempty"`
	OpenAIAPIKey          string          `json:"openai_api_key,omitempty"`
	GeminiAPIKey          string          `json:"gemini_api_key,omitempty"`
	ActiveModel           string          `json:"active_model,omitempty"`
	ModelSortCol          string          `json:"model_sort_col,omitempty"`   // "name","provider","price","context"
	ModelSortDirs         map[string]bool `json:"model_sort_dirs,omitempty"` // column name → ascending (per-column)
	ContainerImage        string          `json:"container_image,omitempty"`
	DisplaySystemPrompts  bool            `json:"display_system_prompts,omitempty"`
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

const defaultContainerImage = "alpine:latest"

func defaultConfig() Config {
	return Config{
		PasteCollapseMinChars: 200,
	}
}

// containerConfig returns a ContainerConfig with the image resolved.
func (c Config) containerConfig() ContainerConfig {
	img := c.ContainerImage
	if img == "" {
		img = defaultContainerImage
	}
	return ContainerConfig{Image: img}
}

func configPath() string {
	return filepath.Join(configDir, configFile)
}

// ensureConfigDir creates the .cpsl/ directory if it doesn't exist.
func ensureConfigDir() error {
	return os.MkdirAll(configDir, 0o755)
}

// loadConfig reads config from .cpsl/config.json.
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

// saveConfig writes config to .cpsl/config.json.
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
