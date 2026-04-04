# Agent Budget Consciousness: Turns, Tokens, and Self-Regulation

**Goal:** Make sub-agents (and the main agent) fully aware of their budget — turns used, turns remaining, tokens consumed — so they can scope work, prioritize, and synthesize before hitting hard limits. Eliminate the failure mode where sub-agents explore endlessly and get killed at `max_turns` with no useful output.

**Context:**

Observed in `debug-20260403-182556.json`: sub-agent `00046725` (explore mode, haiku) was tasked with "explore internal packages, check go.mod, look at langdag, understand what modules do." It spent all 20 turns reading files one-by-one (`find`, `ls`, `head`, `read_file`) and hit `max_turns` mid-tool-call — still trying to `ls /prompts/` — without ever producing a summary. The other two sub-agents finished fine (one used 5 turns, another used 10).

**Root cause chain:**
1. The sub-agent has **zero visibility** into its own budget. It doesn't know it has 20 turns, how many it has used, or when to stop exploring and start synthesizing.
2. The turn limit is enforced externally — `subagent.go:530` cancels the agent after `turns > maxTurns`. There's no "wrap up" phase. The agent gets killed mid-work.
3. The sub-agent role prompt (`role_subagent.md`) says "be token-efficient" and "stop when you have enough" but never mentions turns, limits, or budget pacing.
4. The main agent's delegation prompt doesn't communicate how many turns sub-agents get, so it can't scope tasks to fit the budget.

**How Claude Code handles this (reference: `notes/claude-code/src/`):**
- **Task Budget API**: Sends `task_budget: { total, remaining }` to the model at the API level — the model knows its remaining budget natively. (Anthropic beta feature: `task-budgets-2026-03-13`)
- **MaxTurns enforcement**: Framework-enforced, but combined with task_budget the model can self-pace.
- **Token budget continuations**: Client-side tracking with nudge messages at 90%.
- **System-reminder injection**: Dynamic context as `<system-reminder>` user messages.
- Claude Code does NOT inject turn counts into sub-agent prompts — it relies on the API-level task_budget.

**Our approach:** Since we don't have access to the task_budget API beta, we'll inject budget progress directly into the system prompt on every LLM call (extending the existing `systemPromptWithStats()` pattern). We'll also add a "wrap-up" phase that gives the agent a chance to synthesize before the hard kill.

**Key files:**
- `cmd/herm/agent.go` — `Agent` struct, `systemPromptWithStats()` (line 703), `runLoop()` (line 1066), `emitUsage()` (line 672)
- `cmd/herm/subagent.go` — `SubAgentTool`, `Execute()` (line 410), turn counting (lines 502-536), `runBackground()` (line 715)
- `cmd/herm/systemprompt.go` — `buildSubAgentSystemPrompt()` (line 123), `PromptData` (line 18)
- `prompts/role_subagent.md` — sub-agent role instructions
- `prompts/tools/agent.md` — agent tool description (main agent sees this)
- `prompts/role.md` — main agent role instructions

**Success criteria:**
- A sub-agent that would previously hit max_turns and die now produces a synthesis in its final turns
- Sub-agents see "Turn 5/20 | 12,400 tokens used" in their system prompt on every LLM call
- At 75% of turns (turn 15/20), the system prompt gains an urgent "wrap up" notice
- At 90% of turns (turn 18/20), a hard "synthesize NOW" instruction is injected
- The main agent's delegation prompt mentions the sub-agent turn budget
- Tests verify budget injection and wrap-up behavior

---

## Phase 1: Inject budget progress into sub-agent system prompts

Sub-agents currently get a static system prompt built once at spawn time. The main agent already has `systemPromptWithStats()` that appends dynamic stats on every LLM call. Sub-agents need the same mechanism — but with turn-specific budget info rather than just session totals.

**Approach:** The sub-agent's `Agent` instance needs to know its turn budget. Add fields to `Agent` for `maxTurns` and `turnsUsed` (updated by the drain loop in `SubAgentTool`). Extend `systemPromptWithStats()` to include turn progress when these fields are set. The drain loop in `Execute()` / `runBackground()` updates `turnsUsed` on the agent after each turn boundary (on `EventUsage`).

**Key challenge:** The turn counter lives in the drain loop (`subagent.go`), but the system prompt is rebuilt inside the agent's `runLoop()` (`agent.go`). We need to bridge this. Two options:
- (a) Pass turn info into the Agent via a thread-safe setter — the drain loop calls `agent.SetTurnProgress(turns, maxTurns)` and `systemPromptWithStats()` reads it.
- (b) Make the Agent count its own turns internally. This is cleaner but duplicates the turn-counting logic.

Option (a) is simpler and avoids duplication. The drain loop already has the turn count; just propagate it.

**What the sub-agent sees (appended to system prompt):**

Early turns (< 50%):
```
---
Budget: Turn 3/20 | 8,200 tokens used
```

Mid turns (50-75%):
```
---
Budget: Turn 12/20 | 34,100 tokens used — you're past halfway. Start narrowing your focus.
```

Late turns (75-90%):
```
---
Budget: Turn 16/20 — 4 turns remaining | 52,300 tokens used
⚠️ Wrap up: You're running low on turns. Stop exploring and begin synthesizing your findings. Your next response should start producing output.
```

Final turns (>90%):
```
---
Budget: Turn 19/20 — 1 turn remaining | 61,800 tokens used
🛑 FINAL TURN: Produce your complete summary NOW. Do not make any more tool calls. Write your findings as a final response.
```

- [ ] 1a: Add `turnBudget` fields to `Agent` struct — `maxTurns int` and `turnsUsed atomic.Int32` (or mutex-protected). Add `SetTurnProgress(used, max int)` method and `WithMaxTurns(n int)` agent option. These are zero-valued by default (no budget display for main agent unless explicitly set)
- [ ] 1b: Extend `systemPromptWithStats()` to include turn budget info when `maxTurns > 0`. Use the tiered messaging described above (thresholds at 50%, 75%, 90%). Include cumulative token count. Extract threshold constants as named values
- [ ] 1c: In `SubAgentTool.Execute()` drain loop, after incrementing `turns` on `EventToolCallStart` (line 526), call `agent.SetTurnProgress(turns, t.maxTurns)`. Do the same in `runBackground()`. Pass `WithMaxTurns(t.maxTurns)` when creating the agent
- [ ] 1d: Add token accumulation to the agent-level budget display. The drain loop already tracks `totalInputTokens`/`totalOutputTokens` — propagate these to the agent via `SetTokenProgress(input, output int)` so `systemPromptWithStats()` can display them
- [ ] 1e: Tests: (1) verify `systemPromptWithStats()` includes turn budget at each tier (early/mid/late/final); (2) verify `SetTurnProgress` is thread-safe; (3) verify budget not shown when `maxTurns == 0` (main agent case)

## Phase 2: Soft wrap-up before hard kill

Currently, hitting `max_turns` triggers an instant `agent.Cancel()` — the agent is killed mid-tool-call. This is harsh. The agent should get a chance to synthesize on its final turn instead of being canceled during exploration.

**Approach:** Change the enforcement strategy. Instead of canceling at `turns > maxTurns`, use a two-stage approach:
1. **Soft limit at `maxTurns - 1`**: On the penultimate turn, the budget injection (Phase 1) already shows "FINAL TURN: synthesize NOW". The agent sees this in its system prompt and should stop tool-calling.
2. **Hard limit at `maxTurns + 1`**: The cancel fires one turn later than before, giving the agent exactly one extra turn to produce text output after seeing the "FINAL TURN" message.
3. **Tools-disabled final call**: If the agent STILL requests tools on its final turn, make a tools-disabled LLM call (like `gracefulExhaustion` does for the main agent) so the model is forced to produce text.

This shifts the paradigm from "kill at limit" to "warn → synthesize → kill only if still exploring."

- [ ] 2a: Change the turn limit check in `Execute()` drain loop (line 530): instead of `if turns > t.maxTurns → cancel`, use `if turns > t.maxTurns + 1 → cancel`. The extra turn is the synthesis window. Update comment to explain the two-stage approach
- [ ] 2b: When `turns == t.maxTurns + 1` (the synthesis turn), check if the LLM response is text-only (no tool calls). If it requested tools, the hard cancel fires. If it produced text, that's the synthesis — let it through
- [ ] 2c: Add a `gracefulExhaustion`-style mechanism for sub-agents. When a sub-agent hits `maxTurns` and the model is still requesting tools, make one final tools-disabled LLM call with a prompt: `"[SYSTEM: Turn limit reached. Produce your final summary based on everything you've gathered so far. Do not request tools.]"`. This guarantees text output even from agents that ignore the system prompt budget warnings
- [ ] 2d: Update `runBackground()` with the same two-stage enforcement and graceful synthesis
- [ ] 2e: Tests: (1) sub-agent that would exceed turns gets a synthesis turn; (2) tools-disabled final call produces text output; (3) hard cancel still fires at `maxTurns + 1` for runaway agents; (4) the error message in the result changes from "partial output returned" to indicate synthesis was attempted

## Phase 3: Improve prompt guidance for budget management

The role prompts need to explicitly teach agents about budget pacing. Currently `role_subagent.md` says "be token-efficient" but never mentions turns. The main agent's tool description mentions turns exist but doesn't say how many a sub-agent gets.

- [ ] 3a: Update `prompts/role_subagent.md` — Add a "Budget management" section after the exploration strategy. Content: "You have a limited number of turns. Each LLM response (which may include multiple tool calls) counts as 1 turn. Your remaining budget is shown in the system prompt. Plan your work accordingly: reserve at least 1-2 turns for synthesizing your findings. If you're past 50% of turns, stop broad exploration and focus on the most relevant files. If the budget warning says to wrap up, your very next response should be your final summary — not more tool calls."
- [ ] 3b: Update `prompts/tools/agent.md` — Add the default turn budget to the description so the main agent can scope tasks. Add after the turns explanation: "Default budget: 20 turns per sub-agent. Scope tasks to fit within ~15 turns of exploration + a few turns for synthesis. If a task requires more depth, consider splitting it into multiple focused sub-agents rather than one broad one."
- [ ] 3c: Update `prompts/role.md` — In the agent delegation section, add: "Each sub-agent has a limited turn budget (default: 20). Scope delegated tasks to be completable within that budget. Prefer focused, specific tasks over broad exploration requests. Example: instead of 'explore the entire internal/ directory', try 'find how token tracking works in agent.go and subagent.go'."
- [ ] 3d: Tests: verify the new prompt sections are included in built system prompts (both main and sub-agent)

## Phase 4: Main agent budget visibility improvements

The main agent's `systemPromptWithStats()` currently shows "Session: X tokens used, Y agent calls" and iteration warnings at 30%. This can be improved:
1. Show cost estimate (the TUI already computes `sessionCostUSD`)
2. Show context window utilization percentage
3. Lower the warning threshold from 30% to include a graduated scale (like sub-agents get)

- [ ] 4a: Extend `systemPromptWithStats()` for the main agent to include context window utilization when `contextWindow > 0`. Format: "Context: ~X% full (Y/Z tokens)". This helps the model anticipate compaction and scope its work. Use the input tokens from the last LLM call as the estimate (already available as `lastInputTokens` on the agent or computable from the usage event)
- [ ] 4b: Add cost estimate to the stats line when `sessionCostUSD` is available. Requires passing cost info into the agent (currently only tracked in the TUI). Add a `SetSessionCost(cost float64)` method on Agent, called from the TUI's `EventUsage` handler. Format: "Session: X tokens, Y agent calls, ~$Z.ZZ"
- [ ] 4c: Add graduated warnings for the main agent too (not just at 30%): at 50% remaining iterations show "past halfway", at 25% show a stronger signal. Reuse the same tier logic from Phase 1
- [ ] 4d: Tests: verify context window % and cost appear in stats; verify graduated warnings at each threshold

## Phase 5: Integration tests — budget-aware sub-agent lifecycle

End-to-end tests that verify the complete budget-aware sub-agent flow, including the wrap-up phase.

- [ ] 5a: Test: sub-agent with `maxTurns=5` — use a scripted LLM that makes tool calls for 4 turns, then on turn 5 (seeing "FINAL TURN" in system prompt) produces text output. Verify: budget shown on each turn, wrap-up message appears, synthesis produced, result includes full output
- [ ] 5b: Test: sub-agent that ignores budget warnings — scripted LLM that keeps requesting tools past `maxTurns`. Verify: tools-disabled final call is made, text output is forced, error indicates synthesis was attempted, agent terminates cleanly
- [ ] 5c: Test: background sub-agent with budget awareness — same as 5a but via `runBackground()`. Verify event forwarding includes budget-aware system prompts
- [ ] 5d: Test: main agent delegates scoped task to sub-agent — verify the sub-agent's system prompt includes turn budget on every LLM call, and the main agent's prompt mentions the sub-agent turn budget

---

**Open questions:**
- Should we expose the `task-budgets` Anthropic API beta when available? This would let the model self-pace at the API level, which is cleaner than prompt injection. Worth tracking as a future enhancement, but prompt injection works now and is provider-agnostic.
- Should the turn budget be dynamically adjustable? E.g., if a sub-agent is making good progress (high-value tool results), could the main agent extend its budget? This adds complexity — defer unless it becomes a clear need.
- Should we add a cost budget (max $ per sub-agent)? Claude Code doesn't do this. Turns are a reasonable proxy for cost. Defer.
