# Smarter Sub-Agent Delegation

**Goal:** Make the orchestrator agent more selective about when it spawns sub-agents, add output-file-based tracking for sub-agent results, and reduce overall token waste. Simple tasks should spawn 0-1 sub-agents, not a fleet.

**Success criteria:**
- Simple tasks (single file edit, one command) complete with zero sub-agent spawns
- Research tasks spawn sub-agents with output files the caller can reference cheaply
- Token usage measurably lower for equivalent tasks
- Caller can inspect sub-agent output files after the fact

---

## Current State

**Architecture:**
- `subagent.go`: SubAgentTool creates agents on demand, runs synchronously, returns text (max 30KB)
- `agent.go`: Tool calls execute sequentially in a for loop (lines 492-588) — no parallel execution
- `prompts/role.md`: Orchestrator prompt says "Delegate multi-step work to sub-agents. Act directly only for simple one-shot operations."
- `prompts/tools.md`: Agent tool section says "Use for multi-step work" and "For simple one-shot operations, act directly."
- Sub-agents get full system prompt + preamble, their own context window, and all tools
- Sub-agent output returned as text in tool result, truncated at 30KB
- No file-based output mechanism — everything flows through event stream and in-memory tool results

**Problems:**
1. **Over-delegation**: The system prompt strongly encourages delegation ("Delegate research, exploration, implementation, and debugging to sub-agents"). The model interprets this as "always delegate", even for 2-3 tool call tasks.
2. **No output files**: Sub-agent results live only in the tool result (30KB text blob in context). The caller can't cheaply re-read results — they're either in context or gone after clearing.
3. **Full system prompt per sub-agent**: Each sub-agent gets the complete system prompt rebuilt from scratch, wasting tokens on instructions it may not need.
4. **Sequential tool execution blocks parallelism**: Even when the LLM requests multiple tool calls, they execute sequentially. Multiple agent spawns could run in parallel but don't.

---

## Phase 1: Tighten Delegation Heuristics in System Prompt

Adjust the orchestrator and tool prompts so the model delegates less aggressively.

- [ ] 1a: **Rewrite `prompts/role.md` orchestrator section** — Change the delegation guidance from "delegate everything except simple one-shot" to a graduated approach: act directly for tasks under ~5 tool calls, delegate only when the task is genuinely multi-step OR would produce verbose output that bloats context. Add explicit examples of what NOT to delegate (single file read, one grep, a quick edit-and-test cycle).

- [ ] 1b: **Rewrite `prompts/tools.md` agent section** — Add a "when NOT to use agent" list mirroring the heuristic: don't spawn a sub-agent for a single grep, a file read, a small edit, or any task you can complete in 3-4 tool calls. Emphasize that each sub-agent has startup overhead (system prompt tokens, LLM call latency) and should only be used when the benefit outweighs that cost.

- [ ] 1c: **Update `subagent.go` preamble** — Make the sub-agent preamble shorter and more results-focused. Remove redundant instructions that duplicate the system prompt. The preamble should be ~3 lines: you're a sub-agent, complete the task, return concise results.

**Key design decisions:**
- The model ultimately decides when to delegate — we can only guide it via prompt. The goal is to shift the default from "delegate" to "act directly" with clear criteria for when delegation is warranted.
- The threshold (~5 tool calls) is a guideline, not enforced in code. Hard limits would be brittle.

---

## Phase 2: Sub-Agent Output Files

Give each sub-agent an output file so results persist on disk and the caller can re-read them cheaply instead of keeping 30KB in context.

- [ ] 2a: **Add output file path to SubAgentTool** — When a sub-agent completes, write its full output to a file at `.herm/agents/<agent_id>.md`. The tool result returned to the caller should be a short summary (first ~500 bytes) plus the file path, not the full 30KB. This keeps context lean while making results recoverable.

- [ ] 2b: **Add output_file to agent tool result format** — Change `formatSubAgentResult` to include the file path: `[agent_id: <id>] [output: .herm/agents/<id>.md]\n\n<summary>`. The caller can use `read_file` on the output path if it needs the full result.

- [ ] 2c: **Clean up old output files** — On agent startup (in `main.go` or `startAgent`), delete output files older than 24 hours from `.herm/agents/`. Keep it simple — no database, just file timestamps.

- [ ] 2d: **Test output file flow** — Verify: sub-agent writes output file, caller receives summary + path, reading the file returns full output, cleanup removes old files.

**Key design decisions:**
- Output files go in `.herm/agents/` (project-local, gitignored) not `/tmp` — they should survive across commands in the same session.
- The summary in the tool result should be enough for the caller to decide whether to read the full file. First ~500 bytes is a reasonable heuristic.
- The caller is not forced to read the file — it gets a useful summary inline. The file is a fallback for when more detail is needed or after context clearing.

---

## Phase 3: Leaner Sub-Agent System Prompt

Reduce token overhead per sub-agent invocation by trimming the system prompt.

- [ ] 3a: **Create a minimal sub-agent system prompt builder** — Sub-agents don't need the full orchestrator framing, context management section, devenv guidance (unless they have devenv), or the agent tool section (depth 1 sub-agents can't spawn children). Build a `buildSubAgentSystemPrompt()` that includes only: tool usage instructions for tools the sub-agent actually has, communication guidelines, and the sub-agent preamble. Estimate: ~40-50% fewer prompt tokens vs current approach.

- [ ] 3b: **Conditionally exclude tool sections** — `buildSystemPrompt` already uses `{{if .HasAgent}}` etc. For sub-agents, ensure we pass `HasAgent: false` when depth prevents nesting. Also skip the git remote operations section if not relevant, and skip devenv if not available.

- [ ] 3c: **Measure prompt size reduction** — Add a test or benchmark that compares `buildSystemPrompt` (main agent) vs `buildSubAgentSystemPrompt` token counts. Target: sub-agent prompt should be <60% of main agent prompt.

**Key design decisions:**
- Don't create a completely separate prompt template — reuse `buildSystemPrompt` with different flags. This avoids prompt drift between main and sub-agent.
- The sub-agent preamble (`subAgentPreamble`) replaces the orchestrator role section, not appends to it.

---

## Phase 4: Parallel Tool Execution for Agent Calls

When the LLM requests multiple agent tool calls in a single turn, run them concurrently instead of sequentially. This is already how Claude Code works (multiple agents in parallel).

- [ ] 4a: **Identify parallelizable tool calls** — In the tool execution loop (`agent.go:492-588`), separate tool calls into two groups: agent tool calls (can run in parallel) and other tool calls (run sequentially, since they may have side effects like file writes). Agent calls are safe to parallelize because each sub-agent has its own context and workspace.

- [ ] 4b: **Implement parallel agent execution** — Use a `sync.WaitGroup` or `errgroup.Group` to run agent tool calls concurrently. Collect results in a thread-safe slice. Non-agent tools still execute sequentially before or after the parallel batch.

- [ ] 4c: **Forward events correctly during parallel execution** — Multiple sub-agents emitting events simultaneously through `parentEvents` channel. Verify the TUI handles interleaved `EventSubAgentDelta` and `EventSubAgentStatus` events from different agent IDs correctly (the existing `AgentID` field on events should make this work).

- [ ] 4d: **Test parallel execution** — Verify: multiple agent calls in one turn run concurrently (measure wall-clock time), results are collected correctly, events from different sub-agents don't interfere, cancellation propagates to all parallel agents.

**Key design decisions:**
- Only agent tool calls are parallelized. File tools, bash, git, etc. remain sequential to avoid race conditions.
- The TUI already tags sub-agent events with AgentID, so interleaved events should render correctly in separate sub-agent display blocks.
- If any parallel agent fails, others continue — collect all results and return them to the LLM.

---

## Phase 5: Token Budget Awareness

Give the orchestrator visibility into how much context sub-agents are consuming so it can make better delegation decisions.

- [ ] 5a: **Include token usage in agent tool result** — After the summary and file path, append a line like `[tokens: input=12345 output=6789 cost=$0.02]`. This gives the orchestrator signal about whether its delegation was cost-effective.

- [ ] 5b: **Add a session token summary to system prompt context** — In the system prompt or as an injected context block, include: `Session: $X.XX spent, Y agent calls, Z tokens used`. This helps the model self-regulate — if it sees high spend, it may choose to act directly more often.

- [ ] 5c: **Test budget visibility** — Verify token usage appears in agent results and session summary is accurate.

---

## Out of Scope (Future Work)

- **Automatic delegation decision**: Code-level heuristic that intercepts the agent tool call and decides whether to actually spawn a sub-agent or execute inline. Too complex for now — prompt guidance is the right first step.
- **Sub-agent model override per call**: Letting the caller specify a model per agent spawn. Currently all sub-agents use `exploration_model`.
- **Shared scratchpad revival**: The scratchpad was implemented in the original sub-agents plan but the current code doesn't seem to use it. Could be useful for multi-agent coordination but adds complexity.
- **Streaming output files**: Writing sub-agent output to file incrementally as it runs, not just at completion. Nice but not essential.
