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

---

## Phase 5: Anchor sub-agent group in the message flow

**Problem:** The sub-agent display is a floating overlay rendered after all `messages` in `buildBlockRows()`. When the main agent produces text before spawning background sub-agents, that text commits to `messages`. Then the main agent produces more text after spawning (e.g. "Three agents are out scouting...") — this also commits to `messages`. The sub-agent group renders below ALL messages, so the pre-spawn and post-spawn text both appear above the sub-agent section. This breaks chronological order.

**Root cause:** `subAgents` is a `map[string]*subAgentDisplay` with no positional anchor in the message history. `buildBlockRows()` renders: all messages → sub-agent block → streaming text. There's no concept of "this sub-agent group belongs between message[i] and message[i+1]."

**Fix:** Add a new message kind `msgSubAgentGroup` that acts as a positional anchor. When the first `EventSubAgentStart` fires, insert a `msgSubAgentGroup` message into `a.messages`. During rendering in `buildBlockRows()`, when encountering a `msgSubAgentGroup` message, render the live sub-agent display inline at that position. Remove the separate floating sub-agent block at the bottom. This way, any text the main agent produces after spawning sub-agents naturally flows below the group.

**Session restore:** Loading a previous session must reproduce the same display. The group doesn't need to be a new langdag node type — background sub-agent spawns are already persisted as "agent" tool_call + tool_result node pairs. During `rebuildChatMessages()` (`tree.go:218`), detect "agent" tool calls with `background: true` in their input JSON, and insert a `msgSubAgentGroup` marker at that position. Reconstruct `subAgentDisplay` entries from the tool result text (which contains agent_id, task, timing). Alternatively, use `Node.Metadata` (`types.go:98`, currently unused `json.RawMessage`) to persist sub-agent group display state (task labels, elapsed times, token counts, success/failure) on the tool_result nodes — this is more robust than parsing result text.

**Design note:** The group concept doesn't need to be materialized in langdag as a new node type. The existing "agent" tool_call nodes already carry the information. If grouping across tool calls becomes awkward, a group ID could be added to nodes (similar to `OutputGroupID` on assistant nodes, `types.go:90`) to tie agent tool calls together, but start without it — adjacency-based detection in `rebuildChatMessages` should suffice since background agent tool calls are typically consecutive.

- [x] 5a: Add `msgSubAgentGroup` to the `chatMsgKind` enum in `main.go`
- [x] 5b: In `agentui.go` `EventSubAgentStart` handler, before creating the sub-agent display entry, check if a `msgSubAgentGroup` marker already exists in `a.messages`. If not, insert one. Use a flag (`a.subAgentGroupInserted bool`) to avoid scanning messages every time — set it on first insertion, reset it when sub-agents are cleared
- [x] 5c: In `buildBlockRows()` (`render.go`), add a case for `msgSubAgentGroup` in the messages loop that calls `a.subAgentDisplayLines()` and renders them inline. Remove the floating sub-agent block that currently renders after all messages (the `if subLines := a.subAgentDisplayLines()` block at line ~409)
- [x] 5d: In `rebuildChatMessages()` (`tree.go`), detect "agent" tool calls with `background: true` in their input. When found, insert a `msgSubAgentGroup` marker and reconstruct `subAgentDisplay` entries from the corresponding tool_result content (agent_id, task, elapsed, tokens, success/failure). Store reconstructed entries in `a.subAgents` so the group renders on session load
- [x] 5e: Add test: set up an App with messages [user, assistant("launching agents"), subAgentGroup marker, assistant("results are in")], with populated subAgents. Call `buildBlockRows()` and verify: "launching agents" appears before sub-agent lines, sub-agent lines appear before "results are in"
- [x] 5f: Add test: verify that when no sub-agents exist (marker present but subAgents map empty), the marker renders nothing (no blank group header)
- [x] 5g: Add test: simulate session restore — build a node ancestry with "agent" tool calls (background: true), call `rebuildChatMessages()`, verify the output contains a `msgSubAgentGroup` marker at the correct position

## Phase 6: Suppress main agent chattiness while sub-agents run

**Problem:** After spawning background sub-agents, the main agent's LLM call returns immediately (the tool result is just "[agent_id: X] Sub-agent started..."). The LLM then gets called again and often produces chatty status text ("Three agents are out scouting...", "Agent 1 is back. Waiting on the other two...") which clutters the display. The sub-agent group display already shows live status — additional main agent commentary is redundant noise.

**Fix approach:** Two complementary changes:

1. **Prompt-level:** In `backgroundCompletion()` (`agent.go:388-395`), the system message that injects background results already tells the model to "incorporate results and continue." Add similar guidance to the tool result returned by `executeBackground()` instructing the model not to produce status updates while waiting — the TUI already shows live sub-agent progress.

2. **Commit suppression during background wait:** When `backgroundCompletion()` is active (the main agent emitted end_turn, we're now waiting for background agents), the main agent is idle — it's not producing text. The chattiness actually comes from the LLM's response *before* end_turn (the same turn that spawned the agents). The fix is prompt-level: tell the model to be concise after spawning background agents.

- [x] 6a: Update the tool result string in `executeBackground()` (`subagent.go:~653`). Current: `"[agent_id: %s] Sub-agent started in background. Task: %s. You will be notified when it completes."`. Add: `"Do not narrate progress — the user sees live sub-agent status in the UI. Move on to your next action or stop."`
- [x] 6b: Add test: verify the updated tool result string contains the suppression guidance

## Phase 7: Improve explore sub-agent efficiency

**Problem:** Explore sub-agents dive too deep too quickly — reading entire large files, exploring every subdirectory, etc. The sub-agent system prompt (`role_subagent.md` + `practices.md` + `tools.md`) has generic "explore in layers" guidance but nothing specific about being token-efficient or using a progressive-depth strategy.

**Fix:** Add exploration strategy guidance to `role_subagent.md` that applies specifically to explore-mode sub-agents (which have read-only tools). The guidance should teach progressive depth: scan structure first (glob, outline), search for patterns (grep), then read only the relevant sections of relevant files. Never read an entire large file when offset/limit or outline would suffice.

- [x] 7a: In `role_subagent.md`, add an explore-specific strategy section (conditionally rendered when the agent lacks edit/write tools). Include: (1) start from the project snapshot — it already has the top-level structure and recent commits, don't re-explore what's given; (2) use glob to map directory structure before reading anything; (3) use outline to see function/type signatures before reading implementations; (4) use grep to find specific patterns rather than reading files sequentially; (5) when reading files, use offset/limit to read only the relevant section — never read a large file fully; (6) stop exploring when you have enough to answer the question — don't be exhaustive when a focused answer suffices
- [x] 7b: Add test: verify that `buildSubAgentSystemPrompt()` with explore-mode tools (no edit/write) includes the exploration strategy text, and that implement-mode tools (with edit/write) do NOT include it

## Phase 8: End-to-end integration tests for phases 5-7

- [x] 8a: Add test for anchored sub-agent group: simulate a full event sequence (assistant text → tool calls spawning agents → EventSubAgentStart → more assistant text → EventSubAgentStatus done), verify that `buildBlockRows()` produces rows in correct chronological order: pre-spawn text, sub-agent group, post-spawn text
- [x] 8b: Add test for explore prompt: build a sub-agent system prompt in explore mode, verify it contains progressive-depth guidance keywords ("outline", "offset/limit", "don't read entire")

---

## Phase 9: Suppress background agent tool call/result display

**Problem:** When the main agent spawns background sub-agents, the TUI displays `~ agent` tool call boxes and `~ result` boxes for each spawn. These are redundant noise — the sub-agent group display already shows all active/completed agents with richer information (task, status, timer, tokens). The session restore path (`tree.go:236,266`) already skips these for background agents, but the live event path does not.

**Fix:** In the `EventToolCallStart` handler (`agentui.go:391`), add `isBackgroundAgentInput()` to the existing suppression check alongside `isAgentStatusCheck()` and `isSleepWaitCommand()`. This uses the existing `suppressedToolIDs` mechanism — the tool ID is tracked so the matching `EventToolResult` is also suppressed automatically. The `isBackgroundAgentInput()` helper already exists in `tree.go:596`.

**Note:** Only suppress when `toolName == "agent"` AND input has `background: true`. Foreground agent tool calls (`background: false` or absent) should still show `~ agent` since they block the main agent and have no group display.

- [x] 9a: In `EventToolCallStart` handler (`agentui.go:391`), add `isBackgroundAgentCall(event.ToolName, event.ToolInput)` to the suppression condition. Write a small helper `isBackgroundAgentCall(toolName string, input json.RawMessage) bool` that returns `toolName == "agent" && isBackgroundAgentInput(input)`. This keeps the suppression line readable. Both the `~ agent` tool call and the matching `~ result` will be suppressed via the existing `suppressedToolIDs` mechanism
- [x] 9b: Add test: simulate `EventToolCallStart` with `toolName="agent"` and `input={"task":"test","mode":"explore","background":true}`, followed by `EventToolResult` for the same tool ID. Verify neither produces a `msgToolCall` or `msgToolResult` in `a.messages`. Also verify that a foreground agent call (`background` absent) still produces the tool call message
- [x] 9c: Add test for session restore consistency: verify that `rebuildChatMessages()` also does not produce `msgToolCall` / `msgToolResult` for background agent tool_use blocks (this should already pass since `tree.go` already handles it, but the test documents the invariant)

## Phase 10: Visual retry of failed sub-agents

**Problem:** When a sub-agent fails (✗), the main agent may retry the task by spawning a new sub-agent. Currently this creates a separate display entry — the failed agent stays as ✗ and the retry appears as a new spinner. This is confusing: the user sees both the failure and the retry as unrelated entries.

**Root cause:** There is no link between a failed agent and its retry. The main agent calls the tool with a new task string, producing a fresh `agentID`. The `subAgentDisplay` struct has no concept of "retrying" or "replaced by".

**Fix:** Add a `retry_of` field to the agent tool input schema. When the main agent wants to retry a failed task, it passes `retry_of: "<failed_agent_id>"`. The system then:
1. Marks the old `subAgentDisplay` entry as replaced (hidden from the group — the retry supersedes it)
2. Copies the original task label to the new entry (preserving display continuity)
3. The new agent shows as a spinner in the same position the failed one occupied

This is a prompt+schema+display change. The LLM must be told to use `retry_of` when retrying a failed task — the tool result for failures already contains the agent_id.

- [x] 10a: Add `RetryOf string` field to `subAgentInput` struct (`subagent.go:143`). Add `"retry_of"` to the tool definition schema with description: `"Optional: ID of a previously failed sub-agent. The failed entry is replaced in the display by this new agent. Use when retrying a task that a previous agent failed."`
- [x] 10b: Add `replacedBy string` field to `subAgentDisplay` struct (`render.go:491`). In `EventSubAgentStart` handler (`agentui.go:549`), when the event has a `RetryOf` agent ID: (1) look up the old `subAgentDisplay`, set `replacedBy = newAgentID`, (2) copy the old agent's task label to the new entry if the new task is empty or similar
- [x] 10c: In `subAgentDisplayLines()` (`render.go:529`), skip agents where `replacedBy != ""` — they've been superseded. This removes the ✗ entry and the spinner for the retry takes its place
- [x] 10d: In `Execute()` / `executeBackground()` (`subagent.go`), pass `in.RetryOf` through the `EventSubAgentStart` event. Add `RetryOf string` to `AgentEvent` struct
- [x] 10e: Update the suppression guidance in `executeBackground()` tool result string to also mention: `"If an agent fails, you can retry by spawning a new agent with retry_of set to the failed agent's ID."`
- [x] 10f: Add test: create two sub-agent displays — one failed with ID "old", one running with `replacedBy=""` and the old one with `replacedBy="new"`. Call `subAgentDisplayLines()` and verify the failed agent is hidden and only the retry is shown
- [x] 10g: Add test: simulate `EventSubAgentStart` with `RetryOf="old-id"` where "old-id" exists in `subAgents` as a failed agent. Verify the old entry gets `replacedBy` set and the new entry inherits the task label
