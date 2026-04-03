# Fix Event Channel Saturation and Dropped Critical Events

**Goal:** Prevent critical lifecycle events (`EventDone`, `EventSubAgentStatus "done"`) from being silently dropped when the event channel fills up during concurrent sub-agent execution, and make the `doneCh` backup path fully recover UI state.

**Context:**

Observed in `debug-20260403-141245.json`: user spawned 3 background explore sub-agents. All 3 completed their work (trace shows `main_agent_llm_calls: 3`, full summary produced). But the trace shows `sub_agents_spawned: 2` — only 2 of 3 `EventSubAgentStatus "done"` events were processed by the TUI. The third sub-agent (`c24d7942`, 13 turns, 3602 output tokens) never had its "done" event processed. The main agent's `EventDone` was also never processed (trace finalized from `cleanup()`, not from `EventDone` handler — evidenced by all phantom events having `ended_at` = session end time). User saw 2 persistent loading indicators until they quit at 110s.

**Root cause chain:**

1. Three concurrent sub-agents each stream text deltas via `forward()` (non-blocking). With 3 agents × ~13 turns × hundreds of text deltas, the 4096-slot event channel fills up during bursts.
2. `c24d7942`'s `forwardBlocking("done")` times out after 5 seconds because the channel stays near capacity — the TUI drains events, but non-blocking `forward()` calls from other streams immediately refill freed slots. The "done" event is silently dropped (`subagent.go:195`).
3. The main agent's `emit(EventDone)` is non-blocking (`agent.go:622-627`). With the channel still saturated, EventDone is dropped. `doneCh` closes as a backup signal.
4. `drainAgentEvents()` detects `doneCh` and sets `agentRunning = false` (`agentui.go:335`). But it does NOT stop the ticker, finalize the trace, commit streaming text, or update `agentNodeID` — all of those only happen in the `EventDone` handler (`agentui.go:513-547`).
5. Result: the ticker keeps firing, the sub-agent with the dropped "done" stays as a spinner, and the main agent timer keeps running. The TUI is stuck in the `else if hasActiveSubAgents()` drain loop.

**Key files:**
- `cmd/herm/agent.go` — `emit()` (line 620), `NewAgent` channel creation (line 292), `doneCh` (line 220)
- `cmd/herm/agentui.go` — `drainAgentEvents()` (line 302), `EventDone` handler (line 513), `EventSubAgentStatus` handler (line 585), `hasActiveSubAgents()` (line 292)
- `cmd/herm/subagent.go` — `forward()` (line 163), `forwardBlocking()` (line 183), `forwardBlockingTimeout` (line 157)
- `cmd/herm/agent_test.go` — `TestDoneChClosedUnderBackpressure` (line 2140), `TestIntegrationBackpressure` (line 2696)
- `cmd/herm/subagent_test.go` — existing sub-agent completion tests

---

## Phase 1: Make `doneCh` backup path fully recover UI state

The `doneCh` backup in `drainAgentEvents()` currently only sets `agentRunning = false`. When `EventDone` was dropped, all the work the `EventDone` handler does is skipped: ticker management, trace finalization, streaming text commit, elapsed time freeze, nodeID update. The backup path must perform the same state transitions.

**Approach:** Extract the state-transition logic from the `EventDone` handler into a helper (e.g., `finalizeAgentTurn(nodeID string)`) that both the `EventDone` handler and the `doneCh` backup path can call. The `EventDone` handler passes `event.NodeID`; the backup path passes empty string (nodeID is lost when EventDone is dropped, but losing nodeID is acceptable — the conversation can still continue by starting a new agent turn with the last known nodeID).

**Edge case:** The backup path may fire while sub-agents are still active (their "done" events are buffered or in-flight). The helper must check `hasActiveSubAgents()` before stopping the ticker, same as the current EventDone handler does.

- [x] 1a: Extract the state-transition logic from the `EventDone` handler (`agentui.go:513-547`) into a `finalizeAgentTurn(nodeID string)` method on `App`. Move: `agentRunning = false`, `cancelSent = false`, `traceUsageSeen = false`, ticker stop (with `hasActiveSubAgents` guard), `agentElapsed` freeze, `agentDisplayInTok`/`agentDisplayOutTok` freeze, `agentNodeID` update, streaming text commit, trace finalize+flush, render. The `EventDone` case calls `finalizeAgentTurn(event.NodeID)`
- [x] 1b: In the `doneCh` backup path inside `drainAgentEvents()` (`agentui.go:321-343`), after the final drain loop completes (the `default` at line 334), call `a.finalizeAgentTurn("")` instead of just setting `agentRunning = false`. Remove the bare `agentRunning = false` and `cancelSent = false` assignments — the helper handles them
- [x] 1c: Add test: create an `Agent` with a 1-slot events channel (forces `EventDone` to be dropped). Run the agent, let it complete. Verify that the `App` detects completion via `doneCh` backup and ends up in the correct state: `agentRunning == false`, `agentTicker == nil` (when no sub-agents), trace finalized, streaming text committed to messages

## Phase 2: Guaranteed delivery for `EventDone`

`emit()` (`agent.go:620`) uses a non-blocking send that drops `EventDone` when the channel is full. `doneCh` is a backup, but as Phase 1 shows, the backup path is lossy (no nodeID). A better approach: make `EventDone` use a blocking send with a timeout, similar to `forwardBlocking`. This gives the TUI time to drain the channel before giving up.

**Approach:** In `emit()`, when the event type is `EventDone`, use a `select` with a timeout (e.g., 5 seconds) instead of the non-blocking default. The `doneCh` close remains as the last-resort backup. This is consistent with how `forwardBlocking` treats "done" events for sub-agents — critical lifecycle events deserve blocking delivery semantics.

**Risk:** A blocked `emit()` delays `Run()` return and `close(a.events)`. This is acceptable — 5 seconds is bounded, and the alternative (dropped EventDone) is worse. The TUI's drain loop processes ~50 events per 50ms tick = 1000 events/second, so a 5-second window drains ~5000 events — more than enough to clear the 4096 buffer.

- [x] 2a: In `emit()` (`agent.go:620`), replace the unconditional non-blocking send with: if `e.Type == EventDone`, use `select { case a.events <- e: default: select { case a.events <- e: case <-time.After(5 * time.Second): debugLog(...) } }`. For all other event types, keep the existing non-blocking send. Extract `5 * time.Second` as a named constant `eventDoneDeliveryTimeout`
- [x] 2b: Add test: create an `Agent` with a small events buffer, fill it completely, emit `EventDone`. Verify the emit blocks briefly while a concurrent goroutine drains events, then `EventDone` is successfully delivered. Verify `doneCh` also closes

## Phase 3: Increase `forwardBlocking` timeout for "done" events

The 5-second `forwardBlockingTimeout` is too short when the channel is sustained at near-capacity with 3+ concurrent sub-agents. The TUI drains ~1000 events/second, so a full 4096 buffer takes ~4 seconds to drain — dangerously close to the 5-second timeout. A longer timeout for the critical "done" status event prevents silent loss.

**Approach:** Add a dedicated timeout constant for the "done" event specifically, longer than the general `forwardBlockingTimeout`. Use this longer timeout in the two places where `EventSubAgentStatus "done"` is sent via `forwardBlocking` (the `EventDone` handler in `runBackground` and the fallback path).

- [x] 3a: Add a `forwardBlockingDoneTimeout` constant (e.g., 30 seconds) in `subagent.go`. Add a `forwardBlockingWithTimeout(e AgentEvent, timeout time.Duration)` method that works like `forwardBlocking` but accepts a custom timeout
- [x] 3b: In `runBackground()`, change the two `forwardBlocking(AgentEvent{..., Text: "done", ...})` calls (the `EventDone` handler at line ~731 and the fallback at line ~819) to use `forwardBlockingWithTimeout(..., forwardBlockingDoneTimeout)`. Leave `EventUsage` forwards using the standard 5-second `forwardBlocking`
- [x] 3c: Add test: create a `SubAgentTool` with a 1-slot `parentEvents` channel, fill it. Call `forwardBlockingWithTimeout` with a "done" event and a 100ms test timeout. Verify it blocks for the timeout duration and logs the timeout. Then repeat with a concurrent drain — verify the event is delivered

## Phase 4: Reduce channel pressure from non-critical events

The core problem is that high-frequency `EventSubAgentDelta` events (text streaming snippets) compete with critical lifecycle events for channel space. Phase 11 of the previous plan already removed per-event `render()` calls, but the events still occupy channel buffer slots. Reducing the volume of these events at the source prevents channel saturation.

**Approach:** Rate-limit `EventSubAgentDelta` forwards in `runBackground()`. Instead of forwarding every text delta, batch them: accumulate text for up to 200ms, then forward a single combined delta. This reduces ~3000 events per sub-agent stream to ~150, dramatically lowering channel pressure. The 50ms render tick in the TUI already limits visible update rate to 20fps, so batching at 200ms loses nothing perceptible.

**Implementation:** In the `EventTextDelta` handler within `runBackground()`, accumulate text in a local buffer. Use a `time.Ticker` (200ms) or track `lastForwardTime` — when 200ms has elapsed since the last forward, send the accumulated text as one `EventSubAgentDelta` and reset. On `EventDone` or stream end, flush any remaining buffered text.

- [ ] 4a: In `runBackground()` (`subagent.go:663`), add delta batching state: a `strings.Builder` for accumulated text and a `time.Time` for last forward time. In the `EventTextDelta` case, append to the builder instead of immediately forwarding. Add a helper `flushDelta()` that forwards the accumulated text (if non-empty) and resets. Call `flushDelta()` when: (1) 200ms has elapsed since last forward, checked after each `EventTextDelta`; (2) on `EventDone`; (3) on loop exit (fallback path). Extract `200ms` as `deltaForwardInterval` constant
- [ ] 4b: Add test: simulate a sub-agent producing 100 rapid text deltas (no delay between them). With the 200ms batching, verify that far fewer than 100 `EventSubAgentDelta` events are forwarded to `parentEvents`. Verify the final accumulated text matches the sum of all deltas (no data loss)

## Phase 5: Tests for the complete saturation scenario

End-to-end test that reproduces the exact failure from the trace: 3 concurrent background sub-agents, channel near saturation, all critical events delivered and processed.

- [ ] 5a: Add integration test: create an `App` with a reduced-size events channel (e.g., 64 slots to force saturation). Spawn 3 background sub-agents via `SubAgentTool` using mock agents that each produce ~100 text deltas then complete. Verify: (1) all 3 sub-agents reach `sa.done == true` in the display, (2) the main agent's `EventDone` is processed (or recovered via `doneCh`), (3) `agentTicker` is stopped, (4) `hasActiveSubAgents()` returns false at the end

---

**Success criteria:**
- All 3 sub-agents show checkmarks (done) after completion, even under channel pressure
- Main agent timer stops when the run is complete
- No silent event drops for critical lifecycle events (EventDone, "done" status)
- The `doneCh` backup path produces the same end state as normal `EventDone` processing
- Channel saturation from text deltas is reduced by ~20x through batching

**Open questions:**
- Should the events channel buffer be increased beyond 4096? Increasing it is a simpler fix but doesn't solve the fundamental pressure problem. The batching approach (Phase 4) is more robust because it reduces event volume at the source rather than papering over it with a larger buffer. Consider both if batching alone isn't sufficient.
