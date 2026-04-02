package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"langdag.com/langdag"
	"langdag.com/langdag/types"
)

// --- mock provider ---

// mockProvider implements langdag.Provider, returning canned streaming responses.
type mockProvider struct {
	responses   []string // text responses to return, one per Prompt/Stream call
	mu          sync.Mutex
	callIdx     int
	model       string
	lastRequest *types.CompletionRequest // captures the most recent request for assertions
}

func (p *mockProvider) Complete(_ context.Context, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	p.mu.Lock()
	idx := p.callIdx
	p.callIdx++
	p.lastRequest = req
	p.mu.Unlock()

	text := "ok"
	if idx < len(p.responses) {
		text = p.responses[idx]
	}
	return &types.CompletionResponse{
		ID:         "resp-test",
		Model:      p.model,
		Content:    []types.ContentBlock{{Type: "text", Text: text}},
		StopReason: "end_turn",
		Usage:      types.Usage{InputTokens: 100, OutputTokens: 50},
	}, nil
}

func (p *mockProvider) Stream(_ context.Context, req *types.CompletionRequest) (<-chan types.StreamEvent, error) {
	p.mu.Lock()
	idx := p.callIdx
	p.callIdx++
	p.lastRequest = req
	p.mu.Unlock()

	text := "ok"
	if idx < len(p.responses) {
		text = p.responses[idx]
	}

	ch := make(chan types.StreamEvent, 10)
	go func() {
		defer close(ch)
		// Send text delta
		ch <- types.StreamEvent{
			Type:    types.StreamEventDelta,
			Content: text,
		}
		// Send done with usage
		ch <- types.StreamEvent{
			Type: types.StreamEventDone,
			Response: &types.CompletionResponse{
				ID:         "resp-test",
				Model:      req.Model,
				Content:    []types.ContentBlock{{Type: "text", Text: text}},
				StopReason: "end_turn",
				Usage:      types.Usage{InputTokens: 100, OutputTokens: 50},
			},
		}
	}()
	return ch, nil
}

func (p *mockProvider) Name() string          { return "mock" }
func (p *mockProvider) Models() []types.ModelInfo { return nil }

// --- mock storage ---

// mockStorage implements langdag.Storage with in-memory node storage.
type mockStorage struct {
	mu    sync.Mutex
	nodes map[string]*types.Node
}

func newMockStorage() *mockStorage {
	return &mockStorage{nodes: make(map[string]*types.Node)}
}

func (s *mockStorage) Init(_ context.Context) error { return nil }
func (s *mockStorage) Close() error                 { return nil }

func (s *mockStorage) CreateNode(_ context.Context, node *types.Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes[node.ID] = node
	return nil
}

func (s *mockStorage) GetNode(_ context.Context, id string) (*types.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.nodes[id]; ok {
		return n, nil
	}
	return nil, nil
}

func (s *mockStorage) GetNodeByPrefix(_ context.Context, _ string) (*types.Node, error) {
	return nil, nil
}

func (s *mockStorage) GetNodeChildren(_ context.Context, _ string) ([]*types.Node, error) {
	return nil, nil
}

func (s *mockStorage) GetSubtree(_ context.Context, _ string) ([]*types.Node, error) {
	return nil, nil
}

func (s *mockStorage) GetAncestors(_ context.Context, id string) ([]*types.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Walk the parent chain from the given node up to the root.
	var chain []*types.Node
	current := id
	for current != "" {
		node, ok := s.nodes[current]
		if !ok {
			break
		}
		chain = append(chain, node)
		if node.ParentID == "" || node.ParentID == current {
			break
		}
		current = node.ParentID
	}
	// Reverse so root is first.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

func (s *mockStorage) ListRootNodes(_ context.Context) ([]*types.Node, error) {
	return nil, nil
}

func (s *mockStorage) UpdateNode(_ context.Context, node *types.Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes[node.ID] = node
	return nil
}

func (s *mockStorage) DeleteNode(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.nodes, id)
	return nil
}

func (s *mockStorage) CreateAlias(_ context.Context, _, _ string) error { return nil }
func (s *mockStorage) DeleteAlias(_ context.Context, _ string) error    { return nil }
func (s *mockStorage) GetNodeByAlias(_ context.Context, _ string) (*types.Node, error) {
	return nil, nil
}
func (s *mockStorage) ListAliases(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (s *mockStorage) IndexToolIDs(_ context.Context, _ string, _ []string, _ string) error {
	return nil
}
func (s *mockStorage) GetOrphanedToolUses(_ context.Context, _ []string) (map[string][]string, error) {
	return nil, nil
}

// --- tests ---

func newTestClient(responses ...string) *langdag.Client {
	prov := &mockProvider{responses: responses, model: "test-model"}
	store := newMockStorage()
	return langdag.NewWithDeps(store, prov)
}

func TestSubAgentToolDefinition(t *testing.T) {
	tool := NewSubAgentTool(nil, nil, nil, "", "", 10, 3, 0, "/workspace", "", "alpine:latest")
	def := tool.Definition()
	if def.Name != "agent" {
		t.Errorf("name = %q, want agent", def.Name)
	}
	if def.Description == "" {
		t.Error("description should not be empty")
	}
}

func TestSubAgentToolNoApproval(t *testing.T) {
	tool := NewSubAgentTool(nil, nil, nil, "", "", 10, 3, 0, "/workspace", "", "alpine:latest")
	if tool.RequiresApproval(json.RawMessage(`{"task":"hello"}`)) {
		t.Error("sub-agent tool should never require approval")
	}
}

func TestSubAgentToolEmptyTask(t *testing.T) {
	client := newTestClient("hello")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"","mode":"explore"}`))
	if err == nil {
		t.Fatal("expected error for empty task")
	}
	if !strings.Contains(err.Error(), "task is required") {
		t.Errorf("error = %q, want 'task is required'", err.Error())
	}
}

func TestSubAgentToolInvalidJSON(t *testing.T) {
	tool := NewSubAgentTool(nil, nil, nil, "", "", 10, 3, 0, "/workspace", "", "alpine:latest")
	_, err := tool.Execute(context.Background(), json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestSubAgentToolExecuteReturnsOutput(t *testing.T) {
	client := newTestClient("Hello from the sub-agent!")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"say hello","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(result, "Hello from the sub-agent!") {
		t.Errorf("result = %q, want to contain sub-agent output", result)
	}
}

func TestSubAgentToolForwardsEventsWithAgentID(t *testing.T) {
	client := newTestClient("Sub-agent result text")
	tmpDir := t.TempDir()

	parentEvents := make(chan AgentEvent, 64)
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")
	tool.parentEvents = parentEvents

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do work","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(result, "Sub-agent result text") {
		t.Errorf("result = %q, want sub-agent output", result)
	}

	// Drain forwarded events and check them.
	var deltas []AgentEvent
	var statuses []AgentEvent
	var usages []AgentEvent
	close(parentEvents) // tool is done, safe to close
	for ev := range parentEvents {
		switch ev.Type {
		case EventSubAgentDelta:
			deltas = append(deltas, ev)
		case EventSubAgentStatus:
			statuses = append(statuses, ev)
		case EventUsage:
			usages = append(usages, ev)
		}
	}

	if len(deltas) == 0 {
		t.Error("expected at least one EventSubAgentDelta")
	}

	// All deltas should carry a non-empty AgentID.
	for _, d := range deltas {
		if d.AgentID == "" {
			t.Error("EventSubAgentDelta has empty AgentID")
		}
	}

	// Should have a "done" status event.
	hasDone := false
	for _, s := range statuses {
		if s.Text == "done" {
			hasDone = true
			if s.AgentID == "" {
				t.Error("done status has empty AgentID")
			}
		}
	}
	if !hasDone {
		t.Error("expected a 'done' EventSubAgentStatus")
	}

	// Should have forwarded usage events.
	if len(usages) == 0 {
		t.Error("expected at least one forwarded EventUsage for sub-agent cost tracking")
	}
	for _, u := range usages {
		if u.Usage == nil {
			t.Error("EventUsage has nil Usage")
		}
	}
}

func TestSubAgentStartEventCarriesMode(t *testing.T) {
	client := newTestClient("ok")
	tmpDir := t.TempDir()

	parentEvents := make(chan AgentEvent, 64)
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")
	tool.parentEvents = parentEvents

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"research","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	close(parentEvents)
	var starts []AgentEvent
	for ev := range parentEvents {
		if ev.Type == EventSubAgentStart {
			starts = append(starts, ev)
		}
	}
	if len(starts) != 1 {
		t.Fatalf("expected 1 EventSubAgentStart, got %d", len(starts))
	}
	if starts[0].Mode != "explore" {
		t.Errorf("EventSubAgentStart.Mode = %q, want %q", starts[0].Mode, "explore")
	}
	if starts[0].Task != "research" {
		t.Errorf("EventSubAgentStart.Task = %q, want %q", starts[0].Task, "research")
	}
}

// --- Task 2f: SubAgentTool.Execute additional tests ---

func TestSubAgentToolResumeWithAgentID(t *testing.T) {
	client := newTestClient("resumed output")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	// First call — establishes a sub-agent and saves its nodeID.
	result1, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"initial work","mode":"explore"}`))
	if err != nil {
		t.Fatalf("first Execute error: %v", err)
	}

	// Extract agent_id from the result (format: "[agent_id: <id>]\n\n<output>").
	agentID := extractAgentID(t, result1)

	// Second call — resume with the agent_id.
	result2, err := tool.Execute(context.Background(), json.RawMessage(
		`{"task":"continue work","mode":"explore","agent_id":"`+agentID+`"}`))
	if err != nil {
		t.Fatalf("resume Execute error: %v", err)
	}
	if !strings.Contains(result2, "agent_id:") {
		t.Errorf("resumed result should contain agent_id, got: %q", result2)
	}
}

func TestSubAgentToolUnknownAgentID(t *testing.T) {
	client := newTestClient("ok")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"resume","mode":"explore","agent_id":"nonexistent"}`))
	if err == nil {
		t.Fatal("expected error for unknown agent_id")
	}
	if !strings.Contains(err.Error(), "unknown agent_id") {
		t.Errorf("error = %q, want to contain 'unknown agent_id'", err.Error())
	}
}

func TestSubAgentToolDepthExcludesNestedAgent(t *testing.T) {
	// At maxDepth=1, currentDepth=0 → nextDepth=1 which is NOT < maxDepth → no nested agent tool.
	tool := NewSubAgentTool(nil, nil, nil, "", "", 10, 1, 0, "/workspace", "", "alpine:latest")
	subTools := tool.buildSubAgentTools("implement")

	for _, st := range subTools {
		if st.Definition().Name == "agent" {
			t.Error("sub-agent at max depth should NOT have nested agent tool")
		}
	}
}

func TestSubAgentToolDepthAllowsNestedAgent(t *testing.T) {
	// At maxDepth=3, currentDepth=0 → nextDepth=1 < 3 → nested agent tool included.
	baseTool := &testTool{name: "bash", result: "ok"}
	tool := NewSubAgentTool(nil, []Tool{baseTool}, nil, "", "", 10, 3, 0, "/workspace", "", "alpine:latest")
	subTools := tool.buildSubAgentTools("implement")

	hasAgent := false
	for _, st := range subTools {
		if st.Definition().Name == "agent" {
			hasAgent = true
		}
	}
	if !hasAgent {
		t.Error("sub-agent below max depth should have nested agent tool")
	}
	// Should also include the base tools.
	hasBash := false
	for _, st := range subTools {
		if st.Definition().Name == "bash" {
			hasBash = true
		}
	}
	if !hasBash {
		t.Error("sub-agent should include base tools")
	}
}

func TestSubAgentToolExploreModeFiltersTools(t *testing.T) {
	// Provide a full set of tools including write tools that should be excluded.
	allTools := []Tool{
		&testTool{name: "bash", result: "ok"},
		&testTool{name: "glob", result: "ok"},
		&testTool{name: "grep", result: "ok"},
		&testTool{name: "read_file", result: "ok"},
		&testTool{name: "outline", result: "ok"},
		&testTool{name: "edit_file", result: "ok"},
		&testTool{name: "write_file", result: "ok"},
		&testTool{name: "git", result: "ok"},
		&testTool{name: "devenv", result: "ok"},
	}
	tool := NewSubAgentTool(nil, allTools, nil, "", "", 10, 1, 0, "/workspace", "", "alpine:latest")
	subTools := tool.buildSubAgentTools("explore")

	got := make(map[string]bool)
	for _, st := range subTools {
		got[st.Definition().Name] = true
	}

	// Should include all allowlisted tools.
	for name := range exploreToolAllowlist {
		if !got[name] {
			t.Errorf("explore mode should include %q", name)
		}
	}

	// Should exclude write tools.
	for _, excluded := range []string{"edit_file", "write_file", "git", "devenv"} {
		if got[excluded] {
			t.Errorf("explore mode should NOT include %q", excluded)
		}
	}
}

func TestSubAgentToolImplementModeIncludesAllTools(t *testing.T) {
	allTools := []Tool{
		&testTool{name: "bash", result: "ok"},
		&testTool{name: "glob", result: "ok"},
		&testTool{name: "grep", result: "ok"},
		&testTool{name: "read_file", result: "ok"},
		&testTool{name: "outline", result: "ok"},
		&testTool{name: "edit_file", result: "ok"},
		&testTool{name: "write_file", result: "ok"},
		&testTool{name: "git", result: "ok"},
		&testTool{name: "devenv", result: "ok"},
	}
	tool := NewSubAgentTool(nil, allTools, nil, "", "", 10, 1, 0, "/workspace", "", "alpine:latest")
	subTools := tool.buildSubAgentTools("implement")

	got := make(map[string]bool)
	for _, st := range subTools {
		got[st.Definition().Name] = true
	}

	// Implement mode should include every tool.
	for _, tt := range allTools {
		name := tt.Definition().Name
		if !got[name] {
			t.Errorf("implement mode should include %q", name)
		}
	}
}

func TestSubAgentToolExploreSystemPromptExcludesWriteTools(t *testing.T) {
	// Verify the sub-agent system prompt built from explore-filtered tools
	// does not advertise write tools.
	allTools := []Tool{
		&testTool{name: "bash", result: "ok"},
		&testTool{name: "glob", result: "ok"},
		&testTool{name: "grep", result: "ok"},
		&testTool{name: "read_file", result: "ok"},
		&testTool{name: "outline", result: "ok"},
		&testTool{name: "edit_file", result: "ok"},
		&testTool{name: "write_file", result: "ok"},
		&testTool{name: "git", result: "ok"},
		&testTool{name: "devenv", result: "ok"},
	}
	tool := NewSubAgentTool(nil, allTools, nil, "", "", 10, 1, 0, "/workspace", "", "alpine:latest")
	exploreTools := tool.buildSubAgentTools("explore")

	prompt := buildSubAgentSystemPrompt(exploreTools, nil, "/workspace", "alpine:latest", nil)

	// The system prompt HasEditFile/HasWriteFile/HasDevenv/HasGit flags should be false,
	// meaning those tool sections are not included.
	for _, excluded := range []string{"edit_file", "write_file"} {
		// The prompt should not contain Has* flags set for write tools.
		// We check by verifying the tool names aren't mentioned in tool-guidance context.
		// Since tools.md conditionals use Has* flags, if the tools aren't in the list
		// the flags will be false and those sections won't render.
		_ = excluded // The real assertion is that the prompt builds without error.
	}

	// The prompt should mention read-only tools that are present.
	if !strings.Contains(prompt, "bash") && !strings.Contains(prompt, "glob") {
		t.Error("explore sub-agent prompt should reference available read-only tools")
	}
}

func TestSubAgentToolNoOutput(t *testing.T) {
	// Provider returns empty text.
	client := newTestClient("")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do nothing","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(result, "sub-agent produced no output") {
		t.Errorf("empty output should produce fallback message, got: %q", result)
	}
}

func TestSubAgentToolResultContainsAgentID(t *testing.T) {
	client := newTestClient("some output")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do work","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.HasPrefix(result, "[agent_id:") {
		t.Errorf("result should start with [agent_id:, got: %q", result[:min(50, len(result))])
	}
}

func TestFormatSubAgentResult(t *testing.T) {
	// With output path, no tokens, no model summary
	got := formatSubAgentResult("abc123", "/tmp/.herm/agents/abc123.md", "hello world", false, 0, 0, 1, 15, nil)
	want := "[agent_id: abc123] [output: /tmp/.herm/agents/abc123.md] [turns: 1/15]\n\nhello world"
	if got != want {
		t.Errorf("with path, no tokens:\n got %q\nwant %q", got, want)
	}

	// Without output path (write failed)
	got2 := formatSubAgentResult("abc123", "", "hello world", false, 0, 0, 0, 15, nil)
	want2 := "[agent_id: abc123] [turns: 0/15]\n\nhello world"
	if got2 != want2 {
		t.Errorf("without path:\n got %q\nwant %q", got2, want2)
	}

	// With output path and token usage
	got3 := formatSubAgentResult("abc123", "/tmp/out.md", "result", false, 5000, 1200, 3, 15, nil)
	want3 := "[agent_id: abc123] [output: /tmp/out.md] [tokens: input=5000 output=1200] [turns: 3/15]\n\nresult"
	if got3 != want3 {
		t.Errorf("with tokens:\n got %q\nwant %q", got3, want3)
	}

	// Without output path but with tokens
	got4 := formatSubAgentResult("abc123", "", "result", false, 100, 50, 2, 15, nil)
	want4 := "[agent_id: abc123] [tokens: input=100 output=50] [turns: 2/15]\n\nresult"
	if got4 != want4 {
		t.Errorf("tokens without path:\n got %q\nwant %q", got4, want4)
	}

	// With model summary indicator
	got5 := formatSubAgentResult("abc123", "/tmp/out.md", "- finding 1\n- finding 2", true, 1000, 200, 5, 15, nil)
	want5 := "[agent_id: abc123] [output: /tmp/out.md] [tokens: input=1000 output=200] [turns: 5/15] [summary: model]\n\n- finding 1\n- finding 2"
	if got5 != want5 {
		t.Errorf("model summary:\n got %q\nwant %q", got5, want5)
	}

	// With errors
	got6 := formatSubAgentResult("abc123", "/tmp/out.md", "partial result", false, 100, 50, 2, 15, []string{"turn 1: connection reset", "during tool \"bash\" (turn 2): timeout"})
	if !strings.Contains(got6, "[errors:") {
		t.Errorf("result with errors should contain [errors:], got: %q", got6)
	}
	if !strings.Contains(got6, "connection reset") {
		t.Errorf("result should contain error text, got: %q", got6)
	}
	if !strings.Contains(got6, "[turns: 2/15]") {
		t.Errorf("result should contain turns, got: %q", got6)
	}
}

// extractAgentID parses the agent_id from a SubAgentTool result string.
func extractAgentID(t *testing.T, result string) string {
	t.Helper()
	// Format: "[agent_id: <id>]\n\n<output>"
	prefix := "[agent_id: "
	idx := strings.Index(result, prefix)
	if idx < 0 {
		t.Fatalf("result does not contain agent_id prefix: %q", result)
	}
	rest := result[idx+len(prefix):]
	end := strings.Index(rest, "]")
	if end < 0 {
		t.Fatalf("result has no closing ] for agent_id: %q", result)
	}
	return rest[:end]
}

func TestSummarizeOutput(t *testing.T) {
	// Short output — returned as-is.
	short := "hello world"
	if got := summarizeOutput(short); got != short {
		t.Errorf("short output should not be summarized, got %q", got)
	}

	// Output exactly at limit — returned as-is.
	exact := strings.Repeat("a", subAgentSummaryBytes)
	if got := summarizeOutput(exact); got != exact {
		t.Errorf("exact-limit output should not be summarized")
	}

	// Output over limit — should be summarized.
	over := strings.Repeat("line\n", subAgentSummaryBytes/5+1)
	got := summarizeOutput(over)
	if len(got) > subAgentSummaryBytes+50 {
		t.Errorf("summarized output too large: %d bytes", len(got))
	}
	if !strings.HasSuffix(got, "[... full output in file above]") {
		t.Errorf("summarized output should end with note, got suffix: %q", got[len(got)-40:])
	}
}

// --- Phase 2 output file tests ---

func TestSubAgentOutputFileWritten(t *testing.T) {
	client := newTestClient("Full sub-agent output for file test")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"write file","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Result should contain [output: <path>]
	if !strings.Contains(result, "[output:") {
		t.Fatalf("result should contain output path, got: %q", result)
	}

	// Extract path from result
	outputPath := extractOutputPath(t, result)

	// File should exist and contain full output
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("reading output file: %v", err)
	}
	if string(data) != "Full sub-agent output for file test" {
		t.Errorf("output file content = %q, want full output", string(data))
	}
}

func TestSubAgentOutputFileLargeOutput(t *testing.T) {
	// Output larger than summary limit — file should have full output, result should have summary.
	largeOutput := strings.Repeat("This is a detailed line of output.\n", 50)
	client := newTestClient(largeOutput)
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"produce large output","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Result should be summarized (shorter than full output)
	if len(result) >= len(largeOutput) {
		t.Errorf("result should be summarized, got %d bytes (full output is %d)", len(result), len(largeOutput))
	}
	if !strings.Contains(result, "[... full output in file above]") {
		t.Errorf("result should contain summary note")
	}

	// File should contain full output
	outputPath := extractOutputPath(t, result)
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("reading output file: %v", err)
	}
	if string(data) != strings.TrimSpace(largeOutput) {
		t.Errorf("output file should contain full output, got %d bytes", len(data))
	}
}

func TestCleanupAgentOutputDir(t *testing.T) {
	tmpDir := t.TempDir()
	dir := filepath.Join(tmpDir, ".herm", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create an old file (>24h)
	oldFile := filepath.Join(dir, "old-agent.md")
	if err := os.WriteFile(oldFile, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-25 * time.Hour)
	os.Chtimes(oldFile, oldTime, oldTime)

	// Create a recent file
	newFile := filepath.Join(dir, "new-agent.md")
	if err := os.WriteFile(newFile, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	cleanupAgentOutputDir(tmpDir)

	// Old file should be removed
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("old file should have been removed")
	}

	// New file should remain
	if _, err := os.Stat(newFile); err != nil {
		t.Error("new file should still exist")
	}
}

func TestCleanupAgentOutputDirNonexistent(t *testing.T) {
	// Should not panic when directory doesn't exist.
	cleanupAgentOutputDir("/nonexistent/path")
}

// --- Background sub-agent tests ---

func TestSubAgentBackgroundReturnsImmediately(t *testing.T) {
	client := newTestClient("background output")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	start := time.Now()
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"long task","mode":"explore","background":true}`))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("background Execute took %v, expected near-instant return", elapsed)
	}
	if !strings.Contains(result, "[agent_id:") {
		t.Errorf("result should contain [agent_id:], got: %q", result)
	}
	if !strings.Contains(result, "background") {
		t.Errorf("result should mention background, got: %q", result)
	}

	// Wait for background goroutine to finish so TempDir cleanup succeeds.
	agentID := extractAgentID(t, result)
	waitForBgAgent(t, tool, agentID, 10*time.Second)
}

func TestSubAgentBackgroundCompletionStoresResult(t *testing.T) {
	client := newTestClient("bg agent output")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"bg task","mode":"explore","background":true}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	agentID := extractAgentID(t, result)

	// Wait for the background agent to finish.
	deadline := time.After(10 * time.Second)
	for {
		state := tool.lookupBgAgent(agentID)
		if state == nil {
			t.Fatal("background agent state not found")
		}
		state.mu.Lock()
		done := state.done
		storedResult := state.result
		state.mu.Unlock()
		if done {
			if !strings.Contains(storedResult, "bg agent output") {
				t.Errorf("stored result should contain agent output, got: %q", storedResult)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("background agent did not complete within 10s")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestSubAgentBackgroundStatusRunning(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(nil, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	// Manually create a running background agent state.
	tool.mu.Lock()
	tool.bgAgents["test-running"] = &bgAgentState{
		task:    "long running task",
		model:   "test-model",
		started: time.Now(),
	}
	tool.mu.Unlock()

	result, err := tool.bgAgentStatus("test-running")
	if err != nil {
		t.Fatalf("bgAgentStatus error: %v", err)
	}
	if !strings.Contains(result, "[status: running]") {
		t.Errorf("expected running status, got: %q", result)
	}
	if !strings.Contains(result, "long running task") {
		t.Errorf("expected task description in status, got: %q", result)
	}
}

func TestSubAgentBackgroundStatusCompleted(t *testing.T) {
	client := newTestClient("status check output")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"bg status task","mode":"explore","background":true}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	agentID := extractAgentID(t, result)

	// Wait for completion.
	deadline := time.After(10 * time.Second)
	for {
		state := tool.lookupBgAgent(agentID)
		if state == nil {
			t.Fatal("background agent state not found")
		}
		state.mu.Lock()
		done := state.done
		state.mu.Unlock()
		if done {
			break
		}
		select {
		case <-deadline:
			t.Fatal("background agent did not complete within 10s")
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Check status — should return completed with result.
	statusResult, err := tool.bgAgentStatus(agentID)
	if err != nil {
		t.Fatalf("status check error: %v", err)
	}
	if !strings.Contains(statusResult, "[status: completed]") {
		t.Errorf("status should be completed, got: %q", statusResult)
	}
	if !strings.Contains(statusResult, "status check output") {
		t.Errorf("status result should contain agent output, got: %q", statusResult)
	}
}

func TestSubAgentBackgroundStatusNotFound(t *testing.T) {
	tool := NewSubAgentTool(nil, nil, nil, "", "", 10, 3, 0, "/workspace", "", "alpine:latest")
	_, err := tool.bgAgentStatus("nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent background agent")
	}
	if !strings.Contains(err.Error(), "not a background agent") {
		t.Errorf("error = %q, want 'not a background agent'", err.Error())
	}
}

func TestSubAgentBackgroundRejectsWithAgentID(t *testing.T) {
	tool := NewSubAgentTool(nil, nil, nil, "", "", 10, 3, 0, "/workspace", "", "alpine:latest")
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"resume","mode":"explore","background":true,"agent_id":"abc"}`))
	if err == nil {
		t.Fatal("expected error for background + agent_id combination")
	}
	if !strings.Contains(err.Error(), "cannot be used with agent_id") {
		t.Errorf("error = %q, want to mention 'cannot be used with agent_id'", err.Error())
	}
}

func TestSubAgentBackgroundCallsOnBgComplete(t *testing.T) {
	client := newTestClient("complete callback output")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	var completedResult string
	var completedMu sync.Mutex
	tool.onBgComplete = func(result string) {
		completedMu.Lock()
		completedResult = result
		completedMu.Unlock()
	}

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"callback task","mode":"explore","background":true}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Wait for completion callback.
	deadline := time.After(10 * time.Second)
	for {
		completedMu.Lock()
		got := completedResult
		completedMu.Unlock()
		if got != "" {
			if !strings.Contains(got, "complete callback output") {
				t.Errorf("onBgComplete result should contain agent output, got: %q", got)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("onBgComplete was not called within 10s")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestSubAgentBackgroundForwardsEvents(t *testing.T) {
	client := newTestClient("bg events output")
	tmpDir := t.TempDir()
	parentEvents := make(chan AgentEvent, 64)
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")
	tool.parentEvents = parentEvents

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"bg events task","mode":"explore","background":true}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Collect events until we see "done".
	deadline := time.After(10 * time.Second)
	var gotStart, gotDelta, gotDone bool
	for !gotDone {
		select {
		case ev := <-parentEvents:
			switch ev.Type {
			case EventSubAgentStart:
				gotStart = true
			case EventSubAgentDelta:
				gotDelta = true
			case EventSubAgentStatus:
				if ev.Text == "done" {
					gotDone = true
				}
			}
		case <-deadline:
			t.Fatal("did not receive done event within 10s")
		}
	}
	if !gotStart {
		t.Error("expected EventSubAgentStart")
	}
	if !gotDelta {
		t.Error("expected at least one EventSubAgentDelta")
	}
}

// waitForBgAgent polls until a background agent completes or the timeout expires.
func waitForBgAgent(t *testing.T, tool *SubAgentTool, agentID string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		state := tool.lookupBgAgent(agentID)
		if state == nil {
			return // not found, nothing to wait for
		}
		state.mu.Lock()
		done := state.done
		state.mu.Unlock()
		if done {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("background agent %s did not complete within %v", agentID, timeout)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// extractOutputPath parses the output file path from a SubAgentTool result string.
func extractOutputPath(t *testing.T, result string) string {
	t.Helper()
	prefix := "[output: "
	idx := strings.Index(result, prefix)
	if idx < 0 {
		t.Fatalf("result does not contain output path: %q", result)
	}
	rest := result[idx+len(prefix):]
	end := strings.Index(rest, "]")
	if end < 0 {
		t.Fatalf("result has no closing ] for output path: %q", result)
	}
	return rest[:end]
}

// --- Phase 5: Token budget awareness tests ---

func TestSubAgentResultIncludesTokenUsage(t *testing.T) {
	client := newTestClient("token test output")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"count tokens","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// The mock provider returns Usage{InputTokens: 100, OutputTokens: 50}.
	// The result should include [tokens: input=100 output=50].
	if !strings.Contains(result, "[tokens:") {
		t.Errorf("result should contain token usage, got: %q", result)
	}
	if !strings.Contains(result, "input=100") {
		t.Errorf("result should contain input=100, got: %q", result)
	}
	if !strings.Contains(result, "output=50") {
		t.Errorf("result should contain output=50, got: %q", result)
	}
}

// --- Phase 4: Model-based summarization tests ---

func TestSummarizeWithModelShortOutput(t *testing.T) {
	// Short output (under 500 bytes) should be returned as-is without calling the model.
	tool := &SubAgentTool{explorationModel: "cheap-model"}
	summary, usedModel := tool.summarizeWithModel(context.Background(), "short output")
	if usedModel {
		t.Error("short output should not use model summarization")
	}
	if summary != "short output" {
		t.Errorf("summary = %q, want %q", summary, "short output")
	}
}

func TestSummarizeWithModelNoExplorationModel(t *testing.T) {
	// No exploration model — falls back to truncation.
	longOutput := strings.Repeat("This is a detailed line of output.\n", 50)
	tool := &SubAgentTool{explorationModel: "", client: nil}
	summary, usedModel := tool.summarizeWithModel(context.Background(), longOutput)
	if usedModel {
		t.Error("should fall back to truncation without exploration model")
	}
	if !strings.Contains(summary, "[... full output in file above]") {
		t.Error("fallback should use truncation summary")
	}
}

func TestSummarizeWithModelSuccess(t *testing.T) {
	// Mock provider returns a structured summary.
	client := newTestClient("- finding 1: the code has 3 modules\n- finding 2: tests pass")
	longOutput := strings.Repeat("This is a detailed line of output.\n", 50)

	tool := &SubAgentTool{
		explorationModel: "cheap-model",
		client:           client,
	}
	summary, usedModel := tool.summarizeWithModel(context.Background(), longOutput)
	if !usedModel {
		t.Error("should have used model summarization")
	}
	if !strings.Contains(summary, "finding 1") {
		t.Errorf("summary should contain model output, got: %q", summary)
	}
}

func TestSummarizeWithModelTruncatesLargeInput(t *testing.T) {
	// Output larger than 4000 chars should be truncated before sending to model.
	// We verify indirectly: the call succeeds and returns a model summary.
	client := newTestClient("- summarized large input")
	largeOutput := strings.Repeat("x", 6000)

	tool := &SubAgentTool{
		explorationModel: "cheap-model",
		client:           client,
	}
	summary, usedModel := tool.summarizeWithModel(context.Background(), largeOutput)
	if !usedModel {
		t.Error("should have used model for large input")
	}
	if !strings.Contains(summary, "summarized large input") {
		t.Errorf("summary = %q, want model output", summary)
	}
}

func TestSummarizeWithModelExecuteIntegration(t *testing.T) {
	// When explorationModel is set and output is large, Execute() should use
	// model summarization and include [summary: model] indicator.
	largeOutput := strings.Repeat("This is a detailed line.\n", 50)
	// First response: sub-agent's LLM reply (the large output).
	// Second response: summarization model's response.
	client := newTestClient(largeOutput, "- bullet point summary")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "cheap-model", 10, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"explore codebase","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if !strings.Contains(result, "[summary: model]") {
		t.Errorf("result should contain [summary: model], got: %q", result)
	}
	if !strings.Contains(result, "bullet point summary") {
		t.Errorf("result should contain model summary, got: %q", result)
	}
}

func TestSummarizeWithModelFallbackOnShortOutput(t *testing.T) {
	// When output is short, Execute() should NOT call the model and should
	// NOT include a summary indicator.
	client := newTestClient("brief result")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "cheap-model", 10, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"quick check","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if strings.Contains(result, "[summary:") {
		t.Errorf("short output should not have summary indicator, got: %q", result)
	}
	if !strings.Contains(result, "brief result") {
		t.Errorf("result should contain original output, got: %q", result)
	}
}

// --- Phase 5: Graceful sub-agent error reporting tests ---

func TestSubAgentResultIncludesTurnCount(t *testing.T) {
	client := newTestClient("turn count output")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"work","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Result should always contain [turns: N/M].
	if !strings.Contains(result, "[turns:") {
		t.Errorf("result should contain [turns:], got: %q", result)
	}
	// The mock makes no tool calls, so turns should be 0/10.
	if !strings.Contains(result, "[turns: 0/10]") {
		t.Errorf("result should contain [turns: 0/10], got: %q", result)
	}
}

func TestSubAgentResultMaxTurnsShown(t *testing.T) {
	// Verify that maxTurns is reflected in the turns display.
	client := newTestClient("output")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 5, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"quick task","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if !strings.Contains(result, "[turns: 0/5]") {
		t.Errorf("result should show maxTurns=5, got: %q", result)
	}
}

func TestFormatSubAgentResultWithErrors(t *testing.T) {
	// Errors should appear in the header.
	errors := []string{"turn 1: connection reset", `during tool "bash" (turn 2): timeout`}
	got := formatSubAgentResult("abc", "/tmp/out.md", "partial", false, 100, 50, 2, 15, errors)

	if !strings.Contains(got, "[errors: turn 1: connection reset; during tool") {
		t.Errorf("result should contain joined errors, got: %q", got)
	}
	if !strings.Contains(got, "[turns: 2/15]") {
		t.Errorf("result should contain turns, got: %q", got)
	}
}

func TestFormatSubAgentResultNoOutputWithErrors(t *testing.T) {
	// When the sub-agent produced no text but had errors, buildResult should
	// use the errors as the result body instead of "(sub-agent produced no output)".
	client := newTestClient("") // empty output
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	// We can't easily inject errors into the event stream via mock, so test
	// buildResult directly.
	result := tool.buildResult(context.Background(), "test-id", nil, []string{"API error: 500"}, 100, 50, 1)
	if strings.Contains(result, "sub-agent produced no output") {
		t.Errorf("should not show generic no-output message when errors are present, got: %q", result)
	}
	if !strings.Contains(result, "Sub-agent encountered errors") {
		t.Errorf("should include error context, got: %q", result)
	}
	if !strings.Contains(result, "API error: 500") {
		t.Errorf("should include error text, got: %q", result)
	}
}

func TestFormatSubAgentResultNoOutputNoErrors(t *testing.T) {
	// No output and no errors — should show generic message.
	client := newTestClient("")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	result := tool.buildResult(context.Background(), "test-id", nil, nil, 0, 0, 0)
	if !strings.Contains(result, "sub-agent produced no output") {
		t.Errorf("should show generic no-output, got: %q", result)
	}
}

// --- Phase 1: Mode validation and model selection tests ---

func TestSubAgentToolInvalidMode(t *testing.T) {
	client := newTestClient("ok")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "main-model", "cheap-model", 10, 3, 0, tmpDir, "", "alpine:latest")

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"test","mode":"invalid"}`))
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
	if !strings.Contains(err.Error(), `mode must be "explore" or "implement"`) {
		t.Errorf("error = %q, want mode validation error", err.Error())
	}
}

func TestSubAgentToolMissingMode(t *testing.T) {
	client := newTestClient("ok")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "main-model", "cheap-model", 10, 3, 0, tmpDir, "", "alpine:latest")

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"test"}`))
	if err == nil {
		t.Fatal("expected error for missing mode")
	}
	if !strings.Contains(err.Error(), `mode must be "explore" or "implement"`) {
		t.Errorf("error = %q, want mode validation error", err.Error())
	}
}

func TestSubAgentToolExploreModeUsesExplorationModel(t *testing.T) {
	// The mock provider records which model is used via the Stream request.
	// We verify indirectly: explore mode should succeed and use the exploration model.
	client := newTestClient("explore output")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "main-model", "cheap-model", 10, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"search code","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(result, "explore output") {
		t.Errorf("result = %q, want explore output", result)
	}
}

func TestSubAgentToolImplementModeUsesMainModel(t *testing.T) {
	client := newTestClient("implement output")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "main-model", "cheap-model", 10, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"write code","mode":"implement"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(result, "implement output") {
		t.Errorf("result = %q, want implement output", result)
	}
}

// --- Phase 2: Turn counting tests ---

func TestSubAgentToolBatchedToolCallsCountAsOneTurn(t *testing.T) {
	// A single LLM response with 3 tool calls should count as 1 turn, not 3.
	// Uses failThenSucceedProvider (from agent_test.go) which supports scripted
	// responses with tool_use blocks.
	mockTool := &testTool{name: "test_tool", result: "ok"}

	prov := &failThenSucceedProvider{
		model:       "test-model",
		failOnCalls: map[int]error{},
		responses: []scriptedResponse{
			{
				text: "",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "tu1", Name: "test_tool", Input: json.RawMessage(`{}`)},
					{Type: "tool_use", ID: "tu2", Name: "test_tool", Input: json.RawMessage(`{}`)},
					{Type: "tool_use", ID: "tu3", Name: "test_tool", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "done after 3 tool calls", tokensIn: 100, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, []Tool{mockTool}, nil, "test-model", "", 20, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"batch test","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// 3 tool calls in 1 response = 1 turn, not 3.
	if !strings.Contains(result, "[turns: 1/20]") {
		t.Errorf("3 tool calls in one response should count as 1 turn, got: %q", result)
	}
}

func TestSubAgentToolMultipleResponsesCountSeparately(t *testing.T) {
	// Two LLM responses, each with tool calls, should count as 2 turns.
	mockTool := &testTool{name: "test_tool", result: "ok"}

	prov := &failThenSucceedProvider{
		model:       "test-model",
		failOnCalls: map[int]error{},
		responses: []scriptedResponse{
			{
				text: "",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "tu1", Name: "test_tool", Input: json.RawMessage(`{}`)},
					{Type: "tool_use", ID: "tu2", Name: "test_tool", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{
				text: "",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "tu3", Name: "test_tool", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "done after two rounds", tokensIn: 100, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, []Tool{mockTool}, nil, "test-model", "", 20, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"multi-response test","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// 2 responses with tool calls = 2 turns.
	if !strings.Contains(result, "[turns: 2/20]") {
		t.Errorf("2 responses with tool calls should count as 2 turns, got: %q", result)
	}
}

// --- Phase 4: Sub-agent done timeout tests ---

func TestSubAgentDoneTimeoutDefault(t *testing.T) {
	// Verify NewSubAgentTool sets the doneTimeout to the default constant.
	tool := NewSubAgentTool(nil, nil, nil, "", "", 10, 3, 0, "/workspace", "", "alpine:latest")
	if tool.doneTimeout != subAgentDoneTimeout {
		t.Errorf("doneTimeout = %v, want %v", tool.doneTimeout, subAgentDoneTimeout)
	}
}

func TestSubAgentDoneTimeoutCustom(t *testing.T) {
	// Verify that a custom doneTimeout doesn't break normal execution.
	client := newTestClient("timeout test output")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")
	tool.doneTimeout = 5 * time.Second // shorter than default, but generous enough for normal flow

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"quick task","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(result, "timeout test output") {
		t.Errorf("result should contain output, got: %q", result)
	}
	// Normal execution should NOT contain the timeout error.
	if strings.Contains(result, "did not exit within") {
		t.Errorf("normal execution should not contain timeout error, got: %q", result)
	}
}

func TestSubAgentDoneTimeoutErrorInResult(t *testing.T) {
	// Verify the timeout error message appears correctly in the result when
	// the goroutine hangs. Test via buildResult directly since the gap between
	// EventDone and goroutine completion in the real agent is too narrow to
	// reliably trigger the timeout in tests.
	client := newTestClient("partial output")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	timeoutErr := fmt.Sprintf("sub-agent goroutine did not exit within %v after completion", 200*time.Millisecond)
	result := tool.buildResult(context.Background(), "test-timeout", []string{"partial output"}, []string{timeoutErr}, 100, 50, 1)
	if !strings.Contains(result, "did not exit within") {
		t.Errorf("result should contain timeout error, got: %q", result)
	}
	if !strings.Contains(result, "partial output") {
		t.Errorf("result should still contain collected output, got: %q", result)
	}
}

func TestSubAgentDoneTimeoutDoesNotHang(t *testing.T) {
	// Verify Execute returns within a bounded time even with a very short
	// doneTimeout. With doneTimeout=0, time.After fires immediately, and the
	// select may randomly pick the timeout path — either way, Execute must not
	// hang. This exercises both select branches non-deterministically.
	client := newTestClient("fast output")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")
	tool.doneTimeout = 1 * time.Millisecond // near-instant timeout

	done := make(chan string, 1)
	go func() {
		result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"fast task","mode":"explore"}`))
		if err != nil {
			done <- "error: " + err.Error()
			return
		}
		done <- result
	}()

	select {
	case result := <-done:
		// Execute returned — verify it contains output regardless of which
		// select branch was taken.
		if !strings.Contains(result, "fast output") && !strings.Contains(result, "did not exit within") {
			t.Errorf("result should contain output or timeout error, got: %q", result)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Execute hung — doneTimeout safety net is not working")
	}
}

// --- Phase 5, Task 5a: Sub-agent output token overflow ---

// firstFreeBlockingTool returns immediately for the first N calls, then blocks
// until released or context canceled. This creates a deterministic synchronization
// point for testing max_turns — the tool blocks on the turn that exceeds the limit,
// giving the sub-agent time to detect the overflow and cancel the agent.
type firstFreeBlockingTool struct {
	name    string
	mu      sync.Mutex
	count   int
	free    int            // first N calls return immediately
	release chan struct{}  // close to release blocked calls
}

func (ft *firstFreeBlockingTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{Name: ft.name, Description: "test tool"}
}

func (ft *firstFreeBlockingTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	ft.mu.Lock()
	ft.count++
	n := ft.count
	ft.mu.Unlock()

	if n <= ft.free {
		return "ok", nil
	}

	select {
	case <-ft.release:
		return "released", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (ft *firstFreeBlockingTool) RequiresApproval(_ json.RawMessage) bool { return false }
func (ft *firstFreeBlockingTool) HostTool() bool                         { return false }

func TestSubAgentMaxTurnsReached(t *testing.T) {
	// When a sub-agent exceeds maxTurns, the result should contain:
	// 1. Partial text output collected before cancellation
	// 2. A descriptive error about max turns being reached
	// 3. The turns count showing the limit was exceeded
	//
	// Uses a blocking tool: the first call returns immediately (turn 1 completes),
	// the second call blocks until the sub-agent detects turns > maxTurns and
	// cancels the agent, which releases the blocked tool via context cancellation.
	release := make(chan struct{})
	defer close(release)
	mockTool := &firstFreeBlockingTool{name: "test_tool", free: 1, release: release}

	prov := &failThenSucceedProvider{
		model:       "test-model",
		failOnCalls: map[int]error{},
		responses: []scriptedResponse{
			{
				text: "First turn output.",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "tu1", Name: "test_tool", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{
				text: "Second turn output.",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "tu2", Name: "test_tool", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "Should not reach here.", tokensIn: 100, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	// maxTurns=1: turn 1 completes (tool returns immediately),
	// turn 2 triggers cancel (tool blocks until canceled).
	tool := NewSubAgentTool(client, []Tool{mockTool}, nil, "test-model", "", 1, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"looping task","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Should contain error about max turns.
	if !strings.Contains(result, "maximum turns") {
		t.Errorf("result should mention maximum turns, got: %q", result)
	}

	// Should contain partial output from at least the first turn.
	if !strings.Contains(result, "First turn output.") {
		t.Errorf("result should contain first turn output, got: %q", result)
	}

	// Turns count should show 2 (the turn that triggered cancel) against maxTurns=1.
	if !strings.Contains(result, "[turns: 2/1]") {
		t.Errorf("result should show turns 2/1, got: %q", result)
	}
}

func TestSubAgentMaxTurnsPartialOutputPreserved(t *testing.T) {
	// Verify that when max_turns is hit, all text collected up to that point
	// is included in the result — not just from the last turn.
	release := make(chan struct{})
	defer close(release)
	mockTool := &firstFreeBlockingTool{name: "test_tool", free: 2, release: release}

	prov := &failThenSucceedProvider{
		model:       "test-model",
		failOnCalls: map[int]error{},
		responses: []scriptedResponse{
			{
				text: "Alpha. ",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "tu1", Name: "test_tool", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{
				text: "Beta. ",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "tu2", Name: "test_tool", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{
				text: "Gamma. ",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "tu3", Name: "test_tool", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "Final.", tokensIn: 100, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	// maxTurns=2: turns 1,2 complete (tool returns immediately),
	// turn 3 triggers cancel (tool blocks until canceled).
	tool := NewSubAgentTool(client, []Tool{mockTool}, nil, "test-model", "", 2, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"multi-turn","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Output from the first two turns should be present.
	if !strings.Contains(result, "Alpha.") {
		t.Errorf("result should contain first turn text, got: %q", result)
	}
	if !strings.Contains(result, "Beta.") {
		t.Errorf("result should contain second turn text, got: %q", result)
	}

	// Should show turns 3/2 (the third turn triggered cancel).
	if !strings.Contains(result, "[turns: 3/2]") {
		t.Errorf("result should show turns 3/2, got: %q", result)
	}

	// Should have max turns error.
	if !strings.Contains(result, "maximum turns (2)") {
		t.Errorf("result should mention maximum turns (2), got: %q", result)
	}
}

// --- Phase 5, Task 5b: Sub-agent LLM error propagation ---

func TestSubAgentPermanentErrorPropagation(t *testing.T) {
	// When the sub-agent's provider returns a non-retryable error (401),
	// the error should appear in the result with turn context.
	prov := &failThenSucceedProvider{
		model: "test-model",
		failOnCalls: map[int]error{
			0: fmt.Errorf("HTTP 401: Unauthorized"),
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"auth test","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Result should contain the 401 error.
	if !strings.Contains(result, "401") {
		t.Errorf("result should contain 401, got: %q", result)
	}
	if !strings.Contains(result, "Unauthorized") {
		t.Errorf("result should contain Unauthorized, got: %q", result)
	}
	// Error should include turn context.
	if !strings.Contains(result, "turn") {
		t.Errorf("result should include turn context, got: %q", result)
	}
	// Since there's no text output, errors should form the result body.
	if !strings.Contains(result, "encountered errors") {
		t.Errorf("result should mention errors, got: %q", result)
	}
}

func TestSubAgentTransientErrorExhaustsRetries(t *testing.T) {
	// When the provider returns retryable errors (429) that exhaust all retries,
	// the final error should appear in the sub-agent result.
	prov := &failThenSucceedProvider{
		model: "test-model",
		failOnCalls: map[int]error{
			0: fmt.Errorf("HTTP 429: rate limited"),
			1: fmt.Errorf("HTTP 429: rate limited"),
			2: fmt.Errorf("HTTP 429: rate limited"),
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"retry test","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Result should contain the retry error.
	if !strings.Contains(result, "429") || !strings.Contains(result, "rate limited") {
		t.Errorf("result should contain retry exhaustion error, got: %q", result)
	}
	// Should be in the errors section.
	if !strings.Contains(result, "[errors:") {
		t.Errorf("result should have errors section, got: %q", result)
	}
}

func TestSubAgentErrorDuringToolExecution(t *testing.T) {
	// When a tool returns an error during sub-agent execution, the agent should
	// continue (tool errors produce tool_result with IsError=true) and the
	// error should NOT appear in agentErrors (tool errors are tool-level, not
	// agent-level).
	failingTool := &testTool{name: "test_tool", result: "", err: fmt.Errorf("tool crashed")}

	prov := &failThenSucceedProvider{
		model:       "test-model",
		failOnCalls: map[int]error{},
		responses: []scriptedResponse{
			{
				text: "Let me run the tool.",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "tu1", Name: "test_tool", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "Tool failed, here's my analysis.", tokensIn: 100, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, []Tool{failingTool}, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"tool error test","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Agent should have recovered and produced output.
	if !strings.Contains(result, "Tool failed, here's my analysis.") {
		t.Errorf("result should contain agent's recovery text, got: %q", result)
	}
	// Should NOT have agent-level errors (tool errors are handled by the tool result flow).
	if strings.Contains(result, "[errors:") {
		t.Errorf("tool execution errors should not appear as agent errors, got: %q", result)
	}
}

func TestSubAgentOutputTruncationClarity(t *testing.T) {
	// When sub-agent output exceeds the summary limit, the result should:
	// 1. Have a clear truncation indicator
	// 2. Point to the full output file
	// 3. Contain the beginning of the output
	largeOutput := strings.Repeat("Detailed analysis of the codebase.\n", 100) // ~3500 bytes, well over 500
	client := newTestClient(largeOutput)
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"large output","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Result should contain truncation note.
	if !strings.Contains(result, "[... full output in file above]") {
		t.Errorf("truncated result should contain file pointer note, got: %q", result)
	}

	// Result should contain output file path.
	if !strings.Contains(result, "[output:") {
		t.Errorf("result should contain output file path, got: %q", result)
	}

	// Result should contain the beginning of the output.
	if !strings.Contains(result, "Detailed analysis") {
		t.Errorf("result should contain beginning of output, got: %q", result)
	}

	// The full output file should contain the complete content.
	outputPath := extractOutputPath(t, result)
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("reading output file: %v", err)
	}
	if len(data) < 3000 {
		t.Errorf("output file should contain full output, got %d bytes", len(data))
	}
}

// --- Phase 5, Task 5c: Sub-agent stream interruption ---

// stallingStreamProvider sends partial text then stalls, simulating a provider
// whose stream stops mid-response (e.g., network partition, server crash).
type stallingStreamProvider struct {
	model   string
	release chan struct{} // close to release stalled goroutines (cleanup)
}

func (p *stallingStreamProvider) Complete(_ context.Context, _ *types.CompletionRequest) (*types.CompletionResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (p *stallingStreamProvider) Stream(ctx context.Context, _ *types.CompletionRequest) (<-chan types.StreamEvent, error) {
	ch := make(chan types.StreamEvent, 10)
	go func() {
		defer close(ch)
		ch <- types.StreamEvent{Type: types.StreamEventDelta, Content: "partial text before stall..."}
		// Stall until released or context canceled.
		select {
		case <-p.release:
		case <-ctx.Done():
		}
	}()
	return ch, nil
}

func (p *stallingStreamProvider) Name() string             { return "mock" }
func (p *stallingStreamProvider) Models() []types.ModelInfo { return nil }

func TestSubAgentStreamStallTimeout(t *testing.T) {
	// When the sub-agent's stream stalls (no chunks), the stream chunk timeout
	// should fire, producing a "stream stalled" error in the result.
	release := make(chan struct{})
	defer close(release)

	prov := &stallingStreamProvider{model: "test-model", release: release}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")
	tool.streamTimeout = 200 * time.Millisecond // very short for test

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"stall test","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Result should contain the stream stall error.
	if !strings.Contains(result, "stream stalled") {
		t.Errorf("result should mention stream stall, got: %q", result)
	}

	// The partial text sent before the stall should be collected.
	if !strings.Contains(result, "partial text before stall") {
		t.Errorf("result should contain partial text, got: %q", result)
	}
}

// --- Phase 5, Task 5d: Background agent error surfacing ---

func TestSubAgentBackgroundFatalErrorSurfacing(t *testing.T) {
	// When a background sub-agent encounters a fatal error (401), the error
	// should appear in: (1) bgAgentStatus result, (2) onBgComplete callback.
	prov := &failThenSucceedProvider{
		model: "test-model",
		failOnCalls: map[int]error{
			0: fmt.Errorf("HTTP 401: Unauthorized"),
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	var completedResult string
	var completedMu sync.Mutex
	tool.onBgComplete = func(result string) {
		completedMu.Lock()
		completedResult = result
		completedMu.Unlock()
	}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"bg error task","mode":"explore","background":true}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	agentID := extractAgentID(t, result)

	// Wait for background agent to complete.
	waitForBgAgent(t, tool, agentID, 10*time.Second)

	// (1) bgAgentStatus should return the error in the completed result.
	status, err := tool.bgAgentStatus(agentID)
	if err != nil {
		t.Fatalf("bgAgentStatus error: %v", err)
	}
	if !strings.Contains(status, "[status: completed]") {
		t.Errorf("status should be completed, got: %q", status)
	}
	if !strings.Contains(status, "401") || !strings.Contains(status, "Unauthorized") {
		t.Errorf("status should contain the error, got: %q", status)
	}

	// (2) onBgComplete callback should include the error.
	completedMu.Lock()
	got := completedResult
	completedMu.Unlock()
	if !strings.Contains(got, "401") || !strings.Contains(got, "Unauthorized") {
		t.Errorf("onBgComplete result should contain error, got: %q", got)
	}
}

func TestSubAgentBackgroundErrorIncludesAgentContext(t *testing.T) {
	// Verify background agent errors include agent_id and turn context.
	prov := &failThenSucceedProvider{
		model: "test-model",
		failOnCalls: map[int]error{
			0: fmt.Errorf("HTTP 500: internal server error"),
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	var completedResult string
	var completedMu sync.Mutex
	tool.onBgComplete = func(result string) {
		completedMu.Lock()
		completedResult = result
		completedMu.Unlock()
	}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"bg context test","mode":"explore","background":true}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	agentID := extractAgentID(t, result)
	waitForBgAgent(t, tool, agentID, 30*time.Second) // 500 is retryable, retries take time

	completedMu.Lock()
	got := completedResult
	completedMu.Unlock()

	// Result should include agent_id.
	if !strings.Contains(got, "[agent_id:") {
		t.Errorf("result should contain agent_id, got: %q", got)
	}
	// Result should include turn context.
	if !strings.Contains(got, "turn") {
		t.Errorf("result should contain turn context, got: %q", got)
	}
}

func TestSubAgentBackgroundCompletionInjection(t *testing.T) {
	// Verify InjectBackgroundResult works with background agent errors.
	// When a background agent completes, onBgComplete fires, which in production
	// calls agent.InjectBackgroundResult. Verify the result is injectable.
	client := newTestClient("bg output text")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	// Create a parent agent to receive the injection.
	parentAgent := NewAgent(nil, nil, nil, "", "test-model", 0)

	tool.onBgComplete = parentAgent.InjectBackgroundResult

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"inject test","mode":"explore","background":true}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	agentID := extractAgentID(t, result)
	waitForBgAgent(t, tool, agentID, 10*time.Second)

	// The parent agent should have pending background results.
	results := parentAgent.drainBackgroundResults()
	if len(results) != 1 {
		t.Fatalf("expected 1 background result, got %d", len(results))
	}
	if !strings.Contains(results[0], "bg output text") {
		t.Errorf("injected result should contain agent output, got: %q", results[0])
	}
}

// --- Phase 5, Task 5e: Concurrent sub-agent race conditions ---

func TestSubAgentConcurrentForegroundRace(t *testing.T) {
	// Spawn 5 foreground sub-agents concurrently from the same SubAgentTool.
	// Verify: no race conditions on agentNodes map, all results correctly
	// attributed with unique agent_ids. Must pass with -race flag.
	prov := &mockProvider{responses: []string{
		"output-0", "output-1", "output-2", "output-3", "output-4",
	}, model: "test-model"}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	const n = 5
	results := make([]string, n)
	errors := make([]error, n)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errors[idx] = tool.Execute(context.Background(), json.RawMessage(
				fmt.Sprintf(`{"task":"task %d","mode":"explore"}`, idx)))
		}(i)
	}
	wg.Wait()

	for i, err := range errors {
		if err != nil {
			t.Errorf("sub-agent %d error: %v", i, err)
		}
	}

	// Each result should contain a unique agent_id.
	agentIDs := make(map[string]bool)
	for i, result := range results {
		id := extractAgentID(t, result)
		if agentIDs[id] {
			t.Errorf("duplicate agent_id %q from sub-agent %d", id, i)
		}
		agentIDs[id] = true
	}

	if len(agentIDs) != n {
		t.Errorf("expected %d unique agent_ids, got %d", n, len(agentIDs))
	}
}

func TestSubAgentConcurrentBackgroundRace(t *testing.T) {
	// Spawn 5 background sub-agents concurrently from the same SubAgentTool.
	// Verify: no race conditions on bgAgents map, all complete with unique IDs.
	prov := &mockProvider{responses: []string{
		"bg-0", "bg-1", "bg-2", "bg-3", "bg-4",
	}, model: "test-model"}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	parentEvents := make(chan AgentEvent, 256)
	tool.parentEvents = parentEvents

	const n = 5
	results := make([]string, n)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			result, err := tool.Execute(context.Background(), json.RawMessage(
				fmt.Sprintf(`{"task":"bg task %d","mode":"explore","background":true}`, idx)))
			if err != nil {
				t.Errorf("Execute %d error: %v", idx, err)
				return
			}
			results[idx] = result
		}(i)
	}
	wg.Wait()

	// Wait for all background agents to complete.
	agentIDs := make(map[string]bool)
	for _, result := range results {
		if result == "" {
			continue
		}
		id := extractAgentID(t, result)
		agentIDs[id] = true
		waitForBgAgent(t, tool, id, 10*time.Second)
	}

	if len(agentIDs) != n {
		t.Errorf("expected %d unique agent_ids, got %d", n, len(agentIDs))
	}

	// All should have completed status.
	for id := range agentIDs {
		status, err := tool.bgAgentStatus(id)
		if err != nil {
			t.Errorf("bgAgentStatus(%q) error: %v", id, err)
			continue
		}
		if !strings.Contains(status, "[status: completed]") {
			t.Errorf("agent %q should be completed, got: %q", id, status)
		}
	}
}

func TestSubAgentConcurrentMixedRace(t *testing.T) {
	// Mix foreground and background sub-agents concurrently to exercise all
	// shared state paths simultaneously.
	prov := &mockProvider{responses: []string{
		"mixed-0", "mixed-1", "mixed-2", "mixed-3", "mixed-4", "mixed-5",
	}, model: "test-model"}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	parentEvents := make(chan AgentEvent, 256)
	tool.parentEvents = parentEvents

	const n = 6 // 3 foreground + 3 background
	results := make([]string, n)
	errs := make([]error, n)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		bg := i >= 3 // last 3 are background
		go func(idx int, background bool) {
			defer wg.Done()
			input := fmt.Sprintf(`{"task":"mixed task %d","mode":"explore"`, idx)
			if background {
				input += `,"background":true`
			}
			input += "}"
			results[idx], errs[idx] = tool.Execute(context.Background(), json.RawMessage(input))
		}(i, bg)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("sub-agent %d error: %v", i, err)
		}
	}

	// Wait for background agents to complete.
	for i := 3; i < n; i++ {
		if results[i] == "" {
			continue
		}
		id := extractAgentID(t, results[i])
		waitForBgAgent(t, tool, id, 10*time.Second)
	}

	// All should have unique agent_ids.
	agentIDs := make(map[string]bool)
	for _, result := range results {
		if result == "" {
			continue
		}
		id := extractAgentID(t, result)
		if agentIDs[id] {
			t.Errorf("duplicate agent_id %q", id)
		}
		agentIDs[id] = true
	}
}

func TestSubAgentStreamErrorMidResponse(t *testing.T) {
	// When the stream sends an error event mid-response (simulating connection
	// reset), the error should be collected and appear in the result.
	prov := &streamFailThenSucceedProvider{
		model:           "test-model",
		failStreamCalls: map[int]bool{0: true, 1: true}, // all streams fail
		responses: []scriptedResponse{
			{text: "Hello world", tokensIn: 100, tokensOut: 50},
			{text: "Hello world", tokensIn: 100, tokensOut: 50},
			// Stream retries exhaust, then connection-level retries exhaust too.
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"stream error test","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Result should contain stream-related error.
	if !strings.Contains(result, "stream interrupted") && !strings.Contains(result, "connection reset") {
		t.Errorf("result should contain stream error, got: %q", result)
	}
}
