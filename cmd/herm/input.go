// input.go handles keyboard/terminal input: key and modifier types, event
// structs, the stdin reader goroutine, escape-sequence parsing, key dispatch,
// history navigation, autocomplete, paste handling, and text-editing helpers.
package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
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

// ─── Key dispatch and byte handling ───

type handleByteOptions struct {
	ch       byte
	stdinCh  chan byte
	readByte func() (byte, bool)
}

// handleByte processes a single byte from stdin. Returns true if the app should quit.
func (a *App) handleByte(opts handleByteOptions) bool {
	ch, stdinCh, readByte := opts.ch, opts.stdinCh, opts.readByte
	// Config editor mode intercept (unless a model picker menu is active)
	if a.cfgActive && !a.menuActive {
		a.handleConfigByte(handleConfigByteOptions{ch: ch, stdinCh: stdinCh, readByte: readByte})
		return false
	}

	// Escape sequence
	if ch == '\033' {
		a.handleEscapeSequence(handleEscapeSequenceOptions{stdinCh: stdinCh, readByte: readByte})
		return false
	}

	// Ctrl+D: immediate quit
	if ch == 4 {
		return true
	}

	// Ctrl+C: double-tap to stop agent (when running) or exit (when idle)
	if ch == 3 {
		if a.agentRunning {
			// When agent is running, double-tap Ctrl+C stops the agent.
			// If Cancel was already sent and agent is still running,
			// force-quit immediately (safety net for stuck agents).
			if a.ctrlCHint && time.Since(a.ctrlCTime) < 2*time.Second {
				if a.cancelSent {
					return true // force quit — Cancel() didn't work
				}
				a.agent.Cancel()
				a.cancelSent = true
				a.ctrlCHint = false
				a.ctrlCTime = time.Time{}
				return false
			}
		} else {
			// Second Ctrl+C within 2 seconds: quit
			if a.ctrlCHint && time.Since(a.ctrlCTime) < 2*time.Second {
				return true
			}
		}
		// First press: clear input if any, show hint
		a.input = nil
		a.cursor = 0
		a.ctrlCHint = true
		a.ctrlCTime = time.Now()
		a.renderInput()
		// Schedule hint removal after 2 seconds
		ch := a.resultCh
		go func() {
			time.Sleep(2 * time.Second)
			select {
			case ch <- ctrlCExpiredMsg{}:
			default:
			}
		}()
		return false
	}

	// Any other key clears the Ctrl+C / ESC hints
	if a.ctrlCHint {
		a.ctrlCHint = false
		a.ctrlCTime = time.Time{}
	}
	if a.escHint {
		a.escHint = false
		a.escTime = time.Time{}
	}

	// Handle approval mode
	if a.awaitingApproval {
		a.handleApprovalByte(ch)
		return false
	}

	// Ctrl+W: delete word backward
	if ch == 0x17 {
		a.deleteWordBackward()
		a.autocompleteIdx = 0
		a.renderInput()
		return false
	}

	// Ctrl+U: kill to start of line
	if ch == 0x15 {
		a.killToStart()
		a.renderInput()
		return false
	}

	// Ctrl+K: kill to end of line
	if ch == 0x0b {
		a.killLine()
		a.renderInput()
		return false
	}

	// Ctrl+A: home
	if ch == 0x01 {
		a.cursor = 0
		a.renderInput()
		return false
	}

	// Ctrl+E: end
	if ch == 0x05 {
		a.cursor = len(a.input)
		a.renderInput()
		return false
	}

	// Tab
	if ch == '\t' {
		if a.menuActive && a.menuModels != nil {
			a.menuSortAsc[a.menuSortCol] = !a.menuSortAsc[a.menuSortCol]
			a.refreshModelMenu()
			a.renderInput()
			return false
		}
		if matches := a.autocompleteMatches(); len(matches) > 0 {
			idx := a.autocompleteIdx
			if idx >= len(matches) {
				idx = 0
			}
			a.setInputValue(matches[idx])
			a.autocompleteIdx = 0
		}
		a.renderInput()
		return false
	}

	// Shift+Enter (LF) — insert newline
	if ch == '\n' {
		if a.menuActive {
			return false
		}
		a.insertAtCursor('\n')
		a.renderInput()
		return false
	}

	// Enter (CR) — submit or menu select
	if ch == '\r' {
		if a.menuActive && a.menuAction != nil {
			a.menuAction(a.menuCursor)
			a.render()
			return false
		}
		a.handleEnter()
		return false
	}

	// When menu is active, block all other input
	if a.menuActive {
		return false
	}

	// Backspace
	if ch == 127 || ch == 0x08 {
		if a.cursor > 0 {
			a.deleteBeforeCursor()
			a.autocompleteIdx = 0
			a.renderInput()
		}
		return false
	}

	// Regular character (possibly multi-byte UTF-8)
	r := rune(ch)
	if ch >= 0x80 {
		b := []byte{ch}
		n := utf8ByteLen(ch)
		for i := 1; i < n; i++ {
			next, ok := readByte()
			if !ok {
				return true
			}
			b = append(b, next)
		}
		r, _ = utf8.DecodeRune(b)
	}

	prevVal := a.inputValue()
	a.insertAtCursor(r)
	if a.inputValue() != prevVal {
		a.autocompleteIdx = 0
	}
	a.renderInput()
	return false
}

// ─── Escape sequence handling ───

func (a *App) handlePlainEscape() {
	if a.awaitingApproval {
		a.handleApprovalByte('n')
	}
	// When agent is running, double-tap ESC to stop it.
	// If Cancel was already sent and agent is still running, cancel again
	// (force the context) so the user is never truly stuck.
	if a.agentRunning {
		if a.escHint && time.Since(a.escTime) < 2*time.Second {
			a.agent.Cancel()
			a.cancelSent = true
			a.escHint = false
			a.escTime = time.Time{}
			return
		}
		a.escHint = true
		a.escTime = time.Now()
		a.renderInput()
		ch := a.resultCh
		go func() {
			time.Sleep(2 * time.Second)
			select {
			case ch <- escExpiredMsg{}:
			default:
			}
		}()
		return
	}
	if a.menuActive {
		a.menuLines = nil
		a.menuHeader = ""
		a.menuActive = false
		a.menuAction = nil
		a.menuCursor = 0
		a.menuScrollOffset = 0
		a.renderInput()
		return
	}
	a.resetInput()
	a.autocompleteIdx = 0
	a.renderInput()
}

type handleEscapeSequenceOptions struct {
	stdinCh  chan byte
	readByte func() (byte, bool)
}

func (a *App) handleEscapeSequence(opts handleEscapeSequenceOptions) {
	stdinCh, readByte := opts.stdinCh, opts.readByte
	// Use a short timeout to distinguish plain ESC from escape sequences.
	// Escape sequences (arrow keys, etc.) send bytes in rapid succession,
	// so if no byte arrives within 50ms, it's a standalone ESC press.
	var b byte
	var ok bool
	select {
	case b, ok = <-stdinCh:
		if !ok {
			return
		}
	case <-time.After(50 * time.Millisecond):
		// Plain ESC key — no sequence followed
		a.handlePlainEscape()
		return
	}

	// Alt+Enter: ESC CR
	if b == '\r' {
		if !a.awaitingApproval {
			a.insertAtCursor('\n')
			a.renderInput()
		}
		return
	}

	if b == 0x7F {
		// Alt+Backspace: delete word backward
		if !a.awaitingApproval {
			a.deleteWordBackward()
			a.renderInput()
		}
		return
	}

	// SS3 sequence: ESC O <letter>
	// Some terminals send arrow keys as SS3 instead of CSI, especially when
	// application cursor key mode (DECCKM) is active. Scroll events converted
	// via DECSET 1007 may also arrive in this format.
	if b == 'O' {
		ss3, ok := readByte()
		if !ok {
			return
		}
		if !a.awaitingApproval {
			a.handleNavKey(handleNavKeyOptions{final: ss3})
		}
		return
	}

	if b != '[' {
		// ESC followed by non-[ byte (e.g. Alt+key) — treat as plain escape
		a.handlePlainEscape()
		return
	}

	// CSI sequence: ESC [
	// Per ECMA-48, collect parameter bytes (0x30–0x3F) and intermediate bytes
	// (0x20–0x2F) until a final byte (0x40–0x7E) arrives. This ensures that
	// unknown or unexpected CSI sequences are fully consumed and never leak
	// trailing bytes into the input buffer (e.g. mouse/scroll events from
	// terminals like Zed that send sequences herm does not handle).
	var params []byte
	var final byte
	for {
		b, ok = readByte()
		if !ok {
			return
		}
		if isCSIFinal(b) {
			final = b
			break
		}
		params = append(params, b)
		if len(params) > 128 {
			return // safety limit for malformed sequences
		}
	}

	// Tilde-terminated sequences
	if final == '~' {
		ps := string(params)
		switch {
		case ps == "200": // Bracketed paste: ESC [ 200 ~
			a.handlePaste(readBracketedPaste(readByte))
		case ps == "3": // Delete: ESC [ 3 ~
			if !a.awaitingApproval {
				a.deleteAtCursor()
				a.renderInput()
			}
		default:
			// modifyOtherKeys: ESC [ 27 ; <mod> ; <code> ~
			if strings.HasPrefix(ps, "27;") {
				var mod, code int
				if n, _ := fmt.Sscanf(ps[3:], "%d;%d", &mod, &code); n == 2 {
					a.handleModifyOtherKeys(handleModifyOtherKeysOptions{mod: mod, code: code})
				}
			}
			// All other tilde sequences (Insert, PgUp, etc.) silently consumed
		}
		return
	}

	if a.awaitingApproval {
		return
	}

	// Navigation keys (arrows, Home, End) — shared by CSI and SS3
	a.handleNavKey(handleNavKeyOptions{final: final, params: params})
	// Unknown final bytes (mode responses, etc.): already fully consumed by
	// the collection loop above — silently discarded.
}

type handleNavKeyOptions struct {
	final  byte
	params []byte
}

// handleNavKey dispatches a navigation key (A/B/C/D/H/F) with optional CSI
// parameters. Called for both CSI (ESC [ ... X) and SS3 (ESC O X) sequences.
func (a *App) handleNavKey(opts handleNavKeyOptions) {
	final, params := opts.final, opts.params
	// Parse modifier from params (e.g. "1;5" in ESC [ 1;5 C → mod 5 = Ctrl).
	// CSI modifier encoding: value = 1 + bitmask (Shift=1, Alt=2, Ctrl=4).
	mod := 0
	if i := strings.LastIndexByte(string(params), ';'); i >= 0 {
		fmt.Sscanf(string(params[i+1:]), "%d", &mod)
	}
	isCtrl := mod > 0 && (mod-1)&4 != 0
	isAlt := mod > 0 && (mod-1)&2 != 0

	switch final {
	case 'A': // Up
		if len(params) > 0 {
			return // parameterized/modified up — no action defined
		}
		if a.menuActive {
			if a.menuCursor > 0 {
				a.menuCursor--
				if a.menuCursor < a.menuScrollOffset {
					a.menuScrollOffset = a.menuCursor
				}
			}
		} else if matches := a.autocompleteMatches(); len(matches) > 0 {
			a.autocompleteIdx--
			if a.autocompleteIdx < 0 {
				a.autocompleteIdx = len(matches) - 1
			}
		} else {
			lineIdx, _ := cursorVisualPos(cursorVisualPosOptions{input: a.input, cursor: a.cursor, width: a.width})
			if lineIdx == 0 && a.history != nil {
				if val, changed := a.history.Up(a.inputValue()); changed {
					a.setInputValue(val)
				}
			} else {
				a.moveUp()
			}
		}
		a.renderInput()
	case 'B': // Down
		if len(params) > 0 {
			return // parameterized/modified down — no action defined
		}
		if a.menuActive {
			if a.menuCursor < len(a.menuLines)-1 {
				a.menuCursor++
				maxVisible := getTerminalHeight() * 60 / 100
				if maxVisible < 1 {
					maxVisible = 1
				}
				if a.menuCursor >= a.menuScrollOffset+maxVisible {
					a.menuScrollOffset = a.menuCursor - maxVisible + 1
				}
			}
		} else if matches := a.autocompleteMatches(); len(matches) > 0 {
			a.autocompleteIdx++
			if a.autocompleteIdx >= len(matches) {
				a.autocompleteIdx = 0
			}
		} else {
			lineIdx, _ := cursorVisualPos(cursorVisualPosOptions{input: a.input, cursor: a.cursor, width: a.width})
			vlines := getVisualLines(getVisualLinesOptions{input: a.input, cursor: a.cursor, width: a.width})
			if lineIdx >= len(vlines)-1 && a.history != nil {
				if val, changed := a.history.Down(a.inputValue()); changed {
					a.setInputValue(val)
				}
			} else {
				a.moveDown()
			}
		}
		a.renderInput()
	case 'C': // Right
		if isCtrl || isAlt {
			a.moveWordRight()
			a.renderInput()
		} else if a.menuActive && a.menuModels != nil {
			if a.menuSortCol < 3 {
				a.menuSortCol++
				a.refreshModelMenu()
			}
			a.renderInput()
		} else if a.cursor < len(a.input) {
			a.cursor++
			a.renderInput()
		}
	case 'D': // Left
		if isCtrl || isAlt {
			a.moveWordLeft()
			a.renderInput()
		} else if a.menuActive && a.menuModels != nil {
			if a.menuSortCol > 0 {
				a.menuSortCol--
				a.refreshModelMenu()
			}
			a.renderInput()
		} else if a.cursor > 0 {
			a.cursor--
			a.renderInput()
		}
	case 'H': // Home
		a.cursor = 0
		a.renderInput()
	case 'F': // End
		a.cursor = len(a.input)
		a.renderInput()
	}
}

type handleModifyOtherKeysOptions struct {
	mod  int
	code int
}

// handleModifyOtherKeys processes a modifyOtherKeys sequence (CSI 27;mod;code~).
// With modifyOtherKeys mode 2, the terminal encodes modified keys that would
// otherwise be ambiguous (e.g., Ctrl+C as CSI 27;5;99~ instead of byte 0x03).
// We translate these back to the actions the app already handles.
func (a *App) handleModifyOtherKeys(opts handleModifyOtherKeysOptions) {
	mod, code := opts.mod, opts.code
	if a.awaitingApproval {
		return
	}
	mod-- // CSI modifier encoding
	isAlt := mod&2 != 0
	isCtrl := mod&4 != 0

	switch {
	case isAlt && code == 127: // Alt+Backspace → delete word
		a.deleteWordBackward()
		a.renderInput()
	case isAlt && code == '\r': // Alt+Enter → insert newline
		a.insertAtCursor('\n')
		a.renderInput()
	case isCtrl && code >= 'a' && code <= 'z':
		// Translate Ctrl+letter to traditional control byte
		// and inject into the input channel for normal processing.
		ctrlByte := byte(code - 'a' + 1)
		go func() { a.stdinCh <- ctrlByte }()
	}
}

// readBracketedPaste reads paste content until the end marker ESC [ 2 0 1 ~.
// Called after the start marker ESC [ 2 0 0 ~ has already been consumed.
func readBracketedPaste(readByte func() (byte, bool)) string {
	var content []byte
	for {
		ch, ok := readByte()
		if !ok {
			break
		}
		if ch == '\033' {
			e0, ok := readByte()
			if !ok {
				break
			}
			if e0 == '[' {
				e1, ok := readByte()
				if !ok {
					break
				}
				e2, ok := readByte()
				if !ok {
					break
				}
				e3, ok := readByte()
				if !ok {
					break
				}
				e4, ok := readByte()
				if !ok {
					break
				}
				if e1 == '2' && e2 == '0' && e3 == '1' && e4 == '~' {
					break
				}
				content = append(content, '\033', e0, e1, e2, e3, e4)
			} else {
				content = append(content, '\033', e0)
			}
		} else {
			content = append(content, ch)
		}
	}
	return string(content)
}

type handleCSIDigit2Options struct {
	readByte func() (byte, bool)
	onPaste  func(string)
}

func (a *App) handleCSIDigit2(opts handleCSIDigit2Options) {
	readByte, onPaste := opts.readByte, opts.onPaste
	// We've read ESC [ 2, check for 0 0 ~
	b0, ok := readByte()
	if !ok {
		return
	}
	b1, ok := readByte()
	if !ok {
		return
	}
	b2, ok := readByte()
	if !ok {
		return
	}

	if b0 == '0' && b1 == '0' && b2 == '~' {
		onPaste(readBracketedPaste(readByte))
		return
	}

	// modifyOtherKeys: ESC [ 27 ; <mod> ; <code> ~
	// We've read ESC [ 2, b0='7', b1=';', b2=<mod digit>
	if b0 == '7' && b1 == ';' {
		// Read remaining: possibly more mod digits, ';', code digits, '~'
		// Collect all remaining bytes until '~'
		seq := []byte{b2}
		for {
			next, ok := readByte()
			if !ok {
				return
			}
			if next == '~' {
				break
			}
			seq = append(seq, next)
		}
		// Parse: seq should be "<mod>;<code>" (b2 is the first byte)
		// Full param after "27;" is string(seq), parse mod and code
		parts := string(seq)
		var mod, code int
		if n, _ := fmt.Sscanf(parts, "%d;%d", &mod, &code); n == 2 {
			a.handleModifyOtherKeys(handleModifyOtherKeysOptions{mod: mod, code: code})
		}
		return
	}

	// Not a bracketed paste, might be another sequence starting with 2
	// e.g., Insert key (ESC [ 2 ~)
	if b0 == '~' {
		// ESC [ 2 ~ = Insert
		return
	}
}

func (a *App) handleModifiedCSI(readByte func() (byte, bool)) {
	// We've read ESC [ 1, expect ; <mod> <letter>
	semi, ok := readByte()
	if !ok {
		return
	}
	if semi != ';' {
		return
	}
	modByte, ok := readByte()
	if !ok {
		return
	}
	letter, ok := readByte()
	if !ok {
		return
	}

	if a.awaitingApproval {
		return
	}

	modNum := int(modByte - '0')
	modNum-- // CSI encoding
	isAlt := modNum&2 != 0
	isCtrl := modNum&4 != 0

	switch letter {
	case 'C': // Right
		if isCtrl || isAlt {
			a.moveWordRight()
		} else if a.cursor < len(a.input) {
			a.cursor++
		}
		a.renderInput()
	case 'D': // Left
		if isCtrl || isAlt {
			a.moveWordLeft()
		} else if a.cursor > 0 {
			a.cursor--
		}
		a.renderInput()
	case 'H': // Home
		a.cursor = 0
		a.renderInput()
	case 'F': // End
		a.cursor = len(a.input)
		a.renderInput()
	}
}

// ─── Paste handling ───

func (a *App) handlePaste(content string) {
	if a.awaitingApproval {
		return
	}
	// Normalize line endings: strip \r so CRLF becomes LF and lone CR becomes nothing.
	// Without this, \r in the input buffer is rendered literally by the terminal,
	// causing the cursor to jump to column 0 and overwrite visible text.
	content = strings.ReplaceAll(content, "\r", "")

	// Check if the pasted content is file path(s) (e.g. drag-and-drop from Finder).
	// Handles both single paths and multiple newline-separated paths.
	trimmed := strings.TrimSpace(content)
	if trimmed != "" {
		if placeholder, ok := a.tryAttachFile(trimmed); ok {
			a.insertText(placeholder)
			a.autocompleteIdx = 0
			a.renderInput()
			return
		}
		// Try as multiple newline-separated file paths.
		if lines := strings.Split(trimmed, "\n"); len(lines) > 1 {
			var placeholders []string
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if p, ok := a.tryAttachFile(line); ok {
					placeholders = append(placeholders, p)
				}
			}
			if len(placeholders) > 0 {
				a.insertText(strings.Join(placeholders, " "))
				a.autocompleteIdx = 0
				a.renderInput()
				return
			}
		}
	}

	// If the paste is empty and the clipboard has image data (e.g. screenshot),
	// save the image to a temp file and attach it.
	if trimmed == "" && clipboardHasImage() {
		path, err := a.clipboardSaveImage()
		if err == nil {
			if placeholder, ok := a.tryAttachFile(path); ok {
				a.insertText(placeholder)
				a.autocompleteIdx = 0
				a.renderInput()
				return
			}
		}
	}

	if len(content) >= a.config.PasteCollapseMinChars {
		a.pasteCount++
		if a.pasteStore == nil {
			a.pasteStore = make(map[int]string)
		}
		a.pasteStore[a.pasteCount] = content
		content = fmt.Sprintf("[pasted #%d | %d chars]", a.pasteCount, len(content))
	}
	a.insertText(content)
	a.autocompleteIdx = 0
	a.renderInput()
}
