# Fix Sub-Agent Display Bugs: Timer, Ordering, and Mode Persistence

**Goal:** Fix three bugs observed in the sub-agent display after the lifecycle fixes in the previous plan (`fix-subagent-lifecycle-and-display-2026-04-02.md`).

**Context:**

Observed in `debug-20260402-223330.json`: user spawned 3 background explore agents. Agents completed in 9.3s, 28.5s, and 77.7s respectively, but the display showed all three with identical elapsed times (~1236s) and the times kept ticking after completion. The main agent's response text appeared above the sub-agent status section, breaking chronological order. A fourth tool call omitted the `mode` field, producing a visible error box.

**Bugs:**

1. **Sub-agent timers never freeze on completion.** `formatSubAgentLine()` (`render.go:652-659`) has identical logic for done and not-done agents: both branches compute `elapsed = time.Since(sa.startTime)`. The `completedAt` field is correctly set in `EventSubAgentStatus` handler (`agentui.go:573`) but the rendering code ignores it. All completed agents show the same ever-increasing time instead of their actual durations.

2. **Main agent text renders above sub-agent display.** `buildBlockRows()` (`render.go:407-432`) renders: messages → streaming text → sub-agent lines → status line. When the main agent streams its response (after `backgroundCompletion`), the text appears above the sub-agent section, breaking chronological order. Thread additions should always flow top-to-bottom: the sub-agent display (which started earlier) must appear above the main agent's new streaming text. Note: interleaving across multiple rounds works naturally — committed messages stay above, sub-agent section sits in the middle, streaming text at the bottom. If the main agent later spawns more sub-agents, those join the same section grouped by mode, and new streaming text flows below them.

3. **Empty mode on agent resume causes error.** The LLM called `{"agent_id": "e6713a08", "task": "give me your summary"}` without a `mode` field. Mode validation (`subagent.go:364-366`) fails with `mode must be "explore" or "implement", got ""`. When resuming with `agent_id`, the sub-agent should keep its original mode — mode is an immutable property set at spawn time. To change mode, the user must launch a new sub-agent. Currently `agentNodes` only stores `agentID → nodeID` (a bare string map). It needs to also store the original mode so resumed agents inherit it.

**Key files:**
- `cmd/herm/render.go` — `formatSubAgentLine()` (line 633), `buildBlockRows()` (line 324), `subAgentDisplay` struct (line 488)
- `cmd/herm/subagent.go` — `Execute()` (line 349), mode validation (line 364), `agentNodes` map (line 71), tool definition schema (line 100)
- `cmd/herm/agentui.go` — `EventSubAgentStatus` handler (line 569)

---

## Phase 1: Fix sub-agent elapsed time to freeze on completion

The `formatSubAgentLine` function has a done/not-done branch (lines 652-659) where both branches are identical — a copy-paste bug. The done branch should use `sa.completedAt.Sub(sa.startTime)` to show the frozen elapsed time at the moment of completion.

- [x] 1a: In `formatSubAgentLine()` (`render.go:656`), change the done branch from `elapsed = time.Since(sa.startTime)` to `elapsed = sa.completedAt.Sub(sa.startTime)`. Add a guard: if `completedAt` is zero (shouldn't happen, but defensive), fall back to `time.Since(sa.startTime)`
- [x] 1b: Add test: create two `subAgentDisplay` entries with different `startTime` and `completedAt` values (both `done: true`), call `formatSubAgentLine()` on each, verify the elapsed times differ and match the expected `completedAt - startTime` durations

## Phase 2: Render sub-agent display above streaming text

In `buildBlockRows()`, swap the rendering order so sub-agent display lines appear before streaming text. New order: messages → sub-agent lines → streaming text → status line.

This preserves chronological ordering: committed messages (text before sub-agents) appear first, then sub-agent activity (which started during the previous response), then the main agent's current streaming text (generated after sub-agents complete). Multiple rounds work naturally — each time streaming text is committed to messages, it moves above the sub-agent section, and new streaming text flows below.

- [x] 2a: In `buildBlockRows()` (`render.go:407-432`), move the sub-agent display block (lines 427-432) to render before the streaming text block (lines 408-424). The status line section (lines 433-462) stays at the bottom unchanged
- [x] 2b: Add test: set up an `App` with both `streamingText` and populated `subAgents`, call `buildBlockRows()`, verify that sub-agent display lines appear before the streaming text lines in the output

## Phase 3: Persist mode in agent state and inherit on resume

When a sub-agent is spawned, store its mode alongside the nodeID. On resume (`agent_id` provided), use the stored mode — ignoring any mode the LLM provides. A resumed agent always continues in its original mode. To use a different mode, the LLM must spawn a new sub-agent.

Currently `agentNodes map[string]string` stores `agentID → nodeID`. Change it to store a struct that includes both nodeID and mode. On resume, look up the stored mode and use it unconditionally.

Also update the tool definition: remove `"mode"` from the `required` array (since it's not required for resume), and update the description to document that mode is ignored on resume.

- [x] 3a: Add an `agentNodeState` struct with `nodeID` and `mode` fields. Change `agentNodes` from `map[string]string` to `map[string]agentNodeState`. Update `saveNodeID()` to accept and store mode alongside nodeID. Update `loadNodeID()` to return the struct (or both values). Update all callers (both in `Execute` and `executeBackground`)
- [x] 3b: In `Execute()` (`subagent.go`), after loading the stored state for a resume (`in.AgentID != ""`), override `in.Mode` with the stored mode. If mode was stored as empty (shouldn't happen), fall back to `"explore"`. Move this logic before the mode validation so resumed agents never hit the mode-required error
- [x] 3c: Update the tool definition schema (`subagent.go:104-127`): remove `"mode"` from the `required` array (only `"task"` is required). Update the `mode` description to document: required for new agents, ignored on resume (original mode is preserved)
- [x] 3d: Add tests: (1) spawn a sub-agent in "implement" mode, resume it without providing mode → verify it runs with "implement" (inherited). (2) spawn in "explore" mode, resume with `mode: "implement"` → verify it still uses "explore" (original mode preserved). (3) new agent without agent_id and without mode → verify mode validation error still fires

## Phase 4: Integration test

- [x] 4a: Add an integration-style test that exercises the combined scenario: create a mock sub-agent display with agents that complete at different times, verify (1) elapsed times are frozen and different per agent, (2) sub-agent lines appear before streaming text in rendered output
