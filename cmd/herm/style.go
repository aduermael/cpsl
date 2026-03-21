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

func lerpColor(r1, g1, b1, r2, g2, b2 int, t float64) (int, int, int) {
	lerp := func(a, b int) int { return a + int(float64(b-a)*t) }
	return lerp(r1, r2), lerp(g1, g2), lerp(b1, b2)
}

func progressBar(n, max int) string {
	if n > max {
		n = max
	}
	ratio := float64(n) / float64(max)
	filled := int(ratio * 24)
	partials := []rune("█▉▊▋▌▍▎▏")

	r, g, b := lerpColor(78, 201, 100, 230, 70, 70, ratio)
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

func writeRows(buf *strings.Builder, rows []string, from int) {
	if len(rows) == 0 {
		return
	}
	buf.WriteString(fmt.Sprintf("\033[%d;1H", from))
	for i, row := range rows {
		if i > 0 {
			buf.WriteString("\r\n")
		}
		buf.WriteString("\033[0m\033[2K")
		buf.WriteString(row)
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

func styledToolResult(result string, isError bool) string {
	if isError {
		return styledError(result)
	}
	return "\033[2m" + result + "\033[0m"
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
		rendered = styledToolResult(content, msg.isError)
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

// renderToolBox renders a tool call and its result as a bordered box:
//
//	┌ ~ glob ───────┐
//	file1.go
//	file2.go
//	└───────────────┘
//
// The box has top/bottom borders but no side borders. The entire output is
// styled dim (or red for errors). Title uses dim+italic.
func renderToolBox(title, content string, maxWidth int, isError bool, durationStr string) string {
	// Replace tabs with single spaces for compact, predictable display.
	content = strings.ReplaceAll(content, "\t", " ")
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
		if minW := len(durationStr) + 2; minW > innerWidth {
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
		title = truncateWithEllipsis(title, maxTitleVW)
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
				line = truncateVisual(line, innerWidth)
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
		durPad := innerWidth - len(durationStr) - 2 // " duration "
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

// hslToRGB converts HSL (h in [0,360), s and l in [0,1]) to RGB [0,255].
func hslToRGB(h, s, l float64) (int, int, int) {
	c := (1 - math.Abs(2*l-1)) * s
	hp := h / 60
	x := c * (1 - math.Abs(math.Mod(hp, 2)-1))
	var r1, g1, b1 float64
	switch {
	case hp < 1:
		r1, g1, b1 = c, x, 0
	case hp < 2:
		r1, g1, b1 = x, c, 0
	case hp < 3:
		r1, g1, b1 = 0, c, x
	case hp < 4:
		r1, g1, b1 = 0, x, c
	case hp < 5:
		r1, g1, b1 = x, 0, c
	default:
		r1, g1, b1 = c, 0, x
	}
	m := l - c/2
	return int(math.Round((r1 + m) * 255)),
		int(math.Round((g1 + m) * 255)),
		int(math.Round((b1 + m) * 255))
}

// pastelColor returns an ANSI true-color escape for a smoothly cycling pastel hue.
func pastelColor(elapsed time.Duration) string {
	hue := math.Mod(elapsed.Seconds()*90, 360) // full rotation every 4s
	r, g, b := hslToRGB(hue, 0.65, 0.78)
	return fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b)
}

// approvalGradientColor returns a bold ANSI true-color escape cycling through
// saturated yellow/amber/gold tones so the approval prompt is impossible to miss.
func approvalGradientColor(t time.Duration) string {
	phase := math.Sin(t.Seconds() * 2 * math.Pi / 1.5)
	hue := 42.5 + 12.5*phase // oscillate 30..55 (gold to yellow)
	r, g, b := hslToRGB(hue, 0.95, 0.52)
	return fmt.Sprintf("\033[1;38;2;%d;%d;%dm", r, g, b)
}

// approvalGradientSep returns a separator line where each dash character is
// individually colored with a shifting yellow gradient wave effect.
func approvalGradientSep(width int, t time.Duration) string {
	var buf strings.Builder
	for i := 0; i < width; i++ {
		charPhase := math.Sin((t.Seconds()*2*math.Pi/1.5) + float64(i)*0.15)
		hue := 42.5 + 12.5*charPhase
		r, g, b := hslToRGB(hue, 0.95, 0.52)
		fmt.Fprintf(&buf, "\033[1;38;2;%d;%d;%dm─", r, g, b)
	}
	buf.WriteString("\033[0m")
	return buf.String()
}

// wrapLineCount returns the number of visual lines that `line` would occupy
// when word-wrapped to `width` columns. It delegates to wrapString.
func wrapLineCount(line string, width int) int {
	return len(wrapString(line, 0, width))
}
