# Smart Model Defaults

**Goal:** When no model is explicitly configured, pick sensible defaults per provider instead of blindly using the first model from the catalog. Exploration model should default to a cheap/fast model (haiku for Anthropic), not fall back to the expensive active model.

**Provider priority (already correct):** Anthropic → OpenAI → Grok → Gemini

---

## Current Behavior

- `resolveActiveModel()` — if `ActiveModel` is unset/invalid, returns `available[0].ID` (first model from catalog for the first configured provider). No control over which model that is.
- `resolveExplorationModel()` — if `ExplorationModel` is unset, falls back to `resolveActiveModel()`. This means sub-agents and compaction use the same expensive model as the main chat.
- `defaultLangdagProvider()` — already prioritizes Anthropic → OpenAI → Grok → Gemini. No changes needed here.

## Desired Behavior

- `resolveActiveModel()` — if unset, prefer a provider-specific "recommended" model ID before falling back to catalog order.
- `resolveExplorationModel()` — if unset, prefer a provider-specific "cheap" model ID (e.g. haiku for Anthropic), then fall back to the active model.

## Codebase Context

- **config.go:79-108** — `resolveActiveModel()` and `resolveExplorationModel()` are the only functions that need logic changes.
- **config.go:54-69** — `defaultLangdagProvider()` determines provider priority. Already correct.
- **models.go:41-58** — `modelsFromCatalog()` builds the model list from the langdag catalog. The catalog contents are dynamic (loaded at runtime), so defaults must gracefully handle missing model IDs.
- **config_test.go:531-613** — existing exploration model tests to extend.

## Open Questions

- Exact model IDs for non-Anthropic provider defaults (e.g. `gpt-4o` / `gpt-4o-mini` for OpenAI, `grok-3` / `grok-3-mini` for Grok, `gemini-2.0-flash` for Gemini). These should match what the langdag catalog actually contains. Will verify during implementation by checking the catalog or model list.

---

## Phase 1: Add provider-specific default model maps
- [ ] 1a: Add `defaultActiveModels` and `defaultExplorationModels` maps in config.go — keyed by provider, valued by model ID. Anthropic: `claude-sonnet-4-6` / `claude-haiku-4-5`. Other providers: best guess from catalog, verified during implementation.
- [ ] 1b: Add helper `preferredDefault(models []ModelDef, provider string, defaults map[string]string) string` — looks up the default ID for the provider, checks it exists in the available models list, returns it or empty string.

## Phase 2: Update resolution logic
- [ ] 2a: Update `resolveActiveModel()` — when falling back (no valid ActiveModel set), try `preferredDefault()` for the default provider before returning `available[0].ID`.
- [ ] 2b: Update `resolveExplorationModel()` — when ExplorationModel is unset, try `preferredDefault()` with the exploration defaults map for the active model's provider, then fall back to `resolveActiveModel()`.

## Phase 3: Tests
- [ ] 3a: Add test: Anthropic key set, no ActiveModel configured → resolves to `claude-sonnet-4-6` (if in catalog).
- [ ] 3b: Add test: Anthropic key set, no ExplorationModel configured → resolves to `claude-haiku-4-5` (if in catalog), NOT to active model.
- [ ] 3c: Add test: preferred default model not in catalog → gracefully falls back to first available.
- [ ] 3d: Add test: OpenAI-only config gets appropriate defaults.
- [ ] 3e: Run full test suite, verify build.
