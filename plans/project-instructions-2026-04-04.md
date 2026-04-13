# Project Instructions Feature

Add support for a user-authored `.herm/instructions.md` file (analogous to `CLAUDE.md` in Claude Code). Its content is injected into the system prompt, giving the agent persistent knowledge of project conventions, architecture, build commands, and behavioral guidelines across sessions.

Users control which agent types see the instructions via a `scope` front-matter field.

## Design Decisions

- **File name and location**: `.herm/instructions.md` inside the project's `.herm/` directory, consistent with other project-level herm config (`.herm/config.json`, `.herm/skills/`, `.herm/environment.md`).
- **Front-matter with `scope` field**: Controls which agents receive the instructions. Three levels, naturally nested:
  - `all` (default) — main agent + implement sub-agents + explore sub-agents
  - `implement` — main agent + implement sub-agents (excludes explore)
  - `main` — main agent only
  Default is `all` because most instructions (coding conventions, architecture) apply everywhere. Users narrow the scope to save tokens for lightweight explore agents.
- **Front-matter is optional**: If the file has no front-matter (or no `scope` field), defaults to `all`. This means a plain markdown file with no `---` block works out of the box.
- **Free-form content**: The body after front-matter is raw markdown, injected as-is. No structured fields required beyond the optional `scope`.
- **Template placement**: New `## Project Instructions` section placed after `skills` in the main agent template chain. For sub-agents, placed at the end of the sub-agent template chain.
- **Graceful absence**: If the file doesn't exist, the section is omitted entirely. Same pattern as `personality` and `skills`.

## Front-matter Example

```markdown
---
scope: implement
---

## Build & Test

- Run tests: `go test ./...`
- Build: `go build ./cmd/herm`

## Conventions

- Use `fmt.Errorf("context: %w", err)` for error wrapping
- Table-driven tests with `t.Run` subtests
```

## Codebase Context

- **PromptData struct**: `cmd/herm/systemprompt.go:18-53` — holds all template data.
- **buildSystemPrompt()**: `cmd/herm/systemprompt.go:59-108` — assembles main agent prompt. Already calls `readContainerEnv(workDir)` internally — project instructions loading follows the same pattern.
- **buildSubAgentSystemPrompt()**: `cmd/herm/systemprompt.go:132-178` — assembles sub-agent prompt. Currently omits personality and skills. Needs to conditionally include instructions when scope allows.
- **Template chains**:
  - `prompts/system.md:3` — main: `environment → role → tools → practices → communication → personality → skills`
  - `prompts/system_subagent.md` — sub-agent: `environment → role_subagent → tools → practices`
- **readContainerEnv()**: `cmd/herm/systemprompt.go:113-126` — reads `.herm/environment.md`. Closest analog (single file read, graceful fallback).
- **Skills front-matter parsing**: `cmd/herm/skills.go:51-91` — `parseSkill()` parses `---` delimited front-matter. The instructions parser follows the same pattern but extracts `scope` instead of `name`/`description`.
- **Sub-agent mode**: `cmd/herm/subagent.go:449` — mode is `"explore"` or `"implement"`. The sub-agent prompt builder needs to know the mode to check against scope.
- **Sub-agent prompt construction**: `cmd/herm/subagent.go:473` — calls `buildSubAgentSystemPrompt(subTools, t.serverTools, t.workDir, t.containerImage, &snap.snapshot)`. Will need a new parameter for instructions content (or workDir-based loading with mode).
- **worktreePath**: Used at `agentui.go:185` as the base for project paths. `.herm/instructions.md` is loaded relative to this (repo root).

## Failure Modes

- **File doesn't exist**: Return empty content, template section omitted.
- **File is empty or whitespace-only**: Treat as absent.
- **No front-matter**: Treat as `scope: all`.
- **Unknown scope value**: Warn in content and fall back to `all`.
- **File is extremely large**: Add a size cap (16 KB). Truncate with a `[truncated — .herm/instructions.md exceeds 16KB limit]` suffix. The LLM can still read the full file via `read_file` if needed.

## Success Criteria

- `buildSystemPrompt()` includes `## Project Instructions` section when `.herm/instructions.md` exists
- Both functions omit the section entirely when `.herm/instructions.md` is absent
- `scope: all` → instructions appear in main agent, implement sub-agents, and explore sub-agents
- `scope: implement` → instructions appear in main agent and implement sub-agents, not explore
- `scope: main` → instructions appear in main agent only
- No front-matter → behaves like `scope: all`
- Content truncated with warning when exceeding 16 KB
- All existing system prompt tests continue to pass

---

## Architecture: Load Once, Pass Through

The instructions file is read once in `startAgent()` and the result is passed down — prompt builders and sub-agents never touch the filesystem for instructions.

Flow:
1. `startAgent()` calls `loadProjectInstructions(workDir)` → returns `ProjectInstructions{Scope, Content}`
2. Passes content to `buildSystemPrompt()` (main agent always gets it regardless of scope)
3. Passes the struct to `NewSubAgentTool()` (new field on `SubAgentTool`)
4. When `SubAgentTool` spawns a sub-agent, it checks scope vs. mode and passes content (or empty string) to `buildSubAgentSystemPrompt()`

This means `buildSystemPrompt()` and `buildSubAgentSystemPrompt()` each receive an instructions string parameter — they don't load anything themselves. The scope check lives in `SubAgentTool` at the call site (`subagent.go:473`), keeping the prompt builders simple.

---

## Phase 1: Implementation

- [x] 1a: Add `ProjectInstructions` struct (with `Scope string` and `Content string`) and `loadProjectInstructions(workDir string) ProjectInstructions` function in a new file `cmd/herm/instructions.go`. Reads `.herm/instructions.md`, parses optional front-matter to extract `scope` (default `"all"`), trims whitespace from body. Returns zero value if file is absent or body is empty. Truncates content at 16 KB with a suffix if too large. Add a method `ContentForMode(mode string) string` on the struct that returns content if scope allows for the given mode (always for `"all"`, only for `"implement"` mode when scope is `"implement"`, empty for sub-agents when scope is `"main"`), and always returns content when mode is empty (main agent). Reuse the front-matter parsing approach from `skills.go:parseSkill()`.
- [x] 1b: Add `ProjectInstructions string` field to `PromptData` in `systemprompt.go`. Create `prompts/instructions.md` template that conditionally renders `## Project Instructions` when the field is non-empty. Add a `projectInstructions string` parameter to both `buildSystemPrompt()` and `buildSubAgentSystemPrompt()`, and populate `PromptData.ProjectInstructions` from it. Add `{{template "instructions" .}}` to both `prompts/system.md` (after skills) and `prompts/system_subagent.md` (at the end).
- [x] 1c: Wire into `startAgent()` in `agentui.go`. Call `loadProjectInstructions(workDir)` once (alongside skills loading at line 183-187). Pass `instructions.Content` to `buildSystemPrompt()`. Store the `ProjectInstructions` struct on `SubAgentTool` — add a field to the struct and a parameter to `NewSubAgentTool()`.
- [x] 1d: Wire into sub-agent spawning. In `SubAgentTool.Execute()` (around line 473), call `instructions.ContentForMode(mode)` and pass the result to `buildSubAgentSystemPrompt()`.
- [x] 1e: Add a brief mention in `prompts/role.md` in the "Project orientation" paragraph so the agent knows to respect project instructions when present.

## Phase 2: Tests

- [x] 2a: Unit tests for `loadProjectInstructions()` and `ContentForMode()` in `cmd/herm/instructions_test.go` — file with content and scope, file with no front-matter (scope defaults to `"all"`), file absent, file empty/whitespace, unknown scope value, file exceeding size cap (verify truncation). `ContentForMode` tests: scope `"all"` returns content for all modes, scope `"implement"` returns content for `""` and `"implement"` but empty for `"explore"`, scope `"main"` returns content for `""` but empty for `"implement"` and `"explore"`.
- [x] 2b: System prompt integration tests in `systemprompt_test.go` — verify `## Project Instructions` appears when a non-empty instructions string is passed, absent when empty string is passed. Test both `buildSystemPrompt()` and `buildSubAgentSystemPrompt()`.
