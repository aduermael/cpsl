# Native Scrollback Rendering

Replace viewport-based chat rendering with native terminal scrollback. Bubbletea stays for input handling, key events, window tracking, and the sticky bottom area. Chat content is printed via `tea.Println` and lives in terminal scrollback. On resize: clear everything, re-print all messages.

## Context

**Current architecture (viewport-based):**
- `main.go` uses `viewport.Model` to hold all chat content (logo, messages, streaming text)
- `View()` renders: viewport output (clamped to `viewportHeight()`) + gap + status bar + input box
- `AltScreen = true` on all views -- bubbletea's renderer clips content to screen height, preventing overflow into scrollback
- pgup/pgdown scroll the viewport's internal YOffset
- `updateViewportContent()` rebuilds viewport content after every Update
- On resize: no good way to clear scrollback without also destroying content the renderer clips

**Previous native scrolling attempt** (see `memory/native-scrolling-refactor.md`):
- Used `tea.Println()` for all message output, removed viewport entirely
- Worked but was reverted in favor of viewport (commit `604187a`)

**New architecture (this plan):**
- Chat mode: inline (no alt screen) -- `AltScreen = false`
- Messages printed via `tea.Println()` -- go into native terminal scrollback
- `View()` renders only the sticky bottom: streaming text / thinking indicator / approval prompt + status bar + input box
- On resize: `\033[2J\033[3J\033[H]` clears screen+scrollback, then re-print all messages via ordered `tea.Println` sequence, then View renders bottom
- Config/model/worktree/branch modes: keep `AltScreen = true` (modal overlays). Terminal preserves scrollback on alt-screen enter/exit.
- User scrolls with native terminal scrollback (mouse wheel, trackpad, scrollbar)

**Key bubbletea v2 internals:**
- `tea.Println(lines ...string) Cmd` produces `printLineMessage` handled by `renderer.insertAbove()` -- prints above current inline view
- `tea.Raw(seq)` produces `RawMsg` handled by `p.execute()` which writes to `p.outputBuf`, flushed BEFORE renderer on each tick
- `tea.Sequence(cmds...)` executes commands one at a time, waiting for each message to be processed before starting the next
- `tea.ClearScreen` calls `renderer.clearScreen()` which does `scr.MoveTo(0,0)` + `scr.Erase()`

**Files involved:**
- `main.go` -- model struct, Update, View, updateViewportContent, viewportHeight, height clamping
- `model_test.go` -- resize tests, viewport assertions, pgup/pgdown tests, inputBoxHeight tests
- `integration_test.go` -- full-flow tests that check view output
- `statusbar_test.go` -- statusBarHeight tests
- `exec_test.go`, `worktreelist_test.go`, `branchlist_test.go` -- tests that call resize()

## Open questions

- Should the `resizeDoneMsg` type (currently unused) be repurposed or removed?
- When returning from alt-screen modes (config, model, worktrees, branches) to chat, should we re-print all messages? The terminal should restore inline scrollback, but the cursor position may have shifted.
- Streaming text: render in View() (ephemeral, above input) or print chunks via tea.Println as they arrive? Rendering in View avoids re-printing partial lines but means the text disappears on resize until flushed.

## Failure modes

- `tea.Sequence` with many `tea.Println` calls on resize could cause visible flicker if messages are long
- Rapidly resizing the terminal could queue multiple clear+reprint sequences -- may need to debounce or cancel pending reprint on new resize
- Mode transitions (chat -> config -> chat) must not double-print messages or lose scrollback
- `tea.Println` in bubbletea v2 might behave differently than v1 -- verify `insertAbove` works in inline mode

## Success criteria

- After resize: scrollback contains exactly the current messages (no ghost frames, no duplicates)
- User can scroll up through terminal scrollback to see all chat history
- Input box stays at bottom of terminal
- Streaming text visible while agent is running
- Config/model/worktree/branch modes still render in alt screen
- All existing tests pass (updated for new architecture)

---

## Phase 1: Remove viewport, switch chat to inline mode

- [x] 1a: Remove `viewport` import, `viewport.Model` field, `userScrolled` field from model struct. Remove `updateViewportContent()`, `viewportHeight()`, pgup/pgdown key handlers. Remove the `updateViewportContent()` call in the `Update()` wrapper.
- [x] 1b: Change chat `View()` to inline mode: set `AltScreen = false`, render only the sticky bottom area (status bar + input box). Remove the viewport output, height clamping, and `linesAbove` cursor math. Keep the `resizeDoneMsg` type removal as cleanup.
- [x] 1c: Print the logo once on first `WindowSizeMsg` via `tea.Println`. Print messages via `tea.Println` wherever `m.messages = append(...)` currently happens (keep storing in `m.messages` for resize reprint). Streaming text and thinking/approval indicators render in `View()` above the input.

## Phase 2: Resize clear and reprint

- [x] 2a: On `WindowSizeMsg` (after first), build a `tea.Sequence` that: (1) sends `tea.Raw("\033[2J\033[3J\033[H")` to clear screen+scrollback, (2) sends `tea.ClearScreen` to reset the renderer, (3) re-prints the logo + all `m.messages` via ordered `tea.Println` calls. Flush any pending `streamingText` into `m.messages` before reprinting so it's not lost.
- [x] 2b: Debounce resize: if a new `WindowSizeMsg` arrives while a reprint sequence is in-flight, cancel the old one (e.g. use a generation counter and ignore stale `resizeDoneMsg`).

## Phase 3: Mode transitions

- [x] 3a: When exiting alt-screen modes (config, model, worktrees, branches) back to chat, re-print all messages to restore scrollback. Use the same reprint sequence as resize.

## Phase 4: Update tests

- [x] 4a: Remove viewport-related assertions from `model_test.go` (pgup/pgdown tests, viewport content checks, `userScrolled` assertions). Update resize tests to verify the reprint command sequence instead of viewport state.
- [x] 4b: Update `integration_test.go`, `exec_test.go`, `statusbar_test.go`, `worktreelist_test.go`, `branchlist_test.go` -- remove any viewport references, update view output assertions for inline mode.
- [x] 4c: Add new test: verify that resize produces correct sequence of commands (clear + println for each message). Verify that mode transition back to chat triggers reprint.
