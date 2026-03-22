# Lazy Tool Context & Role Simplification

**Goal:** Reduce system prompt size by moving tool guidance into tool Description fields loaded from markdown files, and simplify the main agent role by removing the "orchestrator" identity.

**Motivation:** The main agent currently loads ~6,200 tokens of system prompt — 55% is tool guidance duplicated between inline Go strings and the `tools.md` template. The "orchestrator" framing is overfit: most tasks are simple and the agent should just act directly. Sub-agents are a tool, not an identity.

## Key files

- `cmd/herm/prompts/role.md` — three-branch role template (sub-agent / orchestrator / expert)
- `cmd/herm/prompts/tools.md` — 148 lines of tool guidance, all rendered upfront in system prompt
- `cmd/herm/prompts/skills.md` — renders full skill content inline in system prompt
- `cmd/herm/systemprompt.go` — prompt builder with PromptData flags
- `cmd/herm/systemprompt_test.go` — includes sub-agent ratio test (<60%)
- `cmd/herm/agent.go` — Tool interface, NewAgent, runLoop
- `cmd/herm/subagent.go` — buildSubAgentTools, Execute
- `cmd/herm/tools.go` — BashTool, GitTool, DevEnvTool with inline Description strings
- `cmd/herm/filetools.go` — GlobTool, GrepTool, ReadFileTool, etc. with inline Description strings
- `cmd/herm/skills.go` — frontmatter markdown parser (reusable pattern)
- `.herm/skills/devenv.md` — 135-line skill always embedded in system prompt

## Design decisions

- **Tool guidance lives in the tool Description field, not the system prompt.** Each tool gets a markdown file under `cmd/herm/prompts/tools/`. Frontmatter `description:` is the brief API description. Body is extended guidance. Both are concatenated into the tool's `types.ToolDefinition.Description`. The LLM sees guidance only for tools that are actually available — no conditionals needed.
- **Markdown files over inline strings.** Tool descriptions are maintained as standalone `.md` files with frontmatter (same pattern as `skills.go`). Embedded at compile time via `//go:embed`. Easier to maintain, review, and diff than inline Go strings or Go templates with conditionals.
- **`tools.md` becomes minimal.** Only cross-tool workflow guidance remains (explore in layers, prefer dedicated tools over bash). Per-tool sections are removed entirely.
- **No runtime injection mechanism.** No `seenTools` tracking, no system message injection, no cache invalidation risk. Guidance is always present in the tool Description from the first call.
- **Role collapse.** The main agent is always "an expert coding agent." Delegation heuristics move to the agent tool's description file.
- **Sub-agent tool filtering by mode.** Explore-mode sub-agents get read-only tools only.
- **Dynamic values.** Bash description needs `ContainerImage` — tool's `Definition()` method appends dynamic info to the loaded markdown description. DevEnv description needs `ContainerImage` similarly.
- **IsSubAgent conditionals dropped from tool guidance.** The git merge/commit guidance (14 lines) and devenv proactive build guidance (8 lines) are included for all agents. The overhead is minor and simplifies the architecture.
- **DevEnv skill consolidation.** Core devenv guidance moves to `prompts/tools/devenv.md` (the tool description). The `.herm/skills/devenv.md` skill is trimmed to only project-specific content (examples, base image details) or removed if redundant.

## Success criteria

- System prompt is ≤50% of current size (tool guidance moved to Description fields)
- All systemprompt_test.go tests pass (adapted for new structure)
- Tool Description fields contain full guidance (verifiable via Definition() tests)
- Explore-mode sub-agents cannot call edit_file, write_file, or devenv
- No behavioral regression: agent still avoids ephemeral bash installs, uses devenv for env setup, prefers dedicated file tools

---

## Phase 1: Simplify main agent role

Collapse role.md from three branches to two. Remove the "orchestrator" identity.

- [x] 1a: Rewrite `role.md` — merge the `HasAgent` and non-`HasAgent` branches into a single main agent role ("expert coding agent"). Keep the sub-agent branch (`IsSubAgent`) unchanged. Remove all delegation heuristics (lines 24-46) — these move to the agent tool's markdown description in Phase 2
- [x] 1b: Update `systemprompt_test.go` — tests checking for "orchestrator" or three-branch structure need updating. Adjust the sub-agent ratio threshold if needed

## Phase 2: Tool description markdown files

Create per-tool markdown files that replace both inline Description strings and tools.md guidance.

- [x] 2a: Create `cmd/herm/prompts/tools/` directory with markdown files for each tool: `bash.md`, `git.md`, `devenv.md`, `agent.md`, `glob.md`, `grep.md`, `read_file.md`, `edit_file.md`, `write_file.md`, `outline.md`. Each file uses frontmatter with `name:` and `description:` (brief 1-line). Body contains extended guidance currently split between the inline Description string and the `tools.md` template section for that tool. The agent tool's body should include the delegation heuristics moved from role.md in Phase 1

- [x] 2b: Add a tool description loader in `systemprompt.go` (or a new `tooldesc.go`). Reuse the frontmatter parsing pattern from `skills.go`. Embed the files via `//go:embed prompts/tools/*.md`. Expose a function like `loadToolDescriptions()` that returns a `map[string]ToolDesc` (name → brief description + full description). The full description is `frontmatter.description + "\n\n" + body`. For tools with dynamic values (bash, devenv need `ContainerImage`), support a placeholder like `__CONTAINER_IMAGE__` that gets replaced at load time

- [x] 2c: Update each tool's `Definition()` method to use the loaded description instead of inline strings. In `filetools.go`: GlobTool, GrepTool, ReadFileTool, OutlineTool, EditFileTool, WriteFileTool. In `tools.go`: BashTool, GitTool, DevEnvTool. In `subagent.go`: SubAgentTool. The InputSchema stays as inline JSON in Go code (structural, not prose)

- [x] 2d: Slim down `tools.md` to only cross-tool guidance. Remove all per-tool `### sections`. Keep only: the "explore in layers" workflow pattern, "prefer dedicated tools over bash" reminder, and "quick decision guide" (glob vs grep vs outline). This should be ~10-15 lines. Remove the `HasGlob`, `HasEditFile`, `HasBash`, `HasGit`, `HasDevenv`, `HasAgent`, `HasWebSearch` conditionals that gate per-tool sections — they're no longer needed since guidance is in Description fields

- [x] 2e: Update tests. Existing tests that check for `### bash`, `### git`, etc. in the system prompt need rewriting — those sections no longer exist there. Add new tests verifying: (1) each tool's `Definition().Description` contains expected guidance keywords, (2) the slimmed `tools.md` renders only cross-tool guidance, (3) `loadToolDescriptions()` correctly parses all markdown files and replaces placeholders

## Phase 3: Consolidate devenv skill

The devenv tool's markdown file now contains core guidance. Reconcile with the `.herm/skills/devenv.md` skill.

- [ ] 3a: Review `.herm/skills/devenv.md` content against `prompts/tools/devenv.md`. Move essential non-duplicated content (Dockerfile examples, common mistakes, RUN layer hygiene) into the tool description markdown file. The skill file should be trimmed or removed if fully redundant
- [ ] 3b: If the skill is kept, slim it to project-specific content only (e.g., base image version specifics). Update skills.md template if needed. If removed, clean up references

## Phase 4: Filter sub-agent tools by mode

Explore-mode sub-agents should only get read-only tools.

- [ ] 4a: Define a package-level `exploreToolAllowlist` as a `map[string]bool` containing: `glob`, `grep`, `read_file`, `outline`, `bash`. Modify `buildSubAgentTools()` in `subagent.go` to accept a `mode` parameter. When mode is `"explore"`, filter the tool list to only tools in the allowlist. When mode is `"implement"`, keep the full tool set. Note: bash remains in explore mode (needed for read-only commands like `ls`, `tree`, build checks) — this is an accepted escape hatch, consistent with Claude Code's approach
- [ ] 4b: Add tests verifying: (1) explore-mode tool list contains only allowlisted tools, (2) implement-mode tool list contains all tools, (3) the sub-agent system prompt built from filtered tools excludes write-tool guidance

## Open questions

- **web_search**: This is a server-side tool (no Go Definition() method — it's a `types.ToolDefinition` literal). Should it also get a markdown file, or keep the inline definition? Leaning toward keeping it inline since it's 3 lines and provider-defined.
- **Placeholder syntax**: `__CONTAINER_IMAGE__` matches the existing `__HERM_VERSION__` pattern in devenv. Are there other dynamic values needed beyond ContainerImage?
