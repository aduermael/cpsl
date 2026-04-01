# Comprehensive Error Testing & Handling

**Goal:** Ensure every error branch in both langdag and herm is tested with mocked providers, find actual bugs, and fix them all. No test may be softened to pass — if a test reveals a bug, the code must be fixed.

**Context:** The user reported agent interruptions with no error display. Prior plans (fix-agent-silent-stops, stream-resilience, max-tokens-crash-fix) addressed the most critical issues. This plan covers the remaining gaps across all error branches.

**Constraint:** All LLM provider interactions must be mocked — no real API calls, no token costs.

**Principle:** Tests exist to find bugs, not to pass. If a test fails, the production code is wrong. Never weaken assertions, skip error checks, or ignore unexpected behavior to make a test green. A phase is not complete until every failing test has led to a code fix.

---

## Repo Layout & Commit Strategy

This plan spans **two separate git repositories**:

| Repo | Path (relative to project root) | Covers |
|------|------|--------|
| **herm** | `.` (project root) | `cmd/herm/`, plans, herm-level tests |
| **langdag** | `external-deps-workspace/langdag` | `internal/provider/`, `internal/conversation/`, `internal/api/`, SDKs, langdag-level tests |

`external-deps-workspace/` is **gitignored** in herm — it's a local clone of `langdag` enabled via `./use-local-dep.sh`, which creates a `go.work` workspace so herm builds against the local langdag instead of a release tag. Not all devs use this setup (some use the published module).

**How to commit:**
- **Langdag changes** → commit inside `external-deps-workspace/langdag` (its own git repo)
- **Herm changes** (including plan updates) → commit inside the herm root
- A task touching both repos needs **two separate commits** (one per repo)
- Always `cd` to the correct repo root before `git add`/`git commit`

**How to run tests:**
- Langdag: `cd external-deps-workspace/langdag && go test ./...`
- Herm: `cd /Users/frl/Desktop/herm && go test ./cmd/herm/...`
- Both repos' tests must pass at the end of each phase

---

## Phase 1: Enhance Mock Provider for Error Simulation

The current `mock.Provider` can only simulate clean responses and context cancellation. Many error scenarios require richer simulation. This phase extends the mock without breaking existing tests.

**Files:** `external-deps-workspace/langdag/internal/provider/mock/mock.go`

**Current state:** 4 modes (random, echo, fixed, tool_use). No way to simulate: mid-stream errors, HTTP-level failures, partial responses with error, call-indexed failures (fail on call N, succeed on N+1). Agent tests work around this with custom `failThenSucceedProvider` and `streamFailThenSucceedProvider` wrappers — those are fine for herm-level tests but langdag's own tests need mock-level support.

- [x] 1a: Add `"error"` mode to mock provider — `Complete()` and `Stream()` return a configurable error (new `Error error` field on `Config`). This lets langdag tests simulate provider-level failures without custom wrappers.
- [x] 1b: Add `"stream_error"` mode — `Stream()` starts sending chunks normally, then emits a `StreamEventError` after a configurable number of chunks (new `ErrorAfterChunks int` field). Simulates mid-stream provider failure (network drop, server crash).
- [x] 1c: Add `"partial_max_tokens"` mode — `Stream()` sends partial text chunks then emits done with `StopReason: "max_tokens"` and a configurable amount of content (empty, partial text, or text + tool_use blocks). Uses existing `FixedResponse` + new `StopReason` field on Config.
- [x] 1d: Add call-counting support — new `FailUntilCall int` field: calls 1..N return `Config.Error`, call N+1 onwards use normal mode. Simulates transient failures followed by recovery. Existing `LastRequest` capture continues to work.
- [x] 1e: Tests for all new modes — verify each mode produces the expected stream events, errors, and stop reasons. Verify `FailUntilCall` transitions correctly.

---

## Phase 2: Langdag — Provider Protocol Error Branches

Each provider (Anthropic, OpenAI, Gemini, Grok) has protocol conversion code that can encounter malformed data. These paths need tests that verify errors propagate (not panic or silently produce garbage).

**Files:** `internal/provider/{anthropic,openai,gemini}/protocol.go`, `internal/provider/openai/{grok.go,responses.go}`

- [x] 2a: **Anthropic protocol** — Test `convertMessages()` with: malformed JSON content blocks, empty content arrays, unknown block types, tool_use blocks with invalid JSON in input field, messages with only empty-text blocks (should be filtered). Test `processStreamEvents()` with: stream that closes mid-content-block, delta with missing index, content_block_start with unknown type. Verify each produces a clear error or safe fallback — never a panic.
- [x] 2b: **OpenAI protocol** — Test `convertMessages()` with: tool_call blocks with empty function name, tool_result referencing non-existent tool_use_id, image blocks with invalid base64. Test `parseSSEStream()` with: malformed JSON lines (should skip, not crash), incomplete SSE data field, `[DONE]` sentinel mid-stream, empty delta chunks. Verify graceful handling.
- [x] 2c: **Gemini protocol** — Test `convertMessages()` with: empty parts array, function_call with nil args, function_response with non-JSON content. Test `parseSSEStream()` with: response containing no candidates, candidate with empty content, finish_reason but no content. Verify each edge case.
- [x] 2d: **Grok/Responses API** — Test the OpenAI Responses API path (`responses.go`) with: malformed response objects, missing output array, output items with unknown types, function_call without arguments field. This is newer code with potentially less battle-testing.
- [x] 2e: Fix any actual bugs found (none found) — Protocol code that panics on malformed input, produces empty content blocks, or silently drops errors must be fixed. Each fix gets its own test proving the fix.

---

## Phase 3: Langdag — Conversation & Storage Error Branches

**Files:** `internal/conversation/conversation.go`, `internal/storage/sqlite/sqlite.go`

- [x] 3a: **Provider.Stream() returns error** — Test `streamResponse()` when the provider's `Stream()` method itself returns an error (not a stream event error). Verify the error is emitted as `StreamEventError` and the events channel is properly closed. Currently untested.
- [x] 3b: **Database failure mid-stream** — Test what happens when `storage.CreateNode()` fails during `streamResponse()` after partial content has been streamed. Verify: error event emitted, no hang, partial content either preserved or clearly reported as lost.
- [x] 3c: **Malformed node content in buildMessages** — Test `buildMessages()` with nodes containing: non-JSON string content (plain text), JSON array with unknown block types, null/empty content field, very large content (>1MB). Verify graceful fallback (raw string) or clear error — never panic.
- [x] 3d: **Output group budget boundary** — Test continuation when cumulative output tokens land exactly at the budget limit. Test with budget of 0 (should not continue). Test when continuation provider call fails (should emit last saved node, not crash).
- [x] 3e: **Orphaned tool_use edge cases** — Test `buildMessages()` with: multiple orphaned tool_use IDs in same message, tool_use with duplicate IDs, tool_result that references a tool_use from a different conversation branch. Verify synthetic results injected correctly.
- [x] 3f: Fix any actual bugs found — `contentToRawMessage` used `fmt.Sprintf("%q")` producing invalid JSON for strings with null bytes or non-printable chars (`\x00` instead of `\u0000`). Fixed by switching to `json.Marshal`. No other bugs found.

---

## Phase 4: Langdag — Router, Retry & Filter Edge Cases

**Files:** `internal/provider/router.go`, `internal/provider/retry.go`, `internal/provider/filter.go`

- [x] 4a: **Router concurrent access** — Test `Router.Stream()` called concurrently from multiple goroutines. Verify no race conditions (run with `-race`). Test fallback chain when primary provider fails with context cancellation vs. transient error — different behavior expected.
- [x] 4b: **Retry edge cases** — Test retry when: context is canceled during backoff sleep (should exit immediately, not wait), error wrapping chain is deep (3+ levels of `fmt.Errorf`), `isTransient()` with edge-case error messages (e.g., "connection timeout" vs. "request timed out"). Test that `MaxRetries=1` means exactly 1 retry (2 total attempts).
- [x] 4c: **Filter with unknown models** — Test `WithServerToolFilter()` when the model ID is not in the known catalog. Verify it defaults to safe behavior (strips server tools) rather than panicking or allowing all. Already comprehensively covered by existing tests (TestFilterProvider_UnknownModel, EmptyModelID, AllServerToolsUnknownModel, MultipleModelsUnknownStripsAll, StreamUnknownModel).
- [x] 4d: Fix any actual bugs found — no bugs found. Filter code is safe by design: nil map lookup returns false, correctly stripping all server tools for unknown models.

---

## Phase 5: Herm — Sub-agent Error Handling & Display

This is the most critical phase for the user's reported issue. Sub-agent interruptions must always produce visible, actionable error messages.

**Files:** `cmd/herm/subagent.go`, `cmd/herm/agent.go`

- [x] 5a: **Sub-agent output token overflow** — Added descriptive "sub-agent reached maximum turns (N) — partial output returned" error in both Execute and runBackground drain loops (using `maxTurnsExceeded` flag to prevent duplicates). Tests verify: max_turns hit produces the error + partial output preserved, multi-turn partial output retained, and large output truncation points to output file.
- [x] 5b: **Sub-agent LLM error propagation** — Tests verify: (1) permanent 401 error appears in result with turn context and "encountered errors" body, (2) transient 429 errors that exhaust retries surface the final error in [errors:] section, (3) tool execution errors are handled gracefully (tool_result with IsError, agent recovers) without appearing as agent-level errors. No bugs found.
- [x] 5c: **Sub-agent stream interruption** — Added `streamTimeout` field to SubAgentTool, passed to inner agent via `WithStreamChunkTimeout`. Tests verify: (1) stream stall → "stream stalled" error in result with partial text preserved, (2) mid-stream error event → "stream interrupted" error in result. No bugs found.
- [x] 5d: **Background agent error surfacing** — Tests verify: (1) fatal 401 error appears in bgAgentStatus completed result, (2) onBgComplete callback includes the error with agent_id and turn context, (3) InjectBackgroundResult correctly forwards background agent output to parent agent's pending queue. No bugs found.
- [x] 5e: **Concurrent sub-agent race conditions** — Tests run with `-race`: (1) 5 concurrent foreground agents — unique agent_ids, no races on agentNodes map, (2) 5 concurrent background agents — unique ids, all complete, no races on bgAgents map, (3) 6 mixed (3 fg + 3 bg) — no shared state corruption. All pass clean.
- [x] 5f: Fix any actual bugs found — One bug fixed in 5a: when sub-agent exceeded maxTurns, Cancel() was called but "context canceled" was filtered out, producing no user-visible reason for stopping. Fixed by adding explicit "sub-agent reached maximum turns (N) — partial output returned" error before Cancel in both Execute and runBackground drain loops. Added `streamTimeout` field (5c) to enable inner agent chunk timeout configuration. No other bugs found — error propagation for LLM errors (5b), stream interruptions (5c), background agent errors (5d), and concurrent access (5e) all work correctly.

---

## Phase 6: Herm — Agent Loop Silent Failures & Edge Cases

Several places in the agent loop silently ignore errors. While some are intentional (non-critical metadata), the user's "interruption with no error display" may trace back to one of these.

**Files:** `cmd/herm/agent.go`

- [x] 6a: **Audit silent error paths** — All four silent error paths tested. `emitUsage()`: returns 0 on storage error, empty nodeID, or nil node — confirmed display-only, no impact. `clearOldToolResults()`: returns silently on GetAncestors error — agent continues normally (integration test confirms). `maybeCompact()`: returns original nodeID on compaction failure (LLM or storage error) — agent continues with uncompacted conversation. `replaceToolResultContent()`: returns original content for malformed JSON (empty, plain text, partial JSON, non-array). No bugs found — all silent paths are justified (display-only or safe fallback).
- [x] 6b: **Tool execution error edge cases** — Tests verify: (1) 35KB error message preserved in full as IsError tool result, agent continues to next LLM call, (2) tool panic caught by top-level recover, emits EventError with "agent panic" + original panic value + stack trace, then EventDone, (3) unknown tool name produces "unknown tool: X" with IsError=true, LLM receives the error and continues. No bugs found.
- [x] 6c: **Approval flow interruption** — Tests verify: (1) context canceled during approval wait → EventError with "context canceled" + EventDone emitted, agent exits cleanly, no deadlock, (2) approval denied → IsError tool result with "denied" message, agent continues with next LLM call, LLM can see and respond to the denial. No bugs found.
- [x] 6d: **Max tool iterations boundary** — **Bug found and fixed**: agent silently stopped when hitting maxToolIterations with no error message. Fixed by adding explicit "reached maximum tool iterations (N) — stopping to prevent runaway loop" error event before breaking. Tests verify: (1) exactly N tool executions occur, (2) EventError with descriptive message emitted, (3) EventDone carries valid nodeID for conversation resume.
- [x] 6e: Fix any actual bugs found — One bug fixed in 6d: agent silently stopped at maxToolIterations with no user-visible error. Fixed by emitting "reached maximum tool iterations (N)" error event. Silent error paths (emitUsage, clearOldToolResults, maybeCompact, replaceToolResultContent) audited and confirmed safe: all are display-only or have graceful fallbacks. No cascading failure bugs found.

---

## Phase 7: Herm — Container & Tool Error Propagation

**Files:** `cmd/herm/container.go`, `cmd/herm/tools.go`

- [x] 7a: **Container exec failure during tool** — Tests verify: (1) non-zero exit code returns result string with "exit code: N" (existing test), (2) container not running returns ContainerError with ErrNotRunning (existing test), (3) docker daemon unreachable (docker exec binary fails to start) returns ContainerError with ErrExecFailed and descriptive "docker exec: ..." message — tested at both container level (Exec, ExecWithStdin) and BashTool level. No bugs found.
- [x] 7b: **Git tool error paths** — Tests verify: (1) credential hint integration — git push to non-existent remote produces "Could not read from remote repository" which triggers credential hint in Execute result, (2) stderr preserved on success — git checkout -b writes to stderr with exit 0, CombinedOutput captures it, (3) canceled context (non-ExitError) — error wrapped as "git exec: ..." not raw context error. GitTool intentionally does not truncate output (unlike BashTool) — git output is typically bounded and runs on host. No bugs found.
- [x] 7c: **Container rebuild during active tool execution** — Test runs 3 concurrent execs + 1 rebuild with `-race`. Verified: no deadlock (completes within timeout), no data races, ErrNotRunning during the rebuild window is expected and handled gracefully (Rebuild sets running=false before Start, any exec in that window gets ErrNotRunning). After rebuild completes, container is running and operational. The error message is "container not running" (not "container being rebuilt") — this is accurate since the container is genuinely not running during the window. No bugs found.
- [x] 7d: Fix any actual bugs found — no bugs found. Container error paths (ErrExecFailed for daemon failure, ErrNotRunning for stopped container) produce descriptive messages. Git tool correctly propagates credential hints, preserves stderr via CombinedOutput, and wraps non-ExitErrors with "git exec:". Concurrent rebuild + exec is safe due to mutex protection. GitTool intentionally does not truncate output (unlike BashTool) — git runs on host and output is typically bounded.

---

## Phase 8: Langdag API Server — Streaming & Error Responses

The API server exposes langdag over HTTP/SSE. Streaming error paths and edge cases are undertested.

**Files:** `external-deps-workspace/langdag/internal/api/server.go`, `api_test.go`

- [x] 8a: **Streaming error mid-response** — Test: start a `POST /prompt?stream=true` request, mock provider emits a few deltas then a `StreamEventError`. Verify: SSE stream sends `event: error` with the error message, then the connection closes cleanly. Client should receive all prior deltas plus the error event.
- [x] 8b: **Provider failure during streaming** — Test: mock provider's `Stream()` returns an error (not a stream). Verify: API returns a well-formed SSE error event (not an HTTP 500 with JSON body — the stream has already started with `Content-Type: text/event-stream`).
- [x] 8c: **Non-streaming error responses** — Test: `POST /prompt` (non-streaming) with a provider that returns errors. Verify: HTTP 500 with `{"error": "..."}` body containing the original error context (not just "internal server error").
- [x] 8d: **Invalid request validation** — Test: `POST /prompt` with empty body, missing `message` field, invalid JSON, extremely long message (>1MB). Verify: 400 status with descriptive error for each case. Empty body and invalid JSON already tested (TestPromptInvalidJSON, TestPromptEmptyMessage). Added: nil body, missing message field, 1MB+ message (succeeds — no server limit), streaming empty body.
- [x] 8e: **Auth edge cases** — Tests: empty X-API-Key header, malformed Bearer tokens (trailing space, no space, Basic scheme, wrong key), health endpoint bypasses auth with wrong credentials, no-config means all endpoints open. All return 401 with "unauthorized" message. No bugs found.
- [x] 8f: Fix any actual bugs found in error responses. **One bug fixed**: SSE error events wrote raw error messages without handling newlines — `fmt.Fprintf(w, "event: error\ndata: %s\n\n", err)` broke SSE format when error contained `\n`, causing parsers to see only the first line. Fixed with `writeSSEError` helper that writes each line with its own `data:` prefix per SSE spec. Also added nil-check for `StreamEventError.Error` to prevent panic on malformed events. All three SDKs (Go, Python, TypeScript) already handle multi-line `data:` fields correctly.

---

## Phase 9: Go SDK — Error Handling & SSE Edge Cases

**Files:** `external-deps-workspace/langdag/sdks/go/client.go`, `sse.go`, `errors.go`

- [x] 9a: **SSE stream without done event** — Added `Content()` and `Err()` methods to `Stream`, with content accumulation during `read()`. Tests verify: no-done-event returns `StreamError` from `Node()`, `Content()` returns accumulated "Hello world!", abrupt connection close doesn't hang (2s timeout guard), partial delta content preserved. No bugs found — existing `Node()` already returned correct error, but content was inaccessible without the new `Content()` method.
- [ ] 9b: **SSE malformed JSON handling consistency** — Currently Go SDK silently ignores JSON parse errors in delta/done events (returns event with empty Content/NodeID). Test and verify this is intentional: a stream with one malformed delta among valid ones should still produce the valid content. If this masks real errors, add a way to surface parse failures (e.g., via `Stream.Err()`).
- [ ] 9c: **HTTP 5xx during streaming** — Test: server returns HTTP 200 with `Content-Type: text/event-stream` headers, then sends an error event. Versus: server returns HTTP 500 before streaming starts. Verify: both cases produce appropriate error types (`StreamError` vs `APIError`).
- [ ] 9d: **Connection drop mid-stream** — Test using a mock HTTP server that closes the connection after sending partial SSE data (mid-event, mid-line). Verify: `Stream` detects the interruption and returns an error via `Stream.Err()` or the events channel, does not hang.
- [ ] 9e: **Concurrent streaming** — Test: open 2+ concurrent streaming requests. Verify: no shared state corruption, each stream receives its own events independently.
- [ ] 9f: Fix any actual bugs found — especially around silent failures in SSE parsing.

---

## Phase 10: Python SDK — Error Handling & SSE Edge Cases

**Files:** `external-deps-workspace/langdag/sdks/python/langdag/client.py`, `async_client.py`, `exceptions.py`

- [ ] 10a: **SSE stream without done event** — Test both sync and async: server sends start + deltas, then closes. Verify: `StreamResult.node_id` is None or raises `StreamError`, accumulated content is still accessible. Test that consuming the stream iterator completes (does not hang).
- [ ] 10b: **Provider error mid-stream** — Test: server sends start, 2 deltas, then `event: error\ndata: provider crashed`. Verify: `StreamError` is raised with the error message when iterating, prior deltas are not lost if caller caught partial content.
- [ ] 10c: **Connection timeout during stream** — Test using httpx mock: configure a timeout that fires after first delta. Verify: raises `ConnectionError` (or `StreamError`), not an unhandled httpx exception. Test both sync and async clients.
- [ ] 10d: **Invalid SSE event sequence** — Test: server sends delta before start, done without any deltas, multiple done events. Verify: SDK handles gracefully (no crash, reasonable behavior).
- [ ] 10e: **Large streamed response** — Test: server sends 10,000 delta events. Verify: memory usage is bounded (no unbounded buffer accumulation if caller iterates lazily), all content collected correctly.
- [ ] 10f: Fix any actual bugs found in Python SDK error handling.

---

## Phase 11: TypeScript SDK — Error Handling & SSE Edge Cases

**Files:** `external-deps-workspace/langdag/sdks/typescript/src/client.ts`, `sse.ts`, `errors.ts`

- [ ] 11a: **SSE stream without done event** — Test: readable stream sends start + deltas then closes. Verify: `stream.node()` throws `SSEParseError` (no done event received), `stream.content` still has accumulated text from deltas.
- [ ] 11b: **Provider error mid-stream** — Test: stream sends start, deltas, then error event. Verify: error event is yielded by `stream.events()` iterator, `stream.node()` rejects with the error. Prior delta content is accessible.
- [ ] 11c: **Chunked SSE delivery edge cases** — Test: SSE events split across ReadableStream chunks at awkward boundaries (mid-UTF8 character, mid-`\n\n` separator, mid-`data:` prefix). Verify: parser reassembles correctly, no data loss or corruption.
- [ ] 11d: **Fetch failure during streaming** — Test: ReadableStream reader throws an error mid-read (simulating network drop). Verify: `stream.events()` throws `NetworkError`, `stream.node()` rejects, no unhandled promise rejections.
- [ ] 11e: **Response.body null** — Test: fetch returns response with null body for streaming request. Verify: throws `NetworkError` with descriptive message. (May already be tested — verify and extend if needed.)
- [ ] 11f: Fix any actual bugs found in TypeScript SDK error handling.

---

## Phase 12: Cross-SDK Consistency & E2E Streaming Errors

Verify all 3 SDKs behave consistently when the server returns errors during streaming. These tests run against a test HTTP server (not a full langdag server — just SSE responses).

- [ ] 12a: **Error event format contract** — Define and document the exact SSE error format the server sends. Verify all 3 SDKs parse it identically: Go → `StreamError`, Python → `StreamError`, TypeScript → error `SSEEvent`. Write a shared test fixture (SSE response bytes) and test each SDK against it.
- [ ] 12b: **Graceful degradation contract** — Define what each SDK should do when the stream ends without a done event. Verify behavior is consistent: all SDKs should make accumulated content available even when the stream terminates abnormally. If SDKs diverge, align them.
- [ ] 12c: **E2E streaming error** — Using the mock LLM server (`tools/mockllm/` or `docker-compose.test.yml` with mock provider), trigger a streaming response that produces an error event. Run each SDK's E2E test suite against it. Verify all 3 SDKs surface the error.
- [ ] 12d: Fix any cross-SDK inconsistencies — especially around malformed delta JSON (Go silently ignores, TypeScript throws `SSEParseError`, Python falls back to message). Align to a single documented behavior or justify the divergence.

---

## Phase 13: Integration — End-to-End Error Chains (Herm)

Verify that errors flow correctly through the full stack: provider error → langdag → herm agent → user-visible message. These tests use the enhanced mock from Phase 1.

- [ ] 13a: **Provider permanent error → user sees error** — Mock returns 401 on all calls. Verify: langdag wraps the error, herm's retry logic classifies it as non-retryable, agent emits `EventError` with the original API error message, and emits `EventDone`. The user should see "Unauthorized" or similar — not just "an error occurred".
- [ ] 13b: **Provider transient error → retry → success** — Mock fails twice with 503, succeeds on third call. Verify: `EventRetry` emitted for each retry (user sees "retrying..."), final response delivered normally, usage stats reflect only the successful call.
- [ ] 13c: **Mid-stream failure → stream retry → success** — Mock sends 3 chunks then errors, retry sends full response. Verify: `EventStreamClear` emitted (partial text discarded), full response displayed, no duplicate content.
- [ ] 13d: **Sub-agent error chain** — Main agent spawns sub-agent, sub-agent's provider returns error. Verify: error collected with full context (agent_id, turn number, tool name), parent agent receives structured error result, parent agent's LLM can see the error and respond appropriately.
- [ ] 13e: **Cascading failure** — Sub-agent hits max_tokens with no content, then parent agent retries with a different approach. Verify the full chain works without corruption: sub-agent error → parent sees error → parent takes corrective action → corrective action succeeds.
- [ ] 13f: Fix any actual bugs found in the end-to-end error chain.

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
- All 3 client SDKs (Go, Python, TypeScript) handle streaming errors consistently: connection drops, mid-stream errors, missing done events, malformed SSE data
- SDK error types map correctly to HTTP status codes: 401→AuthenticationError, 404→NotFoundError, 400→BadRequestError
- SSE parsing behavior is documented and aligned across SDKs (or divergence is explicitly justified)
- Python and TypeScript SDK tests run via their respective test runners (`pytest`, `vitest`); Go SDK tests via `go test`
