# Plan: Agent Skills, Dev Env Tool, and System Prompt

Date: 2026-03-05

## Goal

Improve the coding agent with:
1. Skills system — load skill definitions from `.cpsl/skills/` folder
2. Dev environment tool — modify Dockerfile, rebuild container on demand
3. Better system prompt — inspired by open-source coding agents
4. Consolidate tool set: bash (container), git (host), devenv (container rebuild), web_search

## Current State

- **Tools**: `BashTool` (container exec) and `GitTool` (host git) in `tools.go`
- **System prompt**: `systemprompt.go` — dynamically built from available tools, basic guidelines
- **Container**: `container.go` — Alpine-based, uses `Config.ContainerImage` (default `alpine:latest`), single container per session, workspace mounted
- **Agent**: `agent.go` — tool loop with approval flow, max 25 iterations, langdag persistence
- **Tool wiring**: `main.go:1338-1345` — tools built from container/worktree state, passed to `NewAgent`
- **Config**: `config.go` — has `ContainerImage` field already, stored at `~/.cpsl/config.json`

## Key Decisions

- Skills are markdown files in `.cpsl/skills/` — each file is a skill with a trigger description and content that gets injected into the system prompt when relevant
- The devenv tool modifies a `Dockerfile` in `.cpsl/`, builds it, and replaces the running container
- Web search tool uses the active provider's search capability (Grok has native X/web search; others can use a generic approach)
- Keep tool count minimal: bash, git, devenv, web_search

## Open Questions

- Should skills be auto-detected based on user message, or explicitly triggered? **Proposal**: Include all skill contents in the system prompt (they're short guidelines, not full programs). The LLM decides when to apply them.
- For web search on non-Grok providers, should we shell out to a search tool or use an API? **Proposal**: Start with Grok's native search (it's built into the API). For other providers, defer web search to a future iteration — just don't register the tool.
- Should `.cpsl/Dockerfile` be committed to the repo? **Proposal**: Yes, it lives in `.cpsl/` which is project-specific. The user can gitignore it if they want.

---

## Phase 1: Skills System

**Context**: Skills are `.md` files in `.cpsl/skills/` (project-local). Each skill file has a YAML front matter with `name` and `description`, followed by markdown content. The skill content is appended to the system prompt so the LLM knows about project-specific conventions.

- [x] 1a: Add `skills.go` — define `Skill` struct (`Name`, `Description`, `Content string`), `loadSkills(dir string) ([]Skill, error)` that reads all `.md` files from a directory, parses YAML front matter (name + description), and returns the skill list. Handle missing directory gracefully (return empty).
- [x] 1b: Integrate skills into system prompt — update `buildSystemPrompt` to accept `[]Skill` parameter. Append a `## Skills` section listing each skill's content. Update call site in `main.go`. Added `--display-system-prompts` CLI flag.
- [x] 1c: Add a default skill — create `.cpsl/skills/coding.md` as an example skill with general coding best practices (read before edit, test after changes, etc.). This also validates the loading works.
- [x] 1d: Tests for skills loading — test `loadSkills` with valid skills dir, empty dir, missing dir, malformed front matter.

## Phase 2: Dev Environment Tool

**Context**: Currently the container uses `Config.ContainerImage` (default `alpine:latest`). The devenv tool lets the LLM modify a Dockerfile at `.cpsl/Dockerfile`, build it, and hot-swap the running container. The tool should also detect if there's already a `Dockerfile` in the project root and offer to use it as a base.

- [x] 2a: Add `DevEnvTool` in `tools.go` — implements `Tool` interface. Input schema: `{action: "read"|"write"|"build", content?: string}`. `read` returns current Dockerfile contents (or states none exists). `write` writes content to `.cpsl/Dockerfile`. `build` builds the image and replaces the container. The tool needs access to `ContainerClient` and project paths.
- [x] 2b: Container rebuild support — add `Rebuild(dockerfilePath string, workspace string, mounts []MountSpec) error` method to `ContainerClient`. It builds the image (`docker build -t cpsl-custom-<id> -f <path> .`), stops current container, starts new one with the built image. Update `ContainerConfig` to track custom image name.
- [x] 2c: Detect existing Dockerfile — when `DevEnvTool` `read` action is called and no `.cpsl/Dockerfile` exists, check for `Dockerfile` in the project root. If found, mention it in the response so the LLM can suggest copying/adapting it.
- [x] 2d: Wire `DevEnvTool` into agent — register in `main.go` tool list (always available when container is ready). Add devenv section to system prompt in `systemprompt.go`.
- [x] 2e: Add a `devenv` skill in `.cpsl/skills/devenv.md` — instructs the agent to check for existing Dockerfiles, propose environment setup, and use the devenv tool when users want to install tools/languages.
- [ ] 2f: Tests for DevEnvTool — test read/write/build actions, Dockerfile detection, error cases (build failure, missing content for write).

## Phase 3: Improved System Prompt

**Context**: Current system prompt is functional but basic. Improve it with patterns from popular open-source agents (Aider, OpenHands, Claude Code) — focused on making the agent more effective at coding tasks without being overly verbose.

- [ ] 3a: Rewrite `buildSystemPrompt` — restructure into clear sections: Role & Capabilities, Tool Usage (per-tool), Coding Practices, Communication Style. Add guidance on: breaking down complex tasks, verifying changes work, asking clarifying questions when ambiguous, not over-engineering. Keep it concise — aim for ~1000 tokens total (excluding skills).
- [ ] 3b: Improve per-tool descriptions in tool `Definition()` methods — make descriptions more actionable and specific about when/how to use each tool. These are what the LLM sees in the tool schema.
- [ ] 3c: Tests — verify system prompt includes expected sections for different tool combinations, verify skills are included.

## Phase 4: Web Search Tool (Grok-only initial)

**Context**: When using Grok models, the API supports web/X search natively. Add a `WebSearchTool` that searches the web and returns results. Initially Grok-only; other providers can be added later.

- [ ] 4a: Add `WebSearchTool` in `tools.go` — implements `Tool` interface. Input: `{query: string, source?: "web"|"x"}`. For Grok provider, uses the Grok API's search feature. Returns formatted search results. The tool needs to know the active provider to decide availability.
- [ ] 4b: Conditional registration — only register `WebSearchTool` when using Grok provider. Update tool wiring in `main.go`.
- [ ] 4c: Add web_search section to system prompt — guidelines for when to search (unfamiliar APIs, recent docs, debugging obscure errors).
- [ ] 4d: Tests for WebSearchTool — test input parsing, provider check, error handling.

## Success Criteria

- Skills loaded from `.cpsl/skills/` and injected into system prompt
- Agent can modify dev environment by editing Dockerfile and rebuilding container mid-session
- Agent detects existing project Dockerfiles and suggests using them
- System prompt produces more effective agent behavior (qualitative)
- Web search works when using Grok models
- All new code has tests
- Existing tests still pass
