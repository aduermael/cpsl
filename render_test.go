package main

import (
	"strings"
	"testing"
)

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
		{"wrap", "abcdefgh", 5, 3}, // promptPrefixCols=2, first line fits 3, then 5, then remainder
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runes := []rune(tt.input)
			got := getVisualLines(runes, len(runes), tt.width)
			if len(got) != tt.want {
				t.Errorf("getVisualLines(%q, w=%d) = %d lines, want %d",
					tt.input, tt.width, len(got), tt.want)
			}
		})
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
	if !strings.Contains(got, "bash") || !strings.Contains(got, "ls -la") {
		t.Errorf("toolCallSummary(bash) = %q, want to contain bash and ls -la", got)
	}

	got = toolCallSummary("unknown_tool", nil)
	if !strings.Contains(got, "unknown_tool") {
		t.Errorf("toolCallSummary(unknown_tool) = %q, want to contain unknown_tool", got)
	}
}

func TestCollapseToolResult(t *testing.T) {
	short := "line1\nline2\nline3"
	if collapseToolResult(short) != short {
		t.Errorf("collapseToolResult should not change short results")
	}

	var lines []string
	for i := range 20 {
		lines = append(lines, strings.Repeat("x", i+1))
	}
	long := strings.Join(lines, "\n")
	collapsed := collapseToolResult(long)
	if !strings.Contains(collapsed, "lines omitted") {
		t.Errorf("collapseToolResult should collapse long results, got %q", collapsed)
	}
}
