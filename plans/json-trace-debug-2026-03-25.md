# JSON Trace Debug Files

**Goal:** Replace the current plain-text debug file format with a structured JSON trace that captures every event in a session — LLM calls with token/model metadata, tool calls paired with their results, sub-agent traces nested hierarchically, timing everywhere, and an `info` summary object. No content truncation. No resize-based regeneration (word-wrapping is a display concern).

**Prior work:** The current debug system (`debuglog.go`, 174 lines) writes plain text with `── Section ──` delimiters. Events are logged one-by-one in `handleAgentEvent()` (`agentui.go:313-548`). Tool calls and results are separate sections. Sub-agent events are forwarded as `EventSubAgentStart/Delta/Status` with limited detail. `regenerateDebugFile()` rewrites the file on every terminal resize. Tests in `debuglog_test.go` (497 lines).

**Codebase context:**
- `debuglog.go` — current debug file writer: `initDebugLog`, `debugWrite`, `regenerateDebugFile`, `sessionSummaryBuilder`
- `agentui.go:313-548` — `handleAgentEvent()` writes debug sections for each event type
- `agentui.go:219-224` — `submitToAgent()` writes system prompt, tool defs, user message to debug
- `agent.go:111-170` — `AgentEvent` struct with all event types and fields
- `agent.go:361-384` — `emitUsage()` extracts token metadata from langdag Node
- `agent.go:840-870` — tool execution with timing (`toolStart := time.Now()`, `toolDur := time.Since(toolStart)`)
- `subagent.go:237-304` — sub-agent event draining, token tracking, forwarding to parent
- `main.go:219-220` — `debugFile *os.File` and `debugFilePath string` on App
- `main.go:139-149` — session-level stats fields (tokens, costs, tool stats)
- `main.go:995` — `regenerateDebugFile()` called on `resizeMsg`
- `commands.go:67-72` — `/clear` closes old debug file, creates new one
- `configeditor.go:91-96` — debug mode toggle re-initializes debug file
- `render.go:682-689` — debug file path shown in footer
- `debuglog_test.go` — 497 lines of tests for current text-based format

**JSON trace schema:**

```json
{
  "info": {
    "session_id": "abc-123",
    "started_at": "2026-03-25T15:04:05.000Z",
    "ended_at": "2026-03-25T15:10:30.000Z",
    "duration_ms": 325000,
    "model": "claude-opus-4-5-20251101",
    "git_branch": "main",
    "git_root": "/Users/frl/Desktop/herm",
    "os": "darwin",
    "totals": {
      "llm_calls": 12,
      "main_agent_llm_calls": 8,
      "sub_agent_llm_calls": 4,
      "input_tokens": 150000,
      "output_tokens": 25000,
      "cache_read_tokens": 50000,
      "cache_creation_tokens": 1000,
      "reasoning_tokens": 0,
      "cost_usd": 1.25,
      "tool_calls": 15,
      "tool_result_bytes": 45000,
      "sub_agents_spawned": 2,
      "compactions": 0,
      "retries": 0,
      "errors": 0
    },
    "tool_summary": {
      "bash": { "calls": 5, "result_bytes": 20000, "total_duration_ms": 8500 },
      "read": { "calls": 3, "result_bytes": 15000, "total_duration_ms": 120 }
    }
  },
  "system_prompt": "full system prompt text...",
  "tools": [
    { "name": "bash", "description": "Execute a bash command", "parameters": {} }
  ],
  "events": []
}
```

**Event types in the `events` array:**

```json
// User message
{
  "type": "user_message",
  "timestamp": "2026-03-25T15:04:05.123Z",
  "content": "full user message text"
}

// LLM response turn (groups text + tool calls + usage)
{
  "type": "llm_response",
  "agent_id": "main-abc",
  "node_id": "node-123",
  "started_at": "2026-03-25T15:04:06.000Z",
  "ended_at": "2026-03-25T15:04:08.500Z",
  "duration_ms": 2500,
  "model": "claude-opus-4-5-20251101",
  "content": "Let me check that for you.",
  "stop_reason": "tool_use",
  "usage": {
    "input_tokens": 5000,
    "output_tokens": 200,
    "cache_read_tokens": 3000,
    "cache_creation_tokens": 500,
    "reasoning_tokens": 0
  },
  "cost_usd": 0.05,
  "tool_calls": [
    {
      "id": "toolu_01abc",
      "name": "bash",
      "input": { "command": "ls -la" },
      "started_at": "2026-03-25T15:04:08.600Z",
      "ended_at": "2026-03-25T15:04:09.800Z",
      "duration_ms": 1200,
      "result": "file1.txt\nfile2.txt",
      "result_bytes": 19,
      "is_error": false,
      "approval": {
        "requested": true,
        "description": "Run bash command",
        "approved": true,
        "wait_duration_ms": 3500
      }
    }
  ]
}

// Sub-agent (nested trace with its own events)
{
  "type": "sub_agent",
  "agent_id": "sub-xyz",
  "task": "Research the codebase",
  "model": "claude-sonnet-4-5-20251101",
  "started_at": "2026-03-25T15:05:00.000Z",
  "ended_at": "2026-03-25T15:05:45.000Z",
  "duration_ms": 45000,
  "usage": {
    "input_tokens": 30000,
    "output_tokens": 5000,
    "cache_read_tokens": 10000,
    "cache_creation_tokens": 0,
    "reasoning_tokens": 0
  },
  "cost_usd": 0.15,
  "turns": 4,
  "max_turns": 10,
  "events": [
    "... same event types as parent, recursively ..."
  ]
}

// Compaction
{
  "type": "compaction",
  "timestamp": "2026-03-25T15:08:00.000Z",
  "node_id": "node-456",
  "summary": "Conversation was summarized to free context window..."
}

// Retry
{
  "type": "retry",
  "timestamp": "2026-03-25T15:06:30.000Z",
  "attempt": 1,
  "max_attempts": 3,
  "delay_ms": 2000,
  "error": "429 Too Many Requests"
}

// Stream clear
{
  "type": "stream_clear",
  "timestamp": "2026-03-25T15:06:29.500Z"
}

// Error
{
  "type": "error",
  "timestamp": "2026-03-25T15:09:00.000Z",
  "message": "Agent error: context canceled"
}
```

**Additional properties beyond user's request:**
- `stop_reason` per LLM call — distinguishes `end_turn` vs `tool_use` vs `max_tokens` (useful for diagnosing silent stream stops)
- `node_id` per LLM response — links back to langdag conversation tree
- `agent_id` — distinguishes main agent from sub-agents
- `reasoning_tokens` — extended thinking tokens (included when non-zero)
- `cost_usd` per LLM call AND in totals — granular cost attribution
- `git_branch`, `git_root`, `os` in info — environment context
- `tool_summary` with `total_duration_ms` per tool — performance profiling
- `approval` object on tool calls — captures approval flow (requested, approved, wait time)
- `result_bytes` per tool call — size tracking without counting yourself
- `turns` + `max_turns` on sub-agents — budget tracking

**Architecture approach:**
- Build trace in memory using Go structs as events arrive
- Tool calls paired with results by matching on tool ID within each LLM turn
- Sub-agent traces collected by the SubAgentTool and attached to the parent trace on completion
- Periodic JSON writes after each complete LLM turn (crash resilience: lose at most one turn)
- Final write on EventDone with complete `info.ended_at` and totals
- No resize-based regeneration — file is written as events complete, not on display changes
- File extension: `.json` instead of `.log`

**Failure modes:**
- Crash mid-session → last periodic write has all completed turns, `info.ended_at` will be null
- Sub-agent crash → partial sub-agent trace included with error event
- Disk full / permissions → same graceful degradation as current (log to stderr, continue)
- Very large sessions → JSON file grows with session; no content truncation means files can be large
- Concurrent sub-agents → events arrive interleaved; collector groups by agent_id

**Success criteria:**
- Debug files are valid JSON parseable by `jq`, Python `json.load`, etc.
- Every LLM call has model, token counts, cost, timing
- Every tool call is paired with its result in the same JSON object
- Sub-agent traces are fully nested with their own events array
- `info.totals` matches the sum of all individual events
- System prompts and tool results appear in full (no truncation)
- Terminal resize does NOT trigger file regeneration
- `/clear` finalizes current trace, starts new `.json` file
- Headless mode (`--prompt --debug`) produces valid JSON trace
- `jq '.info.totals.cost_usd' trace.json` works

**Open questions:**
1. Should we keep a `.jsonl` streaming format alongside the final `.json` for real-time tailing? (Recommendation: no — periodic full writes are simpler and `tail -f` + `jq` works on partial JSON)
2. Should sub-agent internal tool call events be forwarded individually to the parent, or collected by the SubAgentTool and attached in bulk? (Recommendation: collected by SubAgentTool — cleaner, no changes to event channel protocol)

---

## Phase 1: Trace Data Structures and Collector

Define the JSON schema as Go structs and implement the in-memory trace collector that builds the hierarchical structure from flat events.

**Contract:** A `TraceCollector` struct accepts events via methods (not channels) and maintains the in-memory trace. It groups text deltas into LLM response content, pairs tool calls with results by ID, and tracks timing. It exposes `MarshalJSON()` for serialization. The collector handles the state machine: text accumulation → usage event finalizes LLM metadata → tool start/result pairs → next turn.

- [x] 1a: **Define trace JSON structs** — New file `trace.go` (or extend `debuglog.go`). Structs: `Trace` (top-level with `Info`, `SystemPrompt`, `Tools`, `Events`), `TraceInfo` (with `Totals` and `ToolSummary`), `TraceEvent` (union type with `Type` discriminator), `TraceLLMResponse` (with nested `TraceToolCall` array), `TraceSubAgent` (with nested `Events`), `TraceUsage`, `TraceToolCall` (with `Result`, `Approval`). All timestamp fields as `time.Time` serialized to RFC3339 with milliseconds. All durations as `int` milliseconds.

- [x] 1b: **Implement TraceCollector** — Methods: `SetSystemPrompt(prompt string)`, `SetTools(tools []ToolDef)`, `AddUserMessage(content string)`, `StartLLMResponse(agentID string)`, `AddTextDelta(agentID, text string)`, `SetUsage(agentID string, usage, model, nodeID, cost)`, `StartToolCall(agentID, toolID, toolName string, input json.RawMessage)`, `EndToolCall(agentID, toolID string, result string, isError bool, duration time.Duration)`, `AddApproval(agentID, toolID string, desc string, approved bool, waitDur time.Duration)`, `AddSubAgent(trace *Trace)`, `AddCompaction(nodeID, summary string)`, `AddRetry(attempt, max int, delay time.Duration, err string)`, `AddStreamClear()`, `AddError(msg string)`, `Finalize()`. Internally tracks `currentTurn map[agentID]*TraceLLMResponse` and `pendingTools map[toolID]*TraceToolCall`.

- [x] 1c: **JSON serialization and file writing** — `Trace.WriteToFile(path string) error` serializes with `json.MarshalIndent` (2-space indent for readability) and atomically writes (write to temp file, rename). `TraceCollector.FlushToFile(path string)` builds the current `Trace` snapshot (including partial `info`) and calls `WriteToFile`. The `info.ended_at` is only set on `Finalize()`.

---

## Phase 2: Sub-Agent Trace Collection

Enable full sub-agent traces by having each SubAgentTool collect its sub-agent's events into a nested trace, then attach to the parent.

**Contract:** When a sub-agent runs, a local `TraceCollector` captures all its events. On completion, the sub-agent's `Trace` is passed back to the parent via the `EventSubAgentStatus` event (new `SubTrace` field on `AgentEvent`). The parent's collector nests it as a `sub_agent` event.

- [x] 2a: **Add `SubTrace` field to AgentEvent** — In `agent.go`, add `SubTrace *Trace` to the `AgentEvent` struct. Only populated on `EventSubAgentStatus` when `Text == "done"`.

- [x] 2b: **Collect sub-agent trace in SubAgentTool** — In `subagent.go`, create a `TraceCollector` when the sub-agent starts. In the event drain loop, feed all events (text deltas, tool calls, results, usage) to this local collector in addition to the existing forwarding logic. On sub-agent completion, call `Finalize()` and attach the trace to the `EventSubAgentStatus` event.

- [ ] 2c: **Forward sub-agent tool call events** — Currently `EventToolCallStart` from sub-agents is forwarded as `EventSubAgentStatus`. Additionally feed these to the local trace collector so the nested trace includes full tool call details (name, input, result, timing).

---

## Phase 3: Integration — Replace Text Debug with JSON Trace

Wire the trace collector into the App lifecycle and event handlers, replacing all `debugWrite` calls.

**Contract:** The App owns a `TraceCollector`. All events feed into it. The collector writes the JSON file after each complete LLM turn and on session end. No resize-based regeneration. File extension changes from `.log` to `.json`.

- [ ] 3a: **Update App struct and lifecycle** — Replace `debugFile *os.File` + `debugFilePath string` with `traceCollector *TraceCollector` + `traceFilePath string`. Update `initAppDebugLog()` to create the trace collector and set the file path (`.json` extension). Update `closeDebugLog` → finalize collector and do final write. Update config editor toggle.

- [ ] 3b: **Feed events from submitToAgent** — In `agentui.go:submitToAgent()`, replace the three `debugWriteSection` calls (system prompt, tool definitions, user message) with `traceCollector.SetSystemPrompt()`, `traceCollector.SetTools()`, `traceCollector.AddUserMessage()`. Capture `agentStartTime` in the trace info.

- [ ] 3c: **Feed events from handleAgentEvent** — Replace every `debugWriteSection` call in `handleAgentEvent()` with the corresponding trace collector method. Map: `EventTextDelta` → `AddTextDelta`, `EventToolCallStart` → `StartToolCall`, `EventToolResult` → `EndToolCall` + flush, `EventUsage` → `SetUsage`, `EventApprovalReq` → `AddApproval`, `EventCompacted` → `AddCompaction`, `EventSubAgentStart` → (record start time), `EventSubAgentStatus` (done) → `AddSubAgent(event.SubTrace)`, `EventStreamClear` → `AddStreamClear()`, `EventRetry` → `AddRetry`, `EventError` → `AddError`, `EventDone` → `Finalize` + final flush.

- [ ] 3d: **Periodic flush after each LLM turn** — After `EventToolResult` (when all tools for a turn are complete) or after `EventUsage` (if no tool calls), call `FlushToFile`. This provides crash resilience — at most one turn is lost.

- [ ] 3e: **Remove regenerateDebugFile and resize trigger** — Delete `regenerateDebugFile()` from `debuglog.go`. Remove the `regenerateDebugFile()` call from the `resizeMsg` handler in `main.go`. The JSON trace is written on events, not on display changes.

- [ ] 3f: **Update /clear and headless mode** — In `/clear` handler: finalize current trace, start new collector with new `.json` file path. In headless mode: same lifecycle, print `.json` path to stderr.

- [ ] 3g: **Update footer display** — In `render.go`, update the debug path display to show the `.json` file path (minimal change — just the extension will naturally change).

- [ ] 3h: **Populate info object** — Wire `info` fields: `session_id` from `a.sessionID`, `started_at` from `a.agentStartTime`, `model` from the first LLM call's model, `git_branch` from `a.branch`, `git_root` from `a.repoRoot`, `os` from `runtime.GOOS`. Compute `totals` from accumulated session stats. Build `tool_summary` from `sessionToolStats` (add duration tracking per tool).

---

## Phase 4: Tests

Rewrite the debug test suite for the new JSON trace format.

- [ ] 4a: **Test trace data structures and serialization** — Construct a `Trace` manually, marshal to JSON, verify structure. Verify timestamps are RFC3339 with milliseconds. Verify `null` for unset optional fields (e.g. `ended_at` before finalize).

- [ ] 4b: **Test TraceCollector event flow** — Feed a realistic sequence of events (user message → text deltas → usage → tool call start → tool result → done), verify the resulting trace has correct structure: one `user_message` event, one `llm_response` event with paired tool call, correct timing, correct totals in `info`.

- [ ] 4c: **Test tool call pairing** — Multiple tool calls in one LLM turn, verify each is paired with its result by ID. Test parallel tool calls (results may arrive in different order than starts).

- [ ] 4d: **Test sub-agent trace nesting** — Create a sub-agent trace, attach via `AddSubAgent`, verify it appears as a `sub_agent` event with nested `events` array. Verify sub-agent tokens are counted in parent's `info.totals.sub_agent_llm_calls`.

- [ ] 4e: **Test file lifecycle** — `initAppDebugLog` creates `.json` file. `/clear` finalizes old trace and creates new file. Config toggle. Headless mode. Debug mode off → no file.

- [ ] 4f: **Test periodic flush and crash resilience** — After flush, read file and verify it's valid JSON. Verify `info.ended_at` is null before finalize, set after. Verify partial trace (mid-session) is still valid JSON.

- [ ] 4g: **Test no resize regeneration** — Verify `resizeMsg` does NOT trigger any trace file write (regression test for the removed behavior).
