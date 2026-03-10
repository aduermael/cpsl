# Plan: DevEnv Improvements + Worktree/Status Bar Rework

Date: 2026-03-10

## Goal

DevEnv tool improvements:
1. Named Dockerfiles — agent picks a descriptive name (e.g. "go", "python"), stored as `.cpsl/<name>.Dockerfile`
2. Project-scoped image names — `cpsl-<shortProjectID>:<name>` instead of `cpsl-custom-<randomID>`, avoids cross-project collisions and is more readable
3. Config display fix — after build, update `Config.ContainerImage` so settings shows the actual running image instead of "alpine:latest"

Workspace and status bar rework:
4. Don't use worktrees by default — start in the repo's working directory. `/worktrees` command offers to create one (user enters a name).
5. Status bar: replace `/b <branch>` with `branch: <branch>`, add `container: <container-id>` line, show `worktree: <name>` only when actually in a worktree.

## Current State

- **DevEnvTool** (`tools.go:213-328`): `read`/`write`/`build` actions, single Dockerfile at `.cpsl/Dockerfile`
- **Rebuild** (`container.go:240-289`): Generates random image name `cpsl-custom-<randomID>`, updates `ContainerClient.config.Image` but never touches `Config.ContainerImage`
- **Settings display** (`main.go:2259`): Reads from `Config.ContainerImage` which stays empty after a rebuild, so it always shows `defaultContainerImage` ("alpine:latest")
- **Project ID** (`worktree.go:30-62`): UUID stored in `.cpsl/project.json`, available via `ensureProjectID(repoRoot)`
- **System prompt** (`systemprompt.go:76-82`): References `.cpsl/Dockerfile` (single name)
- **Workspace startup** (`main.go:659-684`): `resolveWorkspaceCmd` calls `selectWorktree(repoRoot)` which auto-creates a worktree if none exist, or picks a clean one. Always uses a worktree.
- **Status bar** (`main.go:1081-1113`): Line 1 shows `/b <branch>` + cost + progress bar. Line 2 shows `/w <worktree-name>` (always, since worktree is always used).
- **Container ID** (`container.go:160-163`): Available via `ContainerClient.ContainerID()` but not shown in status bar.
- **`/worktrees` command** (`main.go:2163-2217`): Lists existing worktrees for selection, no option to create new ones.

## Key Decisions

### DevEnv
- **Name field**: Optional in tool input, defaults to "custom". Agent picks a descriptive name. Only `[a-z0-9-]` characters allowed.
- **Dockerfile naming**: `.cpsl/<name>.Dockerfile` — e.g. `.cpsl/go.Dockerfile`. Backwards compat: `read` action also checks for legacy `.cpsl/Dockerfile` and migrates it.
- **Image naming**: `cpsl-<first8 of projectUUID>:<name>` — deterministic, so rebuilds reuse the same tag (no image garbage). Example: `cpsl-83fb9a53:go`.
- **Config sync**: DevEnvTool gets an `onRebuild func(imageName string)` callback. After successful build, it calls this to update `Config.ContainerImage` and save to disk. Main.go wires this up in `startAgent()`.
- **Rebuild signature change**: `Rebuild` takes the desired image name as a parameter instead of generating a random one internally. This makes the caller responsible for naming.

### Workspace / Status Bar
- **No worktrees by default**: `resolveWorkspaceCmd` should use the repo root (or cwd) directly. No `selectWorktree` call on startup.
- **`/worktrees` creates**: The command should offer a "New worktree" option at the top of the menu. When selected, prompt the user for a name (text input), then create the worktree with that name.
- **Status bar format**: Three lines (all dim), only shown when data is available:
  - `branch: <branch-name>` — always shown (from git)
  - `container: <short-container-id>` — shown when container is ready (first 12 chars of docker container ID)
  - `worktree: <worktree-name>` — only shown when `worktreePath` differs from repo root (i.e. actually in a worktree)
- Cost and progress bar stay on the `branch:` line (right-aligned, same as current).

## Open Questions

- Should we clean up old `.cpsl/Dockerfile` (legacy name) when writing a new named one? **Proposal**: Yes, if a legacy `Dockerfile` exists and we're writing `<name>.Dockerfile`, remove the old one and mention it in the response.

---

## Phase 1: Rebuild Signature and Image Naming

**Context**: `container.go` `Rebuild` currently generates a random image name internally. Change it to accept the image name as a parameter so the caller controls naming. Also need to pass project ID into DevEnvTool.

- [x] 1a: Change `Rebuild(dockerfilePath, workspace string, mounts []MountSpec) error` to `Rebuild(imageName, dockerfilePath, workspace string, mounts []MountSpec) error` in `container.go`. Remove internal `cpsl-custom-<randomID>` generation. Update the one call site in `tools.go` `buildAndReplace()`.
- [x] 1b: Add `projectID string` and `onRebuild func(imageName string)` fields to `DevEnvTool` struct. Update `NewDevEnvTool` to accept these. Update the call site in `main.go` `startAgent()` to pass the project ID (from `ensureProjectID(gitRepoRoot())`) and a callback that sets `a.config.ContainerImage = imageName` and calls `saveConfig(a.config)`.

## Phase 2: Named Dockerfiles

**Context**: Currently one Dockerfile at `.cpsl/Dockerfile`. Change to `.cpsl/<name>.Dockerfile` with a `name` field in the tool input.

- [ ] 2a: Add `name` field to `devenvInput` struct and tool `InputSchema`. Optional, default "custom". Validate: lowercase alphanumeric plus hyphens only, max 30 chars.
- [ ] 2b: Update `dockerfilePath()` to use the name: `.cpsl/<name>.Dockerfile`. Update `readDockerfile` — if no named Dockerfile exists, also check for legacy `.cpsl/Dockerfile` and mention it can be migrated. Update `writeDockerfile` — write to `<name>.Dockerfile`, remove legacy `Dockerfile` if it exists.
- [ ] 2c: Update `buildAndReplace()` — compute image name as `cpsl-<first8 of projectID>:<name>`, call `Rebuild(imageName, ...)`. On success, call `onRebuild(imageName)`. Handle missing projectID gracefully (fall back to `cpsl-local:<name>`).

## Phase 3: No Worktrees by Default

**Context**: Currently `resolveWorkspaceCmd` always uses worktrees (auto-creates if none exist). Change to use the repo working directory by default. Worktrees become opt-in via `/worktrees`.

- [ ] 3a: Simplify `resolveWorkspaceCmd` — remove the `selectWorktree` call. Use the repo root as workspace (or cwd if not in a git repo). No worktree creation on startup. Remove worktree locking on startup (only lock when user explicitly switches to a worktree via `/worktrees`).
- [ ] 3b: Update `/worktrees` command — add a "New worktree" entry at the top of the menu. When selected, prompt the user for a worktree name (reuse the input buffer or show a simple text prompt), then call `createWorktree` with that name. After creation, switch to it (update `a.worktreePath`, `a.status`, reboot container with new workspace).

## Phase 4: Status Bar Rework

**Context**: Status bar currently shows `/b <branch>` and `/w <worktree>`. Change format and add container info.

- [ ] 4a: Replace `/b <branch>` with `branch: <branch>` in the status bar rendering (`main.go:1081-1108`). Keep cost + progress bar on the same line, right-aligned.
- [ ] 4b: Add `container: <short-id>` line below the branch line. Show first 12 chars of `a.container.ContainerID()`. Only render when `a.containerReady && a.container != nil`.
- [ ] 4c: Change worktree line from `/w <name>` to `worktree: <name>`. Only show when the workspace is actually a worktree (i.e. `a.worktreePath` is under `~/.cpsl/worktrees/`, not the repo root). Add a helper or field to track whether current workspace is a worktree.

## Phase 5: Tests and System Prompt

- [ ] 5a: Update all tests in `devenv_test.go` — new `NewDevEnvTool` signature (add projectID + onRebuild), named Dockerfile paths (`<name>.Dockerfile`), verify onRebuild callback is called after build, test name validation, test legacy Dockerfile detection.
- [ ] 5b: Update system prompt in `systemprompt.go` — mention the `name` parameter and that Dockerfiles are stored as `.cpsl/<name>.Dockerfile`.

## Success Criteria

- `go test ./...` passes
- After agent builds a Dockerfile, settings shows the actual image name (e.g. `cpsl-83fb9a53:go`)
- Dockerfiles are stored as `.cpsl/<name>.Dockerfile`
- Docker images are tagged as `cpsl-<shortID>:<name>` — deterministic, no random garbage
- Legacy `.cpsl/Dockerfile` detected and handled gracefully
- App starts without creating/using worktrees — works in the repo root
- `/worktrees` offers to create a new worktree with a user-provided name
- Status bar shows `branch:`, `container:`, and `worktree:` (only when applicable)
