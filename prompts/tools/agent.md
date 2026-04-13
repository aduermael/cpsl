---
name: agent
description: Spawn a sub-agent to handle a complex subtask
runs_on: container
---

Runs inside the dev container. Spawn a sub-agent with its own context window. Each sub-agent has startup cost (system prompt tokens + LLM call latency), so only use when the benefit outweighs that overhead.

**Modes — you must specify one:**
- `"explore"` — uses a fast, cheap model. For research, search, reading code, investigating issues, gathering information.
- `"general"` — uses the full orchestrator model. For writing code, making edits, running build/test cycles, executing changes.

**When to use:**
- Tasks requiring deep exploration across many files (10+ tool calls) -> `explore`
- Self-contained implementation work that would produce verbose output -> `general`
- Running multiple independent investigations in parallel (spawn several sub-agents) -> `explore`

When spawning multiple general-mode sub-agents, ensure they work on separate files — parallel edits to the same file will conflict. Partition work by file or directory.

**When NOT to use — act directly instead:**
- A single grep, glob, or file read
- A small edit (even edit -> test -> fix cycles)
- Running one command and interpreting the output
- Any task completable in ~5 or fewer tool calls

**Background mode** (`background: true`):
- The sub-agent runs asynchronously. You get an agent_id immediately and can continue working.
- When the background agent completes, its result is automatically injected into your next LLM context — you will see a "[Background agent completed]" notification.
- Use for long-running explorations or parallel independent tasks where you don't need the result right away.
- Check status anytime: `agent(agent_id: "<id>", task: "status")` — returns "running" or "completed" with the full result.
- Cannot be combined with agent_id (no background resume).

**Usage:**
- Provide a clear, self-contained task description — the sub-agent has the same tools you do but no shared memory.
- Resume a previous sub-agent by passing its agent_id with a new task — this continues from where it left off with full context preserved.

**Reading results:**
- Results include metadata: `[agent_id]`, `[output]`, `[tokens]`, `[turns]`, and a summary.
- `[summary: model]` — intelligent summary; usually sufficient to act on.
- `[summary: truncated]` — naive truncation; read the full output file via `read_file` for complete findings.
- `[errors: ...]` — sub-agent hit errors; review and consider retrying with a narrower task.
- `[turns: N/M]` — turns count LLM response cycles, not individual tool calls (one response with 5 tool calls = 1 turn). N=M means the sub-agent hit its turn limit and may have incomplete results.

**Turn budget:** Default is __DEFAULT_MAX_TURNS__ turns per sub-agent. Scope tasks to fit within ~__EXPLORATION_TURNS__ turns of exploration + a few turns for synthesis. If a task requires more depth, consider splitting it into multiple focused sub-agents rather than one broad one.
