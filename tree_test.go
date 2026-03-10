package main

import (
	"strings"
	"testing"

	"langdag.com/langdag/types"
)

func TestRenderTree_LinearConversation(t *testing.T) {
	a := &App{
		models: []ModelDef{
			{ID: "claude-sonnet-4-20250514", Provider: ProviderAnthropic, PromptPrice: 3, CompletionPrice: 15},
		},
	}

	nodes := []*types.Node{
		{ID: "1", NodeType: types.NodeTypeUser, Content: "Hello"},
		{ID: "2", ParentID: "1", NodeType: types.NodeTypeAssistant, Content: "Hi there!", Model: "claude-sonnet-4-20250514", TokensIn: 100, TokensOut: 50},
		{ID: "3", ParentID: "2", NodeType: types.NodeTypeUser, Content: "Help me fix a bug"},
		{ID: "4", ParentID: "3", NodeType: types.NodeTypeAssistant, Content: "Sure, let me look.", Model: "claude-sonnet-4-20250514", TokensIn: 500, TokensOut: 200},
	}

	result := a.renderTree(nodes, "1")

	// All lines should start at column 0 (flat spine, no indentation).
	for _, line := range strings.Split(strings.TrimRight(result, "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "Total:") {
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "│") || strings.HasPrefix(line, "├") || strings.HasPrefix(line, "└") {
			t.Errorf("linear conversation should be flat, got indented line: %q", line)
		}
	}
	if !strings.Contains(result, "Hello") {
		t.Errorf("expected 'Hello', got:\n%s", result)
	}
	if !strings.Contains(result, "Help me fix a bug") {
		t.Errorf("expected 'Help me fix a bug', got:\n%s", result)
	}
	if !strings.Contains(result, "Total:") {
		t.Errorf("expected total cost, got:\n%s", result)
	}
}

func TestRenderTree_WithToolCalls(t *testing.T) {
	a := &App{}

	nodes := []*types.Node{
		{ID: "1", NodeType: types.NodeTypeUser, Content: "Run ls"},
		{ID: "2", ParentID: "1", NodeType: types.NodeTypeAssistant, Content: "Let me run that."},
		{ID: "3", ParentID: "2", NodeType: types.NodeTypeToolCall, Content: `{"name":"bash","input":{"command":"ls"}}`},
		{ID: "4", ParentID: "3", NodeType: types.NodeTypeToolResult, Content: `file1.go\nfile2.go`},
		{ID: "5", ParentID: "4", NodeType: types.NodeTypeAssistant, Content: "Here are the files."},
	}

	result := a.renderTree(nodes, "1")
	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")

	// Tool call and result lines should be indented.
	foundIndentedTool := false
	for _, line := range lines {
		if strings.Contains(line, "bash") && strings.HasPrefix(line, "  ") {
			foundIndentedTool = true
		}
	}
	if !foundIndentedTool {
		t.Errorf("expected tool call line to be indented, got:\n%s", result)
	}

	// User/Assistant lines should NOT be indented (flat spine).
	for _, line := range lines {
		if strings.Contains(line, "Run ls") && strings.HasPrefix(line, " ") {
			t.Errorf("user line should not be indented: %q", line)
		}
		if strings.Contains(line, "Here are the files") && strings.HasPrefix(line, " ") {
			t.Errorf("continuation assistant line should not be indented: %q", line)
		}
	}
}

func TestRenderTree_Branching(t *testing.T) {
	a := &App{}

	// Tree with a branch: user asked two different follow-ups from same assistant response.
	nodes := []*types.Node{
		{ID: "1", NodeType: types.NodeTypeUser, Content: "Start"},
		{ID: "2", ParentID: "1", NodeType: types.NodeTypeAssistant, Content: "OK"},
		{ID: "3a", ParentID: "2", NodeType: types.NodeTypeUser, Content: "Branch A"},
		{ID: "3b", ParentID: "2", NodeType: types.NodeTypeUser, Content: "Branch B"},
	}

	result := a.renderTree(nodes, "1")

	if !strings.Contains(result, "Branch A") {
		t.Errorf("expected tree to contain 'Branch A', got:\n%s", result)
	}
	if !strings.Contains(result, "Branch B") {
		t.Errorf("expected tree to contain 'Branch B', got:\n%s", result)
	}
	// Should use tree connectors for branching.
	if !strings.Contains(result, "├─") {
		t.Errorf("expected tree to use ├─ connector for branching, got:\n%s", result)
	}
}

func TestExtractToolName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`{"name":"bash","input":{"command":"ls"}}`, "bash"},
		{`{"name":"git","input":{"args":"status"}}`, "git"},
		{`no json here`, ""},
		{`{}`, ""},
	}
	for _, tt := range tests {
		got := extractToolName(tt.input)
		if got != tt.want {
			t.Errorf("extractToolName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestShortModel(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"claude-sonnet-4-20250514", "claude-sonnet-4-20250514"},
		{"anthropic/claude-sonnet-4-20250514", "claude-sonnet-4-20250514"},
		{"openai/gpt-4o", "gpt-4o"},
	}
	for _, tt := range tests {
		got := shortModel(tt.input)
		if got != tt.want {
			t.Errorf("shortModel(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate short = %q", got)
	}
	got := truncate("this is a very long string", 10)
	// Should be 9 chars of original + "…"
	if got != "this is a…" {
		t.Errorf("truncate long = %q, want %q", got, "this is a…")
	}
}
