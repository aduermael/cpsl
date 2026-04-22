// style.go provides ANSI color helpers, text styling functions, message
// rendering, the tool-result box renderer, the logo builder, and the
// animated progress/gradient effects used by the herm TUI.
package main

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// ─── Progress bar (from simple-chat) ───

type progressBarOptions struct {
	n, max int
}

func progressBar(opts progressBarOptions) string {
	n, max := opts.n, opts.max
	if n > max {
		n = max
	}
	ratio := float64(n) / float64(max)
	filled := int(ratio * 24)
	partials := []rune("█▉▊▋▌▍▎▏")

	// Lerp green→red gradient based on fill ratio.
	lerp := func(a, b int) int { return a + int(float64(b-a)*ratio) }
	r, g, b := lerp(78, 230), lerp(201, 70), lerp(100, 70)
	fillFg := fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b)
	dimBg := "\033[48;5;240m"

	const reset = "\033[0m"

	var buf strings.Builder
	for i := range 3 {
		cellFilled := filled - i*8
		switch {
		case cellFilled >= 8:
			buf.WriteString(dimBg + fillFg + "█")
		case cellFilled <= 0:
			buf.WriteString(dimBg + " ")
		default:
			buf.WriteString(dimBg + fillFg + string(partials[8-cellFilled]))
		}
	}
	buf.WriteString(reset)
	return buf.String()
}

// ─── ANSI rendering helpers (from simple-chat) ───

type writeRowsOptions struct {
	buf  *strings.Builder
	rows []string
	from int
}

func writeRows(opts writeRowsOptions) {
	if len(opts.rows) == 0 {
		return
	}
	opts.buf.WriteString(fmt.Sprintf("\033[%d;1H", opts.from))
	for i, row := range opts.rows {
		if i > 0 {
			opts.buf.WriteString("\r\n")
		}
		opts.buf.WriteString("\033[0m\033[2K")
		opts.buf.WriteString(row)
	}
}

// ─── Logo ───

// buildLogo returns the colored logo lines.
// Shell body uses two warm sand tones, eyes are black,
// and the interior (face area) has a grey-blue background.
// HERM acronym rendered as 2-row block art with cyan→pink gradient.
func buildLogo(width int) []string {
	shA := "\033[38;5;180m" // shell body (warm sand)
	shB := "\033[38;5;223m" // shell highlight (lighter peach)
	ib := "\033[48;5;60m"   // interior bg (grey-blue)
	ey := "\033[38;5;232m"  // black eyes
	tb := "\033[48;5;60m"   // tentacle bg
	r := "\033[0m"
	d := "\033[2m" // dim (tagline)
	// HERM gradient: cyan → blue-purple → purple → hot pink
	cH := "\033[1;38;2;0;212;255m"
	cE := "\033[1;38;2;85;140;230m"
	cR := "\033[1;38;2;170;68;200m"
	cM := "\033[1;38;2;255;20;147m"
	prefix := " " + shA + "▀" + shB + "██" + shA + "█" + tb + shA + "▌▌▌" + r + shA + "█" + r + "  " + d
	tagline := "Helpful Encapsulated Reasoning Machine"
	prefixWidth := visibleWidth(prefix)
	available := width - prefixWidth
	if available < len(tagline) && available > 1 {
		tagline = tagline[:available-1] + "…"
	} else if available <= 1 {
		tagline = ""
	}
	return []string{
		"",
		"    " + shA + "▄" + shB + "███" + shA + "▄" + r + "  " + cH + "█ █" + r + " " + cE + "█▀▀" + r + " " + cR + "█▀█" + r + " " + cM + "█▄ ▄█" + r,
		"  " + shA + "▄██" + ib + ey + "• •" + r + shA + "█" + r + "  " + cH + "█▀█" + r + " " + cE + "██▄" + r + " " + cR + "█▀▄" + r + " " + cM + "█ ▀ █" + r,
		prefix + tagline + r,
		"",
	}
}

// ─── Styling helpers ───

func styledUserMsg(content string) string {
	// Style each line individually so \n splits in buildBlockRows preserve it.
	lines := strings.Split(renderInlineMarkdown(content), "\n")
	lines[0] = "\033[1m▸ " + lines[0] + "\033[0m"
	for i := 1; i < len(lines); i++ {
		lines[i] = "\033[1m" + lines[i] + "\033[0m"
	}
	return strings.Join(lines, "\n")
}

func styledAssistantText(content string) string {
	return content
}

func styledToolCall(summary string) string {
	return "\033[2;3m" + summary + "\033[0m"
}

type styledToolResultOptions struct {
	result  string
	isError bool
}

func styledToolResult(opts styledToolResultOptions) string {
	if opts.isError {
		return styledError(opts.result)
	}
	return "\033[2m" + opts.result + "\033[0m"
}

func styledError(msg string) string {
	return "\033[31;3m" + msg + "\033[0m"
}

func styledSuccess(msg string) string {
	return "\033[32;3m" + msg + "\033[0m"
}

func styledInfo(msg string) string {
	return "\033[34;3m" + msg + "\033[0m"
}

func styledSystemPrompt(msg string) string {
	// dim italic — same style as tool calls / thinking indicator.
	// Style each line individually so \n splits in buildBlockRows preserve it.
	lines := strings.Split(msg, "\n")
	for i, line := range lines {
		lines[i] = "\033[2;3m" + line + "\033[0m"
	}
	return strings.Join(lines, "\n")
}

func renderMessage(msg chatMessage) string {
	var parts []string
	if msg.leadBlank {
		parts = append(parts, "")
	}
	// Strip carriage returns to prevent terminal cursor jumps that garble output.
	content := strings.ReplaceAll(msg.content, "\r", "")
	var rendered string
	switch msg.kind {
	case msgUser:
		rendered = styledUserMsg(content)
	case msgAssistant:
		rendered = styledAssistantText(content)
	case msgToolCall:
		rendered = styledToolCall(content)
	case msgToolResult:
		rendered = styledToolResult(styledToolResultOptions{result: content, isError: msg.isError})
	case msgInfo:
		rendered = styledInfo(content)
	case msgSystemPrompt:
		rendered = styledSystemPrompt(content)
	case msgSuccess:
		rendered = styledSuccess(content)
	case msgError:
		rendered = styledError(content)
	}
	parts = append(parts, rendered)
	return strings.Join(parts, "\n")
}

type renderToolBoxOptions struct {
	title       string
	content     string
	maxWidth    int
	isError     bool
	durationStr string
}

// renderToolBox renders a tool call and its result as a bordered box:
//
//	┌ ~ glob ───────┐
//	file1.go
//	file2.go
//	└───────────────┘
//
// The box has top/bottom borders but no side borders. The entire output is
// styled dim (or red for errors). Title uses dim+italic.
func renderToolBox(opts renderToolBoxOptions) string {
	title, maxWidth, isError, durationStr := opts.title, opts.maxWidth, opts.isError, opts.durationStr
	// Replace tabs with single spaces for compact, predictable display,
	// and trim trailing newlines to avoid blank lines inside the box.
	content := strings.TrimRight(strings.ReplaceAll(opts.content, "\t", " "), "\n")
	// Compute inner width from title and content lines.
	titleVW := visibleWidth(title)
	innerWidth := titleVW + 2 // "┌ " + title + " " + pad + "┐" → need at least title + 2 spaces
	if content != "" {
		for _, line := range strings.Split(content, "\n") {
			if lw := visibleWidth(line); lw > innerWidth {
				innerWidth = lw
			}
		}
	}
	// Ensure inner width is wide enough for the duration label if present.
	// Bottom border: └─── duration ┘ → needs len(duration) + 2 (spaces around it).
	if durationStr != "" {
		if minW := visibleWidth(durationStr) + 2; minW > innerWidth {
			innerWidth = minW
		}
	}
	// Cap at maxWidth minus 2 for corner characters (┌/┐ are each 1 wide).
	if maxWidth > 0 && innerWidth > maxWidth-2 {
		innerWidth = maxWidth - 2
	}
	// Truncate title if it doesn't fit within the capped inner width.
	// The top border is "┌ title ─┐", so title needs innerWidth - 2 visible chars.
	if maxTitleVW := innerWidth - 2; titleVW > maxTitleVW && maxTitleVW >= 0 {
		title = truncateWithEllipsis(truncateWithEllipsisOptions{s: title, maxLen: maxTitleVW})
		titleVW = visibleWidth(title)
	}

	// Pick ANSI style for borders vs content.
	var borderStyle, titleStyle, contentStyle, reset string
	if isError {
		borderStyle = "\033[31m"   // red
		titleStyle = "\033[31;3m"  // red italic
		contentStyle = "\033[31m"  // red
		reset = "\033[0m"
	} else {
		borderStyle = "\033[2m"    // dim
		titleStyle = "\033[2;3m"   // dim italic
		contentStyle = "\033[2m"   // dim
		reset = "\033[0m"
	}

	var b strings.Builder

	// Top border: ┌ title ─...─┐
	pad := innerWidth - titleVW - 2 // spaces taken by " title "
	if pad < 0 {
		pad = 0
	}
	b.WriteString(borderStyle)
	b.WriteString("┌ ")
	b.WriteString(reset)
	b.WriteString(titleStyle)
	b.WriteString(title)
	b.WriteString(reset)
	b.WriteString(borderStyle)
	b.WriteByte(' ')
	b.WriteString(strings.Repeat("─", pad))
	b.WriteString("┐")
	b.WriteString(reset)

	// Content lines (no side borders).
	if content != "" {
		isDiff := isDiffContent(content)
		for _, line := range strings.Split(content, "\n") {
			b.WriteByte('\n')
			lineStyle := contentStyle
			if isDiff {
				if ds := diffLineStyle(line); ds != "" {
					lineStyle = ds
				}
			}
			b.WriteString(lineStyle)
			if visibleWidth(line) > innerWidth {
				line = truncateVisual(truncateVisualOptions{s: line, maxCols: innerWidth})
			}
			b.WriteString(line)
			b.WriteString(reset)
		}
	}

	// Bottom border: └─...─┘ or └─...─ 1.2s ┘
	b.WriteByte('\n')
	b.WriteString(borderStyle)
	b.WriteString("└")
	if durationStr != "" {
		durPad := innerWidth - visibleWidth(durationStr) - 2 // " duration "
		if durPad < 0 {
			durPad = 0
		}
		b.WriteString(strings.Repeat("─", durPad))
		b.WriteByte(' ')
		b.WriteString(reset)
		b.WriteString(titleStyle)
		b.WriteString(durationStr)
		b.WriteString(reset)
		b.WriteString(borderStyle)
		b.WriteString(" ┘")
	} else {
		b.WriteString(strings.Repeat("─", innerWidth))
		b.WriteString("┘")
	}
	b.WriteString(reset)

	return b.String()
}

type renderToolGroupOptions struct {
	entries    []toolGroupEntry
	maxWidth   int
	inProgress bool
	liveDur    string
}

// renderToolGroup renders a group of tool calls as a single bordered block:
//
//	┌ Read file (README.md) ────────────────────┐
//	├ Read file (foo.txt)
//	├ ~ git log --oneline
//	│ d26138d commit message
//	└───────────────────────────────────────────┘
//
// The first entry gets the ┌…┐ top border. Subsequent entries use ├ prefix.
// Tool output lines use │ prefix. Bottom border is omitted when inProgress is true.
// When inProgress and liveDur is non-empty, the duration is shown on the last ├ line.
func renderToolGroup(opts renderToolGroupOptions) string {
	entries, maxWidth, inProgress, liveDur := opts.entries, opts.maxWidth, opts.inProgress, opts.liveDur
	const (
		borderStyle     = "\033[2m"    // dim
		titleStyle      = "\033[2;3m"  // dim italic
		contentStyle    = "\033[2m"    // dim
		errTitleStyle   = "\033[31;3m" // red italic
		errContentStyle = "\033[31m"   // red
		reset           = "\033[0m"
	)

	// Overflow collapsing: when >6 entries, show first 3 + marker + last 3.
	collapsedCount := 0
	showFirst := len(entries)
	showLast := 0
	if len(entries) > toolGroupOverflowThreshold {
		showFirst = toolGroupShowEdge
		showLast = toolGroupShowEdge
		collapsedCount = len(entries) - showFirst - showLast
	}

	// Find the last entry with a result (for bash/git output rule).
	lastResultIdx := -1
	for j := len(entries) - 1; j >= 0; j-- {
		if entries[j].result != "" {
			lastResultIdx = j
			break
		}
	}

	// Compute inner width from visible summaries and shown content.
	innerWidth := 0
	for j, entry := range entries {
		if vw := visibleWidth(entry.summary) + 2; vw > innerWidth {
			innerWidth = vw
		}
		if entry.result != "" && shouldShowToolOutput(shouldShowToolOutputOptions{entry: entry, idx: j, lastResultIdx: lastResultIdx}) {
			for _, line := range strings.Split(strings.TrimRight(strings.ReplaceAll(entry.result, "\t", " "), "\n"), "\n") {
				if lw := visibleWidth(line); lw > innerWidth {
					innerWidth = lw
				}
			}
		}
	}
	if maxWidth > 0 && innerWidth > maxWidth-2 {
		innerWidth = maxWidth - 2
	}

	var b strings.Builder

	for j, entry := range entries {
		// Emit collapse marker at the boundary.
		if collapsedCount > 0 && j == showFirst {
			b.WriteByte('\n')
			marker := fmt.Sprintf("%d tool calls… 🛠️", collapsedCount)
			b.WriteString(borderStyle + "├ " + reset + titleStyle + marker + reset)
		}
		// Skip collapsed entries.
		if collapsedCount > 0 && j >= showFirst && j < len(entries)-showLast {
			continue
		}

		ts, cs := titleStyle, contentStyle
		if entry.isError {
			ts, cs = errTitleStyle, errContentStyle
		}

		summary := entry.summary

		if j == 0 {
			// Top border: ┌ summary ─────┐
			if maxTitleVW := innerWidth - 2; visibleWidth(summary) > maxTitleVW && maxTitleVW >= 0 {
				summary = truncateWithEllipsis(truncateWithEllipsisOptions{s: summary, maxLen: maxTitleVW})
			}
			pad := innerWidth - visibleWidth(summary) - 2
			if pad < 0 {
				pad = 0
			}
			b.WriteString(borderStyle + "┌ " + reset)
			b.WriteString(ts + summary + reset)
			b.WriteString(borderStyle + " " + strings.Repeat("─", pad) + "┐" + reset)
		} else {
			b.WriteByte('\n')
			if visibleWidth(summary) > innerWidth {
				summary = truncateWithEllipsis(truncateWithEllipsisOptions{s: summary, maxLen: innerWidth})
			}
			// Show live duration on the last ├ line when in-progress.
			isLast := j == len(entries)-1
			if isLast && inProgress && liveDur != "" {
				b.WriteString(borderStyle + "├ " + reset)
				b.WriteString(ts + summary + reset)
				b.WriteString(" " + titleStyle + liveDur + reset)
			} else {
				b.WriteString(borderStyle + "├ " + reset)
				b.WriteString(ts + summary + reset)
			}
		}

		// Content lines with │ prefix — filtered by tool output rules.
		// Rules: errors always shown, edit/write show diff output,
		// bash/git show output only for the last result-bearing tool,
		// read/glob/grep/other results are hidden (summary is enough).
		if entry.result != "" && shouldShowToolOutput(shouldShowToolOutputOptions{entry: entry, idx: j, lastResultIdx: lastResultIdx}) {
			content := strings.TrimRight(strings.ReplaceAll(entry.result, "\t", " "), "\n")
			isDiff := isDiffContent(content)
			for _, line := range strings.Split(content, "\n") {
				b.WriteByte('\n')
				ls := cs
				if isDiff {
					if ds := diffLineStyle(line); ds != "" {
						ls = ds
					}
				}
				if visibleWidth(line) > innerWidth {
					line = truncateVisual(truncateVisualOptions{s: line, maxCols: innerWidth})
				}
				b.WriteString(borderStyle + "│ " + reset)
				b.WriteString(ls + line + reset)
			}
		}
	}

	// Bottom border (omitted when in-progress).
	if !inProgress {
		b.WriteByte('\n')
		b.WriteString(borderStyle + "└" + strings.Repeat("─", innerWidth) + "┘" + reset)
	}

	return b.String()
}

type shouldShowToolOutputOptions struct {
	entry         toolGroupEntry
	idx           int
	lastResultIdx int
}

// shouldShowToolOutput determines whether a tool entry's result should be
// displayed within a grouped block. Rules:
//   - Errors: always shown
//   - Edit/write tools: always shown (diff output)
//   - Bash/git: shown only for the last result-bearing tool in the group
//   - All others (read, glob, grep, etc.): hidden (summary line is enough)
func shouldShowToolOutput(opts shouldShowToolOutputOptions) bool {
	entry := opts.entry
	if entry.isError {
		return true
	}
	switch entry.toolName {
	case "edit_file", "write_file":
		return true
	case "bash", "git":
		return opts.idx == opts.lastResultIdx
	default:
		return false
	}
}

var funnyTexts = []string{
	"pondering the cosmos...",
	"consulting the oracle...",
	"herding electrons...",
	"untangling spaghetti...",
	"asking the rubber duck...",
	"dividing by zero...",
	"reticulating splines...",
	"compiling thoughts...",
	"traversing the astral plane...",
	"shaking the magic 8-ball...",
	"feeding the hamsters...",
	"polishing pixels...",
	"summoning the muse...",
	"counting backwards from infinity...",
	"aligning the chakras...",
	"brewing coffee virtually...",
	"negotiating with the compiler...",
	"reading the tea leaves...",
}

// hsl is a color in HSL space (h in [0,360), s and l in [0,1]).
type hsl struct {
	h, s, l float64
}

// hslToRGB converts HSL to RGB [0,255].
func hslToRGB(c hsl) (int, int, int) {
	chroma := (1 - math.Abs(2*c.l-1)) * c.s
	hp := c.h / 60
	x := chroma * (1 - math.Abs(math.Mod(hp, 2)-1))
	var r1, g1, b1 float64
	switch {
	case hp < 1:
		r1, g1, b1 = chroma, x, 0
	case hp < 2:
		r1, g1, b1 = x, chroma, 0
	case hp < 3:
		r1, g1, b1 = 0, chroma, x
	case hp < 4:
		r1, g1, b1 = 0, x, chroma
	case hp < 5:
		r1, g1, b1 = x, 0, chroma
	default:
		r1, g1, b1 = chroma, 0, x
	}
	m := c.l - chroma/2
	return int(math.Round((r1 + m) * 255)),
		int(math.Round((g1 + m) * 255)),
		int(math.Round((b1 + m) * 255))
}

// pastelColor returns an ANSI true-color escape for a smoothly cycling pastel hue.
func pastelColor(elapsed time.Duration) string {
	hue := math.Mod(elapsed.Seconds()*90, 360) // full rotation every 4s
	r, g, b := hslToRGB(hsl{h: hue, s: 0.65, l: 0.78})
	return fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b)
}

// approvalGradientColor returns a bold ANSI true-color escape cycling through
// saturated yellow/amber/gold tones so the approval prompt is impossible to miss.
func approvalGradientColor(t time.Duration) string {
	phase := math.Sin(t.Seconds() * 2 * math.Pi / 1.5)
	hue := 42.5 + 12.5*phase // oscillate 30..55 (gold to yellow)
	r, g, b := hslToRGB(hsl{h: hue, s: 0.95, l: 0.52})
	return fmt.Sprintf("\033[1;38;2;%d;%d;%dm", r, g, b)
}

type wrapLineCountOptions struct {
	line  string
	width int
}

// wrapLineCount returns the number of visual lines that `line` would occupy
// when word-wrapped to `width` columns. It delegates to wrapString.
func wrapLineCount(opts wrapLineCountOptions) int {
	return len(wrapString(wrapStringOptions{s: opts.line, w: opts.width}))
}
