# File Size Limits & Coding Guidelines

## Context

The codebase has grown organically and several files exceed the 1000-line target:

| File | Lines | Status |
|------|-------|--------|
| `cmd/herm/main.go` | 5,637 | **Critical** — needs splitting into ~6-8 files |
| `cmd/herm/agent.go` | 858 | Approaching limit — monitor |
| `cmd/herm/filetools.go` | 783 | Approaching limit — monitor |
| `cmd/herm/tree.go` | 568 | OK |
| `cmd/herm/tools.go` | 507 | OK |
| `cmd/herm/subagent.go` | 435 | OK |
| `cmd/herm/models.go` | 422 | OK |

Additionally, only 3 of 46 .go files have package-level doc comments (6.5% coverage). No coding guidelines document exists.

Test files over 1000 lines (`agent_test.go` at 1,871, `filetools_test.go` at 1,329, `tree_test.go` at 1,084) are excluded from splitting — large test files that mirror source structure are acceptable.

### Key constraints

- All source files are in `package main` — splitting is purely organizational (move functions/types to new files), no import changes needed within `cmd/herm/`.
- The `App` struct in main.go has 120+ fields and is the central state container. Methods on `App` can live in any file in the same package.
- Existing tests reference functions by name, not by file — splits should not break any tests.
- Each new file needs a package-level doc comment explaining its purpose.

---

## Phase 1: Create coding guidelines document

- [x] 1a: Create `GUIDELINES.md` at the repo root with rules covering: max file size (1000 lines for source, flexible for tests), package-level doc comments required on every non-test .go file, and other Go style conventions observed in the codebase (naming, error handling, test patterns)

## Phase 2: Add doc comments to existing files

Add a doc comment block before `package main` in every non-test .go file that lacks one. The comment should be 1-3 lines describing the file's purpose. Files to update:

- [x] 2a: `cmd/herm/main.go`, `cmd/herm/agent.go`, `cmd/herm/config.go`
- [x] 2b: `cmd/herm/models.go`, `cmd/herm/container.go`, `cmd/herm/tools.go`
- [x] 2c: `cmd/herm/filetools.go`, `cmd/herm/subagent.go`, `cmd/herm/compact.go`
- [x] 2d: `cmd/herm/systemprompt.go`, `cmd/herm/skills.go`, `cmd/herm/history.go`
- [x] 2e: `cmd/herm/worktree.go`, `cmd/herm/tree.go`, `cmd/herm/term.go`, `cmd/herm/dockerfiles.go`
- [x] 2f: `tools/outline/main.go`, `tools/edit-file/main.go`, `tools/write-file/main.go`, `cmd/debug/main.go`

**Parallel Tasks: 2a, 2b, 2c, 2d, 2e, 2f**

## Phase 3: Split main.go — extract rendering

main.go's rendering functions (~1500 lines) are the largest cohesive group and have the fewest entanglements with other logic.

- [x] 3a: Create `render.go` — move all rendering/display functions: `getVisualLines`, `cursorVisualPos`, `visibleWidth`, `padCodeBlockRow`, `wrapString`, `buildLogo`, `writeRows`, `renderMessage`, `renderToolBox`, `styledUserMsg`, `styledAssistantText`, `styledError`, `progressBar`, `lerpColor`, `hslToRGB`, `buildBlockRows`, `buildInputRows`, `positionCursor`, and related types/constants. Add doc comment.
- [x] 3b: Verify all tests pass after the rendering extraction

## Phase 4: Split main.go — extract content processing

- [x] 4a: Create `content.go` — move content/attachment processing: `expandPastes`, `expandAttachments`, `isFilePath`, `isImageExt`, `mimeForExt`, `toolCallSummary`, `approvalCmdDesc`, `collapseToolResult`, `collapseDiff`, `compactLineNumbers`, `isDiffContent`, `diffLineStyle`, and related types/constants. Add doc comment.
- [x] 4b: Verify all tests pass after content extraction

## Phase 5: Split main.go — extract session management

- [x] 5a: All session functions already live in tree.go — no extraction needed
- [x] 5b: No changes made, tests already passing

## Phase 6: Split main.go — extract input handling

- [x] 6a: Create `input.go` — move input and key event handling: key code constants, `EventKey`, `EventPaste`, `EventResize` types, `Key`/`Modifier` enums, input processing methods on App (key dispatch, history navigation, paste handling). Add doc comment.
- [x] 6b: Verify all tests pass after input extraction

## Phase 7: Split main.go — extract background tasks and utilities

- [x] 7a: Create `background.go` — move async/background functions: `bootContainerCmd`, `ensureImageLocal`, `fetchStatusCmd`, `fetchCommitInfo`, `fetchProjectSnapshot`, `fetchSWEScoresCmd`, and related helpers. Add doc comment.
- [x] 7b: Create `helpers.go` — move standalone utility functions: `formatDuration`, `truncateWithEllipsis`, `truncateVisual`, `debugLog`, `gitRepoRoot`, color helpers, and any remaining small functions that don't fit elsewhere. Add doc comment.
- [x] 7c: Verify all tests pass after phase 7 extractions

## Phase 8: Split main.go — extract config editor

main.go is still ~2300 lines after phase 7. The config editor UI (~530 lines) is a self-contained mode with its own types and methods.

- [x] 8a: Create `configeditor.go` — move config editor types and methods: `cfgTabNames`, `cfgField` struct, `maskKey`, `enterConfigMode`, `exitConfigMode`, `openConfigModelPicker`, `cfgCurrentFields`, `settingsTabFields`, `projectTabFields`, `buildConfigRows`, `handleConfigByte`, `handleConfigEditByte`. Add doc comment.
- [ ] 8b: Verify all tests pass after config editor extraction

## Phase 9: Split main.go — extract commands and shell mode

The command dispatch (~360 lines) and shell mode (~58 lines) are cohesive command-handling logic.

- [ ] 9a: Create `commands.go` — move slash command handling: `handleCommand`, `handleCompactCommand`, `handleUsageCommand`, `promptForWorktreeName`, `switchToWorktree`, `isInWorktree`, `enterShellMode`. Add doc comment.
- [ ] 9b: Verify all tests pass after commands extraction

## Phase 10: Split main.go — extract agent orchestration

The agent UI methods (~418 lines) bridge the App and Agent types. agent.go (860 lines) is too large to absorb them, so they go in a new file.

- [ ] 10a: Create `agentui.go` — move agent orchestration methods on App: `showModelChange`, `maybeShowInitialModels`, `startAgent`, `drainAgentEvents`, `handleAgentEvent`. Add doc comment.
- [ ] 10b: Verify all tests pass and that main.go is under 1000 lines

## Phase 11: Final validation

- [ ] 11a: Run full test suite, verify no regressions
- [ ] 11b: Verify all source files are under 1000 lines; verify all non-test .go files have doc comments; update GUIDELINES.md if any rules changed during implementation

### Success criteria

- All non-test .go source files are ≤1000 lines
- Every non-test .go file has a package-level doc comment
- `GUIDELINES.md` exists at repo root with file-size and documentation rules
- All existing tests pass without modification (or with minimal test file moves)
- main.go is reduced from 5,637 lines to roughly 800-1000 lines
