# Coding Agent

**Goal:** Add an LLM-powered coding agent to CPSL that can execute tools (bash in container, git on host), streams responses in the TUI, and persists conversations as a DAG in SQLite via langdag.

**Builds on:** All previous plans (container execution, model selection, config, TTY shell)

## Context

CPSL currently has a chat UI (textarea → messages list), Docker container management (`ContainerClient.Exec()`), multi-provider model registry (Anthropic, OpenAI, Grok via OpenRouter), and git worktree management. User messages are displayed but never sent to an LLM. This plan adds the agent brain.

### Key files involved

- `main.go` — Chat mode update loop, message rendering, app state
- `config.go` — `Config` struct with API keys, `configuredProviders()`
- `container.go` — `ContainerClient.Exec(command, timeout)` returns `CommandResult{Stdout, Stderr, ExitCode}`
- `models.go` — `ModelDef` with provider/ID/pricing, `fetchModels()` from OpenRouter
- `configform.go` — Config form fields (API keys)

### Dependencies

- **langdag** (`github.com/langdag/langdag`) — DAG-based conversation persistence in SQLite, multi-provider LLM streaming (Anthropic, OpenAI, Gemini). Provides `Client.Prompt()`/`PromptFrom()` for LLM calls, `Storage` for node persistence. Node types: user, assistant, tool_call, tool_result.
- **pi-mono** (reference only) — Inspiration for system prompt structure: tool-aware guidelines (conditionally include based on available tools), project context loading, date/time appended last.

### Architecture

```
User Input (textarea)
    │
    ▼
Agent Loop (goroutine)
    │
    ├── langdag.Client.Provider().Stream()  ← LLM call with tools
    │       │
    │       ▼
    │   Stream events → chan AgentEvent → TUI Update loop
    │       │
    │       ▼ (if stop_reason == tool_use)
    │   Tool Router
    │       ├── BashTool → container.Exec() (inside Docker)
    │       └── GitTool  → os/exec git (on host, push needs approval)
    │       │
    │       ▼
    │   tool_result → append to messages → re-call LLM
    │
    ├── langdag Storage ← persist all nodes (user/assistant/tool_call/tool_result)
    │
    ▼
Done → final assistant message displayed
```

- Agent loop runs in a goroutine. Events (text deltas, tool calls, tool results, approval requests, errors, done) flow to the TUI via `chan AgentEvent` wrapped in `tea.Msg`.
- langdag handles both LLM provider abstraction and SQLite persistence.
- Tool interface: `Execute(ctx, input) → (result, error)` + `RequiresApproval(input) → bool`.
- Bash always runs in container. Git runs on host; push (force or not) requires user confirmation.
- Max tool iterations (e.g., 25) to prevent runaway loops.
- Web search is left to LLM providers (server-side tool, no local implementation needed).

### Open questions

- langdag's public `Prompt`/`PromptFrom` API may not expose `ToolDefinition` in the `CompletionRequest`. We may need to add a `WithTools()` option to langdag, or access the Provider/Storage directly. Resolve during Phase 1 by inspecting langdag's public surface.
- Should we add a `GeminiAPIKey` to config? langdag supports Gemini natively. We could also route Grok through OpenAI-compatible endpoint. Decide during Phase 1.

---

## Phase 1: langdag Integration & Agent Types

- [x] 1a: Add langdag dependency (`go get github.com/langdag/langdag`). Add `GeminiAPIKey` to `Config` struct and `configform.go`. Create `agent.go` with core types: `Tool` interface (`Definition()`, `Execute()`, `RequiresApproval()`), `AgentEvent` variants (text delta, tool call start, tool call done, tool result, approval request, done, error), `Agent` struct holding langdag client, tools, system prompt, and event/approval channels.
- [x] 1b: Initialize langdag client on startup — configure providers from API keys in config, open SQLite DB at `~/.cpsl/conversations.db`. Map `Config.AnthropicAPIKey` / `OpenAIAPIKey` / `GeminiAPIKey` to langdag provider config. Wire into the `model` struct in `main.go`.
- [x] 1c: Implement the agent loop in `Agent.Run()` — accept user message, build `CompletionRequest` with system prompt + conversation history + tool definitions, call `Provider.Stream()`, consume stream events, emit `AgentEvent` on channel. When stop reason is `tool_use`: extract tool calls, check `RequiresApproval()`, execute tools (or wait for approval), append tool results, re-call LLM. Cap at max iterations. Persist all nodes (user, assistant, tool_call, tool_result) to langdag storage after each exchange.

## Phase 2: Tool Implementations

- [x] 2a: Implement `BashTool` in `tools.go` — wraps `ContainerClient.Exec(command, timeout)`. Tool definition describes it as bash execution inside the dev container at `/workspace`. Truncate output beyond a limit (e.g., last 200 lines / 30KB) with a note that output was truncated. Default timeout configurable. Return combined stdout+stderr with exit code.
- [x] 2b: Implement `GitTool` in `tools.go` — executes git commands on the host via `os/exec` in the worktree directory. Allowlist of subcommands: `status`, `diff`, `log`, `show`, `branch`, `checkout`, `add`, `commit`, `pull`, `push`, `fetch`, `stash`, `rebase`, `merge`, `reset`, `tag`. `RequiresApproval()` returns true for `push` (including `--force`). Return stdout+stderr.

## Phase 3: System Prompt

- [x] 3a: Create `systemprompt.go` with `buildSystemPrompt(tools []Tool, workDir string) string`. Structure (inspired by pi-mono): (1) Role — you are a coding agent working in a containerized environment; (2) Tool guidelines — conditional on which tools are available (e.g., "Use bash to explore files, run tests, install packages. Commands execute inside the container at /workspace."); (3) Git guidelines — if git tool available, explain it runs on the host, describe approval flow for push; (4) General coding guidelines — read before editing, explain changes, don't over-engineer; (5) Current date/time and working directory (always last).

## Phase 4: UI Integration

- [ ] 4a: Wire agent into chat mode — when user sends a non-slash message and an LLM provider is configured, create/resume an `Agent` and call `Run()` in a goroutine. Receive `AgentEvent` messages in `Update()`. Display streaming text deltas in the viewport (append to current assistant message as chunks arrive). Show a spinner/indicator while agent is working. Handle done/error events. If no provider is configured, show a message directing to `/config`.
- [ ] 4b: Render tool calls and results in the viewport — when a tool_call event arrives, show the tool name and a summary of the input (e.g., `▶ bash: ls -la /workspace`). When the tool result arrives, show it (collapsed if long, expandable). Style with distinct colors (dimmed for tool I/O vs. normal for assistant text).
- [ ] 4c: Implement approval flow — when the agent emits an approval request (git push), show a confirmation bar or inline prompt ("Allow `git push origin main`? [y/n]"). Capture y/n keypress, send approval/denial back to the agent via the approval channel. On denial, the agent tells the LLM the tool call was rejected.

## Phase 5: Tests

- [ ] 5a: Agent loop tests — use langdag's mock provider (or build a mock implementing the Provider interface). Test: single text response streams correctly, tool call is detected and executed, tool result is re-submitted, max iteration cap works, approval flow pauses and resumes, error handling (LLM error, tool error). Test conversation persistence (nodes saved in correct order with correct types).
- [ ] 5b: Tool tests — `BashTool`: mock `ContainerClient`, verify command passed through, output truncation works, timeout respected. `GitTool`: mock `exec.Command`, verify allowlisted commands succeed, non-allowlisted commands rejected, `RequiresApproval` returns true for push variants.
- [ ] 5c: UI integration tests — use bubbletea programmatic Update loop. Test: user sends message → agent starts → streaming text appears in viewport → tool call rendered → tool result rendered → final message displayed. Test approval flow: approval event → confirmation shown → user presses y → agent continues. Test no-provider case shows config message.

## Success criteria

- User types a message → LLM responds with streaming text in the viewport
- LLM can call bash tool → command runs in container → output returned to LLM → conversation continues
- LLM can call git tool → command runs on host → output returned to LLM
- Git push shows approval prompt → user approves → push executes (or denies → LLM informed)
- Conversation persists in SQLite — restarting CPSL can load previous conversation
- Works with Anthropic, OpenAI, and Gemini (any configured provider)
- All tests pass
