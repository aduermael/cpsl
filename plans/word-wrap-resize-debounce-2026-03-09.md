# Word Wrapping & Resize Debounce

## Context

- Messages are rendered through `buildBlockRows()` → `wrapString()` pipeline in `main.go`
- `wrapString()` (line ~174) is ANSI-aware but wraps at **character boundaries** — it breaks mid-word whenever column limit is hit
- `wrapLineCount()` (line ~556) is a separate function that does word-aware wrapping but only returns a **count**, not actual lines — logic is duplicated and inconsistent with `wrapString`
- SIGWINCH handler (line ~1361) calls `renderFull()` immediately on every signal — no debounce, so rapid resize events trigger expensive full re-renders
- `renderFull()` resets scroll and clears scrollback, then calls `render()` which rebuilds all block rows

## Key Files

- `main.go`: `wrapString()`, `wrapLineCount()`, `buildBlockRows()`, `render()`, `renderFull()`, SIGWINCH handler
- `wrap_test.go`: Tests for `wrapLineCount()`
- `render_test.go`: Tests for `wrapString()` and rendering

## Failure Modes

- Word wrapping with ANSI codes: splitting on word boundaries must not break mid-escape-sequence or lose active styling on continuation lines
- Words longer than terminal width must fall back to character-level breaking
- Debounce timer must not prevent final render — the last resize event must always trigger a render after the debounce period
- Race condition: debounce timer goroutine and main render loop both write to stdout — need to ensure mutual exclusion or use the existing signal goroutine pattern

---

## Phase 1: Word-aware wrapping

- [x] 1a: Modify `wrapString()` to prefer breaking at word boundaries (spaces) instead of mid-word. When a single word exceeds the available width, fall back to character-level breaking. Keep ANSI-awareness and style re-emission intact. The `startCol` parameter must still work for first-line indentation.
- [x] 1b: Replace `wrapLineCount()` with a thin wrapper that calls `wrapString()` and returns `len(result)`. Remove the duplicated word-wrap logic. Update `wrap_test.go` — existing `wrapLineCount` tests should still pass since both functions now use the same algorithm.
- [ ] 1c: Add tests to `render_test.go` for word-wrap behavior in `wrapString`: basic word wrap, long-word fallback, ANSI codes across word boundaries, startCol offset, and empty/single-word edge cases.

## Phase 2: Debounced resize

- [ ] 2a: Add a debounce mechanism to the SIGWINCH handler. On resize signal: update `a.width`, display a brief "resizing..." indicator (reuse existing info/dim style), and reset a debounce timer (~100ms). Only call `renderFull()` after the timer fires without interruption. Use `time.AfterFunc` or a `time.Timer` — keep it in the existing signal goroutine, no new goroutines needed beyond the timer.
- [ ] 2b: Add a test that verifies debounce behavior: multiple rapid width changes should result in only one final render (or verify the timer reset logic). May need to extract the debounce logic into a testable helper.

## Success Criteria

- Long assistant messages wrap at word boundaries, not mid-word
- ANSI styling is preserved across wrapped lines (no style bleeding or loss)
- `wrapLineCount` and `wrapString` produce consistent results
- Rapid window resizing shows brief "resizing..." then settles to correct layout
- All existing tests in `wrap_test.go` and `render_test.go` still pass
