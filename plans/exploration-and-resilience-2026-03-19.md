# Exploration & Resilience Improvements

**Goal:** Make herm's project exploration faster, more cost-effective, and more resilient. Improve sub-agent UX so the user understands what's happening, add smart file reading to reduce context waste, improve sub-agent result quality, and ensure transient failures never kill the conversation.

**Success criteria:**
- Sub-agent display clearly shows per-agent progress with task labels, not a merged stream
- File outline tool lets the agent scan function signatures before committing to full reads
- Sub-agent results include structured summaries (not just first 500 bytes)
- API failures trigger automatic retry with backoff — conversation never dies from a transient error
- Main agent can recover from any sub-agent failure gracefully

---

## Current State

**What's already been done** (from prior plans):
- Dedicated Glob, Grep, Read tools with structured I/O (token-efficient-exploration)
- Exploration model routing — sub-agents use cheaper model (smart-model-defaults)
- Project snapshot at startup — sub-agents receive it too (fast-project-orientation)
- Sub-agent output files at `.herm/agents/<id>.md` with 500-byte inline summary (smarter-subagent-delegation)
- Parallel sub-agent execution via WaitGroup (smarter-subagent-delegation)
- Non-blocking emit, channel closure, panic recovery (fix-agent-silent-stops)
- Tool result clearing at 80% + compaction at 95% context (token-efficient-exploration)
- Lean sub-agent system prompts (~40% shorter than main agent)
- Head+tail bash truncation, lowered output limits

**What's still missing:**
1. **Sub-agent display is confusing** — All sub-agents share 3 dim/italic lines with no visual separation between agents. The user sees `[sub-agent] tool: bash / [sub-agent] tool: bash / [sub-agent] I'll compile...` and can't tell if that's 1 agent doing 3 things or 3 agents. No task labels.
2. **No smart file reading** — Agent reads entire files (up to 500 lines) even when it only needs function signatures. No outline/skeleton mode. Context fills up fast during exploration.
3. **Sub-agent summaries are dumb** — `summarizeOutput()` takes first 500 bytes, cut at line boundary. Often returns boilerplate like "Based on my analysis..." instead of the actual findings.
4. **No API retry** — Any LLM API failure immediately kills the conversation. No backoff, no retry. Stream interruptions are fatal.
5. **Sub-agent errors are informational only** — `EventError` in sub-agent is caught but not actionable. The main agent sees "sub-agent produced no output" with no explanation of what went wrong.

---

**Parallel Phases: 1, 2, 3**

## Phase 1: Per-Agent Sub-Agent Display

Fix the confusing merged sub-agent display. Each active sub-agent should show its own labeled status line so the user can track parallel agents independently.

**Codebase context:**
- Display rendering: `main.go` `subAgentDisplayLines()` (~line 2068-2090) — returns last 3 lines from shared `subAgentLines` slice
- Event handling: `main.go` EventSubAgentDelta/EventSubAgentStatus handlers (~line 4913-4939) — appends to shared `subAgentBuf`/`subAgentLines`
- Events carry `AgentID` field (agent.go EventSubAgentDelta/EventSubAgentStatus) but it's currently ignored in display
- Sub-agent tool input includes `task` field that could be used as a display label

**What needs to change:**
- Replace the single shared `subAgentLines` slice with a per-agent state map keyed by AgentID
- Each agent's display shows: its task label (first ~40 chars of task), current activity (tool name or text snippet), and a simple status (running/done)
- When multiple agents run in parallel, show one line per active agent
- When an agent completes, collapse its line to a brief completion summary
- The task label comes from the `task` field in the agent tool input — capture it when `EventSubAgentStatus` with `"tool: ..."` first arrives, or from a new `EventSubAgentStart` event

- [ ] 1a: **Add `EventSubAgentStart` event** — Emit from `SubAgentTool.Execute()` when a sub-agent begins, carrying `AgentID` and `Task` (the task description). This gives the display the label it needs. In `subagent.go`, forward this event right after creating the agent, before the goroutine starts.

- [ ] 1b: **Replace shared sub-agent display state with per-agent tracking** — In `main.go`, replace `subAgentBuf string` and `subAgentLines []string` with a `subAgents map[string]*subAgentDisplay` struct containing: `task string` (label), `status string` (current activity), `done bool`. Update EventSubAgentDelta/EventSubAgentStatus handlers to route events to the correct agent entry by AgentID.

- [ ] 1c: **Rewrite `subAgentDisplayLines()`** — Instead of returning the last 3 shared lines, iterate active sub-agents and render one line per agent: `[agent] <truncated task label>: <current status>`. Show up to 5 active agents. Done agents are removed from display (their completion was already logged as a message). Use dim/italic styling but make the task label normal weight for readability.

- [ ] 1d: **Show agent completion as a structured message** — When `EventSubAgentStatus` with `"done"` arrives, instead of the current `"[sub-agent] completed: <first line>"`, show: `[agent <short-id>] completed: <task label>`. Include token usage if available. The user should be able to match the completion message back to the agent they saw running.

- [ ] 1e: **Separate main-agent vs sub-agent token counters** — Currently `sessionInputTokens`/`sessionOutputTokens` include sub-agent usage because sub-agents forward `EventUsage` events that get added to the same counters (main.go ~line 4862). Split this: add `mainAgentInputTokens`/`mainAgentOutputTokens` fields that only count main-agent LLM calls. The status line (↑/↓ display at ~line 2026) should show main-agent tokens only. The `/stats` display should show both: main agent tokens and total tokens (including sub-agents). To distinguish, tag `EventUsage` events from sub-agents — they already flow through `SubAgentTool.forward()` which could set a flag, or use the existing `AgentID` field (empty = main agent).

- [ ] 1f: **Show per-agent token usage in completion messages** — When a sub-agent completes, the completion message in the conversation should include its token usage: `[agent <short-id>] completed: <task label> (↑Nk ↓Nk, M tool calls)`. The sub-agent already tracks `totalInputTokens`/`totalOutputTokens` in `Execute()` — include them in the `EventSubAgentStatus` "done" event or in a new field on the completion message. Tool call count can be derived from `turns` counter already in `Execute()`.

- [ ] 1g: **Test per-agent display** — Verify: single agent shows labeled status, parallel agents show separate lines, agent completion shows structured summary with token counts, status line shows only main-agent tokens, `/stats` shows both main and total, display handles rapid event interleaving correctly.

**Failure modes:**
- AgentID mismatch: events without matching start event → create entry on first event with "unknown task" label
- Many parallel agents (>5): show first 5 + "and N more..." line

---

## Phase 2: File Outline Tool for Smart Exploration

Add an `outline` tool that returns function/type/class signatures from a file without reading the full content. This lets the agent understand a file's structure in ~50-100 tokens instead of ~2000-5000 tokens for a full read.

**Codebase context:**
- Tool registration: tools are defined as structs implementing the `Tool` interface (Definition, RequiresApproval, Execute)
- File tools: `filetools.go` has ReadFileTool, EditFileTool, WriteFileTool, GlobTool, GrepTool
- Tools execute commands in the Docker container via `execInContainer()`
- The container has standard tools: `rg`, `awk`, `sed`, `grep`, `tree` — but NOT `ctags` or `tree-sitter`

**Approach:** Use `grep`/`awk` patterns tuned per language rather than requiring tree-sitter. This is less precise but works universally and needs no new binary in the container.

The outline tool should:
1. Detect language from file extension
2. Run a language-appropriate regex to extract signatures (function defs, type/struct/class declarations, method signatures, interface definitions)
3. Return them with line numbers, indented to show nesting
4. Cap output at ~100 lines

**Supported languages (initial):**
- Go: `^func `, `^type `, `^var `, `^const ` — captures all top-level declarations
- Python: `^class `, `^\s*def `, `^\s*async def ` — captures classes and functions with indentation
- JavaScript/TypeScript: `^(export )?(function|class|const|interface|type|enum) `, `^\s*(async )?[a-zA-Z]+\(` — top-level declarations and methods
- Rust: `^(pub )?(fn|struct|enum|trait|impl|mod|type) `
- Fallback: `grep -n` for common patterns or just return first 20 + last 20 lines

- [ ] 2a: **Implement `OutlineTool`** — New tool in `filetools.go`. Input schema: `file_path` (required). Detects language from extension, runs appropriate `grep -n` or `awk` command in container to extract signatures. Returns compact output with line numbers. Falls back to head+tail for unsupported languages.

- [ ] 2b: **Add outline guidance to system prompt** — In the tools section, add guidance: "Use `outline` before `read_file` when exploring unfamiliar files. `outline` returns function/type signatures (~50-100 tokens) — read the full file only when you need implementation details." Place this near the existing Read tool guidance.

- [ ] 2c: **Add outline to sub-agent tool set** — Ensure the outline tool is included in the sub-agent's tool list. Sub-agents doing exploration will benefit the most from this.

- [ ] 2d: **Test outline tool** — Test with Go files (the herm codebase itself), Python, JS/TS. Verify: correct signature extraction, line numbers present, output capped, fallback works for unknown extensions, binary files handled gracefully.

**Failure modes:**
- File doesn't exist → return error like ReadFileTool
- Binary file → detect and return "binary file, cannot outline"
- Very large file with many signatures → cap at 100 lines with "[... N more declarations]"
- Language detection wrong (e.g., .h could be C or C++) → patterns overlap enough to work for both

**Open questions:**
- Should outline also show import/include statements? They reveal dependencies cheaply. Leaning yes for Go (imports are compact) and no for JS (imports can be 50+ lines).

---

## Phase 3: Resilient API Calls with Retry

Ensure transient API failures (rate limits, network blips, server errors) never kill the conversation. Add retry with exponential backoff to LLM API calls.

**Codebase context:**
- API calls: `agent.go` `runLoop()` calls `t.client.Prompt()` / `t.client.PromptFrom()` (langdag client)
- Stream processing: `for chunk := range stream.Events()` loop processes responses
- Current failure behavior: API error → `EventError` + `EventDone` → conversation ends
- Stream interruption: detected after loop exits without `chunk.Done` → `EventError` → loop breaks
- Sub-agent failures: caught in `subagent.go:219-222`, non-fatal to parent

**What needs to change:**
- Wrap LLM API calls in a retry loop with exponential backoff
- Distinguish retryable errors (429, 500, 502, 503, 529, network timeout) from permanent errors (400, 401, invalid request)
- Show retry status to user via events so they know the agent is retrying, not stuck
- Cap retries (3 attempts with 2s/4s/8s backoff)

- [ ] 3a: **Add `retryablePrompt()` helper** — New function in `agent.go` that wraps `client.Prompt()` / `client.PromptFrom()` with retry logic. Takes the same arguments plus a retry config (max attempts, base delay, retryable error classifier). Returns the stream or final error after exhausting retries. Emits `EventInfo` with "retrying in Ns..." on each retry so the TUI can show status.

- [ ] 3b: **Add error classification** — Function `isRetryableError(err error) bool` that checks for: HTTP 429/500/502/503/529, network timeout errors, connection reset, EOF during stream. Non-retryable: 400 (bad request), 401 (auth), 403 (forbidden), context canceled.

- [ ] 3c: **Replace direct API calls with retryable wrapper** — In `runLoop()`, replace the two `client.Prompt()`/`client.PromptFrom()` call sites with `retryablePrompt()`. Both the initial prompt and the follow-up (after tool execution) should retry.

- [ ] 3d: **Add `EventRetry` event type** — New event that carries: attempt number, delay, error message. The TUI shows this as a transient status line: `"⟳ API error, retrying in 4s (attempt 2/3)..."`. This replaces the current silent failure.

- [ ] 3e: **Handle stream interruption with retry** — When a stream closes without `chunk.Done`, instead of immediately emitting EventError and breaking, attempt to retry the same prompt (from the same node). This handles network blips mid-response. Limit stream retries to 1 attempt since partial responses may have already been processed.

- [ ] 3f: **Test retry behavior** — Test: retryable error triggers retry with correct delays, permanent error fails immediately, max retries exhausted emits final error, stream interruption retries once, EventRetry events are emitted correctly. Use a mock langdag client that returns errors on demand.

**Failure modes:**
- Retry succeeds but response is different from what would have been → fine, LLM responses are non-deterministic anyway
- Rate limit persists beyond 3 retries → fail with clear error message including the rate limit details
- Stream retry after partial response → potential duplicate content. Mitigate by clearing any partial text collected before retry.

---

## Phase 4: Structured Sub-Agent Summaries

Replace the naive first-500-bytes summary with a model-generated summary that captures actual findings.

**Codebase context:**
- Current: `summarizeOutput()` in `subagent.go` takes first 500 bytes, cuts at line boundary
- Sub-agent output written to `.herm/agents/<id>.md` — full output always available
- Exploration model is available and cheap — already used for compaction
- `compact.go` has precedent for using the exploration model for summarization

- [ ] 4a: **Add `summarizeWithModel()` function** — New function in `subagent.go` that calls the exploration model with a short prompt: "Summarize the key findings from this sub-agent's output in 3-5 bullet points. Focus on facts, decisions, and actionable information. Skip preamble." Input: full sub-agent output (truncated to ~4000 chars to fit in a single cheap call). Output: structured summary. Falls back to `summarizeOutput()` (first 500 bytes) if the model call fails.

- [ ] 4b: **Use model summary in `formatSubAgentResult()`** — Replace the `summarizeOutput()` call with `summarizeWithModel()`. The tool result now contains an intelligent summary that the main agent can act on without reading the full output file. This costs ~0.001$ per sub-agent call (haiku-level pricing on ~1K tokens) but saves the main agent from reading the output file in most cases.

- [ ] 4c: **Add summary quality indicator** — Append `[summary: model]` or `[summary: truncated]` to the tool result so the main agent knows whether it got an intelligent summary or a fallback. If truncated, the agent should read the full file.

- [ ] 4d: **Test model summarization** — Test: model summary is concise and structured, fallback works when model call fails, very short outputs skip summarization (not worth the call), summary indicator is present.

**Open questions:**
- Should the summarization happen synchronously (adds ~1-2s latency per sub-agent completion) or asynchronously? Synchronous is simpler and the latency is acceptable since the parent agent is already waiting.
- Cost concern: with 5 parallel sub-agents, this adds ~5 cheap model calls. Still much cheaper than the main agent reading 5 full output files.

---

## Phase 5: Graceful Sub-Agent Error Reporting

When a sub-agent fails, tell the main agent why so it can adapt.

**Codebase context:**
- `subagent.go:219-222`: EventError is caught but only checked for "context canceled"
- Tool result on failure: `"(sub-agent produced no output)"` with no explanation
- Main agent has no way to know if the failure was: context limit, API error, tool failure, or empty output

- [ ] 5a: **Collect sub-agent errors** — In the event loop in `Execute()`, accumulate error messages from `EventError` events into an `errors []string` slice. Include error text and any available context (which tool was running, how many turns completed).

- [ ] 5b: **Include error context in tool result** — When the sub-agent finishes with errors, append an `[errors]` section to the tool result: `[errors: <error messages>]`. When the sub-agent produced no output AND had errors, return the errors as the result instead of the generic "(sub-agent produced no output)".

- [ ] 5c: **Add turn count to tool result** — Always include `[turns: N/M]` in the result (completed turns / max turns). If the sub-agent hit maxTurns, the main agent knows it was cut off and can resume it or spawn a new one with a narrower task.

- [ ] 5d: **Test error reporting** — Test: sub-agent with tool error shows error in result, sub-agent hitting maxTurns shows turns count, sub-agent with API failure shows error context, main agent receives actionable information.

---

## Phase 6: Integration Tests & Prompt Tuning

- [ ] 6a: **Update system prompt for new tools and behaviors** — Add outline tool guidance, retry behavior explanation (agent should not manually retry on API errors — the system handles it), and guidance for interpreting structured sub-agent results (check `[summary: truncated]` → read the output file).

- [ ] 6b: **End-to-end exploration test** — Simulate a "understand how X works" task and verify: agent uses outline before full reads, spawns sub-agents for parallel research, sub-agent display shows per-agent progress, results are structured, total token usage is lower than current approach.

- [ ] 6c: **Resilience test** — Simulate: API failure mid-conversation → retry succeeds → conversation continues. Sub-agent failure → main agent gets error context → adapts strategy. Multiple simultaneous failures → no deadlock, no silent stop.

---

## Out of Scope (Future Work)

- **Repository map (tree-sitter + PageRank):** The outline tool is a pragmatic step toward this. A full repo map would give ~1K token architectural awareness but requires tree-sitter binaries in the container and significant implementation effort.
- **LSP integration:** Go-to-definition, find-references would replace many grep+read cycles. High value but requires language server setup per project.
- **Streaming sub-agent output files:** Write output incrementally as the sub-agent runs, so the parent could read partial results. Adds complexity with minimal gain since sub-agents are fast (15 turns max).
- **Automatic task decomposition:** Main agent automatically breaks complex tasks into sub-tasks and spawns agents. Currently relies on the model's judgment, which works well enough.
- **Sub-agent model override per call:** Let the main agent choose a model per sub-agent (e.g., use the main model for a critical sub-task). Configurable but adds complexity.
- **Checkpoint/resume for main agent:** Save main agent state so the conversation can survive a crash. Sub-agents already have resume via agent_id; the main agent doesn't.
