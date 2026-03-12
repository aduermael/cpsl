# Base Container Tooling for File Exploration

Ship a debian base Dockerfile with essential exploration tools (git, ripgrep, find, tree). Build it automatically on first startup so the agent can navigate codebases immediately — no devenv round-trip needed. Update the system prompt with a layered file exploration strategy.

## Codebase Context

- **Default image**: `alpine:latest` (config.go:95). Busybox-only — limited `grep`, no `git`, no `tree`.
- **Container startup**: `bootContainerCmd()` (main.go:847) runs in a goroutine, sends status messages via channel, then sends `containerReadyMsg`. Currently does: check docker → start container with raw image.
- **DevEnvTool**: Reads/writes `.cpsl/Dockerfile`, builds and hot-swaps via `container.Rebuild()`. The `readDockerfile()` method already checks for named `.cpsl/*.Dockerfile` and root `Dockerfile`.
- **System prompt** (systemprompt.go:74): Bash tool section says `"Explore files with grep, find, cat. Run tests after changes."` — no search strategy guidance.
- **Devenv skill** (.cpsl/skills/devenv.md): Recommends `debian:bookworm-slim` or `alpine:3`. Shows runtime installation patterns.
- **No `//go:embed` used** for Dockerfiles currently.
- **`containerConfig()`** (config.go:104): Returns `ContainerConfig{Image}` where Image defaults to `alpine:latest` when not set.

## Design

### Embedded base Dockerfile

Embed one Dockerfile in the binary using `//go:embed`:
- `dockerfiles/base.Dockerfile` — debian:bookworm-slim + essential tools

```dockerfile
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        git tree ca-certificates ripgrep \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /workspace
```

GNU grep and findutils are already in debian:bookworm-slim. Ripgrep (`rg`) is added as the primary code search tool — faster than grep on large repos, respects .gitignore, skips binary files, and produces cleaner output. This is the same engine Claude Code uses under the hood. GNU grep remains available as a fallback (~2MB added).

No Alpine template — Debian covers the common case and is more versatile. Users who want Alpine can create their own `.cpsl/Dockerfile`.

### Auto-build on startup

In `bootContainerCmd()`, before starting the container:
1. Check if `.cpsl/Dockerfile` exists in the workspace
2. If not, write the embedded base template to `.cpsl/Dockerfile`
3. Build the image from it (same `docker build` logic as `DevEnvTool.buildAndReplace()`)
4. Start the container with the built image

If `.cpsl/Dockerfile` already exists (user has customized their env), build from that instead of the raw default. This means every container always starts from a built Dockerfile, never from a raw base image.

The status messages update accordingly: "checking docker…" → "building image…" → "starting…" → ready.

### Change default image constant

Change `defaultContainerImage` from `"alpine:latest"` to `"debian:bookworm-slim"` as the fallback. This only matters if the build step is skipped for some reason (e.g., Docker build fails, fall back to raw image).

### System prompt: file exploration strategy

Replace the bash tool's `"Explore files with grep, find, cat"` line with a layered strategy:
1. Structure first — `tree` or `find` (filenames only, cheap)
2. Content search — `rg` (ripgrep) as the primary search tool: fast, .gitignore-aware, recursive by default. Fall back to `grep -rn` if needed
3. Read selectively — `cat`/`head`/`tail` on specific files (expensive)
4. History — `git log`/`git blame` when understanding changes matters

### DevEnvTool: template awareness

When `devenv read` finds no `.cpsl/Dockerfile`, the response already says "No .cpsl/Dockerfile exists yet." But now the auto-build writes one on startup, so this case only occurs if the user deletes it mid-session. Keep the existing behavior — no change needed here.

Update the devenv skill doc to note that the base image includes exploration tools and that Debian is the default.

## Failure Modes

- **First launch is slower** due to `docker build`: The build installs ~4 packages on debian slim, typically <15s. Status message "building image…" keeps the user informed. Subsequent launches reuse the cached image.
- **Docker build fails on startup**: Fall back to starting with the raw `debian:bookworm-slim` image (degraded — no git/tree, but still functional). Log the error via status message.
- **User has existing `.cpsl/Dockerfile`**: Respected — we build from theirs, not the template. No overwrite.
- **`.cpsl/Dockerfile` gets committed to git**: This is fine and expected — it's the project's dev environment definition.

## Phase 1: Embed base Dockerfile and auto-build on startup
- [ ] 1a: Create `dockerfiles/base.Dockerfile` (debian:bookworm-slim + git, tree, ca-certificates, ripgrep, WORKDIR /workspace)
- [ ] 1b: Add `dockerfiles.go` with `//go:embed dockerfiles/base.Dockerfile` exposing `var BaseDockerfile string`
- [ ] 1c: Change `defaultContainerImage` in config.go from `"alpine:latest"` to `"debian:bookworm-slim"`
- [ ] 1d: In `bootContainerCmd()`, after docker-available check: if no `.cpsl/Dockerfile` exists, write the embedded template there. Then build the image from `.cpsl/Dockerfile` (status: "building image…"). Start the container with the built image. On build failure, fall back to starting with the raw default image

## Phase 2: System prompt and devenv skill updates
- [ ] 2a: Replace the bash tool's `"Explore files with grep, find, cat"` line with layered exploration guidance (structure → content with `rg` as primary tool → read → history), ~4-5 lines
- [ ] 2b: Update devenv skill doc to note Debian is the default base, exploration tools are pre-installed, and Alpine is available for advanced users who create their own Dockerfile
- [ ] 2c: Add a test verifying the exploration guidance appears in the system prompt when bash tool is present

## Success Criteria
- Fresh project with no `.cpsl/Dockerfile`: startup writes the template, builds it, container has `git`, `rg`, `grep`, `find`, `tree` working immediately
- Project with existing `.cpsl/Dockerfile`: startup builds from the user's file, template is not written
- System prompt includes layered exploration strategy
- `docker build` failure falls back gracefully to raw image start
