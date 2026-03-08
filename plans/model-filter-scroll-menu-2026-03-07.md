# Model Filtering & Scrollable Menus

## Context

- Models displayed via `/model` command using `menuLines`/`menuCursor`/`menuActive` in `main.go`
- Currently shows all items with no limit, no sorting, no columns
- `ModelDef` has: Provider, ID, DisplayName, PromptPrice, CompletionPrice, SWEScore — no context window field
- `Config.ModelSortDirs` (`map[string]bool`) exists but is unused
- `term.go` has `getTerminalHeight()` for terminal height
- Menu navigation: Up/Down arrows for cursor, Enter to select, Escape to close
- Left/Right arrows currently move text cursor (not used when menu is active)

## Open Questions

- Context window sizes are not in ModelDef. Need to add a `ContextWindow int` field (token count).
- The user wants a JSON file for model data. Current models are in `builtinModels()` Go function. Migration needed.
- "Filter" in the user's description means **sorting columns** (name, provider, price, context) with ASC/DESC toggle — not text-based filtering.

---

## Phase 1: Model data JSON file and context window field

- [x] 1a: Create `models.json` at repo root with all current models plus a `context_window` field (tokens). Add context window values for each model (e.g., Claude Opus 4.6 = 200000, GPT-4o = 128000, Gemini 2.5 Pro = 1000000, etc.). Structure: array of objects with provider, id, display_name, prompt_price, completion_price, context_window.
- [x] 1b: Add `ContextWindow int` field to `ModelDef` struct. Replace `builtinModels()` to load from embedded `models.json` (use `//go:embed`). Update tests.
- [x] 1c: Add unit test that validates `models.json` loads correctly and all entries have required fields populated.

## Phase 2: Scrollable menu with 60% height cap

- [x] 2a: Add scroll state to menu: `menuScrollOffset int` in the App struct. Compute `menuMaxVisible` as `60% of getTerminalHeight()`. In `buildInputRows`, only render the visible window slice `[scrollOffset : scrollOffset+maxVisible]`.
- [x] 2b: Update Up/Down key handling to adjust `menuScrollOffset` when cursor moves beyond visible bounds (scroll up when cursor < offset, scroll down when cursor >= offset+maxVisible). No wraparound — stop at top/bottom.
- [x] 2c: Render a position indicator line below the menu items showing `(first->last / total)` format, e.g., `(3->10 / 40)`. Style it dim like status indicators.
- [x] 2d: Apply the same scroll behavior to `/branches` and `/worktrees` menus (they share the same menu system, so this should work automatically — verify and test).

## Phase 3: Multi-column display for model menu

- [x] 3a: Format model menu lines as aligned columns: Name, Provider, Price (prompt price), Context Window. Use fixed-width formatting with padding so columns align. Price shown as `$X.XX/M`. Context window shown as `XXXk` or `X.Xm`.
- [ ] 3b: Add a header row at the top of the model menu (not selectable, rendered before the scrollable items) showing column names. Highlight the currently active sort column.

## Phase 4: Column sorting with Left/Right and Tab

- [ ] 4a: Add `menuSortCol int` (0=name, 1=provider, 2=price, 3=context) to App struct. When menu is active and Left/Right arrow pressed, change active sort column. Re-sort the model list and re-render.
- [ ] 4b: Add `menuSortAsc bool` to App struct. Tab toggles ASC/DESC for current column. Update header to show sort direction indicator (▲/▼). Re-sort and re-render.
- [ ] 4c: Persist sort preferences: on sort change, save `ModelSortDirs` to config (reuse existing field). On `/model` open, restore last sort column and direction from config. Update `ModelSortDirs` format if needed — current `map[string]bool` may need to also store which column is active.
- [ ] 4d: Add tests for sorting logic: sort by each column ASC/DESC, verify order. Test persistence round-trip.

## Phase 5: Integration testing and polish

- [ ] 5a: Test the full flow: open `/model`, verify scroll indicator, navigate with arrows, change sort column, toggle direction, select a model. Verify config saves sort preferences.
- [ ] 5b: Handle edge cases: fewer models than max visible (no scroll needed), single model, terminal resize while menu is open (recalculate maxVisible).

---

## Success Criteria

- `/model` shows a multi-column table (name, provider, price, context window) with aligned columns
- Menu shows at most 60% of terminal height entries, scrolls with Up/Down
- Position indicator `(first->last / total)` visible when items exceed view
- Left/Right changes sort column, Tab toggles ASC/DESC
- Sort direction indicator (▲/▼) shown in header on active column
- Sort preferences persist across sessions in `.cpsl/config.json`
- All other menus (`/branches`, `/worktrees`) also respect the 60% height cap with scroll
- Model data loaded from `models.json` (embedded in binary)
