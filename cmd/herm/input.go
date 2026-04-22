// input.go defines input event types, the stdin reader goroutine, and the
// text-editing / cursor-movement / autocomplete helpers shared by key dispatch
// (in input_keys.go) and the rest of the app.
package main

import (
	"log"
	"os"
	"strings"
	"syscall"
)

// ─── Input event types ───

type Key int

const (
	KeyNone Key = iota
	KeyRune
	KeyEnter
	KeyTab
	KeyBackspace
	KeyDelete
	KeyEscape
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	KeyPgUp
	KeyPgDown
	KeyInsert
)

type Modifier int

const (
	ModShift Modifier = 1 << iota
	ModAlt
	ModCtrl
)

type EventKey struct {
	Key  Key
	Rune rune
	Mod  Modifier
}

type EventPaste struct {
	Content string
}

type EventResize struct {
	Width  int
	Height int
}

// ─── Input helpers ───

func (a *App) abandonHistoryNav() {
	if a.history != nil && a.history.IsNavigating() {
		a.history.Reset()
	}
}

func (a *App) insertAtCursor(r rune) {
	a.abandonHistoryNav()
	a.input = append(a.input, 0)
	copy(a.input[a.cursor+1:], a.input[a.cursor:])
	a.input[a.cursor] = r
	a.cursor++
}

func (a *App) insertText(s string) {
	for _, r := range s {
		a.insertAtCursor(r)
	}
}

func (a *App) deleteBeforeCursor() {
	a.abandonHistoryNav()
	if a.cursor <= 0 {
		return
	}
	a.cursor--
	copy(a.input[a.cursor:], a.input[a.cursor+1:])
	a.input = a.input[:len(a.input)-1]
}

func (a *App) deleteAtCursor() {
	a.abandonHistoryNav()
	if a.cursor >= len(a.input) {
		return
	}
	copy(a.input[a.cursor:], a.input[a.cursor+1:])
	a.input = a.input[:len(a.input)-1]
}

func (a *App) deleteWordBackward() {
	a.abandonHistoryNav()
	if a.cursor <= 0 {
		return
	}
	// Skip trailing spaces
	for a.cursor > 0 && a.input[a.cursor-1] == ' ' {
		a.deleteBeforeCursor()
	}
	// Delete word
	for a.cursor > 0 && a.input[a.cursor-1] != ' ' && a.input[a.cursor-1] != '\n' {
		a.deleteBeforeCursor()
	}
}

func (a *App) killLine() {
	a.abandonHistoryNav()
	// Delete from cursor to end of current line (or end of input)
	end := a.cursor
	for end < len(a.input) && a.input[end] != '\n' {
		end++
	}
	a.input = append(a.input[:a.cursor], a.input[end:]...)
}

func (a *App) killToStart() {
	a.abandonHistoryNav()
	// Delete from cursor to start of current line
	start := a.cursor
	for start > 0 && a.input[start-1] != '\n' {
		start--
	}
	a.input = append(a.input[:start], a.input[a.cursor:]...)
	a.cursor = start
}

func (a *App) moveUp() {
	lineIdx, col := cursorVisualPos(cursorVisualPosOptions{input: a.input, cursor: a.cursor, width: a.width})
	if lineIdx == 0 {
		return
	}
	vlines := getVisualLines(getVisualLinesOptions{input: a.input, cursor: a.cursor, width: a.width})
	prev := vlines[lineIdx-1]
	targetCol := col
	if targetCol > prev.startCol+prev.length {
		targetCol = prev.startCol + prev.length
	}
	if targetCol < prev.startCol {
		targetCol = prev.startCol
	}
	a.cursor = prev.start + (targetCol - prev.startCol)
}

func (a *App) moveDown() {
	lineIdx, _ := cursorVisualPos(cursorVisualPosOptions{input: a.input, cursor: a.cursor, width: a.width})
	vlines := getVisualLines(getVisualLinesOptions{input: a.input, cursor: a.cursor, width: a.width})
	if lineIdx >= len(vlines)-1 {
		return
	}
	_, col := cursorVisualPos(cursorVisualPosOptions{input: a.input, cursor: a.cursor, width: a.width})
	next := vlines[lineIdx+1]
	targetCol := col
	if targetCol > next.startCol+next.length {
		targetCol = next.startCol + next.length
	}
	if targetCol < next.startCol {
		targetCol = next.startCol
	}
	a.cursor = next.start + (targetCol - next.startCol)
}

func (a *App) moveWordLeft() {
	if a.cursor <= 0 {
		return
	}
	a.cursor--
	for a.cursor > 0 && a.input[a.cursor] == ' ' {
		a.cursor--
	}
	for a.cursor > 0 && a.input[a.cursor-1] != ' ' && a.input[a.cursor-1] != '\n' {
		a.cursor--
	}
}

func (a *App) moveWordRight() {
	if a.cursor >= len(a.input) {
		return
	}
	a.cursor++
	for a.cursor < len(a.input) && a.input[a.cursor] != ' ' && a.input[a.cursor] != '\n' {
		a.cursor++
	}
	for a.cursor < len(a.input) && a.input[a.cursor] == ' ' {
		a.cursor++
	}
}

func (a *App) inputValue() string {
	return string(a.input)
}

func (a *App) setInputValue(s string) {
	a.input = []rune(s)
	a.cursor = len(a.input)
}

func (a *App) resetInput() {
	a.input = a.input[:0]
	a.cursor = 0
}

// ─── Autocomplete ───

func (a *App) autocompleteMatches() []string {
	if a.mode != modeChat {
		return nil
	}
	val := a.inputValue()
	if !strings.HasPrefix(val, "/") {
		return nil
	}
	return filterCommands(val)
}

// ─── Stdin reader goroutine ───

// startStdinReader creates a dup'd stdin fd and starts a goroutine that reads
// from it byte-by-byte into a.stdinCh. This allows us to stop the reader by
// closing the dup'd fd (e.g. before entering shell mode).
func (a *App) startStdinReader() {
	dupFd, err := syscall.Dup(int(os.Stdin.Fd()))
	if err != nil {
		log.Fatalf("dup stdin: %v", err)
	}
	a.stdinDup = os.NewFile(uintptr(dupFd), "stdin-dup")
	a.stdinCh = make(chan byte, 64)

	go func() {
		buf := make([]byte, 1)
		for {
			_, err := a.stdinDup.Read(buf)
			if err != nil {
				close(a.stdinCh)
				return
			}
			a.stdinCh <- buf[0]
		}
	}()

	a.readByte = func() (byte, bool) {
		b, ok := <-a.stdinCh
		return b, ok
	}
}

// stopStdinReader closes the dup'd stdin fd, causing the reader goroutine to
// exit, then drains any remaining bytes from stdinCh.
func (a *App) stopStdinReader() {
	if a.stdinDup != nil {
		a.stdinDup.Close()
		a.stdinDup = nil
	}
	// Drain remaining bytes
	for {
		select {
		case _, ok := <-a.stdinCh:
			if !ok {
				return
			}
		default:
			return
		}
	}
}
