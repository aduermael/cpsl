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
- [x] 9b: **SSE malformed JSON handling consistency** — Tested malformed delta among valid ones: valid content accumulates correctly ("Hello world!"), malformed delta emitted with empty Content (not skipped), no stream-level error. Verified behavior is intentional — per-event parse failures don't escalate to stream errors. Added `Err()` method (in 9a) for stream-level errors (SSE error events, I/O errors). No bugs found.
- [x] 9c: **HTTP 5xx during streaming** — Tested with httptest servers: HTTP 200 + SSE error event → `*StreamError` with "provider crashed" and partial content preserved via `Content()`. HTTP 500 before streaming → `*APIError` with status 500 and server message. Error type differentiation works correctly. No bugs found.
- [x] 9d: **Connection drop mid-stream** — Tested with httptest server using Hijack() to abruptly close connections: (1) drop between events — start+delta received, content "before drop" preserved, Node() returns error, no hang (5s guard), (2) drop mid-event (mid-line) — incomplete delta line, stream detects and exits cleanly, Node() returns error. No bugs found.
- [x] 9e: **Concurrent streaming** — 5 concurrent `PromptStream` requests against httptest server, each with unique message/content. Verified with `-race`: each stream receives its own events independently, no content mixing, no duplicate content, unique nodeIDs. No bugs found.
- [x] 9f: Fix any actual bugs found — no bugs found. SSE parsing handles all edge cases safely: malformed JSON silently ignored per-event, connection drops detected via scanner error, error types correctly differentiated. Added `Content()` and `Err()` methods to `Stream` (in 9a) as an API improvement — prior to this, accumulated content from delta events was inaccessible after a failed stream.

---

## Phase 10: Python SDK — Error Handling & SSE Edge Cases

**Files:** `external-deps-workspace/langdag/sdks/python/langdag/client.py`, `async_client.py`, `exceptions.py`

- [x] 10a: **SSE stream without done event** — Tested both sync and async: server sends start + deltas then closes. Both clients complete iteration without hanging. Content from deltas is fully accessible via manual accumulation. No node_id available (all events return None). Parser-level tests verify the same. No bugs found.
- [x] 10b: **Provider error mid-stream** — Tested sync and async: server sends start + 2 deltas + error event. Error event yielded as `SSEEvent` with `SSEEventType.ERROR` (not raised as exception — consistent with SDK's event-driven design). Prior delta content preserved and accessible. Plain text error data wrapped as `{"message": ...}`. No bugs found.
- [x] 10c: **Connection timeout during stream** — Tested sync and async: connect timeout to unreachable host raises `ConnectionError`. Read timeout (via httpx mock) raises `httpx.ReadTimeout`. Neither hangs or produces unhandled exceptions. No bugs found.
- [x] 10d: **Invalid SSE event sequence** — Tested sync, async, and parser-level: delta before start (yielded normally — parser doesn't enforce ordering), done without deltas (valid sequence, yielded), multiple done events (all yielded), empty data lines (event skipped), data without event type (skipped). All handled gracefully with no crashes. No bugs found.
- [x] 10e: **Large streamed response** — Tested sync, async, and parser-level: 10,000 delta events all yielded correctly with content intact. Parser uses lazy generator (no unbounded buffering). First and last chunks verified. Event count matches expected (10,002: start + 10,000 deltas + done). No bugs found.
- [x] 10f: Fix any actual bugs found in Python SDK error handling — no bugs found. SSE parser handles all edge cases correctly: no-done streams complete without hanging, error events yielded as data (not exceptions), invalid sequences handled gracefully, malformed JSON falls back to `{"message": ...}`, large streams use lazy generators. Both sync and async clients behave identically.

---

## Phase 11: TypeScript SDK — Error Handling & SSE Edge Cases

**Files:** `external-deps-workspace/langdag/sdks/typescript/src/client.ts`, `sse.ts`, `errors.ts`

- [x] 11a: **SSE stream without done event** — Added `content` getter to Stream class (previously `collectedContent` was private and inaccessible after failed streams — same gap as Go SDK Phase 9). Tests verify: start + deltas + close without done → `stream.node()` throws `SSEParseError`, `stream.content` has accumulated "Hello world!". Auto-consume path: `node()` throws but `stream.content` still has "partial". No bugs found — API improvement only.
- [x] 11b: **Provider error mid-stream** — Tests verify: (1) error after deltas — error event yielded, `stream.content` has "partial response", `node()` throws SSEParseError, (2) auto-consume path — `node()` throws, content "before error" still accessible, (3) error after done — all 4 events yielded, `node()` still works (done event captured nodeId). No bugs found — error events are data-only (not thrown), consistent with SDK design.
- [x] 11c: **Chunked SSE delivery edge cases** — Tests verify parser reassembles correctly across 6 awkward split scenarios: (1) mid-`\n\n` separator, (2) mid-`data:` prefix, (3) mid-`event:` prefix, (4) mid-UTF8 multibyte character (é split across chunks), (5) single-byte-at-a-time delivery, (6) mid-JSON in data field. All pass — `TextDecoder` with `stream:true` handles partial UTF-8, and buffer accumulation in `parseSSEStream` handles all other splits. No bugs found.
- [x] 11d: **Fetch failure during streaming** — Tests verify at both parser and Stream level: (1) reader error mid-stream — events received before error, error propagates through async generator, no hang, (2) reader error on first read — no events, error propagates, (3) Stream-level — `events()` throws, `stream.content` preserves "before drop", (4) `node()` auto-consume — rejects with reader error, content preserved. Errors propagate as raw `Error` (not `NetworkError`) — consistent with Go SDK where scanner errors are not wrapped. Parser is a low-level utility that shouldn't depend on SDK error types. No bugs found.
- [x] 11e: **Response.body null** — Existing test verified and extended with 3 cases: (1) null body → `NetworkError` with "Response body is null" message (existing test, verified message), (2) null body on `promptStreamFrom` path (streaming continuation via `node.promptStream()`) → same `NetworkError`, (3) undefined body treated as null → `NetworkError`. All paths through `requestStream()` correctly guarded. No bugs found.
- [x] 11f: Fix any actual bugs found in TypeScript SDK error handling — no bugs found. SSE parser handles all edge cases safely: chunked delivery (including mid-UTF8), connection drops propagate cleanly, error events yielded as data (not thrown), null body guarded with descriptive `NetworkError`. Added `content` getter to Stream class (11a) as an API improvement — prior to this, accumulated content from delta events was inaccessible after a failed stream. Reader errors propagate as raw `Error` rather than `NetworkError` — this is consistent with Go SDK and justified since `parseSSEStream` is a low-level utility.

---

## Phase 12: Cross-SDK Consistency & E2E Streaming Errors

Verify all 3 SDKs behave consistently when the server returns errors during streaming. These tests run against a test HTTP server (not a full langdag server — just SSE responses).

- [x] 12a: **Error event format contract** — Documented exact SSE wire format in `sdks/SSE_FORMAT.md` (event types, multi-line data handling, error format). Created 4 canonical SSE fixtures (normal, error-mid-stream, multi-line-error, error-only) tested with identical byte strings across all 3 SDKs. Go → `StreamError{Message}` via `Err()`, Python → `SSEEvent(ERROR, {"message": msg})`, TypeScript → `SSEEvent{type:'error', error: msg}`. All parse identically: same event counts, types, content, error messages. No bugs found.
- [x] 12b: **Graceful degradation contract** — Documented in SSE_FORMAT.md: all SDKs must complete iteration, preserve accumulated content, and signal abnormal state. Tests verify 4 scenarios (no-done, error-termination, empty-response, I/O error) across all 3 SDKs. Behavior is consistent: Go uses `Content()`+`Node()` error, Python uses manual delta accumulation + no node_id, TypeScript uses `stream.content`+`node()` rejection. Content always preserved. No bugs found.
- [x] 12c: **E2E streaming error** — Added `LANGDAG_MOCK_ERROR_MESSAGE` and `LANGDAG_MOCK_ERROR_AFTER_CHUNKS` env vars to mock config. Created `scripts/test-e2e-errors.sh` that starts 3 servers (echo, error, stream_error) and runs all SDK E2E tests. Tests verify: (1) immediate error → all 3 SDKs receive SSE error event with "test error" message, (2) mid-stream error → all 3 SDKs see start + deltas + error with partial content preserved. Go uses `StreamError` via `Err()`, Python yields `SSEEvent(ERROR)`, TypeScript yields `{type:'error'}`. No bugs found.
- [x] 12d: **Cross-SDK divergences documented and justified** — Two intentional divergences documented in SSE_FORMAT.md with explicit tests: (1) Malformed delta JSON: Go emits with empty Content (resilient), Python wraps as `{"message":...}` (consistent API), TypeScript throws `SSEParseError` (fail-fast). (2) Unknown event types: Go forwards as-is, Python skips, TypeScript throws. Divergence is intentional — server always sends valid data; these paths only fire on corruption or protocol mismatch, and each SDK handles them idiomatically. No alignment needed — divergence is justified.

---

## Phase 13: Integration — End-to-End Error Chains (Herm)

Verify that errors flow correctly through the full stack: provider error → langdag → herm agent → user-visible message. These tests use the enhanced mock from Phase 1.

- [x] 13a: **Provider permanent error → user sees error** — Mock returns 401 on all calls. Verified: langdag retry classifies as non-retryable (no EventRetry), herm's retryablePrompt passes through immediately, agent emits EventError with original "401: Unauthorized" message preserved through all wrapping layers, emits EventDone with empty NodeID. No EventUsage or EventTextDelta emitted. No bugs found.
- [x] 13b: **Provider transient error → retry → success** — Mock fails twice with 503, succeeds on third call. Verified: 2 EventRetry events emitted (attempt 1 and 2, each carrying the 503 error), final response delivered normally with full text, EventUsage reflects only the successful call (200 input tokens, not accumulated from failed attempts). No bugs found.
- [x] 13c: **Mid-stream failure → stream retry → success** — Mock sends partial chunks then StreamEventError, retry sends full response. Verified: EventStreamClear emitted before EventRetry (correct ordering for TUI), text after clear contains only the retry response (no duplicate content), EventUsage reflects retry call (150 tokens). No bugs found.
- [x] 13d: **Sub-agent error chain** — Main agent spawns sub-agent via tool_use, sub-agent's provider returns 401. Verified: sub-agent collects error with turn context ("turn 0: prompt: HTTP 401: Unauthorized"), parent agent receives structured tool result containing "401", "Unauthorized", and turn context. Tool result is not IsError (sub-agent Execute succeeded, error is in content). Parent agent's LLM sees error and responds appropriately. No bugs found.
- [x] 13e: **Cascading failure** — Sub-agent's provider returns 529 overloaded on all 3 retry attempts, exhausting retries. Parent agent receives error result, takes corrective action (calls simple_tool instead), corrective action succeeds. Verified: first sub-agent result contains "529"/"overloaded", parent emits second tool call for fallback, final response indicates successful recovery, EventDone has valid nodeID for conversation resume. No conversation corruption. No bugs found.
- [x] 13f: Fix any actual bugs found in the end-to-end error chain — no bugs found. All error chains work correctly: permanent errors propagate with original messages through all layers, transient errors retry with correct EventRetry events and usage stats, mid-stream failures trigger StreamClear → retry without content duplication, sub-agent errors surface with full context (turn number, error message), and cascading failures recover cleanly with valid conversation state. All tests pass with `-race`.

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
