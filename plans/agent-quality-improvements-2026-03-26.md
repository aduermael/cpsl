# Agent Quality Improvements

**Goal:** Address all findings from the trace analysis review — fix trace collector bugs, restructure the system prompt for clarity and efficiency, polish tool descriptions, add environment manifests from devenv, and introduce background sub-agent execution. Each phase is independent unless noted.

**Prior work:** JSON trace system landed in phases 1–4 (`plans/archive/json-trace-debug-2026-03-25.md`). Sub-agent system in `plans/archive/sub-agents-2026-03-09.md` and `plans/archive/smarter-subagent-delegation-2026-03-16.md`. System prompt restructured in `plans/archive/lazy-context-and-role-simplification-2026-03-22.md`.

**Codebase context:**
- `cmd/herm/trace.go` — TraceCollector: `ensureTurn` (line ~490) returns existing turn instead of finalizing; `SetUsage` (line ~279) sets stop_reason based on accumulated tool calls; `FinalizeTurn` (line ~455) only called from `EventDone` handler
- `cmd/herm/agentui.go` — `handleAgentEvent()` (line ~334): bridges agent events to trace collector; `EventDone` (line ~463) is the only place `FinalizeTurn` is called for main agent
- `cmd/herm/agent.go` — `runLoop()` (line ~733): LLM call → emitUsage → tool loop → PromptFrom → emitUsage → repeat; `emitUsage()` (line ~364) emits EventUsage after each LLM call
- `cmd/herm/subagent.go` — `Execute()` (line ~182): blocks until sub-agent finishes; event draining (line ~253) already finalizes turns on EventTextDelta when usageSeen; tool allowlists (line ~148)
- `cmd/herm/debuglog.go` — `initAppDebugLog()` (line ~39): creates TraceCollector at workspace init, before first user message
- `cmd/herm/tools.go` — BashTool (line ~20): timeout max 600s documented but not enforced in code
- `cmd/herm/filetools.go` — `readFileDefaultLimit = 500` (line ~342) but schema says "default: 2000" (line ~325)
- `cmd/herm/tooldesc.go` — `loadToolDescriptions()` (line ~28): loads from `prompts/tools/*.md` with YAML frontmatter
- `cmd/herm/systemprompt.go` — `buildSystemPrompt()` (line ~45): assembles PromptData, executes "system" template
- `prompts/system.md` — template chain: role → tools → practices → communication → personality → skills → environment
- `prompts/role.md` — 5-step task flow, sandbox explanation, project orientation
- `prompts/practices.md` — duplicates several task flow items
- `prompts/communication.md` — response style guidelines
- `prompts/personality.md` — optional custom personality
- `prompts/environment.md` — date, container image, project snapshot
- `prompts/tools/*.md` — per-tool descriptions; `devenv.md` is 81 lines (~650 tokens), largest by far

---

**Parallel Phases: 1, 2, 3**

## Phase 1: Fix Trace Collector Bugs

The trace currently merges multiple LLM calls into a single `llm_response` event because `FinalizeTurn` is only called at `EventDone`. This loses per-call boundaries, corrupts `stop_reason`, and makes timing analysis impossible.

**Design:** Introduce turn finalization between LLM calls. The sub-agent code (`subagent.go` ~line 263) already does this correctly — when new text arrives after usage was seen, it finalizes the previous turn. Apply the same pattern in `agentui.go`.

The signal chain: each LLM call ends with `EventUsage` → tools execute → next LLM call starts with `EventTextDelta` or `EventToolCallStart`. When we see text/tool-start after usage was recorded, finalize the previous turn before starting a new one.

Also fix `started_at` to reflect first user interaction rather than TraceCollector creation time.

- [x] 1a: Add turn boundary detection in `handleAgentEvent` — when `EventTextDelta` or `EventToolCallStart` arrives and the current turn already has usage set, call `FinalizeTurn` before processing the new event. Mirror the pattern already used in `subagent.go`. This automatically fixes `stop_reason` since each turn's tool_calls are isolated.
- [x] 1b: Fix `started_at` — defer setting `TraceInfo.StartedAt` until `AddUserMessage` is first called (or add a `first_message_at` field). Update `DurationMS` calculation to use the correct start time.
- [x] 1c: Update trace tests — verify that a 3-LLM-call sequence produces 3 separate `llm_response` events with correct per-call usage, tool_calls, and stop_reason. Test that the final turn has `stop_reason: "end_turn"` when no tool calls follow.

## Phase 2: System Prompt Restructure

The current prompt has environment buried at the bottom, redundancy between task flow and practices, no priority guidance for conflicting instructions, and underspecified personality.

**Design:** Reorder sections so the agent has grounding context first. Deduplicate the task flow and practices. Add a priority stack. Improve personality to behavioral language. Add missing guidance for secrets, destructive actions, and failure recovery.

- [x] 2a: Reorder `system.md` template chain to: environment → role → tools → practices → communication → personality → skills. The agent needs to know where it is before reading how to behave.
- [x] 2b: Deduplicate `practices.md` — remove items already covered by the task flow in `role.md`: "break complex tasks into steps" (= step 3), "verify your work" (= step 5). Keep items that add genuinely new constraints: "read before writing", "don't refactor unrelated code", "API errors retried automatically".
- [x] 2c: Revise the 5-step task flow in `role.md` — fold environment setup into step 1 as a conditional ("if tools are missing, use devenv first"). Add failure recovery to verify step ("if verification fails after two attempts, explain and ask"). Add preamble: "For simple questions or small edits, act directly."
- [x] 2d: Add a priority stack to `role.md` or `practices.md` — when instructions conflict: (1) Don't break things — verify before and after changes, (2) Do what was asked, nothing more, (3) Keep changes minimal, (4) Keep communication brief.
- [x] 2e: Improve `personality.md` template — the personality is user-configured and injected verbatim (e.g., "Concise, a bit grumpy yet very helpful"). The model may interpret creative words too literally. Frame it as a user preference: "The user has requested the following personality/tone: {{.Personality}}. Interpret this as communication style guidance — be helpful and accurate first, personality second." This keeps the user's words intact but adds guardrails.
- [x] 2f: Add missing guidance — secrets handling ("never echo, log, or commit secrets; use them in-place"), destructive action safety valve ("if a requested action is destructive and irreversible, confirm with the user"), large file handling ("for large files, read only the relevant section").

## Phase 3: Tool Description Polish

Several tool descriptions have factual errors, unclear guidance, or are heavier than needed.

- [x] 3a: Fix `read_file` schema — change limit description from "default: 2000" to "default: 500" to match `readFileDefaultLimit` in `filetools.go`.
- [x] 3b: Refine bash tool guidance — replace blanket "Do NOT use bash for file operations" with: "Prefer dedicated tools for reading, searching, and editing files. Use bash for commands without a dedicated tool equivalent (mkdir, mv, cp, chmod, running builds and tests)."
- [x] 3c: Strengthen `edit_file` description — move "Always read_file before editing" to the first sentence. Currently buried at the end.
- [x] 3d: Improve `agent.md` description — rename display to "sub-agent" in the brief description (e.g., "Spawn a sub-agent with its own context window"). Add note about parallel edit risk: "When spawning multiple implement-mode sub-agents, ensure they work on separate files — parallel edits to the same file will conflict. Partition work by file or directory."
- [x] 3e: Enforce bash timeout cap — the description says "max: 600" but code doesn't enforce it. Add `if in.Timeout > 600 { in.Timeout = 600 }` in BashTool.Execute.
- [x] 3f: Add "trust documented environment" to practices — "Trust the documented environment capabilities. Don't verify tool or runtime presence when the system prompt or environment manifest confirms it."

## Phase 4: DevEnv Environment Manifest

Currently the devenv tool description is ~650 tokens (35% of all tool descriptions) because it includes Dockerfile examples, installation patterns, and common mistakes. Most conversations never touch devenv, wasting tokens. Additionally, the agent sometimes wastes tool calls checking what's installed (e.g., `which python3`) because the system prompt only vaguely describes the base image.

**Design:** After `devenv build` succeeds, generate `.herm/environment.md` — a machine-readable manifest of what's in the container (installed runtimes with versions, system tools, key paths). Inject this into the system prompt's Environment section. Trim the devenv tool description to essentials (workflow rules, base image requirement) and move the Dockerfile examples and common mistakes into the output of `devenv read`.

The manifest should be generated by running detection commands in the newly built container (e.g., `go version`, `node --version`, `python3 --version`). The devenv tool's `read` action should also regenerate the manifest if it's stale or missing.

- [x] 4a: Define environment manifest format — `.herm/environment.md` with sections: base image tag, installed runtimes (name + version), system tools, custom packages. Keep it compact (target <20 lines) so it's cheap to inject.
- [x] 4b: Implement manifest generation — after successful `devenv build`, exec detection commands in the new container to discover installed runtimes and tools. Write results to `.herm/environment.md`. Also generate on `devenv read` if the manifest is missing or the Dockerfile has changed since last generation.
- [x] 4c: Inject manifest into system prompt — in `environment.md` template, if `.herm/environment.md` exists, include its contents under a "Container environment" heading. Read the file in `fetchProjectSnapshot()` or `buildSystemPrompt()`.
- [x] 4d: Trim devenv tool description — keep in `devenv.md`: mandatory workflow (read → write → build), base image requirement, ONE environment rule, and "run `devenv read` for full Dockerfile guidance". Move the installation examples, RUN layer hygiene, and common mistakes into the string returned by the `read` action (append to the Dockerfile content shown to the agent).
- [x] 4e: Update devenv tool's `read` output — when action is "read", return the current Dockerfile contents followed by a "## Dockerfile Guidelines" section containing the installation examples and common mistakes currently in `devenv.md`. This way the agent gets the detailed guidance exactly when it needs it (about to edit the Dockerfile) and not on every request.

## Phase 5: Background Sub-Agents

Currently sub-agents block the parent until completion. For long-running tasks (large explorations, multi-step implementations), this prevents the parent from doing other work. The user wants a background mode where sub-agents run asynchronously and the CLI schedules periodic status checks.

**Design:** Add a `background: boolean` parameter to the agent tool. When true:
1. `Execute()` starts the sub-agent goroutine but returns immediately with a handle: `"[agent_id: <id>] Sub-agent started in background. Task: <task>. You will be notified when it completes."`
2. The sub-agent runs to completion, collecting events into its trace as usual.
3. When the sub-agent finishes, the CLI surfaces the result by injecting it into the conversation at the next natural breakpoint (after the current tool batch completes, or between LLM calls). This works similarly to how EventSubAgentStatus is already forwarded to the parent.
4. The parent can also explicitly check via `agent(agent_id: "<id>", task: "status")` which returns current progress or final result.

**CLI scheduling:** The CLI doesn't need a polling loop. The sub-agent's goroutine sends `EventSubAgentStatus{Text: "done", SubTrace: ...}` when finished (already implemented). The bridge in `agentui.go` receives this asynchronously. The change is in how this event is surfaced: instead of being part of a blocking tool result, it becomes an injected message in the conversation.

- [x] 5a: Add `background` parameter to agent tool schema and input parsing. When true, `Execute()` launches the sub-agent goroutine and returns immediately with the agent_id and a brief status message.
- [x] 5b: Background sub-agent lifecycle — store running background agents in a map on `SubAgentTool` (agent_id → goroutine channel). The goroutine collects events as usual. When done, it stores the final result and signals completion.
- [x] 5c: Result surfacing — when a background sub-agent completes, forward the completion event to the parent's event channel. In `agentui.go`, handle this as an injected tool result that gets included in the next LLM call's context. Design the injection so it appears as a natural "background agent completed" message.
- [x] 5d: Status checking — when `agent(agent_id: "<id>", task: "status")` is called, return the current state: "running" with turns/tools so far, or "completed" with the full result. Reuse the existing resume infrastructure (`agentNodes` map).
- [x] 5e: Update `agent.md` tool description — document background mode: when to use it (long-running explorations, parallel independent tasks), how results are delivered (automatic notification on completion), how to check status.
- [x] 5f: Tests — verify background execution returns immediately, result surfacing works, status checking returns correct state for running and completed agents.

## Phase 6: Trace Improvements (Nice-to-Have)

Additional trace quality improvements that emerged from the review.

- [x] 6a: Add `parallel_group` field to `TraceToolCall` — tool calls returned in the same LLM response share a group ID, distinguishing intentional parallelism from coincidental timing. Set the group ID from the LLM response's tool_use blocks.
- [x] 6b: Add system prompt hash to `TraceInfo` — SHA256 of the system prompt text for quick comparison across traces without diffing kilobytes. Keep the full text too for reproducibility.

---

**Success criteria:**
- Trace: a 3-LLM-call run produces 3 separate `llm_response` events with correct per-call usage and stop_reason
- Prompt: Environment section appears first; no duplicated instructions between task flow and practices
- Tools: `read_file` schema matches code default; devenv description is <200 tokens; environment manifest exists after build
- Background agents: `agent(task: "...", mode: "explore", background: true)` returns immediately; result surfaces when done
- Overall: the same hello-world trace shows 2 LLM calls instead of 3 (no wasted `which python3`)
