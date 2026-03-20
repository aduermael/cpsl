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

// testTool is a minimal Tool implementation for agent tests.
type testTool struct {
	name             string
	result           string
	err              error
	requiresApproval bool
}

func (t *testTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{Name: t.name, Description: "test tool " + t.name}
}

func (t *testTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return t.result, t.err
}

func (t *testTool) RequiresApproval(_ json.RawMessage) bool {
	return t.requiresApproval
}

// blockingTool blocks Execute until the release channel is closed.
type blockingTool struct {
	name    string
	release chan struct{}
}

func (t *blockingTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{Name: t.name, Description: "blocking tool"}
}

func (t *blockingTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	select {
	case <-t.release:
		return "released", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (t *blockingTool) RequiresApproval(_ json.RawMessage) bool { return false }

// --- Task 1a: NewAgent with option funcs ---

func TestNewAgentDefaults(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(client, nil, nil, "system prompt", "test-model", 100000)

	if agent.client != client {
		t.Error("client not set")
	}
	if agent.systemPrompt != "system prompt" {
		t.Errorf("systemPrompt = %q, want %q", agent.systemPrompt, "system prompt")
	}
	if agent.model != "test-model" {
		t.Errorf("model = %q, want %q", agent.model, "test-model")
	}
	if agent.contextWindow != 100000 {
		t.Errorf("contextWindow = %d, want 100000", agent.contextWindow)
	}
	if agent.id == "" {
		t.Error("agent ID should not be empty")
	}
	if agent.events == nil {
		t.Error("events channel should not be nil")
	}
	if agent.approval == nil {
		t.Error("approval channel should not be nil")
	}
	// Default option values
	if agent.explorationModel != "" {
		t.Errorf("explorationModel = %q, want empty", agent.explorationModel)
	}
	if agent.maxToolIterations != 0 {
		t.Errorf("maxToolIterations = %d, want 0 (uses default)", agent.maxToolIterations)
	}
}

func TestNewAgentWithContextWindow(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(client, nil, nil, "", "", 0, WithContextWindow(200000))

	if agent.contextWindow != 200000 {
		t.Errorf("contextWindow = %d, want 200000", agent.contextWindow)
	}
}

func TestNewAgentWithExplorationModel(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(client, nil, nil, "", "", 0, WithExplorationModel("cheap-model"))

	if agent.explorationModel != "cheap-model" {
		t.Errorf("explorationModel = %q, want %q", agent.explorationModel, "cheap-model")
	}
}

func TestNewAgentWithMaxToolIterations(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(client, nil, nil, "", "", 0, WithMaxToolIterations(50))

	if agent.maxToolIterations != 50 {
		t.Errorf("maxToolIterations = %d, want 50", agent.maxToolIterations)
	}
}

func TestNewAgentMultipleOptions(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(client, nil, nil, "", "model-x", 0,
		WithContextWindow(150000),
		WithExplorationModel("summary-model"),
		WithMaxToolIterations(10),
	)

	if agent.contextWindow != 150000 {
		t.Errorf("contextWindow = %d, want 150000", agent.contextWindow)
	}
	if agent.explorationModel != "summary-model" {
		t.Errorf("explorationModel = %q, want %q", agent.explorationModel, "summary-model")
	}
	if agent.maxToolIterations != 10 {
		t.Errorf("maxToolIterations = %d, want 10", agent.maxToolIterations)
	}
}

func TestNewAgentToolRegistration(t *testing.T) {
	client := newTestClient("ok")
	tools := []Tool{&testTool{name: "bash", result: "ok"}, &testTool{name: "read", result: "contents"}}

	agent := NewAgent(client, tools, nil, "", "", 0)

	if len(agent.tools) != 2 {
		t.Fatalf("tools map len = %d, want 2", len(agent.tools))
	}
	if _, ok := agent.tools["bash"]; !ok {
		t.Error("tool 'bash' not registered")
	}
	if _, ok := agent.tools["read"]; !ok {
		t.Error("tool 'read' not registered")
	}
	if len(agent.toolDefs) != 2 {
		t.Errorf("toolDefs len = %d, want 2", len(agent.toolDefs))
	}
}

func TestNewAgentServerTools(t *testing.T) {
	client := newTestClient("ok")
	tools := []Tool{&testTool{name: "bash", result: "ok"}}
	serverTools := []types.ToolDefinition{
		{Name: "web_search", Description: "Search the web"},
	}

	agent := NewAgent(client, tools, serverTools, "", "", 0)

	// toolDefs should contain both client tools and server tools.
	if len(agent.toolDefs) != 2 {
		t.Errorf("toolDefs len = %d, want 2", len(agent.toolDefs))
	}
	// Server tools should NOT be in the tools map (they're provider-executed).
	if _, ok := agent.tools["web_search"]; ok {
		t.Error("server tool should not be in tools map")
	}
}

// --- Task 1b: newLangdagClient ---

func TestNewLangdagClientSelectsFirstAvailableProvider(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Anthropic key present → should select Anthropic.
	cfg := Config{AnthropicAPIKey: "sk-ant-test"}
	client, err := newLangdagClient(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewLangdagClientFallsThrough(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Only OpenAI key present → should select OpenAI.
	cfg := Config{OpenAIAPIKey: "sk-openai-test"}
	client, err := newLangdagClient(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client when OpenAI key is set")
	}
}

func TestNewLangdagClientNoKeys(t *testing.T) {
	// No API keys → returns nil, nil.
	cfg := Config{}
	client, err := newLangdagClient(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client != nil {
		t.Error("expected nil client when no keys configured")
	}
}

// --- Task 1c: newLangdagClientForProvider ---

func TestNewLangdagClientForProviderBranches(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	tests := []struct {
		provider string
		cfg      Config
	}{
		{ProviderAnthropic, Config{AnthropicAPIKey: "key-a"}},
		{ProviderOpenAI, Config{OpenAIAPIKey: "key-o"}},
		{ProviderGrok, Config{GrokAPIKey: "key-g"}},
		{ProviderGemini, Config{GeminiAPIKey: "key-m"}},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			client, err := newLangdagClientForProvider(tt.cfg, tt.provider)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if client == nil {
				t.Fatal("expected non-nil client")
			}
		})
	}
}

func TestNewLangdagClientForProviderInvalid(t *testing.T) {
	_, err := newLangdagClientForProvider(Config{}, "unsupported-provider")
	if err == nil {
		t.Fatal("expected error for unsupported provider")
	}
	if !strings.Contains(err.Error(), "unsupported provider") {
		t.Errorf("error = %q, want to contain 'unsupported provider'", err.Error())
	}
}

// --- Task 1d: generateAgentID ---

func TestGenerateAgentIDFormat(t *testing.T) {
	id := generateAgentID()
	// 4 random bytes → 8 hex characters.
	if len(id) != 8 {
		t.Errorf("id length = %d, want 8", len(id))
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("id contains non-hex char: %c", c)
		}
	}
}

func TestGenerateAgentIDUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := generateAgentID()
		if seen[id] {
			t.Fatalf("duplicate ID after %d calls: %s", i, id)
		}
		seen[id] = true
	}
}

// --- Task 1e: langdagStoragePath ---

func TestLangdagStoragePath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	path := langdagStoragePath()

	// Should be under HOME/.herm/conversations.db
	wantDir := filepath.Join(tmp, ".herm")
	wantPath := filepath.Join(wantDir, "conversations.db")
	if path != wantPath {
		t.Errorf("path = %q, want %q", path, wantPath)
	}

	// Directory should have been created.
	info, err := os.Stat(wantDir)
	if err != nil {
		t.Fatalf("directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected directory, got file")
	}
}

// --- Task 1f: Run() and runLoop() ---

// scriptedResponse describes a single LLM response for the scripted provider.
type scriptedResponse struct {
	text       string               // text to stream
	toolCalls  []types.ContentBlock // tool_use blocks to include
	tokensIn   int
	tokensOut  int
}

// scriptedProvider returns pre-defined responses with optional tool calls.
type scriptedProvider struct {
	mu        sync.Mutex
	responses []scriptedResponse
	callIdx   int
	model     string
}

func (p *scriptedProvider) Complete(_ context.Context, _ *types.CompletionRequest) (*types.CompletionResponse, error) {
	p.mu.Lock()
	idx := p.callIdx
	p.callIdx++
	p.mu.Unlock()

	if idx >= len(p.responses) {
		return &types.CompletionResponse{
			ID: "resp-test", Model: p.model,
			Content: []types.ContentBlock{{Type: "text", Text: "ok"}},
			StopReason: "end_turn",
			Usage: types.Usage{InputTokens: 100, OutputTokens: 50},
		}, nil
	}
	r := p.responses[idx]
	content := []types.ContentBlock{{Type: "text", Text: r.text}}
	content = append(content, r.toolCalls...)
	stopReason := "end_turn"
	if len(r.toolCalls) > 0 {
		stopReason = "tool_use"
	}
	return &types.CompletionResponse{
		ID: fmt.Sprintf("resp-%d", idx), Model: p.model,
		Content:    content,
		StopReason: stopReason,
		Usage:      types.Usage{InputTokens: r.tokensIn, OutputTokens: r.tokensOut},
	}, nil
}

func (p *scriptedProvider) Stream(_ context.Context, req *types.CompletionRequest) (<-chan types.StreamEvent, error) {
	p.mu.Lock()
	idx := p.callIdx
	p.callIdx++
	p.mu.Unlock()

	ch := make(chan types.StreamEvent, 20)
	go func() {
		defer close(ch)

		var r scriptedResponse
		if idx < len(p.responses) {
			r = p.responses[idx]
		} else {
			r = scriptedResponse{text: "ok", tokensIn: 100, tokensOut: 50}
		}

		// Stream text delta
		if r.text != "" {
			ch <- types.StreamEvent{
				Type:    types.StreamEventDelta,
				Content: r.text,
			}
		}

		// Stream tool_use content blocks
		for _, tc := range r.toolCalls {
			tc := tc
			ch <- types.StreamEvent{
				Type:         types.StreamEventContentDone,
				ContentBlock: &tc,
			}
		}

		// Build complete response for done event
		content := []types.ContentBlock{{Type: "text", Text: r.text}}
		content = append(content, r.toolCalls...)
		stopReason := "end_turn"
		if len(r.toolCalls) > 0 {
			stopReason = "tool_use"
		}

		ch <- types.StreamEvent{
			Type: types.StreamEventDone,
			Response: &types.CompletionResponse{
				ID:         fmt.Sprintf("resp-%d", idx),
				Model:      req.Model,
				Content:    content,
				StopReason: stopReason,
				Usage:      types.Usage{InputTokens: r.tokensIn, OutputTokens: r.tokensOut},
			},
		}
	}()
	return ch, nil
}

func (p *scriptedProvider) Name() string             { return "mock" }
func (p *scriptedProvider) Models() []types.ModelInfo { return nil }

// drainEvents collects all agent events until EventDone, with a timeout.
func drainEvents(t *testing.T, ch <-chan AgentEvent, timeout time.Duration) []AgentEvent {
	t.Helper()
	var events []AgentEvent
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case ev := <-ch:
			events = append(events, ev)
			if ev.Type == EventDone {
				return events
			}
		case <-timer.C:
			t.Fatal("timeout waiting for EventDone")
			return events
		}
	}
}

func TestRunTextStreaming(t *testing.T) {
	prov := &scriptedProvider{
		model:     "test-model",
		responses: []scriptedResponse{{text: "Hello world", tokensIn: 100, tokensOut: 50}},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	agent := NewAgent(client, nil, nil, "", "test-model", 0)

	go agent.Run(context.Background(), "hi", "")
	events := drainEvents(t, agent.Events(), 5*time.Second)

	// Should have at least: TextDelta, Usage, Done
	var hasText, hasUsage, hasDone bool
	for _, ev := range events {
		switch ev.Type {
		case EventTextDelta:
			hasText = true
			if ev.Text != "Hello world" {
				t.Errorf("text = %q, want %q", ev.Text, "Hello world")
			}
		case EventUsage:
			hasUsage = true
		case EventDone:
			hasDone = true
		}
		if ev.AgentID == "" {
			t.Errorf("event type %d has empty AgentID", ev.Type)
		}
	}
	if !hasText {
		t.Error("expected EventTextDelta")
	}
	if !hasUsage {
		t.Error("expected EventUsage")
	}
	if !hasDone {
		t.Error("expected EventDone")
	}
}

func TestRunToolCallDispatch(t *testing.T) {
	// Call 0: return a tool call, call 1: return text (after tool result).
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "Let me run that.",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "call_1", Name: "bash", Input: json.RawMessage(`{"cmd":"ls"}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "Done!", tokensIn: 200, tokensOut: 30},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	bashTool := &testTool{name: "bash", result: "file1.txt\nfile2.txt"}
	agent := NewAgent(client, []Tool{bashTool}, nil, "", "test-model", 0)

	go agent.Run(context.Background(), "list files", "")
	events := drainEvents(t, agent.Events(), 5*time.Second)

	var hasToolStart, hasToolDone, hasToolResult bool
	for _, ev := range events {
		switch ev.Type {
		case EventToolCallStart:
			hasToolStart = true
			if ev.ToolName != "bash" {
				t.Errorf("tool name = %q, want bash", ev.ToolName)
			}
			if ev.ToolID != "call_1" {
				t.Errorf("tool ID = %q, want call_1", ev.ToolID)
			}
		case EventToolCallDone:
			hasToolDone = true
			if !strings.Contains(ev.ToolResult, "file1.txt") {
				t.Errorf("tool result = %q, want to contain file1.txt", ev.ToolResult)
			}
		case EventToolResult:
			hasToolResult = true
			if ev.IsError {
				t.Error("tool result should not be an error")
			}
		}
	}
	if !hasToolStart {
		t.Error("expected EventToolCallStart")
	}
	if !hasToolDone {
		t.Error("expected EventToolCallDone")
	}
	if !hasToolResult {
		t.Error("expected EventToolResult")
	}
}

func TestRunUnknownTool(t *testing.T) {
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "call_1", Name: "nonexistent", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "ok", tokensIn: 200, tokensOut: 30},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	agent := NewAgent(client, nil, nil, "", "test-model", 0)

	go agent.Run(context.Background(), "do something", "")
	events := drainEvents(t, agent.Events(), 5*time.Second)

	var hasToolError bool
	for _, ev := range events {
		if ev.Type == EventToolResult && ev.IsError && strings.Contains(ev.ToolResult, "unknown tool") {
			hasToolError = true
		}
	}
	if !hasToolError {
		t.Error("expected error for unknown tool")
	}
}

func TestRunApprovalFlow(t *testing.T) {
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "Running dangerous command.",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "call_1", Name: "bash", Input: json.RawMessage(`{"cmd":"rm -rf /"}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "Command executed.", tokensIn: 200, tokensOut: 30},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	bashTool := &testTool{name: "bash", result: "deleted", requiresApproval: true}
	agent := NewAgent(client, []Tool{bashTool}, nil, "", "test-model", 0)

	go agent.Run(context.Background(), "delete everything", "")

	// Wait for approval request, then approve.
	var approvalReceived bool
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()

loop:
	for {
		select {
		case ev := <-agent.Events():
			if ev.Type == EventApprovalReq {
				approvalReceived = true
				if ev.ToolName != "bash" {
					t.Errorf("approval tool = %q, want bash", ev.ToolName)
				}
				agent.Approve(ApprovalResponse{Approved: true})
			}
			if ev.Type == EventDone {
				break loop
			}
		case <-timeout.C:
			t.Fatal("timeout")
		}
	}

	if !approvalReceived {
		t.Error("expected EventApprovalReq")
	}
}

func TestRunApprovalDenied(t *testing.T) {
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "call_1", Name: "bash", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "ok denied", tokensIn: 200, tokensOut: 30},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	bashTool := &testTool{name: "bash", result: "should not run", requiresApproval: true}
	agent := NewAgent(client, []Tool{bashTool}, nil, "", "test-model", 0)

	go agent.Run(context.Background(), "do it", "")

	var deniedResult bool
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()

loop:
	for {
		select {
		case ev := <-agent.Events():
			if ev.Type == EventApprovalReq {
				agent.Approve(ApprovalResponse{Approved: false})
			}
			if ev.Type == EventToolResult && ev.IsError && strings.Contains(ev.ToolResult, "denied") {
				deniedResult = true
			}
			if ev.Type == EventDone {
				break loop
			}
		case <-timeout.C:
			t.Fatal("timeout")
		}
	}

	if !deniedResult {
		t.Error("expected denied tool result")
	}
}

func TestRunCancel(t *testing.T) {
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "call_1", Name: "bash", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	// Tool that blocks until context is cancelled.
	blockingTool := &testTool{name: "bash", result: "ok"}
	agent := NewAgent(client, []Tool{blockingTool}, nil, "", "test-model", 0)

	go agent.Run(context.Background(), "run", "")

	// Wait briefly for the run to start, then cancel.
	time.Sleep(100 * time.Millisecond)
	agent.Cancel()

	// Should eventually get EventDone.
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case ev := <-agent.Events():
			if ev.Type == EventDone {
				return // success
			}
		case <-timeout.C:
			t.Fatal("timeout waiting for EventDone after Cancel()")
		}
	}
}

func TestRunMaxToolIterations(t *testing.T) {
	// Provider always returns a tool call — agent should stop after max iterations.
	responses := make([]scriptedResponse, 10)
	for i := range responses {
		responses[i] = scriptedResponse{
			text: "",
			toolCalls: []types.ContentBlock{
				{Type: "tool_use", ID: fmt.Sprintf("call_%d", i), Name: "bash", Input: json.RawMessage(`{}`)},
			},
			tokensIn: 100, tokensOut: 50,
		}
	}
	prov := &scriptedProvider{model: "test-model", responses: responses}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	bashTool := &testTool{name: "bash", result: "ok"}
	agent := NewAgent(client, []Tool{bashTool}, nil, "", "test-model", 0,
		WithMaxToolIterations(3),
	)

	go agent.Run(context.Background(), "loop", "")
	events := drainEvents(t, agent.Events(), 5*time.Second)

	// Count tool call starts — should be capped at 3 iterations.
	var toolStarts int
	for _, ev := range events {
		if ev.Type == EventToolCallStart {
			toolStarts++
		}
	}
	if toolStarts > 3 {
		t.Errorf("tool starts = %d, want ≤ 3 (max iterations)", toolStarts)
	}
}

func TestRunAlreadyRunning(t *testing.T) {
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "call_1", Name: "blocker", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "done", tokensIn: 100, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	release := make(chan struct{})
	bt := &blockingTool{name: "blocker", release: release}
	agent := NewAgent(client, []Tool{bt}, nil, "", "test-model", 0)

	// Start first run — it will block in the tool execution.
	go agent.Run(context.Background(), "first", "")

	// Wait for the tool call to start (agent is definitely running).
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case ev := <-agent.Events():
			if ev.Type == EventToolCallStart {
				goto secondRun
			}
		case <-timeout.C:
			t.Fatal("timeout waiting for tool call start")
		}
	}

secondRun:
	// Start second run — should emit an error about already running.
	go agent.Run(context.Background(), "second", "")

	// Collect the "already running" error, then release the blocker.
	var gotAlreadyRunning bool
	timeout2 := time.NewTimer(5 * time.Second)
	defer timeout2.Stop()
	for {
		select {
		case ev := <-agent.Events():
			if ev.Type == EventError && ev.Error != nil && strings.Contains(ev.Error.Error(), "already running") {
				gotAlreadyRunning = true
				close(release) // unblock first run
			}
			if ev.Type == EventDone && gotAlreadyRunning {
				if !gotAlreadyRunning {
					t.Error("expected 'already running' error")
				}
				return
			}
		case <-timeout2.C:
			if !gotAlreadyRunning {
				t.Fatal("timeout: never got 'already running' error")
			}
			return
		}
	}
}

// --- Task 1g: emit() and emitUsage() ---

func TestEmitSetsAgentID(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(client, nil, nil, "", "test-model", 0)

	agent.emit(AgentEvent{Type: EventTextDelta, Text: "hello"})

	select {
	case ev := <-agent.Events():
		if ev.AgentID != agent.id {
			t.Errorf("AgentID = %q, want %q", ev.AgentID, agent.id)
		}
		if ev.Text != "hello" {
			t.Errorf("Text = %q, want %q", ev.Text, "hello")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestEmitUsageReturnsInputTokens(t *testing.T) {
	store := newMockStorage()
	prov := &mockProvider{model: "test-model"}
	client := langdag.NewWithDeps(store, prov)

	// Store a node with known token counts.
	_ = store.CreateNode(context.Background(), &types.Node{
		ID:                  "node-1",
		NodeType:            types.NodeTypeAssistant,
		Model:               "test-model",
		TokensIn:            5000,
		TokensOut:           200,
		TokensCacheRead:     1000,
		TokensCacheCreation: 500,
		TokensReasoning:     100,
	})

	agent := NewAgent(client, nil, nil, "", "test-model", 0)
	inputTokens := agent.emitUsage(context.Background(), "node-1")

	// Input tokens = TokensIn + TokensCacheRead = 5000 + 1000 = 6000.
	if inputTokens != 6000 {
		t.Errorf("inputTokens = %d, want 6000", inputTokens)
	}

	// Should have emitted a usage event.
	select {
	case ev := <-agent.Events():
		if ev.Type != EventUsage {
			t.Errorf("event type = %d, want EventUsage", ev.Type)
		}
		if ev.Model != "test-model" {
			t.Errorf("model = %q, want test-model", ev.Model)
		}
		if ev.Usage == nil {
			t.Fatal("usage is nil")
		}
		if ev.Usage.InputTokens != 5000 {
			t.Errorf("InputTokens = %d, want 5000", ev.Usage.InputTokens)
		}
		if ev.Usage.OutputTokens != 200 {
			t.Errorf("OutputTokens = %d, want 200", ev.Usage.OutputTokens)
		}
		if ev.Usage.CacheReadInputTokens != 1000 {
			t.Errorf("CacheReadInputTokens = %d, want 1000", ev.Usage.CacheReadInputTokens)
		}
		if ev.Usage.ReasoningTokens != 100 {
			t.Errorf("ReasoningTokens = %d, want 100", ev.Usage.ReasoningTokens)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for usage event")
	}
}

func TestEmitUsageEmptyNodeID(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(client, nil, nil, "", "test-model", 0)

	inputTokens := agent.emitUsage(context.Background(), "")
	if inputTokens != 0 {
		t.Errorf("inputTokens = %d, want 0 for empty nodeID", inputTokens)
	}
}

func TestEmitUsageMissingNode(t *testing.T) {
	store := newMockStorage()
	prov := &mockProvider{model: "test-model"}
	client := langdag.NewWithDeps(store, prov)
	agent := NewAgent(client, nil, nil, "", "test-model", 0)

	// Node doesn't exist in storage.
	inputTokens := agent.emitUsage(context.Background(), "nonexistent")
	if inputTokens != 0 {
		t.Errorf("inputTokens = %d, want 0 for missing node", inputTokens)
	}
}

// --- Phase 4: Parallel agent execution ---

// parallelTracker is a tool that tracks concurrent execution. Each Execute call
// increments a running counter, blocks on a gate channel, then decrements.
// Peak concurrency is recorded in maxConc.
type parallelTracker struct {
	name    string
	result  string
	mu      sync.Mutex
	running int
	maxConc int
	gate    chan struct{}
}

func (t *parallelTracker) Definition() types.ToolDefinition {
	return types.ToolDefinition{Name: t.name, Description: "parallel tracking tool"}
}

func (t *parallelTracker) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	t.mu.Lock()
	t.running++
	if t.running > t.maxConc {
		t.maxConc = t.running
	}
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		t.running--
		t.mu.Unlock()
	}()

	select {
	case <-t.gate:
		return t.result, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (t *parallelTracker) RequiresApproval(_ json.RawMessage) bool { return false }

func TestRunParallelAgentCalls(t *testing.T) {
	// LLM returns 3 agent tool calls in one response.
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "Spawning agents",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "a1", Name: "agent", Input: json.RawMessage(`{"task":"t1"}`)},
					{Type: "tool_use", ID: "a2", Name: "agent", Input: json.RawMessage(`{"task":"t2"}`)},
					{Type: "tool_use", ID: "a3", Name: "agent", Input: json.RawMessage(`{"task":"t3"}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "All done!", tokensIn: 200, tokensOut: 30},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	gate := make(chan struct{})
	tracker := &parallelTracker{name: "agent", result: "agent result", gate: gate}
	agent := NewAgent(client, []Tool{tracker}, nil, "", "test-model", 0)

	go agent.Run(context.Background(), "spawn agents", "")

	// Wait for all 3 to be running concurrently.
	deadline := time.After(5 * time.Second)
	for {
		tracker.mu.Lock()
		r := tracker.running
		tracker.mu.Unlock()
		if r >= 3 {
			break
		}
		select {
		case <-deadline:
			tracker.mu.Lock()
			t.Fatalf("timeout: only %d concurrent, want 3", tracker.running)
			tracker.mu.Unlock()
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// All 3 running in parallel — release them.
	close(gate)

	events := drainEvents(t, agent.Events(), 5*time.Second)

	// Verify peak concurrency was 3 (proves parallel execution).
	tracker.mu.Lock()
	mc := tracker.maxConc
	tracker.mu.Unlock()
	if mc < 3 {
		t.Errorf("max concurrency = %d, want 3", mc)
	}

	// All 3 agent tool results should be present.
	var agentResults int
	for _, ev := range events {
		if ev.Type == EventToolResult && ev.ToolName == "agent" {
			agentResults++
			if ev.ToolResult != "agent result" {
				t.Errorf("agent result = %q, want 'agent result'", ev.ToolResult)
			}
		}
	}
	if agentResults != 3 {
		t.Errorf("agent results = %d, want 3", agentResults)
	}
}

func TestRunMixedSequentialAndParallelCalls(t *testing.T) {
	// LLM returns a bash call and 2 agent calls in one response.
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "bash_1", Name: "bash", Input: json.RawMessage(`{}`)},
					{Type: "tool_use", ID: "agent_1", Name: "agent", Input: json.RawMessage(`{"task":"t1"}`)},
					{Type: "tool_use", ID: "agent_2", Name: "agent", Input: json.RawMessage(`{"task":"t2"}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "Done", tokensIn: 200, tokensOut: 30},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	bashTool := &testTool{name: "bash", result: "bash output"}
	agentTool := &testTool{name: "agent", result: "agent output"}
	agent := NewAgent(client, []Tool{bashTool, agentTool}, nil, "", "test-model", 0)

	go agent.Run(context.Background(), "do things", "")
	events := drainEvents(t, agent.Events(), 5*time.Second)

	// Collect all tool results by ID.
	toolResults := make(map[string]string)
	for _, ev := range events {
		if ev.Type == EventToolResult {
			toolResults[ev.ToolID] = ev.ToolResult
		}
	}
	if toolResults["bash_1"] != "bash output" {
		t.Errorf("bash result = %q, want 'bash output'", toolResults["bash_1"])
	}
	if toolResults["agent_1"] != "agent output" {
		t.Errorf("agent_1 result = %q, want 'agent output'", toolResults["agent_1"])
	}
	if toolResults["agent_2"] != "agent output" {
		t.Errorf("agent_2 result = %q, want 'agent output'", toolResults["agent_2"])
	}
}

func TestRunParallelAgentCancellation(t *testing.T) {
	// LLM returns 2 agent calls; we cancel before they finish.
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "a1", Name: "agent", Input: json.RawMessage(`{"task":"t1"}`)},
					{Type: "tool_use", ID: "a2", Name: "agent", Input: json.RawMessage(`{"task":"t2"}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	gate := make(chan struct{}) // never closed — agents block until cancelled
	tracker := &parallelTracker{name: "agent", result: "ok", gate: gate}
	agent := NewAgent(client, []Tool{tracker}, nil, "", "test-model", 0)

	ctx, cancel := context.WithCancel(context.Background())
	go agent.Run(ctx, "go", "")

	// Wait for both agents to be running.
	deadline := time.After(5 * time.Second)
	for {
		tracker.mu.Lock()
		r := tracker.running
		tracker.mu.Unlock()
		if r >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for 2 concurrent agents")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Cancel context — both agents should stop.
	cancel()

	// Should eventually get EventDone.
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev := <-agent.Events():
			if ev.Type == EventDone {
				return
			}
		case <-timer.C:
			t.Fatal("timeout waiting for EventDone after cancellation")
		}
	}
}

func TestRunParallelAgentEventIDs(t *testing.T) {
	// Verify that events from parallel agent calls carry the correct tool IDs.
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "a1", Name: "agent", Input: json.RawMessage(`{"task":"t1"}`)},
					{Type: "tool_use", ID: "a2", Name: "agent", Input: json.RawMessage(`{"task":"t2"}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "Done", tokensIn: 200, tokensOut: 30},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	agentTool := &testTool{name: "agent", result: "ok"}
	agent := NewAgent(client, []Tool{agentTool}, nil, "", "test-model", 0)

	go agent.Run(context.Background(), "go", "")
	events := drainEvents(t, agent.Events(), 5*time.Second)

	// Collect distinct tool IDs from EventToolCallStart for agent calls.
	toolIDs := make(map[string]bool)
	for _, ev := range events {
		if ev.Type == EventToolCallStart && ev.ToolName == "agent" {
			toolIDs[ev.ToolID] = true
		}
	}
	if len(toolIDs) != 2 {
		t.Errorf("distinct agent tool IDs = %d, want 2", len(toolIDs))
	}
	if !toolIDs["a1"] || !toolIDs["a2"] {
		t.Errorf("expected tool IDs a1 and a2, got %v", toolIDs)
	}
}

// --- Phase 5: Token budget awareness ---

func TestSystemPromptWithStatsNoStats(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(client, nil, nil, "base prompt", "test-model", 0)

	got := agent.systemPromptWithStats()
	if got != "base prompt" {
		t.Errorf("with no stats, systemPromptWithStats should return base prompt, got: %q", got)
	}
}

func TestSystemPromptWithStatsIncludesStats(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(client, nil, nil, "base prompt", "test-model", 0)
	agent.sessionInputTokens = 10000
	agent.sessionOutputTokens = 2000
	agent.sessionAgentCalls = 3

	got := agent.systemPromptWithStats()
	if !strings.Contains(got, "base prompt") {
		t.Error("augmented prompt should still contain base prompt")
	}
	if !strings.Contains(got, "12000 tokens used") {
		t.Errorf("prompt should contain total tokens (10000+2000=12000), got: %q", got)
	}
	if !strings.Contains(got, "3 agent calls") {
		t.Errorf("prompt should contain agent call count, got: %q", got)
	}
}

func TestSessionStatsAccumulateFromEmitUsage(t *testing.T) {
	store := newMockStorage()
	prov := &mockProvider{model: "test-model"}
	client := langdag.NewWithDeps(store, prov)

	_ = store.CreateNode(context.Background(), &types.Node{
		ID:              "node-1",
		NodeType:        types.NodeTypeAssistant,
		Model:           "test-model",
		TokensIn:        5000,
		TokensOut:       200,
		TokensCacheRead: 1000,
	})
	_ = store.CreateNode(context.Background(), &types.Node{
		ID:              "node-2",
		NodeType:        types.NodeTypeAssistant,
		Model:           "test-model",
		TokensIn:        3000,
		TokensOut:       150,
		TokensCacheRead: 500,
	})

	agent := NewAgent(client, nil, nil, "", "test-model", 0)
	agent.emitUsage(context.Background(), "node-1")
	// Drain the event.
	<-agent.Events()
	agent.emitUsage(context.Background(), "node-2")
	<-agent.Events()

	// Input: (5000+1000) + (3000+500) = 9500
	if agent.sessionInputTokens != 9500 {
		t.Errorf("sessionInputTokens = %d, want 9500", agent.sessionInputTokens)
	}
	// Output: 200 + 150 = 350
	if agent.sessionOutputTokens != 350 {
		t.Errorf("sessionOutputTokens = %d, want 350", agent.sessionOutputTokens)
	}
}

func TestSessionAgentCallsTracked(t *testing.T) {
	// LLM returns 2 agent calls, then finishes.
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "a1", Name: "agent", Input: json.RawMessage(`{"task":"t1"}`)},
					{Type: "tool_use", ID: "a2", Name: "agent", Input: json.RawMessage(`{"task":"t2"}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "Done", tokensIn: 200, tokensOut: 30},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	agentTool := &testTool{name: "agent", result: "ok"}
	agent := NewAgent(client, []Tool{agentTool}, nil, "", "test-model", 0)

	go agent.Run(context.Background(), "go", "")
	drainEvents(t, agent.Events(), 5*time.Second)

	if agent.sessionAgentCalls != 2 {
		t.Errorf("sessionAgentCalls = %d, want 2", agent.sessionAgentCalls)
	}
}

func TestBuildPromptOptsIncludesModel(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(client, nil, nil, "prompt", "test-model", 0)

	opts := agent.buildPromptOpts()
	// Should have at least 4 options: system prompt, max tokens, tools, model.
	if len(opts) != 4 {
		t.Errorf("buildPromptOpts returned %d options, want 4", len(opts))
	}
}

func TestBuildPromptOptsNoModel(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(client, nil, nil, "prompt", "", 0)

	opts := agent.buildPromptOpts()
	// No model → only 3 options: system prompt, max tokens, tools.
	if len(opts) != 3 {
		t.Errorf("buildPromptOpts returned %d options, want 3", len(opts))
	}
}

// --- Phase: Fix agent silent stops ---

// panicTool is a tool that panics during Execute to test panic recovery.
type panicTool struct {
	name string
	msg  string
}

func (t *panicTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{Name: t.name, Description: "panicking tool"}
}

func (t *panicTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	panic(t.msg)
}

func (t *panicTool) RequiresApproval(_ json.RawMessage) bool { return false }

func TestEmitNonBlockingWhenChannelFull(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(client, nil, nil, "", "", 0)

	// Fill the event channel to capacity.
	for i := 0; i < cap(agent.events); i++ {
		agent.events <- AgentEvent{Type: EventTextDelta, Text: "fill"}
	}

	// emit() should not block — the event should be dropped.
	done := make(chan struct{})
	go func() {
		agent.emit(AgentEvent{Type: EventTextDelta, Text: "overflow"})
		close(done)
	}()

	select {
	case <-done:
		// Success — emit returned without blocking.
	case <-time.After(time.Second):
		t.Fatal("emit() blocked on full channel — should be non-blocking")
	}
}

func TestRunPanicRecovery(t *testing.T) {
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "call_1", Name: "panic_tool", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	pt := &panicTool{name: "panic_tool", msg: "intentional test panic"}
	agent := NewAgent(client, []Tool{pt}, nil, "", "test-model", 0)

	go agent.Run(context.Background(), "trigger panic", "")
	events := drainEvents(t, agent.Events(), 5*time.Second)

	var hasPanicError, hasDone bool
	for _, ev := range events {
		if ev.Type == EventError && ev.Error != nil && strings.Contains(ev.Error.Error(), "intentional test panic") {
			hasPanicError = true
		}
		if ev.Type == EventDone {
			hasDone = true
		}
	}
	if !hasPanicError {
		t.Error("expected EventError containing panic information")
	}
	if !hasDone {
		t.Error("expected EventDone after panic recovery")
	}
}

func TestSubAgentManyEventsNoDeadlock(t *testing.T) {
	client := newTestClient("sub-agent output with events")
	tmpDir := t.TempDir()

	// Deliberately small parent buffer — forward() must not block.
	parentEvents := make(chan AgentEvent, 1)
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 1, 0, tmpDir, "", "", nil)
	tool.parentEvents = parentEvents

	done := make(chan struct{})
	var result string
	var execErr error
	go func() {
		defer close(done)
		result, execErr = tool.Execute(context.Background(), json.RawMessage(`{"task":"generate events"}`))
	}()

	// Do NOT drain parent events — test that forward() drops rather than blocks.
	select {
	case <-done:
		if execErr != nil {
			t.Fatalf("Execute error: %v", execErr)
		}
		if result == "" {
			t.Error("expected non-empty result")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("sub-agent deadlocked: forward() is blocking on full parent channel")
	}
}

// --- Phase 6b: End-to-end exploration test ---

func TestExplorationFlowOutlineThenReadThenAgent(t *testing.T) {
	// Simulates: LLM calls outline → reads file → spawns sub-agent → synthesizes.
	// Uses the same scripted provider pattern as TestRunToolCallDispatch but with
	// multiple sequential tool rounds followed by a parallel agent call.
	//
	// The flow: outline (1 tool call) → read_file (1 tool call) → agent (parallel) → final text.
	// Each tool response triggers one LLM re-call, so we need 4 scripted responses total.
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			// Response 0: initial prompt → outline tool call
			{
				text: "Let me outline the file first.",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "c1", Name: "outline", Input: json.RawMessage(`{"file_path":"main.go"}`)},
				},
				tokensIn: 200, tokensOut: 50,
			},
			// Response 1: after outline result → read_file tool call
			{
				text: "Now I'll read the implementation.",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "c2", Name: "read_file", Input: json.RawMessage(`{"file_path":"main.go"}`)},
				},
				tokensIn: 300, tokensOut: 50,
			},
			// Response 2: after read result → agent tool call
			{
				text: "I'll delegate deep analysis.",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "c3", Name: "agent", Input: json.RawMessage(`{"task":"analyze error handling"}`)},
				},
				tokensIn: 400, tokensOut: 50,
			},
			// Response 3: after agent result → final synthesis
			{
				text: "The code uses structured error handling with log.Printf and os.Exit.",
				tokensIn: 500, tokensOut: 100,
			},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	outlineTool := &testTool{name: "outline", result: "1: func main()\n5: func handleError(err error)"}
	readTool := &testTool{name: "read_file", result: "func handleError(err error) {\n\tos.Exit(1)\n}"}
	agentTool := &testTool{name: "agent", result: "[agent_id: abc] [turns: 3/15] [summary: model]\n\n- Uses os.Exit"}

	agent := NewAgent(client, []Tool{outlineTool, readTool, agentTool}, nil, "", "test-model", 0)

	go agent.Run(context.Background(), "understand error handling", "")
	events := drainEvents(t, agent.Events(), 10*time.Second)

	// Collect tool call sequence and errors.
	var toolOrder []string
	var finalText string
	var errors []string
	for _, ev := range events {
		switch ev.Type {
		case EventToolCallStart:
			toolOrder = append(toolOrder, ev.ToolName)
		case EventTextDelta:
			finalText += ev.Text
		case EventError:
			if ev.Error != nil {
				errors = append(errors, ev.Error.Error())
			}
		}
	}

	// Log any errors for debugging.
	for _, e := range errors {
		t.Logf("EventError: %s", e)
	}

	// The flow should produce exactly 3 tool calls in sequence.
	if len(toolOrder) < 3 {
		t.Fatalf("expected 3 tool calls, got %d: %v (errors: %v)", len(toolOrder), toolOrder, errors)
	}
	if toolOrder[0] != "outline" {
		t.Errorf("first tool = %q, want outline", toolOrder[0])
	}
	if toolOrder[1] != "read_file" {
		t.Errorf("second tool = %q, want read_file", toolOrder[1])
	}
	if toolOrder[2] != "agent" {
		t.Errorf("third tool = %q, want agent", toolOrder[2])
	}

	// Agent should synthesize a final response.
	if !strings.Contains(finalText, "structured error handling") {
		t.Errorf("final text = %q, want to contain 'structured error handling'", finalText)
	}

	// The agent tool result should carry structured metadata.
	var agentResult string
	for _, ev := range events {
		if ev.Type == EventToolResult && ev.ToolName == "agent" {
			agentResult = ev.ToolResult
		}
	}
	if !strings.Contains(agentResult, "[summary: model]") {
		t.Errorf("agent result = %q, want to contain [summary: model]", agentResult)
	}
}

func TestExplorationFlowParallelSubAgents(t *testing.T) {
	// Simulates: LLM spawns 2 sub-agents in parallel for investigation, then synthesizes.
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "Spawning parallel investigations.",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "a1", Name: "agent", Input: json.RawMessage(`{"task":"analyze module A"}`)},
					{Type: "tool_use", ID: "a2", Name: "agent", Input: json.RawMessage(`{"task":"analyze module B"}`)},
				},
				tokensIn: 200, tokensOut: 50,
			},
			{
				text: "Module A handles auth, Module B handles data processing.",
				tokensIn: 400, tokensOut: 100,
			},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	gate := make(chan struct{})
	tracker := &parallelTracker{name: "agent", result: "[agent_id: x] [turns: 2/15] [summary: model]\n\n- module analyzed", gate: gate}
	agent := NewAgent(client, []Tool{tracker}, nil, "", "test-model", 0)

	go agent.Run(context.Background(), "explore the codebase architecture", "")

	// Wait for both agents to be running concurrently.
	deadline := time.After(5 * time.Second)
	for {
		tracker.mu.Lock()
		r := tracker.running
		tracker.mu.Unlock()
		if r >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for 2 concurrent sub-agents")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	close(gate) // release both
	events := drainEvents(t, agent.Events(), 5*time.Second)

	// Verify both agent results were collected.
	var agentResults int
	for _, ev := range events {
		if ev.Type == EventToolResult && ev.ToolName == "agent" {
			agentResults++
		}
	}
	if agentResults != 2 {
		t.Errorf("expected 2 agent results, got %d", agentResults)
	}

	// Peak concurrency should be 2.
	tracker.mu.Lock()
	mc := tracker.maxConc
	tracker.mu.Unlock()
	if mc < 2 {
		t.Errorf("max concurrency = %d, want 2 (parallel sub-agents)", mc)
	}
}

// --- Phase 6c: Resilience tests ---

// failThenSucceedProvider returns errors on specified call indices, then succeeds.
type failThenSucceedProvider struct {
	mu          sync.Mutex
	callIdx     int
	failOnCalls map[int]error          // call index → error to return
	responses   []scriptedResponse     // responses for successful calls
	successIdx  int                    // tracks which response to use next
	model       string
}

func (p *failThenSucceedProvider) Complete(_ context.Context, _ *types.CompletionRequest) (*types.CompletionResponse, error) {
	p.mu.Lock()
	idx := p.callIdx
	p.callIdx++
	if err, fail := p.failOnCalls[idx]; fail {
		p.mu.Unlock()
		return nil, err
	}
	si := p.successIdx
	p.successIdx++
	p.mu.Unlock()

	r := scriptedResponse{text: "ok", tokensIn: 100, tokensOut: 50}
	if si < len(p.responses) {
		r = p.responses[si]
	}
	content := []types.ContentBlock{{Type: "text", Text: r.text}}
	content = append(content, r.toolCalls...)
	return &types.CompletionResponse{
		ID: fmt.Sprintf("resp-%d", si), Model: p.model,
		Content: content, StopReason: "end_turn",
		Usage: types.Usage{InputTokens: r.tokensIn, OutputTokens: r.tokensOut},
	}, nil
}

func (p *failThenSucceedProvider) Stream(_ context.Context, req *types.CompletionRequest) (<-chan types.StreamEvent, error) {
	p.mu.Lock()
	idx := p.callIdx
	p.callIdx++
	if err, fail := p.failOnCalls[idx]; fail {
		p.mu.Unlock()
		return nil, err
	}
	si := p.successIdx
	p.successIdx++
	p.mu.Unlock()

	r := scriptedResponse{text: "ok", tokensIn: 100, tokensOut: 50}
	if si < len(p.responses) {
		r = p.responses[si]
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
				ID: fmt.Sprintf("resp-%d", si), Model: req.Model,
				Content: content, StopReason: stopReason,
				Usage: types.Usage{InputTokens: r.tokensIn, OutputTokens: r.tokensOut},
			},
		}
	}()
	return ch, nil
}

func (p *failThenSucceedProvider) Name() string             { return "mock" }
func (p *failThenSucceedProvider) Models() []types.ModelInfo { return nil }

func TestResilienceRetrySucceedsAfterTransientError(t *testing.T) {
	// Initial LLM call fails with a retryable error (429), retry succeeds.
	prov := &failThenSucceedProvider{
		model: "test-model",
		failOnCalls: map[int]error{
			0: fmt.Errorf("HTTP 429: rate limited"),
		},
		responses: []scriptedResponse{
			{text: "Hello after retry!", tokensIn: 100, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	agent := NewAgent(client, nil, nil, "", "test-model", 0)

	go agent.Run(context.Background(), "hello", "")
	events := drainEvents(t, agent.Events(), 10*time.Second)

	// Should have a retry event.
	var hasRetry, hasDone bool
	var finalText string
	for _, ev := range events {
		switch ev.Type {
		case EventRetry:
			hasRetry = true
			if ev.Attempt != 1 {
				t.Errorf("retry attempt = %d, want 1", ev.Attempt)
			}
		case EventTextDelta:
			finalText += ev.Text
		case EventDone:
			hasDone = true
		}
	}
	if !hasRetry {
		t.Error("expected EventRetry for transient 429 error")
	}
	if !hasDone {
		t.Error("expected EventDone — conversation should complete after retry")
	}
	if !strings.Contains(finalText, "Hello after retry") {
		t.Errorf("final text = %q, want text from successful retry", finalText)
	}
}

func TestResiliencePermanentErrorStopsImmediately(t *testing.T) {
	// LLM call fails with a non-retryable error (401) — should not retry.
	prov := &failThenSucceedProvider{
		model: "test-model",
		failOnCalls: map[int]error{
			0: fmt.Errorf("HTTP 401: unauthorized"),
		},
		responses: []scriptedResponse{
			{text: "should not reach this", tokensIn: 100, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	agent := NewAgent(client, nil, nil, "", "test-model", 0)

	go agent.Run(context.Background(), "hello", "")
	events := drainEvents(t, agent.Events(), 5*time.Second)

	var hasRetry, hasError bool
	for _, ev := range events {
		if ev.Type == EventRetry {
			hasRetry = true
		}
		if ev.Type == EventError && ev.Error != nil && strings.Contains(ev.Error.Error(), "401") {
			hasError = true
		}
	}
	if hasRetry {
		t.Error("should not retry on 401 — it's a permanent error")
	}
	if !hasError {
		t.Error("expected EventError with 401 details")
	}
}

func TestResilienceRetryDuringToolLoop(t *testing.T) {
	// First LLM call succeeds with a tool call. After tool execution, the
	// follow-up LLM call fails once (retryable) then succeeds.
	prov := &failThenSucceedProvider{
		model: "test-model",
		failOnCalls: map[int]error{
			1: fmt.Errorf("HTTP 502: bad gateway"), // second provider call (tool result)
		},
		responses: []scriptedResponse{
			// Response 0: initial prompt → tool call
			{
				text: "Running tool.",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "c1", Name: "bash", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			// Response 1: after retry succeeds → final text
			{text: "Tool completed successfully.", tokensIn: 200, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	bashTool := &testTool{name: "bash", result: "ok"}
	agent := NewAgent(client, []Tool{bashTool}, nil, "", "test-model", 0)

	go agent.Run(context.Background(), "run tool", "")
	events := drainEvents(t, agent.Events(), 10*time.Second)

	var hasRetry, hasToolStart bool
	var finalText string
	for _, ev := range events {
		switch ev.Type {
		case EventRetry:
			hasRetry = true
		case EventToolCallStart:
			hasToolStart = true
		case EventTextDelta:
			finalText += ev.Text
		}
	}
	if !hasToolStart {
		t.Error("expected tool call before retry")
	}
	if !hasRetry {
		t.Error("expected retry on 502 during tool loop")
	}
	if !strings.Contains(finalText, "Tool completed successfully") {
		t.Errorf("final text = %q, want success message after retry", finalText)
	}
}

func TestResilienceSubAgentFailureReportsErrors(t *testing.T) {
	// Sub-agent encounters an error. Main agent should get structured error info.
	client := newTestClient("") // empty output triggers no-output path
	tmpDir := t.TempDir()
	tool := NewSubAgentTool(client, nil, nil, "test-model", "", 10, 3, 0, tmpDir, "", "alpine:latest", nil)

	// Use buildResult directly with errors to verify the error reporting path.
	result := tool.buildResult(context.Background(), "err-agent", nil,
		[]string{"during tool \"bash\" (turn 3): HTTP 500 internal server error"},
		500, 100, 3)

	if !strings.Contains(result, "[errors:") {
		t.Errorf("result should contain [errors:], got: %q", result)
	}
	if !strings.Contains(result, "HTTP 500") {
		t.Errorf("result should include the specific error, got: %q", result)
	}
	if !strings.Contains(result, "Sub-agent encountered errors") {
		t.Errorf("no-output + errors should use error body, got: %q", result)
	}
	if !strings.Contains(result, "[turns: 3/10]") {
		t.Errorf("result should include turn count, got: %q", result)
	}
}

func TestResilienceNoDeadlockOnMultipleFailures(t *testing.T) {
	// Agent calls 2 tools in parallel; both fail. Agent should still complete
	// without deadlock and emit EventDone.
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "a1", Name: "agent", Input: json.RawMessage(`{"task":"t1"}`)},
					{Type: "tool_use", ID: "a2", Name: "agent", Input: json.RawMessage(`{"task":"t2"}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "Both failed, adapting.", tokensIn: 200, tokensOut: 30},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	// Tool that always returns an error.
	failingTool := &testTool{name: "agent", err: fmt.Errorf("sub-agent crashed")}
	agent := NewAgent(client, []Tool{failingTool}, nil, "", "test-model", 0)

	go agent.Run(context.Background(), "go", "")

	// Must complete within timeout — no deadlock.
	events := drainEvents(t, agent.Events(), 5*time.Second)

	var hasDone bool
	var errorResults int
	for _, ev := range events {
		if ev.Type == EventDone {
			hasDone = true
		}
		if ev.Type == EventToolResult && ev.IsError {
			errorResults++
		}
	}
	if !hasDone {
		t.Error("agent should complete without deadlock after tool failures")
	}
	if errorResults != 2 {
		t.Errorf("expected 2 error tool results, got %d", errorResults)
	}
}
