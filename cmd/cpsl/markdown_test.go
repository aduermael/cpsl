package main

import "testing"

func TestRenderInlineMarkdown(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text", "hello world", "hello world"},
		{"inline code", "run `echo hi`", "run \033[7m echo hi \033[27m"},
		{"bold", "this is **bold** text", "this is \033[1mbold\033[22m text"},
		{"italic", "this is *italic* text", "this is \033[3mitalic\033[23m text"},
		{"strikethrough", "this is ~~gone~~ text", "this is \033[9mgone\033[29m text"},
		{"bold italic", "**bold and *italic* too**", "\033[1mbold and \033[3mitalic\033[23m too\033[22m"},
		{"link", "[click](https://example.com)", "\033]8;;https://example.com\033\\\033[4mclick\033[24m\033]8;;\033\\"},
		{"bracketed link", "[[1]](https://example.com)", "\033]8;;https://example.com\033\\\033[4m[1]\033[24m\033]8;;\033\\"},
		{"no match single backtick", "it's fine", "it's fine"},
		{"no match single star", "a * b", "a * b"},
		{"preserves ansi", "\033[2mhello\033[0m", "\033[2mhello\033[0m"},
		{"code protects content", "`**not bold**`", "\033[7m **not bold** \033[27m"},
		{"bullet list no italic", "* list item", "* list item"},
		{"list with italic", "- check *this* out", "- check \033[3mthis\033[23m out"},
		{"multiple inline code", "`a` and `b`", "\033[7m a \033[27m and \033[7m b \033[27m"},
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
		{"code block line", "fmt.Println()", true, "\033[7mfmt.Println()\033[27m", true, false},
		{"closing fence", "```", true, "", false, true},
		{"h1", "# Title", false, "\033[1;4mTitle\033[0m", false, false},
		{"h2", "## Subtitle", false, "\033[1mSubtitle\033[0m", false, false},
		{"h3", "### Section", false, "\033[1mSection\033[0m", false, false},
		{"inline md outside code", "use `foo` here", false, "use \033[7m foo \033[27m here", false, false},
		{"no inline md in code block", "use `foo` here", true, "\033[7muse `foo` here\033[27m", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, state, skip := processMarkdownLine(tt.line, tt.inCodeBlock)
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
		result, inCodeBlock, skip = processMarkdownLine(line, inCodeBlock)
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
	if results[1] != "\033[7mdef hello():\033[27m" {
		t.Errorf("line 1: got %q", results[1])
	}
	if results[2] != "\033[7m    print('hi')\033[27m" {
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
