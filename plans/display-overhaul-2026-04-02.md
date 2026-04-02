# Display Overhaul

**Goal:** Redesign the conversation display to be clean, grouped, and information-rich — matching the target mockup with grouped tool call blocks, structured sub-agent groups with per-agent metrics, and a richer status line.

**Context:**
- The current display renders each tool call as an individual bordered box paired with its result. This is verbose when the agent makes many tool calls in a row.
- Sub-agents are shown as flat `[agent] <task>: <status>` lines with no grouping by type, no tool count, no elapsed time, and no token counts.
- The status line shows funny text + elapsed + tokens, but no tool count and no spinner prefix.
- Assistant text is already rendered without prefix (good for copy/paste), but tool output within grouped blocks needs refinement.

**Key files involved:**
- `cmd/herm/render.go` — `buildBlockRows()` (main render pipeline), `subAgentDisplay`, `subAgentDisplayLines()`
- `cmd/herm/style.go` — `renderToolBox()`, `pastelColor()`, styling helpers
- `cmd/herm/agentui.go` — event handling for tool calls, sub-agents, agent lifecycle
- `cmd/herm/agent.go` — `AgentEvent` struct, event types
- `cmd/herm/content.go` — `toolCallSummary()`, `collapseToolResult()`
- `cmd/herm/main.go` — `chatMessage` struct, `App` struct fields
- `cmd/herm/subagent.go` — sub-agent execution, event forwarding

---

## Phase 1: Enrich sub-agent display state and events

Currently `subAgentDisplay` only tracks `task`, `status`, `done`. The target display needs tool count, elapsed time, tokens, mode, and failure state per sub-agent. The `AgentEvent` also lacks a mode field for sub-agent start events.

- [x] 1a: Add `mode` field to `AgentEvent` (populated on `EventSubAgentStart`) and pass `in.Mode` from `subagent.go` when forwarding the start event
- [x] 1b: Extend `subAgentDisplay` with: `mode string`, `toolCount int`, `startTime time.Time`, `inputTokens int`, `outputTokens int`, `failed bool`
- [x] 1c: Update event handling in `agentui.go` to populate the new fields — increment `toolCount` on `EventSubAgentStatus` when text starts with `"tool:"`, set `startTime` on `EventSubAgentStart`, capture tokens and failed state from the `EventSubAgentStatus` "done" event
- [x] 1d: Add tests for the enriched sub-agent display state transitions (start → tool increments → done with metrics)

## Phase 2: Grouped sub-agent display with per-agent metrics

Replace the flat `[agent] task: status` lines with a structured group display matching the target:

```
Running 3 Explore agents…
✓ Research conversation storage      | 41 🛠️ | 5.51s | ↑348 ↓169
✓ Research checkpoint-message linking | 37 🛠️ | 5.51s | ↑348 ↓169
⣾ Research client vs server state    | 35 🛠️ | 5.51s | ↑348 ↓169
```

- [x] 2a: Rewrite `subAgentDisplayLines()` to group active sub-agents by mode. Emit a header line per group ("Running N Explore agents…" / "Running N agents…"). For each agent, show: spinner/✓/✗ + task + `| N 🛠️ | Xs | ↑in ↓out` (hide metric sections that are zero)
- [x] 2b: Implement braille spinner animation (`⣾⣽⣻⢿⡿⣟⣯⣷`) on the per-agent spinner character, using the same `pastelColor()` cycle so it gets the rainbow effect. Green `✓` for done, red `✗` for failed
- [x] 2c: When a sub-agent completes, keep it visible in the group display (as ✓/✗) until the whole group finishes or the main agent emits text — don't immediately remove completed agents. Adjust cleanup logic in `EventDone` handler accordingly
- [x] 2d: Stop appending `[agent <id>] completed: <task>` as `msgInfo` chat messages — the completion is now shown inline in the group display. Only emit a `msgInfo` message if the agent failed (with error details)
- [x] 2e: Add tests for grouped display rendering: multiple agents of same mode, mixed modes, completion transitions, failure display

## Phase 3: Grouped tool call blocks for the main agent

Replace individual tool call boxes with a single bordered block grouping consecutive tool calls:

```
┌ Read file (README.md) ──────────────────────────────────────┐
├ Read file (foo.txt)
├ Read file (bar.txt)
├ ~ git log --oneline main --count
├ 32 tool calls… 🛠️
├ Edit file (foo.txt)
├ ~ git log --oneline main --count
│ d26138d 13a-f: add end-to-end error chain integration tests
│ 35b5d60 12d: mark cross-SDK divergence documentation complete
└─────────────────────────────────────────────────────────────┘
```

This requires a shift from the current per-tool-call message model to a grouping model during rendering.

- [x] 3a: In `buildBlockRows()`, collect consecutive `msgToolCall`/`msgToolResult` pairs into a "tool group" before rendering. A group breaks when a non-tool message (assistant text, info, etc.) is encountered
- [x] 3b: Create `renderToolGroup()` function in `style.go` that renders a grouped block: `┌ <first-tool-summary> ───┐` top border, `├ <tool-summary>` for middle entries, tool output shown with `│` prefix for result-bearing tools (edits, bash with output), `└───┘` bottom border
- [x] 3c: When a group has more than 6 tool calls, show first 3 + `├ N tool calls… 🛠️` + last 3 (with their outputs). This applies only to the tool call summaries — the actual output lines for the shown tools are still displayed
- [x] 3d: For tool output within grouped blocks: show diff output for edit tools (capped at the existing collapse limits), show bash output for the last tool in the group. Hide intermediate read/glob/grep results (the summary line is enough). Show error results always
- [ ] 3e: Handle in-progress tool calls within a group: the current tool (no result yet) appears as the last `├` entry with no bottom border (open group), live timer shown on the `├` line
- [ ] 3f: Add tests for tool group rendering: single tool, multi-tool grouping, overflow collapsing (first 3 + last 3), in-progress state, error result display, group breaking on text messages

## Phase 4: Status line with tool count and spinner

Update the main agent status line format from:
```
pondering the cosmos... 58.51s ↑348 ↓169
```
to:
```
⣾ pondering the cosmos... | 12 🛠️ | 58.51s | ↑348 ↓169
```

- [ ] 4a: Add `mainAgentToolCount int` field to `App`, increment on `EventToolResult` when the event comes from the main agent (check `event.AgentID == a.agent.ID()`)
- [ ] 4b: Update the running status line in `buildBlockRows()` to use pipe-separated format: `⣾ <funny-text> | N 🛠️ | Xs | ↑in ↓out`. Add braille spinner prefix with `pastelColor()`. Apply pastel color to the entire line
- [ ] 4c: Update the paused status line (approval mode) to match format: `⏸ <funny-text> | N 🛠️ | Xs | ↑in ↓out` (dim instead of pastel)
- [ ] 4d: Update the finished status line (after agent done) to match: `✓ N 🛠️ | Xs | ↑in ↓out` (dim, with green ✓)
- [ ] 4e: Reset `mainAgentToolCount` alongside other session state in `/new` command handler
- [ ] 4f: Add tests for status line rendering across all three states (running, paused, finished) with tool count display

## Phase 5: Polish and edge cases

- [ ] 5a: Ensure assistant text responses have no prefix or border — verify nothing in the rendering pipeline adds left-side decorations to `msgAssistant` content
- [ ] 5b: Verify that tool output displayed outside of grouped blocks (standalone results, sub-agent output quoted by the main agent) still renders correctly as individual boxes
- [ ] 5c: Test the full render pipeline end-to-end with a mock conversation that exercises: user message → tool group → assistant text → sub-agent group → tool group → assistant text → done status
- [ ] 5d: Verify braille spinner animation renders correctly at 20fps (50ms tick) — ensure the spinner frame index advances on each tick and wraps correctly through the 8-frame cycle

## Phase 6: Fix flaky sub-agent tests (event channel drops)

Three sub-agent tests are flaky — they pass in isolation but sometimes fail under the full suite. The root cause is `SubAgentTool.forward()` (`subagent.go:141-155`) which uses a non-blocking `select/default` send on the parent event channel. Under contention (many concurrent sub-agents or a full test suite), the channel buffer fills and **critical events** (`EventDone`, `EventError`) are silently dropped, leaving test assertions checking stale state.

Affected tests:
- `TestSubAgentMaxTurnsPartialOutputPreserved` — text collection stalls when event drain delays
- `TestSubAgentBackgroundFatalErrorSurfacing` — `EventError` dropped, error missing from result
- `TestSubAgentBackgroundCompletionInjection` / `TestE2ESubAgentErrorChain` — `EventDone` dropped, `bgAgentState.done` never set

- [ ] 6a: Make `forward()` blocking for critical event types (`EventSubAgentStatus` with `Text:"done"`, `EventUsage`) — these carry state that callers depend on (completion status, token counts). Keep non-blocking for high-frequency display events (`EventSubAgentDelta`, `EventSubAgentStatus` with tool/text updates) where dropping is acceptable. Add a short timeout (e.g. 5s) to the blocking send to prevent indefinite hangs if the parent is stuck
- [ ] 6b: Increase the parent event channel buffer in tests that spawn concurrent sub-agents — the current `make(chan AgentEvent, 64)` is too small when multiple agents each emit ~20 events. Use a buffer proportional to the number of agents (e.g. `agents * 64`)
- [ ] 6c: Add a `forwardBlocking()` helper alongside the existing `forward()` for the critical-event path, with a context-aware timeout and a clear error log when the send times out (replacing the silent `debugLog` drop)
- [ ] 6d: Verify all three previously-flaky tests pass reliably under `go test -count=20 -race` with the fixes applied
