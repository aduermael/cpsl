# Prompt and Trace Improvements

**Goal:** Address all findings from the second trace analysis review — fix the trace turn-attribution bug, trim token waste from system prompt and tool descriptions, add missing grep capability, and improve trace accuracy with real API metadata.

**Prior work:** Previous plan (`plans/agent-quality-improvements-2026-03-26.md`, all 6 phases complete) added the `traceUsageSeen` turn-boundary pattern, restructured the system prompt order, trimmed devenv descriptions, added environment manifests, background sub-agents, and `parallel_group`/system prompt hash to traces.

**Codebase context:**
- `cmd/herm/trace.go` — `finalizeTurnLocked` (line ~509) infers `stop_reason` from whether tool calls are attached; `SetUsage` (line ~288) records usage but doesn't finalize; `ensureTurn` (line ~494) creates turns; `StartToolCall` (line ~322) attaches tool calls to current turn
- `cmd/herm/agentui.go` — `handleAgentEvent` (line ~335): `traceUsageSeen` flag triggers `FinalizeTurn` on both `EventTextDelta` (line ~341) and `EventToolCallStart` (line ~371), which splits same-call usage and tool calls across trace events
- `cmd/herm/agent.go` — `runLoop` (line ~759): event order per LLM call is `EventTextDelta*` (from `drainStream`) → `EventUsage` (from `emitUsage`) → `EventToolCallStart*` (from tool loop); `drainStream` (line ~702) reads stream chunks, has access to `chunk.Done` but discards stop_reason
- `cmd/herm/filetools.go` — `grepInput` struct (line ~205) has no `CaseInsensitive` field; `Execute` (line ~217) builds rg command without `-i` option; schema (line ~175) has no `case_insensitive` property
- `cmd/herm/systemprompt.go` — `PromptData` (line ~18) has `ContainerEnv` for conditional devenv rendering; `buildSystemPrompt` (line ~51) assembles prompt
- `cmd/herm/background.go` — `projectSnapshot` (line ~115): `RecentCommits` uses `git log --oneline -20`
- `prompts/role.md` — contains "Do not ask for permission. Act freely." (line ~5) alongside priority stack that says "Don't break things" (line ~18); devenv paragraph (line ~8) always included
- `prompts/practices.md` — "Read before writing" (line ~6) duplicates role.md step 1; "Keep changes minimal" (line ~7) duplicates role.md step 3
- `prompts/tools/*.md` — "Runs inside the dev container." repeated in 7 tool descriptions; "Do NOT use bash for X" repeated in 5 tool descriptions; `git.md` is 42 lines with procedural workflows
- `prompts/environment.md` — "Uncommitted changes: no uncommitted changes" rendered even when clean (line ~37)

---

## Phase 1: Fix Trace Turn-Attribution Bug

The `traceUsageSeen` pattern in `agentui.go` triggers `FinalizeTurn` on `EventToolCallStart`, but tool call starts from the SAME LLM call arrive AFTER `EventUsage` (because `emitUsage` runs before the tool loop in `runLoop`). This splits a single LLM call's usage and tool calls across two trace events, creating a phantom "end_turn" event with no tools.

**Root cause:** Within one LLM call, event order is: `EventTextDelta*` → `EventUsage` → `EventToolCallStart*`. The turn boundary between calls is: `...EventToolCallStart*` → (tool execution) → `EventTextDelta*` or `EventUsage` (next call). So `EventToolCallStart` is NOT a turn boundary signal — it belongs to the same call as the preceding `EventUsage`.

**Fix:** Remove the `traceUsageSeen` check from `EventToolCallStart`. Add it to `EventUsage` instead (if `traceUsageSeen` is already true when a new `EventUsage` arrives, finalize the previous turn). Keep the check on `EventTextDelta` (text from a new call arrives before its usage). This ensures tool calls attach to the same turn as their originating LLM call.

- [x] 1a: Move turn finalization trigger — in `handleAgentEvent`, remove the `traceUsageSeen` check from `EventToolCallStart` (lines ~371-374). Add a `traceUsageSeen` check to `EventUsage` (lines ~421-445): if true, finalize previous turn before setting new usage. Keep the existing check on `EventTextDelta` (lines ~341-343).
- [x] 1b: Update trace tests — verify a 2-LLM-call run (write_file then bash) produces exactly 2 `llm_response` events, not 3. First event should have `stop_reason: "tool_use"` with the tool call attached. Verify the phantom 0-tool "end_turn" event no longer appears.

## Phase 2: Resolve "Act Freely" vs "Confirm Destructive" Contradiction

`role.md` says "Do not ask for permission. Act freely." while `practices.md` says "If a requested action is destructive and irreversible, confirm with the user." The priority stack doesn't mention the destructive-action rule. A model may weigh "act freely" against the guardrail.

- [ ] 2a: Remove "Do not ask for permission. Act freely." from `role.md` line ~5. The sandbox framing ("You have full control — run any commands, modify any files. Nothing affects the host.") already conveys freedom without creating a competing directive.
- [ ] 2b: Expand the priority stack in `role.md` line ~18 — add destructive-action rule as item 2: "(1) Don't break things — verify before and after changes. (2) Confirm with the user before destructive, irreversible actions. (3) Do what was asked, nothing more. (4) Keep changes minimal. (5) Keep communication brief." Format as a numbered list instead of inline bold.

## Phase 3: Deduplicate Tool Description Boilerplate

"Runs inside the dev container." appears in 7 tool descriptions (~48 tokens). "Do NOT use bash for X" appears in 5 descriptions (~70 tokens). The bash description and system prompt already cover routing. Total waste: ~120 tokens per request.

- [ ] 3a: Remove "Runs inside the dev container." opener from `glob.md`, `grep.md`, `read_file.md`, `edit_file.md`, `write_file.md`, `outline.md`. Keep it only in `bash.md` (where the image name and mount point are useful context). Add one line to `prompts/tools.md`: "All tools except git run inside the dev container."
- [ ] 3b: Remove "Do NOT use bash for X" from `glob.md`, `grep.md`, `read_file.md`, `edit_file.md`, `write_file.md`. The bash description already says "Prefer dedicated tools for reading, searching, and editing files."
- [ ] 3c: Update tests that assert on tool description content, if any.

## Phase 4: Trim Git Tool Description

`git.md` is 42 lines — the longest tool description after devenv. Procedural workflows (merge conflicts, commit messages, exploration examples) belong in the system prompt, not the tool definition sent on every call.

- [ ] 4a: Create a `prompts/content/git_practices.md` embedded file containing the merge conflict workflow, commit message style guide, and exploration examples currently in `git.md` lines 22-41.
- [ ] 4b: Trim `git.md` to essentials (~12 lines): what it does, where it runs, allowed subcommands, credential requirement for remote ops, push requires approval, never force-push. Remove procedural sections.
- [ ] 4c: Add git practices to system prompt — in `prompts/tools.md`, conditionally include the git practices content when `HasGit` is true via template reference or inline. This ensures the guidance is in the prompt once (not per-tool-definition) and only when git is available.

## Phase 5: Add Case-Insensitive Search to Grep

The grep tool has no `-i` flag. Case-insensitive search is common (searching for "TODO", "error" regardless of casing). Without it, the model must construct fragile regexes like `[Ee]rror` or fall back to bash with `rg -i`, defeating the dedicated tool.

- [ ] 5a: Add `case_insensitive` boolean to grep — add property to JSON schema in `Definition()`, add `CaseInsensitive bool` field to `grepInput` struct, pass `-i` to the rg command in `Execute()` when true.
- [ ] 5b: Update grep tool description in `grep.md` — mention the case_insensitive option briefly.

## Phase 6: Deduplicate Practices and Role Workflow

`practices.md` duplicates items from `role.md`'s workflow and `bash.md`'s description. Each duplicate is ~10-15 tokens.

- [ ] 6a: Remove "Read before writing — understand existing code, patterns, and conventions first." from `practices.md` — this is step 1 of the workflow in `role.md`.
- [ ] 6b: Remove "Keep changes minimal and focused. Don't refactor unrelated code or over-engineer." from `practices.md` — this is step 3 of the workflow ("Implement — make focused, minimal changes") plus priority item 4 ("Keep changes minimal").
- [ ] 6c: Remove "Run tests after changes." from `bash.md` — this is step 4 of the workflow ("Verify — run tests or the build to confirm changes work").
- [ ] 6d: Add failure-reporting guidance to `communication.md` — "When reporting failures, identify the root cause from the error output and state your next step. Don't paste the full error — the user already sees it."

## Phase 7: Pass Real stop_reason and Fix API Timing

`finalizeTurnLocked` infers `stop_reason` from whether tool calls are attached. This is fragile — it produced a wrong "end_turn" in the phantom event bug. The actual stop_reason from the API stream should be used. Also, `StartedAt` on a turn is set when `ensureTurn` creates the turn object, not when the API call actually starts.

- [ ] 7a: Capture stop_reason from API stream — in `drainStream`, the `chunk.Done` branch (line ~746) returns but discards any stop_reason from the API. Check if `langdag.PromptResult` or the Done chunk carries the stop_reason. If so, return it from `drainStream` and propagate through `EventUsage` or a new event field. If the langdag client doesn't expose it, add a `StopReason` field to the chunk/result type.
- [ ] 7b: Propagate stop_reason to trace — add a `StopReason` field to `AgentEvent` (or to `EventUsage`). In `SetUsage` on the trace collector, store the real stop_reason on the turn. Remove the inference logic in `finalizeTurnLocked` (lines ~514-520) and use the API-provided value.
- [ ] 7c: Fix turn timing — emit a `StartLLMResponse` call from `runLoop` at the beginning of each `retryableStream` invocation (before the stream starts). This sets the turn's `StartedAt` to the actual API call start time, not whenever the first event happens to create the turn.

## Phase 8: Trim Project Context

The project snapshot includes 20 recent commits and always renders "Uncommitted changes: no uncommitted changes" even when clean. The devenv paragraph in `role.md` is always included even when the container is already configured.

- [ ] 8a: Reduce recent commits from 20 to 10 — in `background.go`, change `git log --oneline -20` to `git log --oneline -10`. 10 commits is sufficient for orientation; commits beyond that are rarely useful.
- [ ] 8b: Skip "Uncommitted changes" line when clean — in `environment.md`, remove the else branch (lines ~37-38) that renders "no uncommitted changes". Only render the "Uncommitted changes:" section when `.GitStatus` is non-empty.
- [ ] 8c: Conditionalize devenv paragraph — in `role.md`, wrap the "The container starts from a minimal base image..." paragraph (line ~8) in `{{if not .ContainerEnv}}...{{end}}`. When a container environment manifest exists, the container is already configured and this paragraph wastes ~50 tokens.

---

**Success criteria:**
- Trace: a write_file→bash run produces exactly 2 `llm_response` events with correct per-call usage, tool calls, and stop_reason (no phantom first event)
- Prompt: "Act freely" removed; priority stack expanded; no duplicate instructions between role workflow and practices
- Tools: "Runs inside the dev container" and "Do NOT use bash" removed from individual tool descriptions; git description is ~12 lines; grep supports `case_insensitive`
- Trace metadata: real `stop_reason` from API; accurate `started_at` timing per turn
- Project context: 10 commits (not 20); no "no uncommitted changes" line; devenv paragraph conditional on container env
