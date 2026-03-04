# Real TTY Shell Session

**Goal:** Replace the current command-by-command shell mode with a real interactive TTY session that supports tab completion, cd persistence, command history, and full terminal control.

**Builds on:** `ui-commands-and-shell-mode-2026-03-04.md`, `ui-polish-2026-03-04.md`

## Context

Currently `/shell` enters a custom `modeShell` that reads one command at a time from the textarea, runs `docker exec <id> sh -c "<cmd>"`, captures stdout/stderr, and displays results in the message viewport. Each command is independent — `cd` doesn't persist, no tab completion, no interactive programs.

**Key insight:** Bubbletea v2 provides `tea.ExecProcess(*exec.Cmd, callback)` which suspends the TUI, hands full terminal control to a subprocess, and resumes the TUI when it exits. Running `docker exec -it <containerID> /bin/sh -l` through this gives us a real TTY for free — the container's shell handles tab completion, history, cd, signals, etc.

## What changes

- `enterShellMode()` stops switching to `modeShell`. Instead it builds an `exec.Cmd` for `docker exec -it <containerID> /bin/sh -l` and returns `tea.ExecProcess(...)`.
- The TUI suspends while the shell runs. User gets a real terminal session.
- When user exits the shell (`exit`, Ctrl+D), the TUI resumes and shows an info message.
- `modeShell`, `updateShellMode()`, `exitShellMode()` are removed (no longer needed).
- The shell-specific view rendering (green border, "SHELL" status bar, `shell $` placeholder) is removed since the TUI isn't visible during the shell session.
- `ContainerClient` gets a `ContainerID() string` getter so `enterShellMode` can build the docker command.

## What we gain vs lose

**Gain:** Tab completion, persistent cd, command history, interactive programs (vim, less, top), signal forwarding (Ctrl+C goes to process), real PS1 prompt.

**Lose:** Shell output doesn't appear in the message history viewport. This is acceptable — the shell session is ephemeral by nature, and keeping a separate mode with inferior UX is worse.

## Failure modes

- Container not ready or errored → keep existing validation, show error message, don't exec (same as today)
- `docker exec` fails to start → callback receives error, show error message in chat
- User's terminal doesn't support TTY (piped stdin) → `docker exec -it` will fail gracefully with a docker error; we can catch this
- Alpine container may not have bash → use `/bin/sh -l` which is always available

## Open questions

- Should we detect bash and prefer it over sh? (Probably not worth it for Alpine — sh is fine, and users can type `bash` if they installed it)

---

## Phase 1: Add ContainerID getter and shell exec command
- [x] 1a: Add `ContainerID() string` method to `ContainerClient`, add `ShellCmd() *exec.Cmd` helper that builds `docker exec -it <id> /bin/sh -l` with stdin/stdout/stderr wired to os.Stdin/os.Stdout/os.Stderr
- [x] 1b: Rewrite `enterShellMode()` to validate container state (keep existing checks), then return `tea.ExecProcess(container.ShellCmd(), callback)` where callback produces a `shellExitMsg`
- [x] 1c: Handle `shellExitMsg` in the main `Update` — show info message ("Shell session ended.") and ensure we're in `modeChat`

## Phase 2: Remove old shell mode
- [x] 2a: Remove `modeShell` constant, `updateShellMode()`, `exitShellMode()`, and all shell-specific view rendering (green border conditional, "SHELL" in status bar, `shell $` placeholder logic)
- [x] 2b: Remove the `updateShellMode` dispatch from the main `Update` switch, remove shell-related message types if no longer used

## Phase 3: Update tests
- [ ] 3a: Remove tests for old shell mode behavior (command echo, exec result in viewport, Ctrl+C exit, placeholder changes, shell status bar) since that UX no longer exists
- [ ] 3b: Add tests for new behavior: container validation still works (not ready, error cases), `enterShellMode` returns a `tea.Cmd` (we can't fully test ExecProcess in unit tests but can verify the flow), `shellExitMsg` handling restores chat mode
- [ ] 3c: Verify existing tests still pass — autocomplete for `/shell` command, container ready/error guards

## Success criteria

- `/shell` suspends the TUI and drops into a real interactive shell inside the container
- Tab completion works (container shell provides it)
- `cd /workspace && ls` persists across commands
- `exit` or Ctrl+D returns to the TUI cleanly
- Container not-ready and error cases still show messages in chat
- All tests pass
