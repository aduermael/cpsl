# Drop Bubbletea: Custom Terminal Rendering Engine

Replace bubbletea/bubbles with a custom terminal engine. Keep lipgloss for styling. The app owns the terminal directly: raw mode, input parsing, SIGWINCH, rendering. This eliminates the `insertAbove` / renderer-state / scrollback bugs that are unfixable within bubbletea's architecture.

## Context

**Why bubbletea doesn't work for this app:**
- `tea.Println` uses `insertAbove()` which assumes the View is at the bottom of the terminal. After `clearScreen`, the View is at position (0,0) and the cursor math breaks.
- The renderer's cell-buffer diffing, internal position tracking, and frame clipping (`frameHeight > s.height` drops lines) make it impossible to cleanly clear+reprint scrollback on resize.
- Every message through the event loop triggers a render tick, so multi-step sequences (Raw + ClearScreen + Println) have intermediate renders that corrupt state.
- These are architectural constraints of bubbletea's `cursedRenderer`, not bugs we can work around.

**What we replace:**
- `charm.land/bubbletea/v2` - event loop, raw mode, input parsing, rendering, tea.Println, tea.Cmd/Msg
- `charm.land/bubbles/v2/textarea` - multi-line text input
- `charm.land/bubbles/v2/textinput` - single-line text input (used in configform, branchlist)
- `charm.land/bubbles/v2/key` - key binding types

**What we keep:**
- `charm.land/lipgloss/v2` - all styling, colors, borders, width calculations
- `github.com/rivo/uniseg` - unicode width (already used directly)
- All business logic: agent.go, config.go, container.go, tools.go, worktree.go, systemprompt.go, models.go

**Current architecture (bubbletea):**
- `model` struct implements `tea.Model` (Init/Update/View)
- `tea.Cmd` functions return `tea.Msg` for async work (container boot, model fetch, agent events, etc.)
- `tea.Println` prints to scrollback via renderer's `insertAbove()`
- `View()` returns `tea.View` with cursor position, AltScreen flag
- Alt-screen modes for config/model/worktree/branch selection
- Test helpers construct `tea.Msg` values and call `m.Update(msg)` directly

**New architecture (custom):**
- `App` struct owns the terminal (raw mode via `golang.org/x/term`)
- Single goroutine event loop reads input + channels + SIGWINCH
- Rendering: write directly to stdout. Scrollback = just printed text. Active area (input box etc.) = rewritten each frame at the cursor position.
- On resize: clear terminal (`\033[2J\033[3J\033[H`), reprint all scrollback content word-wrapped to new width, then render active area below.
- No cell buffer, no insertAbove, no frame diffing. Just print text and manage cursor position.
- Async work uses Go channels (same pattern as tea.Cmd but without the Msg wrapper)

**Key rendering insight:**
The terminal is split into two regions:
1. **Scrollback region** (above): Logo + conversation messages. Written once via `fmt.Fprint(stdout, ...)` and left in terminal scrollback. On resize, cleared and reprinted.
2. **Active region** (bottom): Streaming text, status bar, input box. Redrawn each frame by: saving cursor position, moving to the start of the active region, writing content, clearing to end of screen.

This is fundamentally simpler than bubbletea's approach because we don't fight a renderer that tries to own the screen.

**Files involved (production):**
- `main.go` (1936 lines) - Complete rewrite of TUI layer; business logic handlers stay
- `configform.go` (184 lines) - Replace bubbles textinput with custom input
- `branchlist.go` (240 lines) - Replace bubbles textinput filter with custom input
- `modellist.go` (422 lines) - Remove tea.Msg types, use custom events
- `worktreelist.go` (170 lines) - Remove tea.Msg types, use custom events

**Files involved (tests):**
- `model_test.go` (2066 lines) - Rewrite test helpers and assertions
- `integration_test.go` (667 lines) - Update for new event model
- `exec_test.go` (263 lines) - Update for new event model
- `statusbar_test.go` (244 lines) - Update rendering assertions
- `configform_test.go` (150 lines) - Update for custom input
- `branchlist_test.go` (274 lines) - Update for custom input
- `worktreelist_test.go` (171 lines) - Minor updates

**Files unchanged:**
- agent.go, config.go, container.go, tools.go, worktree.go, systemprompt.go, models.go, wrap_test.go

## Contracts and interfaces

**App struct** replaces `model`:
- Owns terminal state (raw mode fd, original termios, width/height)
- Holds all the same business state (messages, config, agent, container, etc.)
- Has `Run()` method that enters raw mode, starts event loop, restores on exit
- Has `render()` method that writes the active area
- Has `printScrollback(content string)` that writes to scrollback above active area

**Event types** replace `tea.Msg`:
- `EventKey` - key press (rune, special key code, modifiers)
- `EventPaste` - bracketed paste content
- `EventResize` - terminal width/height change
- Plus all existing domain events (agentEventMsg, etc.) sent via channels

**Input reader:**
- Runs in a goroutine, reads from stdin in raw mode
- Parses ANSI escape sequences into EventKey
- Handles bracketed paste (`\033[200~` ... `\033[201~`)
- Sends parsed events to a channel

**Resize handler:**
- Listens for SIGWINCH signal
- Queries terminal size via ioctl
- Sends EventResize to event channel

**TextInput** replaces both `textarea.Model` and `textinput.Model`:
- Single-line and multi-line modes
- Handles cursor movement, insert, delete, word boundaries
- Returns rendered string (styled via lipgloss)
- Newline on shift+enter/alt+enter (multi-line mode)
- Cursor position tracking for terminal cursor placement

**Alt-screen modes:**
- Enter: write `\033[?1049h` (save cursor + switch to alt buffer)
- Exit: write `\033[?1049l` (restore cursor + switch back)
- While in alt-screen: render the modal UI (config/model/worktree/branch) to fill the screen
- On exit: scrollback is preserved by the terminal automatically

## Failure modes

- **Raw mode not restored on panic:** Must use defer to restore terminal state. Wrap main loop in recover().
- **Input parsing edge cases:** Terminal escape sequences vary. Start with common sequences (xterm/VT100), handle unknowns gracefully (ignore).
- **SIGWINCH race:** Resize signal and input can arrive simultaneously. Channel-based event loop serializes them.
- **Unicode width:** Use `uniseg` for correct East Asian character width. Lipgloss already handles this for styled content.
- **Cursor positioning drift:** If our tracking of "how many lines is the active area" drifts from reality, the display corrupts. Mitigate by clearing and redrawing the active area each frame rather than incremental updates.
- **Large scrollback reprint:** Reprinting 1000+ messages on resize could be slow. Acceptable for now; can optimize later with batched writes.

## Open questions

- Should we support mouse wheel scrolling? Terminal mouse mode (`\033[?1000h`) can capture mouse events but disables native scroll in some terminals. Start without mouse support; native trackpad/scrollbar scrolling works automatically with scrollback.
- Should the cursor blink? Bubbletea's textarea had cursor blinking via a timer Cmd. We can use a goroutine timer that sends blink events, or just show a solid cursor. Start with solid cursor.

## Success criteria

- After resize: scrollback contains exactly the logo + all messages with correct word wrap at the new width. No ghost content above. Scrolling up shows conversation from the start.
- Input box renders correctly at the bottom of the conversation.
- All key handling works: typing, enter to send, shift+enter for newline, ctrl+c to quit, arrow keys, tab for autocomplete, esc to clear.
- Bracketed paste works with collapse threshold.
- Alt-screen modes (config, model, worktrees, branches) work and preserve scrollback on exit.
- Agent streaming text displays correctly and flushes to scrollback on completion.
- `/clear` clears everything and reprints logo.
- `/shell` suspends TUI and runs interactive shell.
- All tests pass with the new architecture.
- `go build` produces no bubbletea/bubbles imports.

---

## Phase 1: Terminal engine core

Build the low-level terminal handling that replaces bubbletea's runtime.

- [x] 1a: Create `term.go` with terminal raw mode management: `enterRawMode()` / `restoreTerminal()` using `golang.org/x/term`, SIGWINCH handler that sends resize events to a channel, `getTerminalSize()` helper. Include panic-safe defer restoration.
- [x] 1b: Create `input.go` with stdin reader goroutine: read bytes in raw mode, parse into key events (printable runes, escape sequences for arrows/home/end/pgup/pgdn/delete/backspace, ctrl+key combos, shift+enter/alt+enter). Handle bracketed paste sequences. Define `EventKey`, `EventPaste`, `EventResize` types. Send parsed events to a channel.
- [x] 1c: Create `render.go` with the rendering engine: `Renderer` struct that tracks active area height, provides `printAbove(content string)` to write to scrollback, `renderActiveArea(lines []string, cursorX, cursorY int)` to redraw the bottom area, `clearAll()` to clear screen+scrollback. All writes go directly to a buffered stdout writer. No cell buffer or diffing.

## Phase 2: Text input widget

Replace bubbles textarea and textinput with a custom implementation.

- [x] 2a: Create `textinput.go` with a `TextInput` struct: multi-line text buffer ([]string for lines), cursor position (row, col), insert/delete/backspace operations, word-boundary navigation (ctrl+left/right), home/end, line wrapping for display. Support both single-line and multi-line modes (newline on shift+enter/alt+enter in multi-line).
- [x] 2b: Add rendering to `TextInput`: `View(width int) string` returns the styled content (using lipgloss for colors). `CursorPosition() (x, y int)` returns cursor coordinates relative to the rendered output. Handle display-width-aware cursor placement (unicode/CJK characters).
- [x] 2c: Add paste support to `TextInput`: `InsertText(s string)` handles multi-line paste, updates cursor position. Used for both typed characters and paste events.

## Phase 3: App event loop

Build the main application loop that replaces bubbletea's Program.

- [x] 3a: Create the `App` struct in `main.go` (replacing `model`): holds all existing state fields (messages, config, agent, textarea → TextInput, container, status, etc.), plus terminal fd, renderer, event channels. Add `Run()` method: enter raw mode, start input reader, start SIGWINCH handler, run main event loop (select on input channel, async result channels, agent event channel), restore terminal on exit.
- [x] 3b: Port async command pattern: replace `tea.Cmd` functions with goroutines that send results to typed channels. `Init()` becomes startup goroutines: fetch models, fetch SWE scores, resolve workspace, init langdag. Each writes to a dedicated result channel that the event loop selects on.
- [x] 3c: Port the main event dispatch: translate the `update()` switch statement from tea.Msg types to the new event types. `EventKey` replaces `tea.KeyPressMsg`, `EventPaste` replaces `tea.PasteMsg`, `EventResize` replaces `tea.WindowSizeMsg`. Domain messages (modelsMsg, statusInfoMsg, etc.) come from their respective channels.

## Phase 4: Chat mode rendering

Port the chat View and scrollback management.

- [ ] 4a: Port `View()` to a `renderChatArea()` method that returns the active area content (streaming text / thinking indicator / approval prompt + status bar / autocomplete + input box) as a string, plus cursor position. Call `renderer.renderActiveArea()` to display it.
- [ ] 4b: Port scrollback printing: when a new message is added, call `renderer.printAbove(renderMessage(msg, width))`. On resize: call `renderer.clearAll()`, reprint logo + all messages at new width, then render active area. This is now trivial direct stdout writes instead of fighting insertAbove.
- [ ] 4c: Port `/clear` command: clear messages slice, call `renderer.clearAll()`, reprint logo.

## Phase 5: Alt-screen modes

Port config, model, worktree, and branch selection screens.

- [ ] 5a: Implement alt-screen enter/exit in the renderer: `enterAltScreen()` writes `\033[?1049h`, `exitAltScreen()` writes `\033[?1049l`. On exit, the terminal automatically restores the main screen with scrollback intact.
- [ ] 5b: Port `configForm` to use the custom `TextInput` (single-line mode) instead of bubbles textinput. Port field navigation (tab/shift+tab), validation, save/discard. Update `configform.go`.
- [ ] 5c: Port `branchList` to use custom `TextInput` for the filter input. Port `modelList` and `worktreeList` (these use only key events, no textinput). Update all four component files to use the new event types instead of tea.Msg.
- [ ] 5d: Port the rendering for each alt-screen mode: config form, model list, worktree list, branch list. Each renders to a full-screen string using lipgloss, written via the renderer.

## Phase 6: Shell exec and agent integration

Port the remaining interactive features.

- [ ] 6a: Port `/shell` command: save terminal state, spawn shell process with inherited stdin/stdout/stderr (using `os/exec` with `Cmd.Stdin/Stdout/Stderr = os.Stdin/Stdout/Stderr`), wait for exit, restore raw mode, reprint scrollback.
- [ ] 6b: Port agent event handling: the agent event channel integration. `listenForAgentEvent` becomes a direct channel read in the event loop select. `handleAgentEvent` logic stays the same but uses direct rendering calls instead of returning tea.Cmd.
- [ ] 6c: Port approval flow: when agent requests approval, render the prompt in the active area, handle y/n key events directly.

## Phase 7: Update tests

Port all test files to the new architecture.

- [ ] 7a: Create new test helpers: `newTestApp(width, height int) *App` (creates app without entering raw mode), `simulateKey(app, key)`, `simulateResize(app, w, h)`, `simulatePaste(app, content)`. These call the app's event handlers directly, same pattern as the old `resize()` / `sendKey()` / `typeString()` / `paste()` helpers.
- [ ] 7b: Port `model_test.go` — update all tests to use the new helpers and assert on the new state fields. Tests that check View output need updated assertions for the new rendering format.
- [ ] 7c: Port `integration_test.go`, `exec_test.go`, `statusbar_test.go` — update event types and assertions.
- [ ] 7d: Port `configform_test.go`, `branchlist_test.go`, `worktreelist_test.go` — update for custom TextInput and new event types.

## Phase 8: Cleanup and remove bubbletea

- [ ] 8a: Remove `charm.land/bubbletea/v2` and `charm.land/bubbles/v2` from go.mod. Run `go mod tidy`. Verify `go build` succeeds with no bubbletea imports.
- [ ] 8b: Run full test suite, fix any remaining failures. Verify resize behavior manually: resize terminal, confirm scrollback is clean with correct word wrap and no ghost content.
