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
