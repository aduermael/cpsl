# SWE-bench Scores & Sortable Model Table

**Goal:** Fetch SWE-bench Verified scores from the public leaderboard, enrich model definitions with benchmark data, and redesign the `/model` view as a columnar table sortable by left/right arrow keys (model name, provider, price, SWE-bench score). Default sort: SWE-bench score descending.

## Codebase Context

- **models.go** — `ModelDef` struct (Provider, ID, DisplayName, PromptPrice, CompletionPrice), `fetchModels()` fetches from OpenRouter API, `parseOpenRouterModels()` converts results.
- **modellist.go** — `modelList` component with cursor/scroll navigation (up/down/j/k), `View()` renders a simple list with model name, provider, price inline. No column sorting.
- **main.go** — `fetchModelsCmd()` runs async at startup via `Init()`. Result arrives as `modelsMsg`. `enterModelMode()` builds list from `config.availableModels()`.
- **models_test.go** — Tests for parsing, filtering, pricing. Uses `testModels()` fixture.
- **model_test.go** — TUI tests for model mode navigation, selection, enter/esc.

## Data Source

SWE-bench Verified leaderboard is publicly available at:
`https://raw.githubusercontent.com/SWE-bench/swe-bench.github.io/master/data/leaderboards.json`

Structure: `{"leaderboards": [{"name": "Verified", "results": [...]}]}`. Each result has:
- `name`: display name (e.g. "live-SWE-agent + Claude 4.5 Opus medium")
- `resolved`: float score (e.g. 79.2)
- `tags`: array including `"Model: claude-opus-4-5-20251101"` entries

Multiple entries may reference the same underlying model (different agent systems). We take the **highest** `resolved` score per model.

**Mapping strategy:** Extract model identifier from `"Model: xxx"` tags, then fuzzy-match against OpenRouter model IDs. The SWE-bench tag value (e.g. `claude-opus-4-5-20251101`) typically appears as a suffix or substring of the OpenRouter ID (e.g. `anthropic/claude-opus-4-5-20251101`). Match by checking if the OpenRouter ID contains the tag value, or if the tag value contains the OpenRouter ID suffix (after the provider prefix like `anthropic/`, `openai/`, `x-ai/`).

## Design Decisions

- **New field:** Add `SWEScore float64` to `ModelDef` (0 means no data)
- **Fetch in parallel:** SWE-bench data fetched concurrently with OpenRouter models at startup. Merge scores into models after both arrive.
- **Graceful degradation:** If SWE-bench fetch fails, models still display with score column showing `—`
- **Sort columns:** 4 sortable columns — Model Name (alpha asc), Provider (alpha asc), Price (prompt price asc), SWE-bench (desc). Left/right arrow keys cycle active sort column. Active column highlighted in header.
- **Table layout:** Fixed-width columns with header row and separator. Cursor (`▸`) on left, active marker (`●`) on right.

## Failure Modes

- SWE-bench JSON is ~7MB — could be slow or fail. Use timeout and don't block app startup.
- Fuzzy matching may miss some models — that's acceptable, they just show `—` for score.
- SWE-bench leaderboard file could change format — defensive parsing with fallback to empty scores.
- The leaderboard has entries that use multiple models (e.g. `"Model: claude-4-sonnet", "Model: o3-mini"`) — skip entries with multiple Model tags or attribute to first.

## Phase 1: SWE-bench Data Fetching & Model Enrichment

- [ ] 1a: Add `SWEScore float64` field to `ModelDef` in `models.go`. Add SWE-bench API types (`sweBenchResponse`, etc.) and a `fetchSWEScores()` function that fetches the leaderboard JSON, parses the "Verified" results, extracts model tags, and returns a `map[string]float64` mapping model tag identifiers to their best (highest) resolved score.
- [ ] 1b: Add a `matchSWEScores()` function that takes `[]ModelDef` and the SWE score map, and enriches each model's `SWEScore` by fuzzy-matching OpenRouter IDs against SWE-bench model tags. Add a new `sweScoresMsg` message type in `main.go`, a `fetchSWEScoresCmd()` tea.Cmd, fire it from `Init()` alongside model fetch, and handle the result in `Update()` to merge scores into `m.models`.
- [ ] 1c: Add tests for SWE-bench parsing (mock JSON → score map), fuzzy matching logic (known model IDs → expected scores), and graceful handling of missing/malformed data.

## Phase 2: Sortable Table UI

- [ ] 2a: Add `sortColumn` and `sortAsc` fields to `modelList`. Define sort column constants (colName, colProvider, colPrice, colSWE). Add left/right arrow key handling in `modelList.Update()` to cycle sort column. Add a `sortModels()` method that sorts the model slice by current column/direction. Default: `colSWE` descending.
- [ ] 2b: Redesign `modelList.View()` as a columnar table — header row with column names (active column highlighted), separator line, model rows with aligned columns (name, provider, price, score). Update hint line to include `←/→` for sort. Adjust `modelListChrome` constant for new header/separator rows.
- [ ] 2c: Add tests for sort behavior — left/right arrow changes sort column, models reorder correctly by each column, default sort is SWE-bench descending, cursor position preserved after re-sort.

## Phase 3: Integration Tests

- [ ] 3a: Add integration tests covering: models with SWE scores display in table format, sorting by different columns via arrow keys, SWE-bench fetch failure shows graceful fallback (`—` in score column), full flow of configure key → `/model` → see sorted table → select model.

## Success Criteria

- SWE-bench Verified scores fetched at startup and matched to OpenRouter models
- `/model` shows a columnar table with Model Name, Provider, Price, SWE-bench columns
- Left/right arrow keys cycle sort column, models re-sort immediately
- Default sort is SWE-bench score descending (highest first)
- Models without SWE-bench data show `—` and sort to bottom
- All existing tests continue to pass
- New tests cover fetching, matching, sorting, and table display
