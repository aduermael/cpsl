package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Renderer manages terminal output, splitting it into a scrollback region
// (above) and an active area (bottom) that gets redrawn each frame.
type Renderer struct {
	out              *bufio.Writer
	activeAreaHeight int // number of lines in the current active area
}

// NewRenderer creates a renderer that writes to stdout.
func NewRenderer() *Renderer {
	return &Renderer{
		out: bufio.NewWriter(os.Stdout),
	}
}

// printAbove writes content to the scrollback region above the active area.
// It first erases the active area, prints the content, then the active area
// will be redrawn on the next render call.
func (r *Renderer) printAbove(content string) {
	r.clearActiveArea()
	fmt.Fprint(r.out, content)
	if !strings.HasSuffix(content, "\n") {
		fmt.Fprint(r.out, "\n")
	}
	r.activeAreaHeight = 0
	r.out.Flush()
}

// renderActiveArea redraws the active area at the bottom of the terminal.
// lines is the content to display, cursorX/cursorY are the cursor position
// relative to the active area (0-indexed).
func (r *Renderer) renderActiveArea(lines []string, cursorX, cursorY int) {
	r.clearActiveArea()

	content := strings.Join(lines, "\n")
	fmt.Fprint(r.out, content)

	r.activeAreaHeight = len(lines)

	// Position the cursor. Move from the end of content to the cursor position.
	if r.activeAreaHeight > 0 {
		// Move cursor to the right row
		linesFromBottom := r.activeAreaHeight - 1 - cursorY
		if linesFromBottom > 0 {
			fmt.Fprintf(r.out, "\033[%dA", linesFromBottom) // move up
		}
		// Move cursor to the right column
		fmt.Fprint(r.out, "\r")         // go to column 0
		if cursorX > 0 {
			fmt.Fprintf(r.out, "\033[%dC", cursorX) // move right
		}
	}

	r.out.Flush()
}

// clearActiveArea erases the current active area from the terminal.
func (r *Renderer) clearActiveArea() {
	if r.activeAreaHeight <= 0 {
		return
	}
	// Move to the start of the active area
	if r.activeAreaHeight > 1 {
		fmt.Fprintf(r.out, "\033[%dA", r.activeAreaHeight-1) // move up
	}
	fmt.Fprint(r.out, "\r")          // go to column 0
	fmt.Fprint(r.out, "\033[J")      // clear from cursor to end of screen
	r.activeAreaHeight = 0
}

// clearAll clears the entire screen and scrollback buffer.
func (r *Renderer) clearAll() {
	fmt.Fprint(r.out, "\033[2J")  // clear screen
	fmt.Fprint(r.out, "\033[3J")  // clear scrollback
	fmt.Fprint(r.out, "\033[H")   // move cursor to top-left
	r.activeAreaHeight = 0
	r.out.Flush()
}

// enterAltScreen switches to the alternate screen buffer.
func (r *Renderer) enterAltScreen() {
	fmt.Fprint(r.out, "\033[?1049h")
	r.activeAreaHeight = 0
	r.out.Flush()
}

// exitAltScreen switches back from the alternate screen buffer.
func (r *Renderer) exitAltScreen() {
	fmt.Fprint(r.out, "\033[?1049l")
	r.activeAreaHeight = 0
	r.out.Flush()
}

// enableBracketedPaste enables bracketed paste mode.
func (r *Renderer) enableBracketedPaste() {
	fmt.Fprint(r.out, "\033[?2004h")
	r.out.Flush()
}

// disableBracketedPaste disables bracketed paste mode.
func (r *Renderer) disableBracketedPaste() {
	fmt.Fprint(r.out, "\033[?2004l")
	r.out.Flush()
}

// showCursor makes the cursor visible.
func (r *Renderer) showCursor() {
	fmt.Fprint(r.out, "\033[?25h")
	r.out.Flush()
}

// hideCursor hides the cursor.
func (r *Renderer) hideCursor() {
	fmt.Fprint(r.out, "\033[?25l")
	r.out.Flush()
}

// flush writes any buffered output to the terminal.
func (r *Renderer) flush() {
	r.out.Flush()
}
