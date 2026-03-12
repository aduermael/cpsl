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
