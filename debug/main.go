package main

import (
	"fmt"
	"os"
	"unicode/utf8"

	"golang.org/x/term"
)

func main() {
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error entering raw mode: %v\n", err)
		os.Exit(1)
	}
	defer term.Restore(fd, oldState)

	fmt.Print("Press keys to see their byte representation (ctrl+c to quit):\r\n\r\n")

	buf := make([]byte, 32)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			break
		}
		bytes := buf[:n]

		// Ctrl+C
		if n == 1 && bytes[0] == 3 {
			fmt.Print("\r\nBye.\r\n")
			break
		}

		// Show hex bytes
		fmt.Printf("bytes=[% x]", bytes)

		// Try to decode as UTF-8 text
		if utf8.Valid(bytes) {
			fmt.Printf("  text=%q", string(bytes))
		}

		fmt.Print("\r\n")
	}
}
