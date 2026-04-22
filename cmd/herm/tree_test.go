package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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

func TestRenderTree_ToolResultTokenEstimate(t *testing.T) {
	a := &App{}

	// Create a tool result with known content size.
	// 400 chars / 4 chars-per-token = ~100 tokens.
	resultContent := strings.Repeat("x", 400)
	nodes := []*types.Node{
		{ID: "1", NodeType: types.NodeTypeUser, Content: "Do something"},
		{ID: "2", ParentID: "1", NodeType: types.NodeTypeAssistant,
			Content: `[{"type":"tool_use","id":"call_1","name":"bash","input":{}}]`},
		{ID: "3", ParentID: "2", NodeType: types.NodeTypeUser,
			Content: `[{"type":"tool_result","tool_use_id":"call_1","content":"` + resultContent + `"}]`},
		{ID: "4", ParentID: "3", NodeType: types.NodeTypeAssistant, Content: "Done."},
	}

	result := a.renderTree(nodes)

	// Should include a token estimate annotation.
	if !strings.Contains(result, "tokens") {
		t.Errorf("expected token estimate in tree, got:\n%s", result)
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

func TestRebuildChatMessages_IncludesToolCalls(t *testing.T) {
	a := &App{}

	nodes := []*types.Node{
		{ID: "1", NodeType: types.NodeTypeUser, Content: "Run ls"},
		{ID: "2", ParentID: "1", NodeType: types.NodeTypeAssistant,
			Content: `[{"type":"text","text":"Let me run that."},{"type":"tool_use","id":"call_1","name":"bash","input":{"command":"ls -la"}}]`},
		{ID: "3", ParentID: "2", NodeType: types.NodeTypeUser,
			Content: `[{"type":"tool_result","tool_use_id":"call_1","content":"file1.go\nfile2.go"}]`},
		{ID: "4", ParentID: "3", NodeType: types.NodeTypeAssistant, Content: "Here are the files."},
	}

	msgs := a.rebuildChatMessages(nodes)

	// Should have: user, assistant text, toolCall, toolResult, assistant text
	var kinds []chatMsgKind
	for _, m := range msgs {
		kinds = append(kinds, m.kind)
	}
	want := []chatMsgKind{msgUser, msgAssistant, msgToolCall, msgToolResult, msgAssistant}
	if len(kinds) != len(want) {
		t.Fatalf("got %d messages %v, want %d %v", len(kinds), kinds, len(want), want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("msg[%d].kind = %v, want %v", i, kinds[i], want[i])
		}
	}

	// Tool call should contain the bash command summary.
	for _, m := range msgs {
		if m.kind == msgToolCall {
			if !strings.Contains(m.content, "ls -la") {
				t.Errorf("tool call content = %q, want it to contain 'ls -la'", m.content)
			}
		}
	}

	// Tool result should contain the collapsed output.
	for _, m := range msgs {
		if m.kind == msgToolResult {
			if !strings.Contains(m.content, "file1.go") {
				t.Errorf("tool result content = %q, want it to contain 'file1.go'", m.content)
			}
		}
	}
}

func TestRebuildChatMessages_ToolResultError(t *testing.T) {
	a := &App{}

	nodes := []*types.Node{
		{ID: "1", NodeType: types.NodeTypeUser, Content: "Do something"},
		{ID: "2", ParentID: "1", NodeType: types.NodeTypeAssistant,
			Content: `[{"type":"tool_use","id":"call_1","name":"bash","input":{"command":"bad-cmd"}}]`},
		{ID: "3", ParentID: "2", NodeType: types.NodeTypeUser,
			Content: `[{"type":"tool_result","tool_use_id":"call_1","content":"command not found","is_error":true}]`},
		{ID: "4", ParentID: "3", NodeType: types.NodeTypeAssistant, Content: "That failed."},
	}

	msgs := a.rebuildChatMessages(nodes)

	for _, m := range msgs {
		if m.kind == msgToolResult {
			if !m.isError {
				t.Errorf("expected tool result to be marked as error")
			}
			return
		}
	}
	t.Errorf("no tool result found in messages")
}

func TestRebuildChatMessages_OldFormatToolNodes(t *testing.T) {
	a := &App{}

	nodes := []*types.Node{
		{ID: "1", NodeType: types.NodeTypeUser, Content: "Run ls"},
		{ID: "2", ParentID: "1", NodeType: types.NodeTypeAssistant, Content: "Let me run that."},
		{ID: "3", ParentID: "2", NodeType: types.NodeTypeToolCall, Content: `{"name":"bash","input":{"command":"ls"}}`},
		{ID: "4", ParentID: "3", NodeType: types.NodeTypeToolResult, Content: "file1.go\nfile2.go"},
		{ID: "5", ParentID: "4", NodeType: types.NodeTypeAssistant, Content: "Here are the files."},
	}

	msgs := a.rebuildChatMessages(nodes)

	var kinds []chatMsgKind
	for _, m := range msgs {
		kinds = append(kinds, m.kind)
	}
	want := []chatMsgKind{msgUser, msgAssistant, msgToolCall, msgToolResult, msgAssistant}
	if len(kinds) != len(want) {
		t.Fatalf("got %d messages %v, want %d %v", len(kinds), kinds, len(want), want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("msg[%d].kind = %v, want %v", i, kinds[i], want[i])
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate(truncateOptions{s: "short", max: 10}); got != "short" {
		t.Errorf("truncate short = %q", got)
	}
	got := truncate(truncateOptions{s: "this is a very long string", max: 10})
	// Should be 9 chars of original + "…"
	if got != "this is a…" {
		t.Errorf("truncate long = %q, want %q", got, "this is a…")
	}
}

// ─── 3a: extractAssistantText ───

func TestExtractAssistantText_PlainText(t *testing.T) {
	got := extractAssistantText("Hello world")
	if got != "Hello world" {
		t.Errorf("plain text: got %q, want %q", got, "Hello world")
	}
}

func TestExtractAssistantText_Empty(t *testing.T) {
	if got := extractAssistantText(""); got != "" {
		t.Errorf("empty: got %q", got)
	}
	if got := extractAssistantText("   "); got != "" {
		t.Errorf("whitespace: got %q", got)
	}
}

func TestExtractAssistantText_JSONTextBlocks(t *testing.T) {
	blocks, _ := json.Marshal([]types.ContentBlock{
		{Type: "text", Text: "First paragraph."},
		{Type: "text", Text: "Second paragraph."},
	})
	got := extractAssistantText(string(blocks))
	if got != "First paragraph.\nSecond paragraph." {
		t.Errorf("got %q, want joined text blocks", got)
	}
}

func TestExtractAssistantText_ToolUseOnly(t *testing.T) {
	blocks, _ := json.Marshal([]types.ContentBlock{
		{Type: "tool_use", ID: "call_1", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)},
	})
	got := extractAssistantText(string(blocks))
	if got != "" {
		t.Errorf("tool_use only: got %q, want empty", got)
	}
}

func TestExtractAssistantText_MixedContent(t *testing.T) {
	blocks, _ := json.Marshal([]types.ContentBlock{
		{Type: "text", Text: "Let me check."},
		{Type: "tool_use", ID: "call_1", Name: "bash", Input: json.RawMessage(`{}`)},
		{Type: "text", Text: "Done now."},
	})
	got := extractAssistantText(string(blocks))
	if got != "Let me check.\nDone now." {
		t.Errorf("mixed: got %q", got)
	}
}

func TestExtractAssistantText_InvalidJSON(t *testing.T) {
	// Starts with '[' but isn't valid JSON → should return "".
	got := extractAssistantText("[not valid json")
	if got != "" {
		t.Errorf("invalid JSON: got %q, want empty", got)
	}
}

func TestExtractAssistantText_EmptyTextBlocks(t *testing.T) {
	blocks, _ := json.Marshal([]types.ContentBlock{
		{Type: "text", Text: ""},
		{Type: "text", Text: "Real content"},
		{Type: "text", Text: ""},
	})
	got := extractAssistantText(string(blocks))
	if got != "Real content" {
		t.Errorf("empty text blocks: got %q, want %q", got, "Real content")
	}
}

// ─── 3b: parseAssistantContent ───

func TestParseAssistantContent_PlainText(t *testing.T) {
	preview, tools := parseAssistantContent("Hello, how can I help?")
	if preview != "Hello, how can I help?" {
		t.Errorf("preview = %q", preview)
	}
	if len(tools) != 0 {
		t.Errorf("tools = %v, want empty", tools)
	}
}

func TestParseAssistantContent_MultilinePlainText(t *testing.T) {
	preview, _ := parseAssistantContent("Line one\nLine two\nLine three")
	if preview != "Line one" {
		t.Errorf("preview = %q, want first line only", preview)
	}
}

func TestParseAssistantContent_Empty(t *testing.T) {
	preview, tools := parseAssistantContent("")
	if preview != "" || tools != nil {
		t.Errorf("empty: preview=%q tools=%v", preview, tools)
	}
}

func TestParseAssistantContent_TextAndToolUse(t *testing.T) {
	blocks, _ := json.Marshal([]types.ContentBlock{
		{Type: "text", Text: "Let me run that command."},
		{Type: "tool_use", ID: "call_abc", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)},
	})
	preview, tools := parseAssistantContent(string(blocks))
	if preview != "Let me run that command." {
		t.Errorf("preview = %q", preview)
	}
	if len(tools) != 1 {
		t.Fatalf("tools count = %d, want 1", len(tools))
	}
	if tools[0].id != "call_abc" || tools[0].name != "bash" {
		t.Errorf("tool = %+v", tools[0])
	}
}

func TestParseAssistantContent_MultipleToolUses(t *testing.T) {
	blocks, _ := json.Marshal([]types.ContentBlock{
		{Type: "tool_use", ID: "c1", Name: "bash", Input: json.RawMessage(`{}`)},
		{Type: "tool_use", ID: "c2", Name: "git", Input: json.RawMessage(`{}`)},
	})
	_, tools := parseAssistantContent(string(blocks))
	if len(tools) != 2 {
		t.Fatalf("tools count = %d, want 2", len(tools))
	}
	if tools[0].name != "bash" || tools[1].name != "git" {
		t.Errorf("tools = %+v %+v", tools[0], tools[1])
	}
}

func TestParseAssistantContent_ToolUseNoName(t *testing.T) {
	blocks, _ := json.Marshal([]types.ContentBlock{
		{Type: "tool_use", ID: "c1", Input: json.RawMessage(`{}`)},
	})
	_, tools := parseAssistantContent(string(blocks))
	if len(tools) != 1 || tools[0].name != "tool" {
		t.Errorf("unnamed tool_use should default to 'tool', got %+v", tools)
	}
}

func TestParseAssistantContent_InvalidJSON(t *testing.T) {
	preview, tools := parseAssistantContent("[{broken")
	if preview != "" || tools != nil {
		t.Errorf("invalid JSON: preview=%q tools=%v", preview, tools)
	}
}

// ─── 3c: parseToolResults and isToolResultContent ───

func TestIsToolResultContent_True(t *testing.T) {
	content := `[{"type":"tool_result","tool_use_id":"c1","content":"ok"}]`
	if !isToolResultContent(content) {
		t.Error("should detect tool_result content")
	}
}

func TestIsToolResultContent_WithWhitespace(t *testing.T) {
	content := `  [{"type":"tool_result","tool_use_id":"c1","content":"ok"}]  `
	if !isToolResultContent(content) {
		t.Error("should handle leading/trailing whitespace")
	}
}

func TestIsToolResultContent_False_PlainText(t *testing.T) {
	if isToolResultContent("Hello world") {
		t.Error("plain text should not be tool result content")
	}
}

func TestIsToolResultContent_False_OtherJSON(t *testing.T) {
	content := `[{"type":"text","text":"hello"}]`
	if isToolResultContent(content) {
		t.Error("text blocks should not be tool result content")
	}
}

func TestIsToolResultContent_False_Empty(t *testing.T) {
	if isToolResultContent("") {
		t.Error("empty should return false")
	}
	if isToolResultContent("   ") {
		t.Error("whitespace should return false")
	}
}

func TestParseToolResults_Single(t *testing.T) {
	content := toolResultContent("call_1", "file1.go")
	results := parseToolResults(content)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].toolUseID != "call_1" {
		t.Errorf("toolUseID = %q", results[0].toolUseID)
	}
	if results[0].isError {
		t.Error("should not be error")
	}
}

func TestParseToolResults_Multiple(t *testing.T) {
	blocks, _ := json.Marshal([]types.ContentBlock{
		{Type: "tool_result", ToolUseID: "c1", Content: "ok"},
		{Type: "tool_result", ToolUseID: "c2", Content: "fail", IsError: true},
	})
	results := parseToolResults(string(blocks))
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].toolUseID != "c1" || results[0].isError {
		t.Errorf("result[0] = %+v", results[0])
	}
	if results[1].toolUseID != "c2" || !results[1].isError {
		t.Errorf("result[1] = %+v", results[1])
	}
}

func TestParseToolResults_MixedBlockTypes(t *testing.T) {
	blocks, _ := json.Marshal([]types.ContentBlock{
		{Type: "text", Text: "some text"},
		{Type: "tool_result", ToolUseID: "c1", Content: "ok"},
	})
	results := parseToolResults(string(blocks))
	if len(results) != 1 {
		t.Fatalf("should only return tool_result blocks, got %d", len(results))
	}
}

func TestParseToolResults_InvalidJSON(t *testing.T) {
	results := parseToolResults("not json")
	if results != nil {
		t.Errorf("invalid JSON should return nil, got %v", results)
	}
}

func TestParseToolResults_EmptyArray(t *testing.T) {
	results := parseToolResults("[]")
	if len(results) != 0 {
		t.Errorf("empty array should return empty, got %d", len(results))
	}
}

// ─── 3e: renderTree with complex structures ───

func TestRenderTree_MultipleToolCallChains(t *testing.T) {
	a := &App{}

	// Assistant with 2 tool_use blocks, followed by user with 2 tool_results.
	assistantContent, _ := json.Marshal([]types.ContentBlock{
		{Type: "text", Text: "Let me check both."},
		{Type: "tool_use", ID: "call_1", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)},
		{Type: "tool_use", ID: "call_2", Name: "git", Input: json.RawMessage(`{"args":"status"}`)},
	})
	resultContent, _ := json.Marshal([]types.ContentBlock{
		{Type: "tool_result", ToolUseID: "call_1", Content: "file1.go"},
		{Type: "tool_result", ToolUseID: "call_2", Content: "On branch main"},
	})

	nodes := []*types.Node{
		{ID: "1", NodeType: types.NodeTypeUser, Content: "Check everything"},
		{ID: "2", ParentID: "1", NodeType: types.NodeTypeAssistant, Content: string(assistantContent)},
		{ID: "3", ParentID: "2", NodeType: types.NodeTypeUser, Content: string(resultContent)},
		{ID: "4", ParentID: "3", NodeType: types.NodeTypeAssistant, Content: "All done."},
	}

	result := a.renderTree(nodes)

	// Both tool names should appear.
	if !strings.Contains(result, "bash") {
		t.Errorf("expected 'bash' in output, got:\n%s", result)
	}
	if !strings.Contains(result, "git") {
		t.Errorf("expected 'git' in output, got:\n%s", result)
	}

	// Both should have success markers since neither is_error.
	bashCount := strings.Count(result, "bash")
	gitCount := strings.Count(result, "git")
	if bashCount < 1 || gitCount < 1 {
		t.Errorf("expected at least one 'bash' and one 'git', got bash=%d git=%d", bashCount, gitCount)
	}

	// Tool lines should be indented (start with spaces).
	for _, line := range strings.Split(strings.TrimRight(result, "\n"), "\n") {
		if (strings.Contains(line, "bash") || strings.Contains(line, "git")) &&
			!strings.Contains(line, "Assistant") && !strings.Contains(line, "You:") {
			if !strings.HasPrefix(line, "  ") {
				t.Errorf("tool line should be indented: %q", line)
			}
		}
	}
}

func TestRenderTree_DeeplyNested(t *testing.T) {
	a := &App{
		models: []ModelDef{
			{ID: "claude-sonnet-4-20250514", Provider: ProviderAnthropic, PromptPrice: 3, CompletionPrice: 15},
		},
	}

	// Build a long conversation: 12 nodes alternating user/assistant,
	// with tool calls at nodes 4-5 (assistant+tool-result).
	toolAssistant, _ := json.Marshal([]types.ContentBlock{
		{Type: "text", Text: "Running command."},
		{Type: "tool_use", ID: "call_deep", Name: "bash", Input: json.RawMessage(`{"command":"echo hi"}`)},
	})
	toolResult, _ := json.Marshal([]types.ContentBlock{
		{Type: "tool_result", ToolUseID: "call_deep", Content: "hi"},
	})

	nodes := []*types.Node{
		{ID: "1", NodeType: types.NodeTypeUser, Content: "Start the project"},
		{ID: "2", ParentID: "1", NodeType: types.NodeTypeAssistant, Content: "Sure, let me help.", Model: "claude-sonnet-4-20250514", TokensIn: 100, TokensOut: 50},
		{ID: "3", ParentID: "2", NodeType: types.NodeTypeUser, Content: "First, list files"},
		{ID: "4", ParentID: "3", NodeType: types.NodeTypeAssistant, Content: string(toolAssistant), Model: "claude-sonnet-4-20250514", TokensIn: 200, TokensOut: 80},
		{ID: "5", ParentID: "4", NodeType: types.NodeTypeUser, Content: string(toolResult)},
		{ID: "6", ParentID: "5", NodeType: types.NodeTypeAssistant, Content: "Here are the files.", Model: "claude-sonnet-4-20250514", TokensIn: 300, TokensOut: 100},
		{ID: "7", ParentID: "6", NodeType: types.NodeTypeUser, Content: "Now fix the bug"},
		{ID: "8", ParentID: "7", NodeType: types.NodeTypeAssistant, Content: "I see the issue.", Model: "claude-sonnet-4-20250514", TokensIn: 400, TokensOut: 150},
		{ID: "9", ParentID: "8", NodeType: types.NodeTypeUser, Content: "Can you also add tests?"},
		{ID: "10", ParentID: "9", NodeType: types.NodeTypeAssistant, Content: "Adding tests now.", Model: "claude-sonnet-4-20250514", TokensIn: 500, TokensOut: 200},
		{ID: "11", ParentID: "10", NodeType: types.NodeTypeUser, Content: "Thanks, looks good"},
		{ID: "12", ParentID: "11", NodeType: types.NodeTypeAssistant, Content: "You're welcome!", Model: "claude-sonnet-4-20250514", TokensIn: 600, TokensOut: 250},
	}

	result := a.renderTree(nodes)
	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")

	// Should have many lines (at least one per node, plus tool, plus total).
	if len(lines) < 12 {
		t.Errorf("expected at least 12 lines for 12-node conversation, got %d:\n%s", len(lines), result)
	}

	// User and assistant lines should be at column 0.
	for _, line := range lines {
		if line == "" || strings.HasPrefix(line, "Total:") {
			continue
		}
		isUserLine := strings.Contains(line, "You:")
		isAssistantLine := strings.Contains(line, "Assistant")
		if isUserLine && strings.HasPrefix(line, "  ") {
			t.Errorf("user line should not be indented: %q", line)
		}
		if isAssistantLine && strings.HasPrefix(line, "  ") {
			t.Errorf("assistant line should not be indented: %q", line)
		}
	}

	// Tool result line should be indented.
	foundIndentedTool := false
	for _, line := range lines {
		if strings.Contains(line, "bash") && strings.HasPrefix(line, "  ") {
			foundIndentedTool = true
		}
	}
	if !foundIndentedTool {
		t.Errorf("expected indented tool line with 'bash', got:\n%s", result)
	}

	// Should contain Total cost line.
	if !strings.Contains(result, "Total:") {
		t.Errorf("expected Total cost line, got:\n%s", result)
	}
}

func TestRenderTree_CostAndTokenDisplay(t *testing.T) {
	a := &App{
		models: []ModelDef{
			{ID: "claude-sonnet-4-20250514", Provider: ProviderAnthropic, PromptPrice: 3, CompletionPrice: 15},
		},
	}

	nodes := []*types.Node{
		{ID: "1", NodeType: types.NodeTypeUser, Content: "Hello"},
		{
			ID: "2", ParentID: "1", NodeType: types.NodeTypeAssistant,
			Content:         "Hi there!",
			Model:           "claude-sonnet-4-20250514",
			TokensIn:        10000,
			TokensOut:       5000,
			TokensCacheRead: 2000,
		},
	}

	result := a.renderTree(nodes)

	// Verify token display: TokensIn + TokensCacheRead = 12000, TokensOut = 5000.
	if !strings.Contains(result, "12000tok in") {
		t.Errorf("expected '12000tok in' in output, got:\n%s", result)
	}
	if !strings.Contains(result, "5000tok out") {
		t.Errorf("expected '5000tok out' in output, got:\n%s", result)
	}

	// Verify cost appears. Compute expected:
	// input: 10000 * 3 / 1_000_000 = $0.03
	// output: 5000 * 15 / 1_000_000 = $0.075
	// cache read: 2000 * 3 * 0.1 / 1_000_000 = $0.0006
	// total: $0.1056
	if !strings.Contains(result, "$0.11") {
		t.Errorf("expected cost ~$0.11 in output, got:\n%s", result)
	}

	// Verify Total line also shows cost.
	if !strings.Contains(result, "Total:") {
		t.Errorf("expected Total line, got:\n%s", result)
	}

	// Now test with large token counts to verify formatting.
	nodes2 := []*types.Node{
		{ID: "1", NodeType: types.NodeTypeUser, Content: "Hello"},
		{
			ID: "2", ParentID: "1", NodeType: types.NodeTypeAssistant,
			Content:  "Response",
			Model:    "claude-sonnet-4-20250514",
			TokensIn: 150000, TokensOut: 50000,
		},
	}
	result2 := a.renderTree(nodes2)
	// 150000 tokens in → should show "150000tok in"
	if !strings.Contains(result2, "150000tok in") {
		t.Errorf("expected '150000tok in' for large token count, got:\n%s", result2)
	}
}

func TestRenderTree_ModelNameShortening(t *testing.T) {
	a := &App{
		models: []ModelDef{
			{ID: "claude-sonnet-4-20250514", Provider: ProviderAnthropic, PromptPrice: 3, CompletionPrice: 15},
			{ID: "gpt-4o", Provider: ProviderOpenAI, PromptPrice: 5, CompletionPrice: 15},
		},
	}

	tests := []struct {
		model     string
		wantShort string
		wantNot   string // prefix that should be stripped
	}{
		{"anthropic/claude-sonnet-4-20250514", "claude-sonnet-4-20250514", "anthropic/"},
		{"openai/gpt-4o", "gpt-4o", "openai/"},
		{"google/gemini-pro", "gemini-pro", "google/"},
		{"x-ai/grok-2", "grok-2", "x-ai/"},
		{"claude-sonnet-4-20250514", "claude-sonnet-4-20250514", ""}, // no prefix to strip
	}

	for _, tt := range tests {
		nodes := []*types.Node{
			{ID: "1", NodeType: types.NodeTypeUser, Content: "Hello"},
			{ID: "2", ParentID: "1", NodeType: types.NodeTypeAssistant, Content: "Hi!", Model: tt.model, TokensIn: 100, TokensOut: 50},
		}

		result := a.renderTree(nodes)

		if !strings.Contains(result, tt.wantShort) {
			t.Errorf("model %q: expected shortened name %q in output, got:\n%s", tt.model, tt.wantShort, result)
		}
		if tt.wantNot != "" && strings.Contains(result, tt.wantNot) {
			t.Errorf("model %q: should not contain prefix %q in output, got:\n%s", tt.model, tt.wantNot, result)
		}
	}
}

func TestRenderTree_EmptyConversation(t *testing.T) {
	a := &App{}

	result := a.renderTree(nil)
	if result != "" {
		t.Errorf("nil nodes should produce empty result, got: %q", result)
	}

	result = a.renderTree([]*types.Node{})
	if result != "" {
		t.Errorf("empty nodes should produce empty result, got: %q", result)
	}
}

func TestRenderTree_ToolErrorAndSuccess(t *testing.T) {
	a := &App{}

	// Assistant issues two tool calls; one succeeds, one fails.
	assistantContent, _ := json.Marshal([]types.ContentBlock{
		{Type: "tool_use", ID: "call_ok", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)},
		{Type: "tool_use", ID: "call_fail", Name: "git", Input: json.RawMessage(`{"args":"push"}`)},
	})
	resultContent, _ := json.Marshal([]types.ContentBlock{
		{Type: "tool_result", ToolUseID: "call_ok", Content: "file1.go"},
		{Type: "tool_result", ToolUseID: "call_fail", Content: "permission denied", IsError: true},
	})

	nodes := []*types.Node{
		{ID: "1", NodeType: types.NodeTypeUser, Content: "Deploy please"},
		{ID: "2", ParentID: "1", NodeType: types.NodeTypeAssistant, Content: string(assistantContent)},
		{ID: "3", ParentID: "2", NodeType: types.NodeTypeUser, Content: string(resultContent)},
		{ID: "4", ParentID: "3", NodeType: types.NodeTypeAssistant, Content: "One succeeded, one failed."},
	}

	result := a.renderTree(nodes)

	// Should have both success and error markers.
	if !strings.Contains(result, "\u2713") {
		t.Errorf("expected success marker in output, got:\n%s", result)
	}
	if !strings.Contains(result, "\u2717") {
		t.Errorf("expected error marker in output, got:\n%s", result)
	}

	// Verify tool names are matched correctly: bash should have success, git should have error.
	lines := strings.Split(result, "\n")
	for _, line := range lines {
		if strings.Contains(line, "bash") && !strings.Contains(line, "Assistant") {
			if strings.Contains(line, "\u2717") {
				t.Errorf("bash should have success marker, not error: %q", line)
			}
		}
		if strings.Contains(line, "git") && !strings.Contains(line, "Assistant") {
			if strings.Contains(line, "\u2713") {
				t.Errorf("git should have error marker, not success: %q", line)
			}
		}
	}
}

// ─── 3f: rebuildChatMessages ───

func TestRebuildChatMessages_EmptyNodes(t *testing.T) {
	a := &App{}

	// nil input
	msgs := a.rebuildChatMessages(nil)
	if len(msgs) != 0 {
		t.Errorf("nil nodes: got %d messages, want 0", len(msgs))
	}

	// empty slice
	msgs = a.rebuildChatMessages([]*types.Node{})
	if len(msgs) != 0 {
		t.Errorf("empty nodes: got %d messages, want 0", len(msgs))
	}
}

func TestRebuildChatMessages_UserOnly(t *testing.T) {
	a := &App{}

	nodes := []*types.Node{
		{ID: "1", NodeType: types.NodeTypeUser, Content: "First question"},
		{ID: "2", ParentID: "1", NodeType: types.NodeTypeUser, Content: "Second question"},
		{ID: "3", ParentID: "2", NodeType: types.NodeTypeUser, Content: "Third question"},
	}

	msgs := a.rebuildChatMessages(nodes)

	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	for i, m := range msgs {
		if m.kind != msgUser {
			t.Errorf("msg[%d].kind = %v, want msgUser", i, m.kind)
		}
		if !m.leadBlank {
			t.Errorf("msg[%d].leadBlank should be true", i)
		}
	}
	if msgs[0].content != "First question" {
		t.Errorf("msg[0].content = %q, want %q", msgs[0].content, "First question")
	}
	if msgs[1].content != "Second question" {
		t.Errorf("msg[1].content = %q, want %q", msgs[1].content, "Second question")
	}
	if msgs[2].content != "Third question" {
		t.Errorf("msg[2].content = %q, want %q", msgs[2].content, "Third question")
	}
}

func TestRebuildChatMessages_MultipleToolResults(t *testing.T) {
	a := &App{}

	// Assistant with 2 tool_use blocks.
	assistantBlocks, _ := json.Marshal([]types.ContentBlock{
		{Type: "text", Text: "Let me check both."},
		{Type: "tool_use", ID: "call_1", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)},
		{Type: "tool_use", ID: "call_2", Name: "git", Input: json.RawMessage(`{"subcommand":"status"}`)},
	})
	// User node with 2 tool_results.
	resultBlocks, _ := json.Marshal([]types.ContentBlock{
		{Type: "tool_result", ToolUseID: "call_1", Content: "file1.go\nfile2.go"},
		{Type: "tool_result", ToolUseID: "call_2", Content: "On branch main"},
	})

	nodes := []*types.Node{
		{ID: "1", NodeType: types.NodeTypeUser, Content: "Check files and status"},
		{ID: "2", ParentID: "1", NodeType: types.NodeTypeAssistant, Content: string(assistantBlocks)},
		{ID: "3", ParentID: "2", NodeType: types.NodeTypeUser, Content: string(resultBlocks)},
		{ID: "4", ParentID: "3", NodeType: types.NodeTypeAssistant, Content: "All done."},
	}

	msgs := a.rebuildChatMessages(nodes)

	// Expected: user, assistant text, toolCall1, toolResult1, toolCall2, toolResult2, assistant text
	wantKinds := []chatMsgKind{msgUser, msgAssistant, msgToolCall, msgToolResult, msgToolCall, msgToolResult, msgAssistant}
	if len(msgs) != len(wantKinds) {
		var gotKinds []chatMsgKind
		for _, m := range msgs {
			gotKinds = append(gotKinds, m.kind)
		}
		t.Fatalf("got %d messages %v, want %d %v", len(msgs), gotKinds, len(wantKinds), wantKinds)
	}
	for i := range wantKinds {
		if msgs[i].kind != wantKinds[i] {
			t.Errorf("msg[%d].kind = %v, want %v", i, msgs[i].kind, wantKinds[i])
		}
	}

	// First tool call should be bash with "ls" command.
	if !strings.Contains(msgs[2].content, "ls") {
		t.Errorf("first tool call should contain 'ls', got %q", msgs[2].content)
	}
	// First tool result should contain file listing.
	if !strings.Contains(msgs[3].content, "file1.go") {
		t.Errorf("first tool result should contain 'file1.go', got %q", msgs[3].content)
	}
	// Second tool call should be git.
	if !strings.Contains(msgs[4].content, "git") {
		t.Errorf("second tool call should contain 'git', got %q", msgs[4].content)
	}
	// Second tool result should contain branch info.
	if !strings.Contains(msgs[5].content, "On branch main") {
		t.Errorf("second tool result should contain 'On branch main', got %q", msgs[5].content)
	}
}

func TestRebuildChatMessages_OldFormatToolResultIsError(t *testing.T) {
	a := &App{}

	nodes := []*types.Node{
		{ID: "1", NodeType: types.NodeTypeUser, Content: "Do something"},
		{ID: "2", ParentID: "1", NodeType: types.NodeTypeAssistant, Content: "Let me try."},
		{ID: "3", ParentID: "2", NodeType: types.NodeTypeToolCall, Content: `{"name":"bash","input":{"command":"bad-cmd"}}`},
		{ID: "4", ParentID: "3", NodeType: types.NodeTypeToolResult, Content: `{"is_error":true,"output":"command not found"}`},
		{ID: "5", ParentID: "4", NodeType: types.NodeTypeAssistant, Content: "That failed."},
	}

	msgs := a.rebuildChatMessages(nodes)

	foundToolResult := false
	for _, m := range msgs {
		if m.kind == msgToolResult {
			foundToolResult = true
			if !m.isError {
				t.Errorf("old-format tool result with is_error:true should have isError set")
			}
		}
	}
	if !foundToolResult {
		t.Errorf("no tool result message found")
	}
}

func TestRebuildChatMessages_ToolResultDuration(t *testing.T) {
	a := &App{}

	// New-format: assistant with tool_use, user with tool_result including duration_ms.
	assistantBlocks, _ := json.Marshal([]types.ContentBlock{
		{Type: "tool_use", ID: "call_1", Name: "bash", Input: json.RawMessage(`{"command":"sleep 5"}`)},
	})
	resultBlocks, _ := json.Marshal([]types.ContentBlock{
		{Type: "tool_result", ToolUseID: "call_1", Content: "done", DurationMs: 5432},
	})

	nodes := []*types.Node{
		{ID: "1", NodeType: types.NodeTypeUser, Content: "Wait"},
		{ID: "2", ParentID: "1", NodeType: types.NodeTypeAssistant, Content: string(assistantBlocks)},
		{ID: "3", ParentID: "2", NodeType: types.NodeTypeUser, Content: string(resultBlocks)},
		{ID: "4", ParentID: "3", NodeType: types.NodeTypeAssistant, Content: "Done waiting."},
	}

	msgs := a.rebuildChatMessages(nodes)

	for _, m := range msgs {
		if m.kind == msgToolResult {
			want := 5432 * time.Millisecond
			if m.duration != want {
				t.Errorf("tool result duration = %v, want %v", m.duration, want)
			}
			return
		}
	}
	t.Errorf("no tool result message found")
}

func TestRebuildChatMessages_MixedOldAndNewFormat(t *testing.T) {
	a := &App{}

	// New-format assistant with tool_use.
	newAssistantBlocks, _ := json.Marshal([]types.ContentBlock{
		{Type: "text", Text: "Running command."},
		{Type: "tool_use", ID: "call_new", Name: "bash", Input: json.RawMessage(`{"command":"echo hello"}`)},
	})
	// New-format user tool_result.
	newResultBlocks, _ := json.Marshal([]types.ContentBlock{
		{Type: "tool_result", ToolUseID: "call_new", Content: "hello"},
	})

	nodes := []*types.Node{
		// New-format tool call/result.
		{ID: "1", NodeType: types.NodeTypeUser, Content: "Say hello"},
		{ID: "2", ParentID: "1", NodeType: types.NodeTypeAssistant, Content: string(newAssistantBlocks)},
		{ID: "3", ParentID: "2", NodeType: types.NodeTypeUser, Content: string(newResultBlocks)},
		{ID: "4", ParentID: "3", NodeType: types.NodeTypeAssistant, Content: "Got it."},
		// Old-format tool call/result.
		{ID: "5", ParentID: "4", NodeType: types.NodeTypeToolCall, Content: `{"name":"git","input":{"subcommand":"status"}}`},
		{ID: "6", ParentID: "5", NodeType: types.NodeTypeToolResult, Content: "On branch main\nnothing to commit"},
		{ID: "7", ParentID: "6", NodeType: types.NodeTypeAssistant, Content: "All clean."},
	}

	msgs := a.rebuildChatMessages(nodes)

	// Expected message kinds:
	// user, assistant("Running command."), toolCall(new bash), toolResult(new),
	// assistant("Got it."), toolCall(old git), toolResult(old), assistant("All clean.")
	wantKinds := []chatMsgKind{
		msgUser, msgAssistant, msgToolCall, msgToolResult,
		msgAssistant, msgToolCall, msgToolResult, msgAssistant,
	}
	if len(msgs) != len(wantKinds) {
		var gotKinds []chatMsgKind
		for _, m := range msgs {
			gotKinds = append(gotKinds, m.kind)
		}
		t.Fatalf("got %d messages %v, want %d %v", len(msgs), gotKinds, len(wantKinds), wantKinds)
	}
	for i := range wantKinds {
		if msgs[i].kind != wantKinds[i] {
			t.Errorf("msg[%d].kind = %v, want %v", i, msgs[i].kind, wantKinds[i])
		}
	}

	// Verify new-format tool call contains bash command.
	if !strings.Contains(msgs[2].content, "echo hello") {
		t.Errorf("new-format tool call should contain 'echo hello', got %q", msgs[2].content)
	}
	// Verify new-format tool result.
	if !strings.Contains(msgs[3].content, "hello") {
		t.Errorf("new-format tool result should contain 'hello', got %q", msgs[3].content)
	}
	// Verify old-format tool call contains git.
	if !strings.Contains(msgs[5].content, "git") {
		t.Errorf("old-format tool call should contain 'git', got %q", msgs[5].content)
	}
	// Verify old-format tool result.
	if !strings.Contains(msgs[6].content, "On branch main") {
		t.Errorf("old-format tool result should contain 'On branch main', got %q", msgs[6].content)
	}
}
