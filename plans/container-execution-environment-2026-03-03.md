# Container Execution Environment

**Goal:** Run commands in an isolated Linux container (Apple Containerization framework) with the project mounted via a git worktree. Each CLI session gets its own worktree in `~/.cpsl/worktrees/`; clean worktrees are auto-reused, dirty ones prompt the user. Add `/exec` command to test the setup.

## Codebase Context

- **main.go** — BubbleTea app with modes (chat/config/model), slash commands (`/config`, `/model`), async `Init()` fetches, `handleCommand()` dispatches commands
- **config.go** — `Config` struct persisted to `.cpsl/config.json`, forward-compatible merge on load
- **lyfegame/local-container** (private repo) — Swift service (`container-service`) managing Linux VMs via Apple Containerization framework. Communicates over Unix socket using JSON-RPC. Methods: `container.start` (params: workspace, mounts), `container.exec` (params: command, timeout), `container.stop`, `container.status`. The service spawns as a subprocess, creates a socket, and waits for requests. Each request is a newline-delimited JSON-RPC 2.0 message over the socket.

## Architecture

```
cpsl (Go CLI, this repo)
    │
    │  ContainerClient (Go, new file)
    │  Spawns subprocess, JSON-RPC over Unix socket
    │
    ▼
container-service (Swift binary, from lyfegame/local-container)
    │
    │  Apple Containerization.framework
    │
    ▼
Alpine Linux VM (arm64)
    │  virtio-fs mount at /workspace → git worktree in ~/.cpsl/worktrees/
    ▼
```

## Design Decisions

- **Go client**: Port the Rust JSON-RPC client logic to Go. Same protocol — newline-delimited JSON-RPC 2.0 over a Unix socket. The Go client spawns the `container-service` binary as a subprocess.
- **One container per CLI session**: No session multiplexing needed. The client manages a single container lifecycle.
- **Project identity**: A random UUID stored in `<project-root>/.cpsl/project.json`. Generated once on first run. Survives folder renames, remote URL changes, etc.
- **Worktree directory**: `~/.cpsl/worktrees/<project-uuid>/<worktree-name>/`.
- **Session tracking**: `.cpsl-lock` file in each worktree dir containing the PID. On startup, check if PID is alive to detect stale locks.
- **Config**: `ContainerServiceBin` and `ContainerImagePath` fields with defaults (`~/.cpsl/service/container-service` and `~/.cpsl/service/oci-image`).
- **Async boot**: Container starts in background after worktree selection. `/exec` shows "container starting..." if not ready yet.
- **Cleanup**: Container destroyed on exit. Worktree persists (unlocked).

## Failure Modes

- Container service binary or OCI image not found → show clear error message with setup instructions, `/exec` returns error
- Container fails to start (timeout, crash) → `containerErrMsg` shown in viewport, allow user to retry or continue without container
- Not in a git repo → skip worktree, mount cwd directly into container
- `.cpsl/` dir not writable → fall back to mounting cwd directly
- Stale lock files → detect by checking if PID process exists, auto-clean stale locks
- Socket path too long (>104 bytes) → use hash-based short socket names in temp dir (same approach as Rust client)

## Phase 1: Container Client (Go)

- [ ] 1a: Add `container.go` with types: `ContainerConfig` (ServiceBinary, ImagePath, SocketDir paths), `MountSpec` (Source, Destination, ReadOnly), `CommandResult` (Stdout, Stderr, ExitCode), `ContainerStatus` (State, Uptime), and `ContainerError` (typed error with codes: BinaryNotFound, ImageNotFound, SpawnFailed, Socket, Protocol, Service, Timeout). Add private JSON-RPC request/response structs matching the protocol.
- [ ] 1b: Implement `ContainerClient` — `NewContainerClient(config)`, `IsAvailable() bool`, `Start(workspace string, mounts []MountSpec) error` (spawns subprocess with `--socket-path` and `--image-path` args, polls for socket up to 30s, sends `container.start` with workspace/mounts), `Exec(command string, timeout int) (CommandResult, error)` (sends `container.exec`), `Stop() error` (sends `container.stop`, kills subprocess), `Status() (ContainerStatus, error)`. Unix socket dial + write request + read response per call (same pattern as Rust client).
- [ ] 1c: Tests in `container_test.go` — mock Unix socket server that speaks JSON-RPC. Test: full start/exec/stop lifecycle, error responses from service, binary-not-found, serialization round-trips. No real container needed.

## Phase 2: Worktree Manager

- [ ] 2a: Add `worktree.go` with: `WorktreeInfo` struct (Path, Branch, Clean bool, Active bool), `ensureProjectID(repoRoot string) (string, error)` (reads UUID from `<repoRoot>/.cpsl/project.json`, generates and writes one if missing), `worktreeBaseDir(projectUUID string) string` (returns `~/.cpsl/worktrees/<projectUUID>/`), `createWorktree(repoRoot, baseDir string) (string, error)` (runs `git worktree add` with generated branch name like `cpsl-<timestamp>`), `listWorktrees(baseDir string) ([]WorktreeInfo, error)` (scans base dir, checks `git status --porcelain` for clean/dirty, checks lock for active).
- [ ] 2b: Session tracking and selection: `lockWorktree(path string, pid int) error`, `unlockWorktree(path string) error`, `isWorktreeLocked(path string) (bool, int)` (returns locked + PID, checks if PID alive). `selectWorktree(repoRoot string) (selected string, dirty []WorktreeInfo, err error)` — auto-selects first clean inactive worktree, or returns dirty list for user prompt. If no worktrees exist, creates one.
- [ ] 2c: Tests in `worktree_test.go` — uses temp git repos. Test: project UUID generation and persistence (read back same UUID), worktree creation, clean/dirty detection (stage a file to make dirty), lock/unlock lifecycle, stale lock cleanup (write a dead PID), auto-selection picks clean over dirty.

## Phase 3: App Integration — `/exec`, Startup, Shutdown

- [ ] 3a: Config + model fields: add `ContainerServiceBin`, `ContainerImagePath` to `Config` with defaults. Add `/exec` to `commands` slice. Add fields to app model: `container *ContainerClient`, `worktreePath string`, `containerReady bool`, `containerErr error`. Add message types: `containerReadyMsg`, `containerErrMsg`, `execResultMsg{CommandResult, error}`.
- [ ] 3b: Startup wiring: in `Init()`, run worktree selection. If a clean worktree is auto-selected (or cwd if not a git repo), fire async `tea.Cmd` to create client, call `Start()`, return `containerReadyMsg`. Handle `containerReadyMsg`/`containerErrMsg` in `Update()`.
- [ ] 3c: `/exec` handler in `handleCommand()`: parse command text after `/exec `. If container not ready, show info/error message. Otherwise fire async `tea.Cmd` calling `client.Exec(command, 120)`, returns `execResultMsg`. Handle in `Update()` — append stdout, stderr, exit code to messages (use `msgSuccess` for exit 0, `msgError` for non-zero).
- [ ] 3d: Shutdown: on `ctrl+c` / `tea.Quit`, if container is running, call `Stop()` and `unlockWorktree()`. The container client's `Stop()` sends `container.stop` then kills the subprocess.
- [ ] 3e: Tests: `/exec echo hello` flow with mock container (inject a test `ContainerClient`), container-not-ready message, shutdown cleanup.

## Phase 4: Dirty Worktree Selection UI

- [ ] 4a: Add `modeWorktree` app mode and `worktreeSelector` component in new `worktreelist.go`. List of options: one entry per dirty worktree ("Hook on: <branch> — N uncommitted changes"), plus "Create new worktree" and "Delete worktree: <branch>" entries. Up/down cursor navigation, enter to select. Styled like `modelList`.
- [ ] 4b: Wire into startup: if `selectWorktree()` returns dirty worktrees and no clean option, enter `modeWorktree` before `modeChat`. On "hook on" → use that worktree, boot container. On "create new" → `createWorktree()`, boot container. On "delete" → `git worktree remove`, refresh list.
- [ ] 4c: Tests for worktree selector: navigation, selection triggers container boot, delete removes and refreshes.

## Open Questions

- Should we vendor/submodule the `local-container` Swift service, or expect users to build it separately? (For now: assume pre-built at config paths.)
- Should `/exec` support streaming output, or wait for completion? (For now: wait for completion, show result.)
- Maximum container resource allocation (CPU, memory)? (Defaults in container-service: 2 CPUs, 512MB RAM.)

## Success Criteria

- `/exec echo hello` runs inside a Linux container and displays `hello` in the viewport
- Container mounts a git worktree from `~/.cpsl/worktrees/`, not the original project directory
- Clean worktrees are automatically reused across CLI sessions without prompting
- Dirty worktrees prompt the user with hook-on / create-new / delete options
- Exiting the CLI stops the container but preserves the worktree
- Container service binary/image paths are configurable in `/config`
- All existing tests continue to pass
- New tests cover container client, worktree management, `/exec` flow, and worktree selection UI
