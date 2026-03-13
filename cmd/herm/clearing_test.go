package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"langdag.com/langdag"
	"langdag.com/langdag/types"
)

// clearingMockStorage extends mockStorage with ancestor chain support.
type clearingMockStorage struct {
	mu    sync.Mutex
	nodes map[string]*types.Node
	// ancestorChains maps nodeID → ordered ancestors (root first).
	ancestorChains map[string][]string
}

func newClearingMockStorage() *clearingMockStorage {
	return &clearingMockStorage{
		nodes:          make(map[string]*types.Node),
		ancestorChains: make(map[string][]string),
	}
}

func (s *clearingMockStorage) Init(_ context.Context) error { return nil }
func (s *clearingMockStorage) Close() error                 { return nil }

func (s *clearingMockStorage) CreateNode(_ context.Context, node *types.Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes[node.ID] = node
	return nil
}

func (s *clearingMockStorage) GetNode(_ context.Context, id string) (*types.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.nodes[id]; ok {
		return n, nil
	}
	return nil, nil
}

func (s *clearingMockStorage) GetNodeByPrefix(_ context.Context, _ string) (*types.Node, error) {
	return nil, nil
}

func (s *clearingMockStorage) GetNodeChildren(_ context.Context, _ string) ([]*types.Node, error) {
	return nil, nil
}

func (s *clearingMockStorage) GetSubtree(_ context.Context, _ string) ([]*types.Node, error) {
	return nil, nil
}

func (s *clearingMockStorage) GetAncestors(_ context.Context, nodeID string) ([]*types.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	chain, ok := s.ancestorChains[nodeID]
	if !ok {
		return nil, nil
	}
	var result []*types.Node
	for _, id := range chain {
		if n, ok := s.nodes[id]; ok {
			result = append(result, n)
		}
	}
	return result, nil
}

func (s *clearingMockStorage) ListRootNodes(_ context.Context) ([]*types.Node, error) {
	return nil, nil
}

func (s *clearingMockStorage) UpdateNode(_ context.Context, node *types.Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes[node.ID] = node
	return nil
}

func (s *clearingMockStorage) DeleteNode(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.nodes, id)
	return nil
}

func (s *clearingMockStorage) CreateAlias(_ context.Context, _, _ string) error { return nil }
func (s *clearingMockStorage) DeleteAlias(_ context.Context, _ string) error    { return nil }
func (s *clearingMockStorage) GetNodeByAlias(_ context.Context, _ string) (*types.Node, error) {
	return nil, nil
}
func (s *clearingMockStorage) ListAliases(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

// Helper: build a tool result content JSON string.
func toolResultContent(toolUseID, content string) string {
	blocks := []types.ContentBlock{{
		Type:      "tool_result",
		ToolUseID: toolUseID,
		Content:   content,
	}}
	data, _ := json.Marshal(blocks)
	return string(data)
}

func TestReplaceToolResultContent(t *testing.T) {
	original := toolResultContent("call_1", "Hello, World! This is a long output from a tool.")
	replaced := replaceToolResultContent(original)

	if replaced == original {
		t.Fatal("expected content to be replaced")
	}
	if !strings.Contains(replaced, `[output cleared]`) {
		t.Errorf("replaced = %q, want to contain '[output cleared]'", replaced)
	}
	// Should still be valid JSON with tool_result structure.
	var blocks []types.ContentBlock
	if err := json.Unmarshal([]byte(replaced), &blocks); err != nil {
		t.Fatalf("replaced is not valid JSON: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Type != "tool_result" {
		t.Errorf("type = %q, want tool_result", blocks[0].Type)
	}
	if blocks[0].ToolUseID != "call_1" {
		t.Errorf("tool_use_id = %q, want call_1", blocks[0].ToolUseID)
	}
}

func TestReplaceToolResultContentAlreadyCleared(t *testing.T) {
	original := toolResultContent("call_1", "[output cleared]")
	replaced := replaceToolResultContent(original)
	if replaced != original {
		t.Error("already-cleared content should not change")
	}
}

func TestReplaceToolResultContentInvalidJSON(t *testing.T) {
	original := "not json at all"
	replaced := replaceToolResultContent(original)
	if replaced != original {
		t.Error("invalid JSON should be returned unchanged")
	}
}

func TestReplaceToolResultContentMultipleResults(t *testing.T) {
	blocks := []types.ContentBlock{
		{Type: "tool_result", ToolUseID: "call_1", Content: "result one"},
		{Type: "tool_result", ToolUseID: "call_2", Content: "result two"},
	}
	data, _ := json.Marshal(blocks)
	replaced := replaceToolResultContent(string(data))

	var parsed []types.ContentBlock
	if err := json.Unmarshal([]byte(replaced), &parsed); err != nil {
		t.Fatalf("parse error: %v", err)
	}
	for i, b := range parsed {
		if b.Content != "[output cleared]" {
			t.Errorf("block[%d] content = %q, want [output cleared]", i, b.Content)
		}
	}
}

func TestClearOldToolResults(t *testing.T) {
	store := newClearingMockStorage()
	prov := &mockProvider{model: "test-model"}
	client := langdag.NewWithDeps(store, prov)

	// Build a conversation chain: user → assistant → tool_result × 6 → assistant (leaf)
	// Each tool_result pair: assistant with tool_use, then user with tool_result
	now := time.Now()
	nodes := []*types.Node{
		{ID: "root", NodeType: types.NodeTypeUser, Content: "hello", CreatedAt: now},
	}

	// Create 6 tool result round-trips
	for i := 0; i < 6; i++ {
		parentID := nodes[len(nodes)-1].ID
		assistantID := "asst-" + string(rune('a'+i))
		resultID := "result-" + string(rune('a'+i))

		// Assistant node with tool_use
		assistantContent, _ := json.Marshal([]types.ContentBlock{
			{Type: "text", Text: "Let me check."},
			{Type: "tool_use", ID: "call_" + string(rune('a'+i)), Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)},
		})
		nodes = append(nodes, &types.Node{
			ID:       assistantID,
			ParentID: parentID,
			NodeType: types.NodeTypeAssistant,
			Content:  string(assistantContent),
			TokensIn: 80000, // simulate high input tokens
			Model:    "test-model",
		})

		// Tool result node (user type)
		resultContent := toolResultContent("call_"+string(rune('a'+i)), strings.Repeat("x", 1000*(i+1)))
		nodes = append(nodes, &types.Node{
			ID:       resultID,
			ParentID: assistantID,
			NodeType: types.NodeTypeUser,
			Content:  resultContent,
		})
	}

	// Final assistant node (the leaf)
	lastResult := nodes[len(nodes)-1]
	nodes = append(nodes, &types.Node{
		ID:        "final-asst",
		ParentID:  lastResult.ID,
		NodeType:  types.NodeTypeAssistant,
		Content:   "All done.",
		TokensIn:  90000,
		TokensOut: 500,
		Model:     "test-model",
	})

	// Store all nodes and build ancestor chain for the leaf.
	var ancestorIDs []string
	for _, n := range nodes {
		_ = store.CreateNode(context.Background(), n)
		ancestorIDs = append(ancestorIDs, n.ID)
	}
	store.ancestorChains["final-asst"] = ancestorIDs

	// Create agent with a 100k context window.
	agent := NewAgent(client, nil, nil, "", "test-model", 100000)

	// Input tokens = 90000, threshold = 80000 (80% of 100k) → should trigger clearing.
	agent.clearOldToolResults(context.Background(), "final-asst", 90000)

	// The 6 tool result nodes: keep last 4, clear first 2.
	// First 2 should be cleared (largest first, but both are < threshold).
	resultA, _ := store.GetNode(context.Background(), "result-a")
	resultB, _ := store.GetNode(context.Background(), "result-b")
	resultE, _ := store.GetNode(context.Background(), "result-e")
	resultF, _ := store.GetNode(context.Background(), "result-f")

	// Oldest results should be cleared
	if !strings.Contains(resultA.Content, "[output cleared]") {
		t.Error("result-a should have been cleared")
	}
	if !strings.Contains(resultB.Content, "[output cleared]") {
		t.Error("result-b should have been cleared")
	}

	// Recent results should be kept
	if strings.Contains(resultE.Content, "[output cleared]") {
		t.Error("result-e should NOT have been cleared (within keep-recent window)")
	}
	if strings.Contains(resultF.Content, "[output cleared]") {
		t.Error("result-f should NOT have been cleared (within keep-recent window)")
	}
}

func TestClearOldToolResultsBelowThreshold(t *testing.T) {
	store := newClearingMockStorage()
	prov := &mockProvider{model: "test-model"}
	client := langdag.NewWithDeps(store, prov)

	agent := NewAgent(client, nil, nil, "", "test-model", 100000)

	// Input tokens = 50000, threshold = 80000 → should NOT trigger clearing.
	// (No ancestors needed since it won't even check.)
	agent.clearOldToolResults(context.Background(), "some-node", 50000)
	// No panic, no errors = success.
}

func TestClearOldToolResultsZeroContextWindow(t *testing.T) {
	store := newClearingMockStorage()
	prov := &mockProvider{model: "test-model"}
	client := langdag.NewWithDeps(store, prov)

	// contextWindow = 0 → clearing disabled.
	agent := NewAgent(client, nil, nil, "", "test-model", 0)
	agent.clearOldToolResults(context.Background(), "some-node", 90000)
	// No panic = success.
}

func TestClearOldToolResultsTooFewCandidates(t *testing.T) {
	store := newClearingMockStorage()
	prov := &mockProvider{model: "test-model"}
	client := langdag.NewWithDeps(store, prov)

	// Create a chain with only 3 tool results (< clearKeepRecent=4).
	now := time.Now()
	nodes := []*types.Node{
		{ID: "root", NodeType: types.NodeTypeUser, Content: "hi", CreatedAt: now},
	}
	for i := 0; i < 3; i++ {
		parentID := nodes[len(nodes)-1].ID
		resultID := "r-" + string(rune('a'+i))
		nodes = append(nodes, &types.Node{
			ID:       resultID,
			ParentID: parentID,
			NodeType: types.NodeTypeUser,
			Content:  toolResultContent("call_x", strings.Repeat("y", 500)),
		})
	}
	nodes = append(nodes, &types.Node{
		ID: "leaf", ParentID: nodes[len(nodes)-1].ID, NodeType: types.NodeTypeAssistant, Content: "ok",
	})

	var ancestorIDs []string
	for _, n := range nodes {
		_ = store.CreateNode(context.Background(), n)
		ancestorIDs = append(ancestorIDs, n.ID)
	}
	store.ancestorChains["leaf"] = ancestorIDs

	agent := NewAgent(client, nil, nil, "", "test-model", 100000)
	agent.clearOldToolResults(context.Background(), "leaf", 90000)

	// All 3 should be untouched (not enough to clear any).
	for i := 0; i < 3; i++ {
		id := "r-" + string(rune('a'+i))
		n, _ := store.GetNode(context.Background(), id)
		if strings.Contains(n.Content, "[output cleared]") {
			t.Errorf("node %s should NOT have been cleared", id)
		}
	}
}
