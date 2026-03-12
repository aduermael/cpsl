package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

type Block struct {
	Text string
}

const (
	promptPrefix     = "▸ "
	promptPrefixCols = 2
)

// vline represents one visual line of the input area.
type vline struct {
	start    int // rune index of first char
	length   int // number of runes
	startCol int // visual column where text starts
}

var (
	blocks        []Block
	width         int
	input         []rune
	cursor        int // rune position within input
	prevRowCount  int // total rows drawn last frame
	sepRow        int // 1-based screen row of top separator
	inputStartRow int // 1-based screen row of first input line
)

func getWidth() int {
	w, _, err := term.GetSize(int(os.Stdin.Fd()))
	if err != nil {
		return 80
	}
	return w
}

// getVisualLines splits the input into visual lines, accounting for
// the prompt prefix on the first line and terminal-width wrapping.
func getVisualLines() []vline {
	var lines []vline
	start := 0
	startCol := promptPrefixCols
	length := 0

	for i, r := range input {
		if r == '\n' {
			lines = append(lines, vline{start, length, startCol})
			start = i + 1
			startCol = 0
			length = 0
			continue
		}
		length++
		if startCol+length >= width {
			lines = append(lines, vline{start, length, startCol})
			start = i + 1
			startCol = 0
			length = 0
		}
	}
	lines = append(lines, vline{start, length, startCol})
	return lines
}

// cursorVisualPos returns the visual line index and column for the cursor.
func cursorVisualPos() (int, int) {
	vlines := getVisualLines()
	for i, vl := range vlines {
		end := vl.start + vl.length
		if cursor >= vl.start && cursor <= end {
			if cursor == end && i < len(vlines)-1 && vl.startCol+vl.length >= width {
				continue
			}
			return i, vl.startCol + (cursor - vl.start)
		}
	}
	last := len(vlines) - 1
	vl := vlines[last]
	return last, vl.startCol + vl.length
}

// wrapString splits a string into visual rows of at most `width` runes.
func wrapString(s string, startCol int) []string {
	runes := []rune(s)
	if len(runes) == 0 {
		return []string{""}
	}
	var rows []string
	col := startCol
	lineStart := 0
	for i := range runes {
		col++
		if col >= width {
			rows = append(rows, string(runes[lineStart:i+1]))
			lineStart = i + 1
			col = 0
		}
	}
	if lineStart <= len(runes) {
		rows = append(rows, string(runes[lineStart:]))
	}
	return rows
}

const charLimit = 250

// lerpColor interpolates between two RGB colors based on t (0.0 to 1.0).
func lerpColor(r1, g1, b1, r2, g2, b2 int, t float64) (int, int, int) {
	lerp := func(a, b int) int { return a + int(float64(b-a)*t) }
	return lerp(r1, r2), lerp(g1, g2), lerp(b1, b2)
}

// progressBar returns a 3-character wide ANSI-styled string using block elements.
// Dim █ for empty cells, colored █ for filled, colored partial on dim bg for transition.
func progressBar(n, max int) string {
	if n > max {
		n = max
	}
	ratio := float64(n) / float64(max)
	filled := int(ratio * 24) // 0..24
	partials := []rune("█▉▊▋▌▍▎▏")

	// Green (78, 201, 100) → Red (230, 70, 70)
	r, g, b := lerpColor(78, 201, 100, 230, 70, 70, ratio)
	fillFg := fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b)
	dimFg := "\033[38;5;240m"
	// For the partial cell: use dim color as bg so the unfilled part matches empty cells
	dimBg := "\033[48;5;240m"

	const reset = "\033[0m"

	var buf strings.Builder
	for i := range 3 {
		cellFilled := filled - i*8
		switch {
		case cellFilled >= 8:
			buf.WriteString(fillFg + "█" + reset)
		case cellFilled <= 0:
			buf.WriteString(dimFg + "█" + reset)
		default:
			// Partial: colored partial block on dim background (single cell only)
			buf.WriteString(dimBg + fillFg + string(partials[8-cellFilled]) + reset)
		}
	}
	return buf.String()
}

// buildInputRows builds just the input area: top separator, input lines, bottom separator.
func buildInputRows() []string {
	sep := strings.Repeat("─", width)
	rows := []string{sep}

	vlines := getVisualLines()
	for i, vl := range vlines {
		line := string(input[vl.start : vl.start+vl.length])
		if i == 0 {
			line = promptPrefix + line
		}
		rows = append(rows, line)
	}

	rows = append(rows, strings.Repeat("─", width))

	// Progress bar row below bottom separator, right-aligned
	totalChars := 0
	for _, b := range blocks {
		totalChars += len([]rune(b.Text))
	}
	totalChars += len(input)
	bar := progressBar(totalChars, charLimit)
	barWidth := 3
	padding := width - barWidth
	if padding < 0 {
		padding = 0
	}
	rows = append(rows, strings.Repeat(" ", padding)+bar+"\033[0m\033[K")

	return rows
}

// writeRows writes rows to buf starting at screen row `from` (1-based).
func writeRows(buf *strings.Builder, rows []string, from int) {
	for i, row := range rows {
		buf.WriteString(fmt.Sprintf("\033[%d;1H\033[0m\033[2K%s", from+i, row))
	}
}

// positionCursor appends the escape to move cursor to its position in the input area.
func positionCursor(buf *strings.Builder) {
	curLine, curCol := cursorVisualPos()
	buf.WriteString(fmt.Sprintf("\033[%d;%dH", inputStartRow+curLine, curCol+1))
}

var logo = []string{
	"",
	"    \033[38;5;75m▄███▄\033[0m ░▄▀▀▒█▀▄░▄▀▀░█▒░",
	"  \033[38;5;75m▄██\033[38;5;255m• •\033[38;5;75m█\033[0m ░▀▄▄░█▀▒▒▄██▒█▄▄",
	" \033[38;5;75m▀███▄█▄█\033[0m Contained Coding Agent",
	"",
}

// render redraws the entire screen (blocks + input area).
func render() {
	var blockRows []string
	blockRows = append(blockRows, logo...)
	for _, b := range blocks {
		for _, logLine := range strings.Split(b.Text, "\n") {
			blockRows = append(blockRows, wrapString(logLine, 0)...)
		}
		blockRows = append(blockRows, "") // empty line after block
	}

	sepRow = len(blockRows) + 1
	inputStartRow = sepRow + 1

	inputRows := buildInputRows()
	totalRows := len(blockRows) + len(inputRows)

	var buf strings.Builder
	writeRows(&buf, blockRows, 1)
	writeRows(&buf, inputRows, sepRow)

	// Clear leftover rows from previous frame
	for i := totalRows; i < prevRowCount; i++ {
		buf.WriteString(fmt.Sprintf("\033[%d;1H\033[2K", i+1))
	}
	prevRowCount = totalRows

	positionCursor(&buf)
	os.Stdout.WriteString(buf.String())
}

// renderInput redraws only the input area (separators + input lines).
// Blocks above are untouched.
func renderInput() {
	inputRows := buildInputRows()
	totalRows := sepRow - 1 + len(inputRows)

	var buf strings.Builder
	writeRows(&buf, inputRows, sepRow)

	// Clear leftover rows below input area
	for i := totalRows; i < prevRowCount; i++ {
		buf.WriteString(fmt.Sprintf("\033[%d;1H\033[2K", i+1))
	}
	prevRowCount = totalRows

	positionCursor(&buf)
	os.Stdout.WriteString(buf.String())
}

func insertAtCursor(r rune) {
	input = append(input, 0)
	copy(input[cursor+1:], input[cursor:])
	input[cursor] = r
	cursor++
}

func deleteBeforeCursor() {
	if cursor <= 0 {
		return
	}
	cursor--
	copy(input[cursor:], input[cursor+1:])
	input = input[:len(input)-1]
}

func moveUp() {
	lineIdx, col := cursorVisualPos()
	if lineIdx == 0 {
		return
	}
	vlines := getVisualLines()
	prev := vlines[lineIdx-1]
	targetCol := col
	if targetCol > prev.startCol+prev.length {
		targetCol = prev.startCol + prev.length
	}
	if targetCol < prev.startCol {
		targetCol = prev.startCol
	}
	cursor = prev.start + (targetCol - prev.startCol)
	renderInput()
}

func moveDown() {
	lineIdx, col := cursorVisualPos()
	vlines := getVisualLines()
	if lineIdx >= len(vlines)-1 {
		return
	}
	next := vlines[lineIdx+1]
	targetCol := col
	if targetCol > next.startCol+next.length {
		targetCol = next.startCol + next.length
	}
	if targetCol < next.startCol {
		targetCol = next.startCol
	}
	cursor = next.start + (targetCol - next.startCol)
	renderInput()
}

func main() {
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	startTime := time.Now()

	fmt.Print("\033[?1049h")
	defer func() {
		fmt.Print("\033[?1049l")
		end := time.Now()
		fmt.Printf("[CHAT %s -> %s]\r\n",
			startTime.Format("Jan 02 15:04"),
			end.Format("Jan 02 15:04"))
	}()

	width = getWidth()

	sigWinch := make(chan os.Signal, 1)
	signal.Notify(sigWinch, syscall.SIGWINCH)
	go func() {
		for range sigWinch {
			width = getWidth()
			render()
		}
	}()

	render()

	raw := make([]byte, 1)
	for {
		_, err := os.Stdin.Read(raw)
		if err != nil {
			break
		}
		ch := raw[0]

		// Escape sequence (arrow keys)
		if ch == '\033' {
			os.Stdin.Read(raw)
			if raw[0] != '[' {
				continue
			}
			os.Stdin.Read(raw)
			switch raw[0] {
			case 'A':
				moveUp()
			case 'B':
				moveDown()
			case 'C': // right
				if cursor < len(input) {
					cursor++
					renderInput()
				}
			case 'D': // left
				if cursor > 0 {
					cursor--
					renderInput()
				}
			}
			continue
		}

		// Ctrl-C / Ctrl-D
		if ch == 3 || ch == 4 {
			break
		}

		// Shift+Enter — insert newline
		if ch == '\n' {
			insertAtCursor('\n')
			renderInput()
			continue
		}

		// Enter — submit
		if ch == '\r' {
			if len(input) > 0 {
				blocks = append(blocks, Block{Text: string(input)})
				input = input[:0]
				cursor = 0
			}
			render()
			continue
		}

		// Backspace
		if ch == 127 {
			if cursor > 0 {
				deleteBeforeCursor()
				renderInput()
			}
			continue
		}

		// Regular character
		r := rune(ch)
		if ch >= 0x80 {
			b := []byte{ch}
			n := utf8ByteLen(ch)
			for i := 1; i < n; i++ {
				os.Stdin.Read(raw)
				b = append(b, raw[0])
			}
			r, _ = utf8.DecodeRune(b)
		}

		insertAtCursor(r)
		renderInput()
	}
}

func utf8ByteLen(first byte) int {
	switch {
	case first&0x80 == 0:
		return 1
	case first&0xE0 == 0xC0:
		return 2
	case first&0xF0 == 0xE0:
		return 3
	default:
		return 4
	}
}
