# Token-Efficient File Exploration

**Goal:** Reduce agent token usage by adopting the file exploration strategies that make Claude Code efficient — dedicated search tools with structured I/O, sub-agent model routing via config, and tool result management.

**Success criteria:**
- Agent uses fewer tokens for equivalent exploration tasks (measure before/after)
- Exploration sub-agents run on a cheaper/faster model configured in settings
- `/model` opens config with inline model picker (enter on a model field → model selector)

---

## Current State

The agent has:
- **Bash tool** that runs commands inside Docker containers — `rg`, `tree`, `cat`, etc. are available there
- **Bash output truncation** at 200 lines / 30KB (keeps tail)
- **System prompt guidance** to explore in layers (structure → search → read)
- **Sub-agents** that always use the same model as the parent and return full text output
- **Config system** with global/project configs, `/config` UI with tabs, `/model` as a separate model picker
- **No `exploration_model`** config field — sub-agents inherit `active_model`

### Why This Is Expensive

1. **Bash overhead per tool call**: Model generates full CLI command strings and parses unstructured terminal output. Claude Code's dedicated tools (Glob, Grep, Read) have structured JSON inputs/outputs — the model writes `{"pattern": "*.go", "path": "src/"}` instead of `find /workspace/src -name '*.go' -type f | sort`. Structured output also means less parsing tokens.
2. **Full file reads**: `cat` dumps the whole file. No way to read just lines 40-60.
3. **No result-level token management**: Tool results live in context forever. Claude Code clears old tool results and compresses conversation.
4. **Sub-agents always use the expensive model**: Exploration doesn't need Opus — Haiku/Sonnet would suffice and be much cheaper/faster.

---

## Phase 1: Dedicated File Exploration Tools

Add native tools (alongside bash) that the agent can use for common file operations. These execute commands inside the Docker container but provide structured I/O. The container already has `rg` and standard tools installed; if a tool is missing, the agent can use devenv to add it.

- [x] 1a: **Glob tool** — pattern-based file finder. Input: `pattern` (glob string, supports `**`), optional `path` (directory). Executes in container (e.g. wraps `rg --files -g <pattern>` or `find`). Output: sorted list of matching file paths, one per line. No file contents.
- [x] 1b: **Grep tool** — content search. Input: `pattern` (regex), optional `path`, optional `glob` (file filter), optional `context` (lines around match), optional `output_mode` (`files_with_matches` default, `content`, `count`). Wraps `rg` inside the container. Returns only matching lines/files, not full files.
- [x] 1c: **Read tool** — file reader with partial support. Input: `file_path`, optional `offset` (start line), optional `limit` (max lines, default 2000). Executes in container (e.g. `sed -n` or `awk` for line ranges). Returns content with line numbers. Truncates lines over 2000 chars.
- [x] 1d: **Update system prompt** — add tool-specific guidance: prefer Glob/Grep/Read over bash for file operations. Keep bash for running builds, tests, and commands that aren't file reads. Mirror Claude Code's approach: "Do NOT use Bash to run cat, head, tail, grep, find, rg when a dedicated tool exists."
- [x] 1e: **Ensure container has required tools** — verify `rg` is in the base Dockerfile. If glob/grep/read tools depend on specific binaries, document them as devenv requirements. The devenv tool can fix missing tools.
  - Verified: `ripgrep` already in base Dockerfile (`apt-get install -y ... ripgrep`). All three tools use `rg` and `awk` — both present in base image.
- [x] 1f: **Test all three tools** — verify correct behavior in the Docker container context (paths relative to `/workspace`), edge cases (missing files, binary files, no matches, very large output), and output conciseness.

**Key design decisions:**
- Tools execute inside the Docker container (same as bash) — they send commands to the running container
- Output should be compact — no decorative formatting, just the data
- Glob should be `.gitignore`-aware (use `rg --files -g` under the hood)
- Read tool clearly indicates truncation when it occurs
- These tools complement bash, not replace it — bash is still needed for running builds, tests, installs

---

## Phase 2: Exploration Model in Config

Add a separate model slot for exploration/sub-agent work. Make `/model` open the config where model fields can be edited with an inline picker.

- [x] 2a: **Add `ExplorationModel` to Config** — new field in `Config` struct and `ProjectConfig`. Falls back to `ActiveModel` if empty. Add to config JSON serialization.
- [x] 2b: **Add exploration model to config UI** — add "Exploration Model" field to both Global (tab 1) and Project (tab 2) config tabs. When the user presses Enter on a model field, show the model picker inline (reuse the existing `/model` menu as a picker within the config editor).
- [x] 2c: **Change `/model` to open config** — instead of showing a standalone model picker, `/model` should open `/config` and navigate to the relevant tab where model fields live. Pressing Enter on "Active Model" or "Exploration Model" opens the model selector.
- [ ] 2d: **Route sub-agents to exploration model** — in `SubAgentTool`, use `config.ExplorationModel` (resolved) instead of the parent's `active_model`. If exploration model is unset, fall back to active model.
- [ ] 2e: **Test model routing** — verify sub-agents use the configured exploration model, and that changing it in config takes effect for subsequent sub-agent calls.

**Open questions:**
- Should the exploration model also be used for compaction/summarization tasks (Phase 3)?
- Should the agent tool definition expose a `model` parameter so the parent can choose per-call?

---

## Phase 3: Tool Result Token Management

Manage how much context tool results consume over a long conversation.

- [ ] 3a: **Research langdag's conversation retrieval** — understand how `GetAncestors()` builds the message chain. Can nodes be modified after creation? Can we mark content as "clearable"?
- [ ] 3b: **Implement tool result clearing** — when context grows beyond a threshold, replace old tool result content with a short placeholder (e.g., `[cleared — re-read if needed]`). Clear largest/oldest results first. This mirrors Anthropic's `clear_tool_uses` API feature.
- [ ] 3c: **Implement conversation compaction** — when approaching context limits, summarize conversation history using the exploration model (cheap). Replace history with: system prompt + summary + recent N turns.
- [ ] 3d: **Add `/compact` command** — manual trigger for compaction with optional focus hint (e.g., `/compact focus on the auth changes`).
- [ ] 3e: **Test compaction** — verify agent maintains coherent behavior after compaction (remembers goals, doesn't re-read files unnecessarily).

**Open questions:**
- Does langdag support modifying nodes in the tree after creation, or do we need a new branch?
- Right threshold for auto-compaction? Claude Code uses ~95% of context window.
- Rule-based clearing (drop old tool results) vs model-based summarization — or both in layers?

---

## Phase 4: Measurement & Tuning

- [ ] 4a: **Token usage benchmark** — define a standard task ("find and explain how X works") and measure total tokens before and after changes.
- [ ] 4b: **Per-tool-result token tracking** — log token count per tool result. Surface in UI or logs to identify expensive operations.
- [ ] 4c: **Tune thresholds** — adjust Read's default line limit, Grep's output cap, compaction trigger, sub-agent output limits based on real usage.

---

## Out of Scope (Future Work)

- **Repository map** (Aider-style): tree-sitter + PageRank for compact codebase summary (~1K tokens for full architectural awareness). High impact but high complexity.
- **Code intelligence / LSP**: Go-to-definition, find-references. Replaces many grep+read cycles.
- **Token-efficient tool use API header**: Anthropic's `token-efficient-tools-2025-02-19` beta.
- **Prompt caching verification**: Ensure system prompt and tool definitions hit Anthropic's prompt cache.
