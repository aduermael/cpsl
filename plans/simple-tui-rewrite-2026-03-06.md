# Simple TUI Rewrite: Adopt simple-chat Rendering Engine

Replace the entire bubbletea-based TUI with the raw terminal approach from `simple-chat/main.go`. No external TUI libraries. No full-screen modes. Everything renders inline with at most 5 lines below the input for navigation/options.

## Context

**Why:** The bubbletea-based TUI has persistent rendering bugs (scrollback corruption, resize issues, insertAbove fighting). `simple-chat/` is a proven, reliable ~440-line terminal UI that handles raw mode, ANSI rendering, cursor positioning, visual line wrapping, and input editing without any external dependencies beyond `golang.org/x/term`.

**What simple-chat provides (reuse as-is):**
- Raw terminal management (`term.MakeRaw` / `term.Restore`)
- ANSI escape sequence rendering (`writeRows`, `positionCursor`)
- Visual line wrapping (`getVisualLines`, `wrapString`, `vline` struct)
- Input buffer with cursor navigation (`insertAtCursor`, `deleteBeforeCursor`, `moveUp`, `moveDown`)
- Progress bar with color interpolation (`progressBar`, `lerpColor`)
- ASCII logo header
- Block-based message display
- Input area with separator lines
- SIGWINCH resize handling
- UTF-8 multi-byte input parsing
- Alt-screen buffer (`\033[?1049h` / `\033[?1049l`)
- Session time display on exit

**What stays unchanged:**
- `simple-chat/` directory (DO NOT MODIFY)
- `agent.go` - LLM agent orchestration (needs minor interface changes for event delivery)
- `config.go` - configuration management
- `container.go` - Docker container management
- `tools.go` - agent tool implementations
- `worktree.go` - git worktree management
- `systemprompt.go` - system prompt builder
- `models.go` - model definitions and SWE-bench fetching

**What gets deleted:**
- `textinput.go` - custom text input widget (replaced by simple-chat's input approach)
- `input.go` - input parsing (replaced by simple-chat's byte-level parsing)
- `render.go` - renderer (replaced by simple-chat's direct ANSI writes)
- `branchlist.go` - full-screen branch selection UI
- `worktreelist.go` - full-screen worktree selection UI
- `modellist.go` - full-screen model selection UI
- `configform.go` - full-screen config form UI
- All `*_test.go` files for deleted components

**What gets rewritten:**
- `main.go` - complete rewrite using simple-chat's architecture
- `term.go` - simplified to just what simple-chat needs

**New UI model (max 5 lines below input):**
Instead of alt-screen modes, all secondary interactions happen inline below the input:
- `/config` - prints current config, prompts for key/value inline
- `/model` - lists models below input, arrow keys to select, enter to confirm
- `/branches` - lists branches below input (filtered), select inline
- `/worktrees` - lists worktrees below input, select inline
- Autocomplete suggestions appear below input (1-2 lines)
- Approval prompts appear below input (1 line: `[y/n]`)
- Agent streaming text appears as blocks above the input area

## Contracts and interfaces

**App struct** (replaces `model`):
- Owns terminal state (fd, oldState, width)
- Global vars like simple-chat (`blocks`, `input`, `cursor`, `width`, etc.)
- Business state: `config`, `agent`, `container`, `langdagClient`, `worktreePath`, `models`
- Agent state: `agentRunning`, `awaitingApproval`, `streamingText`
- Below-input state: `menuLines []string`, `menuCursor int`, `menuAction func(int)`

**Rendering contract (from simple-chat):**
- `render()` - full redraw: logo + blocks + input area + menu lines
- `renderInput()` - redraw only input area + menu lines (for typing)
- Input area: top separator + input lines + bottom separator + progress bar + menu lines (max 5)

**Agent event delivery:**
- Agent currently sends events via bubbletea's `tea.Cmd`/`tea.Msg` pattern
- Change to: agent sends events to a Go channel, main loop reads the channel
- Event types: `AgentEvent` (text delta, tool call, tool result, done, error)

**Inline menu contract:**
- When a command triggers a menu (e.g., `/model`), populate `menuLines` with up to 5 visible items
- Arrow up/down navigates, enter selects, esc cancels
- Menu renders below the progress bar row
- On selection, `menuAction` callback executes and clears the menu

## Failure modes

- **Terminal not restored on crash:** Use defer pattern exactly like simple-chat
- **Agent events during menu:** Queue agent output; show it after menu closes
- **Long model/branch lists:** Show 5 at a time with scroll indicators (e.g., `... 3 more above`, `... 5 more below`)
- **Concurrent writes to stdout:** Agent goroutine must send events to channel, only main loop writes to stdout

## Open questions

- Should `/config` allow editing API keys inline (typing masked input) or just show current config and tell user to edit `~/.cpsl/config.json`? Start with the latter - simpler and safer for secrets.
- Should bracketed paste still be supported? Yes, the simple-chat approach can be extended to detect `\033[200~` sequences in the byte reader.

## Success criteria

- `go build` produces no bubbletea/bubbles imports
- Logo from simple-chat displays at top on launch
- Chat messages display correctly with styling (user, assistant, tool call, tool result, error, info, success)
- Agent streaming works: text appears progressively, tool calls/results display inline
- Input editing works: typing, cursor movement, arrow keys, backspace, shift+enter for newline, enter to submit
- `/model` shows inline picker below input, selection changes active model
- `/config` shows current config inline
- `/branches` shows filterable branch list inline
- `/worktrees` shows worktree list inline
- `/clear` clears conversation and reprints logo
- `/shell` suspends TUI and runs interactive shell
- Resize redraws everything correctly
- Progress bar shows conversation size
- All business logic tests still pass (config, container, worktree, models, tools)

---

## Phase 1: Core terminal engine

Port simple-chat's rendering engine into the main app, adapted for the richer content types.

- [x] 1a: Delete UI files that will be replaced: `textinput.go`, `input.go`, `render.go`, `branchlist.go`, `worktreelist.go`, `modellist.go`, `configform.go`, and all their `*_test.go` counterparts. Delete `textinput_test.go`, `branchlist_test.go`, `worktreelist_test.go`, `configform_test.go`, `statusbar_test.go`. Keep `model_test.go`, `integration_test.go`, `exec_test.go` for now (they'll be rewritten later).
- [x] 1b: Rewrite `main.go` with simple-chat's architecture: global state vars (`blocks`, `input`, `cursor`, `width`, `prevRowCount`, `sepRow`, `inputStartRow`), `Block` struct extended with `kind` (user/assistant/tool/info/error/success) and styling, logo from simple-chat, `render()` / `renderInput()` / `buildInputRows()` / `writeRows()` / `positionCursor()` ported directly. Wire up `term.MakeRaw`, alt-screen buffer, SIGWINCH handler, and the main byte-reading loop — all from simple-chat. Add the business state fields as package-level vars: `config`, `agent`, `langdagClient`, `container`, `worktreePath`, `models`, `agentRunning`, `streamingText`, etc.
- [x] 1c: Rewrite `term.go` to only contain what's needed: `getWidth()` (from simple-chat), terminal height helper. Remove any bubbletea-related code.

## Phase 2: Agent integration

Connect the agent/LLM system to the new rendering engine.

- [x] 2a: Modify agent event delivery in `agent.go`: replace the bubbletea `tea.Cmd` pattern with a Go channel (`chan AgentEvent`). The agent loop sends events (text delta, tool call, tool result, done, error) to the channel. Define `AgentEvent` struct with event type enum and payload fields.
- [x] 2b: Add agent event handling to the main loop: use a separate goroutine that reads from the agent event channel and writes to a pipe/signal that the main byte-reading loop can select on (or use `os.Pipe` + poll, or switch main loop to use `select` with channels). On text delta: append to `streamingText`, call `renderInput()` to update display. On tool call/result: add block, call `render()`. On done: flush `streamingText` to a block, mark `agentRunning = false`.
- [x] 2c: Wire up message submission: on Enter, if input is not a command and `agentRunning` is false, create user block, expand pastes, start agent goroutine with the message. Handle approval flow: when agent sends approval event, set `awaitingApproval = true`, show `[y/n]` below input, wait for key press.

## Phase 3: Commands and inline menus

Implement slash commands using inline display (max 5 lines below input).

- [x] 3a: Implement the inline menu system: `menuLines []string`, `menuCursor int`, `menuActive bool`. When active, arrow up/down moves cursor, enter selects, esc cancels. Menu lines render below the progress bar in `buildInputRows()`. Autocomplete for `/` commands also uses this (show matching commands as menu).
- [x] 3b: Implement `/model` command: populate menu with available models (filtered by configured providers), show 5 at a time with scroll indicators. On select, update config's active model and reinitialize langdag client if provider changed. Implement `/config` command: print current config as info blocks (masked API keys), no editing.
- [x] 3c: Implement `/branches` command: populate menu with git branches (from worktree path), filterable by typing. On select, checkout branch. Implement `/worktrees` command: populate menu with worktrees, on select switch active worktree.
- [x] 3d: Implement `/clear` (clear blocks, reprint logo), `/shell` (restore terminal, spawn shell, re-enter raw mode on exit, reprint everything).

## Phase 4: Input enhancements

Add features beyond simple-chat's basic input.

- [x] 4a: Add bracketed paste support: detect `\033[200~` / `\033[201~` sequences in the byte reader, collect pasted content, insert into input buffer (or collapse large pastes using existing `pasteStore` / `pasteCount` logic from current main.go).
- [x] 4b: Add Ctrl+key shortcuts: Ctrl+A (home), Ctrl+E (end), Ctrl+W (delete word), Ctrl+U (clear line). These are standard terminal shortcuts and straightforward to add in the byte reader.

## Phase 5: Cleanup and dependency removal

- [x] 5a: Remove `charm.land/bubbletea/v2` and `charm.land/bubbles/v2` from `go.mod`. Run `go mod tidy`. Verify `go build` succeeds with zero bubbletea/bubbles imports. Remove `charm.land/lipgloss/v2` only if no styling code uses it anymore (likely still used for message styling — keep if so, or replace with direct ANSI codes).
- [x] 5b: Delete or rewrite remaining test files (`model_test.go`, `integration_test.go`, `exec_test.go`) to test business logic without bubbletea. Keep tests for unchanged files (`config_test.go`, `container_test.go`, `worktree_test.go`, `models_test.go`, `wrap_test.go`). Add basic tests for the new rendering functions (`wrapString`, `getVisualLines`, `progressBar`, `buildInputRows`).

## Phase 6: Verification

- [ ] 6a: `go build` succeeds, manual smoke test: launch app, verify logo, type message, verify agent interaction works end-to-end (if API key configured), test `/model`, `/config`, `/clear`, `/shell`, `/branches`, `/worktrees`, resize terminal, verify clean redraw.
