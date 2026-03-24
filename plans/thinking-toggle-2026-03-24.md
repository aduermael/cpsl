# Thinking Toggle — langdag + herm

Add a `Think *bool` field to langdag's completion API, wire it into each provider, and expose it in herm's config with thinking **off by default**.

## Context

Qwen 3.5 models (via Ollama) enable thinking by default, producing hidden `<think>` tokens that slow responses without user awareness. langdag has no way to control this. The fix spans two repos: langdag (provider-agnostic plumbing) and herm (user-facing config).

**Tri-state semantics:** `nil` = provider/model default, `true` = enable, `false` = disable. This matters because some models think by default (Qwen via Ollama) while others don't (GPT-4).

### Provider-specific behavior

| Provider | Think=true | Think=false | Think=nil |
|---|---|---|---|
| **Ollama** | `"think": true` in request body (Ollama extension to OpenAI format) | `"think": false` | No field sent → model default (Qwen thinks, others don't) |
| **Anthropic** | Enable extended thinking (`thinking.type = "enabled"` + `budget_tokens`). Requires `temperature` unset and adequate `max_tokens`. Use a sensible default budget (e.g. 8192). | Don't send thinking params (models don't think by default, so this is a no-op) | Same as false |
| **Gemini** | `thinkingConfig.thinkingBudget = N` in generationConfig (sensible default) | `thinkingConfig.thinkingBudget = 0` to explicitly disable | No thinkingConfig sent → model default |
| **OpenAI (direct)** | Ignored — o-series always reasons, others never do | Ignored | Ignored |
| **Grok** | Ignored — reasoning models always reason | Ignored | Ignored |

### Files involved

**langdag:**
- `types/types.go` — `CompletionRequest`, `Usage`
- `langdag.go` — prompt options (`WithThink`)
- `internal/provider/openai/protocol.go` — `chatCompletionRequest`, `buildRequest()`
- `internal/provider/openai/ollama.go` — Ollama-specific (shares protocol.go)
- `internal/provider/anthropic/protocol.go` — `buildParams()`
- `internal/provider/gemini/protocol.go` — `buildRequest()`, `generationConfig`

**herm:**
- `cmd/herm/config.go` — `Config`, `ProjectConfig`, defaults
- `cmd/herm/configeditor.go` — TUI tabs (Global, Project)
- `cmd/herm/agent.go` — `buildPromptOpts()`

### Success criteria

- `curl` to Ollama with Think=false produces no `<think>` tags in response
- Anthropic Think=true triggers extended thinking (visible in usage as reasoning tokens)
- Gemini Think=true triggers thinking (visible in usage as reasoning tokens)
- OpenAI/Grok: Think value does not cause API errors (silently ignored)
- herm defaults to thinking off; toggling it on in config propagates to langdag

---

## Phase 1: langdag — Core types and prompt option

- [x] 1a: Add `Think *bool` field to `CompletionRequest` in `types/types.go`; add `WithThink(enabled bool)` prompt option in `langdag.go` that wires into promptOptions → CompletionRequest conversion
- [x] 1b: Tests — unit test that `WithThink(true)` and `WithThink(false)` set the field correctly on the built CompletionRequest; test that omitting WithThink leaves it nil

## Phase 2: langdag — Provider wiring

**Parallel Tasks: 2a, 2b, 2c**

- [ ] 2a: **Ollama / OpenAI protocol** — Add `Think *bool` json field to `chatCompletionRequest` in `protocol.go`; set it in `buildRequest()` from `CompletionRequest.Think`. Use `*bool` + `omitempty` so it's omitted when nil. Applies to both Ollama and OpenAI direct (OpenAI ignores unknown fields). Add test in `openai_test.go` / `ollama_test.go` verifying the field appears in serialized JSON when set and is absent when nil.
- [ ] 2b: **Anthropic** — In `buildParams()`, when `req.Think != nil && *req.Think == true`, enable extended thinking with a default budget. Handle the constraints (temperature must be default, max_tokens must accommodate budget). When false or nil, don't send thinking params. Add test in `anthropic_test.go` / `protocol_test.go`.
- [ ] 2c: **Gemini** — In `buildRequest()`, when `req.Think != nil && *req.Think == true`, add `thinkingConfig` to generationConfig with a sensible default budget. When `*req.Think == false`, set budget to 0 to explicitly disable. When nil, omit. Add test in `gemini_test.go`.

## Phase 3: herm — Config and integration

- [ ] 3a: Add `Thinking *bool` to `Config` and `ProjectConfig` structs in `config.go`. Default to `false` (disabled) for new configs. Add to `mergeConfigs()`.
- [ ] 3b: Add "Thinking" toggle to the config editor — Global tab and Project tab (with globalHint). Follow the same pattern as DebugMode toggle.
- [ ] 3c: Wire into `buildPromptOpts()` in `agent.go` — pass `langdag.WithThink(value)` based on resolved config. Since herm defaults to false, this means thinking is off by default for all providers.
- [ ] 3d: Tests — verify config loading/saving round-trips the Thinking field; verify buildPromptOpts includes WithThink.
