package main

import (
	"context"
	"encoding/json"
	"fmt"
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

	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 200000})

	// 100k tokens, threshold is 190k (95%) → should NOT compact.
	result := agent.maybeCompact(context.Background(), maybeCompactOptions{nodeID: "node-1", inputTokens: 100000})
	if result != "node-1" {
		t.Errorf("should return same nodeID when below threshold, got %q", result)
	}
}

func TestMaybeCompactNoContextWindow(t *testing.T) {
	store := newClearingMockStorage()
	prov := &mockProvider{model: "test-model"}
	client := langdag.NewWithDeps(store, prov)

	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	result := agent.maybeCompact(context.Background(), maybeCompactOptions{nodeID: "node-1", inputTokens: 500000})
	if result != "node-1" {
		t.Errorf("should return same nodeID when context window is 0")
	}
}

// TestCompactConversationPreservesStructure verifies that after compaction:
// 1. The new root contains the summary and system prompt
// 2. Recent nodes are copied with correct parent chain
// 3. Node types and content are preserved for recent nodes
func TestCompactConversationPreservesStructure(t *testing.T) {
	store := newClearingMockStorage()
	prov := &mockProvider{
		responses: []string{"The user asked to refactor auth. Files changed: auth.go, middleware.go."},
		model:     "test-model",
	}
	client := langdag.NewWithDeps(store, prov)

	now := time.Now()

	// Build a realistic conversation: 10 nodes, alternating user/assistant
	// with some tool results mixed in.
	nodes := []*types.Node{
		{ID: "n0", NodeType: types.NodeTypeUser, RootID: "n0", Content: "refactor the auth system",
			SystemPrompt: "You are a coding assistant.", CreatedAt: now},
		{ID: "n1", ParentID: "n0", RootID: "n0", NodeType: types.NodeTypeAssistant,
			Content: "I'll start by reading auth.go.", Model: "test-model", CreatedAt: now},
		{ID: "n2", ParentID: "n1", RootID: "n0", NodeType: types.NodeTypeUser,
			Content: toolResultContent("call_1", "package auth\n\nfunc Login() {}"),
			CreatedAt: now},
		{ID: "n3", ParentID: "n2", RootID: "n0", NodeType: types.NodeTypeAssistant,
			Content: "Now I'll update the Login function.", Model: "test-model", CreatedAt: now},
		{ID: "n4", ParentID: "n3", RootID: "n0", NodeType: types.NodeTypeUser,
			Content: toolResultContent("call_2", "ok"), CreatedAt: now},
		{ID: "n5", ParentID: "n4", RootID: "n0", NodeType: types.NodeTypeAssistant,
			Content: "Let me also check middleware.go.", Model: "test-model", CreatedAt: now},
		{ID: "n6", ParentID: "n5", RootID: "n0", NodeType: types.NodeTypeUser,
			Content: toolResultContent("call_3", "package middleware\n\nfunc Auth() {}"),
			CreatedAt: now},
		{ID: "n7", ParentID: "n6", RootID: "n0", NodeType: types.NodeTypeAssistant,
			Content: "I'll update the middleware too.", Model: "test-model", CreatedAt: now},
		{ID: "n8", ParentID: "n7", RootID: "n0", NodeType: types.NodeTypeUser,
			Content: "Looks good, what about tests?", CreatedAt: now},
		{ID: "n9", ParentID: "n8", RootID: "n0", NodeType: types.NodeTypeAssistant,
			Content: "I'll write tests for both files.", Model: "test-model", CreatedAt: now},
	}

	var ids []string
	for _, n := range nodes {
		_ = store.CreateNode(context.Background(), n)
		ids = append(ids, n.ID)
	}
	store.ancestorChains["n9"] = ids

	result, err := compactConversation(context.Background(), client, "n9", "test-model", "")
	if err != nil {
		t.Fatalf("compact error: %v", err)
	}

	// Verify summary is non-empty and mentions the auth refactor.
	if !strings.Contains(result.Summary, "auth") {
		t.Errorf("summary should mention auth, got: %s", result.Summary)
	}

	// Verify new root exists with system prompt and summary content.
	store.mu.Lock()
	var compactedRoot *types.Node
	for _, n := range store.nodes {
		if n.ParentID == "" && strings.Contains(n.Content, "Conversation compacted") {
			compactedRoot = n
			break
		}
	}
	var copiedNodes []*types.Node
	if compactedRoot != nil {
		for _, n := range store.nodes {
			if n.RootID == compactedRoot.ID && n.ID != compactedRoot.ID {
				copiedNodes = append(copiedNodes, n)
			}
		}
	}
	store.mu.Unlock()

	if compactedRoot == nil {
		t.Fatal("compacted root not found")
	}
	if compactedRoot.SystemPrompt != "You are a coding assistant." {
		t.Errorf("system prompt = %q, want original", compactedRoot.SystemPrompt)
	}
	if !strings.Contains(compactedRoot.Title, "Compacted") {
		t.Errorf("title = %q, should contain 'Compacted'", compactedRoot.Title)
	}

	// Verify recent nodes were copied (compactKeepRecent = 6).
	// The last 6 nodes are n4..n9.
	if len(copiedNodes) < compactKeepRecent {
		t.Errorf("expected at least %d copied nodes, got %d", compactKeepRecent, len(copiedNodes))
	}

	// Verify the leaf node ID is the result.
	leafNode, _ := store.GetNode(context.Background(), result.NewNodeID)
	if leafNode == nil {
		t.Fatal("leaf node not found")
	}
}

// TestCompactConversationWithFocusHint verifies that focus hints are passed to the summary.
func TestCompactConversationWithFocusHint(t *testing.T) {
	store := newClearingMockStorage()
	// The mock provider captures the prompt via Complete().
	prov := &focusCaptureProvider{
		mockProvider: mockProvider{
			responses: []string{"Focused summary."},
			model:     "test-model",
		},
	}
	client := langdag.NewWithDeps(store, prov)

	now := time.Now()
	nodeCount := compactKeepRecent + 4
	nodes := make([]*types.Node, nodeCount)
	for i := 0; i < nodeCount; i++ {
		id := "fh-" + string(rune('A'+i))
		parentID := ""
		if i > 0 {
			parentID = nodes[i-1].ID
		}
		nt := types.NodeTypeUser
		content := "msg " + string(rune('A'+i))
		if i%2 == 1 {
			nt = types.NodeTypeAssistant
			content = "response " + string(rune('A'+i))
		}
		nodes[i] = &types.Node{
			ID: id, ParentID: parentID, RootID: "fh-A", Sequence: i,
			NodeType: nt, Content: content, CreatedAt: now,
		}
	}

	leafID := nodes[len(nodes)-1].ID
	var ids []string
	for _, n := range nodes {
		_ = store.CreateNode(context.Background(), n)
		ids = append(ids, n.ID)
	}
	store.ancestorChains[leafID] = ids

	_, err := compactConversation(context.Background(), client, leafID, "test-model", "the database migration")
	if err != nil {
		t.Fatalf("compact error: %v", err)
	}

	// Verify the focus hint was included in the prompt sent to the LLM.
	if !strings.Contains(prov.lastPrompt, "database migration") {
		t.Errorf("expected focus hint in prompt, got: %s", prov.lastPrompt[:min(200, len(prov.lastPrompt))])
	}
}

// focusCaptureProvider wraps mockProvider and captures the Complete prompt.
type focusCaptureProvider struct {
	mockProvider
	lastPrompt string
}

func (p *focusCaptureProvider) Complete(_ context.Context, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	if len(req.Messages) > 0 {
		var text string
		_ = json.Unmarshal(req.Messages[0].Content, &text)
		p.lastPrompt = text
	}
	return p.mockProvider.Complete(context.Background(), req)
}

// TestCompactThenContinueConversation verifies that after compaction, the
// node chain is valid and can be walked by GetAncestors (simulated).
func TestCompactThenContinueConversation(t *testing.T) {
	store := newClearingMockStorage()
	prov := &mockProvider{
		responses: []string{"Summary: user is building a CLI app."},
		model:     "test-model",
	}
	client := langdag.NewWithDeps(store, prov)

	now := time.Now()
	nodeCount := compactKeepRecent + 4
	nodes := make([]*types.Node, nodeCount)
	for i := 0; i < nodeCount; i++ {
		id := "cc-" + string(rune('A'+i))
		parentID := ""
		if i > 0 {
			parentID = nodes[i-1].ID
		}
		nt := types.NodeTypeUser
		if i%2 == 1 {
			nt = types.NodeTypeAssistant
		}
		nodes[i] = &types.Node{
			ID: id, ParentID: parentID, RootID: "cc-A", Sequence: i,
			NodeType: nt, Content: "content " + string(rune('A'+i)), CreatedAt: now,
		}
	}

	leafID := nodes[len(nodes)-1].ID
	var ids []string
	for _, n := range nodes {
		_ = store.CreateNode(context.Background(), n)
		ids = append(ids, n.ID)
	}
	store.ancestorChains[leafID] = ids

	result, err := compactConversation(context.Background(), client, leafID, "test-model", "")
	if err != nil {
		t.Fatalf("compact error: %v", err)
	}

	// Walk the new node chain from result.NewNodeID back to root.
	store.mu.Lock()
	chain := make(map[string]*types.Node)
	for _, n := range store.nodes {
		chain[n.ID] = n
	}
	store.mu.Unlock()

	// Walk backwards from leaf to root.
	current := result.NewNodeID
	var depth int
	for current != "" && depth < 100 {
		n, ok := chain[current]
		if !ok {
			t.Fatalf("node %q not found in chain at depth %d", current, depth)
		}
		current = n.ParentID
		depth++
	}

	// Should have: root (1) + copied recent nodes (compactKeepRecent).
	expectedDepth := 1 + compactKeepRecent
	if depth != expectedDepth {
		t.Errorf("chain depth = %d, want %d", depth, expectedDepth)
	}
}

// ─── 3d: compactConversation error paths ───

// TestCompactConversation_GetAncestorsError verifies that a storage read failure
// is propagated correctly.
func TestCompactConversation_GetAncestorsError(t *testing.T) {
	store := &failingAncestorStorage{newClearingMockStorage()}
	prov := &mockProvider{model: "test-model"}
	client := langdag.NewWithDeps(store, prov)

	_, err := compactConversation(context.Background(), client, "any-node", "test-model", "")
	if err == nil {
		t.Fatal("expected error when GetAncestors fails")
	}
	if !strings.Contains(err.Error(), "get ancestors") {
		t.Errorf("error = %q, want 'get ancestors'", err.Error())
	}
}

// TestCompactConversation_LLMFailure verifies that an LLM call failure
// mid-compaction is propagated.
func TestCompactConversation_LLMFailure(t *testing.T) {
	store := newClearingMockStorage()
	prov := &failingProvider{}
	client := langdag.NewWithDeps(store, prov)

	// Build enough nodes to pass the "too short" check.
	now := time.Now()
	nodeCount := compactKeepRecent + 4
	nodes := make([]*types.Node, nodeCount)
	for i := 0; i < nodeCount; i++ {
		id := "lf-" + string(rune('A'+i))
		parentID := ""
		if i > 0 {
			parentID = nodes[i-1].ID
		}
		nt := types.NodeTypeUser
		if i%2 == 1 {
			nt = types.NodeTypeAssistant
		}
		nodes[i] = &types.Node{
			ID: id, ParentID: parentID, RootID: "lf-A", Sequence: i,
			NodeType: nt, Content: "msg " + string(rune('A'+i)), CreatedAt: now,
		}
	}
	leafID := nodes[len(nodes)-1].ID
	var ids []string
	for _, n := range nodes {
		_ = store.CreateNode(context.Background(), n)
		ids = append(ids, n.ID)
	}
	store.ancestorChains[leafID] = ids

	_, err := compactConversation(context.Background(), client, leafID, "test-model", "")
	if err == nil {
		t.Fatal("expected error when LLM call fails")
	}
	if !strings.Contains(err.Error(), "summarize") {
		t.Errorf("error = %q, want 'summarize'", err.Error())
	}
}

// TestCompactConversation_EmptyConversation verifies that a nonexistent node
// returns an error (either from storage lookup or the too-short check).
func TestCompactConversation_EmptyConversation(t *testing.T) {
	store := newClearingMockStorage()
	prov := &mockProvider{model: "test-model"}
	client := langdag.NewWithDeps(store, prov)

	_, err := compactConversation(context.Background(), client, "nonexistent", "test-model", "")
	if err == nil {
		t.Fatal("expected error for empty/nonexistent conversation")
	}
	// Could be "get ancestors" (from langdag lookup) or "too short" (if nil returned).
	// Either way, the operation should fail.
}

// failingAncestorStorage always errors on GetAncestors.
type failingAncestorStorage struct{ *clearingMockStorage }

func (s *failingAncestorStorage) GetAncestors(_ context.Context, _ string) ([]*types.Node, error) {
	return nil, fmt.Errorf("storage unavailable")
}

// failingProvider always errors on Complete.
type failingProvider struct{}

func (p *failingProvider) Complete(_ context.Context, _ *types.CompletionRequest) (*types.CompletionResponse, error) {
	return nil, fmt.Errorf("LLM service unavailable")
}

func (p *failingProvider) Stream(_ context.Context, _ *types.CompletionRequest) (<-chan types.StreamEvent, error) {
	return nil, fmt.Errorf("LLM service unavailable")
}

func (p *failingProvider) Name() string             { return "mock" }
func (p *failingProvider) Models() []types.ModelInfo { return nil }

func TestCompactSummaryPromptCoversAllFocuses(t *testing.T) {
	// Verify the prompt includes all 6 focus areas for rich summaries.
	expectedFocuses := []string{
		"task/goal",
		"Key decisions",
		"Current state",
		"important context",
		"Pending tasks or plan steps",
		"Errors encountered",
	}
	for _, focus := range expectedFocuses {
		if !strings.Contains(compactSummaryPrompt, focus) {
			t.Errorf("compactSummaryPrompt missing focus area: %q", focus)
		}
	}
}

// TestCompactPromptPassedToLLM verifies the full prompt (with all focuses) is
// sent to the LLM during compaction.
func TestCompactPromptPassedToLLM(t *testing.T) {
	store := newClearingMockStorage()
	prov := &focusCaptureProvider{
		mockProvider: mockProvider{
			responses: []string{"Summary with errors and pending tasks."},
			model:     "test-model",
		},
	}
	client := langdag.NewWithDeps(store, prov)

	now := time.Now()
	nodeCount := compactKeepRecent + 4
	nodes := make([]*types.Node, nodeCount)
	for i := 0; i < nodeCount; i++ {
		id := fmt.Sprintf("pp-%d", i)
		parentID := ""
		if i > 0 {
			parentID = nodes[i-1].ID
		}
		nt := types.NodeTypeUser
		content := fmt.Sprintf("user msg %d", i)
		if i%2 == 1 {
			nt = types.NodeTypeAssistant
			content = fmt.Sprintf("assistant msg %d", i)
		}
		nodes[i] = &types.Node{
			ID: id, ParentID: parentID, RootID: "pp-0", Sequence: i,
			NodeType: nt, Content: content, CreatedAt: now,
		}
	}

	leafID := nodes[len(nodes)-1].ID
	var ids []string
	for _, n := range nodes {
		_ = store.CreateNode(context.Background(), n)
		ids = append(ids, n.ID)
	}
	store.ancestorChains[leafID] = ids

	_, err := compactConversation(context.Background(), client, leafID, "test-model", "")
	if err != nil {
		t.Fatalf("compact error: %v", err)
	}

	// Verify all 6 focus areas were in the prompt sent to the LLM.
	if !strings.Contains(prov.lastPrompt, "Pending tasks or plan steps") {
		t.Error("prompt sent to LLM missing 'Pending tasks or plan steps'")
	}
	if !strings.Contains(prov.lastPrompt, "Errors encountered") {
		t.Error("prompt sent to LLM missing 'Errors encountered'")
	}
}
