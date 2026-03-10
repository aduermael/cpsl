# Plan: DevEnv Tool — Named Dockerfiles, Project-Scoped Images, Config Sync

Date: 2026-03-10

## Goal

Three improvements to the devenv tool:
1. Named Dockerfiles — agent picks a descriptive name (e.g. "go", "python"), stored as `.cpsl/<name>.Dockerfile`
2. Project-scoped image names — `cpsl-<shortProjectID>:<name>` instead of `cpsl-custom-<randomID>`, avoids cross-project collisions and is more readable
3. Config display fix — after build, update `Config.ContainerImage` so settings shows the actual running image instead of "alpine:latest"

## Current State

- **DevEnvTool** (`tools.go:213-328`): `read`/`write`/`build` actions, single Dockerfile at `.cpsl/Dockerfile`
- **Rebuild** (`container.go:240-289`): Generates random image name `cpsl-custom-<randomID>`, updates `ContainerClient.config.Image` but never touches `Config.ContainerImage`
- **Settings display** (`main.go:2259`): Reads from `Config.ContainerImage` which stays empty after a rebuild, so it always shows `defaultContainerImage` ("alpine:latest")
- **Project ID** (`worktree.go:30-62`): UUID stored in `.cpsl/project.json`, available via `ensureProjectID(repoRoot)`
- **System prompt** (`systemprompt.go:76-82`): References `.cpsl/Dockerfile` (single name)

## Key Decisions

- **Name field**: Optional in tool input, defaults to "custom". Agent picks a descriptive name. Only `[a-z0-9-]` characters allowed.
- **Dockerfile naming**: `.cpsl/<name>.Dockerfile` — e.g. `.cpsl/go.Dockerfile`. Backwards compat: `read` action also checks for legacy `.cpsl/Dockerfile` and migrates it.
- **Image naming**: `cpsl-<first8 of projectUUID>:<name>` — deterministic, so rebuilds reuse the same tag (no image garbage). Example: `cpsl-83fb9a53:go`.
- **Config sync**: DevEnvTool gets an `onRebuild func(imageName string)` callback. After successful build, it calls this to update `Config.ContainerImage` and save to disk. Main.go wires this up in `startAgent()`.
- **Rebuild signature change**: `Rebuild` takes the desired image name as a parameter instead of generating a random one internally. This makes the caller responsible for naming.

## Open Questions

- Should we clean up old `.cpsl/Dockerfile` (legacy name) when writing a new named one? **Proposal**: Yes, if a legacy `Dockerfile` exists and we're writing `<name>.Dockerfile`, remove the old one and mention it in the response.

---

## Phase 1: Rebuild Signature and Image Naming

**Context**: `container.go` `Rebuild` currently generates a random image name internally. Change it to accept the image name as a parameter so the caller controls naming. Also need to pass project ID into DevEnvTool.

- [ ] 1a: Change `Rebuild(dockerfilePath, workspace string, mounts []MountSpec) error` to `Rebuild(imageName, dockerfilePath, workspace string, mounts []MountSpec) error` in `container.go`. Remove internal `cpsl-custom-<randomID>` generation. Update the one call site in `tools.go` `buildAndReplace()`.
- [ ] 1b: Add `projectID string` and `onRebuild func(imageName string)` fields to `DevEnvTool` struct. Update `NewDevEnvTool` to accept these. Update the call site in `main.go` `startAgent()` to pass the project ID (from `ensureProjectID(gitRepoRoot())`) and a callback that sets `a.config.ContainerImage = imageName` and calls `saveConfig(a.config)`.

## Phase 2: Named Dockerfiles

**Context**: Currently one Dockerfile at `.cpsl/Dockerfile`. Change to `.cpsl/<name>.Dockerfile` with a `name` field in the tool input.

- [ ] 2a: Add `name` field to `devenvInput` struct and tool `InputSchema`. Optional, default "custom". Validate: lowercase alphanumeric plus hyphens only, max 30 chars.
- [ ] 2b: Update `dockerfilePath()` to use the name: `.cpsl/<name>.Dockerfile`. Update `readDockerfile` — if no named Dockerfile exists, also check for legacy `.cpsl/Dockerfile` and mention it can be migrated. Update `writeDockerfile` — write to `<name>.Dockerfile`, remove legacy `Dockerfile` if it exists.
- [ ] 2c: Update `buildAndReplace()` — compute image name as `cpsl-<first8 of projectID>:<name>`, call `Rebuild(imageName, ...)`. On success, call `onRebuild(imageName)`. Handle missing projectID gracefully (fall back to `cpsl-local:<name>`).

## Phase 3: Tests and System Prompt

- [ ] 3a: Update all tests in `devenv_test.go` — new `NewDevEnvTool` signature (add projectID + onRebuild), named Dockerfile paths (`<name>.Dockerfile`), verify onRebuild callback is called after build, test name validation, test legacy Dockerfile detection.
- [ ] 3b: Update system prompt in `systemprompt.go` — mention the `name` parameter and that Dockerfiles are stored as `.cpsl/<name>.Dockerfile`.

## Success Criteria

- `go test ./...` passes
- After agent builds a Dockerfile, settings shows the actual image name (e.g. `cpsl-83fb9a53:go`)
- Dockerfiles are stored as `.cpsl/<name>.Dockerfile`
- Docker images are tagged as `cpsl-<shortID>:<name>` — deterministic, no random garbage
- Legacy `.cpsl/Dockerfile` detected and handled gracefully
