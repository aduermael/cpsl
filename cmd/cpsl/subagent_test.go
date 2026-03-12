package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

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

// --- tests ---

func newTestClient(responses ...string) *langdag.Client {
	prov := &mockProvider{responses: responses, model: "test-model"}
	store := newMockStorage()
	return langdag.NewWithDeps(store, prov)
}

func TestSubAgentToolDefinition(t *testing.T) {
	tool := NewSubAgentTool(nil, nil, nil, "", 10, "/workspace", "", "alpine:latest")
	def := tool.Definition()
	if def.Name != "agent" {
		t.Errorf("name = %q, want agent", def.Name)
	}
	if def.Description == "" {
		t.Error("description should not be empty")
	}
}

func TestSubAgentToolNoApproval(t *testing.T) {
	tool := NewSubAgentTool(nil, nil, nil, "", 10, "/workspace", "", "alpine:latest")
	if tool.RequiresApproval(json.RawMessage(`{"task":"hello"}`)) {
		t.Error("sub-agent tool should never require approval")
	}
}

func TestSubAgentToolEmptyTask(t *testing.T) {
	client := newTestClient("hello")
	tool := NewSubAgentTool(client, nil, nil, "test-model", 10, "/workspace", "", "alpine:latest")

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"task":""}`))
	if err == nil {
		t.Fatal("expected error for empty task")
	}
	if !strings.Contains(err.Error(), "task is required") {
		t.Errorf("error = %q, want 'task is required'", err.Error())
	}
}

func TestSubAgentToolInvalidJSON(t *testing.T) {
	tool := NewSubAgentTool(nil, nil, nil, "", 10, "/workspace", "", "alpine:latest")
	_, err := tool.Execute(context.Background(), json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestSubAgentToolExecuteReturnsOutput(t *testing.T) {
	client := newTestClient("Hello from the sub-agent!")
	tool := NewSubAgentTool(client, nil, nil, "test-model", 10, "/workspace", "", "alpine:latest")

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

	parentEvents := make(chan AgentEvent, 64)
	tool := NewSubAgentTool(client, nil, nil, "test-model", 10, "/workspace", "", "alpine:latest")
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
