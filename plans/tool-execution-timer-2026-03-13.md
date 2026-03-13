# Tool Execution Timer

## Context

Tool call boxes are rendered by `renderToolBox()` (main.go:759-842). The title is integrated into the top border as `┌ ~ glob ─────┐`. The bottom border is plain `└─────────────┘`. We want to mirror the title pattern on the bottom-right with execution duration:

```
┌ ~ $ go build ───────────────┐
main.go:12: undefined: foo
└──────────────────────── 1.2s ┘
```

Duration should only appear when execution takes >500ms. During execution, a live elapsed timer updates in place. On completion, the final duration is shown. When loading past sessions, stored durations are restored.

### Key files

- **main.go:759-842** — `renderToolBox()`: draws the box with borders and styled title
- **main.go:44-62** — `chatMessage` struct and `chatMsgKind` constants
- **main.go:1283-1340** — `buildBlockRows()`: pairs `msgToolCall`/`msgToolResult`, renders boxes
- **main.go:4001-4048** — `handleAgentEvent()`: maps agent events to chat messages
- **agent.go:95-141** — `AgentEvent` struct and event types
- **agent.go:470-569** — tool execution loop: emits `EventToolCallStart`, calls `tool.Execute()`, emits `EventToolCallDone`/`EventToolResult`
- **tree.go:216-264** — `rebuildChatMessages()`: rebuilds messages from langdag nodes for session replay

### Rendering pattern for bottom-right duration

Same ANSI isolation technique used for top-left title (main.go:806-821): switch out of `borderStyle` into `titleStyle` for the duration text, with spaces as separators. The duration uses dim+italic (or red+italic for errors) matching the title style.

### Live timer mechanism

Currently, in-progress tool calls render as an open box (bottom border stripped at main.go:1321). For the live timer: once 500ms elapses, show the bottom border with a running timer. A ticker goroutine fires re-renders during execution; the event channel already supports this pattern via `handleAgentEvent`.

### langdag persistence

`types.ContentBlock` needs a `DurationMs int` field to persist tool execution duration on `tool_result` blocks. The langdag team has been given instructions — this plan defers the persistence integration to Phase 4 (blocked on langdag v0.5.5+).

The existing `LatencyMs` field on `types.Node` tracks LLM call latency, not tool execution time — they're different things.

## Phase 1: Capture timing and plumb duration through events

Add timing around `tool.Execute()` in the agent loop and carry the duration through events to chat messages.

- [x] 1a: Add `Duration time.Duration` field to `AgentEvent` struct (agent.go). Set it on `EventToolCallDone` and `EventToolResult` by wrapping `tool.Execute()` with `time.Now()`/`time.Since()`
- [x] 1b: Add `duration time.Duration` field to `chatMessage` struct (main.go). In `handleAgentEvent` for `EventToolResult`, populate `duration` from `event.Duration`
- [x] 1c: Add `formatDuration(d time.Duration) string` helper — returns `""` for <500ms, `"620ms"` for <1s, `"1.2s"` for <1m, `"2m03s"` for ≥1m

## Phase 2: Render duration in the bottom border

Modify `renderToolBox` to accept and render an optional duration string in the bottom-right corner, using the same ANSI isolation pattern as the top-left title.

- [ ] 2a: Change `renderToolBox` signature to accept a duration string parameter (empty string = no duration shown). Render it right-aligned in the bottom border: `└──── 1.2s ┘` with `titleStyle` on the duration text
- [ ] 2b: Update all call sites of `renderToolBox` in `buildBlockRows()` — pass the formatted duration for completed tool pairs, empty string for standalone results
- [ ] 2c: Add/update tests for `renderToolBox` — verify bottom border rendering with and without duration, edge cases (duration wider than box, very narrow box)

## Phase 3: Live timer during execution

Show a live elapsed timer on in-progress tool boxes once 500ms has elapsed.

- [ ] 3a: Add `toolStartTime time.Time` and `toolTimer *time.Ticker` fields to App. On `EventToolCallStart`, set `toolStartTime` and start a ticker (~100ms). On `EventToolResult`, stop the ticker and clear both fields
- [ ] 3b: Route ticker events through the existing event channel to trigger re-renders. In `buildBlockRows`, for unpaired (in-progress) tool calls: if `time.Since(a.toolStartTime) >= 500ms`, render a full box (with bottom border + live duration) instead of an open box
- [ ] 3c: Verify the timer updates smoothly and stops cleanly on tool completion — no goroutine leaks, no flickering

## Phase 4: Session replay (blocked on langdag update)

Once langdag adds `DurationMs int` to `types.ContentBlock`, persist and restore durations.

- [ ] 4a: In `agent.go` tool execution loop, set `DurationMs: int(elapsed.Milliseconds())` on the `types.ContentBlock` tool_result entries appended to `toolResults`
- [ ] 4b: In `tree.go` `rebuildChatMessages()`, read `DurationMs` from tool_result ContentBlocks and populate `chatMessage.duration` for replayed sessions
- [ ] 4c: Verify loading a past session shows durations on tool boxes that had them

## Success Criteria

- Completed tool boxes show duration in bottom-right when execution took >500ms
- In-progress tool boxes show a live counting timer after 500ms of execution
- Timer stops and shows final duration when tool completes
- Duration uses dim+italic styling (red+italic for errors) matching the title
- `formatDuration` produces readable output: `620ms`, `1.2s`, `2m03s`
- No goroutine leaks from the ticker mechanism
- All existing tests pass; new tests cover duration rendering
- Past sessions show durations once langdag persistence is available (Phase 4)
