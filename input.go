package main

import (
	"os"
	"unicode/utf8"
)

// Key represents a special key code.
type Key int

const (
	KeyNone Key = iota
	KeyRune     // regular character in EventKey.Rune
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

// Modifier flags for key events.
type Modifier int

const (
	ModShift Modifier = 1 << iota
	ModAlt
	ModCtrl
)

// EventKey represents a key press.
type EventKey struct {
	Key  Key
	Rune rune
	Mod  Modifier
}

// EventPaste represents a bracketed paste.
type EventPaste struct {
	Content string
}

// EventResize represents a terminal size change.
type EventResize struct {
	Width  int
	Height int
}

// readInput reads raw bytes from stdin and sends parsed events to the channel.
// It runs until stdin is closed or an error occurs.
func readInput(keys chan<- EventKey, pastes chan<- EventPaste, stop <-chan struct{}) {
	buf := make([]byte, 256)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		parseInput(buf[:n], keys, pastes)

		// Check if we should stop (non-blocking).
		select {
		case <-stop:
			return
		default:
		}
	}
}

// parseInput parses raw terminal bytes into key and paste events.
func parseInput(data []byte, keys chan<- EventKey, pastes chan<- EventPaste) {
	i := 0
	for i < len(data) {
		// Check for bracketed paste start: ESC [ 200 ~
		if i+6 <= len(data) && data[i] == 0x1b && data[i+1] == '[' && data[i+2] == '2' && data[i+3] == '0' && data[i+4] == '0' && data[i+5] == '~' {
			i += 6
			// Read until paste end: ESC [ 201 ~
			var content []byte
			for i < len(data) {
				if i+6 <= len(data) && data[i] == 0x1b && data[i+1] == '[' && data[i+2] == '2' && data[i+3] == '0' && data[i+4] == '1' && data[i+5] == '~' {
					i += 6
					break
				}
				content = append(content, data[i])
				i++
			}
			pastes <- EventPaste{Content: string(content)}
			continue
		}

		// ESC sequence
		if data[i] == 0x1b {
			if i+1 >= len(data) {
				keys <- EventKey{Key: KeyEscape}
				i++
				continue
			}

			// Alt+key: ESC followed by printable char
			if data[i+1] >= 0x20 && data[i+1] < 0x7f && (i+2 >= len(data) || data[i+1] != '[') {
				// Alt+Enter is ESC followed by CR
				if data[i+1] == '\r' {
					keys <- EventKey{Key: KeyEnter, Mod: ModAlt}
					i += 2
					continue
				}
				keys <- EventKey{Key: KeyRune, Rune: rune(data[i+1]), Mod: ModAlt}
				i += 2
				continue
			}

			// CSI sequence: ESC [
			if data[i+1] == '[' {
				key, consumed := parseCSI(data[i+2:])
				if consumed > 0 {
					keys <- key
					i += 2 + consumed
					continue
				}
			}

			// ESC O sequences (SS3)
			if data[i+1] == 'O' && i+2 < len(data) {
				switch data[i+2] {
				case 'H':
					keys <- EventKey{Key: KeyHome}
					i += 3
					continue
				case 'F':
					keys <- EventKey{Key: KeyEnd}
					i += 3
					continue
				}
			}

			// Alt+Enter: ESC CR
			if data[i+1] == '\r' {
				keys <- EventKey{Key: KeyEnter, Mod: ModAlt}
				i += 2
				continue
			}

			keys <- EventKey{Key: KeyEscape}
			i++
			continue
		}

		// Ctrl+key
		if data[i] < 0x20 {
			switch data[i] {
			case '\r', '\n':
				keys <- EventKey{Key: KeyEnter}
			case '\t':
				keys <- EventKey{Key: KeyTab}
			case 0x7f:
				keys <- EventKey{Key: KeyBackspace}
			case 0x01: // Ctrl+A
				keys <- EventKey{Key: KeyHome, Mod: ModCtrl}
			case 0x02: // Ctrl+B
				keys <- EventKey{Key: KeyLeft, Mod: ModCtrl}
			case 0x03: // Ctrl+C
				keys <- EventKey{Key: KeyRune, Rune: 'c', Mod: ModCtrl}
			case 0x04: // Ctrl+D
				keys <- EventKey{Key: KeyRune, Rune: 'd', Mod: ModCtrl}
			case 0x05: // Ctrl+E
				keys <- EventKey{Key: KeyEnd, Mod: ModCtrl}
			case 0x06: // Ctrl+F
				keys <- EventKey{Key: KeyRight, Mod: ModCtrl}
			case 0x08: // Ctrl+H (backspace on some terminals)
				keys <- EventKey{Key: KeyBackspace}
			case 0x0b: // Ctrl+K
				keys <- EventKey{Key: KeyRune, Rune: 'k', Mod: ModCtrl}
			case 0x15: // Ctrl+U
				keys <- EventKey{Key: KeyRune, Rune: 'u', Mod: ModCtrl}
			case 0x17: // Ctrl+W
				keys <- EventKey{Key: KeyRune, Rune: 'w', Mod: ModCtrl}
			default:
				// Other ctrl combos: send as Ctrl+letter
				keys <- EventKey{Key: KeyRune, Rune: rune(data[i] + 0x60), Mod: ModCtrl}
			}
			i++
			continue
		}

		// DEL
		if data[i] == 0x7f {
			keys <- EventKey{Key: KeyBackspace}
			i++
			continue
		}

		// Regular UTF-8 character
		r, size := utf8.DecodeRune(data[i:])
		if r != utf8.RuneError {
			keys <- EventKey{Key: KeyRune, Rune: r}
			i += size
		} else {
			i++
		}
	}
}

// parseCSI parses a CSI (Control Sequence Introducer) escape sequence.
// data starts after "ESC [". Returns the parsed key event and bytes consumed.
func parseCSI(data []byte) (EventKey, int) {
	if len(data) == 0 {
		return EventKey{}, 0
	}

	// Simple sequences: ESC [ X
	switch data[0] {
	case 'A':
		return EventKey{Key: KeyUp}, 1
	case 'B':
		return EventKey{Key: KeyDown}, 1
	case 'C':
		return EventKey{Key: KeyRight}, 1
	case 'D':
		return EventKey{Key: KeyLeft}, 1
	case 'H':
		return EventKey{Key: KeyHome}, 1
	case 'F':
		return EventKey{Key: KeyEnd}, 1
	case 'Z': // Shift+Tab
		return EventKey{Key: KeyTab, Mod: ModShift}, 1
	}

	// Numeric sequences: ESC [ <number> ~  or ESC [ 1 ; <mod> <letter>
	// Collect digits and semicolons
	j := 0
	for j < len(data) && ((data[j] >= '0' && data[j] <= '9') || data[j] == ';') {
		j++
	}
	if j >= len(data) {
		return EventKey{}, 0
	}

	params := string(data[:j])
	final := data[j]

	// ESC [ <number> ~
	if final == '~' {
		switch params {
		case "2":
			return EventKey{Key: KeyInsert}, j + 1
		case "3":
			return EventKey{Key: KeyDelete}, j + 1
		case "5":
			return EventKey{Key: KeyPgUp}, j + 1
		case "6":
			return EventKey{Key: KeyPgDown}, j + 1
		}
		return EventKey{}, j + 1 // unknown, consume but ignore
	}

	// ESC [ 1 ; <mod> <letter> — modified keys
	if final >= 'A' && final <= 'Z' {
		mod := parseCSIMod(params)
		switch final {
		case 'A':
			return EventKey{Key: KeyUp, Mod: mod}, j + 1
		case 'B':
			return EventKey{Key: KeyDown, Mod: mod}, j + 1
		case 'C':
			return EventKey{Key: KeyRight, Mod: mod}, j + 1
		case 'D':
			return EventKey{Key: KeyLeft, Mod: mod}, j + 1
		case 'H':
			return EventKey{Key: KeyHome, Mod: mod}, j + 1
		case 'F':
			return EventKey{Key: KeyEnd, Mod: mod}, j + 1
		}
	}

	return EventKey{}, j + 1
}

// parseCSIMod extracts modifier flags from CSI parameters like "1;2" or "1;5".
func parseCSIMod(params string) Modifier {
	// Look for the modifier number after the semicolon
	idx := 0
	for idx < len(params) {
		if params[idx] == ';' {
			idx++
			break
		}
		idx++
	}
	if idx >= len(params) {
		return 0
	}

	modNum := 0
	for idx < len(params) && params[idx] >= '0' && params[idx] <= '9' {
		modNum = modNum*10 + int(params[idx]-'0')
		idx++
	}

	// CSI modifier encoding: value = 1 + sum of modifier bits
	// 2=Shift, 3=Alt, 4=Shift+Alt, 5=Ctrl, 6=Shift+Ctrl, 7=Alt+Ctrl, 8=Shift+Alt+Ctrl
	var mod Modifier
	modNum-- // subtract 1 to get the actual modifier bits
	if modNum&1 != 0 {
		mod |= ModShift
	}
	if modNum&2 != 0 {
		mod |= ModAlt
	}
	if modNum&4 != 0 {
		mod |= ModCtrl
	}
	return mod
}
