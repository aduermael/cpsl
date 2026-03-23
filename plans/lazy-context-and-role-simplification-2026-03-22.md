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

- [x] 3a: Review `.herm/skills/devenv.md` content against `prompts/tools/devenv.md`. Move essential non-duplicated content (Dockerfile examples, common mistakes, RUN layer hygiene) into the tool description markdown file. The skill file should be trimmed or removed if fully redundant
- [x] 3b: If the skill is kept, slim it to project-specific content only (e.g., base image version specifics). Update skills.md template if needed. If removed, clean up references

## Phase 4: Filter sub-agent tools by mode

Explore-mode sub-agents should only get read-only tools.

- [x] 4a: Define a package-level `exploreToolAllowlist` as a `map[string]bool` containing: `glob`, `grep`, `read_file`, `outline`, `bash`. Modify `buildSubAgentTools()` in `subagent.go` to accept a `mode` parameter. When mode is `"explore"`, filter the tool list to only tools in the allowlist. When mode is `"implement"`, keep the full tool set. Note: bash remains in explore mode (needed for read-only commands like `ls`, `tree`, build checks) — this is an accepted escape hatch, consistent with Claude Code's approach
- [x] 4b: Add tests verifying: (1) explore-mode tool list contains only allowlisted tools, (2) implement-mode tool list contains all tools, (3) the sub-agent system prompt built from filtered tools excludes write-tool guidance

## Phase 5: Rename HasGit to RunsOnHost

The `HasGit` flag in `PromptData` gates host-specific guidance ("runs on the host", "SSH keys and credentials"), not git-specific logic. Rename for clarity and extensibility — more tools may run on host in the future.

- [x] 5a: Rename `HasGit` to `RunsOnHost` in the `PromptData` struct in `systemprompt.go`. Update both `buildSystemPrompt()` and `buildSubAgentSystemPrompt()` to set it from `toolNames["git"]` (same source, better name). Update all template references in `role.md` and `environment.md` from `.HasGit` to `.RunsOnHost`. Reframe the gated content to be about host access generically: e.g. "Some tools run on the host rather than inside the container, giving them access to SSH keys and credentials" rather than "The git tool runs on the host"
- [x] 5b: Update `systemprompt_test.go` for the rename. Any tests checking for `HasGit` or the old template output need updating

## Phase 6: Align explore-mode role text with filtered tools

Phase 4 filters tools for explore mode, but `role.md` still unconditionally tells sub-agents "modify any files." The role text should reflect actual capabilities.

- [x] 6a: In `role.md`, make the sub-agent capability statement conditional. When `HasEditFile` or `HasWriteFile` is true: "You have full control — run any commands, modify any files." When neither is true (explore mode): "You can run commands, search code, and read files." No new `PromptData` fields needed — the existing `Has*` flags are already correctly computed from the filtered tool list
- [x] 6b: Add a test in `systemprompt_test.go` verifying: (1) sub-agent prompt with write tools includes "modify" language, (2) sub-agent prompt without write tools does NOT include "modify" language

## Phase 7: Move prompts directory to repo root

The `cmd/herm/prompts/` directory is buried too deep. Move it to the repo root so all system prompt markdown is easily discoverable. Note: `//go:embed` does not allow `..` paths, so the embed FS must be restructured — likely a dedicated `prompts` package with its own embed, imported by `cmd/herm`.

- [x] 7a: Create a `prompts/` package at the repo root. Move all 18 markdown files from `cmd/herm/prompts/` into `prompts/` (preserving the `tools/` subdirectory). Add a `prompts.go` file that embeds the files and exports the template set and tool description FS. This package owns `//go:embed prompts/*.md` and `//go:embed tools/*.md` (relative to its own directory)
- [x] 7b: Update `cmd/herm/systemprompt.go` to import the new `prompts` package instead of using its own embed. Remove the old `//go:embed prompts/*.md` directive. Update `promptTemplates` to use the exported template set from the prompts package
- [x] 7c: Update `cmd/herm/tooldesc.go` to import the new `prompts` package instead of embedding `prompts/tools/*.md` directly. Remove the old `//go:embed prompts/tools/*.md` directive
- [x] 7d: Remove the now-empty `cmd/herm/prompts/` directory. Update any tests that reference prompt paths. Run `go test ./...` to verify everything still works

## Phase 8: Split system.md into main and sub-agent entry points

The single `system.md` template branches on `.IsSubAgent`, and that conditional cascades through `role.md`, `tools.md`, etc. Two separate entry-point templates make the flow easier to read. Also add Go template docstring comments (`{{/* ... */}}`) to each template file for maintainability.

- [x] 8a: Create `system_subagent.md` as a new entry-point template for sub-agents. It should define `{{define "system_subagent"}}` and chain only the templates relevant to sub-agents (role, tools, practices, environment — no communication, personality, or skills). Remove the `{{if not .IsSubAgent}}` conditional from `system.md` — it becomes the main-agent-only entry point
- [x] 8b: Update `buildSubAgentSystemPrompt()` in `systemprompt.go` to execute `"system_subagent"` instead of `"system"`. Update `prompts.go` to ensure the new file is included in the embed
- [x] 8c: Simplify `role.md` — split into `role.md` (main agent) and `role_subagent.md` (sub-agent), removing the top-level `{{if .IsSubAgent}}` branch. Update both `system.md` and `system_subagent.md` to reference the appropriate role template
- [x] 8d: Add Go template comments (`{{/* ... */}}`) as docstrings to each template file. Each file should have a brief comment at the top describing its purpose and which entry point uses it. These are standard `text/template` comments, stripped at render time
- [x] 8e: Update `systemprompt_test.go` — the `TestPromptTemplateParsing` test needs to include the new template names. Existing tests for `buildSubAgentSystemPrompt` should still pass since behavior is unchanged. Run `go test ./...`

## Phase 9: Per-tool execution context and host tool grouping

The `RunsOnHost` boolean is a proxy for "git tool is present" but pretends to be generic. The vague "Some tools run on the host" language doesn't tell the LLM *which* tools. Meanwhile, 7 of 10 tool description files don't mention where they execute. This phase makes execution context a first-class property of each tool, and renders it explicitly in prompts.

**Guiding principle:** The agent and sub-agents all run on the host — but most *tools* execute inside the container. Host-executing tools are the exception and should be clearly flagged and grouped.

- [x] 9a: Add `HostTool() bool` to the `Tool` interface in `agent.go`. Returns `true` if the tool executes on the host rather than in the container. Implement on all tool structs: `GitTool` returns `true`; all others (`BashTool`, `GlobTool`, `GrepTool`, `ReadFileTool`, `EditFileTool`, `WriteFileTool`, `OutlineTool`, `DevEnvTool`, `SubAgentTool`) return `false`. DevEnvTool is borderline (reads/writes Dockerfile on host) but its primary user-facing behavior is container-focused, so `false` is correct — revisit if this causes confusion

- [x] 9b: Replace `RunsOnHost bool` with `HostTools []string` in the `PromptData` struct in `systemprompt.go`. In both `buildSystemPrompt` and `buildSubAgentSystemPrompt`, compute the list by iterating tools and collecting `t.Definition().Name` where `t.HostTool()` returns `true`. This makes the field self-maintaining — adding a future host tool only requires implementing `HostTool() bool { return true }` on that struct. Drop all `toolNames["git"]` references that previously set `RunsOnHost`

- [x] 9c: Update `role.md` and `role_subagent.md` templates. Replace the vague `{{if .RunsOnHost}} Some tools run on the host...{{end}}` conditional with explicit enumeration using the `HostTools` slice. When the list is non-empty, render something like: "Most tools execute inside the container. **Host exceptions:** {{range .HostTools}}{{.}}, {{end}} — these run on the host with access to SSH keys and credentials that container tools cannot reach. Use `git` for remote operations (push, pull, fetch)." When the list is empty, omit the section entirely. The git-specific tip ("Use `git` for remote operations") should be conditional on `"git"` being in the list, not hardcoded

- [x] 9d: Update `environment.md`. Replace `{{if .RunsOnHost}}` with `{{if .HostTools}}`. The git worktree line should check for `"git"` in the list (use a template helper function or range/eq). Consider adding a grouped "Host tools" bullet under Environment that lists them, e.g. `- Host tools: git (worktree: branch-name)` instead of the current separate `- Git: project is in a worktree managed by herm`

- [x] 9e: Update all tool description markdown files in `prompts/tools/` to consistently state execution context. Add a `runs_on:` field to each file's frontmatter (`container` or `host`). Near the top of each description body, include a one-line statement: "Runs inside the dev container." or "Runs on the host (not in the container)." Currently only `bash.md` and `git.md` state this; update `devenv.md`, `glob.md`, `grep.md`, `read_file.md`, `edit_file.md`, `write_file.md`, `outline.md`, and `agent.md`

- [x] 9f: Update `systemprompt_test.go`. Remove tests checking for `RunsOnHost` boolean behavior. Add tests verifying: (1) `HostTools` slice is correctly populated from tools where `HostTool()` returns `true`, (2) main and sub-agent prompts enumerate host tool names explicitly when present, (3) when no host tools exist (e.g. explore-mode sub-agent without git), the host exception section is omitted entirely, (4) each tool struct returns the expected `HostTool()` value

## Phase 10: Smarter project snapshot with tree view and truncation

The current project snapshot uses `ls -1` (flat listing) with a fallback to `tree -L 2` only when ≤8 entries. This is too shallow for most projects. The "clean" label for uncommitted changes is terse and unnatural.

**Goal:** Two-level tree view with smart truncation, and better wording for clean status.

- [ ] 10a: Create a `buildProjectTree(rootPath string, maxTopLevel, maxPerSubdir int) string` function in `background.go`. It should: (1) list top-level entries, (2) for directories, list one level of sub-entries, (3) truncate top-level entries beyond `maxTopLevel` (default 20) with a "+N more" line, (4) truncate sub-entries beyond `maxPerSubdir` (default 8) with "+N more" per directory, (5) use simple indentation (2 spaces) rather than ASCII tree characters. The function runs `ls` commands in the container — no new dependencies. Important files (README, go.mod, package.json, Makefile, Dockerfile) should be kept when truncating top-level, with less notable entries dropped first

- [ ] 10b: Update `fetchProjectSnapshot()` in `background.go` (lines 431-509) to use `buildProjectTree()` instead of the current `ls -1` / `tree -L 2` fallback logic. Keep the 2-second timeout. The `projectSnapshot.TopLevel` field now holds hierarchical output

- [ ] 10c: Update `prompts/environment.md` template. Change the "Uncommitted changes" section: when `GitStatus` is empty, render "no uncommitted changes" instead of "clean". Keep current behavior when `GitStatus` is non-empty

- [ ] 10d: Update tests. Modify `TestBuildSystemPromptCleanRepo` in `snapshot_test.go` to check for "no uncommitted changes" instead of "clean". Add `TestBuildProjectTree` verifying: (1) two-level output for normal directories, (2) "+N more" truncation when entries exceed limits, (3) important files preserved during truncation

## Phase 11: Show full agent context in display mode

When `display_system_prompts` is enabled, the user only sees the rendered system prompt text. But the LLM also receives tool definitions (name + full Description from `prompts/tools/*.md` + InputSchema) and session stats — these are invisible. The user should see everything the agent sees.

**Design decision:** Don't duplicate tool descriptions into the system prompt text (wastes tokens). Instead, expand the display in `agentui.go` to append a "Tools" section showing the tool definitions the LLM receives alongside the system prompt. This keeps the actual prompt lean while giving full transparency.

- [ ] 11a: In `agentui.go`, expand the `displaySystemPrompts` block (line 166-168). After appending the system prompt message, build and append a second `chatMessage` (or extend the same one) that lists each tool's Definition: name, brief description, and parameter names from InputSchema. Format as a compact block, e.g. `── Tool Definitions ──\n\nbash: Run a shell command in the dev container\n  params: command, timeout\n\ngit: Run git commands on the host...\n  params: subcommand, args\n...`. Use the same `msgSystemPrompt` kind so it renders with the same dim italic style. Include both client tools (from the `tools` slice) and server tools (web_search)

- [ ] 11b: Extract InputSchema parameter names for display. Add a helper `toolParamNames(schema json.RawMessage) []string` that unmarshals the InputSchema just enough to extract the `properties` keys. This keeps the display concise — users see param names, not full JSON schemas. Server tools with no InputSchema show "(server-side)" instead

- [ ] 11c: Also append the session stats that get added to the system prompt at runtime. Currently `systemPromptWithStats()` in `agent.go` (lines 348-354) appends token/agent-call stats, but this happens after the display. Either show the stats line in the display (with a note it updates at runtime), or accept that initial display won't show stats since they're zero at startup

- [ ] 11d: Update `prompts/role.md` to briefly mention delegation capability when the agent tool is available. Add a conditional after the workflow steps: `{{if .HasAgent}}You can delegate complex subtasks to sub-agents — see the agent tool.{{end}}`. This restores the delegation awareness that was removed when the orchestrator identity was collapsed, without reintroducing heavy orchestrator framing. This one change does belong in the system prompt text since it's role guidance, not tool metadata

- [ ] 11e: Update tests. Add `TestDisplaySystemPromptsShowsToolDefinitions` verifying: (1) when display mode is on, tool definitions are included in displayed messages, (2) tool names and brief descriptions appear, (3) parameter names are extracted correctly, (4) server tools show "(server-side)". Add `TestToolParamNames` for the helper function

## Open questions

- **web_search**: This is a server-side tool (no Go Definition() method — it's a `types.ToolDefinition` literal). Should it also get a markdown file, or keep the inline definition? Leaning toward keeping it inline since it's 3 lines and provider-defined.
- **Placeholder syntax**: `__CONTAINER_IMAGE__` matches the existing `__HERM_VERSION__` pattern in devenv. Are there other dynamic values needed beyond ContainerImage?
- **Tree depth vs token cost**: Two levels is a good default, but large monorepos could still blow up. The `maxTopLevel` and `maxPerSubdir` caps mitigate this, but we may need a total line cap (~100 lines) as a safety valve.
