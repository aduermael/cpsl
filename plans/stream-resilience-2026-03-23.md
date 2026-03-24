# Stream Resilience: Fix Silent Interruptions

**Goal:** Eliminate the "stream just stops with no error" experience. Every stream failure should either recover transparently or surface a clear error to the user.

**Prior work:** The `fix-agent-silent-stops` (2026-03-17) and `exploration-and-resilience` (2026-03-19) plans already addressed: non-blocking `emit()` with 4096 buffer, `defer close(a.events)`, panic recovery, `retryablePrompt()` with exponential backoff, `isRetryableError()` classification, and `EventRetry` events.

**What's still broken — three remaining gaps:**

1. **No per-chunk stream timeout.** `drainStream()` (`agent.go:584-602`) does `for chunk := range result.Stream` — if the provider stops sending chunks mid-stream (network stall, server hang, etc.), this blocks **forever**. The retry logic only covers connection-level failures (before the stream starts), not mid-stream stalls. This is the most likely cause of the reported "stream just stops" behavior.

2. **Critical events can be dropped.** `emit()` (`agent.go:307-315`) silently drops ANY event when the 4096-slot channel buffer is full — including `EventDone` and `EventError`. When `EventDone` is dropped, the TUI remains in "agent running" state indefinitely. The sub-agent code (`subagent.go:296`) explicitly acknowledges this: `"Channel closed without EventDone (e.g., EventDone was dropped)"`.

3. **Stream interruptions are not retried.** When `drainStream` returns `streamOK=false` (lines 622-626 and 847-850), the agent emits an error and stops. No attempt to retry the LLM call. This turns every transient mid-stream network blip into a conversation-ending failure.

**Codebase context:**
- `agent.go:584-602` — `drainStream()`: reads `result.Stream` (a `<-chan langdag.StreamChunk`), returns `(toolCalls, nodeID, streamOK)`
- `agent.go:307-315` — `emit()`: non-blocking send to `a.events` (4096 buffer), drops on full
- `agent.go:604-862` — `runLoop()`: two call sites for `drainStream` (line 622 and 847), both treat `!streamOK` as fatal
- `agent.go:549-576` — `retryablePrompt()`: retries connection-level errors, not stream errors
- `subagent.go:110-120` — `forward()`: same drop pattern as `emit()`
- `subagent.go:281, 308` — `<-done`: waits on sub-agent goroutine with no timeout
- `agentui.go:252` — `agent.Run(context.Background(), ...)`: no overall timeout on agent
- `langdag.StreamChunk`: `{Content, ContentBlock, Done, Error, NodeID, StopReason}`

**Success criteria:**
- A mid-stream provider stall (no chunks for 60s) results in a retry, not a permanent hang
- `EventDone` is never silently dropped — the TUI always learns the agent has finished
- A retryable stream interruption triggers at least one retry attempt before giving up
- Sub-agent goroutine hangs resolve within a bounded time
- All existing tests pass; new tests cover each scenario

---

## Phase 1: Per-Chunk Stream Timeout

Add a timeout to `drainStream` so that if no chunk arrives within a configurable period, the stream is treated as interrupted rather than blocking forever.

**Contract:** `drainStream` should return `streamOK=false` if no chunk arrives within the timeout window. The timeout resets on every chunk received (it's an inactivity timeout, not a total timeout — long responses are fine as long as chunks keep flowing).

- [x] 1a: **Add `streamChunkTimeout` field to Agent** — Default to 90 seconds. This allows future configurability without a constant. Set it in `NewAgent()` alongside existing defaults.

- [x] 1b: **Rewrite `drainStream` with per-chunk timeout** — Replace the `for chunk := range result.Stream` loop with a `select` that reads from `result.Stream` or a `time.After` timer that resets on each chunk. On timeout, drain any remaining buffered chunks (non-blocking), then return `streamOK=false`. The context should also be checked so cancellation still works.

- [x] 1c: **Test stream timeout** — Test with a mock stream that sends a few chunks then stalls. Verify `drainStream` returns `streamOK=false` after the timeout, not blocking forever. Also test that a slow-but-steady stream (chunks arriving just within the timeout) completes normally.

**Failure modes:**
- Timeout too short: legitimate slow streams (e.g., thinking models) get killed. 90s is generous — even the slowest models emit at least one chunk within 90s.
- Timeout too long: user waits a long time before seeing an error. 90s is the upper bound of acceptable wait.

---

## Phase 2: Guaranteed Critical Event Delivery

Ensure `EventDone` and `EventError` events are never silently dropped. These are control-flow events that the TUI depends on to know the agent has finished.

**Contract:** `EventDone` and `EventError` must always reach the consumer. Text deltas and other events can still be dropped under backpressure.

- [x] 2a: **Add a `done` signaling channel to Agent** — Add a `doneCh chan struct{}` that is closed when `EventDone` is emitted. The TUI can `select` on both `a.events` and `a.doneCh` to detect completion even if the event was dropped from the buffer. This is simpler and more robust than making `emit()` blocking for certain event types (which risks reintroducing deadlocks).

- [x] 2b: **Close `doneCh` on EventDone emission** — In `emit()`, when the event type is `EventDone`, close `a.doneCh` (using `sync.Once` to prevent double-close). The regular event channel send is still non-blocking — the `doneCh` is a backup signal.

- [x] 2c: **Update TUI to check `doneCh`** — In `drainAgentEvents()` (`agentui.go:255-275`), add a case that selects on the agent's done channel. When it fires and the events channel has been fully drained, mark `a.agentRunning = false`. This ensures the TUI always exits the "running" state.

- [x] 2d: **Apply same pattern to sub-agent `forward()`** — In `SubAgentTool`, when forwarding `EventDone` or `EventError`, use a dedicated signal or blocking send to ensure the parent receives these critical events. The `for event := range agent.Events()` loop in `Execute()` (`subagent.go:238`) should also check the agent's done channel so it doesn't hang if `EventDone` was dropped.

- [x] 2e: **Test critical event delivery under backpressure** — Fill the event buffer to capacity, then trigger agent completion. Verify that the TUI (or test consumer) still detects that the agent is done, even though the `EventDone` event was dropped from the main channel.

**Failure modes:**
- Double-close panic on `doneCh`: mitigated by `sync.Once`
- `doneCh` closed but events still being drained: fine — the TUI should drain remaining events before acting on `doneCh`

---

## Phase 3: Retry on Stream Interruption

When a stream fails mid-response, retry the LLM call instead of immediately giving up.

**Contract:** On `drainStream` returning `streamOK=false`, if the failure is retryable (not context cancellation), retry the prompt once. Clear any partial text emitted before the retry so the user doesn't see duplicate content. If the retry also fails, emit the error and stop.

**Codebase context:**
- Two call sites: initial prompt (line 622) and tool-loop follow-up (line 847)
- `retryablePrompt()` already handles connection-level retries; stream retries are a separate layer on top
- Partial text has already been emitted via `EventTextDelta` before the interruption

- [x] 3a: **Add `retryableStream` helper** — New function that wraps `retryablePrompt` + `drainStream` into a single call that retries on stream failure. Signature: `retryableStream(ctx, cfg, promptFn) (toolCalls, nodeID, error)`. On stream failure with a retryable error pattern (or channel-closed-without-Done), emit an `EventRetry` event and re-call `promptFn`. Emit `EventStreamRetry` (or reuse `EventRetry`) so the TUI shows "stream interrupted, retrying...". Limit stream retries to 1 additional attempt (on top of the connection-level retries inside `retryablePrompt`).

- [x] 3b: **Emit `EventStreamClear` before retry** — When retrying after a stream interruption, emit a new event type that tells the TUI to discard the in-progress streaming text. The TUI's `handleAgentEvent` should reset `a.streamingText` when it receives this event. Without this, the user sees the partial response followed by the full retry response, which looks like duplicate text.

- [x] 3c: **Replace both `drainStream` call sites with `retryableStream`** — In `runLoop()`, replace the patterns at lines 609-626 and 838-850 with calls to `retryableStream`. The error handling after each call remains the same (emit error + done/break).

- [x] 3d: **Test stream retry** — Test: stream fails mid-response → retry succeeds → full response received. Stream fails twice → gives up with error. `EventStreamClear` emitted before retry. Context cancellation during retry → no retry attempted.

**Failure modes:**
- Retry produces different response than original → acceptable, LLM responses are non-deterministic
- Partial tool calls emitted before failure → tool calls are collected but not executed until stream completes, so no side effects to undo
- Infinite retry loop → capped at 1 stream retry per prompt call

---

## Phase 4: Sub-Agent Timeout Guard

Prevent indefinite hangs when a sub-agent goroutine gets stuck.

**Codebase context:**
- `subagent.go:281` — `<-done` after `EventDone`, waits for goroutine
- `subagent.go:308` — `<-done` in fallback path (channel closed without EventDone)
- The sub-agent goroutine runs `agent.Run()` which now has stream timeouts (Phase 1), but could still hang in tool execution or other paths

- [x] 4a: **Add timeout to sub-agent `<-done` waits** — Replace both `<-done` with a `select` that also checks `time.After(subAgentDoneTimeout)`. Use a generous timeout (5 minutes) — this is a safety net, not the primary timeout. On timeout, log the hang and return whatever results were collected so far, appending an error note.

- [x] 4b: **Test sub-agent timeout** — Test with a mock agent that never completes its goroutine. Verify the parent returns within the timeout with an error message rather than hanging forever.

---

## Phase 5: Integration Tests

- [x] 5a: **End-to-end stream stall test** — Simulate a provider that sends 3 chunks then stalls. Verify: stream timeout fires, retry is attempted, if retry succeeds the user gets a complete response, if retry fails the user sees a clear error and the agent exits cleanly.

- [x] 5b: **End-to-end backpressure test** — Create an agent with a small event buffer (e.g., 64). Trigger a response that generates many events. Verify: agent completes without deadlock, TUI detects completion via `doneCh` even if `EventDone` was dropped from the main channel, no goroutine leaks.

---

## Open Questions

1. **Stream timeout value:** 90s is proposed. Should this be configurable via settings, or is a compile-time constant sufficient for now?
2. **EventStreamClear UX:** When the TUI discards partial text on retry, should it show a placeholder like "[retrying...]" or just silently replace the text when the retry succeeds?
3. **langdag guarantees:** Does langdag guarantee that `result.Stream` is closed (not just no more sends) when the provider connection drops? If the channel is never closed AND no error chunk is sent, the per-chunk timeout is the only safety net.
