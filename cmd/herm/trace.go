// trace.go implements structured JSON trace logging for debug sessions.
// Each session produces a .json file containing every event: LLM calls with
// token/model metadata, tool calls paired with results, sub-agent traces
// nested hierarchically, and an info summary object.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"langdag.com/langdag/types"
)

// ── JSON trace schema structs ──

// Trace is the top-level JSON document written to the debug file.
type Trace struct {
	Info         *TraceInfo        `json:"info"`
	SystemPrompt string            `json:"system_prompt"`
	Tools        []TraceTool       `json:"tools"`
	Events       []json.RawMessage `json:"events"`
}

// TraceInfo holds session metadata and aggregate totals.
type TraceInfo struct {
	SessionID        string       `json:"session_id"`
	StartedAt        *time.Time   `json:"started_at"`
	EndedAt          *time.Time   `json:"ended_at"`
	DurationMS       *int64       `json:"duration_ms"`
	Model            string       `json:"model,omitempty"`
	SystemPromptHash string       `json:"system_prompt_hash,omitempty"`
	GitBranch        string       `json:"git_branch,omitempty"`
	GitRoot          string       `json:"git_root,omitempty"`
	OS               string       `json:"os"`
	Totals           TraceTotals  `json:"totals"`
	ToolSummary      map[string]*TraceToolSummary `json:"tool_summary,omitempty"`
}

// TraceTotals holds cumulative counters for the session.
type TraceTotals struct {
	LLMCalls          int     `json:"llm_calls"`
	MainAgentLLMCalls int     `json:"main_agent_llm_calls"`
	SubAgentLLMCalls  int     `json:"sub_agent_llm_calls"`
	InputTokens       int     `json:"input_tokens"`
	OutputTokens      int     `json:"output_tokens"`
	CacheReadTokens   int     `json:"cache_read_tokens"`
	CacheCreateTokens int     `json:"cache_creation_tokens"`
	ReasoningTokens   int     `json:"reasoning_tokens"`
	CostUSD           float64 `json:"cost_usd"`
	ToolCalls         int     `json:"tool_calls"`
	ToolResultBytes   int     `json:"tool_result_bytes"`
	SubAgentsSpawned  int     `json:"sub_agents_spawned"`
	Compactions       int     `json:"compactions"`
	Retries           int     `json:"retries"`
	Errors            int     `json:"errors"`
}

// TraceToolSummary holds per-tool aggregate stats.
type TraceToolSummary struct {
	Calls          int   `json:"calls"`
	ResultBytes    int   `json:"result_bytes"`
	TotalDurationMS int64 `json:"total_duration_ms"`
}

// TraceTool describes a tool available to the LLM.
type TraceTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ── Event types (discriminated union via "type" field) ──

// TraceUserMessage is a user_message event.
type TraceUserMessage struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Content   string    `json:"content"`
}

// TraceLLMResponse is an llm_response event grouping text, usage, and tool calls.
type TraceLLMResponse struct {
	Type       string           `json:"type"`
	AgentID    string           `json:"agent_id"`
	NodeID     string           `json:"node_id,omitempty"`
	StartedAt  time.Time        `json:"started_at"`
	EndedAt    *time.Time       `json:"ended_at,omitempty"`
	DurationMS *int64           `json:"duration_ms,omitempty"`
	Model      string           `json:"model,omitempty"`
	Content    string           `json:"content"`
	StopReason string           `json:"stop_reason,omitempty"`
	Usage      *TraceUsage      `json:"usage,omitempty"`
	CostUSD    float64          `json:"cost_usd,omitempty"`
	ToolCalls  []TraceToolCall  `json:"tool_calls,omitempty"`
}

// TraceUsage holds token counts for a single LLM call.
type TraceUsage struct {
	InputTokens       int `json:"input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	CacheReadTokens   int `json:"cache_read_tokens,omitempty"`
	CacheCreateTokens int `json:"cache_creation_tokens,omitempty"`
	ReasoningTokens   int `json:"reasoning_tokens,omitempty"`
}

// TraceToolCall pairs a tool invocation with its result.
type TraceToolCall struct {
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	Input         json.RawMessage  `json:"input"`
	ParallelGroup int              `json:"parallel_group"`
	StartedAt     *time.Time       `json:"started_at,omitempty"`
	EndedAt       *time.Time       `json:"ended_at,omitempty"`
	DurationMS    *int64           `json:"duration_ms,omitempty"`
	Result        string           `json:"result,omitempty"`
	ResultBytes   int              `json:"result_bytes,omitempty"`
	IsError       bool             `json:"is_error,omitempty"`
	Approval      *TraceApproval   `json:"approval,omitempty"`
}

// TraceApproval captures the tool approval flow.
type TraceApproval struct {
	Requested      bool   `json:"requested"`
	Description    string `json:"description,omitempty"`
	Approved       bool   `json:"approved"`
	WaitDurationMS int64  `json:"wait_duration_ms,omitempty"`
}

// TraceSubAgent is a sub_agent event containing a nested trace.
type TraceSubAgent struct {
	Type       string           `json:"type"`
	AgentID    string           `json:"agent_id"`
	Task       string           `json:"task,omitempty"`
	Model      string           `json:"model,omitempty"`
	StartedAt  *time.Time       `json:"started_at,omitempty"`
	EndedAt    *time.Time       `json:"ended_at,omitempty"`
	DurationMS *int64           `json:"duration_ms,omitempty"`
	Usage      *TraceUsage      `json:"usage,omitempty"`
	CostUSD    float64          `json:"cost_usd,omitempty"`
	Turns      int              `json:"turns,omitempty"`
	MaxTurns   int              `json:"max_turns,omitempty"`
	Events     []json.RawMessage `json:"events,omitempty"`
}

// TraceCompaction is a compaction event.
type TraceCompaction struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	NodeID    string    `json:"node_id,omitempty"`
	Summary   string    `json:"summary,omitempty"`
}

// TraceRetry is a retry event.
type TraceRetry struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Attempt   int       `json:"attempt"`
	MaxRetry  int       `json:"max_attempts"`
	DelayMS   int64     `json:"delay_ms"`
	Error     string    `json:"error,omitempty"`
}

// TraceStreamClear is a stream_clear event.
type TraceStreamClear struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
}

// TraceError is an error event.
type TraceError struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
}

// ── TraceCollector: in-memory trace builder ──

// TraceCollector accumulates trace events and builds the Trace structure.
// It is not safe for concurrent use from multiple goroutines without external
// synchronization (the App's event loop is single-threaded).
type TraceCollector struct {
	mu sync.Mutex

	info         TraceInfo
	systemPrompt string
	tools        []TraceTool
	events       []json.RawMessage

	// Current LLM response being accumulated per agent.
	currentTurn map[string]*TraceLLMResponse

	// Pending tool calls keyed by tool ID, for pairing with results.
	pendingTools map[string]*TraceToolCall

	// Which agent's turn owns which pending tool.
	toolAgent map[string]string

	// Track main agent ID for distinguishing main vs sub-agent LLM calls.
	mainAgentID string

	// Monotonic counter for parallel_group — incremented each new turn.
	parallelGroupSeq int
}

// NewTraceCollector creates a new trace collector.
func NewTraceCollector(sessionID string) *TraceCollector {
	return &TraceCollector{
		info: TraceInfo{
			SessionID:   sessionID,
			OS:          runtime.GOOS,
			ToolSummary: make(map[string]*TraceToolSummary),
		},
		currentTurn:  make(map[string]*TraceLLMResponse),
		pendingTools: make(map[string]*TraceToolCall),
		toolAgent:    make(map[string]string),
	}
}

// SetGitInfo sets git branch and root in the trace info.
func (tc *TraceCollector) SetGitInfo(branch, root string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.info.GitBranch = branch
	tc.info.GitRoot = root
}

// SetMainAgentID sets the main agent ID for distinguishing main vs sub-agent calls.
func (tc *TraceCollector) SetMainAgentID(id string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.mainAgentID = id
}

// SetSystemPrompt records the system prompt and its SHA256 hash.
func (tc *TraceCollector) SetSystemPrompt(prompt string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.systemPrompt = prompt
	tc.info.SystemPromptHash = fmt.Sprintf("%x", sha256.Sum256([]byte(prompt)))
}

// SetTools records the tool definitions.
func (tc *TraceCollector) SetTools(tools []TraceTool) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.tools = tools
}

// AddUserMessage appends a user_message event.
func (tc *TraceCollector) AddUserMessage(content string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	now := time.Now()
	if tc.info.StartedAt == nil {
		tc.info.StartedAt = &now
	}
	ev := TraceUserMessage{
		Type:      "user_message",
		Timestamp: now,
		Content:   content,
	}
	tc.appendEvent(ev)
}

// StartLLMResponse begins accumulating a new LLM response turn for an agent.
func (tc *TraceCollector) StartLLMResponse(agentID string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.ensureTurn(agentID)
}

// AddTextDelta appends streaming text to the current LLM response.
func (tc *TraceCollector) AddTextDelta(agentID, text string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	turn := tc.ensureTurn(agentID)
	turn.Content += text
}

// SetUsage finalizes the current LLM response with usage metadata.
// This also updates the info totals.
func (tc *TraceCollector) SetUsage(agentID, model, nodeID string, usage *TraceUsage, costUSD float64) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	turn := tc.ensureTurn(agentID)
	turn.Model = model
	turn.NodeID = nodeID
	turn.Usage = usage
	turn.CostUSD = costUSD

	if tc.info.Model == "" {
		tc.info.Model = model
	}

	// Update totals.
	tc.info.Totals.LLMCalls++
	if agentID == tc.mainAgentID {
		tc.info.Totals.MainAgentLLMCalls++
	} else {
		tc.info.Totals.SubAgentLLMCalls++
	}
	if usage != nil {
		tc.info.Totals.InputTokens += usage.InputTokens
		tc.info.Totals.OutputTokens += usage.OutputTokens
		tc.info.Totals.CacheReadTokens += usage.CacheReadTokens
		tc.info.Totals.CacheCreateTokens += usage.CacheCreateTokens
		tc.info.Totals.ReasoningTokens += usage.ReasoningTokens
	}
	tc.info.Totals.CostUSD += costUSD

	// Note: we don't finalize the turn here because tool calls may follow.
	// The turn is finalized when the next turn starts or on Finalize().
}

// StartToolCall records a tool call starting within the current LLM response.
func (tc *TraceCollector) StartToolCall(agentID, toolID, toolName string, input json.RawMessage) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	now := time.Now()
	tc.ensureTurn(agentID)

	call := &TraceToolCall{
		ID:            toolID,
		Name:          toolName,
		Input:         input,
		ParallelGroup: tc.parallelGroupSeq,
		StartedAt:     &now,
	}
	tc.pendingTools[toolID] = call
	tc.toolAgent[toolID] = agentID
	tc.info.Totals.ToolCalls++
}

// EndToolCall records a tool call result.
func (tc *TraceCollector) EndToolCall(toolID, result string, isError bool, duration time.Duration) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	call, ok := tc.pendingTools[toolID]
	if !ok {
		return
	}
	now := time.Now()
	durMS := duration.Milliseconds()
	call.EndedAt = &now
	call.DurationMS = &durMS
	call.Result = result
	call.ResultBytes = len(result)
	call.IsError = isError

	// Attach to the agent's current turn.
	agentID := tc.toolAgent[toolID]
	if turn, exists := tc.currentTurn[agentID]; exists {
		turn.ToolCalls = append(turn.ToolCalls, *call)
	}

	// Update per-tool summary.
	ts := tc.info.ToolSummary[call.Name]
	if ts == nil {
		ts = &TraceToolSummary{}
		tc.info.ToolSummary[call.Name] = ts
	}
	ts.Calls++
	ts.ResultBytes += len(result)
	ts.TotalDurationMS += duration.Milliseconds()

	tc.info.Totals.ToolResultBytes += len(result)

	delete(tc.pendingTools, toolID)
	delete(tc.toolAgent, toolID)
}

// AddApproval records an approval request/response on a pending tool call.
func (tc *TraceCollector) AddApproval(toolID, desc string, approved bool, waitDur time.Duration) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	call, ok := tc.pendingTools[toolID]
	if !ok {
		return
	}
	call.Approval = &TraceApproval{
		Requested:      true,
		Description:    desc,
		Approved:       approved,
		WaitDurationMS: waitDur.Milliseconds(),
	}
}

// AddSubAgent attaches a completed sub-agent trace as a sub_agent event.
func (tc *TraceCollector) AddSubAgent(sub *TraceSubAgent) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if sub == nil {
		return
	}
	sub.Type = "sub_agent"
	tc.info.Totals.SubAgentsSpawned++
	tc.appendEvent(sub)
}

// AddCompaction appends a compaction event.
func (tc *TraceCollector) AddCompaction(nodeID, summary string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.info.Totals.Compactions++
	tc.appendEvent(TraceCompaction{
		Type:      "compaction",
		Timestamp: time.Now(),
		NodeID:    nodeID,
		Summary:   summary,
	})
}

// AddRetry appends a retry event.
func (tc *TraceCollector) AddRetry(attempt, maxAttempts int, delay time.Duration, errMsg string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.info.Totals.Retries++
	tc.appendEvent(TraceRetry{
		Type:      "retry",
		Timestamp: time.Now(),
		Attempt:   attempt,
		MaxRetry:  maxAttempts,
		DelayMS:   delay.Milliseconds(),
		Error:     errMsg,
	})
}

// AddStreamClear appends a stream_clear event.
func (tc *TraceCollector) AddStreamClear() {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.appendEvent(TraceStreamClear{
		Type:      "stream_clear",
		Timestamp: time.Now(),
	})
}

// AddError appends an error event.
func (tc *TraceCollector) AddError(msg string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.info.Totals.Errors++
	tc.appendEvent(TraceError{
		Type:      "error",
		Timestamp: time.Now(),
		Message:   msg,
	})
}

// FinalizeTurn completes the current LLM response for an agent and appends it
// to the events list. Called when a new turn starts or on session end.
func (tc *TraceCollector) FinalizeTurn(agentID string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.finalizeTurnLocked(agentID)
}

// Finalize completes the trace: sets ended_at, computes duration, and
// finalizes any in-progress turns.
func (tc *TraceCollector) Finalize() {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	now := time.Now()
	tc.info.EndedAt = &now
	if tc.info.StartedAt != nil {
		d := now.Sub(*tc.info.StartedAt).Milliseconds()
		tc.info.DurationMS = &d
	}
	// Finalize any in-progress turns.
	for agentID := range tc.currentTurn {
		tc.finalizeTurnLocked(agentID)
	}
}

// FlushToFile builds a snapshot of the current trace and writes it to path.
func (tc *TraceCollector) FlushToFile(path string) error {
	tc.mu.Lock()
	trace := tc.buildTraceLocked()
	tc.mu.Unlock()
	return writeTraceFile(path, trace)
}

// ── Internal helpers ──

// ensureTurn returns or creates the current LLM response turn for an agent.
// Must be called with tc.mu held.
func (tc *TraceCollector) ensureTurn(agentID string) *TraceLLMResponse {
	if turn, ok := tc.currentTurn[agentID]; ok {
		return turn
	}
	tc.parallelGroupSeq++
	turn := &TraceLLMResponse{
		Type:      "llm_response",
		AgentID:   agentID,
		StartedAt: time.Now(),
	}
	tc.currentTurn[agentID] = turn
	return turn
}

// finalizeTurnLocked completes an LLM response turn and appends it to events.
// Must be called with tc.mu held.
func (tc *TraceCollector) finalizeTurnLocked(agentID string) {
	turn, ok := tc.currentTurn[agentID]
	if !ok {
		return
	}
	// Set stop_reason based on final tool_calls state (deferred from SetUsage
	// so that tool results attached via EndToolCall are included).
	if len(turn.ToolCalls) > 0 {
		turn.StopReason = "tool_use"
	} else if turn.Usage != nil {
		turn.StopReason = "end_turn"
	}
	now := time.Now()
	turn.EndedAt = &now
	d := now.Sub(turn.StartedAt).Milliseconds()
	turn.DurationMS = &d
	tc.appendEvent(turn)
	delete(tc.currentTurn, agentID)
}

// appendEvent marshals an event and appends its JSON to the events slice.
// Must be called with tc.mu held.
func (tc *TraceCollector) appendEvent(ev any) {
	data, err := json.Marshal(ev)
	if err != nil {
		// Best effort — skip events that can't be marshaled.
		return
	}
	tc.events = append(tc.events, json.RawMessage(data))
}

// buildTraceLocked constructs a Trace snapshot from the current state.
// Must be called with tc.mu held.
func (tc *TraceCollector) buildTraceLocked() *Trace {
	info := tc.info // copy

	// Include in-progress turns in the events snapshot.
	events := make([]json.RawMessage, len(tc.events))
	copy(events, tc.events)
	for _, turn := range tc.currentTurn {
		// Snapshot the turn without finalizing it.
		snapshot := *turn
		data, err := json.Marshal(&snapshot)
		if err == nil {
			events = append(events, json.RawMessage(data))
		}
	}

	return &Trace{
		Info:         &info,
		SystemPrompt: tc.systemPrompt,
		Tools:        tc.tools,
		Events:       events,
	}
}

// traceUsageFromTypes converts a langdag types.Usage to a TraceUsage.
func traceUsageFromTypes(u *types.Usage) *TraceUsage {
	if u == nil {
		return nil
	}
	return &TraceUsage{
		InputTokens:       u.InputTokens,
		OutputTokens:      u.OutputTokens,
		CacheReadTokens:   u.CacheReadInputTokens,
		CacheCreateTokens: u.CacheCreationInputTokens,
		ReasoningTokens:   u.ReasoningTokens,
	}
}

// BuildSubAgentEvent constructs a TraceSubAgent from the collector's accumulated state.
// Call Finalize() before calling this to ensure timing and in-progress turns are captured.
func (tc *TraceCollector) BuildSubAgentEvent(agentID, task, model string, turns, maxTurns int) *TraceSubAgent {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	ev := &TraceSubAgent{
		Type:     "sub_agent",
		AgentID:  agentID,
		Task:     task,
		Model:    model,
		Turns:    turns,
		MaxTurns: maxTurns,
	}

	// Copy events.
	ev.Events = make([]json.RawMessage, len(tc.events))
	copy(ev.Events, tc.events)

	// Copy timing from info.
	ev.StartedAt = tc.info.StartedAt
	ev.EndedAt = tc.info.EndedAt
	if tc.info.DurationMS != nil {
		d := *tc.info.DurationMS
		ev.DurationMS = &d
	}

	// Aggregate usage from totals.
	ev.Usage = &TraceUsage{
		InputTokens:       tc.info.Totals.InputTokens,
		OutputTokens:      tc.info.Totals.OutputTokens,
		CacheReadTokens:   tc.info.Totals.CacheReadTokens,
		CacheCreateTokens: tc.info.Totals.CacheCreateTokens,
		ReasoningTokens:   tc.info.Totals.ReasoningTokens,
	}
	ev.CostUSD = tc.info.Totals.CostUSD

	return ev
}

// writeTraceFile atomically writes a Trace to a JSON file.
func writeTraceFile(path string, trace *Trace) error {
	data, err := json.MarshalIndent(trace, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling trace: %w", err)
	}
	data = append(data, '\n')

	// Write to temp file in the same directory, then rename for atomicity.
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".trace-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("writing trace: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming trace file: %w", err)
	}
	return nil
}
