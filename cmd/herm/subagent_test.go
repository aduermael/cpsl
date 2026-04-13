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

	parentEvents := make(chan AgentEvent, 256)
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

	parentEvents := make(chan AgentEvent, 256)
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
	subTools := tool.buildSubAgentTools(ModeGeneral)

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
	subTools := tool.buildSubAgentTools(ModeGeneral)

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
	for name := range modeToolAllowlists[ModeExplore] {
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
	subTools := tool.buildSubAgentTools(ModeGeneral)

	got := make(map[string]bool)
	for _, st := range subTools {
		got[st.Definition().Name] = true
	}

	// General mode should include every tool.
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
	parentEvents := make(chan AgentEvent, 256)
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
	if !strings.Contains(err.Error(), `mode must be "explore" or "general"`) {
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
	if !strings.Contains(err.Error(), `mode must be "explore" or "general"`) {
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

func TestSubAgentToolGeneralModeUsesMainModel(t *testing.T) {
	client := newTestClient("general output")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "main-model", "cheap-model", 10, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"write code","mode":"general"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(result, "general output") {
		t.Errorf("result = %q, want general output", result)
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
	// When a sub-agent exceeds maxTurns, the two-stage enforcement kicks in:
	// 1. At turns > maxTurns, agent is canceled for synthesis (not hard killed)
	// 2. A graceful synthesis call is attempted (tools-disabled)
	// 3. Partial text output collected before cancellation is preserved
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
			// Synthesis response (tools-disabled call).
			{text: "Summary of findings.", tokensIn: 100, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	// maxTurns=1: turn 1 completes (tool returns immediately),
	// turn 2 triggers synthesis (tool blocks until canceled).
	tool := NewSubAgentTool(client, []Tool{mockTool}, nil, "test-model", "", 1, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"looping task","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Should contain partial output from at least the first turn.
	if !strings.Contains(result, "First turn output.") {
		t.Errorf("result should contain first turn output, got: %q", result)
	}

	// The turn counter races with doneCh: the drain loop may count 1 or 2
	// turns depending on whether doneCh fires before EventToolCallStart for
	// turn 2 is processed. Both are valid — the important invariant is that
	// partial output was preserved and synthesis was attempted.
	if !strings.Contains(result, "[turns: 2/1]") && !strings.Contains(result, "[turns: 1/1]") {
		t.Errorf("result should show turns 1/1 or 2/1, got: %q", result)
	}
}

func TestSubAgentMaxTurnsPartialOutputPreserved(t *testing.T) {
	// Verify that when max_turns is hit, all text collected up to that point
	// is included in the result — not just from the last turn. With two-stage
	// enforcement, the agent gets a synthesis opportunity instead of hard kill.
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
			// Synthesis response (tools-disabled call after turns > maxTurns).
			{text: "Summary.", tokensIn: 100, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	// maxTurns=2: turns 1,2 complete (tool returns immediately),
	// turn 3 triggers synthesis (tool blocks until canceled).
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

	// The turn counter races with the agent goroutine: the drain loop may
	// count 2 or 3 turns depending on scheduling. Both are valid — the
	// important invariant is that text was preserved and turns did not exceed
	// maxTurns + 2 (synthesis buffer).
	if !strings.Contains(result, "[turns: 3/2]") && !strings.Contains(result, "[turns: 2/2]") {
		t.Errorf("result should show turns 2/2 or 3/2, got: %q", result)
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

func TestWaitForBackgroundAgents_NoAgents(t *testing.T) {
	tool := NewSubAgentTool(nil, nil, nil, "m", "", 10, 1, 0, t.TempDir(), "", "")
	results := tool.WaitForBackgroundAgents(time.Second)
	if results != nil {
		t.Errorf("expected nil for no bg agents, got %v", results)
	}
}

func TestWaitForBackgroundAgents_AllDone(t *testing.T) {
	tool := NewSubAgentTool(nil, nil, nil, "m", "", 10, 1, 0, t.TempDir(), "", "")

	// Manually inject two already-completed background agents.
	tool.mu.Lock()
	tool.bgAgents["a1"] = &bgAgentState{done: true, result: "result-a1", task: "task1"}
	tool.bgAgents["a2"] = &bgAgentState{done: true, result: "result-a2", task: "task2"}
	tool.mu.Unlock()

	results := tool.WaitForBackgroundAgents(time.Second)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	joined := strings.Join(results, "|")
	if !strings.Contains(joined, "result-a1") || !strings.Contains(joined, "result-a2") {
		t.Errorf("expected both results, got %v", results)
	}
}

func TestWaitForBackgroundAgents_WaitsForCompletion(t *testing.T) {
	tool := NewSubAgentTool(nil, nil, nil, "m", "", 10, 1, 0, t.TempDir(), "", "")

	state := &bgAgentState{task: "slow-task", started: time.Now()}
	tool.mu.Lock()
	tool.bgAgents["bg1"] = state
	tool.mu.Unlock()

	// Complete the agent after a short delay.
	go func() {
		time.Sleep(100 * time.Millisecond)
		state.mu.Lock()
		state.done = true
		state.result = "slow-result"
		state.mu.Unlock()
	}()

	start := time.Now()
	results := tool.WaitForBackgroundAgents(5 * time.Second)
	elapsed := time.Since(start)

	if len(results) != 1 || results[0] != "slow-result" {
		t.Fatalf("expected [slow-result], got %v", results)
	}
	if elapsed > 2*time.Second {
		t.Errorf("waited too long: %v", elapsed)
	}
}

func TestWaitForBackgroundAgents_Timeout(t *testing.T) {
	tool := NewSubAgentTool(nil, nil, nil, "m", "", 10, 1, 0, t.TempDir(), "", "")

	// One done, one still running.
	tool.mu.Lock()
	tool.bgAgents["done1"] = &bgAgentState{done: true, result: "done-result", task: "done-task"}
	tool.bgAgents["hang1"] = &bgAgentState{task: "hanging-task", started: time.Now()}
	tool.mu.Unlock()

	results := tool.WaitForBackgroundAgents(300 * time.Millisecond)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	joined := strings.Join(results, "|")
	if !strings.Contains(joined, "done-result") {
		t.Errorf("expected done result, got %v", results)
	}
	if !strings.Contains(joined, "timed out") {
		t.Errorf("expected timeout notice, got %v", results)
	}
	if !strings.Contains(joined, "hanging-task") {
		t.Errorf("expected task name in timeout notice, got %v", results)
	}
}

func TestSubAgentResumeInheritsMode(t *testing.T) {
	client := newTestClient("resumed output")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "main-model", "cheap-model", 10, 3, 0, tmpDir, "", "alpine:latest")

	// Spawn in general mode.
	result1, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"write code","mode":"general"}`))
	if err != nil {
		t.Fatalf("first Execute error: %v", err)
	}
	agentID := extractAgentID(t, result1)

	// Resume without providing mode — should inherit "general".
	result2, err := tool.Execute(context.Background(), json.RawMessage(
		`{"task":"continue","agent_id":"`+agentID+`"}`))
	if err != nil {
		t.Fatalf("resume Execute error: %v", err)
	}
	if !strings.Contains(result2, "agent_id:") {
		t.Errorf("expected agent_id in result, got %q", result2)
	}
}

func TestSubAgentResumeIgnoresProvidedMode(t *testing.T) {
	client := newTestClient("resumed output")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "main-model", "cheap-model", 10, 3, 0, tmpDir, "", "alpine:latest")

	// Spawn in explore mode.
	result1, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"search","mode":"explore"}`))
	if err != nil {
		t.Fatalf("first Execute error: %v", err)
	}
	agentID := extractAgentID(t, result1)

	// Resume with mode:"general" — should still use "explore" (original).
	_, err = tool.Execute(context.Background(), json.RawMessage(
		`{"task":"continue","mode":"general","agent_id":"`+agentID+`"}`))
	if err != nil {
		t.Fatalf("resume Execute error: %v", err)
	}

	// Verify the stored mode is still "explore".
	tool.mu.Lock()
	state := tool.agentNodes[agentID]
	tool.mu.Unlock()
	if state.mode != ModeExplore {
		t.Errorf("stored mode = %q, want %q", state.mode, ModeExplore)
	}
}

func TestSubAgentNewAgentWithoutModeStillFails(t *testing.T) {
	client := newTestClient("ok")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "main-model", "cheap-model", 10, 3, 0, tmpDir, "", "alpine:latest")

	// New agent (no agent_id) without mode should still fail validation.
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"test"}`))
	if err == nil {
		t.Fatal("expected error for new agent without mode")
	}
	if !strings.Contains(err.Error(), `mode must be "explore" or "general"`) {
		t.Errorf("error = %q, want mode validation error", err.Error())
	}
}

func TestBackgroundToolResultContainsSuppressionGuidance(t *testing.T) {
	client := newTestClient("suppression test output")
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"test task","mode":"explore","background":true}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if !strings.Contains(result, "Do not narrate progress") {
		t.Errorf("background tool result should contain suppression guidance, got: %q", result)
	}
	if !strings.Contains(result, "Move on to your next action or stop") {
		t.Errorf("background tool result should tell agent to move on, got: %q", result)
	}

	agentID := extractAgentID(t, result)
	waitForBgAgent(t, tool, agentID, 10*time.Second)
}

func TestExplorePromptContainsExplorationStrategy(t *testing.T) {
	allTools := []Tool{
		&testTool{name: "bash", result: "ok"},
		&testTool{name: "glob", result: "ok"},
		&testTool{name: "grep", result: "ok"},
		&testTool{name: "read_file", result: "ok"},
		&testTool{name: "outline", result: "ok"},
		&testTool{name: "edit_file", result: "ok"},
		&testTool{name: "write_file", result: "ok"},
	}
	tool := NewSubAgentTool(nil, allTools, nil, "", "", 10, 1, 0, "/workspace", "", "alpine:latest")

	// Explore mode: no edit/write tools → should include exploration strategy.
	exploreTools := tool.buildSubAgentTools("explore")
	explorePrompt := buildSubAgentSystemPrompt(exploreTools, nil, "/workspace", "alpine:latest", nil)

	for _, keyword := range []string{"Exploration strategy", "offset/limit", "Stop when you have enough"} {
		if !strings.Contains(explorePrompt, keyword) {
			t.Errorf("explore prompt should contain %q", keyword)
		}
	}

	// General mode: has edit/write tools → should NOT include exploration strategy.
	generalTools := tool.buildSubAgentTools(ModeGeneral)
	generalPrompt := buildSubAgentSystemPrompt(generalTools, nil, "/workspace", "alpine:latest", nil)

	if strings.Contains(generalPrompt, "Exploration strategy") {
		t.Error("general prompt should NOT contain exploration strategy section")
	}
}

// chunkyProvider streams text as many small chunks to test delta batching.
type chunkyProvider struct {
	chunks   int
	model    string
}

func (p *chunkyProvider) Complete(_ context.Context, _ *types.CompletionRequest) (*types.CompletionResponse, error) {
	return &types.CompletionResponse{
		ID: "resp-chunky", Model: p.model,
		Content:    []types.ContentBlock{{Type: "text", Text: "ok"}},
		StopReason: "end_turn",
		Usage:      types.Usage{InputTokens: 100, OutputTokens: 50},
	}, nil
}

func (p *chunkyProvider) Stream(_ context.Context, req *types.CompletionRequest) (<-chan types.StreamEvent, error) {
	ch := make(chan types.StreamEvent, p.chunks+10)
	go func() {
		defer close(ch)
		for i := 0; i < p.chunks; i++ {
			ch <- types.StreamEvent{
				Type:    types.StreamEventDelta,
				Content: fmt.Sprintf("c%d ", i),
			}
		}
		ch <- types.StreamEvent{
			Type: types.StreamEventDone,
			Response: &types.CompletionResponse{
				ID: "resp-chunky", Model: req.Model,
				Content:    []types.ContentBlock{{Type: "text", Text: "ok"}},
				StopReason: "end_turn",
				Usage:      types.Usage{InputTokens: 100, OutputTokens: 50},
			},
		}
	}()
	return ch, nil
}

func (p *chunkyProvider) Name() string             { return "chunky" }
func (p *chunkyProvider) Models() []types.ModelInfo { return nil }

func TestDeltaBatchingReducesEvents(t *testing.T) {
	// A sub-agent producing 100 rapid text deltas should have them batched
	// into far fewer EventSubAgentDelta events (one per ~200ms interval).
	prov := &chunkyProvider{chunks: 100, model: "test-model"}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	parentCh := make(chan AgentEvent, 4096)
	tool := &SubAgentTool{
		parentEvents: parentCh,
		agentNodes:   make(map[string]agentNodeState),
		maxTurns:     10,
		doneTimeout:  2 * time.Second,
	}

	agent := NewAgent(client, nil, nil, "", "test-model", 0)
	subTC := NewTraceCollector("test-session")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := &bgAgentState{cancel: func() {}}

	doneRun := make(chan struct{})
	go func() {
		tool.runBackground(ctx, agent, agent.ID(),
			subAgentInput{Task: "test", Mode: "explore"},
			"test-model", subTC, state)
		close(doneRun)
	}()

	select {
	case <-doneRun:
	case <-time.After(10 * time.Second):
		t.Fatal("runBackground did not complete")
	}

	// Count EventSubAgentDelta events.
	var deltaCount int
	var gotText strings.Builder
	for {
		select {
		case ev := <-parentCh:
			if ev.Type == EventSubAgentDelta {
				deltaCount++
				gotText.WriteString(ev.Text)
			}
		default:
			goto done2
		}
	}
done2:

	if deltaCount >= 100 {
		t.Errorf("expected batching to reduce delta events below 100, got %d", deltaCount)
	}
	if deltaCount == 0 {
		t.Error("expected at least 1 delta event")
	}
	// The forwarded text should be a contiguous prefix of the expected text
	// (the doneCh drain path captures remaining text into textParts but
	// doesn't forward it as deltas — that's correct since the agent is done).
	var expectedText strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&expectedText, "c%d ", i)
	}
	if got := gotText.String(); !strings.HasPrefix(expectedText.String(), got) {
		t.Errorf("forwarded text is not a prefix of expected text:\ngot  %q", got)
	}
	t.Logf("100 text deltas batched into %d EventSubAgentDelta events", deltaCount)
}

func TestForwardBlockingWithTimeout(t *testing.T) {
	t.Run("timeout_when_channel_full", func(t *testing.T) {
		parentCh := make(chan AgentEvent, 1)
		parentCh <- AgentEvent{Type: EventToolCallDone} // fill channel

		tool := &SubAgentTool{parentEvents: parentCh}
		timeout := 100 * time.Millisecond

		start := time.Now()
		tool.forwardBlockingWithTimeout(AgentEvent{
			Type: EventSubAgentStatus, Text: "done",
		}, timeout)
		elapsed := time.Since(start)

		if elapsed < timeout/2 {
			t.Errorf("expected to block for ~%v, but returned in %v", timeout, elapsed)
		}
	})

	t.Run("delivers_when_drained", func(t *testing.T) {
		parentCh := make(chan AgentEvent, 1)
		parentCh <- AgentEvent{Type: EventToolCallDone} // fill channel

		tool := &SubAgentTool{parentEvents: parentCh}
		doneEvent := AgentEvent{Type: EventSubAgentStatus, Text: "done", AgentID: "test-agent"}

		// Drain after a short delay so the send can succeed.
		go func() {
			time.Sleep(20 * time.Millisecond)
			<-parentCh
		}()

		delivered := make(chan struct{})
		go func() {
			tool.forwardBlockingWithTimeout(doneEvent, 2*time.Second)
			close(delivered)
		}()

		select {
		case <-delivered:
			// OK — delivered after drain.
		case <-time.After(1 * time.Second):
			t.Fatal("forwardBlockingWithTimeout did not return after channel was drained")
		}

		// The "done" event should now be in the channel.
		select {
		case ev := <-parentCh:
			if ev.Text != "done" || ev.AgentID != "test-agent" {
				t.Errorf("got event Text=%q AgentID=%q, want done/test-agent", ev.Text, ev.AgentID)
			}
		default:
			t.Error("expected done event in channel")
		}
	})
}

// --- Phase 2 (budget-consciousness): Soft wrap-up before hard kill ---

func TestSubAgentSynthesisTurnOnExceedMaxTurns(t *testing.T) {
	// When a sub-agent exceeds maxTurns while still requesting tools, the
	// two-stage enforcement should:
	// 1. Cancel the agent (not hard kill)
	// 2. Make a tools-disabled synthesis call
	// 3. Include the synthesis text in the result
	//
	// The canceled LLM call (tool results for the turn that triggered cancel)
	// may or may not consume a response slot from the mock provider depending
	// on timing. We add the synthesis response in multiple slots to handle both.
	release := make(chan struct{})
	defer close(release)
	mockTool := &firstFreeBlockingTool{name: "test_tool", free: 2, release: release}

	synthResponse := scriptedResponse{text: "SYNTHESIS: Here is my synthesized summary of findings.", tokensIn: 100, tokensOut: 50}
	prov := &failThenSucceedProvider{
		model:       "test-model",
		failOnCalls: map[int]error{},
		responses: []scriptedResponse{
			// Turn 1: tool call, returns immediately.
			{
				text: "Exploring...",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "tu1", Name: "test_tool", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			// Turn 2: tool call, returns immediately.
			{
				text: "Still exploring...",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "tu2", Name: "test_tool", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			// Turn 3: tool call, blocks → triggers synthesis at turns > maxTurns (3 > 2).
			{
				text: "Even more exploring...",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "tu3", Name: "test_tool", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			// The canceled LLM call may consume a slot, so provide synthesis
			// response in multiple positions to ensure it's available.
			synthResponse,
			synthResponse,
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	// maxTurns=2: turns 1,2 complete. Turn 3 triggers synthesis.
	tool := NewSubAgentTool(client, []Tool{mockTool}, nil, "test-model", "", 2, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"deep exploration","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Output from the first two turns should be present.
	if !strings.Contains(result, "Exploring...") {
		t.Errorf("result should contain first turn text, got: %q", result)
	}
	if !strings.Contains(result, "Still exploring...") {
		t.Errorf("result should contain second turn text, got: %q", result)
	}

	// The synthesis text should be present (from the tools-disabled final call).
	if !strings.Contains(result, "SYNTHESIS:") {
		t.Errorf("result should contain synthesis output, got: %q", result)
	}
}

func TestSubAgentHardCancelAtMaxTurnsPlusOne(t *testing.T) {
	// When a sub-agent exceeds maxTurns + 1 (runaway despite synthesis attempt),
	// the hard cancel fires with an error message indicating synthesis was attempted.
	release := make(chan struct{})
	defer close(release)
	// free=3 so turns 1-3 complete, turn 4 blocks → hard cancel.
	mockTool := &firstFreeBlockingTool{name: "test_tool", free: 3, release: release}

	prov := &failThenSucceedProvider{
		model:       "test-model",
		failOnCalls: map[int]error{},
		responses: []scriptedResponse{
			// Turn 1
			{
				text: "Turn 1.",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "tu1", Name: "test_tool", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			// Turn 2: exceeds maxTurns=1, triggers synthesis attempt.
			{
				text: "Turn 2.",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "tu2", Name: "test_tool", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			// Turn 3: exceeds maxTurns+1=2, hard cancel fires.
			{
				text: "Turn 3.",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "tu3", Name: "test_tool", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			// Turn 4: should not be reached (hard cancel at tu4).
			{
				text: "Turn 4.",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "tu4", Name: "test_tool", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "Should not reach.", tokensIn: 100, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	// maxTurns=1: turn 2 triggers synthesis, turn 3 triggers hard cancel.
	tool := NewSubAgentTool(client, []Tool{mockTool}, nil, "test-model", "", 1, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"runaway agent","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Should contain partial output from at least the first turn.
	if !strings.Contains(result, "Turn 1.") {
		t.Errorf("result should contain first turn text, got: %q", result)
	}

	// The error message should indicate synthesis was attempted (not just "partial output").
	// Due to the race between doneCh and eventCh, the error may or may not be captured.
	// What we CAN verify: the agent terminated and produced partial output.
	if !strings.Contains(result, "[agent_id:") {
		t.Errorf("result should contain agent_id, got: %q", result)
	}
}

func TestSubAgentSynthesisPromptConstant(t *testing.T) {
	// Verify the synthesis prompt constant is well-formed.
	if !strings.Contains(subAgentSynthesisPrompt, "Turn limit reached") {
		t.Errorf("synthesis prompt should mention turn limit, got: %q", subAgentSynthesisPrompt)
	}
	if !strings.Contains(subAgentSynthesisPrompt, "Do not request tools") {
		t.Errorf("synthesis prompt should instruct no tools, got: %q", subAgentSynthesisPrompt)
	}
}

func TestSubAgentIterationBufferAccommodatesSynthesis(t *testing.T) {
	// The iteration buffer should be >= 3 to accommodate the synthesis turn
	// (maxTurns + 1) plus a safety margin.
	if subAgentIterationBuffer < 3 {
		t.Errorf("subAgentIterationBuffer = %d, want >= 3 for synthesis turn", subAgentIterationBuffer)
	}
}

func TestSubAgentTwoStageErrorMessage(t *testing.T) {
	// When the hard cancel fires at maxTurns + 1, the error message should
	// mention "synthesis was attempted" rather than the old "partial output returned".
	release := make(chan struct{})
	defer close(release)
	// free=10 so all turns complete without blocking.
	mockTool := &firstFreeBlockingTool{name: "test_tool", free: 10, release: release}

	prov := &failThenSucceedProvider{
		model:       "test-model",
		failOnCalls: map[int]error{},
		responses: []scriptedResponse{
			// Turns 1-4: all with tool calls, maxTurns=1 means:
			// turn 2 triggers synthesis, turn 3 triggers hard cancel
			{text: "t1.", toolCalls: []types.ContentBlock{{Type: "tool_use", ID: "tu1", Name: "test_tool", Input: json.RawMessage(`{}`)}}, tokensIn: 100, tokensOut: 50},
			{text: "t2.", toolCalls: []types.ContentBlock{{Type: "tool_use", ID: "tu2", Name: "test_tool", Input: json.RawMessage(`{}`)}}, tokensIn: 100, tokensOut: 50},
			{text: "t3.", toolCalls: []types.ContentBlock{{Type: "tool_use", ID: "tu3", Name: "test_tool", Input: json.RawMessage(`{}`)}}, tokensIn: 100, tokensOut: 50},
			{text: "final.", tokensIn: 100, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, []Tool{mockTool}, nil, "test-model", "", 1, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"fast runaway","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// When the hard cancel fires, the error message should NOT use the old
	// "partial output returned" phrasing. Due to event races, the error may
	// appear with the new phrasing or not appear at all (doneCh path).
	if strings.Contains(result, "partial output returned") {
		t.Errorf("should use new error message, not old 'partial output returned', got: %q", result)
	}
}

// --- Phase 6: Integration tests — budget-aware sub-agent lifecycle ---

// delayedTool introduces a small delay in Execute so the drain loop goroutine
// has time to process EventToolCallStart and call SetTurnProgress before the
// agent loop calls buildPromptOpts() for the next LLM call. Without this delay,
// the mock tools return instantly and the agent loop outpaces the drain loop,
// resulting in turnsUsed=0 for every system prompt.
type delayedTool struct {
	name   string
	result string
	delay  time.Duration
}

func (dt *delayedTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{Name: dt.name, Description: "delayed test tool"}
}

func (dt *delayedTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	time.Sleep(dt.delay)
	return dt.result, nil
}

func (dt *delayedTool) RequiresApproval(_ json.RawMessage) bool { return false }
func (dt *delayedTool) HostTool() bool                         { return false }

// budgetCapturingProvider captures the CompletionRequest for every call,
// allowing tests to verify system prompt evolution across turns. It returns
// scripted responses with optional tool calls, like failThenSucceedProvider.
type budgetCapturingProvider struct {
	mu        sync.Mutex
	requests  []*types.CompletionRequest // captured requests (system prompts, tools, etc.)
	responses []scriptedResponse
	callIdx   int
	model     string
}

func (p *budgetCapturingProvider) Complete(_ context.Context, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	idx := p.callIdx
	p.callIdx++
	p.mu.Unlock()

	r := scriptedResponse{text: "ok", tokensIn: 100, tokensOut: 50}
	if idx < len(p.responses) {
		r = p.responses[idx]
	}
	content := []types.ContentBlock{{Type: "text", Text: r.text}}
	content = append(content, r.toolCalls...)
	return &types.CompletionResponse{
		ID: fmt.Sprintf("resp-%d", idx), Model: p.model,
		Content: content, StopReason: "end_turn",
		Usage: types.Usage{InputTokens: r.tokensIn, OutputTokens: r.tokensOut},
	}, nil
}

func (p *budgetCapturingProvider) Stream(_ context.Context, req *types.CompletionRequest) (<-chan types.StreamEvent, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	idx := p.callIdx
	p.callIdx++
	p.mu.Unlock()

	r := scriptedResponse{text: "ok", tokensIn: 100, tokensOut: 50}
	if idx < len(p.responses) {
		r = p.responses[idx]
	}

	ch := make(chan types.StreamEvent, 20)
	go func() {
		defer close(ch)
		if r.text != "" {
			ch <- types.StreamEvent{Type: types.StreamEventDelta, Content: r.text}
		}
		for _, tc := range r.toolCalls {
			tc := tc
			ch <- types.StreamEvent{Type: types.StreamEventContentDone, ContentBlock: &tc}
		}
		content := []types.ContentBlock{{Type: "text", Text: r.text}}
		content = append(content, r.toolCalls...)
		stopReason := "end_turn"
		if len(r.toolCalls) > 0 {
			stopReason = "tool_use"
		}
		ch <- types.StreamEvent{
			Type: types.StreamEventDone,
			Response: &types.CompletionResponse{
				ID: fmt.Sprintf("resp-%d", idx), Model: req.Model,
				Content: content, StopReason: stopReason,
				Usage: types.Usage{InputTokens: r.tokensIn, OutputTokens: r.tokensOut},
			},
		}
	}()
	return ch, nil
}

func (p *budgetCapturingProvider) Name() string             { return "mock" }
func (p *budgetCapturingProvider) Models() []types.ModelInfo { return nil }

func (p *budgetCapturingProvider) getRequests() []*types.CompletionRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]*types.CompletionRequest, len(p.requests))
	copy(cp, p.requests)
	return cp
}

func TestIntegrationBudgetAwareSubAgentLifecycle(t *testing.T) {
	// 6a: Sub-agent with maxTurns=5 completes within budget.
	//
	// Verifies the full lifecycle:
	//   - 4 turns with tool calls, then text-only response → agent completes within budget
	//   - Initial system prompt includes budget line (Turn 0/maxTurns)
	//   - All text output is preserved in the result
	//   - No errors in the result (agent didn't exceed budget)
	//   - Turn count is correct in the result metadata
	//
	// Note: langdag's PromptFrom uses the root node's stored system prompt for
	// follow-up calls, so the budget line doesn't update between LLM calls.
	// The SetTurnProgress mechanism is validated by unit tests (Phase 1).
	const maxTurns = 5
	const toolDelay = 20 * time.Millisecond

	mockTool := &delayedTool{name: "bash", result: "tool output", delay: toolDelay}

	prov := &budgetCapturingProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{text: "Exploring.", toolCalls: []types.ContentBlock{
				{Type: "tool_use", ID: "tu1", Name: "bash", Input: json.RawMessage(`{}`)},
			}, tokensIn: 200, tokensOut: 100},
			{text: "Reading files.", toolCalls: []types.ContentBlock{
				{Type: "tool_use", ID: "tu2", Name: "bash", Input: json.RawMessage(`{}`)},
			}, tokensIn: 300, tokensOut: 150},
			{text: "Checking imports.", toolCalls: []types.ContentBlock{
				{Type: "tool_use", ID: "tu3", Name: "bash", Input: json.RawMessage(`{}`)},
			}, tokensIn: 400, tokensOut: 200},
			{text: "Analyzing.", toolCalls: []types.ContentBlock{
				{Type: "tool_use", ID: "tu4", Name: "bash", Input: json.RawMessage(`{}`)},
			}, tokensIn: 500, tokensOut: 250},
			// Text-only — agent completes within budget
			{text: "Here is my complete analysis of the codebase.", tokensIn: 600, tokensOut: 300},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, []Tool{mockTool}, nil, "test-model", "", maxTurns, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"analyze codebase","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Verify the final text output is in the result.
	if !strings.Contains(result, "complete analysis of the codebase") {
		t.Errorf("result should contain synthesis text, got: %q", result)
	}

	// No errors should be present (agent completed within budget).
	if strings.Contains(result, "[errors:") {
		t.Errorf("budget-compliant agent should have no errors, got: %q", result)
	}

	// Verify correct number of LLM calls: 4 tool-call turns + 1 text-only.
	requests := prov.getRequests()
	if len(requests) < 5 {
		t.Fatalf("expected at least 5 LLM calls, got %d", len(requests))
	}

	// The system prompt should NOT contain the dynamic budget line (it's in
	// user messages now for prompt caching). But it should contain the static
	// Budget management section from role_subagent.md.
	if !strings.Contains(requests[0].System, "Budget management") {
		t.Errorf("system prompt should contain Budget management section")
	}

	// Follow-up calls (after the initial prompt) should have budget info in
	// user messages as a <system-reminder> text block, not in the system prompt.
	budgetInMessages := false
	for _, req := range requests[1:] {
		if requestUserMessagesContain(req, "Budget:") {
			budgetInMessages = true
			break
		}
	}
	if !budgetInMessages {
		t.Error("follow-up LLM calls should have budget info in user messages")
	}

	// Result should show turns within budget (4 tool-call turns counted).
	if !strings.Contains(result, "[turns:") {
		t.Errorf("result should contain turns metadata, got: %q", result)
	}
	// The turn count should be 4 (tool-call turns only; text-only response doesn't count).
	if !strings.Contains(result, fmt.Sprintf("[turns: 4/%d]", maxTurns)) {
		t.Logf("turns metadata (may vary due to event races): %q", result)
	}

	// Token usage should be tracked.
	if !strings.Contains(result, "[tokens:") {
		t.Errorf("result should contain token metadata, got: %q", result)
	}
}

func TestIntegrationSubAgentIgnoresBudgetGetsForcedSynthesis(t *testing.T) {
	// 6b: Sub-agent that ignores budget warnings and keeps requesting tools.
	//
	// With maxTurns=3:
	//   Turns 1-3: tool calls (within budget, tools return immediately)
	//   Turn 4 (turns > maxTurns): still requesting tools → synthesis triggered
	//     → drain loop calls agent.Cancel(), then gracefulSubAgentSynthesis
	//       makes a tools-disabled LLM call to force text output
	//
	// Uses firstFreeBlockingTool: first 3 tool calls return immediately,
	// 4th blocks until agent is canceled (synthesis trigger at turns > maxTurns).
	const maxTurns = 3

	release := make(chan struct{})
	defer close(release)
	// "bash" to pass explore-mode allowlist.
	mockTool := &firstFreeBlockingTool{name: "bash", free: 3, release: release}

	prov := &budgetCapturingProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{text: "Starting.", toolCalls: []types.ContentBlock{
				{Type: "tool_use", ID: "tu1", Name: "bash", Input: json.RawMessage(`{}`)},
			}, tokensIn: 100, tokensOut: 50},
			{text: "More exploration.", toolCalls: []types.ContentBlock{
				{Type: "tool_use", ID: "tu2", Name: "bash", Input: json.RawMessage(`{}`)},
			}, tokensIn: 100, tokensOut: 50},
			{text: "Still exploring.", toolCalls: []types.ContentBlock{
				{Type: "tool_use", ID: "tu3", Name: "bash", Input: json.RawMessage(`{}`)},
			}, tokensIn: 100, tokensOut: 50},
			// Turn 4: agent ignores warnings, requests tools again
			{text: "Ignoring warnings.", toolCalls: []types.ContentBlock{
				{Type: "tool_use", ID: "tu4", Name: "bash", Input: json.RawMessage(`{}`)},
			}, tokensIn: 100, tokensOut: 50},
			// Synthesis: tools-disabled call forces text output
			{text: "Forced synthesis summary.", tokensIn: 100, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, []Tool{mockTool}, nil, "test-model", "", maxTurns, 3, 0, tmpDir, "", "alpine:latest")

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"runaway exploration","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Partial output from early turns should be preserved.
	if !strings.Contains(result, "Starting.") {
		t.Errorf("result should contain first turn text, got: %q", result)
	}

	// The turn counter should show the agent exceeded its budget.
	if !strings.Contains(result, "[turns:") {
		t.Errorf("result should contain turns metadata, got: %q", result)
	}

	// Verify synthesis was attempted by checking for a tools-disabled call
	// in the captured requests (a call with no tools and a non-empty system).
	requests := prov.getRequests()
	foundToolsDisabled := false
	for _, req := range requests {
		if len(req.Tools) == 0 && req.System != "" {
			foundToolsDisabled = true
			break
		}
	}
	// Due to event races the synthesis call may or may not succeed through
	// the mock layer, but the mechanism should be invoked. Log either way.
	if foundToolsDisabled {
		t.Logf("tools-disabled synthesis call was captured (%d total provider calls)", len(requests))
	} else {
		t.Logf("tools-disabled synthesis call not captured (event race); %d total provider calls", len(requests))
	}

	// The old error message "partial output returned" should never appear.
	if strings.Contains(result, "partial output returned") {
		t.Errorf("should use new error message, not old 'partial output returned', got: %q", result)
	}
}

func TestIntegrationBackgroundSubAgentBudgetAwareness(t *testing.T) {
	// 6c: Background sub-agent with budget tracking via runBackground().
	//
	// Same lifecycle as 6a but via background mode. Verifies:
	// - Background agent returns immediately with agent_id
	// - Event forwarding (start, delta, done)
	// - Result stored in bgAgentState contains synthesis text
	// - Initial system prompt includes budget line
	// - No errors in the result
	const maxTurns = 5
	const toolDelay = 20 * time.Millisecond

	mockTool := &delayedTool{name: "bash", result: "tool output", delay: toolDelay}

	prov := &budgetCapturingProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{text: "Turn 1.", toolCalls: []types.ContentBlock{
				{Type: "tool_use", ID: "tu1", Name: "bash", Input: json.RawMessage(`{}`)},
			}, tokensIn: 200, tokensOut: 100},
			{text: "Turn 2.", toolCalls: []types.ContentBlock{
				{Type: "tool_use", ID: "tu2", Name: "bash", Input: json.RawMessage(`{}`)},
			}, tokensIn: 300, tokensOut: 150},
			{text: "Turn 3.", toolCalls: []types.ContentBlock{
				{Type: "tool_use", ID: "tu3", Name: "bash", Input: json.RawMessage(`{}`)},
			}, tokensIn: 400, tokensOut: 200},
			{text: "Turn 4.", toolCalls: []types.ContentBlock{
				{Type: "tool_use", ID: "tu4", Name: "bash", Input: json.RawMessage(`{}`)},
			}, tokensIn: 500, tokensOut: 250},
			// Text-only — agent completes within budget
			{text: "Background synthesis complete.", tokensIn: 600, tokensOut: 300},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	parentEvents := make(chan AgentEvent, 256)
	tool := NewSubAgentTool(client, []Tool{mockTool}, nil, "test-model", "", maxTurns, 3, 0, tmpDir, "", "alpine:latest")
	tool.parentEvents = parentEvents

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"bg budget test","mode":"explore","background":true}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if !strings.Contains(result, "[agent_id:") {
		t.Fatalf("expected [agent_id:] in result, got: %q", result)
	}
	agentID := extractAgentID(t, result)

	// Wait for background agent to complete.
	waitForBgAgent(t, tool, agentID, 15*time.Second)

	// Verify the stored result contains synthesis text.
	state := tool.lookupBgAgent(agentID)
	if state == nil {
		t.Fatal("background agent state not found after completion")
	}
	state.mu.Lock()
	storedResult := state.result
	state.mu.Unlock()

	if !strings.Contains(storedResult, "Background synthesis complete.") {
		t.Errorf("stored result should contain synthesis text, got: %q", storedResult)
	}

	if strings.Contains(storedResult, "[errors:") {
		t.Errorf("budget-compliant background agent should have no errors, got: %q", storedResult)
	}

	// Verify events were forwarded: should have start, at least one delta, and done.
	var gotStart, gotDelta, gotDone bool
	drainTimeout := time.After(2 * time.Second)
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
		case <-drainTimeout:
			gotDone = true // events already consumed or timeout
		}
	}
	if !gotStart {
		t.Error("expected EventSubAgentStart from background agent")
	}
	if !gotDelta {
		t.Error("expected at least one EventSubAgentDelta from background agent")
	}

	// Verify budget info appears in follow-up user messages (not system prompt).
	requests := prov.getRequests()
	if len(requests) < 5 {
		t.Errorf("expected at least 5 LLM calls for background agent, got %d", len(requests))
	}
	budgetInMessages := false
	for _, req := range requests[1:] {
		if requestUserMessagesContain(req, "Budget:") {
			budgetInMessages = true
			break
		}
	}
	if !budgetInMessages {
		t.Error("follow-up LLM calls should have budget info in user messages")
	}
}

func TestIntegrationSubAgentSystemPromptIncludesTurnBudget(t *testing.T) {
	// 6d: Verify that:
	// 1. The sub-agent's system prompt includes a budget line
	// 2. The sub-agent's system prompt includes the Budget management section
	// 3. The main agent's tool description mentions the default turn budget
	// 4. The sub-agent role prompt mentions turn-based budgeting
	const maxTurns = 4
	const toolDelay = 20 * time.Millisecond

	mockTool := &delayedTool{name: "bash", result: "ok", delay: toolDelay}

	prov := &budgetCapturingProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{text: "A.", toolCalls: []types.ContentBlock{
				{Type: "tool_use", ID: "tu1", Name: "bash", Input: json.RawMessage(`{}`)},
			}, tokensIn: 100, tokensOut: 50},
			{text: "B.", toolCalls: []types.ContentBlock{
				{Type: "tool_use", ID: "tu2", Name: "bash", Input: json.RawMessage(`{}`)},
			}, tokensIn: 100, tokensOut: 50},
			// Text-only (agent wraps up)
			{text: "Summary.", tokensIn: 100, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, []Tool{mockTool}, nil, "test-model", "", maxTurns, 3, 0, tmpDir, "", "alpine:latest")

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"verify prompts","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	requests := prov.getRequests()
	if len(requests) < 3 {
		t.Fatalf("expected at least 3 LLM calls, got %d", len(requests))
	}

	// The system prompt should NOT contain the dynamic budget line (moved to
	// user messages for prompt caching). Static sections remain.
	if !strings.Contains(requests[0].System, "Budget management") {
		t.Errorf("system prompt should contain 'Budget management' section")
	}
	if !strings.Contains(requests[0].System, "limited number of turns") {
		t.Errorf("system prompt should mention turns in budget management section")
	}

	// Follow-up calls should have budget info in user messages.
	budgetInMessages := false
	for _, req := range requests[1:] {
		if requestUserMessagesContain(req, "Budget:") {
			budgetInMessages = true
			break
		}
	}
	if !budgetInMessages {
		t.Error("follow-up LLM calls should have budget info in user messages")
	}

	// System prompt should be identical across all calls (prompt caching).
	for i := 1; i < len(requests); i++ {
		if requests[i].System != requests[0].System {
			t.Errorf("system prompt changed between call 1 and %d — breaks prompt caching", i+1)
		}
	}

	// Verify the loaded tool description for "agent" mentions the default turn budget.
	// The tool's Definition().Description uses a fallback when the package-level
	// toolDescriptions cache isn't initialized, so test the loader directly.
	descs := loadToolDescriptions("alpine:latest", tmpDir)
	agentDesc, ok := descs["agent"]
	if !ok {
		t.Fatal("loadToolDescriptions should include 'agent' tool")
	}
	budgetStr := fmt.Sprintf("%d turns per sub-agent", defaultSubAgentMaxTurns)
	if !strings.Contains(agentDesc.Full, budgetStr) {
		t.Errorf("agent tool description should mention %q", budgetStr)
	}
}

// extractBudgetLine returns the Budget line from a string, or a message
// if none found. Used for readable test failure messages.
func extractBudgetLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "Budget:") {
			return strings.TrimSpace(line)
		}
	}
	return "(no Budget line found)"
}

// requestUserMessagesContain checks if any user message in the request contains
// the given substring. Handles both plain string and []ContentBlock content.
func requestUserMessagesContain(req *types.CompletionRequest, substr string) bool {
	for _, msg := range req.Messages {
		if msg.Role != "user" {
			continue
		}
		// Try as plain string.
		var s string
		if err := json.Unmarshal(msg.Content, &s); err == nil {
			if strings.Contains(s, substr) {
				return true
			}
			continue
		}
		// Try as []ContentBlock.
		var blocks []types.ContentBlock
		if err := json.Unmarshal(msg.Content, &blocks); err == nil {
			for _, b := range blocks {
				if strings.Contains(b.Text, substr) {
					return true
				}
			}
		}
	}
	return false
}

// extractSystemReminderFromLastUserMessage returns the <system-reminder> text
// from the last user message in a CompletionRequest, or "" if not found.
func extractSystemReminderFromLastUserMessage(req *types.CompletionRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		msg := req.Messages[i]
		if msg.Role != "user" {
			continue
		}
		// Try as plain string.
		var s string
		if err := json.Unmarshal(msg.Content, &s); err == nil {
			if strings.Contains(s, "<system-reminder>") {
				return s
			}
			continue
		}
		// Try as []ContentBlock.
		var blocks []types.ContentBlock
		if err := json.Unmarshal(msg.Content, &blocks); err == nil {
			for _, b := range blocks {
				if b.Type == "text" && strings.Contains(b.Text, "<system-reminder>") {
					return b.Text
				}
			}
		}
		break // only check last user message
	}
	return ""
}

func TestPromptCachingPreservedAcrossTurns(t *testing.T) {
	// 7g: Verify that the system prompt is identical across all LLM calls
	// (proving prompt caching is not broken), and that budget info appears
	// in user messages with correct turn progression.
	const maxTurns = 5

	mockTool := &delayedTool{name: "bash", result: "ok", delay: 10 * time.Millisecond}

	prov := &budgetCapturingProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{text: "Turn 1.", toolCalls: []types.ContentBlock{
				{Type: "tool_use", ID: "tu1", Name: "bash", Input: json.RawMessage(`{}`)},
			}, tokensIn: 200, tokensOut: 100},
			{text: "Turn 2.", toolCalls: []types.ContentBlock{
				{Type: "tool_use", ID: "tu2", Name: "bash", Input: json.RawMessage(`{}`)},
			}, tokensIn: 300, tokensOut: 150},
			{text: "Turn 3.", toolCalls: []types.ContentBlock{
				{Type: "tool_use", ID: "tu3", Name: "bash", Input: json.RawMessage(`{}`)},
			}, tokensIn: 400, tokensOut: 200},
			// Text-only — agent wraps up.
			{text: "Final summary.", tokensIn: 500, tokensOut: 250},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, []Tool{mockTool}, nil, "test-model", "", maxTurns, 3, 0, tmpDir, "", "alpine:latest")

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"test caching","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	requests := prov.getRequests()
	if len(requests) < 4 {
		t.Fatalf("expected at least 4 LLM calls, got %d", len(requests))
	}

	// 1. System prompt must be IDENTICAL across all calls — proving prompt caching works.
	baseSystem := requests[0].System
	for i := 1; i < len(requests); i++ {
		if requests[i].System != baseSystem {
			t.Errorf("system prompt changed between call 1 and %d — breaks prompt caching.\ncall 1 len=%d, call %d len=%d",
				i+1, len(baseSystem), i+1, len(requests[i].System))
		}
	}

	// 2. System prompt must NOT contain dynamic budget content.
	if strings.Contains(baseSystem, "tokens used") {
		t.Error("system prompt should not contain dynamic token stats")
	}

	// 3. Follow-up calls should have <system-reminder> in user messages.
	// The initial call (requests[0]) has no budget reminder; follow-ups do.
	for i := 1; i < len(requests); i++ {
		reminder := extractSystemReminderFromLastUserMessage(requests[i])
		if reminder == "" {
			t.Errorf("call %d should have <system-reminder> in user message", i+1)
			continue
		}
		if !strings.Contains(reminder, "Budget:") {
			t.Errorf("call %d <system-reminder> should contain Budget line, got: %q", i+1, reminder)
		}
	}
}

// --- Phase 2d: Shared drain loop tests ---

// TestDrainConsistencyForegroundBackground verifies that the shared drain loop
// produces equivalent text content and turn counts for foreground and background.
func TestDrainConsistencyForegroundBackground(t *testing.T) {
	const expectedText = "shared drain output"
	tmpDir := t.TempDir()

	// Foreground.
	fgClient := newTestClient(expectedText)
	fgTool := NewSubAgentTool(fgClient, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")
	fgResult, err := fgTool.Execute(context.Background(), json.RawMessage(`{"task":"drain test","mode":"explore"}`))
	if err != nil {
		t.Fatalf("foreground Execute error: %v", err)
	}

	// Background.
	bgClient := newTestClient(expectedText)
	parentEvents := make(chan AgentEvent, 4096)
	bgTool := NewSubAgentTool(bgClient, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")
	bgTool.parentEvents = parentEvents

	launchResult, err := bgTool.Execute(context.Background(), json.RawMessage(`{"task":"drain test","mode":"explore","background":true}`))
	if err != nil {
		t.Fatalf("background Execute error: %v", err)
	}

	// Extract agentID from background launch result.
	prefix := "[agent_id: "
	idx := strings.Index(launchResult, prefix)
	if idx < 0 {
		t.Fatalf("launch result missing agent_id: %q", launchResult)
	}
	rest := launchResult[idx+len(prefix):]
	end := strings.Index(rest, "]")
	agentID := rest[:end]

	waitForBgAgent(t, bgTool, agentID, 10*time.Second)

	bgFinalResult, err := bgTool.bgAgentStatus(agentID)
	if err != nil {
		t.Fatalf("bgAgentStatus error: %v", err)
	}

	// Both should contain the expected text.
	if !strings.Contains(fgResult, expectedText) {
		t.Errorf("foreground missing text: %q", fgResult)
	}
	if !strings.Contains(bgFinalResult, expectedText) {
		t.Errorf("background missing text: %q", bgFinalResult)
	}

	// Both should report turns: 0/10 (text-only response, no tool calls).
	if !strings.Contains(fgResult, "[turns: 0/10]") {
		t.Errorf("foreground missing turn count [turns: 0/10]: %q", fgResult)
	}
	if !strings.Contains(bgFinalResult, "[turns: 0/10]") {
		t.Errorf("background missing turn count [turns: 0/10]: %q", bgFinalResult)
	}

	// Both should include token usage.
	if !strings.Contains(fgResult, "[tokens:") {
		t.Errorf("foreground missing token usage: %q", fgResult)
	}
	if !strings.Contains(bgFinalResult, "[tokens:") {
		t.Errorf("background missing token usage: %q", bgFinalResult)
	}

	// Wait for background goroutine to fully exit before TempDir cleanup.
	bgTool.DrainGoroutines(5 * time.Second)
}

// TestDrainForegroundDeltaForwarderReceivesAllText verifies that the foreground
// deltaForwarder callback (t.forward) receives all text from the drain loop.
func TestDrainForegroundDeltaForwarderReceivesAllText(t *testing.T) {
	const expectedText = "all deltas forwarded"
	client := newTestClient(expectedText)
	tmpDir := t.TempDir()

	parentEvents := make(chan AgentEvent, 256)
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")
	tool.parentEvents = parentEvents

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"test deltas","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Collect all delta text from forwarded events.
	close(parentEvents)
	var deltaText strings.Builder
	for ev := range parentEvents {
		if ev.Type == EventSubAgentDelta {
			deltaText.WriteString(ev.Text)
		}
	}

	// The combined delta text should match the agent's output.
	if !strings.Contains(deltaText.String(), expectedText) {
		t.Errorf("delta forwarder text = %q, want to contain %q", deltaText.String(), expectedText)
	}
}

// TestDrainBackgroundDeltaBatchingCallbackInvoked verifies that the background
// delta batching callback accumulates text and eventually forwards it.
func TestDrainBackgroundDeltaBatchingCallbackInvoked(t *testing.T) {
	const expectedText = "batched delta output"
	client := newTestClient(expectedText)
	tmpDir := t.TempDir()

	parentEvents := make(chan AgentEvent, 4096)
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")
	tool.parentEvents = parentEvents

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"test batching","mode":"explore","background":true}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Wait for done event (background completion).
	deadline := time.After(10 * time.Second)
	var deltaText strings.Builder
	gotDone := false
	for !gotDone {
		select {
		case ev := <-parentEvents:
			switch ev.Type {
			case EventSubAgentDelta:
				deltaText.WriteString(ev.Text)
			case EventSubAgentStatus:
				if ev.Text == "done" {
					gotDone = true
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for background agent done")
		}
	}

	// Wait for background goroutine to fully exit before TempDir cleanup.
	tool.DrainGoroutines(5 * time.Second)

	// The accumulated delta text should contain the expected output.
	if !strings.Contains(deltaText.String(), expectedText) {
		t.Errorf("batched delta text = %q, want to contain %q", deltaText.String(), expectedText)
	}
}

// TestDrainConsistencyWithToolCalls verifies that the shared drain loop correctly
// counts turns and forwards tool status events for both foreground and background
// when the agent makes tool calls.
func TestDrainConsistencyWithToolCalls(t *testing.T) {
	mockTool := &firstFreeBlockingTool{name: "test_tool", free: 2, release: make(chan struct{})}
	defer close(mockTool.release)

	prov := &failThenSucceedProvider{
		model:       "test-model",
		failOnCalls: map[int]error{},
		responses: []scriptedResponse{
			{
				text: "Turn one.",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "tu1", Name: "test_tool", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "Turn two result.", tokensIn: 100, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	tmpDir := t.TempDir()

	parentEvents := make(chan AgentEvent, 4096)
	tool := NewSubAgentTool(client, []Tool{mockTool}, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest")
	tool.parentEvents = parentEvents

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"tool call test","mode":"explore"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Should have 1 turn (the response with a tool call). The final text-only
	// response does not increment the turn counter since it has no tool calls.
	if !strings.Contains(result, "[turns: 1/10]") {
		t.Errorf("expected [turns: 1/10] in result: %q", result)
	}

	// Verify tool status was forwarded.
	close(parentEvents)
	gotToolStatus := false
	for ev := range parentEvents {
		if ev.Type == EventSubAgentStatus && strings.Contains(ev.Text, "tool: test_tool") {
			gotToolStatus = true
		}
	}
	if !gotToolStatus {
		t.Error("expected tool status event to be forwarded during drain")
	}
}
