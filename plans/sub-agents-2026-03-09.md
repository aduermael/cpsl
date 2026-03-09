# Sub-Agent Support

## Context

- CPSL uses langdag (currently v0.2.0, upgrading to v0.3.0) as a conversation/LLM-routing library
- The agent loop lives entirely in CPSL's `agent.go` — langdag provides conversation persistence, streaming, and multi-provider routing
- langdag v0.3.0 adds: `ModelCatalog` with pricing data (per 1M token prices via LiteLLM), `WithMaxTurns` option, thread-safe `PromptResult`, structured tool results (`ContentJSON`), and confirmed concurrent-safe `Client`
- CPSL currently has a single `Agent` that runs tools sequentially with a 25-iteration cap
- Current pricing data is in CPSL's embedded `models.json` (`ModelDef.PromptPrice`/`CompletionPrice`) — langdag now provides this via `ModelCatalog`
- Config editor (`/config`) has 2 tabs: "API Keys" and "Settings" — new fields go in Tab 1
- System prompt is built in `systemprompt.go` with conditional tool sections
- `handleAgentEvent` in `main.go` renders events into `a.messages` (committed lines) and `a.streamingText` (live partial line)

## Key files

- `agent.go` — Agent struct, NewAgent, Run, runLoop, Tool interface, AgentEvent types
- `main.go` — App struct, startAgent, handleAgentEvent, config editor (cfgTabFields), rendering
- `systemprompt.go` — buildSystemPrompt with tool-conditional sections
- `config.go` — Config struct, load/save
- `models.go` — ModelDef, builtinModels, pricing formatting, models.json embed
- `tools.go` — BashTool, GitTool, DevEnvTool, WebSearchToolDef

## Open questions

- Should sub-agents share the same langdag `Client` instance (documented as concurrent-safe in v0.3.0) or create separate ones? Sharing is simpler and uses one SQLite connection. Start with sharing; revisit if issues arise.
- What tools should the sub-agent get? Same tools as the parent for now (bash, git, devenv if available). The orchestrator prompt should guide when to delegate vs. act directly.
- Should sub-agent conversations be linked to the parent conversation tree? For now, sub-agents create independent conversation roots in the same SQLite DB. Linking via metadata can come later.

---

## Phase 1: Upgrade langdag to v0.3.0

- [x] 1a: Update `go.mod` to use `langdag.com/langdag v0.3.0`. Run `go mod tidy`. Verify build and existing tests pass.

## Phase 2: Replace CPSL model pricing with langdag's ModelCatalog

- [x] 2a: On startup, load langdag's model catalog via `langdag.LoadModelCatalog(cachePath)` (cache at `~/.cpsl/model_catalog.json`). Store the `*langdag.ModelCatalog` on the App struct. Populate `ModelDef.PromptPrice`, `CompletionPrice`, `ContextWindow` from the catalog, falling back to `models.json` values when a model isn't in the catalog.
- [x] 2b: Remove `prompt_price`, `completion_price`, and `context_window` fields from `models.json` — keep only `provider`, `id`, `display_name`. `models.json` becomes the list of models CPSL knows about; pricing comes from langdag catalog. Update `ModelDef` struct and `builtinModels()` accordingly. Update tests.
- [x] 2c: Add a background fetch on startup: `langdag.FetchModelCatalog(ctx, cachePath)` to update the cache. Non-blocking, best-effort — if it fails, cached/embedded data is fine.

## Phase 3: Live cost display

- [x] 3a: Add cost tracking state to App: `sessionCostUSD float64` (accumulated cost for current session). Add a helper `computeCost(modelID string, usage types.Usage) float64` that looks up the model in the catalog and calculates `(inputTokens * inputPrice + outputTokens * outputPrice) / 1_000_000`. Handle cache pricing for Anthropic (cache read tokens are cheaper).
- [x] 3b: Extend `AgentEvent` with a `Usage *types.Usage` and `Model string` field. In `runLoop`, after each stream completes (the `Done` chunk with `Response`), extract usage from the `StreamChunk`'s completion response and emit it alongside `EventDone` or a new `EventUsage` event. For tool-loop iterations, emit usage after each LLM call.
- [x] 3c: In `handleAgentEvent`, when receiving usage data, call `computeCost` and add to `sessionCostUSD`. Display the running cost in the status/separator line (where the model name or other metadata is shown). Format as `$0.0000` for small amounts, `$0.01` etc. for larger. Update on every LLM call completion.
- [x] 3d: Add tests for `computeCost` covering standard tokens, cache read tokens, and models not in catalog (returns 0).

## Phase 4: Sub-agent tool

- [x] 4a: Create a `SubAgentTool` struct implementing the `Tool` interface. Definition: name `"agent"`, description explaining it spawns a sub-agent for complex subtasks, input schema with `task` (string, required — the task description) and `tools` (optional string array — which tools the sub-agent should have, defaults to all available). `RequiresApproval` returns false.
- [x] 4b: `SubAgentTool.Execute` creates a new `Agent` sharing the same `langdag.Client`. Builds a focused system prompt for the sub-agent (reuse `buildSystemPrompt` but prepend a "you are a sub-agent" preamble instructing it to complete the task and return a concise summary). Passes `WithMaxTurns(n)` from the config's token budget (interpreted as max turns for now). Runs the sub-agent synchronously, drains its event stream, collects all text output. Returns the final concatenated text as the tool result.
- [ ] 4c: Add `SubAgentMaxTurns int` to `Config` struct (default: 15). Add it to the `/config` Settings tab as "Sub-Agent Max Turns" with numeric input. Wire it through to `SubAgentTool` construction.
- [ ] 4d: Register `SubAgentTool` in `startAgent()` alongside other tools, passing the shared `langdag.Client`, available tools list, and config. Add it to the system prompt tool guidelines in `systemprompt.go` with a section explaining when to use sub-agents.

## Phase 5: Orchestrator system prompt

- [ ] 5a: Restructure `buildSystemPrompt` to frame the main agent as an **orchestrator**. Add a top-level section before the current "Role & Capabilities" that explains: you are an orchestrator agent, you should delegate complex subtasks to sub-agents using the `agent` tool, keep your own context lean by delegating research/implementation/debugging to sub-agents, synthesize their results. Keep direct tool use for simple one-shot operations.
- [ ] 5b: Add guidelines for context window management: "When a task involves multiple steps (exploration, implementation, testing), delegate each step to a sub-agent rather than doing everything in your context. Sub-agents have their own context windows and won't consume yours. Use sub-agents proactively to stay within budget."

## Phase 6: Live sub-agent display (3-line cap)

- [ ] 6a: Add an `AgentID string` field to `AgentEvent`. Generate a unique ID per agent instance in `NewAgent`. The main agent and sub-agents emit events tagged with their ID.
- [ ] 6b: In `SubAgentTool.Execute`, instead of silently draining events, forward sub-agent events to the parent agent's event channel with a new event type `EventSubAgentDelta` (text) and `EventSubAgentStatus` (tool calls, completion). These carry the sub-agent's `AgentID`.
- [ ] 6c: In `handleAgentEvent`, handle `EventSubAgentDelta`: maintain a `subAgentLines []string` buffer on App. On each delta, update the buffer. In the rendering path (where `streamingText` is displayed), show the sub-agent activity as a dim/italic block capped to 3 lines — show the last 3 lines of the sub-agent's output with a `[sub-agent]` prefix. Clear the buffer when `EventSubAgentStatus` signals completion.
- [ ] 6d: When the sub-agent finishes, collapse its 3-line live display into a single summary line in `a.messages` (e.g., `[sub-agent] completed: <first line of result>`). The full result is passed back as the tool result in the main agent's conversation.

## Phase 7: Sub-agent cost tracking

- [ ] 7a: Sub-agent events should include usage data (same as Phase 3b). In `handleAgentEvent`, accumulate sub-agent usage into the same `sessionCostUSD`. The live cost display automatically reflects sub-agent costs.
- [ ] 7b: Add a test that creates a SubAgentTool with a mock langdag client, runs a task, and verifies the tool result contains the sub-agent's output text. Verify events are emitted with correct AgentID.

---

## Success criteria

- `go build` and `go test ./...` pass with langdag v0.3.0
- Model pricing in `/model` menu comes from langdag catalog (no duplicate pricing data in CPSL)
- Running cost displayed live during agent work, updating after each LLM call
- Main agent uses orchestrator-style system prompt, delegating to sub-agents via `agent` tool
- Sub-agent max turns configurable in `/config` Settings tab
- Sub-agent work streams live in the TUI, capped to 3 lines with dim styling
- Sub-agent cost included in session total
- No regressions in existing tool execution, approval flow, or conversation continuity
