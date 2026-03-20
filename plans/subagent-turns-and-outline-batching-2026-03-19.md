# Sub-agent Specialization, Turn Counting Fix & Outline Batching

**Problems:**
1. Sub-agents always run on the cheap exploration model, even for implementation
   tasks — the prompt tells the main agent to delegate implementation, but the
   sub-agent executes it with Haiku-class quality.
2. Sub-agents hit their turn limit too often because turns are counted per tool call
   (`EventToolCallStart`) instead of per LLM response cycle.
3. The `outline` tool only accepts one file per call, forcing many tool calls for
   multi-file exploration.

**Four fixes:**
1. Introduce explicit sub-agent modes: `explore` (cheap model) and `implement` (main model)
2. Count sub-agent turns per LLM response cycle, not per tool call
3. Bump default turn limit from 15 to 20
4. Add multi-file support to `outline` tool

---

## Phase 1: Specialized sub-agent modes

**Key files:** `cmd/herm/subagent.go`, `cmd/herm/subagent_test.go`, `cmd/herm/main.go`,
`cmd/herm/prompts/role.md`, `cmd/herm/prompts/tools.md`

**Current state:** `SubAgentTool` takes a single `model` field used for all sub-agent
LLM calls, and an `explorationModel` used only for summarization. In `main.go:4859`,
both are set to `explorationModelID` — so sub-agents always run on the cheap model.
The tool input schema is `{"task": string, "agent_id": string}` with no way to
specify what kind of work the sub-agent should do.

**Approach:** Replace the single `model` field with two: `mainModel` (the full
orchestrator model) and `explorationModel` (cheap model). Add a required `mode` field
to the tool input: `"explore"` or `"implement"`. In `Execute()`, select the model
based on mode — `explore` uses `explorationModel`, `implement` uses `mainModel`.
Summarization always uses `explorationModel` regardless of mode.

In `main.go`, pass `modelID` as `mainModel` and `explorationModelID` as
`explorationModel` to the constructor.

Update the tool definition description and the role/tools prompts to make the
distinction crystal clear to the LLM.

- [x] 1a: Refactor `SubAgentTool` struct — rename `model` to `mainModel`, keep `explorationModel`; update `NewSubAgentTool()` constructor signature and `main.go` call site to pass `modelID` as `mainModel` and `explorationModelID` as `explorationModel`
- [x] 1b: Add `mode` field to `subAgentInput` struct and tool `InputSchema` (enum: `"explore"`, `"implement"`; required); in `Execute()`, select model based on mode; validate that mode is one of the two values
- [x] 1c: Update tool `Definition()` description to clearly explain the two modes and when to use each; update `prompts/tools.md` agent section to document modes
- [x] 1d: Update `prompts/role.md` "When to Delegate" section — replace the current generic delegation guidance with mode-specific guidance: `explore` for research/search/reading, `implement` for writing code/making changes; make it unambiguous
- [x] 1e: Update `buildSubAgentTools()` to pass both models through to nested sub-agents
- [x] 1f: Update tests — fix constructor calls throughout `subagent_test.go` for new signature; add tests for mode validation (invalid mode returns error), model selection (explore uses explorationModel, implement uses mainModel)

**Success criteria:**
- `{"task": "...", "mode": "explore"}` runs on the cheap model
- `{"task": "...", "mode": "implement"}` runs on the main model
- Summarization uses the cheap model in both cases
- Invalid or missing mode returns a clear error
- The LLM prompt makes it unambiguous which mode to use when
- Existing tests updated and passing

---

## Phase 2: Fix sub-agent turn counting

**Key files:** `cmd/herm/subagent.go`, `cmd/herm/subagent_test.go`

**Event flow context:** The agent emits events in this order per LLM response:
- `EventTextDelta` (streaming text, zero or more)
- `EventToolCallStart` (one per tool call in the batch)
- `EventToolCallDone` (one per tool, after execution)
- `EventUsage` (once per LLM response — the response boundary marker)
- Loop repeats for next LLM response, or `EventDone`

**Approach:** Use a `responseCounted` bool. On `EventToolCallStart`, if not already
counted for this response, increment `turns` and set `responseCounted = true`. On
`EventUsage` (which fires once per LLM response), reset `responseCounted = false`.
This way, 5 tool calls in one response = 1 turn. The limit check stays on
`EventToolCallStart` for fast cancellation.

Also bump `defaultSubAgentMaxTurns` from 15 to 20 and update comment from
"tool-call cap" to "response-cycle cap".

- [x] 2a: Change turn counting in `Execute()` event loop — add `responseCounted` bool, increment on first `EventToolCallStart` per response, reset on `EventUsage`
- [x] 2b: Update `defaultSubAgentMaxTurns` constant from 15 to 20 and update comment
- [ ] 2c: Update `prompts/tools.md` — change turn-limit wording to reflect response-based counting
- [ ] 2d: Update tests — fix hard-coded turn expectations; add a new test that verifies multiple tool calls in one response count as 1 turn (will need `scriptedProvider` or equivalent to emit multiple `EventToolCallStart` events)

**Success criteria:**
- A sub-agent LLM response with N tool calls counts as 1 turn, not N
- Default limit is 20
- Existing tests pass with updated expectations
- New test confirms batched tool calls = 1 turn

---

## Phase 3: Multi-file outline tool

**Key files:** `cmd/herm/filetools.go`, `cmd/herm/filetools_test.go`, `cmd/herm/prompts/tools.md`

**Current state:** `OutlineTool` accepts `{"file_path": "..."}` — one file per call.
The input struct is `outlineInput{FilePath string}`.

**Approach:** Add `file_paths` (array of strings) to the input schema alongside
`file_path`. When `file_paths` is provided, iterate over each path, call the outline
binary for each, and return combined output with file headers. Keep `file_path`
backward-compatible — if only `file_path` is given, behave exactly as today.

Cap `file_paths` at a reasonable limit (e.g., 20 files) to prevent abuse.

- [ ] 3a: Extend `outlineInput` struct to add `FilePaths []string` field; update `InputSchema` in `Definition()` to include `file_paths` array property; update `Description` to mention multi-file support
- [ ] 3b: Refactor `Execute()` — extract single-file logic into a helper, add multi-file loop that calls the helper for each path and combines results with `=== <path> ===` headers between files
- [ ] 3c: Update `prompts/tools.md` outline description to mention multi-file support
- [ ] 3d: Add tests — `TestOutlineTool_Execute_MultipleFiles` (happy path), `TestOutlineTool_Execute_MultipleFiles_PartialError` (one file fails, others succeed), `TestOutlineTool_Execute_MultipleFiles_TooMany` (over cap returns error), `TestOutlineTool_Execute_BothInputs` (file_path + file_paths merges correctly)

**Success criteria:**
- `{"file_paths": ["a.go", "b.go"]}` returns combined outline with file headers
- `{"file_path": "a.go"}` still works unchanged
- Over-cap input returns a clear error
- Partial failures include error messages inline without aborting other files
