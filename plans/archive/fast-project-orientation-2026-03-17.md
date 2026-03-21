# Fast Project Orientation & Token Efficiency

**Goal:** Give the herm agent immediate project awareness at session start — eliminating wasted exploration tool calls — pass that context to sub-agents so they don't re-explore, and reduce token waste from oversized tool outputs.

**Success criteria:**
- Agent receives a project snapshot (top-level structure + recent commits) in its system prompt before the first user message
- Sub-agents receive the same snapshot, avoiding redundant exploration
- Zero additional tool calls needed for basic project orientation
- Token cost of snapshot stays under ~300 tokens (compact, structured format)
- Tool output truncation uses head+tail strategy instead of tail-only, so the model sees both the beginning and end of output
- Bash output ceiling lowered from 200 lines / 30KB to ~80 lines / 12KB, cutting worst-case per-call token cost in half

---

## Current State

**What the agent knows before the first message:**
- Git branch name (via `WorktreeBranch` in environment section)
- Date, container image, working directory
- Available tools

**What it does NOT know (must discover via tool calls):**
- Project file structure (requires `glob` or `bash ls/tree`)
- Recent history (requires `git log`)
- Tech stack (requires reading go.mod, package.json, etc.)
- Uncommitted changes (requires `git status` / `git diff`)
- What the project is about (requires reading README)

**What's already gathered but not injected:**
- `fetchStatusCmd()` (main.go:1246) gathers: branch, PR number, diff stats vs main, worktree count — but this only feeds the TUI status line, not the system prompt.

**Sub-agent context:**
- `buildSubAgentSystemPrompt()` (systemprompt.go:79) intentionally omits `WorktreeBranch`, personality, skills — sub-agents get no project orientation at all.
- Task description is the only way to pass project context; parent must manually include it.

**Prompt guidance gaps:**
- `role.md` says "Understand what's needed — read relevant code" but gives no recommended first steps for unfamiliar projects.
- `tools.md` teaches layered exploration (glob → grep → read_file) but doesn't suggest an initial orientation sequence.
- Git is documented for version control, not as an exploration/discovery tool.

---

## Phase 1: Pre-Gather Project Snapshot at Startup

Extend the startup sequence to gather a lightweight project snapshot that will be injected into the system prompt. This runs in parallel with existing init tasks (container boot, model loading, etc.) so it adds zero latency to startup.

- [x] 1a: **Add `fetchProjectSnapshot()` function** — New async command (like `fetchStatusCmd`) that runs at startup after workspace detection. Gathers:
  1. `ls -1` of the worktree root (top-level files and directories, one per line)
  2. `git log --oneline -20` (20 most recent commits)
  3. `git status --short` (uncommitted changes, if any)

  Returns a `projectSnapshotMsg` with three string fields. Each command runs with a short timeout (2s) and gracefully returns empty on failure. If `ls` returns fewer than ~8 entries, also run `tree -L 2 --noreport` as a fallback for richer structure (but cap output at 50 lines).

- [x] 1b: **Add `ProjectSnapshot` fields to `PromptData`** — Extend the `PromptData` struct with: `TopLevelListing string`, `RecentCommits string`, `GitStatus string`. These are populated from the snapshot gathered in 1a. Store the snapshot on the App struct so it's available when `startAgent()` builds the system prompt.

- [x] 1c: **Inject snapshot into `environment.md` template** — Add a "Project context" subsection to the environment template that renders the snapshot fields when non-empty. Format compactly:
  ```
  ## Project context

  Top-level:
  <ls output>

  Recent commits:
  <git log output>

  Uncommitted changes:
  <git status output or "clean">
  ```
  Keep it raw and compact — no decoration, no markdown formatting beyond headers. Goal is ~200-300 tokens for a medium project.

- [x] 1d: **Wire snapshot into startup flow** — Dispatch `fetchProjectSnapshot()` from the workspace-ready handler (alongside `fetchStatusCmd` and `bootContainerCmd`). Store result on `App` when the msg arrives. Ensure `startAgent()` reads it when building the system prompt. Handle the race: if the user sends a message before the snapshot arrives, build the prompt without it (the agent can still explore manually).

**Key design decisions:**
- The snapshot is a read-only, one-time gather — no caching, no persistence, no staleness concern within a session.
- `git log --oneline -20` gives the most signal per token: commit messages reveal tech stack, active areas, team members, and recent focus.
- `ls -1` at root is cheaper than `tree` but tree is a useful fallback for sparse top-levels. The threshold (~8 entries) avoids tree for large monorepos where it would be noisy.
- `git status --short` reveals dirty state which changes how the agent should behave (e.g., stash before checkout, or warn user about uncommitted work).

---

## Phase 2: Update System Prompts for Cold-Start Orientation

Add explicit guidance for how the agent should orient itself in unfamiliar projects, leveraging the pre-gathered snapshot.

- [x] 2a: **Add "Project Orientation" section to `role.md`** — Insert after "When given a task:" and before the delegation section. Guide the agent to:
  1. Read the project context from the environment section (snapshot)
  2. If the task needs more context than the snapshot provides, follow a recommended quick-explore sequence: check key config files (go.mod, package.json, Dockerfile, Makefile) → find entry points → scan README if present
  3. Emphasize: do NOT re-run `ls` or `git log` — that data is already in your system prompt

  Keep this section to ~5-6 lines. The agent should internalize the pattern, not follow a rigid checklist.

- [x] 2b: **Add git as exploration tool to `tools.md`** — In the git tool section, add a brief note that git is also useful for understanding code evolution:
  - `git log --oneline -10 -- <path>` to see history of a specific file/directory
  - `git show <commit>` to examine a specific change
  - `git diff <branch>` to compare branches

  Currently git documentation focuses only on mutation (commit, push, merge). Two lines of exploration guidance would broaden its perceived use.

- [x] 2c: **Add search strategy hints to `tools.md`** — After the existing "Explore in layers" line, add a brief decision guide:
  - Know the file name/pattern? → glob first
  - Know the code pattern? → grep first
  - Exploring unfamiliar project? → Start from the project snapshot, then glob to narrow

  Three lines max. Helps the agent pick the right first tool instead of trial-and-error.

**Key design decisions:**
- Prompt changes are minimal additions, not rewrites. The existing layered exploration guidance is good — we're filling gaps, not replacing it.
- "Don't re-run ls/git log" is critical: the whole point of pre-gathering is to avoid that first tool call. If the prompt doesn't say this, the agent will explore anyway out of habit.

---

## Phase 3: Pass Project Context to Sub-Agents

Ensure sub-agents receive the project snapshot so they don't waste their first few tool calls re-discovering what the parent already knows.

- [x] 3a: **Add snapshot to `buildSubAgentSystemPrompt()`** — Extend the function signature to accept the project snapshot fields. Include the same "Project context" block in the sub-agent's environment section. This costs ~200-300 extra tokens per sub-agent but saves 2-4 tool calls (and their much larger token cost).

- [x] 3b: **Thread snapshot through `SubAgentTool`** — The `SubAgentTool` struct (subagent.go) needs access to the snapshot to pass it to `buildSubAgentSystemPrompt()`. Add the snapshot fields to the tool's constructor and store them on the struct. Update `main.go:startAgent()` to pass the snapshot when creating the SubAgentTool.

- [x] 3c: **Update sub-agent role prompt** — In the sub-agent section of `role.md`, add one line: "The project snapshot in the Environment section gives you the project layout and recent history — use it instead of re-exploring." This ensures the sub-agent model knows the context is there and doesn't ignore it.

**Key design decisions:**
- The snapshot is static for the session — sub-agents get the same snapshot as the parent, which is correct since they share the same filesystem.
- We inject into the system prompt rather than prepending to the task description. System prompt is the right place for ambient context; task description should stay focused on the specific task.
- ~200-300 tokens is a small overhead vs the 1,100-token sub-agent system prompt. The ROI is clear: avoids 2-4 tool calls that would cost 500-2000+ tokens each.

---

## Phase 4: Tests

- [x] 4a: **Test `fetchProjectSnapshot()`** — Unit test covering: normal repo (all three commands succeed), non-git directory (git commands fail gracefully), sparse directory (tree fallback triggers), large directory (tree fallback skipped), command timeout handling.

- [x] 4b: **Test snapshot injection in system prompt** — Verify `buildSystemPrompt()` includes the project context section when snapshot fields are populated, and omits it cleanly when empty. Test both main agent and sub-agent prompt builders.

- [x] 4c: **Test sub-agent receives snapshot** — Verify that `buildSubAgentSystemPrompt()` with snapshot fields produces a prompt containing the project context block.

---

## Phase 5: Tool Output Truncation Improvements

Reduce token waste from tool outputs. Currently bash keeps the last 200 lines / 30KB (tail-only), which can dump ~10K tokens into context per call. Two changes: switch to head+tail so the model sees both ends, and lower the ceilings.

- [x] 5a: **Switch `truncateOutput()` to head+tail strategy** — Instead of keeping only the tail, keep the first N lines and last N lines with a `[... X lines omitted ...]` separator in between. This gives the model the command echo / headers at the top AND the errors / final status at the bottom. Suggested split: 20 head + 60 tail (tail-heavy because errors and exit status matter more). Apply the same byte limit after line truncation.

- [x] 5b: **Lower bash output limits** — Reduce `bashMaxLines` from 200 → 80 and `bashMaxBytes` from 30KB → 12KB. Update the tool description string to match. This cuts worst-case token cost from ~10K to ~3-4K per call. Also review grep (200 lines) and glob (1000 files) — consider lowering grep to 100 lines.

- [x] 5c: **Update truncation tests** — Rewrite `truncateOutput` tests in `tools_test.go` for the new head+tail behavior: verify head lines preserved, tail lines preserved, omission message present with correct count, byte limit still applied, and small outputs pass through unchanged.

**Key design decisions:**
- Head+tail is strictly better than tail-only: you never lose the command echo or initial output, and you still see the final errors. The omission count tells the model how much was skipped.
- 80 lines / 12KB is still generous for most commands (test failures, build errors, ls output). Commands that genuinely need full output (like `cat` on a large file) should use `read_file` instead.
- The 20/60 head/tail split is tail-heavy because the end of output (exit status, error messages, test summaries) is almost always more actionable than the beginning.

---

## Out of Scope (Future Work)

- **Repository map (Aider-style):** Tree-sitter AST + PageRank for compact codebase summary. High-value but high-complexity — the snapshot approach is the pragmatic first step.
- **Dynamic snapshot refresh:** Re-gathering the snapshot mid-conversation after major changes (branch switch, big commit). The snapshot is accurate enough for orientation; the agent can always re-explore.
- **Tech stack detection in snapshot:** Auto-detecting Go/Node/Python/etc. from config files and injecting framework-specific guidance. Useful but adds complexity to the snapshot logic.
- **Prompt caching optimization:** Ensuring the system prompt (with snapshot) hits Anthropic/provider prompt caches. Worth investigating but orthogonal to this plan.
- **Progressive disclosure for tool output:** Instead of dumping truncated output, return a compact summary (exit code, total line count, first/last few lines) and let the model explicitly request more via an offset/range parameter on the bash tool. Most token-efficient approach but requires a tool schema change and model behavior adjustment.
