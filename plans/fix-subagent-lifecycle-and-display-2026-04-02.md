# Fix Sub-Agent Lifecycle: Background Completion and Display Persistence

**Goal:** Fix four interacting bugs that cause (1) the main agent to stop while background sub-agents are still running, (2) completed sub-agents to vanish from the display, and (3) background agent results to be silently lost.

**Context:**

Observed in `debug-20260402-182627.json`: user asked the main agent to spawn 3 background explore agents. Agent `c1d88937` completed quickly. The main agent polled status once, slept 10s, said "Still running. Let me wait a bit more." — then returned `end_turn` (no tool calls). `EventDone` fired, deleted the completed agent from the display. The two remaining agents kept running but their events were never processed. Their results were injected via `onBgComplete` → `InjectBackgroundResult` but `runLoop` had already returned. Session stalled for ~4 minutes until the user killed it.

**Bug chain:**
1. The LLM returned `end_turn` while background agents were running. The run loop has no guard against this — it only triggers graceful exhaustion when `iteration >= maxIter`, not on premature `end_turn`.
2. `EventDone` cleanup (`agentui.go:518-523`) deletes all `sa.done == true` sub-agents from the display map. This immediately removed the 3rd agent from the UI.
3. The main event loop (`main.go:401-414`) stops selecting on `agent.Events()` once `agentRunning` becomes false. Sub-agent events pile up in the buffer and are never drained.
4. `subAgentDisplayLines()` (`render.go:558-568`) returns nil when all sub-agents are done, hiding the final state instead of showing checkmarks/crosses.

**Key files:**
- `cmd/herm/agent.go` — `runLoop()` (line 879), `gracefulExhaustion()` (line 363), `InjectBackgroundResult()` (line 323), `BackgroundWaiter` interface (line 127)
- `cmd/herm/subagent.go` — `WaitForBackgroundAgents()` (line 237), `bgAgentState` (line 39), `forward()`/`forwardBlocking()` (lines 151-186)
- `cmd/herm/agentui.go` — `EventDone` handler (line 503), `drainAgentEvents()` (line 292), `EventSubAgentStatus` handler (line 561)
- `cmd/herm/main.go` — main event loop (line 376), headless loop (line 474)
- `cmd/herm/render.go` — `subAgentDisplayLines()` (line 526), `subAgentDisplay` struct (line 488), `allDone` check (line 558)

---

## Phase 1: Prevent run loop from exiting while background agents are pending

The run loop exits when `len(toolCalls) == 0` (LLM returned `end_turn`). Currently, graceful exhaustion only triggers when `iteration >= maxIter && len(toolCalls) > 0`. We need a second trigger: when the LLM chose to stop but background agents haven't reported back yet.

After the main for-loop exits and before `EventDone` is emitted, check if there are pending background agents. If so, wait for them (reusing the existing `WaitForBackgroundAgents` infrastructure), inject their results, and make one final LLM call so the model can incorporate the results.

**Key distinction from the existing graceful exhaustion path:** The existing path fires when the LLM is *still requesting tools* but the budget is exhausted. This new path fires when the LLM *chose to stop* but background work is outstanding. The mechanics are similar (wait → collect → final call) but the trigger condition and the synthesis prompt differ — the model needs to know that background agents just finished and it should incorporate their results, not that it ran out of iterations.

**Edge cases:**
- If no background agents exist, skip entirely — don't add latency to normal `end_turn`
- Need a method on `SubAgentTool` (or the `BackgroundWaiter` interface) to check whether pending background agents exist without blocking
- The final LLM call must include tools so the model can act on the results (unlike graceful exhaustion which strips tools). The model may need to spawn more work or use tools to complete the task.
- Cap the number of "wait-and-resume" cycles to avoid infinite loops if the model keeps spawning background agents and stopping

- [x] 1a: Add a `HasPendingBackgroundAgents() bool` method to `SubAgentTool` that checks if any `bgAgentState` has `done == false`. Add it to the `BackgroundWaiter` interface
- [x] 1b: In `runLoop()`, after the main for-loop exits (but before the existing graceful exhaustion check), add a "background completion" block: if the loop exited naturally (not due to iteration exhaustion) and `HasPendingBackgroundAgents()` returns true, wait for them using `WaitForBackgroundAgents` with the same timeout. Then inject the results and re-call the LLM **with tools enabled** so it can continue working. Guard against infinite re-entry with a counter (e.g., max 3 background-wait cycles)
- [x] 1c: Add tests: (1) run loop waits for pending background agents on `end_turn`, re-calls LLM with their results; (2) run loop skips the wait when no background agents exist; (3) background-wait cycle is capped to prevent infinite loops

## Phase 2: Keep completed sub-agents visible in the display

Two changes: stop deleting completed sub-agents from the display map on `EventDone`, and stop suppressing the display when all agents are done.

**Display lifecycle after fix:** sub-agents appear when started (spinner), update while running (tool count, elapsed time), and persist when done (checkmark or cross with final metrics). They remain visible until the user's *next* message triggers a new agent turn — at which point stale entries from the previous turn can be cleared.

- [x] 2a: Remove the cleanup loop in `EventDone` handler that deletes completed sub-agents from `a.subAgents`. Sub-agents persist in the display across turns; cleared only on `/clear`
- [x] 2b: Remove the `allDone` early-return in `subAgentDisplayLines()`. When all agents are done, the display still shows them (with checkmarks/crosses). Header changes from "Running N Explore agents…" to "N Explore agents" when all done
- [x] 2c: Update existing tests: `TestSubAgentGroupedDisplay/"all done returns nil"` changed to "all done shows completed agents" — expects output with checkmark and no "Running" header

## Phase 3: Continue draining sub-agent events after main agent stops

When `agentRunning` becomes false, the main event loop (`main.go:376-416`) stops selecting on `agent.Events()`, and `drainAgentEvents()` returns early. Background sub-agents that are still running emit events (tool status, completion) into the parent channel, but nobody reads them. The display freezes.

The fix: keep draining sub-agent events even after the main agent's turn ends, as long as sub-agents are still active. The main event loop's `else` branch (when `agentRunning == false`) should still select on `agent.Events()` if there are active sub-agents.

**Key consideration:** `drainAgentEvents()` currently gates on `a.agentRunning`. We need a separate condition: "are there still active (non-done) sub-agents?" This can check `a.subAgents` for any entry with `done == false`.

Also, the `agentTicker` is stopped on `EventDone`, which means the 50ms render cadence stops. Sub-agent spinner animations and elapsed time counters would freeze. The ticker should keep running as long as there are active sub-agents.

- [x] 3a: Add a helper `hasActiveSubAgents() bool` on `App` that checks if any entry in `a.subAgents` has `done == false`
- [x] 3b: Modify `drainAgentEvents()` to also drain when `hasActiveSubAgents()` returns true, even if `agentRunning` is false
- [x] 3c: In the main event loop's `else` branch, add `agent.Events()` to the select when `hasActiveSubAgents()` is true. Applied same fix to headless loop
- [x] 3d: In `EventDone`, don't stop `agentTicker` if `hasActiveSubAgents()`. Stop it when the last sub-agent completes in `EventSubAgentStatus` handler
- [x] 3e: Add tests: verify sub-agent events processed after main EventDone; verify ticker kept alive for active sub-agents and stopped on last completion

## Phase 4: Integration test — full background lifecycle

- [ ] 4a: Add an integration test that exercises the complete scenario from the bug report: main agent spawns 3 background sub-agents, one completes before the main agent stops, main agent returns `end_turn`, system waits for the remaining agents, re-calls LLM with all results, and produces a final response. Verify: (1) all 3 agents appear in the display, (2) the completed agent shows a checkmark, (3) the two running agents continue updating, (4) the final response incorporates all 3 agents' findings
