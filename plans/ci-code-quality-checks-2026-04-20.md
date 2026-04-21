# CI Code-Quality Checks

Goal: add CI-enforced structural rules that keep the codebase simple, stable,
and easy for humans + agents to reason about. Three new rules, all enforced by
a single stdlib-only Go program:

1. **File length** — non-test source files ≤ 1000 lines (current CI: 1750).
2. **Positional params** — Go functions/methods ≤ 1 positional param; multi-arg
   functions take an options struct instead.
3. **File docstring** — every non-test `.go` file starts with a leading doc
   comment whose body is ≥ 60 chars and ≤ 3 lines (already in `GUIDELINES.md`).

Guiding constraints from the user:
- No new third-party dependencies. Use `go/ast` + `go/parser` from stdlib.
- Prefer refactoring over patches; codebase must stay simple and stable.
- Reduce repetition (DRY) — one checker binary, one workflow, shared scan logic.

---

## Codebase Context

- **Repo layout**: main binary at `cmd/herm/`, standalone Go sub-binaries at
  `tools/outline/`, `tools/write-file/`, `tools/edit-file/` (each with its own
  `go.mod`). External code lives at `external-deps-workspace/` and must be
  excluded from all checks.
- **Existing CI workflows**:
  - `.github/workflows/source-file-length.yml` — 1750-line cap on `*.go` files
  - `.github/workflows/prompt-length.yml` — 100-line cap on `prompts/*.md`
  - `.github/workflows/test.yml` — `go test ./...`
- **Files currently over 1000 lines** (non-test):
  - `cmd/herm/render.go` — 1060
  - `cmd/herm/subagent.go` — 1169
  - `cmd/herm/main.go` — 1180
  - `cmd/herm/agent.go` — 1474
- **Docstring compliance**: all sampled non-test files already pass a 60-char
  floor. Shortest body observed ≈ 68 chars (`worktree.go`).
- **Positional-param violations**: ~84 functions in `cmd/herm` and ~19 in
  `tools/` currently have 2+ positional params. No systematic options-struct
  convention exists today beyond ad-hoc `Config` structs.

---

## Contracts & Conventions

### CI checker binary

Single Go program, stdlib only. Location: `tools/ci-check/` with its own
`go.mod` (mirrors existing `tools/outline/` pattern).

Subcommand layout: `ci-check <rule> [paths...]` where `<rule>` is
`file-length`, `docstring`, or `positional-params`. `ci-check all` runs every
rule. Each rule exits non-zero on violation and prints
`path:line:message`-style output so GitHub Actions highlights the file.

Shared scaffolding (DRY target):
- One file-walker that yields `.go` files, honors the exclusion set
  (`*_test.go`, `external-deps-workspace/`, `vendor/`, `tools/ci-check/`).
- One AST-parse helper (`parser.ParseFile` with `parser.ParseComments`).
- One violation reporter that formats and accumulates errors.

### Options-struct convention (for positional-params rule)

When a function needs > 1 positional param, callers pass one struct:

```go
type FooOptions struct { ... }
func Foo(opts FooOptions) ...
```

Naming: `<FuncName>Options` for ad-hoc argument bundles; existing module-level
`Config` structs keep their names. Field order inside the struct is not
constrained.

### File-docstring rule

- Target: the `*ast.File`'s leading doc comment (`file.Doc`, populated by
  `parser.ParseComments`). This is the per-file comment block, not the
  package-level doc.
- Length measured on `file.Doc.Text()` (stdlib already strips `// ` prefixes).
- Floor: 60 characters. Ceiling: 3 lines (already in `GUIDELINES.md`).
- Applies to non-test `.go` files only. Markdown / shell scripts unaffected.

### Positional-params rule

- Applies to `*ast.FuncDecl` nodes in non-test Go files.
- Receiver is **not** counted.
- Variadic final parameter is allowed alongside at most one regular param
  (`func foo(x int, args ...string)` is fine).
- Interface method declarations (`*ast.InterfaceType` fields) are **not**
  checked.
- `context.Context` as the first param is **exempt** from the count.

---

## Resolved Decisions

1. **ctx exemption** — `ctx context.Context` as the first param is **exempt**
   from the positional count. A signature like `func Foo(ctx context.Context,
   x int)` passes; `func Foo(ctx context.Context, x int, y int)` does not
   (still > 1 non-ctx positional param).
2. **Interface method declarations** — **not checked**. Only `*ast.FuncDecl`
   nodes are scanned; interface `*ast.InterfaceType` fields are skipped.
3. **Migration scope** — refactor **all directories at once** in Phase 4. If a
   specific call site turns out to be too invasive to refactor cleanly, flag it
   on the commit and bring it back to the user rather than splitting into a
   phased rollout.
4. **Workflow consolidation** — merge the three source-code rules into a
   single workflow file `.github/workflows/ci-checks.yml`, but use **one
   GitHub Actions job per rule** so file-length, docstring, and
   positional-params each show up as distinct status checks in the PR UI.
   Jobs share a common `build-ci-check` job that compiles the binary once and
   uploads it as an artifact (or, simpler, each job rebuilds — decide at
   implementation time based on whichever is faster and DRYer). `prompt-length.yml`
   stays standalone — prompts are not source code.

---

## Failure Modes

- **Tight coupling during splits** — if a file split drags shared unexported
  helpers into a cycle, colocate the helpers with their primary consumer rather
  than introducing a new shared file. Splits are per Phase 2 proposals; any
  deviation is noted on the commit.
- **Options-struct churn** — refactoring 100+ call sites risks review fatigue.
  Mitigation: one commit per function-or-group, tests run each time.
- **Docstring over/underflow during splits** — newly created files inherit or
  are given a ≥ 60-char doc. Checked in CI after Phase 3.

---

## Tests & Success Criteria

- The checker binary ships with its own tests under `tools/ci-check/`:
  fixture files that violate each rule plus clean counterparts.
- Success: on the `main` branch post-Phase 6, the new workflow passes green
  with all three rules enforced. `go test ./...` stays green throughout.
- Every file split (Phase 2) is verified by running `go test ./...` and by
  confirming no public symbol is added or removed.

---

## Phase 1: CI checker foundation

- [x] 1a: Scaffold `tools/ci-check/` — `go.mod`, `main.go` with subcommand
  dispatch, shared file-walker and AST-parse helpers, shared violation
  reporter. No rules wired yet.
- [x] 1b: Implement `file-length` rule in the new binary, parameterised by a
  max-lines flag (default 1000). Reuse the shared walker.
- [x] 1c: Implement `docstring` rule (floor 60 chars, ceiling 3 lines) using
  `ast.File.Doc`.
- [x] 1d: Implement `positional-params` rule (receiver excluded, variadic
  allowed as final param, `context.Context` as first param exempt, interface
  methods not scanned).
- [x] 1e: Add fixture-based tests in `tools/ci-check/` covering pass/fail for
  each rule, including exclusion-set edge cases.

## Phase 2: Split oversize files

**Parallel Tasks: 2a, 2b, 2c, 2d**

- [x] 2a: Split `cmd/herm/render.go` (1060) into `render.go` +
  `render_subagent.go`. Move `subAgentDisplay`, `subAgentDisplayLines`,
  `formatSubAgentLine`, and related constants to the new file. Keep core
  rendering and input handling in `render.go`. Both files keep file-level
  doc comments.
- [x] 2b: Split `cmd/herm/subagent.go` (1169) into `subagent.go` +
  `subagent_drain.go`. Move `drainSubAgentEvents`, `drainOptions`,
  `drainResult`, and event-processing logic to the new file. Keep
  `SubAgentTool`, config, and the public API in `subagent.go`.
- [x] 2c: Split `cmd/herm/main.go` (1180) into `main.go` + `wiring.go`. Move
  initialization helpers (`startInit`, attachment helpers, clipboard helpers,
  `cleanupTmpDir`) to `wiring.go`. Keep `main`, `newApp`, `Run`, and the core
  event loop in `main.go`. Also moved `handleUpdateCommand` to keep main.go
  under the 1000-line cap.
- [x] 2d: Split `cmd/herm/agent.go` (1474) into `agent.go` + `agent_loops.go`.
  Move `runLoop`, `gracefulExhaustion`, `backgroundCompletion`,
  `clearOldToolResults`, `maybeCompact`, and related retry/context helpers
  to `agent_loops.go`. Keep the `Agent` type, `NewAgent`, `Events`, `Cancel`,
  `ID`, `emit` in `agent.go`.

## Phase 3: Enforce file-length + docstring rules

- [x] 3a: Create `.github/workflows/ci-checks.yml` with one job per rule:
  `file-length` (max 1000), `docstring`, and `positional-params`. The
  `positional-params` job starts in warning mode (`continue-on-error: true`)
  so it stays visible in the PR UI without blocking until Phase 5. Delete
  `.github/workflows/source-file-length.yml`.
- [x] 3b: Run the new workflow locally, fix any stragglers surfaced by the
  docstring rule or by Phase 2 splits (e.g., new files missing doc comments).

## Phase 4: Refactor cmd/herm rendering (20 violations)

**Parallel Tasks: 4a, 4b**

- [x] 4a: `cmd/herm/style.go` (10) — `lerpColor`, `progressBar`, `writeRows`,
  `styledToolResult`, `renderToolBox`, `renderToolGroup`,
  `shouldShowToolOutput`, `hslToRGB`, `approvalGradientSep`, `wrapLineCount`.
  Introduce per-function `*Options` structs; update call sites.
- [x] 4b: `cmd/herm/render.go` + `input.go` (10) — `getVisualLines`,
  `cursorVisualPos`, `padCodeBlockRow`, `wrapString`, `collectToolGroup`,
  `handleByte`, `handleEscapeSequence`, `handleNavKey`,
  `handleModifyOtherKeys`, `handleCSIDigit2`.

## Phase 5: Refactor cmd/herm models/tools/commands (15 violations)

**Parallel Tasks: 5a, 5b**

- [x] 5a: `cmd/herm/models.go` (9) — `supportsServerTools`,
  `ollamaContextWindow`, `filterModelsByProviders`, `findModelByID`,
  `sortModelsByCol`, `formatPricePerM`, `formatModelMenuLines`, `computeCost`,
  `matchSWEScores`.
- [x] 5b: `cmd/herm/tools.go` + `filetools.go` + `commands.go` (6) —
  `NewBashTool`, `NewGitTool`, `NewDevEnvTool`, `outlineFallback`,
  `promptForWorktreeName`, `switchToWorktree`.

## Phase 6: Refactor tools/ binaries (17 violations)

**Parallel Tasks: 6a, 6b, 6c**

- [x] 6a: `tools/write-file/` (7) — `unifiedDiff`, `myersDiff`, `backtrack`,
  `buildHunks`, `newHunk`, `prevHunkEditEnd`, `extendHunk`.
- [x] 6b: `tools/edit-file/` (7) — same diff/hunk helpers as 6a; mirror the
  options-struct design for consistency.
- [ ] 6c: `tools/outline/` (3) — `formatFunc`, `formatGenDecl`, `outlineRegex`.

## Phase 7: Refactor cmd/herm agent core (22 violations)

**Parallel Tasks: 7a, 7b, 7c**

- [ ] 7a: `cmd/herm/agent.go` + `agent_loops.go` (10) —
  `newLangdagClientForProvider`, `SetTurnProgress`, `SetTokenProgress`,
  `NewAgent`, `Run`, `emitUsage`, `backgroundCompletion`,
  `clearOldToolResults`, `maybeCompact`, `runLoop`.
- [ ] 7b: `cmd/herm/subagent.go` (7) — `forwardBlockingWithTimeout`,
  `saveNodeID`, `runBackground`, `gracefulSubAgentSynthesis`, `buildResult`,
  `formatSubAgentResult`, `writeOutputFile`.
- [ ] 7c: `cmd/herm/systemprompt.go` + `tooldesc.go` + `agentui.go` (5) —
  `buildSystemPrompt`, `buildSubAgentSystemPrompt`, `loadToolDescriptions`,
  `getToolDescription`, `formatToolDefinitions`.

## Phase 8: Refactor cmd/herm runtime/infra (22 violations)

**Parallel Tasks: 8a, 8b, 8c**

- [ ] 8a: `cmd/herm/trace.go` (10) — `SetGitInfo`, `AddTextDelta`, `SetUsage`,
  `StartToolCall`, `EndToolCall`, `AddApproval`, `AddCompaction`, `AddRetry`,
  `BuildSubAgentEvent`, `writeTraceFile`.
- [ ] 8b: `cmd/herm/background.go` + `container.go` (8) — `bootContainerCmd`,
  `ensureImageLocal`, `buildContainerImage`, `buildProjectTree`, `Start`,
  `Exec`, `ExecWithStdin`, `Rebuild`.
- [ ] 8c: `cmd/herm/worktree.go` + `compact.go` (4) — `createWorktree`,
  `lockWorktree`, `compactConversation`, `callLLMDirect`.

## Phase 9: Refactor cmd/herm config + content (22 violations)

**Parallel Tasks: 9a, 9b, 9c**

- [ ] 9a: `cmd/herm/config.go` + `configeditor.go` (9) — `preferredDefault`,
  `ollamaModelProvider`, `mergeConfigs`, `saveConfigTo`, `saveProjectConfig`,
  `openConfigModelPicker`, `doOpenConfigModelPicker`, `handleConfigByte`,
  `handleConfigEditByte`.
- [ ] 9b: `cmd/herm/content.go` (8) — `expandPastes`, `expandAttachments`,
  `isAgentStatusCheck`, `isSleepWaitCommand`, `isBackgroundAgentCall`,
  `toolCallSummary`, `approvalCmdDesc`, `approvalShortDesc`.
- [ ] 9c: `cmd/herm/helpers.go` + `history.go` (5) — `truncateWithEllipsis`,
  `truncateVisual`, `truncateForLog`, `newDebouncer`, `newHistory`.

## Phase 10: Refactor cmd/herm markdown/tree (6 violations)

- [ ] 10a: `cmd/herm/markdown.go` + `tree.go` (6) — `processMarkdownLine`,
  `indexByte`, `indexPair`, `indexDouble`, `indexSingleStar`, `truncate`.

## Phase 11: Enforce positional-params rule

- [ ] 11a: Flip the `positional-params` job in
  `.github/workflows/ci-checks.yml` from `continue-on-error: true` to a hard
  fail after confirming no remaining violations across `cmd/herm/` and
  `tools/`.
- [ ] 11b: Update `GUIDELINES.md` with a short section describing the three
  enforced rules (file length ≤ 1000, ≤ 1 positional param with `ctx`
  exempt, file docstring ≥ 60 chars / ≤ 3 lines) and pointing at
  `tools/ci-check/`.

## Phase 12: Cleanup

- [ ] 12a: Audit `.github/workflows/` — confirm `test.yml`, `ci-checks.yml`,
  `prompt-length.yml`, and release workflows are the full set. Remove any
  dead references to the old `source-file-length.yml`.
- [ ] 12b: Smoke-test the full pipeline against `main` by opening a dry-run PR
  or running the workflow locally; confirm green across all rules.
