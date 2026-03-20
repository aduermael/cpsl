# Remove Alternate Screen Buffer

**Goal:** Stop using the alternate screen buffer (`\033[?1049h`) so that native terminal scrolling works in all terminals (Zed, Ghostty, iTerm, Terminal.app, VS Code, etc.), and conversation history remains accessible in the terminal's scrollback.

**Why alt screen was used:** It provides an isolated buffer where absolute positioning (`\033[row;1H`) works predictably, exit is clean (terminal restores previous content), and resize is simple (clear + redraw).

**Why it must go:** Alt screen has no scrollback by design. Terminals like Zed and Ghostty strictly follow this — no scrollback means no scroll. iTerm/Terminal.app have proprietary workarounds, but depending on those isn't portable.

**Target behavior (matching Claude Code):**
- **Start:** Append to terminal session — previous shell commands remain visible above
- **Resize:** Clear everything (visible screen + scrollback) and re-render with new width — this is acceptable since users should expect a dedicated terminal for this kind of CLI
- **Exit:** Conversation stays in scrollback — no longer refreshed on resize, but that's fine since the session is over
- **Shell mode:** Clear screen before handing off to shell, clear + re-render after shell exits — no alt screen needed

**Key insight from research:** herm's rendering already works without alt screen — `writeRows()` uses absolute positioning within the viewport (`\033[row;1H`), which targets the visible screen regardless of scrollback. The `scrollShift` overflow logic (write only visible rows at position 1) is viewport-relative and works in both modes.

---

## Phase 1: Remove alt screen and fix terminal setup/cleanup
- [x] 1a: Remove `\033[?1049h` from startup and `\033[?1049l` from cleanup defer; keep bracketed paste and modifyOtherKeys (both work without alt screen); on exit, position cursor below the last rendered row and print a newline so the shell prompt appears in the right place
- [x] 1b: In `renderFull()`, use `\033[H\033[2J\033[3J` (home + clear visible screen + clear scrollback) for a completely blank canvas before re-rendering — this is what runs on SIGWINCH and guarantees artifact-free resize; update the `render()` content-shrank path similarly
- [x] 1c: Update shell-mode transitions — currently exit/re-enter alt screen; without alt screen: clear screen before shell (`\033[H\033[2J\033[3J`), restore terminal, run shell, re-enter raw mode, clear screen, full re-render

## Phase 2: Verify and fix edge cases
- [x] 2a: Ensure the SIGWINCH → `renderFull()` path produces a clean re-render: hide cursor before clearing (`\033[?25l`), clear screen + scrollback, write rows, show cursor — this eliminates flicker during resize
- [x] 2b: Verify that the overflow render path (content > terminal height) works correctly: visible rows written at position 1, `\033[J` clears below, cursor positioned correctly via `scrollShift`
- [x] 2c: Verify exit behavior: on clean exit and on Ctrl+C/Ctrl+D, the conversation output remains in the terminal and the shell prompt appears below it

## Phase 3: Add tests and verify cross-terminal behavior
- [x] 3a: Add a test that verifies `renderFull()` output uses `\033[H\033[2J\033[3J` (full clear), and that `writeRows` output uses `\033[2K` per line and ends with `\033[J`
- [x] 3b: Manual verification checklist: test in iTerm, Terminal.app, Ghostty, Zed terminal, VS Code terminal — verify: no artifacts on resize, native scroll works, clean exit leaves conversation visible with shell prompt below, shell mode (/shell) works cleanly

---

**Success criteria:**
- Mouse/trackpad scroll works natively in all terminals (Zed, Ghostty, iTerm, etc.)
- Terminal resize produces no visual artifacts — full clear + re-render with new width
- Conversation history is accessible via native terminal scrollback after exit
- Exiting herm leaves the terminal in a clean state with shell prompt below the conversation
- Shell mode works without alt screen — clear before/after, full re-render on return
- The robust CSI parser (already implemented) silently consumes any unknown escape sequences
