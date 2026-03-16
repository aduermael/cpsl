package main

import (
	"context"
	"encoding/json"
	"testing"

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
