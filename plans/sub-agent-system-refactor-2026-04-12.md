# Sub-Agent System Refactor: Modes, Communication, and Efficiency

**Goal:** Refactor the sub-agent orchestration system for clarity, efficiency, and maintainability. Rename modes, eliminate code duplication, optimize token usage in communication, and streamline context bootstrapping and budget reminders.

**Builds on:** `agent-budget-consciousness-2026-04-03.md` (all 8 phases completed — budget injection, graceful synthesis, prompt guidance, centralized constants, and user-message-based reminders are all in place).

**Context:**

The sub-agent system works well but has accumulated technical debt:
- Two modes ("explore" and "implement") where "implement" should be "general" since it handles all tasks, not just code writing
- ~140 lines of duplicated drain loop logic between foreground `Execute` and `runBackground` in `subagent.go`
- Output summarization thresholds are too aggressive (500 bytes triggers model summarization even for short results)
- Every sub-agent spawn fetches a fresh project snapshot (tree, git log, git status) even when multiple agents spawn in rapid succession
- Explore-mode agents receive git status and commit history they cannot act on (read-only tools)
- Budget reminders for sub-agents include main-agent-only fields (session stats, context window %) that are always empty/zero
- Two nearly identical synthesis prompts exist in `subagent.go` and `agent.go`

**Reference:** Claude Code's approach (in `notes/claude-code/`) was studied for comparison. Key takeaways incorporated: Explore agents should skip unnecessary context (CC skips CLAUDE.md and git status for Explore/Plan agents); output should flow back efficiently; mode system should be extensible without over-engineering.

**Key files:**
- `cmd/herm/subagent.go` — SubAgentTool, mode validation, tool filtering, drain loops, synthesis, summarization
- `cmd/herm/agent.go` — Agent struct, budgetReminderBlock, turnBudgetLine, gracefulExhaustion, clearOldToolResults
- `cmd/herm/systemprompt.go` — buildSubAgentSystemPrompt, PromptData
- `cmd/herm/config.go` — SubAgentMaxTurns, ProjectConfig
- `cmd/herm/tooldesc.go` — tool description placeholder replacement
- `cmd/herm/compact.go` — compact system, summarization
- `cmd/herm/background.go` — fetchProjectSnapshot
- `prompts/tools/agent.md` — agent tool description
- `prompts/role_subagent.md` — sub-agent role prompt
- `prompts/environment.md` — environment template with project snapshot

**Constraints:**
- No new module dependencies
- Prefer refactoring over patches — the codebase must remain simple to work with
- DRY: eliminate code duplication
- Keep the mode system minimal (only "explore" and "general" for now)

**Success criteria:**
- `"implement"` mode is renamed to `"general"` with no residual references
- Foreground and background drain loops share a single implementation
- Sub-agent output under 2KB passes through without model summarization
- Explore-mode agents skip git status, saving tokens proportional to repo state
- Rapid sequential spawns share a single cached project snapshot (10s TTL)
- Budget reminders for sub-agents are lean (turn budget only, no empty session/context fields)
- All existing tests pass (updated for the rename); new tests cover the refactored drain loop
- Synthesis prompts are unified and produce structured output

---

## Phase 1: Mode Constants and Rename

Introduce named constants for modes and rename "implement" → "general". This is foundation work that everything else builds on.

Currently modes are raw string literals scattered across ~8 files. The rename is safe because mode strings are only used in-flight (tool call JSON, transient `agentNodeState` for resume within a session) — no on-disk persistence, no migration needed.

- [x] 1a: Add `ModeExplore` and `ModeGeneral` constants to `subagent.go`. Replace all raw string comparisons (`"explore"`, `"implement"`) with constants in Go source files: `subagent.go`, `agent.go`, `render.go`. Update the JSON schema enum in the tool Definition from `["explore", "implement"]` to `["explore", "general"]`
- [x] 1b: Update prompt files — `prompts/tools/agent.md` mode descriptions and usage guidance to reference "general" instead of "implement". Update `render.go` `modeOrder` slice
- [x] 1c: Update all test files (`subagent_test.go`, `render_test.go`, `systemprompt_test.go`) to use the new mode name and constants

## Phase 2: DRY — Extract Shared Drain Loop

The foreground `Execute` (lines ~516-654) and background `runBackground` (lines ~778-906) in `subagent.go` contain ~140 lines of nearly identical event drain logic: turn counting, two-stage enforcement, event-type switching, the `doneCh` fallback drain, and post-loop synthesis. The only differences are delta forwarding strategy (immediate vs batched) and result storage (return vs bgAgentState).

Extract a shared `drainSubAgentEvents` method that both call.

- [x] 2a: Define a `drainResult` struct (textParts, agentErrors, token counts, turns, lastNodeID) and a `drainOptions` struct (agentID, mode, agent, traceCollector, deltaForwarder callback). The `deltaForwarder` callback parameterizes the one behavioral difference: foreground passes a function that calls `t.forward()` directly, background passes one that accumulates into a batch buffer and flushes periodically
- [x] 2b: Extract the shared drain loop into `(t *SubAgentTool) drainSubAgentEvents(ctx, opts) drainResult`. Include the `doneCh` fallback drain. Both the main select loop and the doneCh drain use the same event processing — avoid duplicating it internally (extract an `processEvent` helper if needed)
- [x] 2c: Refactor `Execute` to: setup → `drainSubAgentEvents` → post-drain synthesis → `buildResult` → return. Refactor `runBackground` similarly: setup → `drainSubAgentEvents` → post-drain synthesis → store in bgAgentState → notify. Verify both paths produce identical behavior to before
- [x] 2d: Tests — verify foreground and background drain produce the same results for identical event sequences. Test the delta batching callback is called correctly for background mode. Existing lifecycle tests must still pass

## Phase 3: Tool Filtering and Per-Mode Turn Budgets

Improve the tool filtering architecture and make turn budgets mode-aware. These are both mode-dependent concerns and fit together.

**Tool filtering:** Replace the standalone `exploreToolAllowlist` map with a mode-keyed `modeToolAllowlists` map. A nil allowlist (for "general") means all tools pass through. This is extensible without over-engineering — adding a future mode is one map entry.

**Per-mode budgets:** Explore agents are cheaper (cheap model, read-only tools) and should default to fewer turns. Implement/general agents are expensive (full model, all tools) and keep the current default. Currently `tooldesc.go` computes `explorationTurns = maxTurns*3/4` as a heuristic — replace this with first-class per-mode config.

- [x] 3a: Replace `exploreToolAllowlist` with `modeToolAllowlists map[string]map[string]bool`. General mode entry is nil (all tools). Simplify `buildSubAgentTools` to do a single map lookup. The nested agent depth check remains separate
- [x] 3b: Add `ExploreMaxTurns` and `GeneralMaxTurns` fields to `Config` and `ProjectConfig` (with `json:"...,omitempty"` for backward compat). Keep `SubAgentMaxTurns` as a legacy fallback that applies to both when the new fields are zero. Add corresponding constants `defaultExploreMaxTurns = 15` and `defaultGeneralMaxTurns = 20`
- [x] 3c: In `Execute`, after determining the model based on mode, also resolve `maxTurns` based on mode (explore → exploreMaxTurns, general → generalMaxTurns). Pass the resolved value to the agent and drain loop
- [x] 3d: Update `tooldesc.go` — replace the `__EXPLORATION_TURNS__` heuristic with `__EXPLORE_MAX_TURNS__` and `__GENERAL_MAX_TURNS__` placeholders populated from actual config values. Update `prompts/tools/agent.md` to reference per-mode budgets
- [x] 3e: Tests — verify tool filtering per mode, verify per-mode turn budget resolution, verify config override precedence (per-mode > legacy SubAgentMaxTurns > default)

## Phase 4: Communication Efficiency — Output and Result Format

Optimize how sub-agent output flows back to the main agent. Current thresholds are too aggressive, and the result format wastes tokens on information the main agent cannot act on.

- [x] 4a: Raise `subAgentSummaryBytes` from 500 to 2000. Outputs under 2KB (~25-30 lines) pass through verbatim — this eliminates unnecessary model summarization calls for most simple sub-agent results. Raise `summarizeWithModelMaxChars` from 4000 to 8000 so the summarizer sees enough context for accurate bullets
- [x] 4b: Replace the summary prompt ("3-5 bullet points") with a structured schema: `STATUS: success|partial|failure`, `FILES: <key files>`, `FINDINGS: <bullets>`, `NEXT: <recommendation or "none">`. This gives the main agent machine-parseable structure while keeping content human-readable
- [ ] 4c: Compact the result metadata header in `formatSubAgentResult`. Remove `[tokens: input=N output=M]` (tracked via EventUsage, not actionable by the main agent). Shorten to `[agent:<id> turns:<n/m> summary:<method>]`. Document the output file path pattern in `agent.md` instead of repeating it per result (~50 tokens saved per sub-agent call)
- [ ] 4d: Add guidance to `prompts/tools/agent.md` for when to read full output: "If you need exact file paths, line numbers, code snippets, or error details not in the summary, read the full output at `.herm/agents/<agent_id>.md`"
- [ ] 4e: Tests — verify outputs under 2KB are not summarized, verify structured summary format, verify compact result header format

## Phase 5: Context Bootstrap Optimization

Sub-agents should get only the context they need, and rapid sequential spawns should share a single snapshot.

**Snapshot caching:** `fetchProjectSnapshot` runs 3 shell commands (tree, git log, git status) per spawn. When the main agent spawns 3 explore agents in quick succession, this runs 9 commands in milliseconds against an unchanged repo. Add a 10s TTL cache on `SubAgentTool`.

**Explore-mode context stripping:** Explore agents are read-only — they cannot modify files, so git status (uncommitted changes) is less actionable. Skip it for explore mode, matching Claude Code's approach. Keep the project tree (useful for orientation) and recent commits (useful for understanding recent changes).

- [ ] 5a: Add snapshot cache fields to `SubAgentTool` (`snapMu sync.Mutex`, `snapCache *projectSnapshot`, `snapTime time.Time`). Add `cachedSnapshot() projectSnapshot` method with `snapshotCacheTTL = 10 * time.Second`. Replace direct `fetchProjectSnapshot` calls in both foreground and background paths
- [ ] 5b: For explore-mode sub-agents, zero the `GitStatus` field on the snapshot before passing it to `buildSubAgentSystemPrompt`. This is simpler than adding template conditionals — just clear the field. Token savings: 50-500 tokens per explore spawn depending on repo state
- [ ] 5c: Tests — verify snapshot is reused within TTL window, verify fresh snapshot after TTL expires, verify explore-mode sub-agents don't receive git status in their system prompt, verify general-mode sub-agents still get full context

## Phase 6: Budget Reminder and Synthesis Refinements

Streamline what sub-agents see in budget reminders and unify the synthesis prompts.

**Budget reminders:** Sub-agents currently get session stats (always "0 tokens, 0 agent calls" since the TUI only updates main agent stats), context window utilization (always empty since sub-agents are constructed with `contextWindow: 0`), and iteration warnings (near-duplicate of turn budget since `maxToolIterations = maxTurns + 3`). For sub-agents, only the turn budget line matters.

**Synthesis prompts:** Two nearly identical prompts exist — `subAgentSynthesisPrompt` in `subagent.go` and the inline string in `gracefulExhaustion` in `agent.go`. Unify them with a shared function that produces structured output.

**Turn budget messages:** The late/final tier messages are verbose (~170 chars each). Compact them since the role prompt already contains the full "wrap up" instructions.

- [ ] 6a: In `budgetReminderBlock`, add an early path for sub-agents (when `maxTurns > 0` and `contextWindow == 0`): emit only the turn budget line wrapped in `<system-reminder>`, skipping session stats, context window, and iteration warnings. This eliminates 3 dead/redundant lines per sub-agent turn
- [ ] 6b: Compact `turnBudgetLine` messages. Keep the default tier as-is. Mid: `"Budget: Turn %d/%d — past halfway, narrow focus."` Late: `"Budget: Turn %d/%d — %d left, wrap up NOW."` Final: `"Budget: Turn %d/%d — FINAL, produce summary, no tools."` This cuts late/final from ~170 chars to ~75 chars each
- [ ] 6c: Create a shared `synthesisPrompt(reason string) string` function that returns a structured synthesis message (key findings, decisions, unfinished work). Replace both `subAgentSynthesisPrompt` and the inline string in `gracefulExhaustion`. Drop the `budgetReminderBlock()` prepend from `gracefulSubAgentSynthesis` (the model is told this is its final turn — budget numbers add nothing)
- [ ] 6d: When synthesis was used (the output is already summary-shaped), skip the post-hoc `summarizeWithModel` call. Add a `synthesisUsed` flag to the drain result that `buildResult` checks
- [ ] 6e: Tests — verify sub-agent budget reminders contain only turn budget, verify compact message format at each tier, verify unified synthesis prompt produces structured output, verify summarization is skipped when synthesis was used

## Phase 7: Compact System Improvements

Incremental improvements to the context management system.

- [ ] 7a: Expand the compact summary prompt from 4 to 6 focuses. Add: "5. Pending tasks or plan steps not yet completed" and "6. Errors encountered and their resolution status." This improves conversation continuity after compaction. Update in `compact.go`
- [ ] 7b: Make `clearOldToolResults` token-budget-aware. Instead of clearing all candidates beyond the most recent 4, estimate tokens freed per clearing (`size / 4` as rough bytes-to-tokens) and stop once the estimated input token count drops below the 80% threshold. This preserves more context when only a few large tool results are the bottleneck
- [ ] 7c: Tests — verify expanded compact prompt produces richer summaries, verify token-budget-aware clearing stops early when possible

---

**Open questions:**
- Should explore-mode agents also skip recent commits from their context? Commits can provide useful orientation ("what was recently changed"), but for narrowly-scoped explore tasks ("find function X in file Y") they're noise. Current recommendation: keep commits for now, revisit based on usage.
- Should we add a "plan" mode? Claude Code has one (read-only, full model, for architecture planning). It would be Explore with the main model instead of cheap model. Worth tracking but not needed for this refactoring — the caller can use "general" mode for planning tasks.
- Should sub-agent output summarization be configurable per invocation? The main agent could pass a flag like `"summarize": false` to get full output inline. This adds API surface — defer unless a clear need emerges.
