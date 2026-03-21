// markdown.go implements inline Markdown rendering for the terminal, including
// bold, italic, code spans, strikethrough, and link formatting with ANSI escapes.
package main

import (
	"strings"
	"unicode/utf8"
)

// ─── Markdown rendering ───
//
// Converts markdown syntax to ANSI terminal escape sequences.
// Uses only standard terminal attributes (bold, dim, italic, underline,
// reverse, strikethrough) and OSC 8 hyperlinks — no hardcoded colors.

// processMarkdownLine handles one line of assistant text, tracking code-block
// state across calls. Returns the styled line, updated inCodeBlock flag, and
// whether the line should be skipped (fence markers).
func processMarkdownLine(line string, inCodeBlock bool) (result string, newState bool, skip bool) {
	trimmed := strings.TrimSpace(line)

	// Code fence toggle (``` with optional language tag)
	if strings.HasPrefix(trimmed, "```") {
		return "", !inCodeBlock, true
	}

	if inCodeBlock {
		// Inside a fenced code block: reverse video (same as inline code).
		return "\033[48;5;236m\033[38;5;248m" + line + "\033[0m", true, false
	}

	// Heading lines: # H1 (bold+underline), ## H2 (bold), ### H3 (bold)
	if strings.HasPrefix(trimmed, "# ") {
		return "\033[1;4m" + trimmed[2:] + "\033[0m", false, false
	}
	if strings.HasPrefix(trimmed, "## ") {
		return "\033[1m" + trimmed[3:] + "\033[0m", false, false
	}
	if strings.HasPrefix(trimmed, "### ") {
		return "\033[1m" + trimmed[4:] + "\033[0m", false, false
	}
	if strings.HasPrefix(trimmed, "#### ") {
		return "\033[1m" + trimmed[5:] + "\033[0m", false, false
	}

	return renderInlineMarkdown(line), false, false
}

// renderInlineMarkdown converts inline markdown to ANSI sequences.
// Handles: `code`, **bold**, *italic*, ~~strikethrough~~, [text](url).
func renderInlineMarkdown(s string) string {
	var buf strings.Builder
	b := []byte(s)
	n := len(b)
	i := 0

	for i < n {
		// Skip existing ANSI escape sequences (CSI: \033[...m, OSC: \033]...\033\)
		if b[i] == '\033' {
			j := i + 1
			if j < n && b[j] == '[' {
				// CSI sequence
				j++
				for j < n && b[j] != 'm' && !isCSIFinal(b[j]) {
					j++
				}
				if j < n {
					j++ // include final byte
				}
			} else if j < n && b[j] == ']' {
				// OSC sequence — terminated by ST (\033\\)
				j++
				for j+1 < n {
					if b[j] == '\033' && b[j+1] == '\\' {
						j += 2
						break
					}
					j++
				}
			}
			buf.Write(b[i:j])
			i = j
			continue
		}

		// Inline code: `code`
		if b[i] == '`' {
			end := indexByte(b, '`', i+1)
			if end > i+1 {
				code := string(b[i+1 : end])
				buf.WriteString("\033[7m")
				buf.WriteString(code)
				buf.WriteString("\033[27m")
				i = end + 1
				continue
			}
		}

		// Bold: **text**
		if i+1 < n && b[i] == '*' && b[i+1] == '*' {
			end := indexDouble(b, '*', i+2)
			if end > i+2 {
				content := renderInlineMarkdown(string(b[i+2 : end]))
				buf.WriteString("\033[1m")
				buf.WriteString(content)
				buf.WriteString("\033[22m")
				i = end + 2
				continue
			}
		}

		// Italic: *text* (single asterisk, not part of **)
		if b[i] == '*' && (i == 0 || b[i-1] != '*') && i+1 < n && b[i+1] != '*' && b[i+1] != ' ' {
			end := indexSingleStar(b, i+1)
			if end > i+1 {
				content := renderInlineMarkdown(string(b[i+1 : end]))
				buf.WriteString("\033[3m")
				buf.WriteString(content)
				buf.WriteString("\033[23m")
				i = end + 1
				continue
			}
		}

		// Strikethrough: ~~text~~
		if i+1 < n && b[i] == '~' && b[i+1] == '~' {
			end := indexDouble(b, '~', i+2)
			if end > i+2 {
				content := renderInlineMarkdown(string(b[i+2 : end]))
				buf.WriteString("\033[9m")
				buf.WriteString(content)
				buf.WriteString("\033[29m")
				i = end + 2
				continue
			}
		}

		// Link: [text](url) — search for ]( to support brackets in text like [[1]](url)
		if b[i] == '[' {
			closeBracket := indexPair(b, ']', '(', i+1)
			if closeBracket > i+1 {
				closeParen := indexByte(b, ')', closeBracket+2)
				if closeParen > closeBracket+2 {
					text := string(b[i+1 : closeBracket])
					url := string(b[closeBracket+2 : closeParen])
					// OSC 8 hyperlink
					buf.WriteString("\033]8;;")
					buf.WriteString(url)
					buf.WriteString("\033\\")
					buf.WriteString("\033[4m")
					buf.WriteString(text)
					buf.WriteString("\033[24m")
					buf.WriteString("\033]8;;\033\\")
					i = closeParen + 1
					continue
				}
			}
		}

		// Default: emit rune as-is
		r, size := utf8.DecodeRune(b[i:])
		buf.WriteRune(r)
		i += size
	}

	return buf.String()
}

// ─── helpers ───

func isCSIFinal(c byte) bool {
	return c >= 0x40 && c <= 0x7E // @ through ~
}

func indexByte(b []byte, target byte, start int) int {
	for i := start; i < len(b); i++ {
		if b[i] == target {
			return i
		}
	}
	return -1
}

// indexPair finds position of a followed by b (e.g. ']' '(' for link delimiter).
func indexPair(buf []byte, a, b byte, start int) int {
	for i := start; i+1 < len(buf); i++ {
		if buf[i] == a && buf[i+1] == b {
			return i
		}
	}
	return -1
}

// indexDouble finds the position of two consecutive target bytes.
func indexDouble(b []byte, target byte, start int) int {
	for i := start; i+1 < len(b); i++ {
		if b[i] == target && b[i+1] == target {
			return i
		}
	}
	return -1
}

// indexSingleStar finds a single * that isn't part of **.
func indexSingleStar(b []byte, start int) int {
	for i := start; i < len(b); i++ {
		if b[i] == '*' {
			// Not part of **
			if i+1 >= len(b) || b[i+1] != '*' {
				return i
			}
			// Skip the ** pair
			i++
		}
	}
	return -1
}
