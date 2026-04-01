# Comprehensive Error Testing & Handling

**Goal:** Ensure every error branch in both langdag and herm is tested with mocked providers, find actual bugs, and fix them all. No test may be softened to pass — if a test reveals a bug, the code must be fixed.

**Context:** The user reported agent interruptions with no error display. Prior plans (fix-agent-silent-stops, stream-resilience, max-tokens-crash-fix) addressed the most critical issues. This plan covers the remaining gaps across all error branches.

**Constraint:** All LLM provider interactions must be mocked — no real API calls, no token costs.

**Principle:** Tests exist to find bugs, not to pass. If a test fails, the production code is wrong. Never weaken assertions, skip error checks, or ignore unexpected behavior to make a test green. A phase is not complete until every failing test has led to a code fix.

---

## Phase 1: Enhance Mock Provider for Error Simulation

The current `mock.Provider` can only simulate clean responses and context cancellation. Many error scenarios require richer simulation. This phase extends the mock without breaking existing tests.

**Files:** `external-deps-workspace/langdag/internal/provider/mock/mock.go`

**Current state:** 4 modes (random, echo, fixed, tool_use). No way to simulate: mid-stream errors, HTTP-level failures, partial responses with error, call-indexed failures (fail on call N, succeed on N+1). Agent tests work around this with custom `failThenSucceedProvider` and `streamFailThenSucceedProvider` wrappers — those are fine for herm-level tests but langdag's own tests need mock-level support.

- [ ] 1a: Add `"error"` mode to mock provider — `Complete()` and `Stream()` return a configurable error (new `Error error` field on `Config`). This lets langdag tests simulate provider-level failures without custom wrappers.
- [ ] 1b: Add `"stream_error"` mode — `Stream()` starts sending chunks normally, then emits a `StreamEventError` after a configurable number of chunks (new `ErrorAfterChunks int` field). Simulates mid-stream provider failure (network drop, server crash).
- [ ] 1c: Add `"partial_max_tokens"` mode — `Stream()` sends partial text chunks then emits done with `StopReason: "max_tokens"` and a configurable amount of content (empty, partial text, or text + tool_use blocks). Uses existing `FixedResponse` + new `StopReason` field on Config.
- [ ] 1d: Add call-counting support — new `FailUntilCall int` field: calls 1..N return `Config.Error`, call N+1 onwards use normal mode. Simulates transient failures followed by recovery. Existing `LastRequest` capture continues to work.
- [ ] 1e: Tests for all new modes — verify each mode produces the expected stream events, errors, and stop reasons. Verify `FailUntilCall` transitions correctly.

---

## Phase 2: Langdag — Provider Protocol Error Branches

Each provider (Anthropic, OpenAI, Gemini, Grok) has protocol conversion code that can encounter malformed data. These paths need tests that verify errors propagate (not panic or silently produce garbage).

**Parallel Phases: 2, 3**

**Files:** `internal/provider/{anthropic,openai,gemini}/protocol.go`, `internal/provider/openai/{grok.go,responses.go}`

- [ ] 2a: **Anthropic protocol** — Test `convertMessages()` with: malformed JSON content blocks, empty content arrays, unknown block types, tool_use blocks with invalid JSON in input field, messages with only empty-text blocks (should be filtered). Test `processStreamEvents()` with: stream that closes mid-content-block, delta with missing index, content_block_start with unknown type. Verify each produces a clear error or safe fallback — never a panic.
- [ ] 2b: **OpenAI protocol** — Test `convertMessages()` with: tool_call blocks with empty function name, tool_result referencing non-existent tool_use_id, image blocks with invalid base64. Test `parseSSEStream()` with: malformed JSON lines (should skip, not crash), incomplete SSE data field, `[DONE]` sentinel mid-stream, empty delta chunks. Verify graceful handling.
- [ ] 2c: **Gemini protocol** — Test `convertMessages()` with: empty parts array, function_call with nil args, function_response with non-JSON content. Test `parseSSEStream()` with: response containing no candidates, candidate with empty content, finish_reason but no content. Verify each edge case.
- [ ] 2d: **Grok/Responses API** — Test the OpenAI Responses API path (`responses.go`) with: malformed response objects, missing output array, output items with unknown types, function_call without arguments field. This is newer code with potentially less battle-testing.
- [ ] 2e: Fix any actual bugs found — Protocol code that panics on malformed input, produces empty content blocks, or silently drops errors must be fixed. Each fix gets its own test proving the fix.

---

## Phase 3: Langdag — Conversation & Storage Error Branches

**Files:** `internal/conversation/conversation.go`, `internal/storage/sqlite/sqlite.go`

- [ ] 3a: **Provider.Stream() returns error** — Test `streamResponse()` when the provider's `Stream()` method itself returns an error (not a stream event error). Verify the error is emitted as `StreamEventError` and the events channel is properly closed. Currently untested.
- [ ] 3b: **Database failure mid-stream** — Test what happens when `storage.CreateNode()` fails during `streamResponse()` after partial content has been streamed. Verify: error event emitted, no hang, partial content either preserved or clearly reported as lost.
- [ ] 3c: **Malformed node content in buildMessages** — Test `buildMessages()` with nodes containing: non-JSON string content (plain text), JSON array with unknown block types, null/empty content field, very large content (>1MB). Verify graceful fallback (raw string) or clear error — never panic.
- [ ] 3d: **Output group budget boundary** — Test continuation when cumulative output tokens land exactly at the budget limit. Test with budget of 0 (should not continue). Test when continuation provider call fails (should emit last saved node, not crash).
- [ ] 3e: **Orphaned tool_use edge cases** — Test `buildMessages()` with: multiple orphaned tool_use IDs in same message, tool_use with duplicate IDs, tool_result that references a tool_use from a different conversation branch. Verify synthetic results injected correctly.
- [ ] 3f: Fix any actual bugs found — Conversation code that hangs, panics, or silently corrupts state must be fixed.

---

## Phase 4: Langdag — Router, Retry & Filter Edge Cases

**Files:** `internal/provider/router.go`, `internal/provider/retry.go`, `internal/provider/filter.go`

- [ ] 4a: **Router concurrent access** — Test `Router.Stream()` called concurrently from multiple goroutines. Verify no race conditions (run with `-race`). Test fallback chain when primary provider fails with context cancellation vs. transient error — different behavior expected.
- [ ] 4b: **Retry edge cases** — Test retry when: context is canceled during backoff sleep (should exit immediately, not wait), error wrapping chain is deep (3+ levels of `fmt.Errorf`), `isTransient()` with edge-case error messages (e.g., "connection timeout" vs. "request timed out"). Test that `MaxRetries=1` means exactly 1 retry (2 total attempts).
- [ ] 4c: **Filter with unknown models** — Test `WithServerToolFilter()` when the model ID is not in the known catalog. Verify it defaults to safe behavior (strips server tools) rather than panicking or allowing all.
- [ ] 4d: Fix any actual bugs found.

---

## Phase 5: Herm — Sub-agent Error Handling & Display

This is the most critical phase for the user's reported issue. Sub-agent interruptions must always produce visible, actionable error messages.

**Files:** `cmd/herm/subagent.go`, `cmd/herm/agent.go`

- [ ] 5a: **Sub-agent output token overflow** — Test behavior when a sub-agent's response exceeds reasonable output size. Currently the sub-agent has `maxTurns` but no explicit output size limit. Verify: (1) if a sub-agent hits max_turns, the partial output collected so far is returned with a descriptive error like "sub-agent reached maximum turns (N) — partial output returned", (2) if a sub-agent's accumulated output text exceeds the 30KB truncation limit, the truncation message is clear and the error header explains why. The parent agent must receive enough context to take action (retry with smaller scope, report to user, etc.).
- [ ] 5b: **Sub-agent LLM error propagation** — Test: sub-agent's provider returns a permanent error (401 auth failure). Verify the error appears in the sub-agent result with context (e.g., "turn 1: prompt: anthropic: 401 Unauthorized"). Test: sub-agent's provider returns transient errors that exhaust retries. Verify the final "max retries exceeded" error appears in the result.
- [ ] 5c: **Sub-agent stream interruption** — Test: sub-agent's stream stalls (no chunks for >timeout). Verify: stream timeout fires, error collected, result includes "stream timed out" with turn/tool context. Parent agent can see this and decide whether to retry the task.
- [ ] 5d: **Background agent error surfacing** — Test: background agent encounters a fatal error. Verify: (1) `bgAgentStatus()` returns the error in the result, (2) when `InjectBackgroundResult()` fires on completion, the injected message includes the error, (3) the parent agent's LLM sees the error and can react.
- [ ] 5e: **Concurrent sub-agent race conditions** — Test: spawn 3+ sub-agents in parallel (via parallel tool execution). Verify: (1) no race conditions on shared state (`bgAgents` map, `agentNodes` map), (2) errors from one sub-agent don't corrupt results of another, (3) all results are correctly attributed to the right agent_id. Run with `-race`.
- [ ] 5f: Fix any actual bugs found — especially around error message clarity and completeness. If sub-agent errors are missing context (tool name, turn number, error type), add it. If errors are swallowed, surface them.

---

## Phase 6: Herm — Agent Loop Silent Failures & Edge Cases

Several places in the agent loop silently ignore errors. While some are intentional (non-critical metadata), the user's "interruption with no error display" may trace back to one of these.

**Files:** `cmd/herm/agent.go`

- [ ] 6a: **Audit silent error paths** — The following errors are currently silently ignored. For each, add a test that triggers the error path and verify the agent still functions correctly. If the silent error actually causes user-visible problems (stale usage, broken context, corrupted state), fix it:
  - `emitUsage()` — `client.GetNode()` error → returns 0 silently. Test: storage error during usage emit. Risk: low (usage is display-only).
  - `clearOldToolResults()` — `client.GetAncestors()` error → returns early. Test: storage error during context cleanup. Risk: medium (may cause context window overflow on next call if cleanup never succeeds).
  - `maybeCompact()` — `compactConversation()` error → returns original nodeID. Test: LLM failure during compaction. Risk: medium (conversation grows unbounded if compaction always fails).
  - `replaceToolResultContent()` — JSON unmarshal error → returns original content. Test: malformed tool result JSON. Risk: low (preserves original).
- [ ] 6b: **Tool execution error edge cases** — Test: tool.Execute() returns error with very long message (>30KB), tool.Execute() panics (not just returns error), unknown tool name in tool_use block. Verify each case produces a tool result with `IsError: true` and a clear message — never hangs or crashes the agent loop.
- [ ] 6c: **Approval flow interruption** — Test: agent requests approval, context is canceled before approval arrives. Verify: agent exits cleanly with an error event, no goroutine leaks, no deadlock on the approval channel.
- [ ] 6d: **Max tool iterations boundary** — Test: agent reaches exactly `maxToolIterations` (default 25). Verify: clear error message emitted ("reached maximum tool iterations"), agent stops gracefully, partial conversation state is valid for resume.
- [ ] 6e: Fix any actual bugs found — especially if silent errors cause cascading failures (e.g., compaction always fails → context grows → eventually hits provider token limit → cryptic API error).

---

## Phase 7: Herm — Container & Tool Error Propagation

**Files:** `cmd/herm/container.go`, `cmd/herm/tools.go`

- [ ] 7a: **Container exec failure during tool** — Test: `BashTool.Execute()` when the container returns a non-zero exit code, when the container is not running, when Docker daemon is unreachable. Verify each produces a tool result with `IsError: true` and a descriptive message (not just "exec failed").
- [ ] 7b: **Git tool error paths** — Test: `GitTool.Execute()` with credential failures (the `gitCredentialHint` path), with commands that produce stderr but succeed (exit 0), with commands that exceed output limits. Verify credential hints are included in error output, stderr is not discarded, truncation is applied correctly.
- [ ] 7c: **Container rebuild during active tool execution** — Test: what happens if a container rebuild is triggered while a tool is executing. Verify: active tool execution fails gracefully (not hangs), error message indicates the container was restarted.
- [ ] 7d: Fix any actual bugs found.

---

## Phase 8: Integration — End-to-End Error Chains

Verify that errors flow correctly through the full stack: provider error → langdag → herm agent → user-visible message. These tests use the enhanced mock from Phase 1.

- [ ] 8a: **Provider permanent error → user sees error** — Mock returns 401 on all calls. Verify: langdag wraps the error, herm's retry logic classifies it as non-retryable, agent emits `EventError` with the original API error message, and emits `EventDone`. The user should see "Unauthorized" or similar — not just "an error occurred".
- [ ] 8b: **Provider transient error → retry → success** — Mock fails twice with 503, succeeds on third call. Verify: `EventRetry` emitted for each retry (user sees "retrying..."), final response delivered normally, usage stats reflect only the successful call.
- [ ] 8c: **Mid-stream failure → stream retry → success** — Mock sends 3 chunks then errors, retry sends full response. Verify: `EventStreamClear` emitted (partial text discarded), full response displayed, no duplicate content.
- [ ] 8d: **Sub-agent error chain** — Main agent spawns sub-agent, sub-agent's provider returns error. Verify: error collected with full context (agent_id, turn number, tool name), parent agent receives structured error result, parent agent's LLM can see the error and respond appropriately.
- [ ] 8e: **Cascading failure** — Sub-agent hits max_tokens with no content, then parent agent retries with a different approach. Verify the full chain works without corruption: sub-agent error → parent sees error → parent takes corrective action → corrective action succeeds.
- [ ] 8f: Fix any actual bugs found in the end-to-end error chain.

---

## Success Criteria

- Every error `fmt.Errorf()` in langdag and herm is reachable by at least one test
- No `panic()` or `log.Fatal()` is triggered by any malformed input (only by truly unrecoverable startup failures like missing embedded templates)
- Sub-agent errors always include: agent_id, turn number, tool name (if applicable), and the original error message
- Background agent errors always reach the parent agent's result
- Silent error paths (`emitUsage`, `clearOldToolResults`, `maybeCompact`) are audited and either justified or fixed
- All tests pass with `-race` flag
- No test assertions are weakened to accommodate bugs — bugs are fixed in production code
- `go test ./...` passes for both `cmd/herm/` and `external-deps-workspace/langdag/`
