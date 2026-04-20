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
  checked in the first cut — see open questions.
- `context.Context` as the first param: see open questions.

---

## Open Questions (resolve before Phase 4 starts)

1. **ctx exemption** — should `ctx context.Context` as the first param be
   excluded from the positional count? Standard Go idiom argues yes; a strict
   reading of "0 or 1" argues no. Default proposal: **yes, exempt**.
2. **Interface method declarations** — check them, or skip? Default: **skip**.
3. **Migration scope** — enforce positional-params rule against all directories
   at once, or start with `cmd/herm` and extend? Default: **all at once** after
   Phase 4 refactor completes.
4. **Workflow consolidation scope** — merge the three length checks
   (file-length, docstring, positional-params) into one workflow; keep
   `prompt-length.yml` separate since prompts are not source code. Default:
   **merge the three, keep prompts standalone**.

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

- [ ] 1a: Scaffold `tools/ci-check/` — `go.mod`, `main.go` with subcommand
  dispatch, shared file-walker and AST-parse helpers, shared violation
  reporter. No rules wired yet.
- [ ] 1b: Implement `file-length` rule in the new binary, parameterised by a
  max-lines flag (default 1000). Reuse the shared walker.
- [ ] 1c: Implement `docstring` rule (floor 60 chars, ceiling 3 lines) using
  `ast.File.Doc`.
- [ ] 1d: Implement `positional-params` rule (receiver excluded, variadic
  allowed, `context.Context` exempt pending open-question resolution).
- [ ] 1e: Add fixture-based tests in `tools/ci-check/` covering pass/fail for
  each rule, including exclusion-set edge cases.

## Phase 2: Split oversize files

**Parallel Tasks: 2a, 2b, 2c, 2d**

- [ ] 2a: Split `cmd/herm/render.go` (1060) into `render.go` +
  `render_subagent.go`. Move `subAgentDisplay`, `subAgentDisplayLines`,
  `formatSubAgentLine`, and related constants to the new file. Keep core
  rendering and input handling in `render.go`. Both files keep file-level
  doc comments.
- [ ] 2b: Split `cmd/herm/subagent.go` (1169) into `subagent.go` +
  `subagent_drain.go`. Move `drainSubAgentEvents`, `drainOptions`,
  `drainResult`, and event-processing logic to the new file. Keep
  `SubAgentTool`, config, and the public API in `subagent.go`.
- [ ] 2c: Split `cmd/herm/main.go` (1180) into `main.go` + `wiring.go`. Move
  initialization helpers (`startInit`, attachment helpers, clipboard helpers,
  `cleanupTmpDir`) to `wiring.go`. Keep `main`, `newApp`, `Run`, and the core
  event loop in `main.go`.
- [ ] 2d: Split `cmd/herm/agent.go` (1474) into `agent.go` + `agent_loops.go`.
  Move `runLoop`, `gracefulExhaustion`, `backgroundCompletion`,
  `clearOldToolResults`, `maybeCompact`, and related retry/context helpers
  to `agent_loops.go`. Keep the `Agent` type, `NewAgent`, `Events`, `Cancel`,
  `ID`, `emit` in `agent.go`.

## Phase 3: Enforce file-length + docstring rules

- [ ] 3a: Replace `.github/workflows/source-file-length.yml` with a unified
  `.github/workflows/ci-checks.yml` that builds `tools/ci-check` and runs
  `ci-check file-length` (max 1000) and `ci-check docstring`. Delete the
  superseded workflow file.
- [ ] 3b: Run the new workflow locally, fix any stragglers surfaced by the
  docstring rule or by Phase 2 splits (e.g., new files missing doc comments).

## Phase 4: Refactor functions to ≤ 1 positional param

**Parallel Tasks: 4a, 4b, 4c, 4d**

- [ ] 4a: `cmd/herm/style.go` hotspots — `renderToolBox`, `renderToolGroup`,
  `lerpColor`, and sibling rendering helpers. Introduce per-function
  `*Options` structs; update call sites.
- [ ] 4b: `cmd/herm/render.go` + `input.go` hotspots — `cursorVisualPos`,
  wrapping/positioning helpers. Options-struct refactor with call-site
  updates.
- [ ] 4c: `cmd/herm/models.go`, `tools.go`, `filetools.go`, `commands.go`
  remaining violations. Options-struct refactor.
- [ ] 4d: `tools/write-file/`, `tools/edit-file/`, `tools/outline/` — update
  any multi-param diff/hunk helpers.

## Phase 5: Enforce positional-params rule

- [ ] 5a: Turn `ci-check positional-params` from a warning into a hard fail in
  `.github/workflows/ci-checks.yml` after confirming no remaining violations.
- [ ] 5b: Update `GUIDELINES.md` with a short section describing the three
  enforced rules and pointing at `tools/ci-check/`.

## Phase 6: Cleanup

- [ ] 6a: Audit `.github/workflows/` — confirm `test.yml`, `ci-checks.yml`,
  `prompt-length.yml`, and release workflows are the full set. Remove any
  dead references to the old `source-file-length.yml`.
- [ ] 6b: Smoke-test the full pipeline against `main` by opening a dry-run PR
  or running the workflow locally; confirm green across all rules.
