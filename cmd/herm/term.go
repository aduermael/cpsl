// term.go provides terminal dimension helpers for the interactive TUI,
// wrapping golang.org/x/term.
package main

import (
	"os"

	"golang.org/x/term"
)

// getTerminalHeight returns the current terminal height.
func getTerminalHeight() int {
	_, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return 24
	}
	return h
}
