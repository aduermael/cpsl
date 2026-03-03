# Multi-Provider API Keys & Model Selection

**Goal:** Allow users to configure API keys for Anthropic, Grok, and OpenAI providers, and select an active model via `/model` command. No API requests — just config and selection UI.

## Codebase Context

- **config.go** — `Config` struct with `loadConfig`/`saveConfig`, stored in `.cpsl/config.json`. Merges defaults for forward compat.
- **configform.go** — `configForm` with `textinput` fields, purple-themed, validation, `applyTo(&Config)`.
- **main.go** — BubbleTea model with `modeChat`/`modeConfig`, slash command system (`commands` slice, `handleCommand`, `filterCommands`), autocomplete.
- **Tests** — `config_test.go` (8 tests), `model_test.go` (58 tests), `integration_test.go` (11 tests). All use programmatic `Update` loop.

## Design Decisions

- **API keys in config:** Three optional string fields (`anthropic_api_key`, `grok_api_key`, `openai_api_key`). Stored in `config.json`. Shown as masked inputs in the `/config` form.
- **Model registry:** A static slice of `ModelDef` structs (provider, model ID, display name). Defined in a new `models.go` file. Providers: `anthropic`, `grok`, `openai`. Each provider has a few representative models.
- **Active model:** Persisted as `active_model` (model ID string) in config. Loaded into `model.activeModel` at startup. If the stored model is invalid or its provider has no key, fall back to the first available model (or empty if no keys).
- **`/model` command:** Opens a new `modeModel` screen showing only models whose provider has a configured API key. Cursor-based selection list (up/down to move, enter to select, esc to cancel). Selecting a model updates both session state and config file.
- **Config form changes:** Add three masked API key fields to the existing `/config` form. Keys shown as `sk-...xxxx` style masked values. Tab cycles through all fields.

## Phase 1: Model Registry & Config Expansion

- [x] 1a: Create `models.go` with provider constants, `ModelDef` struct (provider, ID, display name), and the static model registry. Add a helper to filter models by available providers (given a set of configured API keys).
- [ ] 1b: Expand `Config` struct with `AnthropicAPIKey`, `GrokAPIKey`, `OpenAIAPIKey` (string) and `ActiveModel` (string). Defaults are empty strings. Add a `configuredProviders()` method that returns which providers have keys set. Add an `availableModels()` helper that returns models filtered to configured providers. Add a `resolveActiveModel()` method that validates `ActiveModel` against available models and falls back if needed.
- [ ] 1c: Add tests for model registry filtering, `configuredProviders`, `resolveActiveModel` (valid model, missing key fallback, empty config).

## Phase 2: API Key Fields in Config Form

- [ ] 2a: Add three API key `textinput` fields to `configForm` in `configform.go`. Use `EchoMode = EchoPassword` for masking. Pre-populate from current config values. Update `validate()` (keys are optional, no validation needed beyond trimming) and `applyTo()` to read/write the key fields.
- [ ] 2b: Add tests for config form with API key fields — verify applyTo writes keys to config, verify masking is set, verify tab cycles through all fields (paste threshold + 3 key fields).

## Phase 3: /model Command & Selection UI

- [ ] 3a: Add `modeModel` to `appMode`. Create `modelList` component in a new `modellist.go` file — holds filtered available models, cursor index, highlighted active model. Supports up/down navigation, enter to select, esc to cancel. Purple-themed view matching existing style.
- [ ] 3b: Register `/model` command in `commands` slice and `handleCommand`. Wire up `enterModelMode` / `exitModelMode` transitions similar to config mode. On enter: build model list from current config. On exit with selection: update `model.activeModel`, persist to config, show success message. Esc discards. If no API keys configured, show error message instead of entering model mode.
- [ ] 3c: Add model selection tests — entering/exiting model mode, selecting a model persists it, esc discards, no-keys-configured error, only shows models for configured providers, active model highlighting.

## Phase 4: Integration Tests

- [ ] 4a: Add integration tests covering full flows: configure an API key via `/config` → use `/model` to select a model → verify it persists. Test switching providers (remove a key, active model falls back). Test `/model` with no keys shows error.

## Success Criteria

- `Config` struct holds API keys and active model, persisted to `.cpsl/config.json`
- `/config` form shows masked API key fields for all three providers
- `/model` lists only models for providers with configured keys
- Selecting a model updates session and persists to config
- Starting a new session loads the previously selected model
- All existing tests continue to pass
- New tests cover model registry, config form expansion, model selection, and integration flows
