package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"langdag.com/langdag"
	"langdag.com/langdag/types"
)

func TestBuildTranscript(t *testing.T) {
	nodes := []*types.Node{
		{NodeType: types.NodeTypeUser, Content: "How do I sort a slice?"},
		{NodeType: types.NodeTypeAssistant, Content: "Use sort.Slice from the standard library."},
		{NodeType: types.NodeTypeUser, Content: toolResultContent("call_1", "ok")},
	}

	transcript := buildTranscript(nodes)

	if !strings.Contains(transcript, "How do I sort a slice?") {
		t.Error("transcript should contain user message")
	}
	if !strings.Contains(transcript, "sort.Slice") {
		t.Error("transcript should contain assistant text")
	}
	if !strings.Contains(transcript, "[Tool result: ok]") {
		t.Error("transcript should contain tool result status")
	}
}

func TestBuildTranscriptAssistantWithTools(t *testing.T) {
	assistantContent, _ := json.Marshal([]types.ContentBlock{
		{Type: "text", Text: "Let me check."},
		{Type: "tool_use", ID: "call_1", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)},
	})
	nodes := []*types.Node{
		{NodeType: types.NodeTypeAssistant, Content: string(assistantContent)},
	}

	transcript := buildTranscript(nodes)

	if !strings.Contains(transcript, "Let me check") {
		t.Error("transcript should contain assistant text")
	}
	if !strings.Contains(transcript, "[called bash]") {
		t.Error("transcript should mention tool calls")
	}
}

func TestCallLLMDirect(t *testing.T) {
	store := newClearingMockStorage()
	prov := &mockProvider{responses: []string{"This is the summary."}, model: "test-model"}
	client := langdag.NewWithDeps(store, prov)

	result, err := callLLMDirect(context.Background(), client, "test-model", "Summarize this.")
	if err != nil {
		t.Fatalf("callLLMDirect error: %v", err)
	}
	if result != "This is the summary." {
		t.Errorf("result = %q, want 'This is the summary.'", result)
	}
}

func TestCompactConversation(t *testing.T) {
	store := newClearingMockStorage()
	prov := &mockProvider{
		responses: []string{"Summary of the conversation: user asked about Go."},
		model:     "test-model",
	}
	client := langdag.NewWithDeps(store, prov)

	// Build a conversation with more nodes than compactKeepRecent.
	now := time.Now()
	nodeCount := compactKeepRecent + 6 // 6 old nodes to summarize
	nodes := make([]*types.Node, nodeCount)
	for i := 0; i < nodeCount; i++ {
		id := "node-" + string(rune('A'+i))
		parentID := ""
		if i > 0 {
			parentID = nodes[i-1].ID
		}
		nt := types.NodeTypeUser
		content := "User message " + string(rune('A'+i))
		if i%2 == 1 {
			nt = types.NodeTypeAssistant
			content = "Assistant response " + string(rune('A'+i))
		}
		nodes[i] = &types.Node{
			ID:        id,
			ParentID:  parentID,
			RootID:    "node-A",
			Sequence:  i,
			NodeType:  nt,
			Content:   content,
			CreatedAt: now.Add(time.Duration(i) * time.Minute),
		}
		if i == 0 {
			nodes[i].SystemPrompt = "You are a helpful assistant."
		}
	}

	// Store nodes and build ancestor chain.
	leafID := nodes[len(nodes)-1].ID
	var ancestorIDs []string
	for _, n := range nodes {
		_ = store.CreateNode(context.Background(), n)
		ancestorIDs = append(ancestorIDs, n.ID)
	}
	store.ancestorChains[leafID] = ancestorIDs

	result, err := compactConversation(context.Background(), client, leafID, "test-model", "")
	if err != nil {
		t.Fatalf("compactConversation error: %v", err)
	}

	if result.NewNodeID == "" {
		t.Fatal("NewNodeID should not be empty")
	}
	if result.Summary == "" {
		t.Fatal("Summary should not be empty")
	}
	if result.OriginalNodes != nodeCount {
		t.Errorf("OriginalNodes = %d, want %d", result.OriginalNodes, nodeCount)
	}
	if result.KeptNodes != compactKeepRecent {
		t.Errorf("KeptNodes = %d, want %d", result.KeptNodes, compactKeepRecent)
	}

	// Verify the new root contains the summary.
	newRoot, _ := store.GetNode(context.Background(), "")
	// Search all nodes for the compacted root.
	store.mu.Lock()
	var foundRoot bool
	for _, n := range store.nodes {
		if n.ParentID == "" && strings.Contains(n.Content, "Conversation compacted") {
			foundRoot = true
			if n.SystemPrompt != "You are a helpful assistant." {
				t.Error("compacted root should preserve system prompt")
			}
			break
		}
	}
	store.mu.Unlock()
	_ = newRoot

	if !foundRoot {
		t.Error("should find a compacted root node in storage")
	}
}

func TestCompactConversationTooShort(t *testing.T) {
	store := newClearingMockStorage()
	prov := &mockProvider{model: "test-model"}
	client := langdag.NewWithDeps(store, prov)

	// Only 3 nodes — too short to compact.
	nodes := []*types.Node{
		{ID: "a", NodeType: types.NodeTypeUser, Content: "hi"},
		{ID: "b", ParentID: "a", NodeType: types.NodeTypeAssistant, Content: "hello"},
		{ID: "c", ParentID: "b", NodeType: types.NodeTypeUser, Content: "bye"},
	}
	var ids []string
	for _, n := range nodes {
		_ = store.CreateNode(context.Background(), n)
		ids = append(ids, n.ID)
	}
	store.ancestorChains["c"] = ids

	_, err := compactConversation(context.Background(), client, "c", "test-model", "")
	if err == nil {
		t.Fatal("expected error for too-short conversation")
	}
	if !strings.Contains(err.Error(), "too short") {
		t.Errorf("error = %q, want 'too short'", err.Error())
	}
}

func TestMaybeCompactBelowThreshold(t *testing.T) {
	store := newClearingMockStorage()
	prov := &mockProvider{model: "test-model"}
	client := langdag.NewWithDeps(store, prov)

	agent := NewAgent(client, nil, nil, "", "test-model", 200000)

	// 100k tokens, threshold is 190k (95%) → should NOT compact.
	result := agent.maybeCompact(context.Background(), "node-1", 100000)
	if result != "node-1" {
		t.Errorf("should return same nodeID when below threshold, got %q", result)
	}
}

func TestMaybeCompactNoContextWindow(t *testing.T) {
	store := newClearingMockStorage()
	prov := &mockProvider{model: "test-model"}
	client := langdag.NewWithDeps(store, prov)

	agent := NewAgent(client, nil, nil, "", "test-model", 0)

	result := agent.maybeCompact(context.Background(), "node-1", 500000)
	if result != "node-1" {
		t.Errorf("should return same nodeID when context window is 0")
	}
}
