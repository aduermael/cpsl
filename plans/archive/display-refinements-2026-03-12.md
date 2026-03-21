# Display Refinements

## Context

The TUI renders messages via `buildBlockRows()` (main.go:1144) which calls `renderMessage()` → `processMarkdownLine()` (markdown.go) → `wrapString()` (main.go:203). Each row is output via `writeRows()` which clears the line with `\033[2K` before writing content.

### Current styling codes
- Inline code (`backtick`): `\033[48;5;237m` bg + `\033[49m` reset (markdown.go:89-91)
- Fenced code blocks: `\033[48;5;236m\033[38;5;248m` bg+fg + `\033[0m` reset (markdown.go:27)
- Tool call (command line): `\033[3;38;5;242m` italic+dim gray (main.go:430)
- Tool result (output): `\033[38;5;242m` dim gray per-line (main.go:438-439)
- Tests (markdown_test.go) expect `\033[7m`/`\033[27m` (reverse video) but source uses 256-color bg — tests are stale

## Phase 1: Enforce max 1 blank line gap

**Problem:** Multiple consecutive blank rows can appear in output — from `leadBlank`, trailing blank insertion in `buildBlockRows()`, empty lines in assistant content, or skipped fence lines leaving adjacent blanks.

**Where:** `buildBlockRows()` in main.go (lines 1144-1192)

**Approach:** After all rows are assembled (messages + streaming + sub-agent), collapse any run of 2+ consecutive empty rows into a single empty row. An "empty row" is one that's either `""` or contains only ANSI reset sequences. This is a display-time enforcement — doesn't touch message content.

- [x] 1a: Add blank-line collapsing pass at end of `buildBlockRows()`, after all rows are built
- [x] 1b: Add test for the collapsing behavior (e.g. input with 3 consecutive blanks → 1)

## Phase 2: Fix inline code background bleed

**Problem:** Inline code backtick styling uses `\033[48;5;237m` for background. When `wrapString()` wraps a line and re-emits active ANSI sequences on the continuation line, the background starts at column 0 of the new line — visually bleeding into the previous line's end (the terminal fills background to where the escape starts on the new line). Also visible in the screenshot: background appears to start on the previous line when code is at the start of a line.

**Where:** `renderInlineMarkdown()` in markdown.go (lines 84-95)

**Root cause analysis:** The `\033[49m` (reset bg) at the end of the code span correctly turns off background. But the `\033[2K` (erase entire line) in `writeRows()` clears the line BEFORE writing content. The issue is likely that when a code span starts at column 0, the background color from a previous line's active sequences leaks. Need to investigate: does `wrapString()` carry over a bg sequence incorrectly when the reset `\033[49m` should have cleared it from `activeSeqs`?

**Key insight:** `applyANSI()` in `wrapString()` (line 242) only clears `activeSeqs` on full reset `\033[0m`. The `\033[49m` (reset bg only) gets *appended* to activeSeqs rather than removing the bg entry. So on wrap, the continuation line re-emits `\033[48;5;237m` then `\033[49m` — but if there's no wrap and code is at line start, the issue may be different.

**Approach:** Make `applyANSI()` smarter about partial resets (49m cancels 48;5;Nm), OR switch inline code to use `\033[7m`/`\033[27m` (reverse video) which doesn't have the background-fill issue and matches what tests already expect. Reverse video is cleaner since it adapts to the terminal theme.

- [x] 2a: Switch inline code to reverse video (`\033[7m`/`\033[27m`) — this matches the existing tests and avoids background bleed entirely
- [x] 2b: Verify the fix works with wrapping by checking the `wrapString()` activeSeqs handling for `\033[27m` (reverse-off) — it may also need the same partial-reset fix
- [x] 2c: Update or verify markdown_test.go expectations match the new rendering

## Phase 3: Fix fenced code block background

**Problem:** Fenced code block lines use `\033[48;5;236m\033[38;5;248m` + full `\033[0m` reset. The full reset means `activeSeqs` is properly cleared. However, the background only covers actual text characters, not the full terminal width — so wrapped continuation lines may show inconsistent background. Also, the `\033[0m` at end of each code line means `wrapString()` loses all styling on wrap continuation.

**Where:** `processMarkdownLine()` in markdown.go (line 27), `wrapString()` in main.go

**Approach:** For code block lines, pad the visible content to terminal width so background fills the entire line. This must happen AFTER wrapping. Options:
1. Pad each wrapped row to `a.width` with spaces while bg is active (before the reset)
2. Use `\033[K` (erase to end of line with current bg) instead of padding — but this interacts with `\033[2K` in writeRows

Option 1 is simpler and more predictable. The padding needs to account for visual width (ANSI sequences don't count).

- [x] 3a: In `buildBlockRows()`, after wrapping code block lines, pad each resulting row to full terminal width with the code block background active
- [x] 3b: Ensure the `\033[0m` reset comes AFTER the padding, not before
- [x] 3c: Test that code block lines fill terminal width and wrapped code lines also get full-width background

## Phase 4: Dim command output further

**Problem:** Tool result output (`styledToolResult`) uses `\033[38;5;242m` (color 242 from 256 palette). The user wants it even less contrasted — ideally using a terminal theme index color for better theme compatibility.

**Where:** `styledToolResult()` in main.go (lines 433-442), `styledToolCall()` (line 429-431)

**Current styling:**
- Tool call (command): `\033[3;38;5;242m` (italic + fg 242)
- Tool result (output): `\033[38;5;242m` (fg 242, per-line)

**Approach:** Use `\033[2m` (dim attribute) instead of or in addition to the 256-color index. Dim adapts to the terminal theme and is guaranteed to be less prominent. For tool results specifically, use `\033[2m` (dim) alone — this will be theme-aware and even less contrasted than the command line itself (which uses italic+dim-gray). Alternatively, combine `\033[2;3m` (dim+italic) for both, with tool results getting just `\033[2m`.

- [x] 4a: Change `styledToolResult()` to use `\033[2m` (dim) instead of `\033[38;5;242m` per-line — this is less contrasted and theme-aware
- [x] 4b: Optionally adjust `styledToolCall()` to use `\033[2;3m` (dim+italic) instead of `\033[3;38;5;242m` for consistency
- [x] 4c: Verify both tool calls and results render with appropriate contrast levels

## Success Criteria

- No consecutive blank lines >1 in rendered output
- Inline code backtick background doesn't bleed to previous/next line
- Fenced code blocks have full-width background on every line (including wrapped)
- Tool output is visually dimmer than assistant text, using theme-aware attributes
- All existing tests pass (updated as needed)
