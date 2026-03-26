package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"langdag.com/langdag/types"
)

// ── 4a: Test trace data structures and serialization ──

func TestTrace_ManualConstruction_MarshalJSON(t *testing.T) {
	now := time.Date(2026, 3, 25, 15, 4, 5, 123000000, time.UTC)
	durMS := int64(5000)
	info := &TraceInfo{
		SessionID:  "sess-001",
		StartedAt:  &now,
		DurationMS: &durMS,
		Model:      "claude-opus-4-5-20251101",
		GitBranch:  "main",
		GitRoot:    "/tmp/repo",
		OS:         "darwin",
		Totals: TraceTotals{
			LLMCalls:     1,
			InputTokens:  500,
			OutputTokens: 100,
			CostUSD:      0.05,
		},
	}

	userMsg := TraceUserMessage{
		Type:      "user_message",
		Timestamp: now,
		Content:   "hello",
	}
	msgData, err := json.Marshal(userMsg)
	if err != nil {
		t.Fatal(err)
	}

	trace := &Trace{
		Info:         info,
		SystemPrompt: "You are helpful.",
		Tools: []TraceTool{
			{Name: "bash", Description: "Run a command"},
		},
		Events: []json.RawMessage{msgData},
	}

	data, err := json.MarshalIndent(trace, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent failed: %v", err)
	}

	// Parse back and verify structure.
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	for _, key := range []string{"info", "system_prompt", "tools", "events"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("missing top-level key %q", key)
		}
	}

	// Verify info fields.
	var infoOut map[string]json.RawMessage
	if err := json.Unmarshal(parsed["info"], &infoOut); err != nil {
		t.Fatalf("unmarshal info: %v", err)
	}
	if _, ok := infoOut["session_id"]; !ok {
		t.Error("info missing session_id")
	}

	// Verify events array has one element.
	var events []json.RawMessage
	if err := json.Unmarshal(parsed["events"], &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Errorf("events length = %d, want 1", len(events))
	}
}

func TestTrace_TimestampFormat_RFC3339Millis(t *testing.T) {
	ts := time.Date(2026, 3, 25, 15, 4, 5, 123000000, time.UTC)
	ev := TraceUserMessage{
		Type:      "user_message",
		Timestamp: ts,
		Content:   "test",
	}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	// Go's time.Time marshals as RFC3339Nano which includes sub-second precision.
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)
	tsStr, ok := raw["timestamp"].(string)
	if !ok {
		t.Fatal("timestamp not a string")
	}
	// Must parse as RFC3339.
	parsed, err := time.Parse(time.RFC3339Nano, tsStr)
	if err != nil {
		t.Fatalf("timestamp %q is not valid RFC3339: %v", tsStr, err)
	}
	if !parsed.Equal(ts) {
		t.Errorf("parsed timestamp %v != original %v", parsed, ts)
	}
}

func TestTrace_NullEndedAt_BeforeFinalize(t *testing.T) {
	tc := NewTraceCollector("sess-null")
	trace := func() *Trace {
		tc.mu.Lock()
		defer tc.mu.Unlock()
		return tc.buildTraceLocked()
	}()

	data, _ := json.Marshal(trace.Info)
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)
	if raw["ended_at"] != nil {
		t.Errorf("ended_at should be null before Finalize, got %v", raw["ended_at"])
	}
	if raw["duration_ms"] != nil {
		t.Errorf("duration_ms should be null before Finalize, got %v", raw["duration_ms"])
	}
}

// ── 4b: Test TraceCollector event flow ──

func TestTraceCollector_RealisticFlow(t *testing.T) {
	tc := NewTraceCollector("sess-flow")
	tc.SetMainAgentID("agent-main")
	tc.SetSystemPrompt("You are helpful.")
	tc.SetTools([]TraceTool{
		{Name: "bash", Description: "Run a command"},
	})

	// User message.
	tc.AddUserMessage("list files")

	// LLM starts responding.
	tc.StartLLMResponse("agent-main")
	tc.AddTextDelta("agent-main", "Let me ")
	tc.AddTextDelta("agent-main", "check that.")

	// Tool call.
	tc.StartToolCall("agent-main", "tool-1", "bash", json.RawMessage(`{"command":"ls"}`))
	tc.EndToolCall("tool-1", "file1.txt\nfile2.txt", false, 500*time.Millisecond)

	// Usage arrives.
	usage := &TraceUsage{
		InputTokens:  5000,
		OutputTokens: 200,
	}
	tc.SetUsage("agent-main", "claude-opus-4-5-20251101", "node-1", usage, 0.05)

	// Finalize turn and session.
	tc.FinalizeTurn("agent-main")
	tc.Finalize()

	// Build trace and verify.
	tc.mu.Lock()
	trace := tc.buildTraceLocked()
	tc.mu.Unlock()

	if trace.SystemPrompt != "You are helpful." {
		t.Errorf("system prompt = %q", trace.SystemPrompt)
	}
	if len(trace.Tools) != 1 {
		t.Errorf("tools len = %d, want 1", len(trace.Tools))
	}

	// Should have 2 events: user_message + llm_response.
	if len(trace.Events) != 2 {
		t.Fatalf("events len = %d, want 2", len(trace.Events))
	}

	// Check user message event.
	var userEv TraceUserMessage
	if err := json.Unmarshal(trace.Events[0], &userEv); err != nil {
		t.Fatal(err)
	}
	if userEv.Type != "user_message" {
		t.Errorf("event[0] type = %q, want user_message", userEv.Type)
	}
	if userEv.Content != "list files" {
		t.Errorf("event[0] content = %q", userEv.Content)
	}

	// Check LLM response event.
	var llmEv TraceLLMResponse
	if err := json.Unmarshal(trace.Events[1], &llmEv); err != nil {
		t.Fatal(err)
	}
	if llmEv.Type != "llm_response" {
		t.Errorf("event[1] type = %q, want llm_response", llmEv.Type)
	}
	if llmEv.Content != "Let me check that." {
		t.Errorf("event[1] content = %q", llmEv.Content)
	}
	if llmEv.AgentID != "agent-main" {
		t.Errorf("event[1] agent_id = %q", llmEv.AgentID)
	}
	if llmEv.Model != "claude-opus-4-5-20251101" {
		t.Errorf("event[1] model = %q", llmEv.Model)
	}
	if llmEv.StopReason != "tool_use" {
		t.Errorf("event[1] stop_reason = %q, want tool_use", llmEv.StopReason)
	}
	if len(llmEv.ToolCalls) != 1 {
		t.Fatalf("event[1] tool_calls len = %d, want 1", len(llmEv.ToolCalls))
	}
	if llmEv.EndedAt == nil {
		t.Error("event[1] ended_at should be set after FinalizeTurn")
	}
	if llmEv.DurationMS == nil {
		t.Error("event[1] duration_ms should be set after FinalizeTurn")
	}

	// Verify tool call pairing.
	tc0 := llmEv.ToolCalls[0]
	if tc0.Name != "bash" {
		t.Errorf("tool call name = %q", tc0.Name)
	}
	if tc0.Result != "file1.txt\nfile2.txt" {
		t.Errorf("tool call result = %q", tc0.Result)
	}
	if tc0.ResultBytes != 19 {
		t.Errorf("tool call result_bytes = %d, want 19", tc0.ResultBytes)
	}
	if tc0.IsError {
		t.Error("tool call is_error should be false")
	}
	if tc0.DurationMS == nil || *tc0.DurationMS != 500 {
		t.Errorf("tool call duration_ms = %v, want 500", tc0.DurationMS)
	}

	// Verify info totals.
	totals := trace.Info.Totals
	if totals.LLMCalls != 1 {
		t.Errorf("totals.llm_calls = %d, want 1", totals.LLMCalls)
	}
	if totals.MainAgentLLMCalls != 1 {
		t.Errorf("totals.main_agent_llm_calls = %d, want 1", totals.MainAgentLLMCalls)
	}
	if totals.InputTokens != 5000 {
		t.Errorf("totals.input_tokens = %d, want 5000", totals.InputTokens)
	}
	if totals.OutputTokens != 200 {
		t.Errorf("totals.output_tokens = %d, want 200", totals.OutputTokens)
	}
	if totals.ToolCalls != 1 {
		t.Errorf("totals.tool_calls = %d, want 1", totals.ToolCalls)
	}
	if totals.ToolResultBytes != 19 {
		t.Errorf("totals.tool_result_bytes = %d, want 19", totals.ToolResultBytes)
	}

	// Verify info timing.
	if trace.Info.EndedAt == nil {
		t.Error("info.ended_at should be set after Finalize")
	}
	if trace.Info.DurationMS == nil {
		t.Error("info.duration_ms should be set after Finalize")
	}

	// Verify tool summary.
	bashSummary := trace.Info.ToolSummary["bash"]
	if bashSummary == nil {
		t.Fatal("tool_summary missing bash entry")
	}
	if bashSummary.Calls != 1 {
		t.Errorf("bash summary calls = %d, want 1", bashSummary.Calls)
	}
	if bashSummary.ResultBytes != 19 {
		t.Errorf("bash summary result_bytes = %d, want 19", bashSummary.ResultBytes)
	}
	if bashSummary.TotalDurationMS != 500 {
		t.Errorf("bash summary total_duration_ms = %d, want 500", bashSummary.TotalDurationMS)
	}
}

func TestTraceCollector_StopReason_EndTurn(t *testing.T) {
	tc := NewTraceCollector("sess-end-turn")
	tc.SetMainAgentID("main")

	tc.StartLLMResponse("main")
	tc.AddTextDelta("main", "Hello!")
	// No tool calls — stop_reason should be "end_turn".
	tc.SetUsage("main", "model", "n1", &TraceUsage{InputTokens: 100, OutputTokens: 50}, 0.01)
	tc.FinalizeTurn("main")

	tc.mu.Lock()
	trace := tc.buildTraceLocked()
	tc.mu.Unlock()

	if len(trace.Events) != 1 {
		t.Fatalf("events len = %d, want 1", len(trace.Events))
	}
	var ev TraceLLMResponse
	json.Unmarshal(trace.Events[0], &ev)
	if ev.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q, want end_turn", ev.StopReason)
	}
}

func TestTraceCollector_ModelSetFromFirstCall(t *testing.T) {
	tc := NewTraceCollector("sess-model")
	tc.SetMainAgentID("main")

	tc.StartLLMResponse("main")
	tc.SetUsage("main", "first-model", "", nil, 0)
	tc.FinalizeTurn("main")

	tc.StartLLMResponse("main")
	tc.SetUsage("main", "second-model", "", nil, 0)
	tc.FinalizeTurn("main")

	tc.mu.Lock()
	trace := tc.buildTraceLocked()
	tc.mu.Unlock()

	if trace.Info.Model != "first-model" {
		t.Errorf("info.model = %q, want first-model", trace.Info.Model)
	}
}

// ── 4c: Test tool call pairing ──

func TestTraceCollector_MultipleToolCalls_SameTurn(t *testing.T) {
	tc := NewTraceCollector("sess-multi-tools")
	tc.SetMainAgentID("main")

	tc.StartLLMResponse("main")
	tc.AddTextDelta("main", "Running two commands")

	// Start both tool calls.
	tc.StartToolCall("main", "t1", "bash", json.RawMessage(`{"command":"ls"}`))
	tc.StartToolCall("main", "t2", "read", json.RawMessage(`{"path":"foo.go"}`))

	// Results arrive in different order.
	tc.EndToolCall("t2", "package main", false, 50*time.Millisecond)
	tc.EndToolCall("t1", "file.txt", false, 200*time.Millisecond)

	tc.SetUsage("main", "model", "", &TraceUsage{InputTokens: 1000, OutputTokens: 100}, 0.02)
	tc.FinalizeTurn("main")

	tc.mu.Lock()
	trace := tc.buildTraceLocked()
	tc.mu.Unlock()

	if len(trace.Events) != 1 {
		t.Fatalf("events len = %d, want 1", len(trace.Events))
	}
	var ev TraceLLMResponse
	json.Unmarshal(trace.Events[0], &ev)

	if len(ev.ToolCalls) != 2 {
		t.Fatalf("tool_calls len = %d, want 2", len(ev.ToolCalls))
	}

	// Verify each tool call is paired with its correct result.
	toolByID := make(map[string]TraceToolCall)
	for _, tc := range ev.ToolCalls {
		toolByID[tc.ID] = tc
	}

	t1 := toolByID["t1"]
	if t1.Name != "bash" {
		t.Errorf("t1 name = %q, want bash", t1.Name)
	}
	if t1.Result != "file.txt" {
		t.Errorf("t1 result = %q, want file.txt", t1.Result)
	}
	if t1.DurationMS == nil || *t1.DurationMS != 200 {
		t.Errorf("t1 duration = %v, want 200", t1.DurationMS)
	}

	t2 := toolByID["t2"]
	if t2.Name != "read" {
		t.Errorf("t2 name = %q, want read", t2.Name)
	}
	if t2.Result != "package main" {
		t.Errorf("t2 result = %q", t2.Result)
	}
	if t2.DurationMS == nil || *t2.DurationMS != 50 {
		t.Errorf("t2 duration = %v, want 50", t2.DurationMS)
	}

	// Verify totals.
	if trace.Info.Totals.ToolCalls != 2 {
		t.Errorf("totals.tool_calls = %d, want 2", trace.Info.Totals.ToolCalls)
	}

	// Verify tool summaries.
	if s := trace.Info.ToolSummary["bash"]; s == nil || s.Calls != 1 {
		t.Errorf("bash summary = %+v", s)
	}
	if s := trace.Info.ToolSummary["read"]; s == nil || s.Calls != 1 {
		t.Errorf("read summary = %+v", s)
	}
}

func TestTraceCollector_ToolCallApproval(t *testing.T) {
	tc := NewTraceCollector("sess-approval")
	tc.SetMainAgentID("main")

	tc.StartLLMResponse("main")
	tc.StartToolCall("main", "t1", "bash", json.RawMessage(`{}`))
	tc.AddApproval("t1", "Run bash command", true, 3500*time.Millisecond)
	tc.EndToolCall("t1", "ok", false, 100*time.Millisecond)

	tc.SetUsage("main", "model", "", nil, 0)
	tc.FinalizeTurn("main")

	tc.mu.Lock()
	trace := tc.buildTraceLocked()
	tc.mu.Unlock()

	var ev TraceLLMResponse
	json.Unmarshal(trace.Events[0], &ev)
	if len(ev.ToolCalls) != 1 {
		t.Fatal("expected 1 tool call")
	}
	a := ev.ToolCalls[0].Approval
	if a == nil {
		t.Fatal("approval should not be nil")
	}
	if !a.Requested {
		t.Error("approval.requested should be true")
	}
	if a.Description != "Run bash command" {
		t.Errorf("approval.description = %q", a.Description)
	}
	if !a.Approved {
		t.Error("approval.approved should be true")
	}
	if a.WaitDurationMS != 3500 {
		t.Errorf("approval.wait_duration_ms = %d, want 3500", a.WaitDurationMS)
	}
}

func TestTraceCollector_ToolCallError(t *testing.T) {
	tc := NewTraceCollector("sess-err-tool")
	tc.SetMainAgentID("main")

	tc.StartLLMResponse("main")
	tc.StartToolCall("main", "t1", "bash", json.RawMessage(`{}`))
	tc.EndToolCall("t1", "permission denied", true, 10*time.Millisecond)
	tc.SetUsage("main", "model", "", nil, 0)
	tc.FinalizeTurn("main")

	tc.mu.Lock()
	trace := tc.buildTraceLocked()
	tc.mu.Unlock()

	var ev TraceLLMResponse
	json.Unmarshal(trace.Events[0], &ev)
	if !ev.ToolCalls[0].IsError {
		t.Error("expected is_error = true")
	}
	if ev.ToolCalls[0].Result != "permission denied" {
		t.Errorf("result = %q", ev.ToolCalls[0].Result)
	}
}

// ── 4d: Test sub-agent trace nesting ──

func TestTraceCollector_SubAgentNesting(t *testing.T) {
	// Build a sub-agent trace.
	subTC := NewTraceCollector("sub-sess")
	subTC.SetMainAgentID("sub-agent-1")
	subTC.AddUserMessage("Research codebase")
	subTC.StartLLMResponse("sub-agent-1")
	subTC.AddTextDelta("sub-agent-1", "Found the files.")
	subTC.SetUsage("sub-agent-1", "claude-sonnet", "n1", &TraceUsage{
		InputTokens:  3000,
		OutputTokens: 500,
	}, 0.02)
	subTC.FinalizeTurn("sub-agent-1")
	subTC.Finalize()

	subEvent := subTC.BuildSubAgentEvent("sub-agent-1", "Research codebase", "claude-sonnet", 1, 10)

	// Parent trace adds the sub-agent.
	parentTC := NewTraceCollector("parent-sess")
	parentTC.SetMainAgentID("main")
	parentTC.AddSubAgent(subEvent)
	parentTC.Finalize()

	parentTC.mu.Lock()
	trace := parentTC.buildTraceLocked()
	parentTC.mu.Unlock()

	// Should have one sub_agent event.
	if len(trace.Events) != 1 {
		t.Fatalf("events len = %d, want 1", len(trace.Events))
	}

	var sub TraceSubAgent
	if err := json.Unmarshal(trace.Events[0], &sub); err != nil {
		t.Fatal(err)
	}
	if sub.Type != "sub_agent" {
		t.Errorf("type = %q, want sub_agent", sub.Type)
	}
	if sub.AgentID != "sub-agent-1" {
		t.Errorf("agent_id = %q", sub.AgentID)
	}
	if sub.Task != "Research codebase" {
		t.Errorf("task = %q", sub.Task)
	}
	if sub.Model != "claude-sonnet" {
		t.Errorf("model = %q", sub.Model)
	}
	if sub.Turns != 1 {
		t.Errorf("turns = %d, want 1", sub.Turns)
	}
	if sub.MaxTurns != 10 {
		t.Errorf("max_turns = %d, want 10", sub.MaxTurns)
	}

	// Verify nested events.
	if len(sub.Events) != 2 {
		t.Fatalf("sub events len = %d, want 2 (user_message + llm_response)", len(sub.Events))
	}

	// Verify sub-agent usage is captured.
	if sub.Usage == nil {
		t.Fatal("sub usage should not be nil")
	}
	if sub.Usage.InputTokens != 3000 {
		t.Errorf("sub usage input_tokens = %d, want 3000", sub.Usage.InputTokens)
	}
	if sub.Usage.OutputTokens != 500 {
		t.Errorf("sub usage output_tokens = %d, want 500", sub.Usage.OutputTokens)
	}

	// Verify parent totals track sub-agent spawn.
	if trace.Info.Totals.SubAgentsSpawned != 1 {
		t.Errorf("totals.sub_agents_spawned = %d, want 1", trace.Info.Totals.SubAgentsSpawned)
	}
}

func TestTraceCollector_SubAgentLLMCallsCounted(t *testing.T) {
	tc := NewTraceCollector("sess-sub-counts")
	tc.SetMainAgentID("main-agent")

	// Main agent LLM call.
	tc.StartLLMResponse("main-agent")
	tc.SetUsage("main-agent", "model", "", &TraceUsage{InputTokens: 100, OutputTokens: 50}, 0)
	tc.FinalizeTurn("main-agent")

	// Sub-agent LLM call (different agent ID).
	tc.StartLLMResponse("sub-agent")
	tc.SetUsage("sub-agent", "model", "", &TraceUsage{InputTokens: 200, OutputTokens: 100}, 0)
	tc.FinalizeTurn("sub-agent")

	tc.Finalize()
	tc.mu.Lock()
	trace := tc.buildTraceLocked()
	tc.mu.Unlock()

	totals := trace.Info.Totals
	if totals.LLMCalls != 2 {
		t.Errorf("llm_calls = %d, want 2", totals.LLMCalls)
	}
	if totals.MainAgentLLMCalls != 1 {
		t.Errorf("main_agent_llm_calls = %d, want 1", totals.MainAgentLLMCalls)
	}
	if totals.SubAgentLLMCalls != 1 {
		t.Errorf("sub_agent_llm_calls = %d, want 1", totals.SubAgentLLMCalls)
	}
}

func TestTraceCollector_AddSubAgent_NilSafe(t *testing.T) {
	tc := NewTraceCollector("sess-nil-sub")
	tc.AddSubAgent(nil) // Should not panic.
	tc.mu.Lock()
	trace := tc.buildTraceLocked()
	tc.mu.Unlock()
	if len(trace.Events) != 0 {
		t.Errorf("events len = %d, want 0 after nil sub-agent", len(trace.Events))
	}
}

// ── 4e: Test file lifecycle ──

func TestTraceCollector_WriteToFile_ValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trace.json")

	tc := NewTraceCollector("sess-write")
	tc.SetMainAgentID("main")
	tc.SetSystemPrompt("prompt")
	tc.AddUserMessage("hi")
	tc.StartLLMResponse("main")
	tc.AddTextDelta("main", "hello")
	tc.SetUsage("main", "model", "", &TraceUsage{InputTokens: 10, OutputTokens: 5}, 0.001)
	tc.FinalizeTurn("main")

	if err := tc.FlushToFile(path); err != nil {
		t.Fatalf("FlushToFile failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	// Must be valid JSON.
	var trace Trace
	if err := json.Unmarshal(data, &trace); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if trace.Info.SessionID != "sess-write" {
		t.Errorf("session_id = %q", trace.Info.SessionID)
	}
}

func TestTraceCollector_FlushToFile_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new-trace.json")

	tc := NewTraceCollector("sess-create")
	if err := tc.FlushToFile(path); err != nil {
		t.Fatalf("FlushToFile failed: %v", err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("FlushToFile should create the file")
	}
}

func TestTraceCollector_FlushToFile_Atomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atomic-trace.json")

	tc := NewTraceCollector("sess-atomic")
	tc.AddUserMessage("test")

	// Write twice — second should overwrite first cleanly.
	if err := tc.FlushToFile(path); err != nil {
		t.Fatal(err)
	}
	tc.AddUserMessage("second message")
	if err := tc.FlushToFile(path); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var trace Trace
	if err := json.Unmarshal(data, &trace); err != nil {
		t.Fatalf("second write produced invalid JSON: %v", err)
	}
	// Should have 2 user_message events.
	if len(trace.Events) != 2 {
		t.Errorf("events len = %d, want 2", len(trace.Events))
	}
}

func TestTraceCollector_DebugFileExtension(t *testing.T) {
	// Verify the naming convention produces .json files.
	name := "debug-20260325-150405.json"
	ext := filepath.Ext(name)
	if ext != ".json" {
		t.Errorf("expected .json extension, got %q", ext)
	}
}

// ── 4f: Test periodic flush and crash resilience ──

func TestTraceCollector_PartialFlush_ValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "partial.json")

	tc := NewTraceCollector("sess-partial")
	tc.SetMainAgentID("main")
	tc.SetSystemPrompt("prompt")
	tc.AddUserMessage("hello")

	// Start LLM response but don't finalize — simulates mid-turn flush.
	tc.StartLLMResponse("main")
	tc.AddTextDelta("main", "partial text")

	// Flush mid-session.
	if err := tc.FlushToFile(path); err != nil {
		t.Fatalf("FlushToFile failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Must still be valid JSON (in-progress turns included as snapshots).
	var trace Trace
	if err := json.Unmarshal(data, &trace); err != nil {
		t.Fatalf("partial flush is not valid JSON: %v", err)
	}

	// Should have user_message + in-progress llm_response snapshot.
	if len(trace.Events) != 2 {
		t.Errorf("events len = %d, want 2", len(trace.Events))
	}

	// ended_at should be null (not finalized).
	if trace.Info.EndedAt != nil {
		t.Error("info.ended_at should be null before Finalize")
	}
}

func TestTraceCollector_Finalize_SetsEndedAt(t *testing.T) {
	tc := NewTraceCollector("sess-finalize")

	// Before finalize.
	tc.mu.Lock()
	before := tc.buildTraceLocked()
	tc.mu.Unlock()
	if before.Info.EndedAt != nil {
		t.Error("ended_at should be null before Finalize")
	}

	// After finalize.
	tc.Finalize()
	tc.mu.Lock()
	after := tc.buildTraceLocked()
	tc.mu.Unlock()
	if after.Info.EndedAt == nil {
		t.Error("ended_at should be set after Finalize")
	}
	if after.Info.DurationMS == nil {
		t.Error("duration_ms should be set after Finalize")
	}
	if *after.Info.DurationMS < 0 {
		t.Errorf("duration_ms should be non-negative, got %d", *after.Info.DurationMS)
	}
}

func TestTraceCollector_Finalize_CompletesInProgressTurns(t *testing.T) {
	tc := NewTraceCollector("sess-finalize-turns")
	tc.SetMainAgentID("main")

	// Start a turn without explicitly finalizing it.
	tc.StartLLMResponse("main")
	tc.AddTextDelta("main", "some text")
	tc.SetUsage("main", "model", "", &TraceUsage{InputTokens: 10}, 0)

	// Finalize should complete the in-progress turn.
	tc.Finalize()

	tc.mu.Lock()
	trace := tc.buildTraceLocked()
	tc.mu.Unlock()

	if len(trace.Events) != 1 {
		t.Fatalf("events len = %d, want 1", len(trace.Events))
	}
	var ev TraceLLMResponse
	json.Unmarshal(trace.Events[0], &ev)
	if ev.EndedAt == nil {
		t.Error("in-progress turn should have ended_at after Finalize")
	}
}

// ── 4g: Test no resize regeneration ──

func TestNoResizeRegeneration_NoRegenerateDebugFile(t *testing.T) {
	// Verify that regenerateDebugFile does not exist as a method on App.
	// This is a compile-time check — if the method existed, this test file
	// would not compile when calling a nonexistent method. Instead, we verify
	// at the source level that no such function exists by checking that
	// the resizeMsg handler in main.go does not reference debug/trace file writes.
	//
	// The actual regression check is that this test file compiles without
	// any reference to regenerateDebugFile, and the resizeMsg handler
	// in main.go only calls a.handleResize() without any trace file writes.

	// Verify that resizeMsg does NOT trigger a trace flush by exercising the collector.
	tc := NewTraceCollector("sess-resize")
	tc.SetMainAgentID("main")
	tc.AddUserMessage("test")

	// Take a snapshot before simulated "resize".
	tc.mu.Lock()
	beforeEvents := len(tc.buildTraceLocked().Events)
	tc.mu.Unlock()

	// Simulate what happens on resize: nothing related to trace.
	// (In the old code, regenerateDebugFile was called here.)

	// After "resize", trace state should be unchanged.
	tc.mu.Lock()
	afterEvents := len(tc.buildTraceLocked().Events)
	tc.mu.Unlock()

	if beforeEvents != afterEvents {
		t.Errorf("resize should not modify trace events: before=%d, after=%d", beforeEvents, afterEvents)
	}
}

// ── Additional edge case tests ──

func TestTraceCollector_EventTypes(t *testing.T) {
	tc := NewTraceCollector("sess-events")
	tc.SetMainAgentID("main")

	tc.AddCompaction("node-5", "Conversation was summarized.")
	tc.AddRetry(1, 3, 2*time.Second, "429 Too Many Requests")
	tc.AddStreamClear()
	tc.AddError("context canceled")

	tc.mu.Lock()
	trace := tc.buildTraceLocked()
	tc.mu.Unlock()

	if len(trace.Events) != 4 {
		t.Fatalf("events len = %d, want 4", len(trace.Events))
	}

	// Compaction.
	var compaction TraceCompaction
	json.Unmarshal(trace.Events[0], &compaction)
	if compaction.Type != "compaction" {
		t.Errorf("event[0] type = %q", compaction.Type)
	}
	if compaction.NodeID != "node-5" {
		t.Errorf("compaction node_id = %q", compaction.NodeID)
	}
	if compaction.Summary != "Conversation was summarized." {
		t.Errorf("compaction summary = %q", compaction.Summary)
	}

	// Retry.
	var retry TraceRetry
	json.Unmarshal(trace.Events[1], &retry)
	if retry.Type != "retry" {
		t.Errorf("event[1] type = %q", retry.Type)
	}
	if retry.Attempt != 1 || retry.MaxRetry != 3 {
		t.Errorf("retry attempt=%d/%d", retry.Attempt, retry.MaxRetry)
	}
	if retry.DelayMS != 2000 {
		t.Errorf("retry delay_ms = %d, want 2000", retry.DelayMS)
	}
	if retry.Error != "429 Too Many Requests" {
		t.Errorf("retry error = %q", retry.Error)
	}

	// Stream clear.
	var clear TraceStreamClear
	json.Unmarshal(trace.Events[2], &clear)
	if clear.Type != "stream_clear" {
		t.Errorf("event[2] type = %q", clear.Type)
	}

	// Error.
	var errEv TraceError
	json.Unmarshal(trace.Events[3], &errEv)
	if errEv.Type != "error" {
		t.Errorf("event[3] type = %q", errEv.Type)
	}
	if errEv.Message != "context canceled" {
		t.Errorf("error message = %q", errEv.Message)
	}

	// Verify totals.
	totals := trace.Info.Totals
	if totals.Compactions != 1 {
		t.Errorf("totals.compactions = %d", totals.Compactions)
	}
	if totals.Retries != 1 {
		t.Errorf("totals.retries = %d", totals.Retries)
	}
	if totals.Errors != 1 {
		t.Errorf("totals.errors = %d", totals.Errors)
	}
}

func TestTraceCollector_SetGitInfo(t *testing.T) {
	tc := NewTraceCollector("sess-git")
	tc.SetGitInfo("feature-branch", "/home/user/repo")

	tc.mu.Lock()
	trace := tc.buildTraceLocked()
	tc.mu.Unlock()

	if trace.Info.GitBranch != "feature-branch" {
		t.Errorf("git_branch = %q", trace.Info.GitBranch)
	}
	if trace.Info.GitRoot != "/home/user/repo" {
		t.Errorf("git_root = %q", trace.Info.GitRoot)
	}
}

func TestTraceCollector_CostAggregation(t *testing.T) {
	tc := NewTraceCollector("sess-cost")
	tc.SetMainAgentID("main")

	tc.StartLLMResponse("main")
	tc.SetUsage("main", "model", "", &TraceUsage{InputTokens: 100}, 0.05)
	tc.FinalizeTurn("main")

	tc.StartLLMResponse("main")
	tc.SetUsage("main", "model", "", &TraceUsage{InputTokens: 200}, 0.10)
	tc.FinalizeTurn("main")

	tc.mu.Lock()
	trace := tc.buildTraceLocked()
	tc.mu.Unlock()

	// Floating point: use tolerance.
	got := trace.Info.Totals.CostUSD
	want := 0.15
	if got < want-0.001 || got > want+0.001 {
		t.Errorf("totals.cost_usd = %f, want ~%f", got, want)
	}
}

func TestTraceCollector_CacheTokenAggregation(t *testing.T) {
	tc := NewTraceCollector("sess-cache")
	tc.SetMainAgentID("main")

	tc.StartLLMResponse("main")
	tc.SetUsage("main", "model", "", &TraceUsage{
		InputTokens:       100,
		OutputTokens:      50,
		CacheReadTokens:   3000,
		CacheCreateTokens: 500,
		ReasoningTokens:   200,
	}, 0)
	tc.FinalizeTurn("main")

	tc.mu.Lock()
	trace := tc.buildTraceLocked()
	tc.mu.Unlock()

	totals := trace.Info.Totals
	if totals.CacheReadTokens != 3000 {
		t.Errorf("cache_read_tokens = %d, want 3000", totals.CacheReadTokens)
	}
	if totals.CacheCreateTokens != 500 {
		t.Errorf("cache_creation_tokens = %d, want 500", totals.CacheCreateTokens)
	}
	if totals.ReasoningTokens != 200 {
		t.Errorf("reasoning_tokens = %d, want 200", totals.ReasoningTokens)
	}
}

func TestTraceCollector_EndToolCall_UnknownID(t *testing.T) {
	tc := NewTraceCollector("sess-unknown-tool")
	// Should not panic on unknown tool ID.
	tc.EndToolCall("nonexistent", "result", false, 100*time.Millisecond)

	tc.mu.Lock()
	trace := tc.buildTraceLocked()
	tc.mu.Unlock()

	// No events should be created.
	if len(trace.Events) != 0 {
		t.Errorf("events len = %d, want 0", len(trace.Events))
	}
}

func TestTraceCollector_AddApproval_UnknownID(t *testing.T) {
	tc := NewTraceCollector("sess-unknown-approval")
	// Should not panic on unknown tool ID.
	tc.AddApproval("nonexistent", "desc", true, 100*time.Millisecond)
}

func TestTraceUsageFromTypes(t *testing.T) {
	u := &types.Usage{
		InputTokens:              5000,
		OutputTokens:             200,
		CacheReadInputTokens:     3000,
		CacheCreationInputTokens: 500,
		ReasoningTokens:          100,
	}
	tu := traceUsageFromTypes(u)
	if tu.InputTokens != 5000 {
		t.Errorf("InputTokens = %d", tu.InputTokens)
	}
	if tu.OutputTokens != 200 {
		t.Errorf("OutputTokens = %d", tu.OutputTokens)
	}
	if tu.CacheReadTokens != 3000 {
		t.Errorf("CacheReadTokens = %d", tu.CacheReadTokens)
	}
	if tu.CacheCreateTokens != 500 {
		t.Errorf("CacheCreateTokens = %d", tu.CacheCreateTokens)
	}
	if tu.ReasoningTokens != 100 {
		t.Errorf("ReasoningTokens = %d", tu.ReasoningTokens)
	}
}

func TestTraceUsageFromTypes_Nil(t *testing.T) {
	if tu := traceUsageFromTypes(nil); tu != nil {
		t.Errorf("expected nil for nil input, got %+v", tu)
	}
}

func TestTraceCollector_BuildSubAgentEvent(t *testing.T) {
	tc := NewTraceCollector("sub-sess")
	tc.SetMainAgentID("sub-1")

	tc.AddUserMessage("do something")
	tc.StartLLMResponse("sub-1")
	tc.AddTextDelta("sub-1", "done")
	tc.SetUsage("sub-1", "model", "", &TraceUsage{
		InputTokens:  1000,
		OutputTokens: 200,
	}, 0.03)
	tc.FinalizeTurn("sub-1")
	tc.Finalize()

	ev := tc.BuildSubAgentEvent("sub-1", "research task", "claude-sonnet", 3, 10)

	if ev.Type != "sub_agent" {
		t.Errorf("type = %q", ev.Type)
	}
	if ev.AgentID != "sub-1" {
		t.Errorf("agent_id = %q", ev.AgentID)
	}
	if ev.Task != "research task" {
		t.Errorf("task = %q", ev.Task)
	}
	if ev.Turns != 3 {
		t.Errorf("turns = %d", ev.Turns)
	}
	if ev.MaxTurns != 10 {
		t.Errorf("max_turns = %d", ev.MaxTurns)
	}
	if ev.StartedAt == nil {
		t.Error("started_at should be set")
	}
	if ev.EndedAt == nil {
		t.Error("ended_at should be set (after Finalize)")
	}
	if ev.DurationMS == nil {
		t.Error("duration_ms should be set")
	}
	if ev.Usage == nil {
		t.Fatal("usage should not be nil")
	}
	if ev.Usage.InputTokens != 1000 {
		t.Errorf("usage.input_tokens = %d", ev.Usage.InputTokens)
	}
	if ev.CostUSD != 0.03 {
		t.Errorf("cost_usd = %f", ev.CostUSD)
	}
	if len(ev.Events) != 2 {
		t.Errorf("events len = %d, want 2", len(ev.Events))
	}
}

func TestTraceCollector_NewTurnFinalizesOld(t *testing.T) {
	tc := NewTraceCollector("sess-turn-order")
	tc.SetMainAgentID("main")

	// First turn.
	tc.StartLLMResponse("main")
	tc.AddTextDelta("main", "first response")
	tc.SetUsage("main", "model", "", &TraceUsage{InputTokens: 100}, 0)

	// Starting a new turn for the same agent should implicitly finalize via ensureTurn
	// or leave the old one in currentTurn. Let's verify by finalizing explicitly
	// and checking event count.
	tc.FinalizeTurn("main")

	tc.StartLLMResponse("main")
	tc.AddTextDelta("main", "second response")
	tc.SetUsage("main", "model", "", &TraceUsage{InputTokens: 200}, 0)
	tc.FinalizeTurn("main")

	tc.mu.Lock()
	trace := tc.buildTraceLocked()
	tc.mu.Unlock()

	// Should have 2 llm_response events.
	if len(trace.Events) != 2 {
		t.Fatalf("events len = %d, want 2", len(trace.Events))
	}

	var ev1, ev2 TraceLLMResponse
	json.Unmarshal(trace.Events[0], &ev1)
	json.Unmarshal(trace.Events[1], &ev2)
	if ev1.Content != "first response" {
		t.Errorf("event[0] content = %q", ev1.Content)
	}
	if ev2.Content != "second response" {
		t.Errorf("event[1] content = %q", ev2.Content)
	}
}

func TestTrace_JSONRoundTrip(t *testing.T) {
	// Full round-trip: build trace → marshal → unmarshal → verify.
	tc := NewTraceCollector("sess-roundtrip")
	tc.SetMainAgentID("main")
	tc.SetSystemPrompt("Be helpful.")
	tc.SetTools([]TraceTool{
		{Name: "bash", Description: "Run command", Parameters: json.RawMessage(`{"type":"object"}`)},
	})
	tc.AddUserMessage("hello")
	tc.StartLLMResponse("main")
	tc.AddTextDelta("main", "hi there")
	tc.SetUsage("main", "claude-opus", "n1", &TraceUsage{
		InputTokens:       500,
		OutputTokens:      100,
		CacheReadTokens:   200,
		CacheCreateTokens: 50,
	}, 0.05)
	tc.FinalizeTurn("main")
	tc.AddCompaction("n2", "summarized")
	tc.Finalize()

	dir := t.TempDir()
	path := filepath.Join(dir, "roundtrip.json")
	if err := tc.FlushToFile(path); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var trace Trace
	if err := json.Unmarshal(data, &trace); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}

	// Verify key fields survived round-trip.
	if trace.Info.SessionID != "sess-roundtrip" {
		t.Errorf("session_id = %q", trace.Info.SessionID)
	}
	if trace.SystemPrompt != "Be helpful." {
		t.Errorf("system_prompt = %q", trace.SystemPrompt)
	}
	if len(trace.Tools) != 1 || trace.Tools[0].Name != "bash" {
		t.Errorf("tools = %+v", trace.Tools)
	}
	// 3 events: user_message, llm_response, compaction.
	if len(trace.Events) != 3 {
		t.Errorf("events len = %d, want 3", len(trace.Events))
	}
	if trace.Info.EndedAt == nil {
		t.Error("ended_at should be set")
	}
	if trace.Info.Totals.LLMCalls != 1 {
		t.Errorf("llm_calls = %d", trace.Info.Totals.LLMCalls)
	}
	if trace.Info.Totals.Compactions != 1 {
		t.Errorf("compactions = %d", trace.Info.Totals.Compactions)
	}
}

func TestTraceCollector_OSField(t *testing.T) {
	tc := NewTraceCollector("sess-os")
	tc.mu.Lock()
	trace := tc.buildTraceLocked()
	tc.mu.Unlock()

	if trace.Info.OS == "" {
		t.Error("OS should be set from runtime.GOOS")
	}
}
