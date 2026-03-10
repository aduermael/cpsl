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

	result := a.renderTree(nodes)

	// All lines should start at column 0 (no indentation).
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

	result := a.renderTree(nodes)
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

	// User/Assistant lines should NOT be indented.
	for _, line := range lines {
		if strings.Contains(line, "Run ls") && strings.HasPrefix(line, " ") {
			t.Errorf("user line should not be indented: %q", line)
		}
		if strings.Contains(line, "Here are the files") && strings.HasPrefix(line, " ") {
			t.Errorf("continuation assistant line should not be indented: %q", line)
		}
	}
}

func TestRenderTree_ToolResultAsUserNode(t *testing.T) {
	a := &App{}

	// Simulates the real langdag storage where tool results are stored as
	// user nodes via PromptFrom, and assistant tool_use is stored as JSON.
	nodes := []*types.Node{
		{ID: "1", NodeType: types.NodeTypeUser, Content: "Run hello world"},
		{ID: "2", ParentID: "1", NodeType: types.NodeTypeAssistant,
			Content: `[{"type":"tool_use","id":"call_1","name":"bash","input":{"command":"go run main.go"}},{"type":"text","text":"Let me run that."}]`},
		{ID: "3", ParentID: "2", NodeType: types.NodeTypeUser,
			Content: `[{"type":"tool_result","tool_use_id":"call_1","content":"Hello, World!"}]`},
		{ID: "4", ParentID: "3", NodeType: types.NodeTypeAssistant, Content: "Done!"},
	}

	result := a.renderTree(nodes)

	// Tool result user node should NOT show as "You:".
	if strings.Contains(result, "You: [{") {
		t.Errorf("tool_result user node should not display raw JSON, got:\n%s", result)
	}

	// Should show the tool name from the assistant node.
	if !strings.Contains(result, "bash") {
		t.Errorf("expected tool name 'bash' in tree, got:\n%s", result)
	}

	// Tool result should show as "✓ bash" (tool name, not output), indented.
	foundToolResult := false
	for _, line := range strings.Split(strings.TrimRight(result, "\n"), "\n") {
		if strings.Contains(line, "✓") && strings.Contains(line, "bash") {
			foundToolResult = true
			if !strings.HasPrefix(line, "  ") {
				t.Errorf("tool result line should be indented: %q", line)
			}
		}
	}
	if !foundToolResult {
		t.Errorf("expected '✓ bash' tool result line, got:\n%s", result)
	}
	// Should NOT show tool output content in tree.
	if strings.Contains(result, "Hello, World!") {
		t.Errorf("tree should not show tool output, got:\n%s", result)
	}

	// The actual user message should still show as "You:" at column 0.
	for _, line := range strings.Split(strings.TrimRight(result, "\n"), "\n") {
		if strings.Contains(line, "You:") && strings.HasPrefix(line, " ") {
			t.Errorf("user line should not be indented: %q", line)
		}
	}

	// Assistant lines should be at column 0.
	for _, line := range strings.Split(strings.TrimRight(result, "\n"), "\n") {
		if strings.Contains(line, "Assistant") && strings.HasPrefix(line, " ") {
			t.Errorf("assistant line should not be indented: %q", line)
		}
		if strings.Contains(line, "Done!") && strings.HasPrefix(line, " ") {
			t.Errorf("assistant line should not be indented: %q", line)
		}
	}
}

func TestRenderTree_ToolResultError(t *testing.T) {
	a := &App{}

	nodes := []*types.Node{
		{ID: "1", NodeType: types.NodeTypeUser, Content: "Do something"},
		{ID: "2", ParentID: "1", NodeType: types.NodeTypeAssistant,
			Content: `[{"type":"tool_use","id":"call_1","name":"bash","input":{}}]`},
		{ID: "3", ParentID: "2", NodeType: types.NodeTypeUser,
			Content: `[{"type":"tool_result","tool_use_id":"call_1","content":"command not found","is_error":true}]`},
		{ID: "4", ParentID: "3", NodeType: types.NodeTypeAssistant, Content: "That failed."},
	}

	result := a.renderTree(nodes)

	// Should show error marker (✗) not success marker (✓).
	if !strings.Contains(result, "✗") {
		t.Errorf("expected error marker ✗ for failed tool result, got:\n%s", result)
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
