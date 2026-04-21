package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

func (t *testTool) HostTool() bool { return false }

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
func (t *blockingTool) HostTool() bool                        { return false }

// --- Task 1a: NewAgent with option funcs ---

func TestNewAgentDefaults(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "system prompt", Model: "test-model", ContextWindow: 100000})

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
	if agent.doneCh == nil {
		t.Error("doneCh should not be nil")
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
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "", ContextWindow: 0}, WithContextWindow(200000))

	if agent.contextWindow != 200000 {
		t.Errorf("contextWindow = %d, want 200000", agent.contextWindow)
	}
}

func TestNewAgentWithExplorationModel(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "", ContextWindow: 0}, WithExplorationModel("cheap-model"))

	if agent.explorationModel != "cheap-model" {
		t.Errorf("explorationModel = %q, want %q", agent.explorationModel, "cheap-model")
	}
}

func TestNewAgentWithMaxToolIterations(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "", ContextWindow: 0}, WithMaxToolIterations(50))

	if agent.maxToolIterations != 50 {
		t.Errorf("maxToolIterations = %d, want 50", agent.maxToolIterations)
	}
}

func TestNewAgentMultipleOptions(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "model-x", ContextWindow: 0}, WithContextWindow(150000), WithExplorationModel("summary-model"), WithMaxToolIterations(10))

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

	agent := NewAgent(NewAgentOptions{Client: client, Tools: tools, ServerTools: nil, SystemPrompt: "", Model: "", ContextWindow: 0})

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

	agent := NewAgent(NewAgentOptions{Client: client, Tools: tools, ServerTools: serverTools, SystemPrompt: "", Model: "", ContextWindow: 0})

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
			client, err := newLangdagClientForProvider(newLangdagClientForProviderOptions{cfg: tt.cfg, provider: tt.provider})
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
	_, err := newLangdagClientForProvider(newLangdagClientForProviderOptions{cfg: Config{}, provider: "unsupported-provider"})
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
	stopReason string // override stop reason (default: "end_turn" or "tool_use" if toolCalls present)
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
	stopReason := r.stopReason
	if stopReason == "" {
		stopReason = "end_turn"
		if len(r.toolCalls) > 0 {
			stopReason = "tool_use"
		}
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
		stopReason := r.stopReason
		if stopReason == "" {
			stopReason = "end_turn"
			if len(r.toolCalls) > 0 {
				stopReason = "tool_use"
			}
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
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "hi"})
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
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{bashTool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "list files"})
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
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "do something"})
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
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{bashTool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "delete everything"})

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
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{bashTool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "do it"})

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
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{blockingTool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "run"})

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
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{bashTool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0}, WithMaxToolIterations(3))

	go agent.Run(context.Background(), RunOptions{UserMessage: "loop"})
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
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{bt}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	// Start first run — it will block in the tool execution.
	go agent.Run(context.Background(), RunOptions{UserMessage: "first"})

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
	go agent.Run(context.Background(), RunOptions{UserMessage: "second"})

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
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

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

	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})
	inputTokens := agent.emitUsage(context.Background(), emitUsageOptions{nodeID: "node-1", stopReason: "end_turn"})

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
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	inputTokens := agent.emitUsage(context.Background(), emitUsageOptions{nodeID: "", stopReason: ""})
	if inputTokens != 0 {
		t.Errorf("inputTokens = %d, want 0 for empty nodeID", inputTokens)
	}
}

func TestEmitUsageMissingNode(t *testing.T) {
	store := newMockStorage()
	prov := &mockProvider{model: "test-model"}
	client := langdag.NewWithDeps(store, prov)
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	// Node doesn't exist in storage.
	inputTokens := agent.emitUsage(context.Background(), emitUsageOptions{nodeID: "nonexistent", stopReason: ""})
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
func (t *parallelTracker) HostTool() bool                        { return false }

func TestRunParallelAgentCalls(t *testing.T) {
	// LLM returns 3 agent tool calls in one response.
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "Spawning agents",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "a1", Name: "agent", Input: json.RawMessage(`{"task":"t1","mode":"explore"}`)},
					{Type: "tool_use", ID: "a2", Name: "agent", Input: json.RawMessage(`{"task":"t2","mode":"explore"}`)},
					{Type: "tool_use", ID: "a3", Name: "agent", Input: json.RawMessage(`{"task":"t3","mode":"explore"}`)},
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
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{tracker}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "spawn agents"})

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
					{Type: "tool_use", ID: "agent_1", Name: "agent", Input: json.RawMessage(`{"task":"t1","mode":"explore"}`)},
					{Type: "tool_use", ID: "agent_2", Name: "agent", Input: json.RawMessage(`{"task":"t2","mode":"explore"}`)},
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
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{bashTool, agentTool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "do things"})
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
					{Type: "tool_use", ID: "a1", Name: "agent", Input: json.RawMessage(`{"task":"t1","mode":"explore"}`)},
					{Type: "tool_use", ID: "a2", Name: "agent", Input: json.RawMessage(`{"task":"t2","mode":"explore"}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	gate := make(chan struct{}) // never closed — agents block until cancelled
	tracker := &parallelTracker{name: "agent", result: "ok", gate: gate}
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{tracker}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	ctx, cancel := context.WithCancel(context.Background())
	go agent.Run(ctx, RunOptions{UserMessage: "go"})

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
					{Type: "tool_use", ID: "a1", Name: "agent", Input: json.RawMessage(`{"task":"t1","mode":"explore"}`)},
					{Type: "tool_use", ID: "a2", Name: "agent", Input: json.RawMessage(`{"task":"t2","mode":"explore"}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "Done", tokensIn: 200, tokensOut: 30},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	agentTool := &testTool{name: "agent", result: "ok"}
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{agentTool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "go"})
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

func TestSystemPromptIsStatic(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0})
	agent.sessionInputTokens = 10000
	agent.sessionOutputTokens = 2000
	agent.sessionAgentCalls = 3

	// The system prompt should always be the static base prompt —
	// dynamic stats are in budgetReminderBlock().
	if agent.systemPrompt != "base prompt" {
		t.Errorf("systemPrompt should be static base prompt, got: %q", agent.systemPrompt)
	}
}

func TestBudgetReminderBlockIncludesStats(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0})
	agent.sessionInputTokens = 10000
	agent.sessionOutputTokens = 2000
	agent.sessionAgentCalls = 3

	block := agent.budgetReminderBlock()
	got := block.Text
	if !strings.Contains(got, "12000 tokens used") {
		t.Errorf("reminder should contain total tokens (10000+2000=12000), got: %q", got)
	}
	if !strings.Contains(got, "3 agent calls") {
		t.Errorf("reminder should contain agent call count, got: %q", got)
	}
	if !strings.Contains(got, "<system-reminder>") {
		t.Errorf("reminder should be wrapped in <system-reminder> tags, got: %q", got)
	}
}

func TestBudgetReminderBlockEmptyWhenNoStats(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0})

	block := agent.budgetReminderBlock()
	if block.Text != "" {
		t.Errorf("with no stats, budgetReminderBlock should return empty block, got: %q", block.Text)
	}
}

func TestBudgetReminderIterationWarningLowThreshold(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0}, WithMaxToolIterations(20))
	// Simulate being at iteration 16 of 20 (20% remaining < 25% low threshold).
	agent.currentIteration = 16

	got := agent.budgetReminderBlock().Text
	if !strings.Contains(got, "4 tool iterations remaining out of 20") {
		t.Errorf("expected low-threshold iteration warning, got: %q", got)
	}
	if !strings.Contains(got, "⚠️") {
		t.Errorf("expected warning emoji for low threshold, got: %q", got)
	}
}

func TestBudgetReminderIterationWarningMidThreshold(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0}, WithMaxToolIterations(20))
	// Simulate being at iteration 12 of 20 (40% remaining < 50% mid threshold).
	agent.currentIteration = 12

	got := agent.budgetReminderBlock().Text
	if !strings.Contains(got, "past halfway") {
		t.Errorf("expected mid-threshold 'past halfway' warning, got: %q", got)
	}
	if !strings.Contains(got, "8/20 remaining") {
		t.Errorf("expected remaining count in mid warning, got: %q", got)
	}
}

func TestBudgetReminderNoIterationWarningAboveThreshold(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0}, WithMaxToolIterations(20))
	// Simulate being at iteration 5 of 20 (75% remaining > 50% mid threshold).
	agent.currentIteration = 5

	got := agent.budgetReminderBlock().Text
	if strings.Contains(got, "tool iterations remaining") || strings.Contains(got, "past halfway") {
		t.Errorf("should not show iteration warning when above threshold, got: %q", got)
	}
}

func TestSubAgentReceivesMaxToolIterations(t *testing.T) {
	client := newTestClient("ok")
	sat := NewSubAgentTool(SubAgentConfig{Client: client, MainModel: "main-model", ExplorationModel: "explore-model", ExploreMaxTurns: 15, GeneralMaxTurns: 15, MaxDepth: 1, WorkDir: "/tmp"})

	// Per-mode max turns should be set to 15.
	if sat.exploreMaxTurns != 15 {
		t.Errorf("exploreMaxTurns = %d, want 15", sat.exploreMaxTurns)
	}
	if sat.generalMaxTurns != 15 {
		t.Errorf("generalMaxTurns = %d, want 15", sat.generalMaxTurns)
	}
	// When creating agents, WithMaxToolIterations(maxTurns + buffer) is passed.
	// Verify by creating an agent with the same pattern used in subagent.go.
	agentOpts := []AgentOption{
		WithMaxToolIterations(sat.generalMaxTurns + subAgentIterationBuffer),
	}
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "", ContextWindow: 0}, agentOpts...)
	want := 15 + subAgentIterationBuffer
	if agent.maxToolIterations != want {
		t.Errorf("sub-agent maxToolIterations = %d, want %d", agent.maxToolIterations, want)
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

	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})
	agent.emitUsage(context.Background(), emitUsageOptions{nodeID: "node-1", stopReason: "tool_use"})
	// Drain the event.
	<-agent.Events()
	agent.emitUsage(context.Background(), emitUsageOptions{nodeID: "node-2", stopReason: "end_turn"})
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
					{Type: "tool_use", ID: "a1", Name: "agent", Input: json.RawMessage(`{"task":"t1","mode":"explore"}`)},
					{Type: "tool_use", ID: "a2", Name: "agent", Input: json.RawMessage(`{"task":"t2","mode":"explore"}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "Done", tokensIn: 200, tokensOut: 30},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	agentTool := &testTool{name: "agent", result: "ok"}
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{agentTool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "go"})
	drainEvents(t, agent.Events(), 5*time.Second)

	if agent.sessionAgentCalls != 2 {
		t.Errorf("sessionAgentCalls = %d, want 2", agent.sessionAgentCalls)
	}
}

func TestBuildPromptOptsIncludesModel(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "prompt", Model: "test-model", ContextWindow: 0})

	opts := agent.buildPromptOpts()
	// Should have 5 options: system prompt, max tokens, max output group tokens, tools, model.
	if len(opts) != 5 {
		t.Errorf("buildPromptOpts returned %d options, want 5", len(opts))
	}
}

func TestBuildPromptOptsNoModel(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "prompt", Model: "", ContextWindow: 0})

	opts := agent.buildPromptOpts()
	// No model → only 4 options: system prompt, max tokens, max output group tokens, tools.
	if len(opts) != 4 {
		t.Errorf("buildPromptOpts returned %d options, want 4", len(opts))
	}
}

func TestBuildPromptOptsWithThinking(t *testing.T) {
	client := newTestClient("ok")
	thinkTrue := true
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "prompt", Model: "test-model", ContextWindow: 0}, WithThinking(&thinkTrue))

	opts := agent.buildPromptOpts()
	// Should have 6 options: system prompt, max tokens, max output group tokens, tools, model, think.
	if len(opts) != 6 {
		t.Errorf("buildPromptOpts returned %d options, want 6", len(opts))
	}
}

func TestBuildPromptOptsThinkingNil(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "prompt", Model: "test-model", ContextWindow: 0})

	opts := agent.buildPromptOpts()
	// No thinking → 5 options: system prompt, max tokens, max output group tokens, tools, model.
	if len(opts) != 5 {
		t.Errorf("buildPromptOpts returned %d options, want 5", len(opts))
	}
}

func TestBuildPromptOptsMaxTokensDefault(t *testing.T) {
	prov := &mockProvider{responses: []string{"ok"}, model: "test-model"}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "prompt", Model: "test-model", ContextWindow: 0})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go agent.Run(ctx, RunOptions{UserMessage: "hello"})
	// Drain events until done.
	for ev := range agent.Events() {
		if ev.Type == EventDone {
			break
		}
	}

	prov.mu.Lock()
	req := prov.lastRequest
	prov.mu.Unlock()

	if req == nil {
		t.Fatal("expected provider to receive a request")
	}
	if req.MaxTokens != defaultMaxOutputTokens {
		t.Errorf("MaxTokens = %d, want %d", req.MaxTokens, defaultMaxOutputTokens)
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
func (t *panicTool) HostTool() bool                        { return false }

func TestEmitNonBlockingWhenChannelFull(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "", ContextWindow: 0})

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
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{pt}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "trigger panic"})
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

	// Small parent buffer — display events (forward) drop, critical events
	// (forwardBlocking) block with a 5s timeout. Drain in a background
	// goroutine so critical sends succeed promptly.
	parentEvents := make(chan AgentEvent, 1)
	tool := NewSubAgentTool(SubAgentConfig{Client: client, MainModel: "test-model", ExploreMaxTurns: 10, GeneralMaxTurns: 10, MaxDepth: 1, WorkDir: tmpDir})
	tool.parentEvents = parentEvents

	// Drain parent events slowly to test that non-critical forward() drops
	// without deadlocking, while critical forwardBlocking() eventually succeeds.
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for range parentEvents {
		}
	}()

	done := make(chan struct{})
	var result string
	var execErr error
	go func() {
		defer close(done)
		result, execErr = tool.Execute(context.Background(), json.RawMessage(`{"task":"generate events","mode":"explore"}`))
	}()

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
	close(parentEvents)
	<-drainDone
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
					{Type: "tool_use", ID: "c3", Name: "agent", Input: json.RawMessage(`{"task":"analyze error handling","mode":"explore"}`)},
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
	agentTool := &testTool{name: "agent", result: "[agent:abc turns:3/15 summary:model]\n\n- Uses os.Exit"}

	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{outlineTool, readTool, agentTool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "understand error handling"})
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
	if !strings.Contains(agentResult, "summary:model") {
		t.Errorf("agent result = %q, want to contain summary:model", agentResult)
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
					{Type: "tool_use", ID: "a1", Name: "agent", Input: json.RawMessage(`{"task":"analyze module A","mode":"explore"}`)},
					{Type: "tool_use", ID: "a2", Name: "agent", Input: json.RawMessage(`{"task":"analyze module B","mode":"explore"}`)},
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
	tracker := &parallelTracker{name: "agent", result: "[agent:x turns:2/15 summary:model]\n\n- module analyzed", gate: gate}
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{tracker}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "explore the codebase architecture"})

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
	retryProv := langdag.WithRetry(prov, langdag.RetryConfig{
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
		MaxDelay:   10 * time.Millisecond,
	})
	client := langdag.NewWithDeps(store, retryProv)
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "hello"})
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
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "hello"})
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
	retryProv := langdag.WithRetry(prov, langdag.RetryConfig{
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
		MaxDelay:   10 * time.Millisecond,
	})
	client := langdag.NewWithDeps(store, retryProv)

	bashTool := &testTool{name: "bash", result: "ok"}
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{bashTool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "run tool"})
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
	tool := NewSubAgentTool(SubAgentConfig{Client: client, MainModel: "test-model", ExploreMaxTurns: 10, GeneralMaxTurns: 10, MaxDepth: 3, WorkDir: tmpDir, ContainerImage: "alpine:latest"})

	// Use buildResult directly with errors to verify the error reporting path.
	result := tool.buildResult(context.Background(), "err-agent", nil,
		[]string{"during tool \"bash\" (turn 3): HTTP 500 internal server error"},
		3, 10, false)

	if !strings.Contains(result, "[errors:") {
		t.Errorf("result should contain [errors:], got: %q", result)
	}
	if !strings.Contains(result, "HTTP 500") {
		t.Errorf("result should include the specific error, got: %q", result)
	}
	if !strings.Contains(result, "Sub-agent encountered errors") {
		t.Errorf("no-output + errors should use error body, got: %q", result)
	}
	if !strings.Contains(result, "turns:3/10") {
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
					{Type: "tool_use", ID: "a1", Name: "agent", Input: json.RawMessage(`{"task":"t1","mode":"explore"}`)},
					{Type: "tool_use", ID: "a2", Name: "agent", Input: json.RawMessage(`{"task":"t2","mode":"explore"}`)},
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
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{failingTool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "go"})

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

// --- Phase 1: Stream timeout tests ---

func TestDrainStreamTimeout(t *testing.T) {
	// A stream that sends a few chunks then stalls should trigger a timeout.
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "", ContextWindow: 0}, WithStreamChunkTimeout(100*time.Millisecond))

	stream := make(chan langdag.StreamChunk, 10)
	// Send a couple of chunks, then stop (no Done, no close).
	stream <- langdag.StreamChunk{Content: "hello "}
	stream <- langdag.StreamChunk{Content: "world"}
	// Don't close and don't send Done — simulates a stall.

	result := &langdag.PromptResult{Stream: stream}
	ctx := context.Background()

	toolCalls, nodeID, _, streamOK := agent.drainStream(ctx, result)
	if streamOK {
		t.Error("drainStream should return streamOK=false on timeout")
	}
	if nodeID != "" {
		t.Errorf("nodeID should be empty, got %q", nodeID)
	}
	if len(toolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(toolCalls))
	}

	// Verify that the timeout error was emitted.
	var foundTimeoutError bool
	for {
		select {
		case ev := <-agent.events:
			if ev.Type == EventError && ev.Error != nil && strings.Contains(ev.Error.Error(), "stream stalled") {
				foundTimeoutError = true
			}
		default:
			goto done
		}
	}
done:
	if !foundTimeoutError {
		t.Error("expected a 'stream stalled' error event")
	}
}

func TestDrainStreamSlowButSteady(t *testing.T) {
	// A stream that sends chunks just within the timeout should complete normally.
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "", ContextWindow: 0}, WithStreamChunkTimeout(200*time.Millisecond))

	stream := make(chan langdag.StreamChunk)
	go func() {
		defer close(stream)
		for i := 0; i < 5; i++ {
			time.Sleep(100 * time.Millisecond) // well within 200ms timeout
			stream <- langdag.StreamChunk{Content: fmt.Sprintf("chunk%d ", i)}
		}
		stream <- langdag.StreamChunk{Done: true, NodeID: "node-123"}
	}()

	result := &langdag.PromptResult{Stream: stream}
	ctx := context.Background()

	_, nodeID, _, streamOK := agent.drainStream(ctx, result)
	if !streamOK {
		t.Error("drainStream should return streamOK=true for slow-but-steady stream")
	}
	if nodeID != "node-123" {
		t.Errorf("nodeID = %q, want %q", nodeID, "node-123")
	}
}

func TestDrainStreamContextCancellation(t *testing.T) {
	// Canceling the context should make drainStream return immediately.
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "", ContextWindow: 0}, WithStreamChunkTimeout(5*time.Second))

	stream := make(chan langdag.StreamChunk) // never sends anything
	result := &langdag.PromptResult{Stream: stream}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, _, _, streamOK := agent.drainStream(ctx, result)
	elapsed := time.Since(start)

	if streamOK {
		t.Error("drainStream should return streamOK=false on context cancellation")
	}
	if elapsed > 2*time.Second {
		t.Errorf("drainStream took %v, should have returned quickly on cancel", elapsed)
	}
}

func TestDrainStreamErrorChunk(t *testing.T) {
	// A stream that sends an error chunk should return streamOK=false.
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "", ContextWindow: 0}, WithStreamChunkTimeout(5*time.Second))

	stream := make(chan langdag.StreamChunk, 10)
	stream <- langdag.StreamChunk{Content: "partial"}
	stream <- langdag.StreamChunk{Error: fmt.Errorf("provider error")}
	close(stream)

	result := &langdag.PromptResult{Stream: stream}
	ctx := context.Background()

	_, _, _, streamOK := agent.drainStream(ctx, result)
	if streamOK {
		t.Error("drainStream should return streamOK=false on error chunk")
	}
}

// --- Phase 2: doneCh tests ---

func TestDoneChClosedOnEventDone(t *testing.T) {
	// Verify that doneCh is closed when EventDone is emitted.
	client := newTestClient("hello")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "hello"})

	// Drain events until done.
	for range agent.Events() {
	}

	// doneCh should be closed.
	select {
	case <-agent.DoneCh():
		// OK — doneCh was closed.
	case <-time.After(2 * time.Second):
		t.Fatal("doneCh was not closed after agent completed")
	}
}

func TestDoneChClosedUnderBackpressure(t *testing.T) {
	// When the events channel is full and EventDone is dropped, doneCh
	// should still be closed so the consumer can detect completion.
	client := newTestClient("hello")
	// Use a tiny event buffer so it fills up quickly.
	agent := &Agent{
		id:                 generateAgentID(),
		client:             client,
		tools:              make(map[string]Tool),
		systemPrompt:       "",
		model:              "",
		streamChunkTimeout: defaultStreamChunkTimeout,
		events:             make(chan AgentEvent, 1), // tiny buffer
		approval:           make(chan ApprovalResponse, 1),
		doneCh:             make(chan struct{}),
	}

	// Fill the events channel so EventDone will be dropped.
	agent.events <- AgentEvent{Type: EventTextDelta, Text: "filler"}

	// Emit EventDone in a goroutine — it now blocks for eventDoneDeliveryTimeout
	// before giving up, but doneCh should still close.
	go agent.emit(AgentEvent{Type: EventDone})

	select {
	case <-agent.DoneCh():
		// OK — doneCh closed despite EventDone being dropped.
	case <-time.After(eventDoneDeliveryTimeout + 2*time.Second):
		t.Fatal("doneCh was not closed when EventDone was dropped due to backpressure")
	}
}

func TestDoneChIdempotent(t *testing.T) {
	// Emitting EventDone twice should not panic (sync.Once protects the close).
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "", ContextWindow: 0})

	agent.emit(AgentEvent{Type: EventDone})
	agent.emit(AgentEvent{Type: EventDone}) // should not panic

	select {
	case <-agent.DoneCh():
		// OK
	default:
		t.Fatal("doneCh should be closed after EventDone")
	}
}

func TestDoneChBackupFinalizesAgentTurn(t *testing.T) {
	// When EventDone is dropped (1-slot buffer, pre-filled), the doneCh backup
	// path in drainAgentEvents must fully finalize the agent turn: stop the
	// ticker, commit streaming text, freeze elapsed time, and set agentRunning
	// to false — the same outcome as receiving EventDone directly.
	client := newTestClient("hello")
	agent := &Agent{
		id:                 generateAgentID(),
		client:             client,
		tools:              make(map[string]Tool),
		streamChunkTimeout: defaultStreamChunkTimeout,
		events:             make(chan AgentEvent, 1),
		approval:           make(chan ApprovalResponse, 1),
		doneCh:             make(chan struct{}),
	}

	// Fill the events channel so EventDone will be dropped.
	// Use EventToolCallDone (a no-op in handleAgentEvent) to avoid modifying streamingText.
	agent.events <- AgentEvent{Type: EventToolCallDone}

	// Close doneCh to simulate agent completion with dropped EventDone.
	agent.doneOnce.Do(func() { close(agent.doneCh) })

	app := &App{
		headless:       true,
		width:          80,
		agent:          agent,
		agentRunning:   true,
		agentStartTime: time.Now().Add(-2 * time.Second),
		streamingText:  "uncommitted text",
		needsTextSep:   true,
		resultCh:       make(chan any, 10),
	}
	app.agentTicker = time.NewTicker(50 * time.Millisecond)

	// drainAgentEvents should detect doneCh, drain the filler event, then
	// call finalizeAgentTurn("") which performs the full state transition.
	app.drainAgentEvents()

	if app.agentRunning {
		t.Error("agentRunning should be false after doneCh backup path")
	}
	if app.agentTicker != nil {
		t.Error("agentTicker should be nil (no active sub-agents)")
	}
	if app.streamingText != "" {
		t.Errorf("streamingText should be empty (committed), got %q", app.streamingText)
	}
	if app.agentElapsed == 0 {
		t.Error("agentElapsed should be non-zero (frozen at completion)")
	}
	// Verify the streaming text was committed as a message.
	found := false
	for _, m := range app.messages {
		if m.kind == msgAssistant && m.content == "uncommitted text" {
			found = true
			break
		}
	}
	if !found {
		t.Error("streaming text should have been committed as an assistant message")
	}
}

func TestEventDoneBlockingDelivery(t *testing.T) {
	// With a full events channel, emit(EventDone) should block while a
	// concurrent goroutine drains events, then successfully deliver EventDone.
	client := newTestClient("hello")
	agent := &Agent{
		id:                 generateAgentID(),
		client:             client,
		tools:              make(map[string]Tool),
		streamChunkTimeout: defaultStreamChunkTimeout,
		events:             make(chan AgentEvent, 2),
		approval:           make(chan ApprovalResponse, 1),
		doneCh:             make(chan struct{}),
	}

	// Fill the buffer completely.
	agent.events <- AgentEvent{Type: EventToolCallDone}
	agent.events <- AgentEvent{Type: EventToolCallDone}

	// Start draining after a short delay so emit has to block briefly.
	go func() {
		time.Sleep(50 * time.Millisecond)
		<-agent.events // free one slot
	}()

	// emit should block briefly, then deliver EventDone.
	done := make(chan struct{})
	go func() {
		agent.emit(AgentEvent{Type: EventDone})
		close(done)
	}()

	select {
	case <-done:
		// emit returned — EventDone was delivered or timed out.
	case <-time.After(3 * time.Second):
		t.Fatal("emit(EventDone) blocked for too long")
	}

	// Drain remaining events and verify EventDone is in the channel.
	var foundDone bool
	for {
		select {
		case ev := <-agent.events:
			if ev.Type == EventDone {
				foundDone = true
			}
		default:
			goto check
		}
	}
check:
	if !foundDone {
		t.Error("EventDone should have been delivered to the events channel")
	}

	// doneCh should also be closed.
	select {
	case <-agent.DoneCh():
		// OK
	default:
		t.Error("doneCh should be closed after EventDone emit")
	}
}

// --- Phase 3: Stream retry tests ---

// streamFailThenSucceedProvider sends partial chunks then a StreamEventError
// (simulating a mid-stream network failure) on specified call indices. On other
// calls it streams the full response normally.
type streamFailThenSucceedProvider struct {
	mu              sync.Mutex
	callIdx         int
	failStreamCalls map[int]bool       // call indices where stream should fail mid-response
	responses       []scriptedResponse // responses for all calls (fail or succeed)
	model           string
}

func (p *streamFailThenSucceedProvider) Complete(_ context.Context, _ *types.CompletionRequest) (*types.CompletionResponse, error) {
	// Not used in streaming tests, but required by the interface.
	return &types.CompletionResponse{
		ID: "resp-test", Model: p.model,
		Content:    []types.ContentBlock{{Type: "text", Text: "ok"}},
		StopReason: "end_turn",
		Usage:      types.Usage{InputTokens: 100, OutputTokens: 50},
	}, nil
}

func (p *streamFailThenSucceedProvider) Stream(_ context.Context, req *types.CompletionRequest) (<-chan types.StreamEvent, error) {
	p.mu.Lock()
	idx := p.callIdx
	p.callIdx++
	p.mu.Unlock()

	var r scriptedResponse
	if idx < len(p.responses) {
		r = p.responses[idx]
	} else {
		r = scriptedResponse{text: "ok", tokensIn: 100, tokensOut: 50}
	}

	ch := make(chan types.StreamEvent, 20)
	shouldFail := p.failStreamCalls[idx]

	go func() {
		defer close(ch)

		if shouldFail {
			// Send partial text then an error — simulates mid-stream network failure.
			if r.text != "" {
				ch <- types.StreamEvent{Type: types.StreamEventDelta, Content: "partial: " + r.text[:min(10, len(r.text))]}
			}
			ch <- types.StreamEvent{Type: types.StreamEventError, Error: fmt.Errorf("connection reset by peer")}
			return
		}

		// Normal streaming.
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

func (p *streamFailThenSucceedProvider) Name() string             { return "mock" }
func (p *streamFailThenSucceedProvider) Models() []types.ModelInfo { return nil }

func TestStreamRetrySucceedsAfterInterruption(t *testing.T) {
	// First stream fails mid-response, retry succeeds with full response.
	prov := &streamFailThenSucceedProvider{
		model:           "test-model",
		failStreamCalls: map[int]bool{0: true}, // first call fails
		responses: []scriptedResponse{
			{text: "Hello world", tokensIn: 100, tokensOut: 50},  // fails (partial)
			{text: "Hello world!", tokensIn: 100, tokensOut: 50}, // retry succeeds
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "hi"})
	events := drainEvents(t, agent.Events(), 10*time.Second)

	var hasRetry, hasClear, hasDone bool
	var finalText string
	for _, ev := range events {
		switch ev.Type {
		case EventRetry:
			hasRetry = true
		case EventStreamClear:
			hasClear = true
		case EventTextDelta:
			finalText += ev.Text
		case EventDone:
			hasDone = true
		}
	}
	if !hasRetry {
		t.Error("expected EventRetry for stream interruption")
	}
	if !hasClear {
		t.Error("expected EventStreamClear before retry")
	}
	if !hasDone {
		t.Error("expected EventDone — conversation should complete after retry")
	}
	if !strings.Contains(finalText, "Hello world!") {
		t.Errorf("final text = %q, want to contain full retry response", finalText)
	}
}

func TestStreamRetryGivesUpAfterMaxRetries(t *testing.T) {
	// Both stream attempts fail — should give up with an error.
	prov := &streamFailThenSucceedProvider{
		model:           "test-model",
		failStreamCalls: map[int]bool{0: true, 1: true}, // both calls fail
		responses: []scriptedResponse{
			{text: "attempt 1", tokensIn: 100, tokensOut: 50},
			{text: "attempt 2", tokensIn: 100, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "hi"})
	events := drainEvents(t, agent.Events(), 10*time.Second)

	var hasError, hasDone bool
	for _, ev := range events {
		switch ev.Type {
		case EventError:
			if ev.Error != nil && strings.Contains(ev.Error.Error(), "stream interrupted") {
				hasError = true
			}
		case EventDone:
			hasDone = true
		}
	}
	if !hasError {
		t.Error("expected 'stream interrupted' error after max retries")
	}
	if !hasDone {
		t.Error("expected EventDone after giving up")
	}
}

func TestStreamRetryEmitsStreamClearBeforeRetry(t *testing.T) {
	// Verify that EventStreamClear is emitted before EventRetry.
	prov := &streamFailThenSucceedProvider{
		model:           "test-model",
		failStreamCalls: map[int]bool{0: true},
		responses: []scriptedResponse{
			{text: "partial data", tokensIn: 100, tokensOut: 50},
			{text: "full data", tokensIn: 100, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "hi"})
	events := drainEvents(t, agent.Events(), 10*time.Second)

	// Find the relative positions of StreamClear and Retry.
	clearIdx, retryIdx := -1, -1
	for i, ev := range events {
		if ev.Type == EventStreamClear && clearIdx == -1 {
			clearIdx = i
		}
		if ev.Type == EventRetry && retryIdx == -1 {
			retryIdx = i
		}
	}
	if clearIdx == -1 {
		t.Fatal("expected EventStreamClear")
	}
	if retryIdx == -1 {
		t.Fatal("expected EventRetry")
	}
	if clearIdx >= retryIdx {
		t.Errorf("EventStreamClear (idx=%d) should come before EventRetry (idx=%d)", clearIdx, retryIdx)
	}
}

func TestStreamRetryNoRetryOnContextCancel(t *testing.T) {
	// Test retryableStream directly: when context is already canceled at the
	// point of stream failure, no retry should be attempted.
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	ctx, cancel := context.WithCancel(context.Background())
	callCount := 0

	promptFn := func() (*langdag.PromptResult, error) {
		callCount++
		stream := make(chan langdag.StreamChunk, 10)
		go func() {
			defer close(stream)
			stream <- langdag.StreamChunk{Content: "partial"}
			// Cancel context before sending error — simulates cancel during stream failure.
			cancel()
			stream <- langdag.StreamChunk{Error: fmt.Errorf("connection reset"), Done: true}
		}()
		return &langdag.PromptResult{Stream: stream}, nil
	}

	_, _, _, err := agent.retryableStream(ctx, promptFn)
	if err == nil {
		t.Error("expected error from retryableStream")
	}
	if callCount > 1 {
		t.Errorf("promptFn called %d times, want 1 (no retry on context cancel)", callCount)
	}

	// Should not have emitted any retry events.
	var hasRetry bool
	for {
		select {
		case ev := <-agent.events:
			if ev.Type == EventRetry {
				hasRetry = true
			}
		default:
			goto done
		}
	}
done:
	if hasRetry {
		t.Error("should not emit EventRetry when context is canceled")
	}
}

func TestStreamRetryDuringToolLoop(t *testing.T) {
	// First LLM call succeeds with a tool call. After tool execution, the
	// follow-up stream fails once then succeeds on retry.
	prov := &streamFailThenSucceedProvider{
		model:           "test-model",
		failStreamCalls: map[int]bool{1: true}, // second call (tool result) stream fails
		responses: []scriptedResponse{
			// Response 0: initial prompt → tool call (succeeds)
			{
				text: "Running tool.",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "c1", Name: "bash", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			// Response 1: tool result follow-up (stream fails)
			{text: "partial result", tokensIn: 200, tokensOut: 50},
			// Response 2: retry succeeds
			{text: "Tool completed successfully.", tokensIn: 200, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	bashTool := &testTool{name: "bash", result: "ok"}
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{bashTool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "run tool"})
	events := drainEvents(t, agent.Events(), 10*time.Second)

	var hasRetry, hasToolStart, hasClear bool
	var finalText string
	for _, ev := range events {
		switch ev.Type {
		case EventRetry:
			hasRetry = true
		case EventStreamClear:
			hasClear = true
		case EventToolCallStart:
			hasToolStart = true
		case EventTextDelta:
			finalText += ev.Text
		}
	}
	if !hasToolStart {
		t.Error("expected tool call before stream retry")
	}
	if !hasRetry {
		t.Error("expected EventRetry during tool loop")
	}
	if !hasClear {
		t.Error("expected EventStreamClear during tool loop retry")
	}
	if !strings.Contains(finalText, "Tool completed successfully") {
		t.Errorf("final text = %q, want success message after retry", finalText)
	}
}

// --- Phase 5: Integration Tests ---

// stallThenSucceedProvider sends partial text chunks then stalls (no Done, no
// error, no close) on specified calls. This simulates a provider that hangs
// mid-stream without sending an error or closing the connection — the exact
// scenario the per-chunk timeout is designed to catch.
type stallThenSucceedProvider struct {
	mu           sync.Mutex
	callIdx      int
	stallOnCalls map[int]bool       // call indices where stream should stall
	responses    []scriptedResponse // responses for all calls
	model        string
	cleanup      chan struct{}       // close to release stalled goroutines (test cleanup)
}

func (p *stallThenSucceedProvider) Complete(_ context.Context, _ *types.CompletionRequest) (*types.CompletionResponse, error) {
	return &types.CompletionResponse{
		ID: "resp-test", Model: p.model,
		Content:    []types.ContentBlock{{Type: "text", Text: "ok"}},
		StopReason: "end_turn",
		Usage:      types.Usage{InputTokens: 100, OutputTokens: 50},
	}, nil
}

func (p *stallThenSucceedProvider) Stream(_ context.Context, req *types.CompletionRequest) (<-chan types.StreamEvent, error) {
	p.mu.Lock()
	idx := p.callIdx
	p.callIdx++
	p.mu.Unlock()

	var r scriptedResponse
	if idx < len(p.responses) {
		r = p.responses[idx]
	} else {
		r = scriptedResponse{text: "ok", tokensIn: 100, tokensOut: 50}
	}

	ch := make(chan types.StreamEvent, 20)
	shouldStall := p.stallOnCalls[idx]

	go func() {
		defer close(ch)

		// Send partial text as individual word-chunks.
		if r.text != "" {
			for _, w := range strings.Fields(r.text) {
				ch <- types.StreamEvent{Type: types.StreamEventDelta, Content: w + " "}
			}
		}

		if shouldStall {
			// Stall: don't send Done. Block until cleanup releases this goroutine.
			// The channel stays open (deferred close fires only on return).
			<-p.cleanup
			return
		}

		// Normal completion.
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

func (p *stallThenSucceedProvider) Name() string             { return "mock" }
func (p *stallThenSucceedProvider) Models() []types.ModelInfo { return nil }

func TestIntegrationStreamStallRetrySucceeds(t *testing.T) {
	// Provider stalls on the first call (sends 3 chunks then hangs), retry succeeds.
	// Verifies the full flow: timeout → EventStreamClear → EventRetry → retry → complete response.
	prov := &stallThenSucceedProvider{
		model:        "test-model",
		stallOnCalls: map[int]bool{0: true},
		responses: []scriptedResponse{
			{text: "chunk one two three", tokensIn: 100, tokensOut: 50}, // call 0: stalls after sending chunks
			{text: "Complete response after retry.", tokensIn: 100, tokensOut: 50}, // call 1: succeeds
		},
		cleanup: make(chan struct{}),
	}
	defer close(prov.cleanup)

	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0}, WithStreamChunkTimeout(200*time.Millisecond)) // short timeout for fast test

	go agent.Run(context.Background(), RunOptions{UserMessage: "hello"})
	events := drainEvents(t, agent.Events(), 10*time.Second)

	var hasStreamClear, hasRetry, hasDone bool
	var hasTimeoutError bool
	var finalText string
	var retryEvent AgentEvent
	for _, ev := range events {
		switch ev.Type {
		case EventStreamClear:
			hasStreamClear = true
		case EventRetry:
			hasRetry = true
			retryEvent = ev
		case EventTextDelta:
			finalText += ev.Text
		case EventError:
			if ev.Error != nil && strings.Contains(ev.Error.Error(), "stream stalled") {
				hasTimeoutError = true
			}
		case EventDone:
			hasDone = true
		}
	}

	if !hasTimeoutError {
		t.Error("expected EventError about stream stall from drainStream timeout")
	}
	if !hasStreamClear {
		t.Error("expected EventStreamClear before retry")
	}
	if !hasRetry {
		t.Error("expected EventRetry after stream stall")
	}
	if hasRetry && retryEvent.Attempt != 1 {
		t.Errorf("retry attempt = %d, want 1", retryEvent.Attempt)
	}
	if !hasDone {
		t.Error("expected EventDone — agent should complete after successful retry")
	}
	if !strings.Contains(finalText, "Complete response after retry") {
		t.Errorf("final text = %q, should contain retry response", finalText)
	}
}

func TestIntegrationStreamStallRetryAlsoFails(t *testing.T) {
	// Provider stalls on both attempts. Agent should give up with an error
	// and exit cleanly.
	prov := &stallThenSucceedProvider{
		model:        "test-model",
		stallOnCalls: map[int]bool{0: true, 1: true},
		responses: []scriptedResponse{
			{text: "first stall", tokensIn: 100, tokensOut: 50},
			{text: "second stall", tokensIn: 100, tokensOut: 50},
		},
		cleanup: make(chan struct{}),
	}
	defer close(prov.cleanup)

	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0}, WithStreamChunkTimeout(200*time.Millisecond))

	go agent.Run(context.Background(), RunOptions{UserMessage: "hello"})
	events := drainEvents(t, agent.Events(), 10*time.Second)

	var hasDone bool
	var timeoutErrors, retryCount int
	var hasInterruptedError bool
	for _, ev := range events {
		switch ev.Type {
		case EventRetry:
			retryCount++
		case EventError:
			if ev.Error != nil {
				if strings.Contains(ev.Error.Error(), "stream stalled") {
					timeoutErrors++
				}
				if strings.Contains(ev.Error.Error(), "stream interrupted") {
					hasInterruptedError = true
				}
			}
		case EventDone:
			hasDone = true
		}
	}

	if timeoutErrors < 2 {
		t.Errorf("expected at least 2 stream stall errors (one per attempt), got %d", timeoutErrors)
	}
	if retryCount != 1 {
		t.Errorf("expected 1 EventRetry (retry once then give up), got %d", retryCount)
	}
	if !hasInterruptedError {
		t.Error("expected final 'stream interrupted' error when both attempts fail")
	}
	if !hasDone {
		t.Error("expected EventDone — agent should exit cleanly even after exhausting retries")
	}
}

func TestIntegrationBackpressure(t *testing.T) {
	// Agent with a tiny event buffer. Generates enough events (tool call +
	// follow-up) to overflow the buffer. Verifies that:
	// - The agent completes without deadlock (non-blocking emit)
	// - doneCh fires even when EventDone is dropped from the full channel
	// - No goroutine leaks
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "Running the tool now.",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "c1", Name: "bash", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "Tool result processed successfully.", tokensIn: 200, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	bashTool := &testTool{name: "bash", result: "command output"}
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{bashTool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})
	// Replace events channel with a tiny buffer to guarantee backpressure.
	// The agent will generate ~8+ events (TextDelta, Usage, ToolCallStart,
	// ToolCallDone, ToolResult, TextDelta, Usage, Done), so a buffer of 4
	// guarantees that later events including EventDone will be dropped.
	agent.events = make(chan AgentEvent, 4)

	go agent.Run(context.Background(), RunOptions{UserMessage: "run tool"})

	// Do NOT drain events — let the buffer fill up and force event drops.
	// Wait for doneCh as the reliable completion signal.
	select {
	case <-agent.DoneCh():
		// Agent completed — doneCh was closed even though EventDone was dropped.
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for doneCh — agent may be deadlocked due to backpressure")
	}

	// Drain whatever events remain in the small buffer.
	var events []AgentEvent
	for {
		select {
		case ev, ok := <-agent.Events():
			if !ok {
				goto drained // channel closed by Run()
			}
			events = append(events, ev)
		default:
			goto drained
		}
	}
drained:

	// With a buffer of 4, we should have captured exactly 4 events (the buffer was full).
	if len(events) == 0 {
		t.Error("expected at least some events in the buffer")
	}
	if len(events) > 4 {
		t.Errorf("buffer was 4 but got %d events — buffer wasn't full?", len(events))
	}

	// The critical assertion is that doneCh fired (tested above), proving that
	// the agent completed without deadlock despite backpressure. Whether EventDone
	// landed in the buffer depends on exact timing — doneCh is the reliable signal.
	var doneInBuffer bool
	for _, ev := range events {
		if ev.Type == EventDone {
			doneInBuffer = true
		}
	}
	t.Logf("captured %d events from buffer of 4; EventDone in buffer: %v", len(events), doneInBuffer)
}

// --- Background result injection tests ---

func TestAgentInjectAndDrainBackgroundResults(t *testing.T) {
	agent := NewAgent(NewAgentOptions{Client: nil, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	// Initially empty.
	if results := agent.drainBackgroundResults(); results != nil {
		t.Errorf("expected nil, got %v", results)
	}

	// Inject two results.
	agent.InjectBackgroundResult("result-1")
	agent.InjectBackgroundResult("result-2")

	// Drain should return both.
	results := agent.drainBackgroundResults()
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0] != "result-1" || results[1] != "result-2" {
		t.Errorf("results = %v, want [result-1, result-2]", results)
	}

	// Drain again should return nil (cleared).
	if results := agent.drainBackgroundResults(); results != nil {
		t.Errorf("expected nil after drain, got %v", results)
	}
}

func TestAgentInjectBackgroundResultConcurrent(t *testing.T) {
	agent := NewAgent(NewAgentOptions{Client: nil, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	// Inject from multiple goroutines concurrently.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			agent.InjectBackgroundResult(fmt.Sprintf("result-%d", n))
		}(i)
	}
	wg.Wait()

	results := agent.drainBackgroundResults()
	if len(results) != 10 {
		t.Errorf("expected 10 results, got %d", len(results))
	}
}

// --- Phase 5: max_tokens handling in herm agent loop ---

func TestRunMaxTokensEmptyResponse(t *testing.T) {
	// When the model returns max_tokens with no usable content, langdag's
	// Phase 4b emits an error (no node saved). The agent should surface an
	// error event to the user rather than silently succeeding.
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{text: "", stopReason: "max_tokens", tokensIn: 100, tokensOut: 0},
			// Retry will hit the same response.
			{text: "", stopReason: "max_tokens", tokensIn: 100, tokensOut: 0},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "write a huge file"})
	events := drainEvents(t, agent.Events(), 5*time.Second)

	var hasError, hasDone bool
	for _, ev := range events {
		switch ev.Type {
		case EventError:
			hasError = true
		case EventDone:
			hasDone = true
		}
	}
	if !hasError {
		t.Error("expected EventError for max_tokens with empty response")
	}
	if !hasDone {
		t.Error("expected EventDone")
	}
}

func TestRunMaxTokensWithPartialText(t *testing.T) {
	// When the model returns max_tokens but has partial text, langdag
	// automatically continues generation (output groups). The agent sees
	// all text deltas transparently — both from the truncated first call
	// and the continuation.
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{text: "Here is the beginning", stopReason: "max_tokens", tokensIn: 100, tokensOut: 500},
			{text: " and here is the rest", stopReason: "end_turn", tokensIn: 600, tokensOut: 200},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "write a huge file"})
	events := drainEvents(t, agent.Events(), 5*time.Second)

	var allText string
	var hasDone, hasUsage, hasError bool
	for _, ev := range events {
		switch ev.Type {
		case EventTextDelta:
			allText += ev.Text
		case EventUsage:
			hasUsage = true
		case EventError:
			hasError = true
		case EventDone:
			hasDone = true
		}
	}
	if allText != "Here is the beginning and here is the rest" {
		t.Errorf("accumulated text = %q, want both parts concatenated", allText)
	}
	if !hasUsage {
		t.Error("expected EventUsage")
	}
	if hasError {
		t.Error("unexpected EventError — continuation should be transparent")
	}
	if !hasDone {
		t.Error("expected EventDone")
	}
}

func TestRunMaxTokensCrashChain_ConversationNotCorrupted(t *testing.T) {
	// Full crash chain reproduction at the herm level:
	// 1. Agent sends prompt → mock returns max_tokens with no content
	// 2. Verify agent emits appropriate error
	// 3. Verify the conversation state is not corrupted — a follow-up prompt works
	//
	// The scriptedProvider goes through langdag, so Phase 4b intercepts the
	// empty max_tokens response. The agent should see errors and complete gracefully.
	// Then a second Run() on the same client should succeed normally.

	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			// Calls 0-1: max_tokens empty (call 0 is initial, call 1 is the retry)
			{text: "", stopReason: "max_tokens", tokensIn: 100, tokensOut: 0},
			{text: "", stopReason: "max_tokens", tokensIn: 100, tokensOut: 0},
			// Call 2: normal response for the follow-up prompt
			{text: "All good now", tokensIn: 100, tokensOut: 30},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	// First run: max_tokens with empty content → error expected.
	agent1 := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})
	go agent1.Run(context.Background(), RunOptions{UserMessage: "generate a huge file"})
	events1 := drainEvents(t, agent1.Events(), 5*time.Second)

	var hasError1, hasDone1 bool
	for _, ev := range events1 {
		switch ev.Type {
		case EventError:
			hasError1 = true
		case EventDone:
			hasDone1 = true
		}
	}
	if !hasError1 {
		t.Error("first run: expected EventError for max_tokens with empty response")
	}
	if !hasDone1 {
		t.Error("first run: expected EventDone")
	}

	// Second run: normal response — conversation state should not be corrupted.
	agent2 := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})
	go agent2.Run(context.Background(), RunOptions{UserMessage: "try something simpler"})
	events2 := drainEvents(t, agent2.Events(), 5*time.Second)

	var hasText2, hasDone2, hasError2 bool
	for _, ev := range events2 {
		switch ev.Type {
		case EventTextDelta:
			hasText2 = true
			if ev.Text != "All good now" {
				t.Errorf("second run: text = %q, want %q", ev.Text, "All good now")
			}
		case EventError:
			hasError2 = true
			t.Errorf("second run: unexpected error: %v", ev.Error)
		case EventDone:
			hasDone2 = true
		}
	}
	if !hasText2 {
		t.Error("second run: expected EventTextDelta")
	}
	if hasError2 {
		t.Error("second run: conversation state corrupted — follow-up prompt failed")
	}
	if !hasDone2 {
		t.Error("second run: expected EventDone")
	}
}

// --- Phase 6: Agent Loop Silent Failures & Edge Cases ---

// --- Task 6a: Audit silent error paths ---

// getNodeErrorStorage wraps mockStorage but errors on GetNode for specific IDs.
type getNodeErrorStorage struct {
	*mockStorage
	failIDs map[string]bool
}

func (s *getNodeErrorStorage) GetNode(_ context.Context, id string) (*types.Node, error) {
	if s.failIDs[id] {
		return nil, fmt.Errorf("storage error: node %s unavailable", id)
	}
	return s.mockStorage.GetNode(context.Background(), id)
}

// TestEmitUsage_StorageError verifies that emitUsage returns 0 and the agent
// continues normally when client.GetNode() fails. The error is silent by design
// (usage is display-only), but must not crash or hang.
func TestEmitUsage_StorageError(t *testing.T) {
	store := &getNodeErrorStorage{
		mockStorage: newMockStorage(),
		failIDs:     map[string]bool{"node-1": true},
	}
	prov := &mockProvider{model: "test-model", responses: []string{"hello"}}
	client := langdag.NewWithDeps(store, prov)

	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	// emitUsage with a node ID that will fail in storage.
	result := agent.emitUsage(context.Background(), emitUsageOptions{nodeID: "node-1", stopReason: "end_turn"})
	if result != 0 {
		t.Errorf("emitUsage returned %d, want 0 on storage error", result)
	}

	// Session stats should not change.
	if agent.sessionInputTokens != 0 {
		t.Errorf("sessionInputTokens = %d, want 0", agent.sessionInputTokens)
	}
	if agent.sessionOutputTokens != 0 {
		t.Errorf("sessionOutputTokens = %d, want 0", agent.sessionOutputTokens)
	}
}

// TestEmitUsage_EmptyNodeID verifies that emitUsage returns 0 immediately
// for an empty node ID without calling storage.
func TestEmitUsage_EmptyNodeID(t *testing.T) {
	prov := &mockProvider{model: "test-model"}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	result := agent.emitUsage(context.Background(), emitUsageOptions{nodeID: "", stopReason: "end_turn"})
	if result != 0 {
		t.Errorf("emitUsage returned %d, want 0 for empty nodeID", result)
	}
}

// TestEmitUsage_NilNode verifies that emitUsage returns 0 when the node
// is not found (GetNode returns nil, nil).
func TestEmitUsage_NilNode(t *testing.T) {
	store := newMockStorage() // has no nodes
	prov := &mockProvider{model: "test-model"}
	client := langdag.NewWithDeps(store, prov)
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	result := agent.emitUsage(context.Background(), emitUsageOptions{nodeID: "nonexistent", stopReason: "end_turn"})
	if result != 0 {
		t.Errorf("emitUsage returned %d, want 0 for nil node", result)
	}
}

// getAncestorsErrorStorage wraps mockStorage but errors on GetAncestors
// after a configurable number of successful calls. This lets langdag's
// internal PromptFrom calls succeed while the agent's clearOldToolResults
// or maybeCompact calls fail.
type getAncestorsErrorStorage struct {
	*mockStorage
	mu            sync.Mutex
	callCount     int
	failAfterCall int // 0 = always fail; N = fail on call N+1 and beyond
}

func (s *getAncestorsErrorStorage) GetAncestors(ctx context.Context, id string) ([]*types.Node, error) {
	s.mu.Lock()
	s.callCount++
	n := s.callCount
	s.mu.Unlock()

	if s.failAfterCall > 0 && n <= s.failAfterCall {
		return s.mockStorage.GetAncestors(ctx, id)
	}
	return nil, fmt.Errorf("storage error: ancestors unavailable")
}

// TestClearOldToolResults_StorageError verifies that clearOldToolResults
// returns silently when GetAncestors fails. The agent should continue without
// clearing — risk is context window overflow on the next call.
func TestClearOldToolResults_StorageError(t *testing.T) {
	store := &getAncestorsErrorStorage{mockStorage: newMockStorage(), failAfterCall: 0}
	prov := &mockProvider{model: "test-model"}
	client := langdag.NewWithDeps(store, prov)

	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 100000})

	// Input tokens above threshold → would normally clear, but GetAncestors fails.
	// Should not panic or hang.
	agent.clearOldToolResults(context.Background(), clearOldToolResultsOptions{nodeID: "some-node", inputTokens: 90000})
}

// TestClearOldToolResults_AgentContinuesOnFailure verifies the full agent loop
// continues normally even when clearOldToolResults fails (GetAncestors error).
func TestClearOldToolResults_AgentContinuesOnFailure(t *testing.T) {
	// Use a provider that returns tool_use, then a final text response.
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			// Call 0: use a tool
			{
				text: "Let me check",
				toolCalls: []types.ContentBlock{{
					Type:  "tool_use",
					ID:    "call_1",
					Name:  "test_tool",
					Input: json.RawMessage(`{}`),
				}},
				tokensIn:  90000, // above threshold
				tokensOut: 100,
			},
			// Call 1: final response after tool result
			{text: "Done", tokensIn: 95000, tokensOut: 50},
		},
	}
	// Allow 2 GetAncestors calls to succeed (langdag's internal PromptFrom calls),
	// then fail on the 3rd+ (agent's clearOldToolResults call).
	store := &getAncestorsErrorStorage{mockStorage: newMockStorage(), failAfterCall: 2}
	client := langdag.NewWithDeps(store, prov)

	tool := &testTool{name: "test_tool", result: "file.txt"}
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{tool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 100000})

	go agent.Run(context.Background(), RunOptions{UserMessage: "check files"})
	events := drainEvents(t, agent.Events(), 5*time.Second)

	var hasDone, hasText bool
	for _, ev := range events {
		if ev.Type == EventDone {
			hasDone = true
		}
		if ev.Type == EventTextDelta && ev.Text == "Done" {
			hasText = true
		}
	}
	if !hasDone {
		t.Error("expected EventDone — agent should complete despite clearOldToolResults failure")
	}
	if !hasText {
		t.Error("expected final text response — agent should continue after clearing failure")
	}
}

// TestMaybeCompact_CompactionFailure verifies that maybeCompact returns the
// original nodeID when compactConversation fails (e.g., LLM failure).
func TestMaybeCompact_CompactionFailure(t *testing.T) {
	// Use failingProvider so the LLM call inside compactConversation fails.
	store := newClearingMockStorage()
	prov := &failingProvider{}
	client := langdag.NewWithDeps(store, prov)

	// Build a conversation chain long enough for compaction (> compactKeepRecent=6).
	nodes := make([]*types.Node, 10)
	for i := range nodes {
		parentID := ""
		if i > 0 {
			parentID = fmt.Sprintf("node-%d", i-1)
		}
		nodes[i] = &types.Node{
			ID:       fmt.Sprintf("node-%d", i),
			ParentID: parentID,
			NodeType: types.NodeTypeUser,
			Content:  fmt.Sprintf("message %d", i),
		}
		if i%2 == 1 {
			nodes[i].NodeType = types.NodeTypeAssistant
		}
	}

	var ancestorIDs []string
	for _, n := range nodes {
		_ = store.CreateNode(context.Background(), n)
		ancestorIDs = append(ancestorIDs, n.ID)
	}
	leafID := nodes[len(nodes)-1].ID
	store.ancestorChains[leafID] = ancestorIDs

	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 100000})

	// Input tokens at 96% of context window → above compactThresholdFraction (0.95).
	result := agent.maybeCompact(context.Background(), maybeCompactOptions{nodeID: leafID, inputTokens: 96000})
	if result != leafID {
		t.Errorf("maybeCompact returned %q, want %q (original) on failure", result, leafID)
	}
}

// TestMaybeCompact_AgentContinuesAfterFailure verifies the agent loop continues
// with the original nodeID when compaction fails.
func TestMaybeCompact_AgentContinuesAfterFailure(t *testing.T) {
	// First call: tool_use with high tokens to trigger compaction.
	// Second call: final text response.
	// compactConversation needs GetAncestors which will fail → compaction fails →
	// agent continues with original nodeID.
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "Checking",
				toolCalls: []types.ContentBlock{{
					Type:  "tool_use",
					ID:    "call_1",
					Name:  "test_tool",
					Input: json.RawMessage(`{}`),
				}},
				tokensIn:  96000, // above compaction threshold
				tokensOut: 100,
			},
			{text: "Finished", tokensIn: 5000, tokensOut: 50},
		},
	}
	// Allow 2 GetAncestors calls to succeed (langdag's internal PromptFrom calls),
	// then fail on 3rd+ (clearOldToolResults + compactConversation).
	store := &getAncestorsErrorStorage{mockStorage: newMockStorage(), failAfterCall: 2}
	client := langdag.NewWithDeps(store, prov)

	tool := &testTool{name: "test_tool", result: "ok"}
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{tool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 100000})

	go agent.Run(context.Background(), RunOptions{UserMessage: "do something"})
	events := drainEvents(t, agent.Events(), 5*time.Second)

	var hasDone, hasFinished bool
	for _, ev := range events {
		if ev.Type == EventDone {
			hasDone = true
		}
		if ev.Type == EventTextDelta && ev.Text == "Finished" {
			hasFinished = true
		}
	}
	if !hasDone {
		t.Error("expected EventDone — agent should complete despite compaction failure")
	}
	if !hasFinished {
		t.Error("expected 'Finished' text — agent should continue after compaction failure")
	}
}

// TestReplaceToolResultContent_MalformedJSON verifies replaceToolResultContent
// returns the original content for various malformed inputs.
func TestReplaceToolResultContent_MalformedJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
	}{
		{"empty string", ""},
		{"plain text", "just some text"},
		{"partial JSON", `[{"type":"tool_result"`},
		{"JSON object not array", `{"type":"tool_result"}`},
		{"null", "null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := replaceToolResultContent(tt.input)
			if result != tt.input {
				t.Errorf("replaceToolResultContent(%q) = %q, want original", tt.input, result)
			}
		})
	}
}

// TestReplaceToolResultContent_MixedBlockTypes verifies that only tool_result
// blocks are replaced; text and tool_use blocks are preserved.
func TestReplaceToolResultContent_MixedBlockTypes(t *testing.T) {
	blocks := []types.ContentBlock{
		{Type: "text", Text: "some text"},
		{Type: "tool_result", ToolUseID: "call_1", Content: "big output here"},
		{Type: "tool_use", ID: "call_2", Name: "bash"},
	}
	data, _ := json.Marshal(blocks)
	result := replaceToolResultContent(string(data))

	var parsed []types.ContentBlock
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(parsed) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(parsed))
	}
	if parsed[0].Text != "some text" {
		t.Errorf("text block changed: %q", parsed[0].Text)
	}
	if parsed[1].Content != "[output cleared]" {
		t.Errorf("tool_result not cleared: %q", parsed[1].Content)
	}
	if parsed[2].Name != "bash" {
		t.Errorf("tool_use block changed: name=%q", parsed[2].Name)
	}
}

// --- Task 6b: Tool execution error edge cases ---

// TestToolExecution_VeryLongError verifies that a tool returning an error with
// a very long message (>30KB) is handled gracefully: emitted as IsError tool
// result, no hang or crash.
func TestToolExecution_VeryLongError(t *testing.T) {
	longMsg := strings.Repeat("x", 35000) // 35KB error message
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "Running tool",
				toolCalls: []types.ContentBlock{{
					Type:  "tool_use",
					ID:    "call_1",
					Name:  "failing_tool",
					Input: json.RawMessage(`{}`),
				}},
			},
			{text: "Handled the error"},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	tool := &testTool{name: "failing_tool", err: fmt.Errorf("%s", longMsg)}
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{tool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "run it"})
	events := drainEvents(t, agent.Events(), 5*time.Second)

	var foundToolResult, hasDone bool
	for _, ev := range events {
		if ev.Type == EventToolResult && ev.ToolName == "failing_tool" {
			foundToolResult = true
			if !ev.IsError {
				t.Error("tool result should be IsError=true")
			}
			if len(ev.ToolResult) < 30000 {
				t.Errorf("error message truncated: len=%d, want >30000", len(ev.ToolResult))
			}
		}
		if ev.Type == EventDone {
			hasDone = true
		}
	}
	if !foundToolResult {
		t.Error("expected EventToolResult for the failing tool")
	}
	if !hasDone {
		t.Error("expected EventDone — agent should handle long error gracefully")
	}
}

// TestToolExecution_Panic verifies that a tool that panics during Execute()
// is caught by the agent's top-level recover and produces EventError + EventDone.
func TestToolExecution_Panic(t *testing.T) {
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "Running tool",
				toolCalls: []types.ContentBlock{{
					Type:  "tool_use",
					ID:    "call_1",
					Name:  "panic_tool",
					Input: json.RawMessage(`{}`),
				}},
			},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	tool := &panicTool{name: "panic_tool", msg: "tool exploded!"}
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{tool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "do it"})
	events := drainEvents(t, agent.Events(), 5*time.Second)

	var hasError, hasDone bool
	for _, ev := range events {
		if ev.Type == EventError {
			hasError = true
			if !strings.Contains(ev.Error.Error(), "panic") {
				t.Errorf("error should mention panic, got: %v", ev.Error)
			}
			if !strings.Contains(ev.Error.Error(), "tool exploded") {
				t.Errorf("error should contain panic value, got: %v", ev.Error)
			}
		}
		if ev.Type == EventDone {
			hasDone = true
		}
	}
	if !hasError {
		t.Error("expected EventError from tool panic")
	}
	if !hasDone {
		t.Error("expected EventDone after panic recovery")
	}
}

// TestToolExecution_UnknownTool verifies that an LLM requesting an unknown tool
// produces a tool result with IsError=true and the agent continues.
func TestToolExecution_UnknownTool(t *testing.T) {
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "Let me use a tool",
				toolCalls: []types.ContentBlock{{
					Type:  "tool_use",
					ID:    "call_1",
					Name:  "nonexistent_tool",
					Input: json.RawMessage(`{}`),
				}},
			},
			{text: "OK, that tool does not exist"},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	// Register a different tool — nonexistent_tool is not registered.
	tool := &testTool{name: "other_tool", result: "ok"}
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{tool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "use tool"})
	events := drainEvents(t, agent.Events(), 5*time.Second)

	var foundToolResult, hasDone, hasFollowup bool
	for _, ev := range events {
		if ev.Type == EventToolResult && ev.ToolName == "nonexistent_tool" {
			foundToolResult = true
			if !ev.IsError {
				t.Error("unknown tool result should be IsError=true")
			}
			if !strings.Contains(ev.ToolResult, "unknown tool") {
				t.Errorf("error should mention unknown tool, got: %q", ev.ToolResult)
			}
		}
		if ev.Type == EventTextDelta && strings.Contains(ev.Text, "does not exist") {
			hasFollowup = true
		}
		if ev.Type == EventDone {
			hasDone = true
		}
	}
	if !foundToolResult {
		t.Error("expected EventToolResult for unknown tool")
	}
	if !hasFollowup {
		t.Error("expected LLM follow-up response after unknown tool error")
	}
	if !hasDone {
		t.Error("expected EventDone")
	}
}

// --- Task 6c: Approval flow interruption ---

// TestApprovalFlow_ContextCanceled verifies that canceling the context while
// the agent is waiting for approval produces EventError + EventDone, no
// goroutine leak, no deadlock.
func TestApprovalFlow_ContextCanceled(t *testing.T) {
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "I need to run a command",
				toolCalls: []types.ContentBlock{{
					Type:  "tool_use",
					ID:    "call_1",
					Name:  "risky_tool",
					Input: json.RawMessage(`{"cmd":"rm -rf /"}`),
				}},
			},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	tool := &testTool{name: "risky_tool", result: "done", requiresApproval: true}
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{tool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	ctx, cancel := context.WithCancel(context.Background())

	go agent.Run(ctx, RunOptions{UserMessage: "do something dangerous"})

	// Wait for the approval request to be emitted.
	var approvalSeen bool
	timeout := time.After(5 * time.Second)
	for !approvalSeen {
		select {
		case ev := <-agent.Events():
			if ev.Type == EventApprovalReq {
				approvalSeen = true
			}
		case <-timeout:
			t.Fatal("timeout waiting for EventApprovalReq")
		}
	}

	// Cancel context while approval is pending.
	cancel()

	// Drain remaining events.
	var hasError, hasDone bool
	for {
		select {
		case ev, ok := <-agent.Events():
			if !ok {
				goto done
			}
			if ev.Type == EventError {
				hasError = true
				if !strings.Contains(ev.Error.Error(), "context canceled") {
					t.Errorf("error should be context canceled, got: %v", ev.Error)
				}
			}
			if ev.Type == EventDone {
				hasDone = true
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for events after cancel")
		}
	}
done:
	if !hasError {
		t.Error("expected EventError with context canceled")
	}
	if !hasDone {
		t.Error("expected EventDone after context cancellation during approval")
	}
}

// TestApprovalFlow_Denied verifies that denying a tool call produces an
// IsError tool result and the agent continues with the next LLM call.
func TestApprovalFlow_Denied(t *testing.T) {
	prov := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "I need to run a command",
				toolCalls: []types.ContentBlock{{
					Type:  "tool_use",
					ID:    "call_1",
					Name:  "risky_tool",
					Input: json.RawMessage(`{"cmd":"rm -rf /"}`),
				}},
			},
			{text: "OK, I won't do that"},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	tool := &testTool{name: "risky_tool", result: "done", requiresApproval: true}
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{tool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "delete everything"})

	// Wait for approval request, then deny.
	timeout2 := time.After(5 * time.Second)
	for {
		select {
		case ev := <-agent.Events():
			if ev.Type == EventApprovalReq {
				agent.Approve(ApprovalResponse{Approved: false})
				goto drain
			}
		case <-timeout2:
			t.Fatal("timeout waiting for approval request")
		}
	}
drain:
	var events []AgentEvent
	for {
		select {
		case ev, ok := <-agent.Events():
			if !ok {
				goto check
			}
			events = append(events, ev)
			if ev.Type == EventDone {
				goto check
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout draining events")
		}
	}
check:
	var foundDenied, hasDone2, hasFollowup bool
	for _, ev := range events {
		if ev.Type == EventToolResult && ev.ToolName == "risky_tool" {
			foundDenied = true
			if !ev.IsError {
				t.Error("denied tool result should be IsError=true")
			}
			if !strings.Contains(ev.ToolResult, "denied") {
				t.Errorf("denied result should mention denial, got: %q", ev.ToolResult)
			}
		}
		if ev.Type == EventTextDelta && strings.Contains(ev.Text, "won't") {
			hasFollowup = true
		}
		if ev.Type == EventDone {
			hasDone2 = true
		}
	}
	if !foundDenied {
		t.Error("expected denied tool result")
	}
	if !hasFollowup {
		t.Error("expected follow-up response after denial")
	}
	if !hasDone2 {
		t.Error("expected EventDone")
	}
}

// --- Task 6d: Max tool iterations boundary ---

// alwaysToolProvider returns a tool_use on every call, forcing the agent to
// loop until maxToolIterations is hit.
type alwaysToolProvider struct {
	mu      sync.Mutex
	callIdx int
	model   string
}

func (p *alwaysToolProvider) Complete(_ context.Context, _ *types.CompletionRequest) (*types.CompletionResponse, error) {
	p.mu.Lock()
	idx := p.callIdx
	p.callIdx++
	p.mu.Unlock()
	return &types.CompletionResponse{
		ID: fmt.Sprintf("resp-%d", idx), Model: p.model,
		Content: []types.ContentBlock{
			{Type: "text", Text: fmt.Sprintf("step %d", idx)},
			{Type: "tool_use", ID: fmt.Sprintf("call_%d", idx), Name: "step_tool", Input: json.RawMessage(`{}`)},
		},
		StopReason: "tool_use",
		Usage:      types.Usage{InputTokens: 100, OutputTokens: 50},
	}, nil
}

func (p *alwaysToolProvider) Stream(_ context.Context, req *types.CompletionRequest) (<-chan types.StreamEvent, error) {
	p.mu.Lock()
	idx := p.callIdx
	p.callIdx++
	p.mu.Unlock()

	ch := make(chan types.StreamEvent, 10)
	go func() {
		defer close(ch)
		text := fmt.Sprintf("step %d", idx)
		tc := types.ContentBlock{Type: "tool_use", ID: fmt.Sprintf("call_%d", idx), Name: "step_tool", Input: json.RawMessage(`{}`)}
		ch <- types.StreamEvent{Type: types.StreamEventDelta, Content: text}
		ch <- types.StreamEvent{Type: types.StreamEventContentDone, ContentBlock: &tc}
		ch <- types.StreamEvent{
			Type: types.StreamEventDone,
			Response: &types.CompletionResponse{
				ID: fmt.Sprintf("resp-%d", idx), Model: req.Model,
				Content:    []types.ContentBlock{{Type: "text", Text: text}, tc},
				StopReason: "tool_use",
				Usage:      types.Usage{InputTokens: 100, OutputTokens: 50},
			},
		}
	}()
	return ch, nil
}

func (p *alwaysToolProvider) Name() string             { return "mock" }
func (p *alwaysToolProvider) Models() []types.ModelInfo { return nil }

// TestMaxToolIterations verifies that when the agent reaches exactly
// maxToolIterations, it emits a clear error message and stops gracefully.
func TestMaxToolIterations(t *testing.T) {
	prov := &alwaysToolProvider{model: "test-model"}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	maxIter := 3
	tool := &testTool{name: "step_tool", result: "ok"}
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{tool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0}, WithMaxToolIterations(maxIter))

	go agent.Run(context.Background(), RunOptions{UserMessage: "loop forever"})
	events := drainEvents(t, agent.Events(), 10*time.Second)

	// Count tool executions.
	var toolExecs int
	var hasError, hasDone bool
	var lastNodeID string
	for _, ev := range events {
		if ev.Type == EventToolCallDone {
			toolExecs++
		}
		if ev.Type == EventError {
			hasError = true
			if !strings.Contains(ev.Error.Error(), "maximum tool iterations") {
				t.Errorf("error should mention max iterations, got: %v", ev.Error)
			}
		}
		if ev.Type == EventDone {
			hasDone = true
			lastNodeID = ev.NodeID
		}
	}

	if toolExecs != maxIter {
		t.Errorf("tool executions = %d, want exactly %d", toolExecs, maxIter)
	}
	if !hasError {
		t.Error("expected EventError when max tool iterations reached")
	}
	if !hasDone {
		t.Error("expected EventDone")
	}
	if lastNodeID == "" {
		t.Error("EventDone should have a valid nodeID for conversation resume")
	}
}

// TestMaxToolIterations_DefaultValue verifies that the default maxToolIterations
// (200) is used when none is configured.
func TestMaxToolIterations_DefaultValue(t *testing.T) {
	agent := NewAgent(NewAgentOptions{Client: newTestClient("ok"), Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})
	if agent.maxToolIterations != 0 {
		t.Errorf("maxToolIterations = %d, want 0 (uses default %d)", agent.maxToolIterations, defaultMaxToolIterations)
	}
}

// --- Phase 13: Integration — End-to-End Error Chains ---

// TestE2EPermanentErrorChain verifies the full error chain when a provider
// returns a non-retryable error (401): provider → langdag retry (skipped) →
// agent emits EventError with original message → EventDone. No EventRetry
// should appear. The original error message must survive all wrapping layers
// so the user sees "401" and "Unauthorized".
func TestE2EPermanentErrorChain(t *testing.T) {
	prov := &failThenSucceedProvider{
		model: "test-model",
		failOnCalls: map[int]error{
			0: fmt.Errorf("HTTP 401: Unauthorized — invalid API key"),
		},
		responses: []scriptedResponse{
			{text: "should never reach this", tokensIn: 100, tokensOut: 50},
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "hello"})
	events := drainEvents(t, agent.Events(), 5*time.Second)

	// Verify the complete event sequence.
	var hasLLMStart, hasError, hasDone bool
	var hasRetry, hasTextDelta, hasUsage bool
	var errorMsg string
	for _, ev := range events {
		switch ev.Type {
		case EventLLMStart:
			hasLLMStart = true
		case EventError:
			hasError = true
			if ev.Error != nil {
				errorMsg = ev.Error.Error()
			}
		case EventDone:
			hasDone = true
			if ev.NodeID != "" {
				t.Errorf("EventDone.NodeID should be empty on permanent error, got %q", ev.NodeID)
			}
		case EventRetry:
			hasRetry = true
		case EventTextDelta:
			hasTextDelta = true
		case EventUsage:
			hasUsage = true
		}
	}

	if !hasLLMStart {
		t.Error("expected EventLLMStart — agent should attempt the LLM call")
	}
	if hasRetry {
		t.Error("should NOT retry on 401 — it's a permanent error")
	}
	if hasTextDelta {
		t.Error("should NOT have text deltas — the call fails before streaming")
	}
	if hasUsage {
		t.Error("should NOT have usage — no successful call was made")
	}
	if !hasError {
		t.Fatal("expected EventError with the original API error")
	}
	// The original error message must be preserved through the wrapping chain.
	if !strings.Contains(errorMsg, "401") {
		t.Errorf("error should contain '401', got: %q", errorMsg)
	}
	if !strings.Contains(errorMsg, "Unauthorized") {
		t.Errorf("error should contain 'Unauthorized', got: %q", errorMsg)
	}
	if !hasDone {
		t.Error("expected EventDone after permanent error")
	}
}

// TestE2ETransientRetryChain verifies the full chain when a provider returns
// transient errors (503) that eventually succeed: provider fails → langdag retry
// emits EventRetry via context callback → provider succeeds → agent streams
// response → EventUsage (only from success) → EventDone.
func TestE2ETransientRetryChain(t *testing.T) {
	prov := &failThenSucceedProvider{
		model: "test-model",
		failOnCalls: map[int]error{
			0: fmt.Errorf("HTTP 503: service unavailable"),
			1: fmt.Errorf("HTTP 503: service unavailable"),
		},
		responses: []scriptedResponse{
			{text: "Success after retries!", tokensIn: 200, tokensOut: 75},
		},
	}
	store := newMockStorage()
	// Wrap with retry (short delays for fast tests) so langdag handles transient errors.
	retryProv := langdag.WithRetry(prov, langdag.RetryConfig{
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
		MaxDelay:   10 * time.Millisecond,
	})
	client := langdag.NewWithDeps(store, retryProv)
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "hello"})
	events := drainEvents(t, agent.Events(), 30*time.Second)

	var retryEvents []AgentEvent
	var finalText string
	var hasUsage, hasDone bool
	var usageTokensIn int
	for _, ev := range events {
		switch ev.Type {
		case EventRetry:
			retryEvents = append(retryEvents, ev)
		case EventTextDelta:
			finalText += ev.Text
		case EventUsage:
			hasUsage = true
			if ev.Usage != nil {
				usageTokensIn = ev.Usage.InputTokens
			}
		case EventDone:
			hasDone = true
		}
	}

	// Should have 2 retry events (attempt 1 and 2).
	if len(retryEvents) != 2 {
		t.Errorf("expected 2 EventRetry events, got %d", len(retryEvents))
	}
	for i, rev := range retryEvents {
		if rev.Attempt != i+1 {
			t.Errorf("retry[%d].Attempt = %d, want %d", i, rev.Attempt, i+1)
		}
		if rev.Error == nil || !strings.Contains(rev.Error.Error(), "503") {
			t.Errorf("retry[%d] should carry the 503 error", i)
		}
	}

	// Final text should contain the success response.
	if !strings.Contains(finalText, "Success after retries!") {
		t.Errorf("final text = %q, want success message", finalText)
	}

	// Usage should reflect only the successful call.
	if !hasUsage {
		t.Error("expected EventUsage from the successful call")
	}
	if usageTokensIn != 200 {
		t.Errorf("usage input tokens = %d, want 200 (from successful call only)", usageTokensIn)
	}

	if !hasDone {
		t.Error("expected EventDone after successful retry")
	}
}

// TestE2EMidStreamFailureRetryChain verifies mid-stream failure recovery:
// provider streams partial content → error → agent emits EventStreamClear →
// EventRetry → retry streams full response → EventUsage → EventDone.
// No duplicate content should appear in the final text.
func TestE2EMidStreamFailureRetryChain(t *testing.T) {
	prov := &streamFailThenSucceedProvider{
		model:           "test-model",
		failStreamCalls: map[int]bool{0: true}, // first call fails mid-stream
		responses: []scriptedResponse{
			{text: "The quick brown fox", tokensIn: 100, tokensOut: 50},       // partial (fails)
			{text: "The quick brown fox jumps!", tokensIn: 150, tokensOut: 60}, // retry succeeds
		},
	}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "hi"})
	events := drainEvents(t, agent.Events(), 10*time.Second)

	// Verify the event sequence ordering: partial text → error → clear → retry → full text → usage → done.
	var (
		clearIdx  = -1
		retryIdx  = -1
		hasDone   bool
		hasUsage  bool
		usageIn   int
	)

	// Collect text before and after StreamClear to verify no duplication.
	var textBeforeClear, textAfterClear string
	clearSeen := false

	for i, ev := range events {
		switch ev.Type {
		case EventTextDelta:
			if clearSeen {
				textAfterClear += ev.Text
			} else {
				textBeforeClear += ev.Text
			}
		case EventStreamClear:
			clearIdx = i
			clearSeen = true
		case EventRetry:
			retryIdx = i
		case EventUsage:
			hasUsage = true
			if ev.Usage != nil {
				usageIn = ev.Usage.InputTokens
			}
		case EventDone:
			hasDone = true
		}
	}

	if clearIdx == -1 {
		t.Fatal("expected EventStreamClear to discard partial text")
	}
	if retryIdx == -1 {
		t.Fatal("expected EventRetry for stream retry")
	}
	if clearIdx >= retryIdx {
		t.Errorf("EventStreamClear (idx=%d) must come before EventRetry (idx=%d)", clearIdx, retryIdx)
	}

	// Text before clear is partial (from the failed stream).
	if textBeforeClear == "" {
		t.Error("expected some partial text before StreamClear")
	}

	// Text after clear should be the full retry response (no duplication).
	if !strings.Contains(textAfterClear, "The quick brown fox jumps!") {
		t.Errorf("text after clear = %q, want full retry response", textAfterClear)
	}
	// Verify no duplication: text after clear should NOT contain the partial prefix twice.
	if strings.Count(textAfterClear, "The quick") > 1 {
		t.Errorf("duplicate content detected in text after clear: %q", textAfterClear)
	}

	if !hasUsage {
		t.Error("expected EventUsage from the successful retry")
	}
	// Usage should reflect the retry response, not the failed stream.
	if usageIn != 150 {
		t.Errorf("usage input tokens = %d, want 150 (from retry)", usageIn)
	}

	if !hasDone {
		t.Error("expected EventDone after stream retry success")
	}
}

// TestE2ESubAgentErrorChain verifies that when a main agent spawns a sub-agent
// via tool call and the sub-agent's provider returns an error, the error flows
// through: sub-agent provider → sub-agent EventError → sub-agent result with
// context (agent_id, turn, error) → parent agent's LLM sees error → parent
// responds appropriately.
func TestE2ESubAgentErrorChain(t *testing.T) {
	// The parent agent's provider will:
	//   Call 0: return a tool_use for "agent" tool
	//   Call 1: receive the sub-agent error result, respond with text
	parentProv := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			{
				text: "I'll delegate this to a sub-agent.",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "sa1", Name: "agent", Input: json.RawMessage(`{"task":"check auth","mode":"explore"}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			{text: "The sub-agent encountered an authentication error. The API key may be invalid.", tokensIn: 200, tokensOut: 60},
		},
	}
	store := newMockStorage()
	parentClient := langdag.NewWithDeps(store, parentProv)

	// The sub-agent's provider returns a permanent 401 error.
	subProv := &failThenSucceedProvider{
		model: "test-model",
		failOnCalls: map[int]error{
			0: fmt.Errorf("HTTP 401: Unauthorized — invalid API key"),
		},
	}
	subStore := newMockStorage()
	subClient := langdag.NewWithDeps(subStore, subProv)

	tmpDir := t.TempDir()
	subAgentTool := NewSubAgentTool(SubAgentConfig{Client: subClient, MainModel: "test-model", ExploreMaxTurns: 10, GeneralMaxTurns: 10, MaxDepth: 3, WorkDir: tmpDir, ContainerImage: "alpine:latest"})

	agent := NewAgent(NewAgentOptions{Client: parentClient, Tools: []Tool{subAgentTool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})
	// Wire sub-agent events to the parent's event channel (as agentui.go does).
	subAgentTool.parentEvents = agent.events

	go agent.Run(context.Background(), RunOptions{UserMessage: "check if auth is working"})
	events := drainEvents(t, agent.Events(), 15*time.Second)

	// Collect the tool result that the parent agent received.
	var toolResult string
	var toolResultIsError bool
	var finalText string
	var hasDone bool
	var subAgentStartSeen bool
	for _, ev := range events {
		switch ev.Type {
		case EventSubAgentStart:
			subAgentStartSeen = true
		case EventToolResult:
			if ev.ToolName == "agent" {
				toolResult = ev.ToolResult
				toolResultIsError = ev.IsError
			}
		case EventTextDelta:
			finalText += ev.Text
		case EventDone:
			hasDone = true
		}
	}

	if !subAgentStartSeen {
		t.Error("expected EventSubAgentStart — sub-agent should have been spawned")
	}

	// The sub-agent tool result should contain the error with context.
	if toolResult == "" {
		t.Fatal("expected a tool result from the sub-agent")
	}
	if !strings.Contains(toolResult, "401") {
		t.Errorf("sub-agent result should contain '401', got: %q", toolResult)
	}
	if !strings.Contains(toolResult, "Unauthorized") {
		t.Errorf("sub-agent result should contain 'Unauthorized', got: %q", toolResult)
	}
	// Error context: should mention turn number.
	if !strings.Contains(toolResult, "turn") {
		t.Errorf("sub-agent result should include turn context, got: %q", toolResult)
	}
	// Sub-agent tool errors come back as non-error tool results (the sub-agent
	// itself completed, but its content reports the error).
	if toolResultIsError {
		t.Error("sub-agent tool result should not be IsError — the Execute() call itself succeeded")
	}

	// The parent agent should have processed the error and responded.
	if !strings.Contains(finalText, "authentication error") || !strings.Contains(finalText, "invalid") {
		t.Errorf("parent agent's response should address the auth error, got: %q", finalText)
	}
	if !hasDone {
		t.Error("expected EventDone — parent agent should complete")
	}
}

// TestE2ECascadingFailureRecovery verifies that when a sub-agent encounters
// a max_tokens truncation with no usable content, the parent agent receives the
// error, and can take corrective action (e.g., retry with a different approach)
// that succeeds without conversation corruption.
func TestE2ECascadingFailureRecovery(t *testing.T) {
	// Sub-agent provider: first call returns max_tokens with empty content (error),
	// simulated by returning an error that the agent will surface.
	subProv := &failThenSucceedProvider{
		model: "test-model",
		failOnCalls: map[int]error{
			// The sub-agent's prompt call fails with a transient-ish error
			// that exhausts retries, producing a clear error result.
			0: fmt.Errorf("HTTP 529: overloaded"),
			1: fmt.Errorf("HTTP 529: overloaded"),
			2: fmt.Errorf("HTTP 529: overloaded"),
		},
	}
	subStore := newMockStorage()
	subClient := langdag.NewWithDeps(subStore, subProv)

	// Parent provider:
	//   Call 0: ask sub-agent to do work (first attempt)
	//   Call 1: see the error, try a different sub-agent approach
	//   Call 2: see second sub-agent success, respond
	parentProv := &scriptedProvider{
		model: "test-model",
		responses: []scriptedResponse{
			// Call 0: delegate to sub-agent
			{
				text: "Delegating to sub-agent.",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "c1", Name: "agent", Input: json.RawMessage(`{"task":"analyze code","mode":"explore"}`)},
				},
				tokensIn: 100, tokensOut: 50,
			},
			// Call 1: sees error, tries different approach
			{
				text: "First attempt failed. Trying with a simpler task.",
				toolCalls: []types.ContentBlock{
					{Type: "tool_use", ID: "c2", Name: "simple_tool", Input: json.RawMessage(`{}`)},
				},
				tokensIn: 200, tokensOut: 60,
			},
			// Call 2: success response
			{text: "Done! Used the fallback approach successfully.", tokensIn: 150, tokensOut: 40},
		},
	}
	parentStore := newMockStorage()
	parentClient := langdag.NewWithDeps(parentStore, parentProv)

	tmpDir := t.TempDir()
	subAgentTool := NewSubAgentTool(SubAgentConfig{Client: subClient, MainModel: "test-model", ExploreMaxTurns: 10, GeneralMaxTurns: 10, MaxDepth: 3, WorkDir: tmpDir, ContainerImage: "alpine:latest"})
	simpleTool := &testTool{name: "simple_tool", result: "fallback result ok"}

	agent := NewAgent(NewAgentOptions{Client: parentClient, Tools: []Tool{subAgentTool, simpleTool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0})

	go agent.Run(context.Background(), RunOptions{UserMessage: "analyze the code"})
	events := drainEvents(t, agent.Events(), 30*time.Second)

	// Track the full sequence.
	var (
		subAgentResults []string
		simpleToolSeen  bool
		finalText       string
		hasDone         bool
		doneNodeID      string
	)
	for _, ev := range events {
		switch ev.Type {
		case EventToolResult:
			if ev.ToolName == "agent" {
				subAgentResults = append(subAgentResults, ev.ToolResult)
			}
			if ev.ToolName == "simple_tool" {
				simpleToolSeen = true
			}
		case EventTextDelta:
			finalText += ev.Text
		case EventDone:
			hasDone = true
			doneNodeID = ev.NodeID
		}
	}

	// First sub-agent should have failed with the overloaded error.
	if len(subAgentResults) == 0 {
		t.Fatal("expected at least one sub-agent tool result")
	}
	firstResult := subAgentResults[0]
	if !strings.Contains(firstResult, "529") && !strings.Contains(firstResult, "overloaded") {
		t.Errorf("first sub-agent result should contain overloaded error, got: %q", firstResult)
	}

	// Parent should have recovered with simple_tool.
	if !simpleToolSeen {
		t.Error("expected simple_tool execution as fallback approach")
	}

	// Final response should indicate success.
	if !strings.Contains(finalText, "fallback approach successfully") {
		t.Errorf("final text should indicate successful recovery, got: %q", finalText)
	}

	if !hasDone {
		t.Fatal("expected EventDone — agent should complete after recovery")
	}
	if doneNodeID == "" {
		t.Error("EventDone should have a valid nodeID for conversation resume")
	}
}

// testBgWaiterTool is a Tool that also implements BackgroundWaiter for testing
// graceful exhaustion with background sub-agents.
type testBgWaiterTool struct {
	testTool
	mu       sync.Mutex
	called   bool
	pending  bool   // if true, HasPendingBackgroundAgents returns true
	injectFn func() // called during WaitForBackgroundAgents to simulate bg agent completion
}

func (t *testBgWaiterTool) WaitForBackgroundAgents(_ time.Duration) []string {
	t.mu.Lock()
	t.called = true
	t.pending = false // after waiting, agents are done
	fn := t.injectFn
	t.mu.Unlock()
	if fn != nil {
		fn()
	}
	return nil
}

func (t *testBgWaiterTool) HasPendingBackgroundAgents() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pending
}

func (t *testBgWaiterTool) DrainGoroutines(_ time.Duration) bool {
	return true
}

func (t *testBgWaiterTool) wasCalled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.called
}

func TestGracefulExhaustion(t *testing.T) {
	// With maxToolIterations=3, the loop runs 3 iterations (each: execute tool,
	// call LLM). The initial call + 3 in-loop calls = 4 LLM calls that return
	// tool calls. The 5th call is the final synthesis (text-only).
	responses := []scriptedResponse{
		// Initial call
		{toolCalls: []types.ContentBlock{{Type: "tool_use", ID: "tc0", Name: "bash", Input: json.RawMessage(`{}`)}}, tokensIn: 100, tokensOut: 50},
		// Loop iterations 0-2
		{toolCalls: []types.ContentBlock{{Type: "tool_use", ID: "tc1", Name: "bash", Input: json.RawMessage(`{}`)}}, tokensIn: 100, tokensOut: 50},
		{toolCalls: []types.ContentBlock{{Type: "tool_use", ID: "tc2", Name: "bash", Input: json.RawMessage(`{}`)}}, tokensIn: 100, tokensOut: 50},
		{toolCalls: []types.ContentBlock{{Type: "tool_use", ID: "tc3", Name: "bash", Input: json.RawMessage(`{}`)}}, tokensIn: 100, tokensOut: 50},
		// Final synthesis call — text only, no tools
		{text: "Here is a synthesis of what was accomplished.", tokensIn: 100, tokensOut: 50},
	}

	prov := &scriptedProvider{model: "test-model", responses: responses}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	bashTool := &testTool{name: "bash", result: "ok"}
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{bashTool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0}, WithMaxToolIterations(3))

	go agent.Run(context.Background(), RunOptions{UserMessage: "test graceful exhaustion"})
	events := drainEvents(t, agent.Events(), 5*time.Second)

	var (
		gotExhaustionError bool
		gotSynthesisText   bool
		doneNodeID         string
	)
	for _, ev := range events {
		if ev.Type == EventError && ev.Error != nil &&
			strings.Contains(ev.Error.Error(), "synthesizing final response") {
			gotExhaustionError = true
		}
		if ev.Type == EventTextDelta &&
			strings.Contains(ev.Text, "synthesis of what was accomplished") {
			gotSynthesisText = true
		}
		if ev.Type == EventDone {
			doneNodeID = ev.NodeID
		}
	}

	if !gotExhaustionError {
		t.Error("expected error event about synthesizing final response")
	}
	if !gotSynthesisText {
		t.Error("expected synthesis text in EventTextDelta from the final LLM call")
	}
	if doneNodeID == "" {
		t.Error("EventDone should carry the nodeID from the synthesis call")
	}
}

func TestGracefulExhaustionCallsBackgroundWaiter(t *testing.T) {
	// Same setup as TestGracefulExhaustion but with a BackgroundWaiter tool.
	// Verifies WaitForBackgroundAgents is called and bg results injected during
	// the wait are included in the final message context.
	responses := []scriptedResponse{
		{toolCalls: []types.ContentBlock{{Type: "tool_use", ID: "tc0", Name: "bash", Input: json.RawMessage(`{}`)}}, tokensIn: 100, tokensOut: 50},
		{toolCalls: []types.ContentBlock{{Type: "tool_use", ID: "tc1", Name: "bash", Input: json.RawMessage(`{}`)}}, tokensIn: 100, tokensOut: 50},
		{toolCalls: []types.ContentBlock{{Type: "tool_use", ID: "tc2", Name: "bash", Input: json.RawMessage(`{}`)}}, tokensIn: 100, tokensOut: 50},
		{toolCalls: []types.ContentBlock{{Type: "tool_use", ID: "tc3", Name: "bash", Input: json.RawMessage(`{}`)}}, tokensIn: 100, tokensOut: 50},
		{text: "Synthesis including background agent context.", tokensIn: 100, tokensOut: 50},
	}

	prov := &scriptedProvider{model: "test-model", responses: responses}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	bgWaiter := &testBgWaiterTool{
		testTool: testTool{name: "agent", result: "ok"},
	}
	bashTool := &testTool{name: "bash", result: "ok"}
	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{bashTool, bgWaiter}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0}, WithMaxToolIterations(3))

	// injectFn simulates background agents completing during WaitForBackgroundAgents,
	// which is what happens in production (onBgComplete → InjectBackgroundResult).
	bgWaiter.injectFn = func() {
		agent.InjectBackgroundResult("bg-result-from-sub-agent")
	}

	go agent.Run(context.Background(), RunOptions{UserMessage: "test with bg waiter"})
	events := drainEvents(t, agent.Events(), 5*time.Second)

	if !bgWaiter.wasCalled() {
		t.Error("WaitForBackgroundAgents should have been called during graceful exhaustion")
	}

	var gotSynthesis bool
	for _, ev := range events {
		if ev.Type == EventTextDelta &&
			strings.Contains(ev.Text, "Synthesis including background agent context") {
			gotSynthesis = true
		}
	}
	if !gotSynthesis {
		t.Error("expected synthesis text from the final LLM call")
	}
}

// TestE2EGracefulExhaustionWithBackgroundSubAgent is an end-to-end integration
// test that exercises the full graceful exhaustion path with a real SubAgentTool.
// Scenario: main agent spawns a background sub-agent, hits its iteration limit
// while the sub-agent is running, waits for it, collects its results via
// onBgComplete → InjectBackgroundResult, and produces a synthesized response
// that includes the sub-agent's output.
func TestE2EGracefulExhaustionWithBackgroundSubAgent(t *testing.T) {
	// --- Sub-agent provider: returns text that the parent should incorporate. ---
	subProv := &mockProvider{
		model:     "test-model",
		responses: []string{"The auth module uses JWT tokens stored in Redis with a 24h TTL."},
	}
	subStore := newMockStorage()
	subClient := langdag.NewWithDeps(subStore, subProv)

	// --- Parent provider sequence ---
	// Response 0 (initial): spawn a background sub-agent
	// Response 1 (iteration 0): after receiving "started in background", call bash
	// Response 2 (iteration 1): after bash completes, request another bash call
	//   → iteration reaches maxIter=2, toolCalls still pending → graceful exhaustion
	// Response 3 (synthesis): text-only response incorporating background context
	parentResponses := []scriptedResponse{
		{
			toolCalls: []types.ContentBlock{{
				Type:  "tool_use",
				ID:    "tc-agent",
				Name:  "agent",
				Input: json.RawMessage(`{"task":"research auth module","mode":"explore","background":true}`),
			}},
			tokensIn: 100, tokensOut: 50,
		},
		{
			toolCalls: []types.ContentBlock{{
				Type:  "tool_use",
				ID:    "tc-bash1",
				Name:  "bash",
				Input: json.RawMessage(`{}`),
			}},
			tokensIn: 150, tokensOut: 60,
		},
		{
			toolCalls: []types.ContentBlock{{
				Type:  "tool_use",
				ID:    "tc-bash2",
				Name:  "bash",
				Input: json.RawMessage(`{}`),
			}},
			tokensIn: 150, tokensOut: 60,
		},
		{
			text:     "Based on the background research, the auth module uses JWT tokens in Redis with a 24h TTL. Here is the synthesized plan.",
			tokensIn: 200, tokensOut: 80,
		},
	}

	parentProv := &scriptedProvider{model: "test-model", responses: parentResponses}
	parentStore := newMockStorage()
	parentClient := langdag.NewWithDeps(parentStore, parentProv)

	tmpDir := t.TempDir()
	bashTool := &testTool{name: "bash", result: "ok"}
	subAgentTool := NewSubAgentTool(SubAgentConfig{Client: subClient, MainModel: "test-model", ExploreMaxTurns: 10, GeneralMaxTurns: 10, MaxDepth: 3, WorkDir: tmpDir, ContainerImage: "alpine:latest"})

	const parentMaxIterations = 2
	agent := NewAgent(NewAgentOptions{Client: parentClient, Tools: []Tool{bashTool, subAgentTool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0}, WithMaxToolIterations(parentMaxIterations))

	// Wire the background completion callback as production does (agentui.go:261).
	subAgentTool.parentEvents = agent.events
	subAgentTool.onBgComplete = agent.InjectBackgroundResult

	go agent.Run(context.Background(), RunOptions{UserMessage: "plan auth module changes"})
	events := drainEvents(t, agent.Events(), 15*time.Second)

	var (
		gotExhaustionError bool
		gotSynthesisText   bool
		subAgentStartSeen  bool
		doneNodeID         string
		allText            strings.Builder
	)
	for _, ev := range events {
		switch ev.Type {
		case EventError:
			if ev.Error != nil && strings.Contains(ev.Error.Error(), "synthesizing final response") {
				gotExhaustionError = true
			}
		case EventTextDelta:
			allText.WriteString(ev.Text)
			if strings.Contains(ev.Text, "synthesized plan") {
				gotSynthesisText = true
			}
		case EventSubAgentStart:
			subAgentStartSeen = true
		case EventDone:
			doneNodeID = ev.NodeID
		}
	}

	if !subAgentStartSeen {
		t.Error("expected EventSubAgentStart — background sub-agent should have been spawned")
	}
	if !gotExhaustionError {
		t.Error("expected error event about synthesizing final response (graceful exhaustion)")
	}
	if !gotSynthesisText {
		t.Errorf("expected synthesis text containing sub-agent findings, got: %q", allText.String())
	}
	if doneNodeID == "" {
		t.Error("EventDone should carry the nodeID from the synthesis call")
	}

	// Verify the sub-agent output file was written in the temp directory.
	agentOutputPath := filepath.Join(tmpDir, ".herm", "agents")
	entries, err := os.ReadDir(agentOutputPath)
	if err != nil {
		t.Fatalf("failed to read agent output dir: %v", err)
	}
	if len(entries) == 0 {
		t.Error("expected at least one sub-agent output file in .herm/agents/")
	}
}

// --- Phase 1c: background completion tests ---

func TestBackgroundCompletionWaitsForPendingAgents(t *testing.T) {
	// Scenario: LLM returns end_turn (no tool calls) on the initial call,
	// but a background agent is pending. The system should wait, inject
	// results, and re-call the LLM which then produces final text.
	responses := []scriptedResponse{
		// Initial call: end_turn (no tool calls)
		{text: "Let me wait for results.", tokensIn: 100, tokensOut: 50},
		// Background completion re-call: model incorporates bg results
		{text: "Based on background findings, here is the answer.", tokensIn: 150, tokensOut: 60},
	}

	prov := &scriptedProvider{model: "test-model", responses: responses}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	bgWaiter := &testBgWaiterTool{
		testTool: testTool{name: "agent", result: "ok"},
		pending:  true,
	}
	bgWaiter.injectFn = func() {
		// Simulate the onBgComplete callback that happens when agents finish.
		// We need the agent reference, so we capture it below.
	}

	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{bgWaiter}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0}, WithMaxToolIterations(10))

	// Wire up the inject function now that agent exists.
	bgWaiter.injectFn = func() {
		agent.InjectBackgroundResult("bg-agent-result: found the answer")
	}

	go agent.Run(context.Background(), RunOptions{UserMessage: "test background completion"})
	events := drainEvents(t, agent.Events(), 5*time.Second)

	if !bgWaiter.wasCalled() {
		t.Error("WaitForBackgroundAgents should have been called")
	}

	var gotBgText bool
	for _, ev := range events {
		if ev.Type == EventTextDelta && strings.Contains(ev.Text, "background findings") {
			gotBgText = true
		}
	}
	if !gotBgText {
		t.Error("expected LLM to be re-called with background results and produce text about them")
	}
}

func TestBackgroundCompletionSkipsWhenNoPending(t *testing.T) {
	// Scenario: LLM returns end_turn, no background agents pending.
	// Should complete normally with just one LLM call.
	responses := []scriptedResponse{
		{text: "Done, no background work.", tokensIn: 100, tokensOut: 50},
	}

	prov := &scriptedProvider{model: "test-model", responses: responses}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	bgWaiter := &testBgWaiterTool{
		testTool: testTool{name: "agent", result: "ok"},
		pending:  false,
	}

	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{bgWaiter}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0}, WithMaxToolIterations(10))

	go agent.Run(context.Background(), RunOptions{UserMessage: "test no pending"})
	events := drainEvents(t, agent.Events(), 5*time.Second)

	if bgWaiter.wasCalled() {
		t.Error("WaitForBackgroundAgents should NOT have been called when no agents are pending")
	}

	// Should have exactly: initial text + done event (plus usage/llmstart)
	var textCount int
	for _, ev := range events {
		if ev.Type == EventTextDelta {
			textCount++
		}
	}
	if textCount == 0 {
		t.Error("expected at least one text delta from initial LLM call")
	}
}

func TestBackgroundCompletionCycleCap(t *testing.T) {
	// Scenario: background agents keep being pending after each cycle.
	// The cap (maxBackgroundCompletionCycles=3) should prevent infinite loops.
	//
	// We need: initial call (end_turn) + 3 cycles of bg-wait + re-call.
	// The bgWaiter always reports pending=true and injects results,
	// and the LLM always returns end_turn (no tools).
	responses := []scriptedResponse{
		{text: "Waiting for background.", tokensIn: 100, tokensOut: 50},
		{text: "Cycle 1 response.", tokensIn: 100, tokensOut: 50},
		{text: "Cycle 2 response.", tokensIn: 100, tokensOut: 50},
		{text: "Cycle 3 response.", tokensIn: 100, tokensOut: 50},
		// No more responses needed — should stop at 3 cycles.
	}

	prov := &scriptedProvider{model: "test-model", responses: responses}
	store := newMockStorage()
	client := langdag.NewWithDeps(store, prov)

	var waitCount int32
	bgWaiter := &testBgWaiterTool{
		testTool: testTool{name: "agent", result: "ok"},
		pending:  true,
	}

	agent := NewAgent(NewAgentOptions{Client: client, Tools: []Tool{bgWaiter}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0}, WithMaxToolIterations(10))

	bgWaiter.injectFn = func() {
		atomic.AddInt32(&waitCount, 1)
		agent.InjectBackgroundResult(fmt.Sprintf("bg-result-%d", atomic.LoadInt32(&waitCount)))
		// Keep reporting pending so the loop tries again.
		bgWaiter.mu.Lock()
		bgWaiter.pending = true
		bgWaiter.mu.Unlock()
	}

	go agent.Run(context.Background(), RunOptions{UserMessage: "test cycle cap"})
	events := drainEvents(t, agent.Events(), 5*time.Second)

	// Should have been called exactly maxBackgroundCompletionCycles times.
	cycles := atomic.LoadInt32(&waitCount)
	if cycles != int32(maxBackgroundCompletionCycles) {
		t.Errorf("expected %d wait cycles, got %d", maxBackgroundCompletionCycles, cycles)
	}

	// Verify we got an EventDone (didn't hang).
	var gotDone bool
	for _, ev := range events {
		if ev.Type == EventDone {
			gotDone = true
		}
	}
	if !gotDone {
		t.Error("expected EventDone — run loop should not hang when cycle cap is reached")
	}
}

// --- Phase 4: Integration test — full background lifecycle ---

// gatedProvider returns pre-defined responses, optionally blocking on per-call
// gates. A gate is a channel that must be closed before the response is sent.
// A nil gate means the response is sent immediately. Used to simulate staggered
// sub-agent completion in integration tests.
type gatedProvider struct {
	mu        sync.Mutex
	responses []string
	gates     []chan struct{}
	callIdx   int
	model     string
}

func (p *gatedProvider) Complete(ctx context.Context, _ *types.CompletionRequest) (*types.CompletionResponse, error) {
	p.mu.Lock()
	idx := p.callIdx
	p.callIdx++
	p.mu.Unlock()

	if idx < len(p.gates) && p.gates[idx] != nil {
		select {
		case <-p.gates[idx]:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	text := "ok"
	if idx < len(p.responses) {
		text = p.responses[idx]
	}
	return &types.CompletionResponse{
		ID: fmt.Sprintf("gated-resp-%d", idx), Model: p.model,
		Content:    []types.ContentBlock{{Type: "text", Text: text}},
		StopReason: "end_turn",
		Usage:      types.Usage{InputTokens: 50, OutputTokens: 20},
	}, nil
}

func (p *gatedProvider) Stream(ctx context.Context, _ *types.CompletionRequest) (<-chan types.StreamEvent, error) {
	p.mu.Lock()
	idx := p.callIdx
	p.callIdx++
	p.mu.Unlock()

	ch := make(chan types.StreamEvent, 10)
	go func() {
		defer close(ch)
		if idx < len(p.gates) && p.gates[idx] != nil {
			select {
			case <-p.gates[idx]:
			case <-ctx.Done():
				return
			}
		}
		text := "ok"
		if idx < len(p.responses) {
			text = p.responses[idx]
		}
		ch <- types.StreamEvent{Type: types.StreamEventDelta, Content: text}
		ch <- types.StreamEvent{
			Type: types.StreamEventDone,
			Response: &types.CompletionResponse{
				ID: fmt.Sprintf("gated-resp-%d", idx), Model: p.model,
				Content:    []types.ContentBlock{{Type: "text", Text: text}},
				StopReason: "end_turn",
				Usage:      types.Usage{InputTokens: 50, OutputTokens: 20},
			},
		}
	}()
	return ch, nil
}

func (p *gatedProvider) Name() string             { return "gated" }
func (p *gatedProvider) Models() []types.ModelInfo { return nil }

// TestE2EBackgroundLifecycleThreeAgents exercises the complete scenario from
// the bug report: main agent spawns 3 background sub-agents, one completes
// before the main agent stops, main agent returns end_turn, system waits for
// the remaining agents via backgroundCompletion, re-calls LLM with all
// results, and produces a final response.
func TestE2EBackgroundLifecycleThreeAgents(t *testing.T) {
	// --- Sub-agent provider: 3 calls, first immediate, others gated. ---
	gate2 := make(chan struct{})
	gate3 := make(chan struct{})
	subProv := &gatedProvider{
		model: "test-model",
		responses: []string{
			"Auth uses OAuth2 with PKCE flow.",
			"Database uses connection pooling with max 50 connections.",
			"Caching layer uses Redis with 5-minute TTL.",
		},
		gates: []chan struct{}{nil, gate2, gate3},
	}
	subStore := newMockStorage()
	subClient := langdag.NewWithDeps(subStore, subProv)

	// --- Parent provider sequence ---
	// Call 0: spawn 3 background sub-agents.
	// Call 1: end_turn text — triggers backgroundCompletion because agents 2/3
	//         are still gated. Agent 1's result may already be injected inline.
	// Call 2: synthesis incorporating remaining background results.
	parentResponses := []scriptedResponse{
		{
			toolCalls: []types.ContentBlock{
				{Type: "tool_use", ID: "tc-bg1", Name: "agent",
					Input: json.RawMessage(`{"task":"research auth","mode":"explore","background":true}`)},
				{Type: "tool_use", ID: "tc-bg2", Name: "agent",
					Input: json.RawMessage(`{"task":"research database","mode":"explore","background":true}`)},
				{Type: "tool_use", ID: "tc-bg3", Name: "agent",
					Input: json.RawMessage(`{"task":"research caching","mode":"explore","background":true}`)},
			},
			tokensIn: 100, tokensOut: 50,
		},
		{text: "Still running. Let me wait a bit more.", tokensIn: 150, tokensOut: 60},
		{text: "All three background agents completed. Consolidated analysis covering auth, database, and caching.", tokensIn: 200, tokensOut: 80},
	}

	parentProv := &scriptedProvider{model: "test-model", responses: parentResponses}
	parentStore := newMockStorage()
	parentClient := langdag.NewWithDeps(parentStore, parentProv)

	tmpDir := t.TempDir()
	subAgentTool := NewSubAgentTool(SubAgentConfig{Client: subClient, MainModel: "test-model", ExploreMaxTurns: 10, GeneralMaxTurns: 10, MaxDepth: 3, WorkDir: tmpDir, ContainerImage: "alpine:latest"})

	agent := NewAgent(NewAgentOptions{Client: parentClient, Tools: []Tool{subAgentTool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0}, WithMaxToolIterations(10))

	// Wire callbacks as production does (agentui.go:260-261).
	subAgentTool.parentEvents = agent.events
	subAgentTool.onBgComplete = agent.InjectBackgroundResult

	// Release gated sub-agents after a short delay so the main agent reaches
	// end_turn and enters backgroundCompletion before they complete.
	go func() {
		time.Sleep(300 * time.Millisecond)
		close(gate2)
		close(gate3)
	}()

	go agent.Run(context.Background(), RunOptions{UserMessage: "analyze architecture: auth, database, caching"})
	events := drainEvents(t, agent.Events(), 15*time.Second)

	// --- Verify event stream ---
	var (
		subAgentStarts int
		subAgentDones  int
		gotSynthesis   bool
		gotDone        bool
		allText        strings.Builder
		agentIDs       = make(map[string]bool)
	)
	for _, ev := range events {
		switch ev.Type {
		case EventSubAgentStart:
			subAgentStarts++
			agentIDs[ev.AgentID] = true
		case EventSubAgentStatus:
			if ev.Text == "done" {
				subAgentDones++
			}
		case EventTextDelta:
			allText.WriteString(ev.Text)
			if strings.Contains(ev.Text, "Consolidated analysis") {
				gotSynthesis = true
			}
		case EventDone:
			gotDone = true
		}
	}

	if subAgentStarts != 3 {
		t.Errorf("expected 3 EventSubAgentStart, got %d", subAgentStarts)
	}
	if len(agentIDs) != 3 {
		t.Errorf("expected 3 distinct agent IDs, got %d", len(agentIDs))
	}
	if subAgentDones < 3 {
		t.Errorf("expected at least 3 sub-agent done events, got %d", subAgentDones)
	}
	if !gotSynthesis {
		t.Errorf("expected synthesis text with 'Consolidated analysis', got: %q", allText.String())
	}
	if !gotDone {
		t.Error("expected EventDone")
	}

	// --- Verify display by replaying events through App ---
	app := &App{headless: true, width: 80}
	for _, ev := range events {
		app.handleAgentEvent(ev)
	}

	if len(app.subAgents) != 3 {
		t.Errorf("expected 3 sub-agents in display, got %d", len(app.subAgents))
	}
	for id, sa := range app.subAgents {
		if !sa.done {
			t.Errorf("sub-agent %s should be marked done in display", id)
		}
	}

	// Completed agents should still be visible (Phase 2 fix).
	lines := app.subAgentDisplayLines()
	if len(lines) == 0 {
		t.Error("subAgentDisplayLines should show completed agents, not return nil")
	}

	// Verify agent output files.
	agentOutputPath := filepath.Join(tmpDir, ".herm", "agents")
	entries, err := os.ReadDir(agentOutputPath)
	if err != nil {
		t.Fatalf("failed to read agent output dir: %v", err)
	}
	if len(entries) < 3 {
		t.Errorf("expected 3 sub-agent output files, got %d", len(entries))
	}
}

// TestE2EChannelSaturationThreeAgents reproduces the exact failure from the
// trace: 3 concurrent background sub-agents with a reduced-size events channel
// (64 slots) to force saturation. Verifies all critical events are delivered.
func TestE2EChannelSaturationThreeAgents(t *testing.T) {
	// Sub-agent provider: each call produces ~50 text delta chunks to generate
	// heavy channel pressure.
	subProv := &chunkyProvider{chunks: 50, model: "test-model"}
	subStore := newMockStorage()
	subClient := langdag.NewWithDeps(subStore, subProv)

	// Parent provider: spawns 3 background sub-agents, then produces final text.
	parentResponses := []scriptedResponse{
		{
			toolCalls: []types.ContentBlock{
				{Type: "tool_use", ID: "tc-s1", Name: "agent",
					Input: json.RawMessage(`{"task":"research auth","mode":"explore","background":true}`)},
				{Type: "tool_use", ID: "tc-s2", Name: "agent",
					Input: json.RawMessage(`{"task":"research db","mode":"explore","background":true}`)},
				{Type: "tool_use", ID: "tc-s3", Name: "agent",
					Input: json.RawMessage(`{"task":"research cache","mode":"explore","background":true}`)},
			},
			tokensIn: 100, tokensOut: 50,
		},
		{text: "Waiting for agents.", tokensIn: 150, tokensOut: 60},
		{text: "All done.", tokensIn: 200, tokensOut: 80},
	}

	parentProv := &scriptedProvider{model: "test-model", responses: parentResponses}
	parentStore := newMockStorage()
	parentClient := langdag.NewWithDeps(parentStore, parentProv)

	tmpDir := t.TempDir()
	subAgentTool := NewSubAgentTool(SubAgentConfig{Client: subClient, MainModel: "test-model", ExploreMaxTurns: 10, GeneralMaxTurns: 10, MaxDepth: 3, WorkDir: tmpDir, ContainerImage: "alpine:latest"})

	agent := NewAgent(NewAgentOptions{Client: parentClient, Tools: []Tool{subAgentTool}, ServerTools: nil, SystemPrompt: "", Model: "test-model", ContextWindow: 0}, WithMaxToolIterations(10))
	// Replace events channel with a small buffer to force saturation.
	agent.events = make(chan AgentEvent, 64)
	subAgentTool.parentEvents = agent.events
	subAgentTool.onBgComplete = agent.InjectBackgroundResult

	go agent.Run(context.Background(), RunOptions{UserMessage: "analyze architecture"})

	// Drain events with a generous timeout. Use doneCh as a backup signal
	// in case EventDone is dropped (the exact scenario we're testing).
	var events []AgentEvent
	deadline := time.After(30 * time.Second)
	eventsDone := false
	for !eventsDone {
		select {
		case ev, ok := <-agent.Events():
			if !ok {
				eventsDone = true
				break
			}
			events = append(events, ev)
		case <-agent.DoneCh():
			// Agent done — drain remaining.
			for {
				select {
				case ev, ok := <-agent.Events():
					if !ok {
						eventsDone = true
						break
					}
					events = append(events, ev)
				default:
					eventsDone = true
				}
				if eventsDone {
					break
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for agent completion under channel saturation")
		}
	}

	// Replay events through App to verify display state.
	app := &App{headless: true, width: 80, resultCh: make(chan any, 10)}
	app.agentRunning = true
	app.agentStartTime = time.Now()
	app.agent = agent
	app.agentTicker = time.NewTicker(50 * time.Millisecond)

	for _, ev := range events {
		app.handleAgentEvent(ev)
	}
	// If EventDone was dropped, simulate the doneCh backup recovery.
	if app.agentRunning {
		app.finalizeAgentTurn("")
	}

	// Verify all 3 sub-agents reached done in the display.
	subDoneCount := 0
	for _, sa := range app.subAgents {
		if sa.done {
			subDoneCount++
		}
	}
	if subDoneCount < 3 {
		t.Errorf("expected all 3 sub-agents done in display, got %d done out of %d total",
			subDoneCount, len(app.subAgents))
	}

	// Verify the main agent is no longer running.
	if app.agentRunning {
		t.Error("agentRunning should be false after completion")
	}

	// Verify the ticker is stopped (no active sub-agents).
	if app.agentTicker != nil && !app.hasActiveSubAgents() {
		app.agentTicker.Stop()
		t.Error("agentTicker should have been stopped by finalizeAgentTurn")
	}

	// Verify hasActiveSubAgents is false.
	if app.hasActiveSubAgents() {
		t.Error("hasActiveSubAgents should return false after all sub-agents complete")
	}

	t.Logf("processed %d events with 64-slot buffer; %d/%d sub-agents done",
		len(events), subDoneCount, len(app.subAgents))
}

// --- Turn budget tests ---

func TestTurnBudgetNotShownWhenMaxTurnsZero(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0})
	// Main agent: maxTurns == 0, no turn budget should appear.
	got := agent.budgetReminderBlock().Text
	if strings.Contains(got, "Budget:") {
		t.Errorf("main agent (maxTurns=0) should not show budget, got: %q", got)
	}
}

func TestTurnBudgetEarlyTier(t *testing.T) {
	maxT := defaultGeneralMaxTurns
	// Pick a turn safely in the early range (below mid threshold).
	turn := int(float64(maxT)*turnBudgetMidThreshold) - 1
	if turn < 1 {
		turn = 1
	}

	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0}, WithMaxTurns(maxT))
	agent.SetTurnProgress(SetTurnProgressOptions{Used: turn, Max: maxT})
	agent.SetTokenProgress(SetTokenProgressOptions{InputTokens: 6000, OutputTokens: 2200})

	got := agent.budgetReminderBlock().Text
	expected := fmt.Sprintf("Budget: Turn %d/%d | 8200 tokens used", turn, maxT)
	if !strings.Contains(got, expected) {
		t.Errorf("early tier should show basic budget, got: %q", got)
	}
	if strings.Contains(got, "halfway") || strings.Contains(got, "Wrap up") || strings.Contains(got, "FINAL") {
		t.Errorf("early tier should not show pacing warnings, got: %q", got)
	}
}

func TestTurnBudgetMidTier(t *testing.T) {
	maxT := defaultGeneralMaxTurns
	// Pick a turn in the mid range (>= midThreshold, < lateThreshold).
	turn := int(float64(maxT)*turnBudgetMidThreshold) + 1

	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0}, WithMaxTurns(maxT))
	agent.SetTurnProgress(SetTurnProgressOptions{Used: turn, Max: maxT})
	agent.SetTokenProgress(SetTokenProgressOptions{InputTokens: 25000, OutputTokens: 9100})

	got := agent.budgetReminderBlock().Text
	expected := fmt.Sprintf("Budget: Turn %d/%d", turn, maxT)
	if !strings.Contains(got, expected) {
		t.Errorf("mid tier should show turn progress, got: %q", got)
	}
	if !strings.Contains(got, "past halfway") {
		t.Errorf("mid tier should include 'past halfway', got: %q", got)
	}
}

func TestTurnBudgetLateTier(t *testing.T) {
	maxT := defaultGeneralMaxTurns
	// Pick a turn in the late range (>= lateThreshold, < finalThreshold).
	turn := int(float64(maxT)*turnBudgetLateThreshold) + 1

	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0}, WithMaxTurns(maxT))
	agent.SetTurnProgress(SetTurnProgressOptions{Used: turn, Max: maxT})
	agent.SetTokenProgress(SetTokenProgressOptions{InputTokens: 40000, OutputTokens: 12300})

	got := agent.budgetReminderBlock().Text
	expected := fmt.Sprintf("Budget: Turn %d/%d", turn, maxT)
	if !strings.Contains(got, expected) {
		t.Errorf("late tier should show turn progress, got: %q", got)
	}
	if !strings.Contains(got, "wrap up NOW") {
		t.Errorf("late tier should include wrap-up warning, got: %q", got)
	}
}

func TestTurnBudgetFinalTier(t *testing.T) {
	maxT := defaultGeneralMaxTurns
	// Pick a turn in the final range (>= finalThreshold).
	turn := int(float64(maxT)*turnBudgetFinalThreshold) + 1
	if turn > maxT {
		turn = maxT
	}

	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0}, WithMaxTurns(maxT))
	agent.SetTurnProgress(SetTurnProgressOptions{Used: turn, Max: maxT})
	agent.SetTokenProgress(SetTokenProgressOptions{InputTokens: 50000, OutputTokens: 11800})

	got := agent.budgetReminderBlock().Text
	expected := fmt.Sprintf("Budget: Turn %d/%d", turn, maxT)
	if !strings.Contains(got, expected) {
		t.Errorf("final tier should show turn progress, got: %q", got)
	}
	if !strings.Contains(got, "FINAL") {
		t.Errorf("final tier should include FINAL message, got: %q", got)
	}
	if !strings.Contains(got, "no tools") {
		t.Errorf("final tier should instruct no tools, got: %q", got)
	}
}

func TestSubAgentBudgetReminderOnlyTurnBudget(t *testing.T) {
	// Sub-agents (maxTurns > 0, contextWindow == 0) should only get the turn
	// budget line, not session stats, context window, or iteration warnings.
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0}, WithMaxTurns(20))
	agent.SetTurnProgress(SetTurnProgressOptions{Used: 5, Max: 20})
	agent.SetTokenProgress(SetTokenProgressOptions{InputTokens: 3000, OutputTokens: 1000})
	// Set main-agent fields that should NOT appear for sub-agents.
	agent.sessionInputTokens = 50000
	agent.sessionOutputTokens = 10000
	agent.sessionAgentCalls = 5
	agent.currentIteration = 18 // would trigger iteration warning for main agent

	got := agent.budgetReminderBlock().Text
	if !strings.Contains(got, "Budget: Turn 5/20") {
		t.Errorf("sub-agent should show turn budget, got: %q", got)
	}
	if strings.Contains(got, "Session:") {
		t.Errorf("sub-agent should not show session stats, got: %q", got)
	}
	if strings.Contains(got, "Context:") {
		t.Errorf("sub-agent should not show context window, got: %q", got)
	}
	if strings.Contains(got, "tool iterations") {
		t.Errorf("sub-agent should not show iteration warnings, got: %q", got)
	}
}

func TestSubAgentBudgetReminderCompactLate(t *testing.T) {
	// Verify the compact late-tier message format for sub-agents.
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0}, WithMaxTurns(20))
	agent.SetTurnProgress(SetTurnProgressOptions{Used: 16, Max: 20})

	got := agent.turnBudgetLine()
	if got != "Budget: Turn 16/20 — 4 left, wrap up NOW." {
		t.Errorf("compact late tier mismatch, got: %q", got)
	}
}

func TestSubAgentBudgetReminderCompactFinal(t *testing.T) {
	// Verify the compact final-tier message format for sub-agents.
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0}, WithMaxTurns(20))
	agent.SetTurnProgress(SetTurnProgressOptions{Used: 19, Max: 20})

	got := agent.turnBudgetLine()
	if got != "Budget: Turn 19/20 — FINAL, produce summary, no tools." {
		t.Errorf("compact final tier mismatch, got: %q", got)
	}
}

func TestSetTurnProgressThreadSafe(t *testing.T) {
	maxT := defaultGeneralMaxTurns
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0}, WithMaxTurns(maxT))

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(turn int) {
			defer wg.Done()
			agent.SetTurnProgress(SetTurnProgressOptions{Used: turn, Max: maxT})
			agent.SetTokenProgress(SetTokenProgressOptions{InputTokens: turn*1000, OutputTokens: turn*200})
			_ = agent.budgetReminderBlock()
		}(i)
	}
	wg.Wait()
	// If we get here without a race detector complaint, the test passes.
}

// --- Phase 4: Main agent budget visibility ---

func TestContextWindowUtilizationShown(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 200000})
	agent.lastInputTokens = 80000

	got := agent.budgetReminderBlock().Text
	if !strings.Contains(got, "Context: ~40% full (80000/200000 tokens)") {
		t.Errorf("expected context window utilization, got: %q", got)
	}
}

func TestContextWindowNotShownWhenZero(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0})
	agent.lastInputTokens = 80000

	got := agent.budgetReminderBlock().Text
	if strings.Contains(got, "Context:") {
		t.Errorf("should not show context window when contextWindow=0, got: %q", got)
	}
}

func TestContextWindowNotShownWhenNoInputTokens(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 200000})
	// lastInputTokens defaults to 0

	got := agent.budgetReminderBlock().Text
	if strings.Contains(got, "Context:") {
		t.Errorf("should not show context window when lastInputTokens=0, got: %q", got)
	}
}

func TestSessionCostShownInStats(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0})
	agent.sessionInputTokens = 50000
	agent.sessionOutputTokens = 5000
	agent.sessionAgentCalls = 2
	agent.SetSessionCost(0.15)

	got := agent.budgetReminderBlock().Text
	if !strings.Contains(got, "~$0.15") {
		t.Errorf("expected cost in stats, got: %q", got)
	}
	if !strings.Contains(got, "55000 tokens used") {
		t.Errorf("expected tokens in stats, got: %q", got)
	}
}

func TestSessionCostNotShownWhenZero(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0})
	agent.sessionInputTokens = 50000
	agent.sessionOutputTokens = 5000

	got := agent.budgetReminderBlock().Text
	if strings.Contains(got, "$") {
		t.Errorf("should not show cost when zero, got: %q", got)
	}
}

func TestGraduatedIterationWarningLow(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0}, WithMaxToolIterations(100))
	// 20% remaining — below 25% low threshold.
	agent.currentIteration = 80

	got := agent.budgetReminderBlock().Text
	if !strings.Contains(got, "⚠️") {
		t.Errorf("expected warning emoji at low threshold, got: %q", got)
	}
	if !strings.Contains(got, "20 tool iterations remaining out of 100") {
		t.Errorf("expected low-threshold message, got: %q", got)
	}
}

func TestGraduatedIterationWarningMid(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0}, WithMaxToolIterations(100))
	// 40% remaining — below 50% mid threshold but above 25% low.
	agent.currentIteration = 60

	got := agent.budgetReminderBlock().Text
	if !strings.Contains(got, "past halfway") {
		t.Errorf("expected 'past halfway' at mid threshold, got: %q", got)
	}
	if strings.Contains(got, "⚠️") {
		t.Errorf("should not show urgent warning at mid threshold, got: %q", got)
	}
}

func TestGraduatedIterationWarningNone(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0}, WithMaxToolIterations(100))
	// 70% remaining — above all thresholds.
	agent.currentIteration = 30

	got := agent.budgetReminderBlock().Text
	if strings.Contains(got, "past halfway") || strings.Contains(got, "iterations remaining") {
		t.Errorf("should not show iteration warnings above thresholds, got: %q", got)
	}
}

func TestSetSessionCostThreadSafe(t *testing.T) {
	client := newTestClient("ok")
	agent := NewAgent(NewAgentOptions{Client: client, Tools: nil, ServerTools: nil, SystemPrompt: "base prompt", Model: "test-model", ContextWindow: 0})
	agent.sessionInputTokens = 1000

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(v int) {
			defer wg.Done()
			agent.SetSessionCost(float64(v) * 0.01)
			_ = agent.budgetReminderBlock()
		}(i)
	}
	wg.Wait()
}
