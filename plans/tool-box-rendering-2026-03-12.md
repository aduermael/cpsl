# Tool Box Rendering

## Context

Tool calls and results are currently rendered as two separate messages:
- `msgToolCall`: dim+italic text like `~ glob` or `~ $ ls -la` (via `toolCallSummary()`)
- `msgToolResult`: dim text with the output (via `collapseToolResult()`, max 10 lines → 4 head + 3 tail)

These are styled independently in `styledToolCall()` and `styledToolResult()` (main.go ~451-460), then rendered per-line in `buildBlockRows()` (main.go ~1171). The event handlers at main.go ~3760-3790 create these as consecutive chatMessages.

## Goal

Merge tool call + result into a single bordered box:

```
┌ ~ glob ───────┐
.cpsl/.DS_Store
.cpsl/Dockerfile
...
hello.ts
main.go
└───────────────┘
```

Rules:
- Top border: `┌ <title> ─...─┐` — title is the tool call summary (already exists via `toolCallSummary()`)
- Bottom border: `└─...─┘` — same width as top
- No left/right borders (content is bare for easy copy/paste)
- Width adapts to content and title — use minimum width needed (max of title width and longest content line)
- Content truncated to **5 lines max**: first 2 + `...` + last 2 (or first 2 + last 3 if exactly 5 lines)
- Applies to all tool types (glob, bash, grep, read, write, edit, etc.)

## Key files

- `main.go:692` — `toolCallSummary()`: generates title text
- `main.go:722` — `collapseToolResult()`: current truncation (needs updating)
- `main.go:451-460` — `styledToolCall()`, `styledToolResult()`: current ANSI styling
- `main.go:484` — `renderMessage()`: dispatches styling by message kind
- `main.go:1171` — `buildBlockRows()`: main rendering pipeline, wraps and assembles rows
- `main.go:3760-3790` — event handlers that create the two messages

## Design decisions

**Merging approach**: Rather than changing the message model (which would require touching event handlers, compaction, usage tracking, etc.), keep `msgToolCall` and `msgToolResult` as separate messages but render them together. In `buildBlockRows()`, when we encounter a `msgToolCall` followed by a `msgToolResult`, render them as a single box.

**Truncation change**: `collapseToolResult()` currently uses 10-line threshold with 4+3 split. Change to: 5-line threshold with 2+2 split (and `...` middle), or 2+3 if exactly 5 lines. Empty results should still get a box (just borders with no content lines).

**Width calculation**: Compute visible width of title and each content line. Box width = max(title_width + 4, longest_content_line + 0) + 2 for the corner chars. Actually simpler: box inner width = max(visible_width(title) + 2, max_content_line_width), then top = `┌ title ` + `─` * remaining + `┐`, bottom = `└` + `─` * (inner_width) + `┘`.

**Wrapping**: Content lines that exceed terminal width need to either be truncated or wrapped. Since box has no side borders, long lines can just flow naturally with `wrapString()`. The top/bottom borders should be capped at terminal width.

**ANSI styling**: The entire box (borders + content) should use dim (`\033[2m`). The title within the top border could be dim+italic as it is now.

## Open questions

- Should error tool results (red styling) still get the box? → Yes, but use red for the border and content instead of dim.

## Phase 1: Update truncation logic

Change `collapseToolResult()` to use the new 5-line rules.

- Current: ≤10 lines show all, >10 lines shows 4 head + 3 tail
- New: ≤5 lines show all, >5 lines shows 2 head + `...` + 2 tail
- Special: exactly 5 lines → show first 2 + last 3 (no `...`)

Wait — re-reading the spec: "display 5 lines at most with ... in the middle if more than 5" and "always display first 2 lines and last 2 lines, or 3 last if exactly 5". So:
- ≤4 lines: show all
- exactly 5: show first 2 + last 3 (= all 5, no ellipsis needed since 2+3=5)
- >5: show first 2 + `...` + last 2

- [x] 1a: Update `collapseToolResult()` with new thresholds (≤4 → all, 5 → first 2 + last 3, >5 → first 2 + `...` + last 2)
- [x] 1b: Update `TestCollapseToolResult` and add cases for 4, 5, 6, and >6 lines

## Phase 2: Add box rendering function

Create a function that takes a title string and content string, returns the bordered box as a single string (newline-separated rows). This is a pure function that can be unit-tested independently.

Signature: `func renderToolBox(title, content string, maxWidth int, isError bool) string`

- Computes box width from title and content
- Caps at maxWidth (terminal width)
- Returns `┌ title ─...─┐\ncontent_line1\n...\n└─...─┘`
- Dim styling on borders; error variant uses red
- Empty content → just top + bottom border (no content lines between)

- [ ] 2a: Implement `renderToolBox()` — builds top border, content lines, bottom border
- [ ] 2b: Add tests for `renderToolBox()`: short title, long title, empty content, error variant, width capping

## Phase 3: Integrate into rendering pipeline

Wire the box rendering into the existing message flow.

- In `buildBlockRows()`: when processing a `msgToolCall` at index `i`, check if `i+1` is a `msgToolResult`. If so, render them together as a box using `renderToolBox(call.content, result.content, a.width, result.isError)` and skip the result message.
- If a `msgToolCall` has no following `msgToolResult` (e.g., tool still running), render just the top border (open box).
- If a `msgToolResult` appears without a preceding `msgToolCall` (shouldn't happen normally), render it with a generic box.
- Remove or bypass `styledToolCall()` and `styledToolResult()` since the box renderer handles styling.

- [ ] 3a: Modify `buildBlockRows()` to detect tool call + result pairs and render as boxes
- [ ] 3b: Handle edge case: tool call without result (in-progress) — render open box (top border only)
- [ ] 3c: Verify existing render tests still pass; add integration test with mock messages

## Phase 4: Polish and edge cases

- [ ] 4a: Ensure box borders wrap correctly when terminal is narrower than box width
- [ ] 4b: Verify tool call display in `/usage` tree view is unaffected (it uses different rendering)
- [ ] 4c: Test with real tool outputs: bash with long command, glob with many files, error results

## Success criteria

- Tool calls render in bordered boxes with `┌ title ─┐` / `└─┘` borders
- Content limited to 5 visible lines (2 head + ... + 2 tail, or 2+3 for exactly 5)
- Box width adapts to content (not full terminal width)
- No side borders (content can be copy/pasted)
- Error results get red-styled boxes
- All existing tests pass
- New tests cover truncation, box rendering, and pipeline integration
