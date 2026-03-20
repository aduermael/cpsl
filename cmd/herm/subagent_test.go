package main

import (
	"context"
	"encoding/json"
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
	responses []string // text responses to return, one per Prompt/Stream call
	mu        sync.Mutex
	callIdx   int
	model     string
}

func (p *mockProvider) Complete(_ context.Context, _ *types.CompletionRequest) (*types.CompletionResponse, error) {
	p.mu.Lock()
	idx := p.callIdx
	p.callIdx++
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

func (s *mockStorage) GetAncestors(_ context.Context, _ string) ([]*types.Node, error) {
	return nil, nil
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
	tool := NewSubAgentTool(nil, nil, nil, "", "", 10, 3, 0, "/workspace", "", "alpine:latest", nil)
	def := tool.Definition()
	if def.Name != "agent" {
		t.Errorf("name = %q, want agent", def.Name)
	}
	if def.Description == "" {
		t.Error("description should not be empty")
	}
}

func TestSubAgentToolNoApproval(t *testing.T) {
	tool := NewSubAgentTool(nil, nil, nil, "", "", 10, 3, 0, "/workspace", "", "alpine:latest", nil)
	if tool.RequiresApproval(json.RawMessage(`{"task":"hello"}`)) {
		t.Error("sub-agent tool should never require approval")
	}
}

func TestSubAgentToolEmptyTask(t *testing.T) {
	client := newTestClient("hello")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest", nil)

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"task":""}`))
	if err == nil {
		t.Fatal("expected error for empty task")
	}
	if !strings.Contains(err.Error(), "task is required") {
		t.Errorf("error = %q, want 'task is required'", err.Error())
	}
}

func TestSubAgentToolInvalidJSON(t *testing.T) {
	tool := NewSubAgentTool(nil, nil, nil, "", "", 10, 3, 0, "/workspace", "", "alpine:latest", nil)
	_, err := tool.Execute(context.Background(), json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestSubAgentToolExecuteReturnsOutput(t *testing.T) {
	client := newTestClient("Hello from the sub-agent!")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest", nil)

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"say hello"}`))
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
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest", nil)
	tool.parentEvents = parentEvents

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do work"}`))
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

// --- Task 2f: SubAgentTool.Execute additional tests ---

func TestSubAgentToolResumeWithAgentID(t *testing.T) {
	client := newTestClient("resumed output")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest", nil)

	// First call — establishes a sub-agent and saves its nodeID.
	result1, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"initial work"}`))
	if err != nil {
		t.Fatalf("first Execute error: %v", err)
	}

	// Extract agent_id from the result (format: "[agent_id: <id>]\n\n<output>").
	agentID := extractAgentID(t, result1)

	// Second call — resume with the agent_id.
	result2, err := tool.Execute(context.Background(), json.RawMessage(
		`{"task":"continue work","agent_id":"`+agentID+`"}`))
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
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest", nil)

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"resume","agent_id":"nonexistent"}`))
	if err == nil {
		t.Fatal("expected error for unknown agent_id")
	}
	if !strings.Contains(err.Error(), "unknown agent_id") {
		t.Errorf("error = %q, want to contain 'unknown agent_id'", err.Error())
	}
}

func TestSubAgentToolDepthExcludesNestedAgent(t *testing.T) {
	// At maxDepth=1, currentDepth=0 → nextDepth=1 which is NOT < maxDepth → no nested agent tool.
	tool := NewSubAgentTool(nil, nil, nil, "", "", 10, 1, 0, "/workspace", "", "alpine:latest", nil)
	subTools := tool.buildSubAgentTools()

	for _, st := range subTools {
		if st.Definition().Name == "agent" {
			t.Error("sub-agent at max depth should NOT have nested agent tool")
		}
	}
}

func TestSubAgentToolDepthAllowsNestedAgent(t *testing.T) {
	// At maxDepth=3, currentDepth=0 → nextDepth=1 < 3 → nested agent tool included.
	baseTool := &testTool{name: "bash", result: "ok"}
	tool := NewSubAgentTool(nil, []Tool{baseTool}, nil, "", "", 10, 3, 0, "/workspace", "", "alpine:latest", nil)
	subTools := tool.buildSubAgentTools()

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

func TestSubAgentToolNoOutput(t *testing.T) {
	// Provider returns empty text.
	client := newTestClient("")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest", nil)

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do nothing"}`))
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
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest", nil)

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"do work"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.HasPrefix(result, "[agent_id:") {
		t.Errorf("result should start with [agent_id:, got: %q", result[:min(50, len(result))])
	}
}

func TestFormatSubAgentResult(t *testing.T) {
	// With output path, no tokens
	got := formatSubAgentResult("abc123", "/tmp/.herm/agents/abc123.md", "hello world", 0, 0)
	want := "[agent_id: abc123] [output: /tmp/.herm/agents/abc123.md]\n\nhello world"
	if got != want {
		t.Errorf("with path, no tokens: got %q, want %q", got, want)
	}

	// Without output path (write failed)
	got2 := formatSubAgentResult("abc123", "", "hello world", 0, 0)
	want2 := "[agent_id: abc123]\n\nhello world"
	if got2 != want2 {
		t.Errorf("without path: got %q, want %q", got2, want2)
	}

	// With output path and token usage
	got3 := formatSubAgentResult("abc123", "/tmp/out.md", "result", 5000, 1200)
	want3 := "[agent_id: abc123] [output: /tmp/out.md] [tokens: input=5000 output=1200]\n\nresult"
	if got3 != want3 {
		t.Errorf("with tokens: got %q, want %q", got3, want3)
	}

	// Without output path but with tokens
	got4 := formatSubAgentResult("abc123", "", "result", 100, 50)
	want4 := "[agent_id: abc123] [tokens: input=100 output=50]\n\nresult"
	if got4 != want4 {
		t.Errorf("tokens without path: got %q, want %q", got4, want4)
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
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest", nil)

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"write file"}`))
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
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest", nil)

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"produce large output"}`))
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
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest", nil)

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"count tokens"}`))
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
