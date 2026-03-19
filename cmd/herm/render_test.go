package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
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
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "short diff unchanged",
			input: "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old",
			want:  "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old",
		},
		{
			name:  "long diff collapsed",
			input: "--- a/f\n+++ b/f\n@@ -1,5 +1,5 @@\n context\n-old1\n+new1\n-old2\n+new2\n more context",
			want:  "--- a/f\n+++ b/f\n@@ -1,5 +1,5 @@\n context\n... (5 more lines)",
		},
		{
			name:  "exactly 4 lines",
			input: "--- a/f\n+++ b/f\n@@ -1 +1 @@\n+added",
			want:  "--- a/f\n+++ b/f\n@@ -1 +1 @@\n+added",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := strings.Split(tt.input, "\n")
			got := collapseDiff(lines)
			if got != tt.want {
				t.Errorf("collapseDiff() =\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}

func TestCollapseToolResultDiff(t *testing.T) {
	// A unified diff with >5 lines should use collapseDiff (not the generic 2+...+2).
	diff := "--- a/main.go\n+++ b/main.go\n@@ -1,5 +1,5 @@\n context\n-old1\n+new1\n-old2\n+new2\n more"
	result := collapseToolResult(diff)

	// Should use diff-style collapse: header + "... (N more lines)".
	if !strings.Contains(result, "--- a/main.go") {
		t.Error("collapsed diff should preserve file header")
	}
	if !strings.Contains(result, "... (") {
		t.Error("collapsed diff should have diff-style continuation marker")
	}
	// Should NOT use the generic "..." marker (no leading/trailing lines format).
	lines := strings.Split(result, "\n")
	if len(lines) > 6 {
		t.Errorf("collapsed diff too long: %d lines", len(lines))
	}
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
