package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"langdag.com/langdag/types"
)

func TestSubAgentDisplayStateTransitions(t *testing.T) {
	agentID := "test-agent-01"

	t.Run("start populates mode and startTime", func(t *testing.T) {
		app := &App{headless: true, width: 80}
		app.handleAgentEvent(AgentEvent{
			Type:    EventSubAgentStart,
			AgentID: agentID,
			Task:    "Research codebase",
			Mode:    "explore",
		})

		sa := app.subAgents[agentID]
		if sa == nil {
			t.Fatal("sub-agent not created")
		}
		if sa.mode != "explore" {
			t.Errorf("mode = %q, want %q", sa.mode, "explore")
		}
		if sa.startTime.IsZero() {
			t.Error("startTime not set")
		}
		if sa.task != "Research codebase" {
			t.Errorf("task = %q, want %q", sa.task, "Research codebase")
		}
	})

	t.Run("tool status increments toolCount", func(t *testing.T) {
		app := &App{headless: true, width: 80}
		app.handleAgentEvent(AgentEvent{
			Type: EventSubAgentStart, AgentID: agentID, Task: "work", Mode: "explore",
		})
		app.handleAgentEvent(AgentEvent{
			Type: EventSubAgentStatus, AgentID: agentID, Text: "tool: read_file",
		})
		app.handleAgentEvent(AgentEvent{
			Type: EventSubAgentStatus, AgentID: agentID, Text: "tool: grep",
		})
		app.handleAgentEvent(AgentEvent{
			Type: EventSubAgentStatus, AgentID: agentID, Text: "thinking about things",
		})

		sa := app.subAgents[agentID]
		if sa.toolCount != 2 {
			t.Errorf("toolCount = %d, want 2", sa.toolCount)
		}
	})

	t.Run("done captures tokens and not failed", func(t *testing.T) {
		app := &App{headless: true, width: 80}
		app.handleAgentEvent(AgentEvent{
			Type: EventSubAgentStart, AgentID: agentID, Task: "work", Mode: "implement",
		})
		app.handleAgentEvent(AgentEvent{
			Type:    EventSubAgentStatus,
			AgentID: agentID,
			Text:    "done",
			Usage:   &types.Usage{InputTokens: 500, OutputTokens: 200},
		})

		sa := app.subAgents[agentID]
		if !sa.done {
			t.Error("expected done=true")
		}
		if sa.failed {
			t.Error("expected failed=false")
		}
		if sa.inputTokens != 500 {
			t.Errorf("inputTokens = %d, want 500", sa.inputTokens)
		}
		if sa.outputTokens != 200 {
			t.Errorf("outputTokens = %d, want 200", sa.outputTokens)
		}
	})

	t.Run("done with error sets failed", func(t *testing.T) {
		app := &App{headless: true, width: 80}
		app.handleAgentEvent(AgentEvent{
			Type: EventSubAgentStart, AgentID: agentID, Task: "work", Mode: "explore",
		})
		app.handleAgentEvent(AgentEvent{
			Type:    EventSubAgentStatus,
			AgentID: agentID,
			Text:    "done",
			IsError: true,
			Usage:   &types.Usage{InputTokens: 100, OutputTokens: 50},
		})

		sa := app.subAgents[agentID]
		if !sa.failed {
			t.Error("expected failed=true")
		}
	})

	t.Run("full lifecycle start-tools-done", func(t *testing.T) {
		app := &App{headless: true, width: 80}
		// Start
		app.handleAgentEvent(AgentEvent{
			Type: EventSubAgentStart, AgentID: agentID, Task: "Explore auth module", Mode: "explore",
		})
		// Tool calls
		for i := 0; i < 5; i++ {
			app.handleAgentEvent(AgentEvent{
				Type: EventSubAgentStatus, AgentID: agentID, Text: "tool: glob",
			})
		}
		// Done
		app.handleAgentEvent(AgentEvent{
			Type:    EventSubAgentStatus,
			AgentID: agentID,
			Text:    "done",
			Usage:   &types.Usage{InputTokens: 1000, OutputTokens: 400},
		})

		sa := app.subAgents[agentID]
		if sa.toolCount != 5 {
			t.Errorf("toolCount = %d, want 5", sa.toolCount)
		}
		if !sa.done {
			t.Error("expected done=true")
		}
		if sa.inputTokens != 1000 {
			t.Errorf("inputTokens = %d, want 1000", sa.inputTokens)
		}
		if sa.outputTokens != 400 {
			t.Errorf("outputTokens = %d, want 400", sa.outputTokens)
		}
	})
}

func TestSubAgentGroupedDisplay(t *testing.T) {
	stripANSI := func(s string) string {
		return ansiEscRe.ReplaceAllString(s, "")
	}

	t.Run("multiple agents same mode grouped", func(t *testing.T) {
		app := &App{headless: true, width: 80}
		now := time.Now()
		app.subAgents = map[string]*subAgentDisplay{
			"a1": {task: "Research auth", mode: "explore", startTime: now, toolCount: 10},
			"a2": {task: "Research storage", mode: "explore", startTime: now, toolCount: 5},
		}
		lines := app.subAgentDisplayLines()
		if len(lines) == 0 {
			t.Fatal("expected display lines")
		}
		// First line should be the group header.
		header := stripANSI(lines[0])
		if !strings.Contains(header, "2 Explore agents") {
			t.Errorf("header = %q, want to contain '2 Explore agents'", header)
		}
		// Should have 3 lines total: header + 2 agents.
		if len(lines) != 3 {
			t.Errorf("got %d lines, want 3", len(lines))
		}
	})

	t.Run("single agent shows singular header", func(t *testing.T) {
		app := &App{headless: true, width: 80}
		app.subAgents = map[string]*subAgentDisplay{
			"a1": {task: "Implement feature", mode: "implement", startTime: time.Now()},
		}
		lines := app.subAgentDisplayLines()
		header := stripANSI(lines[0])
		if !strings.Contains(header, "Implement agent") {
			t.Errorf("header = %q, want to contain 'Implement agent'", header)
		}
	})

	t.Run("mixed modes produce separate groups", func(t *testing.T) {
		app := &App{headless: true, width: 80}
		now := time.Now()
		app.subAgents = map[string]*subAgentDisplay{
			"a1": {task: "Research", mode: "explore", startTime: now},
			"a2": {task: "Write code", mode: "implement", startTime: now},
		}
		lines := app.subAgentDisplayLines()
		// Should have 2 headers + 2 agent lines = 4.
		if len(lines) != 4 {
			t.Errorf("got %d lines, want 4", len(lines))
		}
		// First header should be explore (sorted first).
		h0 := stripANSI(lines[0])
		if !strings.Contains(h0, "Explore") {
			t.Errorf("first header = %q, want Explore group first", h0)
		}
	})

	t.Run("completed agent shows checkmark", func(t *testing.T) {
		app := &App{headless: true, width: 80}
		now := time.Now().Add(-5 * time.Second)
		app.subAgents = map[string]*subAgentDisplay{
			"a1": {task: "Done task", mode: "explore", startTime: now, done: true, inputTokens: 500, outputTokens: 200, toolCount: 10},
			"a2": {task: "Active task", mode: "explore", startTime: now},
		}
		lines := app.subAgentDisplayLines()
		// Find the completed agent line.
		found := false
		for _, line := range lines {
			plain := stripANSI(line)
			if strings.Contains(plain, "Done task") {
				found = true
				if !strings.Contains(plain, "✓") {
					t.Errorf("completed agent line = %q, expected ✓", plain)
				}
			}
		}
		if !found {
			t.Error("completed agent not found in display")
		}
	})

	t.Run("failed agent shows cross", func(t *testing.T) {
		app := &App{headless: true, width: 80}
		now := time.Now().Add(-3 * time.Second)
		app.subAgents = map[string]*subAgentDisplay{
			"a1": {task: "Failed task", mode: "explore", startTime: now, done: true, failed: true},
			"a2": {task: "Active task", mode: "explore", startTime: now},
		}
		lines := app.subAgentDisplayLines()
		found := false
		for _, line := range lines {
			plain := stripANSI(line)
			if strings.Contains(plain, "Failed task") {
				found = true
				if !strings.Contains(plain, "✗") {
					t.Errorf("failed agent line = %q, expected ✗", plain)
				}
			}
		}
		if !found {
			t.Error("failed agent not found in display")
		}
	})

	t.Run("all done returns nil", func(t *testing.T) {
		app := &App{headless: true, width: 80}
		app.subAgents = map[string]*subAgentDisplay{
			"a1": {task: "Done", mode: "explore", done: true},
		}
		lines := app.subAgentDisplayLines()
		if lines != nil {
			t.Errorf("expected nil when all done, got %v", lines)
		}
	})

	t.Run("metrics shown in agent line", func(t *testing.T) {
		app := &App{headless: true, width: 80}
		now := time.Now().Add(-2 * time.Second)
		app.subAgents = map[string]*subAgentDisplay{
			"a1": {task: "Research", mode: "explore", startTime: now, toolCount: 15, inputTokens: 1200, outputTokens: 300},
		}
		lines := app.subAgentDisplayLines()
		// Agent line should contain tool count and token counts.
		agentLine := stripANSI(lines[1]) // skip header
		if !strings.Contains(agentLine, "15 🛠️") {
			t.Errorf("agent line = %q, missing tool count", agentLine)
		}
		if !strings.Contains(agentLine, "↑1200") {
			t.Errorf("agent line = %q, missing input tokens", agentLine)
		}
		if !strings.Contains(agentLine, "↓300") {
			t.Errorf("agent line = %q, missing output tokens", agentLine)
		}
	})
}

func TestBrailleSpinner(t *testing.T) {
	// Verify spinner cycles through all 8 frames.
	seen := make(map[string]bool)
	for i := 0; i < brailleSpinnerFrameCount; i++ {
		elapsed := time.Duration(i*50) * time.Millisecond
		s := brailleSpinner(elapsed)
		plain := ansiEscRe.ReplaceAllString(s, "")
		seen[plain] = true
	}
	if len(seen) != brailleSpinnerFrameCount {
		t.Errorf("expected %d unique frames, got %d", brailleSpinnerFrameCount, len(seen))
	}

	// Verify it wraps (frame 8 == frame 0).
	s0 := ansiEscRe.ReplaceAllString(brailleSpinner(0), "")
	s8 := ansiEscRe.ReplaceAllString(brailleSpinner(time.Duration(brailleSpinnerFrameCount*50)*time.Millisecond), "")
	if s0 != s8 {
		t.Errorf("spinner should wrap: frame 0 = %q, frame %d = %q", s0, brailleSpinnerFrameCount, s8)
	}
}

func TestSubAgentDoneNoCompletionMessage(t *testing.T) {
	// Verify that successful sub-agent completion doesn't append a msgInfo message.
	app := &App{headless: true, width: 80}
	app.handleAgentEvent(AgentEvent{
		Type: EventSubAgentStart, AgentID: "a1", Task: "work", Mode: "explore",
	})
	app.handleAgentEvent(AgentEvent{
		Type:    EventSubAgentStatus,
		AgentID: "a1",
		Text:    "done",
		Usage:   &types.Usage{InputTokens: 100, OutputTokens: 50},
	})

	// Should not have any info messages about completion.
	for _, msg := range app.messages {
		if msg.kind == msgInfo && strings.Contains(msg.content, "completed") {
			t.Errorf("unexpected completion message: %q", msg.content)
		}
	}
}

func TestSubAgentFailedEmitsMessage(t *testing.T) {
	// Verify that failed sub-agent completion does append a msgInfo message.
	app := &App{headless: true, width: 80}
	app.handleAgentEvent(AgentEvent{
		Type: EventSubAgentStart, AgentID: "a1", Task: "risky work", Mode: "implement",
	})
	app.handleAgentEvent(AgentEvent{
		Type:    EventSubAgentStatus,
		AgentID: "a1",
		Text:    "done",
		IsError: true,
		Usage:   &types.Usage{InputTokens: 100, OutputTokens: 50},
	})

	foundFailed := false
	for _, msg := range app.messages {
		if msg.kind == msgInfo && strings.Contains(msg.content, "failed") {
			foundFailed = true
		}
	}
	if !foundFailed {
		t.Error("expected a failed message for errored sub-agent")
	}
}

func TestWrapString(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		startCol int
		w        int
		want     []string
	}{
		{"empty", "", 0, 80, []string{""}},
		{"short", "hello", 0, 80, []string{"hello"}},
		{"exact width fits", "abcde", 0, 5, []string{"abcde"}},
		{"wraps at width", "abcdef", 0, 5, []string{"abcde", "f"}},
		{"with startCol", "abc", 3, 5, []string{"", "abc"}},
		{"emoji width", "Hi 👋 there", 0, 8, []string{"Hi 👋 ", "there"}},
		{"ansi not counted", "\033[34;3mhello\033[0m", 0, 5, []string{"\033[34;3mhello\033[0m"}},
		{"ansi re-emitted on wrap", "\033[34;3mabcdefgh\033[0m", 0, 5, []string{"\033[34;3mabcde", "\033[34;3mfgh\033[0m"}},

		// Word-wrap behavior
		{"word wrap basic", "hello world foo", 0, 11, []string{"hello world", "foo"}},
		{"word wrap multi", "one two three four five", 0, 10, []string{"one two ", "three four", "five"}},
		{"long word fallback", "abcdefghijklmno pq", 0, 10, []string{"abcdefghij", "klmno pq"}},
		{"single word exact", "hello", 0, 5, []string{"hello"}},
		{"single word overflow", "overflow", 0, 5, []string{"overf", "low"}},
		{"startCol word wrap", "hello world", 4, 10, []string{"hello ", "world"}},
		{"startCol forces wrap", "longword", 6, 10, []string{"", "longword"}},
		{"ansi across word boundary", "\033[1mhello\033[0m \033[2mworld\033[0m", 0, 8, []string{"\033[1mhello\033[0m ", "\033[2mworld\033[0m"}},
		{"ansi mid-word preserved", "\033[1mhel\033[31mlo\033[0m world", 0, 8, []string{"\033[1mhel\033[31mlo\033[0m ", "world"}},
		{"multiple spaces", "a  b  c", 0, 80, []string{"a  b  c"}},
		{"empty string", "", 0, 5, []string{""}},
		{"only spaces", "   ", 0, 5, []string{"   "}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapString(tt.s, tt.startCol, tt.w)
			if len(got) != len(tt.want) {
				t.Fatalf("wrapString(%q, %d, %d) = %v (len %d), want %v (len %d)",
					tt.s, tt.startCol, tt.w, got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("row %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestGetVisualLines(t *testing.T) {
	tests := []struct {
		name  string
		input string
		width int
		want  int // number of visual lines
	}{
		{"empty", "", 80, 1},
		{"short", "hi", 80, 1},
		{"newline", "a\nb", 80, 2},
		{"char_wrap", "abcdefgh", 5, 3},            // no spaces → char wrap: "abc" | "defgh" | ""
		{"word_wrap", "hello world", 10, 2},         // "hello " | "world"
		{"word_wrap_multi", "a bc de", 5, 3},        // "a " | "bc " | "de"
		{"long_word_fallback", "abcdefghij klm", 7, 3}, // "abcde" | "fghij " | "klm" (char then word)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runes := []rune(tt.input)
			got := getVisualLines(runes, len(runes), tt.width)
			if len(got) != tt.want {
				t.Errorf("getVisualLines(%q, w=%d) = %d lines, want %d; lines=%+v",
					tt.input, tt.width, len(got), tt.want, got)
			}
		})
	}
}

func TestGetVisualLinesContent(t *testing.T) {
	// Verify exact line breaks for word wrapping
	input := []rune("hello world foo")
	lines := getVisualLines(input, len(input), 12) // avail first=10, then 12
	// "hello " (6) + prefix 2 = 8 < 12; "world " would push to 14 → wrap at space
	// Expected: "hello " | "world foo"
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %+v", len(lines), lines)
	}
	line0 := string(input[lines[0].start : lines[0].start+lines[0].length])
	line1 := string(input[lines[1].start : lines[1].start+lines[1].length])
	if line0 != "hello " {
		t.Errorf("line 0 = %q, want %q", line0, "hello ")
	}
	if line1 != "world foo" {
		t.Errorf("line 1 = %q, want %q", line1, "world foo")
	}
}

func TestCursorVisualPosWordWrap(t *testing.T) {
	input := []rune("abc defg")
	// width=7, prefix=2, avail first=5
	// Word wrap: "abc " (4 chars, col 2+4=6 < 7), then 'd' makes 2+5=7 → wrap at space
	// Line 0: {0, 4, 2} = "abc ", Line 1: {4, 4, 0} = "defg"

	// Cursor on space (pos 3) → line 0, col 5
	line, col := cursorVisualPos(input, 3, 7)
	if line != 0 || col != 5 {
		t.Errorf("cursor at space: got (%d,%d), want (0,5)", line, col)
	}

	// Cursor on 'd' (pos 4) → line 1, col 0
	line, col = cursorVisualPos(input, 4, 7)
	if line != 1 || col != 0 {
		t.Errorf("cursor at 'd': got (%d,%d), want (1,0)", line, col)
	}

	// Cursor at newline boundary still works
	input2 := []rune("ab\ncd")
	line, col = cursorVisualPos(input2, 2, 80) // at '\n'
	if line != 0 || col != 4 { // prefix 2 + 2 = 4
		t.Errorf("cursor at newline: got (%d,%d), want (0,4)", line, col)
	}
}

func TestProgressBar(t *testing.T) {
	// Verify it produces non-empty output and doesn't panic
	bar := progressBar(0, 250)
	if bar == "" {
		t.Error("progressBar(0, 250) returned empty string")
	}

	bar = progressBar(125, 250)
	if bar == "" {
		t.Error("progressBar(125, 250) returned empty string")
	}

	bar = progressBar(300, 250)
	if bar == "" {
		t.Error("progressBar(300, 250) returned empty string")
	}
}

func TestBuildInputRows(t *testing.T) {
	app := &App{
		width: 40,
		input: []rune("hello"),
	}

	rows := app.buildInputRows()
	if len(rows) < 3 {
		t.Fatalf("buildInputRows() returned %d rows, want at least 3 (sep + input + sep + progress)", len(rows))
	}

	// First row should be a separator
	if !strings.HasPrefix(rows[0], "─") {
		t.Errorf("first row should be separator, got %q", rows[0])
	}

	// Second row should contain the prompt and input
	if !strings.Contains(rows[1], promptPrefix+"hello") {
		t.Errorf("second row should contain prompt + input, got %q", rows[1])
	}
}

func TestToolCallSummary(t *testing.T) {
	got := toolCallSummary("bash", []byte(`{"command":"ls -la"}`))
	if !strings.Contains(got, "~ $") || !strings.Contains(got, "ls -la") {
		t.Errorf("toolCallSummary(bash) = %q, want to contain '~ $' and 'ls -la'", got)
	}

	got = toolCallSummary("unknown_tool", nil)
	if !strings.Contains(got, "unknown_tool") {
		t.Errorf("toolCallSummary(unknown_tool) = %q, want to contain unknown_tool", got)
	}
}

func TestPadCodeBlockRow(t *testing.T) {
	tests := []struct {
		name  string
		row   string
		width int
		want  string
	}{
		{
			"pads short line",
			"\033[48;5;236m\033[38;5;248mhi\033[0m",
			10,
			"\033[48;5;236m\033[38;5;248mhi        \033[0m",
		},
		{
			"exact width no padding",
			"\033[48;5;236m\033[38;5;248m1234567890\033[0m",
			10,
			"\033[48;5;236m\033[38;5;248m1234567890\033[0m",
		},
		{
			"no trailing reset adds one",
			"\033[48;5;236m\033[38;5;248mab",
			5,
			"\033[48;5;236m\033[38;5;248mab   \033[0m",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := padCodeBlockRow(tt.row, tt.width)
			if got != tt.want {
				t.Errorf("padCodeBlockRow(%q, %d)\n  got  %q\n  want %q", tt.row, tt.width, got, tt.want)
			}
		})
	}
}

func TestVisibleWidth(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want int
	}{
		{"plain", "hello", 5},
		{"ansi", "\033[1mhello\033[0m", 5},
		{"emoji", "hi 👋", 5},
		{"empty", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := visibleWidth(tt.s); got != tt.want {
				t.Errorf("visibleWidth(%q) = %d, want %d", tt.s, got, tt.want)
			}
		})
	}
}

func TestCollapseBlankRows(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"no blanks", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"single blank preserved", []string{"a", "", "b"}, []string{"a", "", "b"}},
		{"double blank collapsed", []string{"a", "", "", "b"}, []string{"a", "", "b"}},
		{"triple blank collapsed", []string{"a", "", "", "", "b"}, []string{"a", "", "b"}},
		{"ansi-only blank collapsed", []string{"a", "", "\033[0m", "", "b"}, []string{"a", "", "b"}},
		{"leading blanks collapsed", []string{"", "", "a"}, []string{"", "a"}},
		{"trailing blanks collapsed", []string{"a", "", ""}, []string{"a", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collapseBlankRows(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("collapseBlankRows(%v) = %v (len %d), want %v (len %d)",
					tt.in, got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("row %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestCollapseToolResult(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "3 lines unchanged",
			input: "a\nb\nc",
			want:  "a\nb\nc",
		},
		{
			name:  "4 lines unchanged",
			input: "a\nb\nc\nd",
			want:  "a\nb\nc\nd",
		},
		{
			name:  "5 lines shows first 2 + last 3",
			input: "a\nb\nc\nd\ne",
			want:  "a\nb\nc\nd\ne",
		},
		{
			name:  "6 lines shows first 2 + ... + last 2",
			input: "a\nb\nc\nd\ne\nf",
			want:  "a\nb\n...\ne\nf",
		},
		{
			name:  "20 lines shows first 2 + ... + last 2",
			input: "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\n15\n16\n17\n18\n19\n20",
			want:  "1\n2\n...\n19\n20",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "single line",
			input: "only",
			want:  "only",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collapseToolResult(tt.input)
			if got != tt.want {
				t.Errorf("collapseToolResult(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRenderToolBox(t *testing.T) {
	// Helper to strip ANSI sequences for easier assertion.
	strip := func(s string) string {
		return ansiEscRe.ReplaceAllString(s, "")
	}

	t.Run("short title with content", func(t *testing.T) {
		got := strip(renderToolBox("~ glob", "file1\nfile2", 80, false, ""))
		lines := strings.Split(got, "\n")
		if len(lines) != 4 {
			t.Fatalf("expected 4 lines, got %d: %q", len(lines), got)
		}
		if !strings.HasPrefix(lines[0], "┌ ~ glob ") || !strings.HasSuffix(lines[0], "┐") {
			t.Errorf("top border wrong: %q", lines[0])
		}
		if lines[1] != "file1" {
			t.Errorf("content line 1: got %q", lines[1])
		}
		if lines[2] != "file2" {
			t.Errorf("content line 2: got %q", lines[2])
		}
		if !strings.HasPrefix(lines[3], "└") || !strings.HasSuffix(lines[3], "┘") {
			t.Errorf("bottom border wrong: %q", lines[3])
		}
		// Top and bottom borders should be same visible width.
		if visibleWidth(lines[0]) != visibleWidth(lines[3]) {
			t.Errorf("border widths differ: top=%d, bottom=%d", visibleWidth(lines[0]), visibleWidth(lines[3]))
		}
	})

	t.Run("empty content", func(t *testing.T) {
		got := strip(renderToolBox("~ bash", "", 80, false, ""))
		lines := strings.Split(got, "\n")
		if len(lines) != 2 {
			t.Fatalf("expected 2 lines (top+bottom), got %d: %q", len(lines), got)
		}
		if !strings.HasPrefix(lines[0], "┌ ~ bash ") {
			t.Errorf("top border: %q", lines[0])
		}
		if !strings.HasPrefix(lines[1], "└") {
			t.Errorf("bottom border: %q", lines[1])
		}
	})

	t.Run("long content widens box", func(t *testing.T) {
		got := strip(renderToolBox("~ x", "short\nthis-is-a-much-longer-line-than-the-title", 80, false, ""))
		lines := strings.Split(got, "\n")
		// Box should be wide enough for the long content line.
		if visibleWidth(lines[0]) < visibleWidth("this-is-a-much-longer-line-than-the-title")+2 {
			t.Errorf("box too narrow for content: %q", lines[0])
		}
	})

	t.Run("width capping", func(t *testing.T) {
		got := strip(renderToolBox("~ glob", strings.Repeat("x", 200), 40, false, ""))
		lines := strings.Split(got, "\n")
		// Top border should not exceed maxWidth.
		if visibleWidth(lines[0]) > 40 {
			t.Errorf("top border exceeds maxWidth: %d", visibleWidth(lines[0]))
		}
	})

	t.Run("narrow terminal truncates long title", func(t *testing.T) {
		// Title "~ bash -c 'very long command here'" is 35 chars, terminal is 20.
		got := strip(renderToolBox("~ bash -c 'very long command here'", "ok", 20, false, ""))
		lines := strings.Split(got, "\n")
		// All lines must fit within maxWidth.
		for i, line := range lines {
			if w := visibleWidth(line); w > 20 {
				t.Errorf("line %d exceeds maxWidth 20: width=%d %q", i, w, line)
			}
		}
		// Title should be truncated (contain ellipsis).
		if !strings.Contains(lines[0], "…") {
			t.Errorf("expected truncated title with ellipsis: %q", lines[0])
		}
		// Border widths should still match.
		if visibleWidth(lines[0]) != visibleWidth(lines[len(lines)-1]) {
			t.Errorf("border widths differ: top=%d, bottom=%d",
				visibleWidth(lines[0]), visibleWidth(lines[len(lines)-1]))
		}
	})

	t.Run("error variant uses red", func(t *testing.T) {
		got := renderToolBox("~ bash", "error!", 80, true, "")
		if !strings.Contains(got, "\033[31m") {
			t.Errorf("expected red ANSI code in error box")
		}
	})

	t.Run("non-error uses dim", func(t *testing.T) {
		got := renderToolBox("~ bash", "ok", 80, false, "")
		if !strings.Contains(got, "\033[2m") {
			t.Errorf("expected dim ANSI code in normal box")
		}
	})

	t.Run("duration in bottom border", func(t *testing.T) {
		got := strip(renderToolBox("~ glob", "file1", 80, false, "1.2s"))
		lines := strings.Split(got, "\n")
		bottom := lines[len(lines)-1]
		if !strings.HasSuffix(bottom, "1.2s ┘") {
			t.Errorf("bottom border should end with duration: %q", bottom)
		}
		if !strings.HasPrefix(bottom, "└") {
			t.Errorf("bottom border should start with └: %q", bottom)
		}
		// Top and bottom borders should be same visible width.
		if visibleWidth(lines[0]) != visibleWidth(bottom) {
			t.Errorf("border widths differ: top=%d, bottom=%d", visibleWidth(lines[0]), visibleWidth(bottom))
		}
	})

	t.Run("no duration omits label", func(t *testing.T) {
		got := strip(renderToolBox("~ bash", "ok", 80, false, ""))
		lines := strings.Split(got, "\n")
		bottom := lines[len(lines)-1]
		if strings.Contains(bottom, "s ┘") {
			t.Errorf("bottom should not have duration text: %q", bottom)
		}
	})

	t.Run("duration wider than title widens box", func(t *testing.T) {
		got := strip(renderToolBox("~ x", "y", 80, false, "2m03s"))
		lines := strings.Split(got, "\n")
		if visibleWidth(lines[0]) != visibleWidth(lines[len(lines)-1]) {
			t.Errorf("border widths differ: top=%d, bottom=%d",
				visibleWidth(lines[0]), visibleWidth(lines[len(lines)-1]))
		}
	})

	t.Run("duration in narrow box", func(t *testing.T) {
		got := strip(renderToolBox("~ x", "", 20, false, "1.5s"))
		lines := strings.Split(got, "\n")
		for i, line := range lines {
			if w := visibleWidth(line); w > 20 {
				t.Errorf("line %d exceeds maxWidth 20: width=%d %q", i, w, line)
			}
		}
		if visibleWidth(lines[0]) != visibleWidth(lines[len(lines)-1]) {
			t.Errorf("border widths differ: top=%d, bottom=%d",
				visibleWidth(lines[0]), visibleWidth(lines[len(lines)-1]))
		}
	})

	t.Run("error box with duration uses red", func(t *testing.T) {
		got := renderToolBox("~ bash", "fail", 80, true, "3.0s")
		if !strings.Contains(got, "\033[31m") {
			t.Errorf("expected red ANSI in error box with duration")
		}
		stripped := strip(got)
		lines := strings.Split(stripped, "\n")
		bottom := lines[len(lines)-1]
		if !strings.HasSuffix(bottom, "3.0s ┘") {
			t.Errorf("error box bottom should show duration: %q", bottom)
		}
	})

	t.Run("tabs replaced with spaces", func(t *testing.T) {
		got := strip(renderToolBox("~ grep", "9:\tlangdag.com/langdag v0.5.5", 80, false, ""))
		lines := strings.Split(got, "\n")
		if strings.Contains(lines[1], "\t") {
			t.Errorf("content should not contain tabs: %q", lines[1])
		}
		if lines[1] != "9: langdag.com/langdag v0.5.5" {
			t.Errorf("tab not replaced with space: got %q", lines[1])
		}
	})
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"under threshold", 200 * time.Millisecond, ""},
		{"at threshold", 499 * time.Millisecond, ""},
		{"just over 500ms", 500 * time.Millisecond, "500ms"},
		{"620ms", 620 * time.Millisecond, "620ms"},
		{"999ms", 999 * time.Millisecond, "999ms"},
		{"1 second", time.Second, "1.0s"},
		{"1.2 seconds", 1200 * time.Millisecond, "1.2s"},
		{"59.9 seconds", 59900 * time.Millisecond, "59.9s"},
		{"1 minute", time.Minute, "1m00s"},
		{"2m03s", 2*time.Minute + 3*time.Second, "2m03s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatDuration(tt.d); got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

func TestBuildBlockRows_ToolBox(t *testing.T) {
	strip := func(s string) string {
		return ansiEscRe.ReplaceAllString(s, "")
	}

	t.Run("tool call + result renders as box", func(t *testing.T) {
		app := &App{width: 80}
		app.messages = []chatMessage{
			{kind: msgToolCall, content: "~ glob", leadBlank: true},
			{kind: msgToolResult, content: "file1\nfile2"},
		}
		rows := app.buildBlockRows()
		// Find the top border row.
		var found bool
		for _, r := range rows {
			s := strip(r)
			if strings.HasPrefix(s, "┌ ~ glob ") {
				found = true
			}
		}
		if !found {
			t.Errorf("expected box top border with tool call title in rows: %v", rows)
		}
		// Should NOT have the old-style dim italic rendering without borders.
		for _, r := range rows {
			s := strip(r)
			if s == "~ glob" {
				t.Errorf("tool call should be in box border, not standalone: %v", rows)
			}
		}
	})

	t.Run("in-progress tool call renders open box", func(t *testing.T) {
		app := &App{width: 80}
		app.messages = []chatMessage{
			{kind: msgToolCall, content: "~ bash", leadBlank: true},
		}
		rows := app.buildBlockRows()
		var hasTop, hasBottom bool
		for _, r := range rows {
			s := strip(r)
			if strings.HasPrefix(s, "┌ ~ bash ") {
				hasTop = true
			}
			if strings.HasPrefix(s, "└") {
				hasBottom = true
			}
		}
		if !hasTop {
			t.Errorf("expected top border for in-progress tool call")
		}
		if hasBottom {
			t.Errorf("in-progress tool call should not have bottom border")
		}
	})

	t.Run("error tool result gets red box", func(t *testing.T) {
		app := &App{width: 80}
		app.messages = []chatMessage{
			{kind: msgToolCall, content: "~ bash", leadBlank: true},
			{kind: msgToolResult, content: "command failed", isError: true},
		}
		rows := app.buildBlockRows()
		var hasRed bool
		for _, r := range rows {
			if strings.Contains(r, "\033[31m") {
				hasRed = true
			}
		}
		if !hasRed {
			t.Errorf("error tool result should have red styling")
		}
	})

	t.Run("bash with long command", func(t *testing.T) {
		app := &App{width: 60}
		app.messages = []chatMessage{
			{kind: msgToolCall, content: "~ $ find . -name '*.go' -exec grep -l 'func main' {} +", leadBlank: true},
			{kind: msgToolResult, content: "./cmd/herm/main.go\n./cmd/debug/main.go\n./cmd/simple-chat/main.go"},
		}
		rows := app.buildBlockRows()
		for _, r := range rows {
			if w := visibleWidth(r); w > 60 {
				t.Errorf("row exceeds terminal width 60: width=%d %q", w, strip(r))
			}
		}
		// Should have top and bottom borders.
		var hasTop, hasBottom bool
		for _, r := range rows {
			s := strip(r)
			if strings.HasPrefix(s, "┌") {
				hasTop = true
			}
			if strings.HasPrefix(s, "└") {
				hasBottom = true
			}
		}
		if !hasTop || !hasBottom {
			t.Errorf("expected both borders: hasTop=%v hasBottom=%v", hasTop, hasBottom)
		}
	})

	t.Run("glob with many files truncated", func(t *testing.T) {
		// Simulate glob output with 20 files — should be collapsed to 5 lines.
		var files []string
		for i := 0; i < 20; i++ {
			files = append(files, fmt.Sprintf("src/pkg/file_%02d.go", i))
		}
		collapsed := collapseToolResult(strings.Join(files, "\n"))
		app := &App{width: 80}
		app.messages = []chatMessage{
			{kind: msgToolCall, content: "~ glob", leadBlank: true},
			{kind: msgToolResult, content: collapsed},
		}
		rows := app.buildBlockRows()
		// Count content rows (between borders).
		var contentRows int
		var inBox bool
		for _, r := range rows {
			s := strip(r)
			if strings.HasPrefix(s, "┌") {
				inBox = true
				continue
			}
			if strings.HasPrefix(s, "└") {
				inBox = false
				continue
			}
			if inBox && s != "" {
				contentRows++
			}
		}
		if contentRows > 5 {
			t.Errorf("expected at most 5 content lines, got %d", contentRows)
		}
	})

	t.Run("error bash result with multiline output", func(t *testing.T) {
		app := &App{width: 80}
		app.messages = []chatMessage{
			{kind: msgToolCall, content: "~ $ go build ./...", leadBlank: true},
			{kind: msgToolResult, content: "# herm/cmd/herm\n./main.go:42:5: undefined: foo\n./main.go:43:5: undefined: bar", isError: true},
		}
		rows := app.buildBlockRows()
		var hasRed, hasTop, hasContent bool
		for _, r := range rows {
			s := strip(r)
			if strings.Contains(r, "\033[31m") {
				hasRed = true
			}
			if strings.HasPrefix(s, "┌") {
				hasTop = true
			}
			if strings.Contains(s, "undefined:") {
				hasContent = true
			}
		}
		if !hasRed {
			t.Errorf("error result should have red styling")
		}
		if !hasTop {
			t.Errorf("expected box top border")
		}
		if !hasContent {
			t.Errorf("expected error content in output")
		}
	})
}

func TestCollectToolGroup(t *testing.T) {
	t.Run("single tool call with result", func(t *testing.T) {
		msgs := []chatMessage{
			{kind: msgToolCall, content: "~ glob", toolName: "glob"},
			{kind: msgToolResult, content: "file1\nfile2", toolName: "glob"},
		}
		g := collectToolGroup(msgs, 0)
		if len(g.entries) != 1 {
			t.Fatalf("entries = %d, want 1", len(g.entries))
		}
		if g.consumed != 2 {
			t.Errorf("consumed = %d, want 2", g.consumed)
		}
		if g.inProgress {
			t.Error("should not be in progress")
		}
		if g.entries[0].toolName != "glob" {
			t.Errorf("toolName = %q, want glob", g.entries[0].toolName)
		}
	})

	t.Run("multiple consecutive pairs", func(t *testing.T) {
		msgs := []chatMessage{
			{kind: msgToolCall, content: "~ read", toolName: "read_file"},
			{kind: msgToolResult, content: "content1"},
			{kind: msgToolCall, content: "~ read", toolName: "read_file"},
			{kind: msgToolResult, content: "content2"},
			{kind: msgToolCall, content: "~ glob", toolName: "glob"},
			{kind: msgToolResult, content: "file.go"},
		}
		g := collectToolGroup(msgs, 0)
		if len(g.entries) != 3 {
			t.Fatalf("entries = %d, want 3", len(g.entries))
		}
		if g.consumed != 6 {
			t.Errorf("consumed = %d, want 6", g.consumed)
		}
		if g.inProgress {
			t.Error("should not be in progress")
		}
	})

	t.Run("in-progress last entry", func(t *testing.T) {
		msgs := []chatMessage{
			{kind: msgToolCall, content: "~ read", toolName: "read_file"},
			{kind: msgToolResult, content: "content"},
			{kind: msgToolCall, content: "~ bash", toolName: "bash"},
		}
		g := collectToolGroup(msgs, 0)
		if len(g.entries) != 2 {
			t.Fatalf("entries = %d, want 2", len(g.entries))
		}
		if g.consumed != 3 {
			t.Errorf("consumed = %d, want 3", g.consumed)
		}
		if !g.inProgress {
			t.Error("should be in progress")
		}
		if g.entries[1].result != "" {
			t.Error("in-progress entry should have empty result")
		}
	})

	t.Run("group breaks on text message", func(t *testing.T) {
		msgs := []chatMessage{
			{kind: msgToolCall, content: "~ read", toolName: "read_file"},
			{kind: msgToolResult, content: "content"},
			{kind: msgAssistant, content: "Here's what I found"},
			{kind: msgToolCall, content: "~ edit", toolName: "edit_file"},
			{kind: msgToolResult, content: "ok"},
		}
		g := collectToolGroup(msgs, 0)
		if len(g.entries) != 1 {
			t.Errorf("entries = %d, want 1 (group should break at assistant)", len(g.entries))
		}
		if g.consumed != 2 {
			t.Errorf("consumed = %d, want 2", g.consumed)
		}
	})

	t.Run("start from middle", func(t *testing.T) {
		msgs := []chatMessage{
			{kind: msgAssistant, content: "thinking"},
			{kind: msgToolCall, content: "~ bash", toolName: "bash"},
			{kind: msgToolResult, content: "output"},
		}
		g := collectToolGroup(msgs, 1)
		if len(g.entries) != 1 {
			t.Fatalf("entries = %d, want 1", len(g.entries))
		}
		if g.consumed != 2 {
			t.Errorf("consumed = %d, want 2", g.consumed)
		}
	})
}

func TestRenderToolGroup(t *testing.T) {
	strip := func(s string) string {
		return ansiEscRe.ReplaceAllString(s, "")
	}

	t.Run("single tool with result", func(t *testing.T) {
		entries := []toolGroupEntry{
			{summary: "~ glob", toolName: "glob", result: "file1\nfile2"},
		}
		out := renderToolGroup(entries, 80, false, "")
		s := strip(out)
		if !strings.HasPrefix(s, "┌ ~ glob ") {
			t.Errorf("expected top border, got: %q", s)
		}
		if !strings.Contains(s, "└") {
			t.Error("expected bottom border")
		}
	})

	t.Run("multi-tool group has ├ entries", func(t *testing.T) {
		entries := []toolGroupEntry{
			{summary: "~ read foo.go", toolName: "read_file", result: "content"},
			{summary: "~ read bar.go", toolName: "read_file", result: "content"},
			{summary: "~ glob", toolName: "glob", result: "file.go"},
		}
		out := renderToolGroup(entries, 80, false, "")
		s := strip(out)
		if !strings.Contains(s, "┌ ~ read foo.go ") {
			t.Error("expected first entry as top border")
		}
		if !strings.Contains(s, "├ ~ read bar.go") {
			t.Error("expected second entry with ├ prefix")
		}
		if !strings.Contains(s, "├ ~ glob") {
			t.Error("expected third entry with ├ prefix")
		}
		if !strings.Contains(s, "└") {
			t.Error("expected bottom border")
		}
	})

	t.Run("overflow collapsing shows first 3 + marker + last 3", func(t *testing.T) {
		var entries []toolGroupEntry
		for i := 0; i < 10; i++ {
			entries = append(entries, toolGroupEntry{
				summary:  fmt.Sprintf("~ read file%d.go", i),
				toolName: "read_file",
				result:   fmt.Sprintf("content%d", i),
			})
		}
		out := renderToolGroup(entries, 80, false, "")
		s := strip(out)
		// First 3 should be visible.
		if !strings.Contains(s, "file0.go") {
			t.Error("expected first entry visible")
		}
		if !strings.Contains(s, "file2.go") {
			t.Error("expected third entry visible")
		}
		// Collapse marker: 10 - 6 = 4 collapsed.
		if !strings.Contains(s, "4 tool calls…") {
			t.Error("expected collapse marker with count 4")
		}
		// Last 3 should be visible.
		if !strings.Contains(s, "file7.go") {
			t.Error("expected file7 visible (third from end)")
		}
		if !strings.Contains(s, "file9.go") {
			t.Error("expected last entry visible")
		}
		// Middle entries should NOT be visible.
		if strings.Contains(s, "file3.go") {
			t.Error("file3 should be collapsed")
		}
		if strings.Contains(s, "file6.go") {
			t.Error("file6 should be collapsed")
		}
	})

	t.Run("in-progress omits bottom border", func(t *testing.T) {
		entries := []toolGroupEntry{
			{summary: "~ read foo.go", toolName: "read_file", result: "content"},
			{summary: "~ bash", toolName: "bash"},
		}
		out := renderToolGroup(entries, 80, true, "")
		s := strip(out)
		if strings.Contains(s, "└") {
			t.Error("in-progress group should not have bottom border")
		}
		if !strings.Contains(s, "├ ~ bash") {
			t.Error("expected in-progress tool as ├ entry")
		}
	})

	t.Run("in-progress with live duration", func(t *testing.T) {
		entries := []toolGroupEntry{
			{summary: "~ read foo.go", toolName: "read_file", result: "content"},
			{summary: "~ bash", toolName: "bash"},
		}
		out := renderToolGroup(entries, 80, true, "1.5s")
		s := strip(out)
		if !strings.Contains(s, "1.5s") {
			t.Error("expected live duration on in-progress entry")
		}
	})

	t.Run("error result always shown", func(t *testing.T) {
		entries := []toolGroupEntry{
			{summary: "~ read foo.go", toolName: "read_file", result: "content"},
			{summary: "~ bash", toolName: "bash", result: "command failed", isError: true},
		}
		out := renderToolGroup(entries, 80, false, "")
		s := strip(out)
		// Error result should be visible with │ prefix.
		if !strings.Contains(s, "│ command failed") {
			t.Error("error result should be shown")
		}
		// Red styling should be present.
		if !strings.Contains(out, "\033[31m") {
			t.Error("error should have red styling")
		}
	})

	t.Run("output rules: edit shown, read hidden", func(t *testing.T) {
		entries := []toolGroupEntry{
			{summary: "~ read foo.go", toolName: "read_file", result: "file content here"},
			{summary: "~ edit bar.go", toolName: "edit_file", result: "@@ -1 +1 @@\n-old\n+new"},
		}
		out := renderToolGroup(entries, 80, false, "")
		s := strip(out)
		// Read result should be hidden (summary only).
		if strings.Contains(s, "file content here") {
			t.Error("read_file result should be hidden in group")
		}
		// Edit result (diff) should be shown.
		if !strings.Contains(s, "-old") || !strings.Contains(s, "+new") {
			t.Error("edit_file diff result should be shown")
		}
	})

	t.Run("output rules: bash only for last", func(t *testing.T) {
		entries := []toolGroupEntry{
			{summary: "~ $ ls", toolName: "bash", result: "first output"},
			{summary: "~ $ pwd", toolName: "bash", result: "/home/user"},
		}
		out := renderToolGroup(entries, 80, false, "")
		s := strip(out)
		// First bash result should be hidden (not last).
		if strings.Contains(s, "first output") {
			t.Error("non-last bash result should be hidden")
		}
		// Last bash result should be shown.
		if !strings.Contains(s, "/home/user") {
			t.Error("last bash result should be shown")
		}
	})

	t.Run("6 entries no overflow", func(t *testing.T) {
		var entries []toolGroupEntry
		for i := 0; i < 6; i++ {
			entries = append(entries, toolGroupEntry{
				summary: fmt.Sprintf("~ read file%d.go", i), toolName: "read_file", result: "ok",
			})
		}
		out := renderToolGroup(entries, 80, false, "")
		s := strip(out)
		// All 6 should be visible, no collapse marker.
		for i := 0; i < 6; i++ {
			name := fmt.Sprintf("file%d.go", i)
			if !strings.Contains(s, name) {
				t.Errorf("expected %s visible (exactly 6, no overflow)", name)
			}
		}
		if strings.Contains(s, "tool calls…") {
			t.Error("6 entries should not trigger overflow")
		}
	})
}

func TestBuildBlockRows_ToolGroup(t *testing.T) {
	strip := func(s string) string {
		return ansiEscRe.ReplaceAllString(s, "")
	}

	t.Run("consecutive tools rendered as single group", func(t *testing.T) {
		app := &App{width: 80}
		app.messages = []chatMessage{
			{kind: msgToolCall, content: "~ read foo.go", toolName: "read_file", leadBlank: true},
			{kind: msgToolResult, content: "content1", toolName: "read_file"},
			{kind: msgToolCall, content: "~ read bar.go", toolName: "read_file", leadBlank: true},
			{kind: msgToolResult, content: "content2", toolName: "read_file"},
		}
		rows := app.buildBlockRows()
		// Should have exactly one ┌ (single group, not two boxes).
		topCount := 0
		for _, r := range rows {
			if strings.HasPrefix(strip(r), "┌") {
				topCount++
			}
		}
		if topCount != 1 {
			t.Errorf("expected 1 top border (single group), got %d", topCount)
		}
		// Should have ├ for second entry.
		var hasBranch bool
		for _, r := range rows {
			if strings.Contains(strip(r), "├ ~ read bar.go") {
				hasBranch = true
			}
		}
		if !hasBranch {
			t.Error("expected ├ prefix for second tool call")
		}
	})

	t.Run("group breaks on assistant text", func(t *testing.T) {
		app := &App{width: 80}
		app.messages = []chatMessage{
			{kind: msgToolCall, content: "~ read foo.go", toolName: "read_file", leadBlank: true},
			{kind: msgToolResult, content: "content", toolName: "read_file"},
			{kind: msgAssistant, content: "Here is the result"},
			{kind: msgToolCall, content: "~ edit bar.go", toolName: "edit_file", leadBlank: true},
			{kind: msgToolResult, content: "ok", toolName: "edit_file"},
		}
		rows := app.buildBlockRows()
		// Should have two ┌ borders (two separate groups).
		topCount := 0
		for _, r := range rows {
			if strings.HasPrefix(strip(r), "┌") {
				topCount++
			}
		}
		if topCount != 2 {
			t.Errorf("expected 2 top borders (separate groups), got %d", topCount)
		}
		// The assistant text should be between them.
		var hasAssistant bool
		for _, r := range rows {
			if strings.Contains(strip(r), "Here is the result") {
				hasAssistant = true
			}
		}
		if !hasAssistant {
			t.Error("expected assistant text between groups")
		}
	})

	t.Run("in-progress last tool in group", func(t *testing.T) {
		app := &App{width: 80}
		app.messages = []chatMessage{
			{kind: msgToolCall, content: "~ read foo.go", toolName: "read_file", leadBlank: true},
			{kind: msgToolResult, content: "content", toolName: "read_file"},
			{kind: msgToolCall, content: "~ bash", toolName: "bash", leadBlank: true},
		}
		rows := app.buildBlockRows()
		var hasTop, hasBottom, hasBranch bool
		for _, r := range rows {
			s := strip(r)
			if strings.HasPrefix(s, "┌") {
				hasTop = true
			}
			if strings.HasPrefix(s, "└") {
				hasBottom = true
			}
			if strings.Contains(s, "├ ~ bash") {
				hasBranch = true
			}
		}
		if !hasTop {
			t.Error("expected top border")
		}
		if hasBottom {
			t.Error("in-progress group should not have bottom border")
		}
		if !hasBranch {
			t.Error("expected ├ for in-progress tool")
		}
	})
}

func TestShouldShowToolOutput(t *testing.T) {
	tests := []struct {
		name          string
		entry         toolGroupEntry
		idx           int
		lastResultIdx int
		want          bool
	}{
		{"error always shown", toolGroupEntry{toolName: "read_file", isError: true, result: "err"}, 0, 2, true},
		{"edit always shown", toolGroupEntry{toolName: "edit_file", result: "diff"}, 0, 2, true},
		{"write always shown", toolGroupEntry{toolName: "write_file", result: "ok"}, 0, 2, true},
		{"bash last shown", toolGroupEntry{toolName: "bash", result: "output"}, 2, 2, true},
		{"bash not last hidden", toolGroupEntry{toolName: "bash", result: "output"}, 0, 2, false},
		{"git last shown", toolGroupEntry{toolName: "git", result: "ok"}, 1, 1, true},
		{"git not last hidden", toolGroupEntry{toolName: "git", result: "ok"}, 0, 1, false},
		{"read hidden", toolGroupEntry{toolName: "read_file", result: "content"}, 0, 2, false},
		{"glob hidden", toolGroupEntry{toolName: "glob", result: "files"}, 0, 2, false},
		{"grep hidden", toolGroupEntry{toolName: "grep", result: "matches"}, 0, 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldShowToolOutput(tt.entry, tt.idx, tt.lastResultIdx); got != tt.want {
				t.Errorf("shouldShowToolOutput(%q, idx=%d, last=%d) = %v, want %v",
					tt.entry.toolName, tt.idx, tt.lastResultIdx, got, tt.want)
			}
		})
	}
}

func TestIsDiffContent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"unified diff with hunk header", "--- a/file\n+++ b/file\n@@ -1,3 +1,3 @@\n context\n-old\n+new", true},
		{"hunk header at start", "@@ -1,3 +1,3 @@\n-old\n+new", true},
		{"no diff markers", "just some text\nwith multiple lines", false},
		{"empty string", "", false},
		{"partial markers only", "--- a/file\n+++ b/file\nno hunk header", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDiffContent(tt.input); got != tt.want {
				t.Errorf("isDiffContent(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestDiffLineStyle(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{"hunk header", "@@ -1,3 +1,3 @@", "\033[2;36m"},
		{"old file header", "--- a/main.go", "\033[2;1m"},
		{"new file header", "+++ b/main.go", "\033[2;1m"},
		{"added line", "+new line", "\033[2;32m"},
		{"removed line", "-old line", "\033[2;31m"},
		{"context line", " unchanged", ""},
		{"empty line", "", ""},
		{"plain text", "not a diff line", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := diffLineStyle(tt.line); got != tt.want {
				t.Errorf("diffLineStyle(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}

func TestCollapseDiff(t *testing.T) {
	t.Run("short diff unchanged", func(t *testing.T) {
		input := "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old"
		lines := strings.Split(input, "\n")
		got := collapseDiff(lines)
		if got != input {
			t.Errorf("short diff should pass through unchanged, got:\n%q", got)
		}
	})

	t.Run("20 lines fits without truncation", func(t *testing.T) {
		var lines []string
		for i := 0; i < 20; i++ {
			lines = append(lines, fmt.Sprintf("line %d", i))
		}
		got := collapseDiff(lines)
		if strings.Contains(got, "... (") {
			t.Error("20 lines should not be truncated")
		}
	})

	t.Run("21+ lines truncated", func(t *testing.T) {
		var lines []string
		for i := 0; i < 25; i++ {
			lines = append(lines, fmt.Sprintf("line %d", i))
		}
		got := collapseDiff(lines)
		if !strings.Contains(got, "... (5 more lines)") {
			t.Errorf("expected truncation marker, got:\n%q", got)
		}
		// First 20 lines should be present.
		if !strings.Contains(got, "line 19") {
			t.Error("should include up to line 19")
		}
		if strings.Contains(got, "line 20") {
			t.Error("should not include line 20")
		}
	})
}

func TestCollapseToolResultDiff(t *testing.T) {
	t.Run("short diff passes through fully", func(t *testing.T) {
		diff := "--- a/main.go\n+++ b/main.go\n@@ -1,5 +1,5 @@\n context\n-old1\n+new1\n-old2\n+new2\n more"
		result := collapseToolResult(diff)

		// 9 lines — under the 20-line limit, should pass through without truncation.
		if !strings.Contains(result, "--- a/main.go") {
			t.Error("collapsed diff should preserve file header")
		}
		if !strings.Contains(result, "+new2") {
			t.Error("collapsed diff should preserve change lines")
		}
		// Should NOT use the generic "..." truncation.
		if result == "--- a/main.go\n+++ b/main.go\n...\n+new2\n more" {
			t.Error("diff should use diff-aware collapse, not generic 2+...+2")
		}
	})

	t.Run("long diff is truncated at 20 lines", func(t *testing.T) {
		var lines []string
		lines = append(lines, "--- a/big.go", "+++ b/big.go", "@@ -1,30 +1,30 @@")
		for i := 0; i < 25; i++ {
			lines = append(lines, fmt.Sprintf("+line %d", i))
		}
		diff := strings.Join(lines, "\n")
		result := collapseToolResult(diff)

		if !strings.Contains(result, "... (") {
			t.Error("long diff should have continuation marker")
		}
		resultLines := strings.Split(result, "\n")
		if len(resultLines) > 22 { // 20 content + 1 marker line
			t.Errorf("collapsed long diff too many lines: %d", len(resultLines))
		}
	})
}

func TestToolBoxDiffColorization(t *testing.T) {
	// A diff result rendered via renderToolBox should contain ANSI color codes.
	diff := "--- a/main.go\n+++ b/main.go\n@@ -1,2 +1,2 @@\n-old\n+new"
	box := renderToolBox("~ edit main.go", diff, 80, false, "")

	// Check for diff-specific ANSI codes.
	if !strings.Contains(box, "\033[2;32m") {
		t.Error("diff box should contain green for added lines")
	}
	if !strings.Contains(box, "\033[2;31m") {
		t.Error("diff box should contain red for removed lines")
	}
	if !strings.Contains(box, "\033[2;36m") {
		t.Error("diff box should contain cyan for hunk headers")
	}
	if !strings.Contains(box, "\033[2;1m") {
		t.Error("diff box should contain dim bold for file headers")
	}
}

func TestCompactLineNumbers(t *testing.T) {
	t.Run("strips cat-n padding", func(t *testing.T) {
		input := "     1\tmodule helloworld\n     2\t\n     3\tgo 1.18"
		want := "1 module helloworld\n2 \n3 go 1.18"
		if got := compactLineNumbers(input); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("preserves non-cat-n content", func(t *testing.T) {
		input := "file1.go\nfile2.go\nfile3.go"
		if got := compactLineNumbers(input); got != input {
			t.Errorf("should not modify non-cat-n content: got %q", got)
		}
	})

	t.Run("handles large line numbers", func(t *testing.T) {
		input := "   998\tline998\n   999\tline999\n  1000\tline1000"
		want := "998 line998\n999 line999\n1000 line1000"
		if got := compactLineNumbers(input); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("collapse integrates compaction", func(t *testing.T) {
		// 6 lines of cat-n output -> collapsed to 2+...+2, all compacted.
		var lines []string
		for i := 1; i <= 6; i++ {
			lines = append(lines, fmt.Sprintf("%6d\tline%d", i, i))
		}
		got := collapseToolResult(strings.Join(lines, "\n"))
		if strings.Contains(got, "     ") {
			t.Errorf("collapsed result should not have wide padding: %q", got)
		}
		if !strings.Contains(got, "1 line1") {
			t.Errorf("expected compacted line 1: %q", got)
		}
	})
}

func TestWriteRowsEscapeSequences(t *testing.T) {
	t.Run("each row gets clear-line prefix", func(t *testing.T) {
		var buf strings.Builder
		rows := []string{"row one", "row two", "row three"}
		writeRows(&buf, rows, 1)
		output := buf.String()

		// Should start by positioning at row 1
		if !strings.HasPrefix(output, "\033[1;1H") {
			t.Errorf("expected CUP(1,1) prefix, got %q", output[:min(20, len(output))])
		}

		// Each row should be preceded by \033[0m\033[2K (reset + clear line)
		count := strings.Count(output, "\033[0m\033[2K")
		if count != 3 {
			t.Errorf("expected 3 clear-line sequences, got %d", count)
		}

		// Rows separated by \r\n
		if strings.Count(output, "\r\n") != 2 {
			t.Errorf("expected 2 \\r\\n separators between 3 rows")
		}
	})

	t.Run("custom start row", func(t *testing.T) {
		var buf strings.Builder
		writeRows(&buf, []string{"hello"}, 5)
		output := buf.String()

		if !strings.HasPrefix(output, "\033[5;1H") {
			t.Errorf("expected CUP(5,1) prefix, got %q", output[:min(20, len(output))])
		}
	})

	t.Run("empty rows no output", func(t *testing.T) {
		var buf strings.Builder
		writeRows(&buf, nil, 1)
		if buf.Len() != 0 {
			t.Errorf("expected no output for empty rows, got %q", buf.String())
		}
	})
}

func TestRenderFullClearSequence(t *testing.T) {
	// Capture stdout to verify renderFull emits the correct escape sequences.
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	app := &App{width: 40}
	app.renderFull()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	// Should contain: hide cursor + home + clear screen + clear scrollback
	if !strings.Contains(output, "\033[?25l\033[H\033[2J\033[3J") {
		t.Errorf("renderFull should emit hide-cursor + home + clear-screen + clear-scrollback sequence")
	}

	// Should contain clear-to-end-of-screen after rows
	if !strings.Contains(output, "\033[0m\033[J") {
		t.Errorf("render should emit clear-to-end-of-screen (\\033[J) after rows")
	}
}

func TestStatusLineFormats(t *testing.T) {
	strip := func(s string) string {
		return ansiEscRe.ReplaceAllString(s, "")
	}

	t.Run("running status has spinner and pipe-separated format", func(t *testing.T) {
		app := &App{
			width:              80,
			agentRunning:       true,
			agentStartTime:     time.Now().Add(-10 * time.Second),
			mainAgentToolCount: 12,
			agentDisplayInTok:  348,
			agentDisplayOutTok: 169,
		}
		rows := app.buildBlockRows()
		var found string
		for _, r := range rows {
			s := strip(r)
			if strings.Contains(s, "🛠️") && strings.Contains(s, "|") {
				found = s
				break
			}
		}
		if found == "" {
			t.Fatalf("expected running status line with tool count, got rows: %v", rows)
		}
		// Should contain braille spinner character at start.
		if !strings.ContainsAny(found, "⣾⣽⣻⢿⡿⣟⣯⣷") {
			t.Errorf("running status should have braille spinner, got: %s", found)
		}
		// Should contain pipe-separated tool count.
		if !strings.Contains(found, "| 12 🛠️ |") {
			t.Errorf("running status should have pipe-separated tool count, got: %s", found)
		}
		// Should contain token arrows.
		if !strings.Contains(found, "↑") || !strings.Contains(found, "↓") {
			t.Errorf("running status should have token counts, got: %s", found)
		}
	})

	t.Run("paused status has pause icon and pipe-separated format", func(t *testing.T) {
		app := &App{
			width:              80,
			agentRunning:       true,
			awaitingApproval:   true,
			agentStartTime:     time.Now().Add(-5 * time.Second),
			mainAgentToolCount: 7,
			agentDisplayInTok:  200,
			agentDisplayOutTok: 100,
		}
		rows := app.buildBlockRows()
		var found string
		for _, r := range rows {
			s := strip(r)
			if strings.Contains(s, "⏸") {
				found = s
				break
			}
		}
		if found == "" {
			t.Fatalf("expected paused status line, got rows: %v", rows)
		}
		if !strings.Contains(found, "| 7 🛠️ |") {
			t.Errorf("paused status should have pipe-separated tool count, got: %s", found)
		}
		if !strings.Contains(found, "↑") || !strings.Contains(found, "↓") {
			t.Errorf("paused status should have token counts, got: %s", found)
		}
	})

	t.Run("finished status has green checkmark and pipe-separated format", func(t *testing.T) {
		app := &App{
			width:                 80,
			agentElapsed:          15 * time.Second,
			mainAgentToolCount:    20,
			mainAgentInputTokens:  500,
			mainAgentOutputTokens: 250,
		}
		rows := app.buildBlockRows()
		var found string
		for _, r := range rows {
			s := strip(r)
			if strings.Contains(s, "✓") && strings.Contains(s, "🛠️") {
				found = s
				break
			}
		}
		if found == "" {
			t.Fatalf("expected finished status line, got rows: %v", rows)
		}
		if !strings.Contains(found, "✓") {
			t.Errorf("finished status should have checkmark, got: %s", found)
		}
		if !strings.Contains(found, "20 🛠️") {
			t.Errorf("finished status should show tool count, got: %s", found)
		}
		if !strings.Contains(found, "15.00s") {
			t.Errorf("finished status should show elapsed time, got: %s", found)
		}
	})

	t.Run("tool count resets on new session", func(t *testing.T) {
		app := &App{
			width:              80,
			mainAgentToolCount: 15,
			agentElapsed:       5 * time.Second,
		}
		// Simulate /new reset.
		app.mainAgentToolCount = 0
		app.agentElapsed = 0
		rows := app.buildBlockRows()
		// With agentElapsed == 0 and agentRunning == false, no status line should appear.
		for _, r := range rows {
			s := strip(r)
			if strings.Contains(s, "🛠️") {
				t.Errorf("after reset, no tool count should appear in status, got: %s", s)
			}
		}
	})
}
