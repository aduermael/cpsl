# Plan: Consolidate `visibleLen` / `visibleWidth` and harden display calculations

## Context

Two functions exist that do nearly the same thing:

1. **`visibleLen()`** in `models.go:289` — simple loop, only handles `\033[…m` sequences, counts runes (all chars = width 1). Introduced in commit `01bea76` for the Ollama offline indicator.

2. **`visibleWidth()`** in `render.go:94` — regex-based ANSI stripping (handles CSI + OSC sequences) + `uniseg.StringWidth()` for proper Unicode column widths (emoji/CJK = 2 columns).

`visibleWidth()` is strictly better. `visibleLen()` will silently produce wrong results for:
- OSC escape sequences (e.g. hyperlinks, title sequences)
- Wide characters (emoji, CJK)

Both belong in `helpers.go`, the designated utility file, rather than scattered across `render.go` and `models.go`.

### `len()` calls in display contexts

Several places use raw `len()` for display width calculations. While most are currently safe because the measured strings happen to be plain ASCII, they're fragile — any future styling added to those strings will silently break alignment.

**Should replace with `visibleWidth()`:**
- `style.go:207` — `len(durationStr) + 2` for tool box min-width
- `style.go:280` — `len(durationStr)` for bottom border padding

**Should replace `len()` + byte truncation with `visibleWidth()` + `truncateVisual()`:**
- `render.go:534-535` — `len(shortMsg)` / `shortMsg[:a.width]` for approval prompt centering
- `render.go:548-549` — `len(detail)` / `detail[:a.width]` for approval detail centering

**Already correct (measuring plain text parts separately, assembling ANSI afterward):**
- `render.go:652` — `8 + len(a.status.Branch)` — branch name is plain text
- `render.go:661` — `1 + len(delStr) + 1 + len(addStr)` — numeric strings from Sprintf
- `render.go:675` — `1 + len(costStr)` — plain cost string from `formatCost`
- `render.go:668` — already uses `uniseg.StringWidth(commitStr)` (correct, has Unicode arrows)

These status line calculations are correct by design and don't need changing.

### Success criteria
- Only one visible-width function exists, in `helpers.go`
- All display-context width calculations use `visibleWidth()`, not `len()`
- All display-context truncations use `truncateVisual()`, not byte slicing
- All existing tests pass (model_test.go visibleLen tests adapted to visibleWidth)
- render_test.go tests continue to pass unchanged

---

## Phase 1: Consolidate into `helpers.go`

Move `ansiEscRe` and `visibleWidth()` from `render.go` to `helpers.go`. Remove `visibleLen()` from `models.go`. Update all callers in `models.go` to use `visibleWidth()`. Adapt the 6 tests in `model_test.go` (TestVisibleLen_*) to call `visibleWidth()` instead — expected values stay the same since the test strings are all ASCII with basic `\033[…m` codes.

- [x] 1a: Move `ansiEscRe` and `visibleWidth` from `render.go` to `helpers.go`; remove `visibleLen` from `models.go`; update all callers; adapt tests

## Phase 2: Harden display calculations

Replace `len()` with `visibleWidth()` and byte-level truncation with `truncateVisual()` in display-context code.

- [x] 2a: `style.go` — replace `len(durationStr)` with `visibleWidth(durationStr)` on lines 207 and 280
- [x] 2b: `render.go` — replace `len(shortMsg)` / `len(detail)` with `visibleWidth()` and `shortMsg[:a.width]` / `detail[:a.width]` with `truncateVisual()` in the approval prompt section (lines 534-551)

## Phase 3: Verify

- [ ] 3a: Run full test suite, confirm all tests pass
