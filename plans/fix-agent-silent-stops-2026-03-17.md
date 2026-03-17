# Fix Agent Silent Stops

The agent sometimes stops working with no error message. Three parallel investigations converged on the same root cause: **the event channel architecture can deadlock**, silently freezing the agent goroutine.

## Root Cause Analysis

### Consensus findings (all 3 investigations agree)

**Primary cause: Blocking `emit()` deadlocks the agent goroutine**
- `agent.go:279` — `a.events <- e` is a blocking channel send with only 64-slot buffer (line 216)
- During long streaming responses (65+ text delta chunks) or parallel tool execution, the buffer fills
- Once full, the agent goroutine blocks silently on the next `emit()` call — no error, no timeout, just frozen
- The TUI drains events via `drainAgentEvents()` (`main.go:4415`) only on 50ms ticks — can't keep up with bursts

**Amplifier: Sub-agent `forward()` creates cascading deadlock**
- `subagent.go:100-104` — `t.parentEvents <- e` is also a blocking send to the parent's event channel
- When parent's buffer is full, `forward()` blocks → sub-agent's `Events()` consumer stalls → sub-agent's own `emit()` blocks
- `wg.Wait()` at `agent.go:696` never returns → parent agent frozen → complete deadlock

**No panic recovery on agent goroutine**
- `main.go:4412` — `go agent.Run(context.Background(), ...)` has no `defer/recover`
- Any panic in the agent silently kills the goroutine with zero user feedback

**Event channel never closed**
- No `close(a.events)` anywhere in the codebase
- `drainAgentEvents()` checks `if !ok` (line 4422) expecting closure, but it never happens
- `subagent.go:182` — `for event := range agent.Events()` blocks forever if EventDone isn't emitted (which is the case when emit deadlocks)

### Secondary findings

- `agent.go:703` — `json.Marshal` error silently ignored with `_`, could send malformed tool results
- Stream can close without `chunk.Done` flag (network interruption), loop exits silently without emitting EventDone
- No stop_reason validation — `max_tokens` truncation treated as success

## Plan

### Phase 1: Make `emit()` non-blocking (fixes the deadlock)

- [ ] 1a: Replace the blocking channel send in `emit()` (`agent.go:277-280`) with a `select` that uses a generous buffer and drops/logs if full, OR switch to an unbounded queue (e.g., slice+mutex or ring buffer). The key contract: **`emit()` must never block the caller**. Consider increasing the channel buffer as a complementary measure.

- [ ] 1b: Apply the same non-blocking treatment to `SubAgentTool.forward()` (`subagent.go:100-104`) — it sends to the parent's event channel and has the same deadlock risk.

- [ ] 1c: Add panic recovery wrapper around `agent.Run()` in `main.go:4412`. On panic, emit an `EventError` with the panic message and stack trace, then emit `EventDone`. This ensures panics surface to the user instead of silently killing the goroutine.

### Phase 2: Proper event channel lifecycle

- [ ] 2a: Close the event channel when the agent finishes. Add `defer close(a.events)` at the right point in `Agent.Run()` (after the deferred running=false block). This unblocks any `range agent.Events()` readers and makes `drainAgentEvents()` detect completion via `!ok`.

- [ ] 2b: In `subagent.go`, handle the case where `Events()` channel closes before `EventDone` is received. The code at line 221 already has a comment acknowledging this — ensure it works correctly with the new close behavior.

### Phase 3: Stream resilience

- [ ] 3a: After both streaming loops (`agent.go:482-497` and `712-727`), detect when the loop exits without receiving `chunk.Done`. Emit an `EventError` indicating the stream was interrupted, rather than silently continuing.

- [ ] 3b: Handle the `json.Marshal` error at `agent.go:703` — log/emit an error instead of ignoring it with `_`.

### Phase 4: Tests

- [ ] 4a: Write a test that creates an agent with a small event buffer and triggers enough emissions to fill it — verify the agent doesn't deadlock (completes within a timeout).

- [ ] 4b: Write a test that panics inside a tool execution — verify the panic is caught, an error event is emitted, and the agent completes gracefully.

- [ ] 4c: Write a test simulating a sub-agent that emits many events — verify the parent doesn't deadlock.

## Success Criteria

- Agent never silently stops — it either completes, shows an error, or times out visibly
- No goroutine leaks: `go test -race` passes
- Parallel sub-agent execution with 5+ agents doesn't deadlock
- Stream interruption surfaces a visible error to the user

## langdag Team Report

The following should be raised with the langdag team (separate from this plan):

1. **Stream channel closure guarantees**: Does `langdag` guarantee that a `StreamEvent` with `Done=true` is always sent before the stream channel closes? If the stream is interrupted (network error, timeout), is an error event sent, or does the channel just close? Consumers currently can't distinguish "ended normally" from "interrupted" when `Done` isn't received.

2. **Stop reason exposure**: The `StreamEvent.Done` flag is boolean, but LLM APIs return a `stop_reason` (e.g., `end_turn`, `max_tokens`, `content_filter`). Consumers need access to this to handle truncation vs. normal completion differently. Consider adding a `StopReason` field to the stream event or result.
