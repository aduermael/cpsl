package main

// Provider constants for supported AI providers.
const (
	ProviderAnthropic = "anthropic"
	ProviderGrok      = "grok"
	ProviderOpenAI    = "openai"
)

// ModelDef describes a model available for selection.
type ModelDef struct {
	Provider    string
	ID          string
	DisplayName string
}

// modelRegistry is the static list of all known models.
var modelRegistry = []ModelDef{
	{Provider: ProviderAnthropic, ID: "claude-sonnet-4-20250514", DisplayName: "Claude Sonnet 4"},
	{Provider: ProviderAnthropic, ID: "claude-haiku-4-20250414", DisplayName: "Claude Haiku 4"},
	{Provider: ProviderAnthropic, ID: "claude-opus-4-20250514", DisplayName: "Claude Opus 4"},
	{Provider: ProviderGrok, ID: "grok-3", DisplayName: "Grok 3"},
	{Provider: ProviderGrok, ID: "grok-3-mini", DisplayName: "Grok 3 Mini"},
	{Provider: ProviderOpenAI, ID: "gpt-4o", DisplayName: "GPT-4o"},
	{Provider: ProviderOpenAI, ID: "gpt-4o-mini", DisplayName: "GPT-4o Mini"},
	{Provider: ProviderOpenAI, ID: "o3-mini", DisplayName: "o3-mini"},
}

// filterModelsByProviders returns models whose provider is in the given set.
func filterModelsByProviders(providers map[string]bool) []ModelDef {
	var result []ModelDef
	for _, m := range modelRegistry {
		if providers[m.Provider] {
			result = append(result, m)
		}
	}
	return result
}

// findModelByID returns the model with the given ID, or nil if not found.
func findModelByID(id string) *ModelDef {
	for i := range modelRegistry {
		if modelRegistry[i].ID == id {
			return &modelRegistry[i]
		}
	}
	return nil
}
