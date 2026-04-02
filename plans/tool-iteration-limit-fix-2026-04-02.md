# Fix Tool Iteration Limit: Raise Default and Add Graceful Exhaustion

**Goal:** Fix two problems with the main agent's tool iteration cap: (1) the default of 25 is too low for real workloads involving sub-agents, and (2) when the limit is hit, the agent hard-stops without producing any useful output.

**Context:**
- The main agent's `runLoop()` (`agent.go:839`) iterates at most `maxToolIterations` (default 25) times. Each iteration is one round of: execute tool calls → call LLM.
- Real workloads easily exceed 25: spawning 2-3 sub-agents, polling their status, running bash commands, reading files. The screenshot shows 45 tool calls on a normal task.
- When `iteration >= maxIter`, the loop emits `EventError` and `EventDone`, and the agent stops dead. Sub-agents may still be running. The user sees a red error and nothing else happens — no synthesis, no summary, no output.
- Background sub-agents use `context.Background()` (not tied to parent), so they keep running after the parent dies. Their results are injected via `InjectBackgroundResult` → `drainBackgroundResults`, but those are only consumed inside the loop that just exited.

**Key files:**
- `cmd/herm/agent.go` — `runLoop()` (line 839), `defaultMaxToolIterations` (line 104), `drainBackgroundResults()` (line 322)
- `cmd/herm/subagent.go` — `bgAgentState` (line 35), `executeBackground()` (line 517), `lookupBgAgent()` (line 204)
- `cmd/herm/agentui.go` — agent creation (line 256), `EventDone` handler (line 503)
- `cmd/herm/config.go` — `MaxToolIterations` config field (line 29)

---

## Phase 1: Raise the default tool iteration limit

The current default of 25 is insufficient. A single sub-agent spawn + status polling cycle burns ~3 iterations (spawn tool call, sleep/wait, status check). With 2-3 sub-agents plus normal tool usage, 25 is easily exhausted.

- [x] 1a: Raise `defaultMaxToolIterations` from 25 to 200 in `agent.go`. Update the config comment in `config.go` to reflect the new default
- [ ] 1b: Update any tests that hard-code 25 as the expected default or use it in assertions

## Phase 2: Add a method to wait for background sub-agents

When the main agent exhausts its iteration budget, background sub-agents may still be running. We need a way to wait for them so their results can be included in a final synthesis.

- [ ] 2a: Add a `WaitForBackgroundAgents(timeout time.Duration) []string` method to `SubAgentTool` that blocks until all entries in `bgAgents` are done (or timeout), returning their results. Use a polling approach: check `state.done` for each agent, sleep briefly, repeat. Return results in completion order
- [ ] 2b: Add a `WaitForBackgroundAgents` method to the `Tool` interface or use a type assertion in `runLoop` to access the sub-agent tool. Since only the agent tool supports this, a type assertion on the `Tool` interface (checking for an optional `BackgroundWaiter` interface) is cleanest
- [ ] 2c: Add tests for `WaitForBackgroundAgents`: returns immediately when no background agents exist, waits and returns results when agents are running, respects timeout

## Phase 3: Graceful exhaustion — final LLM call with accumulated context

When the iteration limit is hit and tool calls remain, instead of hard-stopping, the agent should:
1. Wait for any running background sub-agents (with a timeout)
2. Collect their results
3. Make one final LLM call that includes the accumulated results and instructs the model to synthesize a response from what it has

This replaces the current "emit error and die" behavior with a structured wind-down.

**Edge cases:**
- The final LLM call must NOT trigger another tool loop — it should be a single response-only call
- If waiting for sub-agents times out, include partial results (what's done) and note which agents timed out
- The iteration warning in `systemPromptWithStats` already tells the LLM it's running low; the final call is the backstop when that warning wasn't enough

- [ ] 3a: In `runLoop()`, after the main loop exits with `iteration >= maxIter && len(toolCalls) > 0`, add a "graceful exhaustion" block: look up the agent tool via type assertion, call `WaitForBackgroundAgents` with a reasonable timeout (e.g., 2 minutes), collect results
- [ ] 3b: Build a final user message that includes: (1) any pending background results, (2) a system instruction telling the model to synthesize a final response from accumulated context without requesting more tools. Make one final `PromptFrom` call with this message
- [ ] 3c: Emit the text response from the final LLM call as `EventText` events so it appears in the UI. Replace the hard `EventError` with an `EventInfo`-level notice (or emit the error before the final call so the user sees the limit was hit, followed by the synthesis)
- [ ] 3d: Add tests: verify that when max iterations is hit with running background agents, the agent waits for them and makes a final LLM call; verify the final call's response is emitted as text

## Phase 4: Integration test

- [ ] 4a: Add an end-to-end test simulating a realistic scenario: main agent spawns background sub-agents, hits iteration limit, waits for sub-agents, and produces a synthesized response instead of hard-stopping
