# Display Polish: Tool Grouping, Sub-Agent Ordering, Iteration Awareness & More

**Goal:** Clean up display issues and improve agent robustness: hide internal tool calls, fix empty lines, group consecutive tool calls into single blocks, stabilize sub-agent display ordering, and make agents aware of their tool iteration budget to avoid runaway loops.

**Context:**
- The previous display overhaul (`display-overhaul-2026-04-02.md`) established grouped tool blocks, sub-agent metrics, and an enriched status line. Several visual artifacts remain.
- The main agent uses the "agent" tool with `task: "status"` to poll background sub-agents, and uses bash `sleep` commands to wait. Both create visible but unhelpful UI blocks.
- Sub-agent display lines are appended *after* the main status line in `buildBlockRows()`, pushing the status line up from its expected bottom position.
- `strings.Split(content, "\n")` on content ending with `\n` creates trailing empty string elements that become blank lines inside rendered boxes.
- Sub-agent lines show `15 🛠️|` with no space before the pipe — the 🛠️ emoji needs a trailing space.
- Sub-agent token counts (`↑in ↓out`) only appear after completion because `EventUsage` events from sub-agents are not accumulated into `subAgentDisplay`. During execution, `inputTokens`/`outputTokens` stay 0 so the metrics section is hidden.

**Key files:**
- `cmd/herm/agentui.go` — event handling, creates `msgToolCall`/`msgToolResult` messages (lines 366-425)
- `cmd/herm/content.go` — `toolCallSummary()` (lines 194-244), `collapseToolResult()` (lines 305-322)
- `cmd/herm/render.go` — `buildBlockRows()` (lines 300-438), status line (401-429), sub-agent lines (431-436)
- `cmd/herm/style.go` — `renderToolGroup()` (line 416), `renderToolBox()` (line 258), content splitting in both

---

## Phase 1: Hide internal "agent" status-check tool calls

When the main agent polls a background sub-agent, it calls the "agent" tool with `{task: "status", agent_id: "..."}`. This creates a `msgToolCall` (showing `~ agent`) and a `msgToolResult` with the status response. Since the status-line and sub-agent display already show this info, these tool blocks are redundant noise.

**Approach:** Filter at the event-handling layer in `agentui.go`. When `EventToolCallStart` fires for tool name `"agent"` and the input contains `"task":"status"`, skip creating the `msgToolCall` message. Track this "suppressed" state so the corresponding `EventToolResult` is also skipped. This avoids polluting the message list with internal bookkeeping.

**Edge cases:**
- Actual agent spawns (`task != "status"`) must NOT be filtered — only status polls
- The trace collector should still receive these events for debugging (don't skip `traceCollector` calls)
- The tool timer should not start for suppressed tools

- [x] 1a: Add a `suppressedToolID` field (or set) to `App` to track tool calls that should be hidden from the UI
- [x] 1b: In `EventToolCallStart` handler, detect agent status checks (tool name `"agent"`, input has `"task":"status"`) and add the tool ID to the suppressed set instead of appending a `msgToolCall`. Still forward to `traceCollector`
- [x] 1c: In `EventToolResult` handler, check if the tool ID is in the suppressed set. If so, skip creating `msgToolResult` and remove from set. Still forward to `traceCollector` and count stats
- [x] 1d: Add tests for the suppression logic: agent status check is hidden, agent spawn is visible, trace collector still receives both

## Phase 2: Hide sleep/wait bash commands used for sub-agent polling

The main agent often calls `bash` with commands like `sleep 15 && echo "done waiting"` or `sleep 30 && echo "done"` to wait for sub-agents. These show up as visible tool blocks but provide no useful information since sub-agent progress is already shown in the sub-agent display.

**Approach:** Extend the suppression mechanism from Phase 1. Detect bash tool calls where the command matches a sleep-only pattern (e.g., starts with `sleep` and contains no meaningful work beyond echo). Use the same `suppressedToolID` set.

**Edge cases:**
- Only suppress pure sleep commands (e.g., `sleep N`, `sleep N && echo "..."`)
- Do NOT suppress bash commands that contain sleep as part of a larger pipeline
- Use a simple regex/heuristic: `^\s*sleep\s+\d+\s*(&&\s*echo\s+.*)?$`

- [x] 2a: Add an `isSleepWaitCommand(input json.RawMessage) bool` helper in `content.go` that parses the bash command and checks if it's a pure sleep/wait
- [x] 2b: Extend the suppression check in `EventToolCallStart` to also suppress bash sleep-wait commands
- [x] 2c: Add tests for sleep detection: pure sleep suppressed, sleep-in-pipeline not suppressed, non-sleep bash visible

## Phase 3: Move status line below sub-agent display

Currently in `buildBlockRows()` (render.go:401-436), the main agent status line is appended first (lines 401-429), then sub-agent display lines are appended after (lines 431-436). This means sub-agents push the status line up. The status line should always be the very last thing before the input area.

**Approach:** Swap the order: emit sub-agent display lines first, then the status line.

- [x] 3a: In `buildBlockRows()`, move the sub-agent display block (lines 431-436) to before the status line block (lines 401-429)
- [x] 3b: Add a test verifying that when both status line and sub-agent lines are present, the status line appears after the sub-agent lines in the output

## Phase 4: Fix empty lines in rendered blocks

Tool result content often ends with `\n`, and `strings.Split(content, "\n")` produces a trailing empty string that becomes a blank line inside bordered blocks. This affects `renderToolBox()`, `renderToolGroup()`, and `collapseToolResult()`.

**Approach:** Trim trailing newlines from content before splitting in the rendering functions. Apply consistently in all three locations.

**Locations to fix:**
- `style.go:416` — `renderToolGroup()`: `strings.Split(content, "\n")` for tool output
- `style.go:258` — `renderToolBox()`: `strings.Split(content, "\n")` for content lines
- `style.go:198` — `renderToolBox()`: `strings.Split(content, "\n")` for width calculation
- `content.go:306` — `collapseToolResult()`: `strings.Split(result, "\n")` — trailing empty element inflates line count

- [x] 4a: In `collapseToolResult()`, trim trailing newlines from `result` before splitting (`strings.TrimRight(result, "\n")`)
- [x] 4b: In `renderToolGroup()`, trim trailing newlines from `content` before the split at line 416
- [x] 4c: In `renderToolBox()`, trim trailing newlines from `content` at the start of the function (before both split locations at lines 198 and 258)
- [x] 4d: Add tests verifying that tool results ending with `\n` do not produce empty lines inside rendered blocks

## Phase 5: Fix sub-agent display line formatting

Two issues in `formatSubAgentLine()` (render.go:594-632):

1. **Missing space after 🛠️**: Line 609 uses `fmt.Sprintf("%d 🛠️", sa.toolCount)` — when joined with `" | "` the next pipe butts up against the emoji. The emoji occupies more visual width than its byte count suggests. Add a trailing space: `"%d 🛠️ "`.

2. **Missing live token counts**: The `EventUsage` handler in `agentui.go` (lines 427-455) accumulates tokens for the session and main agent, but does NOT accumulate into `subAgentDisplay`. Tokens are only set at "done" time (agentui.go:549). During execution, `sa.inputTokens`/`sa.outputTokens` stay 0, so the `↑in ↓out` section is hidden for running agents.

**Approach for tokens:** In the `EventUsage` handler, when `event.AgentID` doesn't match the main agent ID, look up the sub-agent display and accumulate `inputTokens`/`outputTokens` from the usage event. This gives live token counts during execution.

- [x] 5a: Fix missing space after 🛠️ in `formatSubAgentLine()` — change the format string to `"%d 🛠️ "` (trailing space)
- [x] 5b: In the `EventUsage` handler, when the event comes from a sub-agent (AgentID != main agent ID), find the corresponding `subAgentDisplay` via `getOrCreateSubAgent()` and accumulate `inputTokens += event.Usage.InputTokens` and `outputTokens += event.Usage.OutputTokens`
- [x] 5c: Add tests for: sub-agent line spacing with 🛠️, live token accumulation during sub-agent execution

## Phase 6: Integration tests

- [x] 6a: Add an end-to-end render test with a mock conversation that includes: agent status checks (should be hidden), sleep waits (should be hidden), active sub-agents with status line (status line at bottom), tool results with trailing newlines (no empty lines in blocks), and sub-agent lines with correct spacing and live token counts

## Phase 7: Group consecutive main-agent tool calls into a single block

When the main agent makes parallel tool calls (e.g., spawning 2 sub-agents), the events arrive as: `toolCall1, toolCall2, toolResult1, toolResult2`. The current `collectToolGroup()` (render.go:272-298) breaks the group when it encounters a `msgToolCall` not immediately followed by a `msgToolResult` (line 289-293). This causes each tool call to render as a separate bordered box instead of being grouped.

**Approach:** Modify `collectToolGroup()` to consume all consecutive `msgToolCall` messages first, then pair them with the following `msgToolResult` messages. The group continues as long as we see tool calls or tool results without a different message kind in between.

**Key file:** `cmd/herm/render.go` — `collectToolGroup()` (lines 272-298)

**Edge cases:**
- A trailing `msgToolCall` with no result (in-progress) should still be the last entry
- Mixed tool names (read + grep + agent) should still group if consecutive
- Results may not arrive in the same order as calls — pair by position (first result → first call) since tool IDs aren't tracked in messages

- [x] 7a: Rewrite `collectToolGroup()` to first collect all consecutive `msgToolCall` messages, then collect following `msgToolResult` messages, pairing them positionally. Mark `inProgress` only if there are more calls than results
- [x] 7b: Update existing `TestCollectToolGroup` and `TestBuildBlockRows_ToolGroup` tests to cover the parallel-calls pattern (multiple calls followed by multiple results)
- [x] 7c: Add a new test for the specific "2 agent spawn" pattern: `[msgToolCall(agent), msgToolCall(agent), msgToolResult(agent), msgToolResult(agent)]` verifying they produce 1 group with 2 entries

## Phase 8: Stable sub-agent display ordering

The `subAgentDisplayLines()` function (render.go:506-510) iterates over a `map[string]*subAgentDisplay`, which has random iteration order in Go. This causes sub-agent lines to jump around on every render tick.

**Desired order:** Completed agents first (sorted by completion time ascending), then running agents (sorted by start time ascending). This keeps the display stable — once an agent finishes it stays pinned at its position, and running agents maintain their relative start order.

**Key files:**
- `cmd/herm/render.go` — `subAgentDisplay` struct (line 463), `subAgentDisplayLines()` (line 497)
- `cmd/herm/agentui.go` — `EventSubAgentStatus` "done" handler (line 563)

- [x] 8a: Add a `completedAt time.Time` field to `subAgentDisplay` struct
- [x] 8b: In the `EventSubAgentStatus` "done" handler (agentui.go:563), set `sa.completedAt = time.Now()`
- [x] 8c: In `subAgentDisplayLines()`, after collecting visible agents into the `visible` slice, sort them: completed agents first (by `completedAt` ascending), then running agents (by `startTime` ascending). Use `sort.SliceStable` or `slices.SortFunc`
- [x] 8d: Add tests verifying stable ordering: 3 agents started at different times, one completes — verify completed one appears first, running ones maintain start-time order

## Phase 9: Avoid hitting max tool iterations

The main agent loop (agent.go:814-1061) has a `defaultMaxToolIterations = 25` limit. When reached, the agent emits an error and stops. The LLM has no awareness of this limit and can't plan accordingly. Sub-agents also don't inherit custom limits from the parent.

**Approach (two parts):**

1. **Inject remaining iterations into the system prompt** — Add a `RemainingToolIterations` field to `PromptData` (systemprompt.go) and include it in the system prompt template so the LLM can plan tool usage. Update it on each LLM call cycle in `runLoop()`.

2. **Pass tool iteration limits to sub-agents** — When creating sub-agents in `subagent.go`, pass `WithMaxToolIterations()` with a sensible fraction of the parent's remaining budget (or a fixed sub-agent cap).

**Key files:**
- `cmd/herm/agent.go` — `runLoop()` (line 814), `defaultMaxToolIterations` (line 104)
- `cmd/herm/systemprompt.go` — `PromptData` struct, `buildSystemPrompt()`
- `cmd/herm/subagent.go` — sub-agent creation (lines 315-319, 521-525)
- `prompts/system.md` — system prompt template

- [x] 9a: Add `RemainingToolIterations int` and `MaxToolIterations int` fields to `PromptData` in systemprompt.go
- [x] 9b: In `systemPromptWithStats()`, add iteration warning when below 30% remaining (adapted from template approach to fit dynamic per-turn injection pattern)
- [x] 9c: In `runLoop()` (agent.go), update `a.currentIteration` before each `buildPromptOpts()` call so `systemPromptWithStats()` can compute remaining iterations
- [ ] 9d: In `subagent.go`, when creating foreground and background sub-agents (lines 315-319, 521-525), pass `WithMaxToolIterations(subLimit)` where `subLimit` is capped at a sensible value (e.g., `min(parentRemaining, defaultSubAgentMaxTurns)`)
- [ ] 9e: Add tests: verify system prompt includes iteration warning when below threshold, verify sub-agents receive a max iterations option
