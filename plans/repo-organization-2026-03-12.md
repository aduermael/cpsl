# Repo Organization

Clean up the repo structure: remove junk, add README/LICENSE, move Go source out of root, and extract system prompts into maintainable markdown templates.

## Current state

- 34 `.go` files (source + tests) all at repo root, single `package main`
- `main.go` is 3823 lines — the App struct, TUI, input handling, rendering, agent orchestration, config UI
- Other files are focused: agent, config, container, history, markdown, models, skills, systemprompt, tools, tree, worktree (~100–500 lines each)
- Junk files at root: `bar2.txt` (empty), `hello.sh`, `main` (stale binary), `cpsl` (binary, already gitignored)
- System prompts are hardcoded string literals in `systemprompt.go` — conditional sections for tools, skills, personality, environment
- `dockerfiles.go` uses `//go:embed dockerfiles/base.Dockerfile`
- `simple-chat/` and `debug/` are separate main packages
- No README, no LICENSE

## Open questions

- Should `plans/` stay at repo root or move elsewhere? (Assume: keep at root — they're project docs, not code)
- Author name for LICENSE copyright line? (Use placeholder, user can fill in)

---

## Phase 1: Clean up junk files and .gitignore

- [x] 1a: Remove `bar2.txt`, `hello.sh`, and stale `main` binary from the repo
- [x] 1b: Update `.gitignore` to cover common Go artifacts (binaries in `cmd/`, `*.exe`, `.DS_Store` already covered)

## Phase 2: Add README and LICENSE

- [x] 2a: Add MIT `LICENSE` file with current year and placeholder author
- [x] 2b: Add `README.md` with project description, build instructions (`go build ./cmd/cpsl`), run instructions, and project structure overview

## Phase 3: Extract system prompts to markdown templates

The goal: move prompt text out of Go string literals into `.md` files that are easy to read and edit. Use Go `text/template` + `embed` to render them at runtime.

**Template structure:** One template file per major section (role, tools, practices, communication, personality, skills, environment). A master template that composes them. Conditional logic stays as `{{if}}` blocks in the templates — same structure as the current Go code, just in markdown.

**Template data:** A struct passed to `template.Execute` with fields like `HasBash`, `HasGit`, `HasDevenv`, `HasWebSearch`, `HasAgent`, `ContainerImage`, `WorkDir`, `Date`, `Personality`, `Skills`, etc.

**Embed location:** Templates live alongside `systemprompt.go` (wherever it ends up after Phase 4). Embedded via `//go:embed prompts/*.md` into a `embed.FS`.

- [x] 3a: Create prompt template files in `prompts/` directory — one file per section (role.md, tools.md, practices.md, communication.md, skills.md, environment.md) plus a master template that includes them
- [x] 3b: Rewrite `buildSystemPrompt` to parse and execute the templates from embedded FS, passing a data struct. The output must be identical to the current implementation
- [ ] 3c: Update `systemprompt_test.go` — existing tests should still pass (same output). Add a test that verifies template parsing succeeds

## Phase 4: Move Go source into `cmd/cpsl/`

Move all `package main` source files from repo root into `cmd/cpsl/`. This is a pure file move — no package refactoring, no export changes, no interface extraction. Everything stays `package main`.

**What moves:**
- All `*.go` files at root → `cmd/cpsl/`
- `dockerfiles/` → `cmd/cpsl/dockerfiles/` (required: `//go:embed` paths are relative to source file)
- `simple-chat/` → `cmd/simple-chat/`
- `debug/` → `cmd/debug/`

**What stays at root:** `go.mod`, `go.sum`, `.gitignore`, `README.md`, `LICENSE`, `plans/`, `.cpsl/`, `.claude/`

**Embed paths:** After the move, `dockerfiles.go`'s embed directive still works (relative path `dockerfiles/base.Dockerfile` stays the same since the dir moves too). Same for prompt templates if they move to `cmd/cpsl/prompts/`.

**Build command changes:** `go build .` → `go build ./cmd/cpsl`

- [ ] 4a: Create `cmd/cpsl/` and move all root `.go` files and `dockerfiles/` into it. Move `simple-chat/` to `cmd/simple-chat/` and `debug/` to `cmd/debug/`
- [ ] 4b: Verify `go build ./cmd/cpsl`, `go test ./cmd/cpsl/`, `go build ./cmd/simple-chat`, `go build ./cmd/debug/` all succeed. Fix any broken embed paths or imports
- [ ] 4c: Update `.gitignore` for new binary output paths. Update README build instructions if needed

## Phase 5: Final verification

- [ ] 5a: Run full test suite (`go test ./...`), verify build, confirm no leftover files at root. Clean up any stale references in `.cpsl/` config or `.claude/` memory
