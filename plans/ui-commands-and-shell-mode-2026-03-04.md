# UI Components, Commands, and Container Shell Mode

**Goal:** Add a status bar showing branch/PR/worktree info, commands for listing worktrees and branches, and replace `/exec` with a `/container-shell` command that switches to an interactive shell mode inside the container.

## Codebase Context

- **main.go** — BubbleTea app with modes (`modeChat`, `modeConfig`, `modeModel`), slash commands (`/config`, `/exec`, `/model`), `View()` renders viewport + autocomplete + input box. No status bar exists currently.
- **worktree.go** — `WorktreeInfo{Path, Branch, Clean, Active}`, `listWorktrees(baseDir)`, `selectWorktree(repoRoot)`, `ensureProjectID(repoRoot)`, `worktreeBaseDir(uuid)`. Session tracking via `.cpsl-lock` with PID.
- **container.go** — `ContainerClient` with `Start/Exec/Stop/Status`. `Exec(command, timeout)` runs `docker exec <id> sh -c <command>` and returns `CommandResult{Stdout, Stderr, ExitCode}`.
- **modellist.go** — existing list UI component pattern: struct with cursor, scroll offset, `Update()` for key handling, `View()` for rendering. Good reference for new list components.
- Model struct fields: `container *ContainerClient`, `worktreePath string`, `containerReady bool`, `containerErr error`.
- `bootContainerCmd()` returns `containerReadyMsg{client, worktreePath}` — the worktree path is already stored.

## Architecture

### Status Bar

A single-line bar rendered between the viewport and autocomplete/input. Content:
- Left side: `branch-name` + ` PR #N` (if a PR exists for this branch)
- Right side: `worktree-name` + ` (N active)` count of locked worktrees

The branch name comes from `git rev-parse --abbrev-ref HEAD` in the worktree path. The PR number comes from `gh pr view --json number -q .number` (fails gracefully if no PR or `gh` not installed). Worktree info comes from `listWorktrees()` at boot and on demand.

The status bar content is fetched once at startup (alongside container boot) and cached in the model. A `statusInfo` struct holds branch, PR number (optional), worktree name, active count.

### Commands

**`/worktrees`** — New app mode `modeWorktrees`. Lists all worktrees for the current project with: branch name, clean/dirty indicator, active/inactive indicator, current session marker. Styled like `modelList`. Esc to return to chat.

**`/branches`** — New app mode `modeBranches`. Lists git branches from `git branch -a` in the worktree. Filterable text input at top, arrow key navigation, Enter to checkout selected branch in the current worktree. Esc to cancel. This changes the branch of the current worktree (runs `git checkout <branch>` in the worktree dir).

**`/container-shell`** — Replaces `/exec`. New app mode `modeShell`. The textarea becomes a shell prompt (visual change: prompt prefix like `container $`). Each Enter sends the input as `docker exec ... sh -c <input>` and displays the output in the message thread. Ctrl+C exits back to `modeChat`. The viewport continues showing the conversation with shell commands/outputs intermixed. This is NOT a real PTY — it's command-by-command execution reusing the existing `Exec()` method, but the UX feels like a shell because the mode stays in shell until Ctrl+C.

## Design Decisions

- **Status bar is passive** — no interaction, just display. Computed once at boot from git/gh commands, refreshed when branch changes (after `/branches` checkout).
- **`gh` is optional** — if `gh` is not installed or no PR exists, the PR number is simply omitted. No error shown.
- **`/branches` checkout** — runs `git checkout <branch>` in the worktree directory. Updates the status bar branch name. Does NOT restart the container (the mount is by path, not branch).
- **`/container-shell` is stateless** — each command is independent (no persistent shell session). Working directory resets each time. This is simpler and matches the current `Exec()` architecture. A future enhancement could track `cwd` by appending `cd <cwd> &&` prefix.
- **Remove `/exec`** — replaced entirely by `/container-shell`. The command list changes from `[/config, /exec, /model]` to `[/branches, /config, /container-shell, /model, /worktrees]`.

## Failure Modes

- `gh` not installed → PR number omitted silently
- `gh pr view` fails (no PR for branch) → PR number omitted
- Not in a git repo → status bar shows "no git repo", `/branches` and `/worktrees` show error
- Branch checkout fails (dirty worktree, branch doesn't exist) → show error message, stay in branch list
- Container not ready when entering `/container-shell` → show info message, don't enter shell mode
- `docker exec` fails in shell mode → show error output in thread, stay in shell mode

## Phase 1: Status Bar

- [x] 1a: Add `statusInfo` struct (Branch string, PRNumber int, WorktreeName string, ActiveCount int) to the model. Add `fetchStatusCmd(worktreePath string)` that runs `git rev-parse --abbrev-ref HEAD`, `gh pr view --json number -q .number`, and counts active worktrees via `listWorktrees()`. Returns a `statusInfoMsg`. Wire into `Init()` after container ready (chain from `containerReadyMsg` handler). Cache in model field.
- [x] 1b: Add `renderStatusBar()` method that produces a single styled line: left-aligned branch + PR, right-aligned worktree info. Use lipgloss for layout (Place or JoinHorizontal). Subtle purple theme consistent with existing UI. Integrate into `View()` between viewport and autocomplete/input. Adjust `viewportHeight()` to account for the status bar line.
- [x] 1c: Tests for status bar: model-level test that `statusInfoMsg` updates fields correctly, `renderStatusBar()` output contains branch name, viewport height reduced by 1 when status bar is present.

## Phase 2: `/worktrees` Command

- [x] 2a: Add `worktreelist.go` with `worktreeList` component: struct with `items []WorktreeInfo`, `cursor int`, `currentPath string` (to mark active), `width/height int`. `Update()` handles up/down/j/k navigation and Esc to exit. `View()` renders the list with branch name, clean/dirty badge, active/current markers. Follow the `modelList` pattern.
- [x] 2b: Add `modeWorktrees` app mode. Wire `/worktrees` command in `handleCommand()`. On enter, fetch worktree list (async cmd calling `listWorktrees()`), populate the component. Esc returns to `modeChat`. Add `viewWorktrees()` method for the mode's View. Add `worktreeListMsg` message type.
- [x] 2c: Tests: worktree list navigation (up/down moves cursor), Esc returns to chat mode, current worktree is marked, clean/dirty display.

## Phase 3: `/branches` Command with Filter and Selection

- [x] 3a: Add `branchlist.go` with `branchList` component: struct with `items []string` (all branches), `filtered []string`, `cursor int`, `filter string`, `filterInput textarea.Model` (or textinput), `width/height int`. `Update()` handles: typing updates filter and re-filters list, up/down navigates filtered results, Enter selects, Esc cancels. `View()` renders filter input at top + scrollable branch list below. Use `textinput` from bubbles for the filter (single-line input).
- [x] 3b: Add `modeBranches` app mode. Wire `/branches` in `handleCommand()`. On enter, fetch branches via async cmd running `git branch -a --format='%(refname:short)'` in worktree dir. On branch selection (Enter), run `git checkout <branch>` in worktree dir (async cmd), show success/error, update status bar branch, return to chat. Add `branchListMsg` and `branchCheckoutMsg` message types.
- [x] 3c: Tests: branch list filtering narrows results, cursor navigation, Enter triggers checkout message, Esc returns to chat, checkout updates status bar branch.

## Phase 4: `/container-shell` Mode (replaces `/exec`)

- [x] 4a: Add `modeShell` app mode. When active: textarea prompt changes (placeholder text "container $" or similar visual indicator), Enter sends input via `Exec()` (same async pattern as old `/exec`), output appended to messages, Ctrl+C returns to `modeChat`. The shell mode is indicated by a visual change in the input area and status bar.
- [x] 4b: Wire `/container-shell` in `handleCommand()`. Remove `/exec` from commands list. On enter: check container ready (show error if not), switch to `modeShell`. In `modeShell` Update: Enter sends command (reuse exec async pattern), handle `execResultMsg` to append output, Ctrl+C sets mode back to `modeChat`. Show a mode indicator (e.g., status bar changes color or text, or a "shell mode" banner).
- [x] 4c: Tests: entering shell mode changes app mode, Ctrl+C exits shell mode, Enter in shell mode fires exec command, container-not-ready prevents entering shell mode, exec results appear in messages.

## Open Questions

- Should `/container-shell` track working directory across commands (prepend `cd /last/dir &&` to each command)? This would make it feel more like a real shell. Starting without it for simplicity.
- Should `/branches` show remote branches by default or only local? Starting with `-a` (all) for discoverability.
- Should the status bar auto-refresh periodically, or only on explicit actions? Starting with on-demand (boot + branch checkout).

## Success Criteria

- Status bar visible below viewport showing branch name, PR number (when available), worktree name, and active worktree count
- `/worktrees` opens a navigable list of all project worktrees with status indicators
- `/branches` opens a filterable, navigable branch list; selecting a branch checks it out and updates the status bar
- `/container-shell` enters shell mode where each Enter runs a command in the container and displays output; Ctrl+C returns to chat
- `/exec` is removed and replaced by `/container-shell`
- All existing tests continue to pass
- New tests cover status bar, worktree list, branch list, and shell mode
