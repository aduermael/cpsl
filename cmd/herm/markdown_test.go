package main

import "testing"

func TestRenderInlineMarkdown(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text", "hello world", "hello world"},
		{"inline code", "run `echo hi`", "run \033[7mecho hi\033[27m"},
		{"bold", "this is **bold** text", "this is \033[1mbold\033[22m text"},
		{"italic", "this is *italic* text", "this is \033[3mitalic\033[23m text"},
		{"strikethrough", "this is ~~gone~~ text", "this is \033[9mgone\033[29m text"},
		{"bold italic", "**bold and *italic* too**", "\033[1mbold and \033[3mitalic\033[23m too\033[22m"},
		{"link", "[click](https://example.com)", "\033]8;;https://example.com\033\\\033[4mclick\033[24m\033]8;;\033\\"},
		{"bracketed link", "[[1]](https://example.com)", "\033]8;;https://example.com\033\\\033[4m[1]\033[24m\033]8;;\033\\"},
		{"no match single backtick", "it's fine", "it's fine"},
		{"no match single star", "a * b", "a * b"},
		{"preserves ansi", "\033[2mhello\033[0m", "\033[2mhello\033[0m"},
		{"code protects content", "`**not bold**`", "\033[7m**not bold**\033[27m"},
		{"bullet list no italic", "* list item", "* list item"},
		{"list with italic", "- check *this* out", "- check \033[3mthis\033[23m out"},
		{"multiple inline code", "`a` and `b`", "\033[7ma\033[27m and \033[7mb\033[27m"},
		{"empty bold", "****", "****"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderInlineMarkdown(tt.in)
			if got != tt.want {
				t.Errorf("renderInlineMarkdown(%q)\n  got  %q\n  want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestProcessMarkdownLine(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		inCodeBlock bool
		wantResult  string
		wantState   bool
		wantSkip    bool
	}{
		{"plain text", "hello", false, "hello", false, false},
		{"opening fence", "```go", false, "", true, true},
		{"code block line", "fmt.Println()", true, "\033[48;5;236m\033[38;5;248mfmt.Println()\033[0m", true, false},
		{"closing fence", "```", true, "", false, true},
		{"h1", "# Title", false, "\033[1;4mTitle\033[0m", false, false},
		{"h2", "## Subtitle", false, "\033[1mSubtitle\033[0m", false, false},
		{"h3", "### Section", false, "\033[1mSection\033[0m", false, false},
		{"inline md outside code", "use `foo` here", false, "use \033[7mfoo\033[27m here", false, false},
		{"no inline md in code block", "use `foo` here", true, "\033[48;5;236m\033[38;5;248muse `foo` here\033[0m", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, state, skip := processMarkdownLine(processMarkdownLineOptions{line: tt.line, inCodeBlock: tt.inCodeBlock})
			if result != tt.wantResult {
				t.Errorf("result: got %q, want %q", result, tt.wantResult)
			}
			if state != tt.wantState {
				t.Errorf("inCodeBlock: got %v, want %v", state, tt.wantState)
			}
			if skip != tt.wantSkip {
				t.Errorf("skip: got %v, want %v", skip, tt.wantSkip)
			}
		})
	}
}

func TestCodeBlockAcrossMessages(t *testing.T) {
	// Simulate multiple assistant messages forming a code block
	lines := []string{
		"Here is some code:",
		"```python",
		"def hello():",
		"    print('hi')",
		"```",
		"That was the code.",
	}

	inCodeBlock := false
	var results []string
	for _, line := range lines {
		var result string
		var skip bool
		result, inCodeBlock, skip = processMarkdownLine(processMarkdownLineOptions{line: line, inCodeBlock: inCodeBlock})
		if !skip {
			results = append(results, result)
		}
	}

	if len(results) != 4 {
		t.Fatalf("expected 4 visible lines, got %d: %v", len(results), results)
	}
	// First line should have inline markdown applied
	if results[0] != "Here is some code:" {
		t.Errorf("line 0: got %q", results[0])
	}
	// Code lines should be dim
	if results[1] != "\033[48;5;236m\033[38;5;248mdef hello():\033[0m" {
		t.Errorf("line 1: got %q", results[1])
	}
	if results[2] != "\033[48;5;236m\033[38;5;248m    print('hi')\033[0m" {
		t.Errorf("line 2: got %q", results[2])
	}
	// After closing fence, normal rendering resumes
	if results[3] != "That was the code." {
		t.Errorf("line 3: got %q", results[3])
	}
	// State should be back to false
	if inCodeBlock {
		t.Error("should not be in code block after closing fence")
	}
}

func TestRenderInlineMarkdownEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			"bold inside italic",
			"*italic and **bold** inside*",
			"\033[3mitalic and \033[1mbold\033[22m inside\033[23m",
		},
		{
			"unclosed bold",
			"**unclosed bold",
			"**unclosed bold",
		},
		{
			"unclosed italic",
			"*unclosed italic",
			"*unclosed italic",
		},
		{
			"unclosed backtick",
			"`unclosed code",
			"`unclosed code",
		},
		{
			"empty code two backticks",
			"``",
			"``",
		},
		{
			"empty strikethrough",
			"~~~~",
			"~~~~",
		},
		{
			"multiple bold sections",
			"**a** then **b**",
			"\033[1ma\033[22m then \033[1mb\033[22m",
		},
		{
			"strikethrough with bold inside",
			"~~**bold** strike~~",
			"\033[9m\033[1mbold\033[22m strike\033[29m",
		},
		{
			"unicode content in bold",
			"**héllo wörld**",
			"\033[1mhéllo wörld\033[22m",
		},
		{
			// After **bold** is consumed, i lands on the third * whose predecessor
			// is also *, so the italic guard fires and *italic* is emitted literally.
			"adjacent bold then italic",
			"**bold***italic*",
			"\033[1mbold\033[22m*italic*",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderInlineMarkdown(tt.in)
			if got != tt.want {
				t.Errorf("renderInlineMarkdown(%q)\n  got  %q\n  want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestProcessMarkdownLineEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		inCodeBlock bool
		wantResult  string
		wantState   bool
		wantSkip    bool
	}{
		{
			"h4 heading",
			"#### Fourth",
			false,
			"\033[1mFourth\033[0m",
			false,
			false,
		},
		{
			// trimmed prefix is "```python" which starts with "```", so it toggles
			"code fence with language tag and extra spaces",
			"   ```python   ",
			false,
			"",
			true,
			true,
		},
		{
			// empty line inside code block still gets code-block styling
			"empty line in code block",
			"",
			true,
			"\033[48;5;236m\033[38;5;248m\033[0m",
			true,
			false,
		},
		{
			// heading syntax inside a code block is treated as code, not a heading
			"heading inside code block",
			"# Not a heading",
			true,
			"\033[48;5;236m\033[38;5;248m# Not a heading\033[0m",
			true,
			false,
		},
		{
			// whitespace-only line outside a code block falls through to renderInlineMarkdown
			// which returns it unchanged
			"whitespace only line",
			"   ",
			false,
			"   ",
			false,
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, state, skip := processMarkdownLine(processMarkdownLineOptions{line: tt.line, inCodeBlock: tt.inCodeBlock})
			if result != tt.wantResult {
				t.Errorf("result: got %q, want %q", result, tt.wantResult)
			}
			if state != tt.wantState {
				t.Errorf("inCodeBlock: got %v, want %v", state, tt.wantState)
			}
			if skip != tt.wantSkip {
				t.Errorf("skip: got %v, want %v", skip, tt.wantSkip)
			}
		})
	}
}

func TestMultiCodeBlockSequence(t *testing.T) {
	// Two successive fenced code blocks separated by plain text.
	// Verifies that state resets properly after each closing fence.
	lines := []struct {
		text string
	}{
		{"before code"},
		{"```go"},
		{"x := 1"},
		{"```"},
		{"between"},
		{"```python"},
		{"y = 2"},
		{"```"},
		{"after code"},
	}

	type lineResult struct {
		result string
		state  bool
		skip   bool
	}

	inCodeBlock := false
	var got []lineResult
	for _, l := range lines {
		r, s, sk := processMarkdownLine(processMarkdownLineOptions{line: l.text, inCodeBlock: inCodeBlock})
		got = append(got, lineResult{r, s, sk})
		inCodeBlock = s
	}

	// fence lines: indices 1, 3, 5, 7 — all skip=true, result=""
	for _, idx := range []int{1, 3, 5, 7} {
		if !got[idx].skip {
			t.Errorf("line %d: expected skip=true", idx)
		}
		if got[idx].result != "" {
			t.Errorf("line %d: expected empty result, got %q", idx, got[idx].result)
		}
	}

	// plain text lines outside code blocks: indices 0, 4, 8
	for _, idx := range []int{0, 4, 8} {
		if got[idx].skip {
			t.Errorf("line %d: expected skip=false", idx)
		}
		if got[idx].state {
			t.Errorf("line %d: expected inCodeBlock=false after plain text", idx)
		}
	}

	// code content lines: indices 2 and 6
	wantCodeLine := func(raw string) string {
		return "\033[48;5;236m\033[38;5;248m" + raw + "\033[0m"
	}
	if got[2].result != wantCodeLine("x := 1") {
		t.Errorf("line 2: got %q, want %q", got[2].result, wantCodeLine("x := 1"))
	}
	if got[6].result != wantCodeLine("y = 2") {
		t.Errorf("line 6: got %q, want %q", got[6].result, wantCodeLine("y = 2"))
	}

	// state after opening fences (indices 1, 5): inCodeBlock=true
	if !got[1].state {
		t.Error("line 1 (opening go fence): expected state=true")
	}
	if !got[5].state {
		t.Error("line 5 (opening python fence): expected state=true")
	}

	// state after closing fences (indices 3, 7): inCodeBlock=false
	if got[3].state {
		t.Error("line 3 (closing go fence): expected state=false")
	}
	if got[7].state {
		t.Error("line 7 (closing python fence): expected state=false")
	}

	// final state is false
	if inCodeBlock {
		t.Error("final inCodeBlock should be false")
	}
}

func TestRenderInlineMarkdownANSIPreservation(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			// A complete OSC 8 hyperlink already in the input should pass through untouched.
			"osc8 hyperlink passthrough",
			"\033]8;;http://x\033\\ link \033]8;;\033\\",
			"\033]8;;http://x\033\\ link \033]8;;\033\\",
		},
		{
			// CSI sequence with a non-'m' final byte (J = erase in display) must be preserved.
			"csi non-m final byte",
			"\033[2J",
			"\033[2J",
		},
		{
			// Existing ANSI sequences should be preserved verbatim while subsequent
			// markdown (** bold **) is still rendered normally.
			"ansi preserved with markdown after",
			"\033[1mhi\033[0m **bold**",
			"\033[1mhi\033[0m \033[1mbold\033[22m",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderInlineMarkdown(tt.in)
			if got != tt.want {
				t.Errorf("renderInlineMarkdown(%q)\n  got  %q\n  want %q", tt.in, got, tt.want)
			}
		})
	}
}
