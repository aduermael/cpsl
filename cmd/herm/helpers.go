// helpers.go provides standalone utility functions used across the herm TUI:
// duration formatting, string truncation, debug logging, git helpers, and
// terminal width detection.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
	"github.com/rivo/uniseg"
)

// ansiEscRe matches ANSI escape sequences (CSI and OSC).
var ansiEscRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]|\x1b\].*?\x1b\\`)

// visibleWidth returns the visual column width of s, ignoring ANSI escapes.
func visibleWidth(s string) int {
	return uniseg.StringWidth(ansiEscRe.ReplaceAllString(s, ""))
}

// formatDuration returns a human-readable duration string, or "" if under 500ms.
func formatDuration(d time.Duration) string {
	if d < 500*time.Millisecond {
		return ""
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%02ds", m, s)
}

// truncateWithEllipsisOptions is the parameter bundle for truncateWithEllipsis.
type truncateWithEllipsisOptions struct {
	s      string
	maxLen int
}

func truncateWithEllipsis(opts truncateWithEllipsisOptions) string {
	s, maxLen := opts.s, opts.maxLen
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}

	if maxLen <= 0 {
		return ""
	}
	if maxLen == 1 {
		return "…"
	}
	return string(runes[:maxLen-1]) + "…"
}

// truncateVisualOptions is the parameter bundle for truncateVisual.
type truncateVisualOptions struct {
	s       string
	maxCols int
}

// truncateVisual truncates s to at most maxCols visible terminal columns,
// appending "…" if truncation occurs. It is ANSI-escape-aware: escape sequences
// do not count toward visible width.
func truncateVisual(opts truncateVisualOptions) string {
	s, maxCols := opts.s, opts.maxCols
	if maxCols <= 0 {
		return ""
	}
	if visibleWidth(s) <= maxCols {
		return s
	}
	// Walk byte-by-byte, skipping ANSI sequences, collecting runes until
	// we would exceed maxCols-1 columns (reserving 1 for the "…").
	b := []byte(s)
	n := len(b)
	i := 0
	var out strings.Builder
	cols := 0
	for i < n {
		if b[i] == 0x1b {
			// Consume the escape sequence without counting columns.
			j := i + 1
			if j < n && b[j] == '[' {
				j++
				for j < n && !isCSIFinal(b[j]) {
					j++
				}
				if j < n {
					j++
				}
			} else if j < n && b[j] == ']' {
				j++
				for j+1 < n {
					if b[j] == 0x1b && b[j+1] == '\\' {
						j += 2
						break
					}
					j++
				}
			}
			out.Write(b[i:j])
			i = j
			continue
		}
		r, size := utf8.DecodeRune(b[i:])
		rw := uniseg.StringWidth(string(r))
		if cols+rw > maxCols-1 {
			break
		}
		out.WriteRune(r)
		cols += rw
		i += size
	}
	out.WriteString("…")
	return out.String()
}

// ─── Debug logging ───

var debugEnabled = os.Getenv("HERM_DEBUG") != ""

func debugLog(format string, args ...any) {
	if !debugEnabled {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(home, ".herm-debug.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	ts := time.Now().Format("2006-01-02T15:04:05.000")
	fmt.Fprintf(f, "[%s] %s\n", ts, fmt.Sprintf(format, args...))
}

// truncateForLogOptions is the parameter bundle for truncateForLog.
type truncateForLogOptions struct {
	s   string
	max int
}

func truncateForLog(opts truncateForLogOptions) string {
	s, max := opts.s, opts.max
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// ─── Git helpers ───

func gitRepoRoot() string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ─── Terminal helpers ───

// debouncer delays a function call, resetting the timer on each trigger.
type debouncer struct {
	delay time.Duration
	fire  func()
	timer *time.Timer
}

// newDebouncerOptions is the parameter bundle for newDebouncer.
type newDebouncerOptions struct {
	delay time.Duration
	fire  func()
}

func newDebouncer(opts newDebouncerOptions) *debouncer {
	return &debouncer{delay: opts.delay, fire: opts.fire}
}

func (d *debouncer) Trigger() {
	if d.timer != nil {
		d.timer.Stop()
	}
	d.timer = time.AfterFunc(d.delay, d.fire)
}

// flushStdin discards any bytes pending in the terminal input buffer.
// A brief pause lets in-flight terminal responses (e.g. DSR cursor position
// reports triggered by alt-screen transitions) arrive before we drain them.
func flushStdin(fd int) {
	time.Sleep(50 * time.Millisecond)
	if err := syscall.SetNonblock(fd, true); err != nil {
		return
	}
	defer syscall.SetNonblock(fd, false)
	buf := make([]byte, 256)
	for {
		n, err := syscall.Read(fd, buf)
		if n <= 0 || err != nil {
			return
		}
	}
}

func getWidth() int {
	w, _, err := term.GetSize(int(os.Stdin.Fd()))
	if err != nil {
		return 80
	}
	return w
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
