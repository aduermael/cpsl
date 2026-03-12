# Base Container Tooling for File Exploration

Ensure every container starts with essential file exploration tools (git, grep, find, tree) so the agent can navigate codebases effectively without needing a devenv build first. Provide base Dockerfile templates for alpine and debian, and update the system prompt with a layered file exploration strategy inspired by Claude Code.

## Codebase Context

- **Default image**: `alpine:latest` (config.go:95). Busybox-only — limited `grep` (no `-r` reliably, no `--include`), limited `find`, no `git`, no `tree`.
- **Debian bookworm-slim**: Has GNU grep and GNU find, but no `git`, no `tree`.
- **Container startup**: `bootContainerCmd()` in main.go starts the container with `sleep infinity`. The image is either the default or whatever `config.ContainerImage` points to.
- **DevEnvTool**: Reads/writes `.cpsl/Dockerfile`, builds and hot-swaps. When no Dockerfile exists, agent starts with the raw base image.
- **System prompt** (systemprompt.go): Bash tool section currently says: `"Explore files with grep, find, cat. Run tests after changes."` — no guidance on search strategy.
- **Devenv skill** (.cpsl/skills/devenv.md): Recommends `debian:bookworm-slim` or `alpine:3` as base images. Shows how to install runtimes but doesn't mention exploration tools.
- **No embedded files**: The binary doesn't use `//go:embed` for Dockerfiles currently.

## What's missing

1. **No git in either base image** — the agent can't explore commit history, blame, or diff within the container (the host `git` tool exists but the container can't use it for file-level exploration like `git log -p -- path`).
2. **Alpine's busybox grep is weak** — no `--include`, no `--exclude-dir`, limited regex. GNU grep is needed for effective code search.
3. **No tree** — the agent has no quick way to see directory structure.
4. **No search strategy guidance** — the system prompt doesn't teach the layered approach (structure → content → file read) that makes Claude Code effective.
5. **Every new project starts from scratch** — the agent must go through devenv read/write/build just to get basic exploration tools, wasting a tool round-trip before it can even look at code.

## Design

### Embedded base Dockerfile templates

Embed two Dockerfile templates in the binary using `//go:embed`:
- `dockerfiles/alpine.Dockerfile` — alpine:3 + essential tools
- `dockerfiles/debian.Dockerfile` — debian:bookworm-slim + essential tools

Both templates install: `git`, GNU `grep`, GNU `find` (findutils), `tree`, and set `WORKDIR /workspace`.

**Alpine template:**
```dockerfile
FROM alpine:3
RUN apk add --no-cache git grep findutils tree
WORKDIR /workspace
```
(`grep` package = GNU grep, `findutils` = GNU find, replacing busybox versions)

**Debian template:**
```dockerfile
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        git tree ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /workspace
```
(GNU grep and findutils already included in slim; git and tree are the only additions. ca-certificates needed for HTTPS git clones and later wget/curl use.)

### DevEnvTool reads templates when no Dockerfile exists

When `devenv read` is called and no `.cpsl/Dockerfile` exists, include the embedded base templates in the response so the agent can pick one as a starting point. This replaces the current bare "No .cpsl/Dockerfile exists yet" message.

### System prompt: file exploration strategy

Replace the single line `"Explore files with grep, find, cat"` with a layered strategy section that guides the agent to:
1. **Discover structure first** — `tree` or `find` (cheap, filenames only)
2. **Search content** — `grep -rn` with `--include` filters (medium, matching lines only)
3. **Read selectively** — `cat`/`head`/`tail` only for files identified above (expensive, full content)
4. **Use git for history** — `git log`, `git blame` when understanding changes matters

### Devenv skill: mention base templates

Update the skill doc to reference the embedded templates so the agent knows they exist and uses them as starting points.

## Failure Modes

- **Alpine `grep` package conflicts with busybox**: Not an issue — `apk add grep` replaces the busybox symlink cleanly.
- **Embedded files increase binary size**: Negligible — two small Dockerfiles add <1KB.
- **Agent ignores templates**: Mitigated by showing them in `devenv read` output and referencing them in the system prompt.
- **Stale pinned versions**: Templates pin to `alpine:3` and `debian:bookworm-slim` which are rolling tags — acceptable for base exploration tools. Runtime versions are the user's responsibility via devenv.

## Open Questions

- Should we pre-build these base images on first run so the initial container already has the tools? This would mean running `docker build` during startup instead of using `alpine:latest` directly. Faster agent experience but slower first launch. **Decision deferred** — start with templates that the agent uses on first devenv interaction; consider pre-building later if the extra round-trip is annoying.

## Phase 1: Embed base Dockerfile templates
- [ ] 1a: Create `dockerfiles/alpine.Dockerfile` and `dockerfiles/debian.Dockerfile` with essential exploration tools (git, GNU grep, findutils, tree)
- [ ] 1b: Add `//go:embed` in a new `dockerfiles.go` file to expose the templates as `var AlpineDockerfile, DebianDockerfile string`

## Phase 2: Surface templates in DevEnvTool
- [ ] 2a: When `devenv read` finds no `.cpsl/Dockerfile`, include both embedded templates in the response with labels, so the agent can choose one as a starting point
- [ ] 2b: Update the devenv skill doc (`.cpsl/skills/devenv.md`) to mention the base templates and that they include exploration essentials (git, grep, find, tree)

## Phase 3: System prompt file exploration guidance
- [ ] 3a: Replace the bash tool's `"Explore files with grep, find, cat"` line with a layered exploration strategy: structure (tree/find) → content (grep -rn --include) → read (cat/head/tail) → history (git log/blame). Keep it concise — 4-5 lines max
- [ ] 3b: Add a test in `systemprompt_test.go` verifying the exploration guidance appears in the generated prompt when bash tool is present

## Success Criteria
- `devenv read` on a fresh project shows both base templates with exploration tools listed
- An agent using the alpine template can immediately run `git log`, `grep -rn`, `find -name`, and `tree` after a single devenv build
- The system prompt guides a layered search strategy (structure → content → read → history)
- Both templates build successfully on linux/amd64
