# Fix: Agent silent stops mid-run

## Problem

The agent sometimes stops in the middle of a run without any error message or explanation. The user sees the input prompt reappear as if the agent finished normally, but the task is incomplete.

## Root cause

Three code paths in `runLoop` (`cmd/herm/agent.go`) allow the tool loop to exit silently — no `EventError` is emitted, so the TUI shows no indication that something went wrong.

### 1. `max_tokens` truncation with a valid node ID (primary bug)

**Before:**

```go
if stopReason == "max_tokens" && len(toolCalls) == 0 && nodeID == "" {
    // emit error...
    break
}
```

The check required all three conditions: `max_tokens` stop reason, no tool calls, **and** empty `nodeID`. But when langdag's multi-part continuation mechanism exhausts its output budget (4× the per-call `max_tokens`, i.e. ~65K tokens), the response comes back with:

- `stopReason = "max_tokens"` — the last provider call was still truncated
- `len(toolCalls) == 0` — no complete tool_use blocks in the response
- `nodeID = "<valid UUID>"` — langdag saved a node for the accumulated text

Because `nodeID` is not empty, the check doesn't trigger. Execution falls through to the for-loop condition `len(toolCalls) > 0`, which is `false`, so the loop exits normally. `EventDone` is emitted with no preceding `EventError`. The user sees the agent stop with no explanation.

**How it happens in practice:** The LLM generates a very long text response (reasoning, analysis, or large file content) that exhausts the continuation budget before it gets to emit any tool calls. The response is truncated but the agent treats it as a normal completion.

**Fix:** Remove the `nodeID == ""` requirement from the condition:

```go
if stopReason == "max_tokens" && len(toolCalls) == 0 {
    // emit error...
    break
}
```

This applies to both the initial LLM call check (line 797) and the tool-loop check (line 1037). Now any `max_tokens` response with no tool calls is surfaced as an error, regardless of whether langdag managed to save a node.

### 2. Empty `nodeID` after successful stream (silent break)

**Before:**

```go
if nodeID == "" {
    break
}
```

Inside the tool loop, if `retryableStream` succeeds (no error) but returns an empty `nodeID`, the loop breaks with no error event. While this should be rare (langdag always assigns a UUID to saved nodes), it can theoretically happen if the stream completes in an unexpected state.

**Fix:** Emit an error before breaking:

```go
if nodeID == "" {
    a.emit(AgentEvent{
        Type:  EventError,
        Error: fmt.Errorf("LLM response produced no conversation node — stopping"),
    })
    break
}
```

### 3. Max tool iterations reached (silent limit)

**Before:**

```go
for iteration := 0; iteration < maxIter && len(toolCalls) > 0; iteration++ {
    // ...
}
// no warning if maxIter was reached
a.emit(AgentEvent{Type: EventDone, NodeID: nodeID})
```

The tool loop is capped at `defaultMaxToolIterations = 25`. When the cap is reached while the LLM still has pending tool calls, the loop exits and `EventDone` is emitted with no indication that the limit was hit. For complex tasks that require many tool calls, this makes the agent appear to stop mid-work for no reason.

**Fix:** Track the iteration count and emit a warning after the loop:

```go
iteration := 0
for iteration < maxIter && len(toolCalls) > 0 {
    iteration++
    // ...
}

if iteration >= maxIter && len(toolCalls) > 0 {
    a.emit(AgentEvent{
        Type:  EventError,
        Error: fmt.Errorf("reached maximum tool iterations (%d) — stopping; send another message to continue", maxIter),
    })
}
```

## Execution flow (annotated)

Below is the simplified tool loop with all exit paths labeled:

```
runLoop(ctx, userMessage, parentNodeID)
  │
  ├─ Initial LLM call via retryableStream()
  │   ├─ err != nil            → EventError + EventDone (VISIBLE ✓)
  │   ├─ max_tokens, no tools  → EventError + EventDone (VISIBLE ✓, was silent when nodeID non-empty)
  │   └─ nodeID == ""          → EventDone only (acceptable — empty response, nothing to do)
  │
  └─ Tool loop (up to maxIter iterations)
      │
      ├─ ctx.Err()             → EventError + break (VISIBLE ✓)
      ├─ Execute tools...
      ├─ Re-call LLM with tool results via retryableStream()
      │   ├─ err != nil        → EventError + break (VISIBLE ✓)
      │   ├─ max_tokens, no tools → EventError + break (VISIBLE ✓, was silent when nodeID non-empty)
      │   └─ nodeID == ""      → EventError + break (VISIBLE ✓, was silent)
      │
      ├─ Loop exits: len(toolCalls) == 0  → normal end (LLM decided it's done)
      └─ Loop exits: iteration >= maxIter → EventError (VISIBLE ✓, was silent)
          │
          └─ EventDone
```

## Files changed

- `cmd/herm/agent.go` — `runLoop` function

## Testing

All existing tests pass. The fixes are additive (emit error events in paths that previously had none) and don't change control flow — every `break` and loop exit remains in the same place.
