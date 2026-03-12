package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"
	"langdag.com/langdag"
	"langdag.com/langdag/types"
	"github.com/rivo/uniseg"
)

// ─── Constants ───

const (
	promptPrefix       = "▸ "
	promptPrefixCols   = 2
	charsPerToken      = 4 // rough estimate for context bar
	maxAttachmentBytes = 20 << 20 // 20 MB
)

// ─── Block and message types ───

type chatMsgKind int

const (
	msgUser chatMsgKind = iota
	msgAssistant
	msgToolCall
	msgToolResult
	msgInfo
	msgSystemPrompt
	msgSuccess
	msgError
)

type chatMessage struct {
	kind      chatMsgKind
	content   string
	isError   bool // for tool results
	leadBlank bool // blank line before this message
}

// ─── App modes ───

type appMode int

const (
	modeChat appMode = iota
	modeConfig
	modeModel
	modeWorktrees
	modeBranches
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

// ─── Visual line wrapping (from simple-chat) ───

type vline struct {
	start    int // rune index of first char
	length   int // number of runes
	startCol int // visual column where text starts
}

// getVisualLines splits the input runes into visual lines, accounting for
// the prompt prefix on the first line and terminal-width wrapping.
// It prefers word boundaries (spaces) for wrapping, falling back to
// character-level breaks for words longer than the available width.
func getVisualLines(input []rune, cursor int, width int) []vline {
	var lines []vline
	start := 0
	startCol := promptPrefixCols
	length := 0
	lastSpaceIdx := -1

	for i, r := range input {
		if r == '\n' {
			lines = append(lines, vline{start, length, startCol})
			start = i + 1
			startCol = 0
			length = 0
			lastSpaceIdx = -1
			continue
		}
		length++
		if r == ' ' {
			lastSpaceIdx = i
		}
		if startCol+length >= width {
			if lastSpaceIdx >= start {
				// Word wrap: break after the last space
				wrapLen := lastSpaceIdx - start + 1
				lines = append(lines, vline{start, wrapLen, startCol})
				start = lastSpaceIdx + 1
				length = length - wrapLen
				startCol = 0
				lastSpaceIdx = -1
			} else {
				// No space found, fall back to character-level wrap
				lines = append(lines, vline{start, length, startCol})
				start = i + 1
				startCol = 0
				length = 0
				lastSpaceIdx = -1
			}
		}
	}
	lines = append(lines, vline{start, length, startCol})
	return lines
}

func cursorVisualPos(input []rune, cursor int, width int) (int, int) {
	vlines := getVisualLines(input, cursor, width)
	for i, vl := range vlines {
		end := vl.start + vl.length
		if cursor >= vl.start && cursor <= end {
			// At a line boundary: if this was a soft wrap (not a newline),
			// the cursor belongs on the next line.
			if cursor == end && i < len(vlines)-1 && (end >= len(input) || input[end] != '\n') {
				continue
			}
			return i, vl.startCol + (cursor - vl.start)
		}
	}
	last := len(vlines) - 1
	vl := vlines[last]
	return last, vl.startCol + vl.length
}

// ansiEscRe matches ANSI escape sequences (CSI and OSC).
var ansiEscRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]|\x1b\].*?\x1b\\`)

// visibleWidth returns the visual column width of s, ignoring ANSI escapes.
func visibleWidth(s string) int {
	return uniseg.StringWidth(ansiEscRe.ReplaceAllString(s, ""))
}

// padCodeBlockRow pads a code block row with spaces so the background fills
// the full terminal width. It ensures \033[0m comes after the padding.
func padCodeBlockRow(row string, width int) string {
	const reset = "\033[0m"
	stripped := row
	hasReset := strings.HasSuffix(row, reset)
	if hasReset {
		stripped = row[:len(row)-len(reset)]
	}
	vw := visibleWidth(stripped)
	if pad := width - vw; pad > 0 {
		stripped += strings.Repeat(" ", pad)
	}
	return stripped + reset
}

// wrapString splits a string into visual rows of at most `w` columns.
// It is ANSI-aware: escape sequences don't count toward visual width, and
// active styling is re-emitted on continuation lines. Character widths are
// measured with uniseg.StringWidth (so wide chars like emoji count as 2).
// Wrapping prefers word boundaries (spaces); words longer than `w` columns
// fall back to character-level breaking.
func wrapString(s string, startCol int, w int) []string {
	if w <= 0 {
		return []string{s}
	}

	// Split into tokens: ANSI sequences and printable segments.
	type token struct {
		text  string
		isSeq bool
	}
	var tokens []token
	rest := s
	for rest != "" {
		loc := ansiEscRe.FindStringIndex(rest)
		if loc == nil {
			tokens = append(tokens, token{rest, false})
			break
		}
		if loc[0] > 0 {
			tokens = append(tokens, token{rest[:loc[0]], false})
		}
		tokens = append(tokens, token{rest[loc[0]:loc[1]], true})
		rest = rest[loc[1]:]
	}

	var rows []string
	var curLine strings.Builder
	col := startCol
	var activeSeqs []string // stack of active ANSI sequences for re-emit

	flush := func() {
		rows = append(rows, curLine.String())
		curLine.Reset()
		col = 0
		for _, seq := range activeSeqs {
			curLine.WriteString(seq)
		}
	}

	applyANSI := func(seq string) {
		curLine.WriteString(seq)
		if seq == "\033[0m" || seq == "\033[m" {
			activeSeqs = nil
		} else {
			activeSeqs = append(activeSeqs, seq)
		}
	}

	// Word buffer: accumulates parts (text chunks and ANSI sequences) that
	// form a single visual word spanning across tokens.
	type wordPart struct {
		text  string
		isSeq bool
	}
	var wordParts []wordPart
	var wordBuf strings.Builder // accumulates current run of non-space chars
	wordWidth := 0

	flushWordBuf := func() {
		if wordBuf.Len() > 0 {
			wordParts = append(wordParts, wordPart{wordBuf.String(), false})
			wordBuf.Reset()
		}
	}

	commitWord := func() {
		flushWordBuf()
		if wordWidth == 0 {
			// Only ANSI sequences — apply them directly
			for _, p := range wordParts {
				if p.isSeq {
					applyANSI(p.text)
				}
			}
			wordParts = wordParts[:0]
			return
		}
		if wordWidth <= w {
			// Word fits on a full line — move to next line if needed
			if col+wordWidth > w {
				flush()
			}
			for _, p := range wordParts {
				if p.isSeq {
					applyANSI(p.text)
				} else {
					curLine.WriteString(p.text)
				}
			}
			col += wordWidth
		} else {
			// Word wider than line — character-break
			for _, p := range wordParts {
				if p.isSeq {
					applyANSI(p.text)
				} else {
					for _, r := range p.text {
						rw := uniseg.StringWidth(string(r))
						if col+rw > w {
							flush()
						}
						curLine.WriteRune(r)
						col += rw
					}
				}
			}
		}
		wordParts = wordParts[:0]
		wordWidth = 0
	}

	for _, tok := range tokens {
		if tok.isSeq {
			flushWordBuf()
			wordParts = append(wordParts, wordPart{tok.text, true})
			continue
		}
		for _, r := range tok.text {
			if unicode.IsSpace(r) {
				commitWord()
				rw := uniseg.StringWidth(string(r))
				if col+rw > w {
					flush()
				} else {
					curLine.WriteRune(r)
					col += rw
				}
			} else {
				wordBuf.WriteRune(r)
				wordWidth += uniseg.StringWidth(string(r))
			}
		}
	}
	commitWord()
	rows = append(rows, curLine.String())

	if len(rows) == 0 {
		return []string{""}
	}
	return rows
}

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
func buildLogo() []string {
	shA := "\033[38;5;180m" // shell body (warm sand)
	shB := "\033[38;5;223m" // shell highlight (lighter peach)
	ib := "\033[48;5;60m"   // interior bg (grey-blue)
	ey := "\033[38;5;232m"  // black eyes
	tb := "\033[48;5;60m"   // tentacle bg
	r := "\033[0m"
	return []string{
		"",
		"    " + shA + "▄" + shB + "███" + shA + "▄" + r + " ░▄▀▀▒█▀▄░▄▀▀░█▒░",
		"  " + shA + "▄██" + ib + ey + "• •" + r + shA + "█" + r + " ░▀▄▄░█▀▒▒▄██▒█▄▄",
		" " + shA + "▀" + shB + "██" + shA + "█" + tb + shA + "▌▌▌" + r + shA + "█" + r + " Contained Coding Agent",
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

// ─── Commands and autocomplete ───

var commands = []string{"/branches", "/clear", "/compact", "/config", "/model", "/session", "/shell", "/usage", "/worktrees"}
var sessionSubcommands = []string{"/session list", "/session load", "/session show"}

func filterCommands(prefix string) []string {
	var matches []string
	for _, cmd := range commands {
		if strings.HasPrefix(cmd, prefix) {
			matches = append(matches, cmd)
		}
	}
	// Only show session subcommands when /session is the sole base match.
	if len(matches) == 1 && matches[0] == "/session" {
		matches = matches[:0]
		all := append([]string{"/session"}, sessionSubcommands...)
		for _, cmd := range all {
			if strings.HasPrefix(cmd, prefix) {
				matches = append(matches, cmd)
			}
		}
	}
	return matches
}

var pasteplaceholderRe = regexp.MustCompile(`\[pasted #(\d+) \| \d+ chars\]`)

func expandPastes(s string, store map[int]string) string {
	return pasteplaceholderRe.ReplaceAllStringFunc(s, func(match string) string {
		sub := pasteplaceholderRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		id, err := strconv.Atoi(sub[1])
		if err != nil {
			return match
		}
		if content, ok := store[id]; ok {
			return content
		}
		return match
	})
}

// ─── Attachment helpers ───

// isFilePath reports whether s looks like an absolute file path that exists on disk.
// It handles shell-escaped paths (backslash-spaces from terminal drag-drop) and
// tilde-prefixed home-dir paths.
func isFilePath(s string) (string, bool) {
	// Unescape backslash-spaces (common in terminal drag-drop).
	p := strings.ReplaceAll(s, "\\ ", " ")
	// Expand tilde.
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	if !filepath.IsAbs(p) {
		return "", false
	}
	info, err := os.Stat(p)
	if err != nil || info.IsDir() {
		return "", false
	}
	return p, true
}

// isImageExt reports whether the file extension indicates an image format.
func isImageExt(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tiff", ".svg":
		return true
	}
	return false
}

// mimeForExt returns the MIME type for a file based on its extension.
func mimeForExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".tiff":
		return "image/tiff"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".txt":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv"
	default:
		return "application/octet-stream"
	}
}

// attachmentPlaceholderRe matches [Image #N] and [File #N] placeholders.
var attachmentPlaceholderRe = regexp.MustCompile(`\[(Image|File) #(\d+)\]`)

// expandAttachments takes a message string (already paste-expanded) and the
// attachment store. If there are no attachment placeholders it returns the
// string as-is. Otherwise it splits the text on placeholders and builds a JSON
// content-block array: text segments become {"type":"text"} blocks, image
// attachments become {"type":"image"} blocks, and file attachments become
// {"type":"document"} blocks. The returned JSON string is understood by
// langdag's contentToRawMessage() which passes arrays through as-is.
func expandAttachments(s string, store map[int]Attachment) string {
	if len(store) == 0 {
		return s
	}
	locs := attachmentPlaceholderRe.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return s
	}

	type block map[string]string
	var blocks []block

	addText := func(t string) {
		if t != "" {
			blocks = append(blocks, block{"type": "text", "text": t})
		}
	}

	prev := 0
	for _, loc := range locs {
		// loc[0..1] = full match, loc[2..3] = kind (Image/File), loc[4..5] = ID
		addText(s[prev:loc[0]])
		idStr := s[loc[4]:loc[5]]
		id, err := strconv.Atoi(idStr)
		if err != nil {
			addText(s[loc[0]:loc[1]])
			prev = loc[1]
			continue
		}
		att, ok := store[id]
		if !ok {
			addText(s[loc[0]:loc[1]])
			prev = loc[1]
			continue
		}
		if att.IsImage {
			blocks = append(blocks, block{
				"type":       "image",
				"media_type": att.MediaType,
				"data":       att.Data,
			})
		} else {
			blocks = append(blocks, block{
				"type":       "document",
				"media_type": att.MediaType,
				"data":       att.Data,
			})
		}
		prev = loc[1]
	}
	addText(s[prev:])

	out, err := json.Marshal(blocks)
	if err != nil {
		return s
	}
	return string(out)
}

// ─── Tool result helpers ───

func toolCallSummary(toolName string, input json.RawMessage) string {
	switch toolName {
	case "bash":
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &in) == nil && in.Command != "" {
			cmd := in.Command
			if len(cmd) > 120 {
				cmd = cmd[:120] + "..."
			}
			return fmt.Sprintf("~ $ %s", cmd)
		}
	case "git":
		var in struct {
			Subcommand string   `json:"subcommand"`
			Args       []string `json:"args,omitempty"`
		}
		if json.Unmarshal(input, &in) == nil && in.Subcommand != "" {
			parts := append([]string{"git", in.Subcommand}, in.Args...)
			cmd := strings.Join(parts, " ")
			if len(cmd) > 120 {
				cmd = cmd[:120] + "..."
			}
			return fmt.Sprintf("~ %s", cmd)
		}
	}
	return fmt.Sprintf("~ %s", toolName)
}

func collapseToolResult(result string) string {
	lines := strings.Split(result, "\n")
	if len(lines) <= 4 {
		return compactLineNumbers(result)
	}
	if len(lines) == 5 {
		// Show first 2 + last 3 (all 5, no ellipsis needed)
		return compactLineNumbers(strings.Join(lines[:2], "\n") + "\n" + strings.Join(lines[2:], "\n"))
	}
	// >5: first 2 + ... + last 2
	head := strings.Join(lines[:2], "\n")
	tail := strings.Join(lines[len(lines)-2:], "\n")
	return compactLineNumbers(fmt.Sprintf("%s\n...\n%s", head, tail))
}

// catNPadRe matches cat-n style line numbers with leading whitespace (e.g. "     1\t").
var catNPadRe = regexp.MustCompile(`(?m)^ +(\d+)\t`)

// compactLineNumbers strips excess leading whitespace from cat-n style
// line-numbered output (e.g. "     1\tcode" → "1 code") for display.
func compactLineNumbers(s string) string {
	return catNPadRe.ReplaceAllString(s, "$1 ")
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
func renderToolBox(title, content string, maxWidth int, isError bool) string {
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
		for _, line := range strings.Split(content, "\n") {
			b.WriteByte('\n')
			b.WriteString(contentStyle)
			b.WriteString(line)
			b.WriteString(reset)
		}
	}

	// Bottom border: └─...─┘
	b.WriteByte('\n')
	b.WriteString(borderStyle)
	b.WriteString("└")
	b.WriteString(strings.Repeat("─", innerWidth))
	b.WriteString("┘")
	b.WriteString(reset)

	return b.String()
}

func truncateWithEllipsis(s string, maxLen int) string {
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

// ─── Async message types ───

type sweScoresMsg struct {
	scores map[string]float64
	err    error
}

type ctrlCExpiredMsg struct{}

type containerReadyMsg struct {
	client       *ContainerClient
	worktreePath string
	imageName    string
}

type containerErrMsg struct {
	err error
}

type containerStatusMsg struct {
	text string
}

type statusInfo struct {
	Branch       string
	PRNumber     int
	WorktreeName string
	ActiveCount  int
	TotalCount   int
	DiffAdd      int
	DiffDel      int
}

type statusInfoMsg struct {
	info statusInfo
}

type worktreeListMsg struct {
	items []WorktreeInfo
	err   error
}

type branchListMsg struct {
	items         []string
	currentBranch string
	err           error
}

type branchCheckoutMsg struct {
	branch string
	err    error
}

type workspaceMsg struct {
	worktreePath string
}

type langdagReadyMsg struct {
	client   *langdag.Client
	provider string
	err      error
}

type catalogMsg struct {
	catalog *langdag.ModelCatalog
}

type resizeMsg struct{}

// ─── Debug logging ───

var debugEnabled = os.Getenv("CPSL_DEBUG") != ""

func debugLog(format string, args ...any) {
	if !debugEnabled {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(home, ".cpsl-debug.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	ts := time.Now().Format("2006-01-02T15:04:05.000")
	fmt.Fprintf(f, "[%s] %s\n", ts, fmt.Sprintf(format, args...))
}

func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// wrapLineCount returns the number of visual lines that `line` would occupy
// when word-wrapped to `width` columns. It delegates to wrapString.
func wrapLineCount(line string, width int) int {
	return len(wrapString(line, 0, width))
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

// ─── Async init commands ───

func resolveWorkspaceCmd(cfg Config) workspaceMsg {
	if repoRoot := gitRepoRoot(); repoRoot != "" {
		ensureGitignoreLock(repoRoot)
		return workspaceMsg{worktreePath: repoRoot}
	}
	cwd, _ := os.Getwd()
	return workspaceMsg{worktreePath: cwd}
}

func bootContainerCmd(workspace string, sessionID string, ch chan<- any) {
	ch <- containerStatusMsg{text: "checking docker…"}

	client := NewContainerClient(ContainerConfig{Image: defaultContainerImage})

	if !client.IsAvailable() {
		ch <- containerStatusMsg{text: "docker not running"}
		ch <- containerErrMsg{err: fmt.Errorf(
			"Docker is not running. Please start Docker Desktop and try again.")}
		return
	}

	// Build from .cpsl/Dockerfile (write base template if none exists).
	imageName := buildContainerImage(workspace, ch)
	if imageName != "" {
		client.mu.Lock()
		client.config.Image = imageName
		client.mu.Unlock()
	}

	ch <- containerStatusMsg{text: "starting…"}

	attachDir := filepath.Join(workspace, ".cpsl", "attachments", sessionID)
	_ = os.MkdirAll(attachDir, 0o755)

	mounts := []MountSpec{
		{Source: workspace, Destination: "/workspace", ReadOnly: false},
		{Source: attachDir, Destination: "/attachments", ReadOnly: true},
	}

	if err := client.Start(workspace, mounts); err != nil {
		ch <- containerStatusMsg{text: "start failed"}
		ch <- containerErrMsg{err: fmt.Errorf("starting container: %w", err)}
		return
	}

	ch <- containerReadyMsg{client: client, worktreePath: workspace, imageName: imageName}
}

// buildContainerImage builds a Docker image from .cpsl/Dockerfile in the workspace.
// If no Dockerfile exists, it writes the embedded base template first.
// Image tag is deterministic: cpsl-<projectID[:8]>:<sha256[:12]> based on Dockerfile content.
// If the image already exists (docker image inspect), the build is skipped.
// Returns the built image name, or empty string on failure (caller falls back to raw image).
func buildContainerImage(workspace string, ch chan<- any) string {
	cpslDir := filepath.Join(workspace, ".cpsl")
	dockerfilePath := filepath.Join(cpslDir, "Dockerfile")

	// Write the embedded base template if no Dockerfile exists.
	if _, err := os.Stat(dockerfilePath); os.IsNotExist(err) {
		_ = os.MkdirAll(cpslDir, 0o755)
		if err := os.WriteFile(dockerfilePath, []byte(BaseDockerfile), 0o644); err != nil {
			return ""
		}
	}

	// Read Dockerfile content and compute hash for deterministic tag.
	content, err := os.ReadFile(dockerfilePath)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(content)
	hashStr := hex.EncodeToString(hash[:])[:12]

	// Derive image name from project ID + content hash.
	imageName := "cpsl-local:" + hashStr
	if repoRoot := gitRepoRoot(); repoRoot != "" {
		if projectID, err := ensureProjectID(repoRoot); err == nil && len(projectID) >= 8 {
			imageName = "cpsl-" + projectID[:8] + ":" + hashStr
		}
	}

	// Check if image already exists — skip build if so (cache hit).
	inspectCtx, inspectCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer inspectCancel()
	inspectCmd := dockerCommand(inspectCtx, "docker", "image", "inspect", imageName)
	if inspectCmd.Run() == nil {
		return imageName
	}

	ch <- containerStatusMsg{text: "building image…"}

	buildCtx, buildCancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer buildCancel()

	buildCmd := dockerCommand(buildCtx, "docker", "build",
		"-t", imageName,
		"-f", dockerfilePath,
		workspace,
	)
	var buildStderr bytes.Buffer
	buildCmd.Stderr = &buildStderr

	if err := buildCmd.Run(); err != nil {
		// Build failed — fall back to raw default image.
		ch <- containerStatusMsg{text: "build failed, using default image"}
		return ""
	}

	return imageName
}

func fetchStatusCmd(worktreePath string) statusInfoMsg {
	var info statusInfo

	info.Branch = worktreeBranch(worktreePath)

	ghCmd := exec.Command("gh", "pr", "view", "--json", "number", "-q", ".number")
	ghCmd.Dir = worktreePath
	if out, err := ghCmd.Output(); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil {
			info.PRNumber = n
		}
	}

	diffCmd := exec.Command("git", "diff", "--shortstat", "main")
	diffCmd.Dir = worktreePath
	if out, err := diffCmd.Output(); err == nil {
		line := strings.TrimSpace(string(out))
		if re := regexp.MustCompile(`(\d+) insertion`); re.MatchString(line) {
			if n, err := strconv.Atoi(re.FindStringSubmatch(line)[1]); err == nil {
				info.DiffAdd = n
			}
		}
		if re := regexp.MustCompile(`(\d+) deletion`); re.MatchString(line) {
			if n, err := strconv.Atoi(re.FindStringSubmatch(line)[1]); err == nil {
				info.DiffDel = n
			}
		}
	}

	info.WorktreeName = filepath.Base(worktreePath)

	repoRoot := gitRepoRoot()
	if repoRoot != "" {
		if projectID, err := ensureProjectID(repoRoot); err == nil {
			baseDir := worktreeBaseDir(projectID)
			if wts, err := listWorktrees(baseDir); err == nil {
				info.TotalCount = len(wts)
				for _, wt := range wts {
					if wt.Active {
						info.ActiveCount++
					}
				}
			}
		}
	}

	return statusInfoMsg{info: info}
}

func fetchSWEScoresCmd() sweScoresMsg {
	scores, err := fetchSWEScores()
	return sweScoresMsg{scores: scores, err: err}
}

// ─── Attachment types ───

// Attachment holds metadata and encoded content for a file or image attached to a message.
type Attachment struct {
	Path      string // original file path (may be temp path for clipboard images)
	MediaType string // MIME type (e.g. "image/png", "application/pdf")
	Data      string // base64-encoded file content
	IsImage   bool   // whether this is an image attachment
}

// ─── App struct ───

type App struct {
	// Terminal
	fd       int
	oldState *term.State
	width    int

	// Rendering state (from simple-chat)
	prevRowCount  int
	sepRow        int
	inputStartRow int
	scrollShift   int // rows scrolled off top when content > terminal height

	// Input buffer (from simple-chat)
	input   []rune
	cursor  int
	history *History

	// Event channels
	resultCh chan any
	stopCh   chan struct{}
	quit     bool

	// Stdin goroutine control
	stdinDup *os.File   // dup'd stdin fd for the reader goroutine
	stdinCh  chan byte   // channel carrying bytes from the reader goroutine
	readByte func() (byte, bool)

	// Chat state
	sessionID        string
	messages         []chatMessage
	globalConfig     Config        // loaded from ~/.cpsl/config.json
	projectConfig    ProjectConfig // loaded from <repo>/.cpsl/config.json
	config           Config        // merged effective config (globalConfig + projectConfig)
	repoRoot         string        // git repo root, for project config path
	pasteCount       int
	pasteStore       map[int]string
	attachmentCount  int
	attachments      map[int]Attachment
	mode             appMode
	models           []ModelDef
	sweScores        map[string]float64
	sweLoaded        bool
	container        *ContainerClient
	worktreePath     string
	containerReady      bool
	containerErr        error
	containerStatusText string
	configReady         bool // true after workspace/project config has been merged
	shownInitialModel   bool // true after the startup model line has been displayed
	status           statusInfo
	modelCatalog     *langdag.ModelCatalog
	langdagClient    *langdag.Client
	langdagProvider  string
	agent            *Agent
	agentNodeID      string
	agentRunning     bool
	awaitingApproval bool
	approvalDesc     string
	autocompleteIdx  int
	streamingText    string
	pendingToolCall  string
	needsTextSep     bool
	sessionCostUSD      float64
	lastInputTokens    int // input tokens from most recent API call (context usage)
	sessionInputTokens  int // cumulative input tokens this session
	sessionOutputTokens int // cumulative output tokens this session
	sessionCacheRead    int // cumulative cache read tokens this session
	sessionLLMCalls     int // number of LLM API calls this session
	sessionToolResults  int            // count of tool results this session
	sessionToolBytes    int            // cumulative tool result bytes this session
	sessionToolStats    map[string][2]int // tool name → [count, bytes]
	scratchpad       Scratchpad
	lastModelID      string   // last model used, for detecting changes
	subAgentBuf      string   // accumulates sub-agent streaming text
	subAgentLines    []string // completed lines from sub-agent output
	containerImage   string   // runtime container image name (not persisted)

	// Menu state (for inline menus below input - Phase 3)
	menuLines        []string
	menuHeader       string // optional header row above scrollable items
	menuCursor       int
	menuActive       bool
	menuAction       func(int)
	menuScrollOffset int
	menuSortCol      int        // active sort column (0=name,1=provider,2=price,3=context)
	menuSortAsc      [4]bool    // per-column sort direction: true=ascending
	menuModels       []ModelDef // model list for re-sorting (nil for non-model menus)
	menuActiveID     string     // active model ID for re-sorting

	// Config editor state
	cfgActive     bool
	cfgTab        int
	cfgCursor     int
	cfgEditing    bool
	cfgEditBuf    []rune
	cfgEditCursor int
	cfgDraft        Config
	cfgProjectDraft ProjectConfig

	// Text prompt overlay (e.g. "Enter worktree name:")
	promptLabel    string
	promptCallback func(string) // called with entered text; nil when inactive

	// Ctrl+C double-tap to exit
	ctrlCTime time.Time // when last Ctrl+C was pressed (for double-tap detection)
	ctrlCHint bool      // show "Press Ctrl-C again to exit" hint

	// CLI flags
	displaySystemPrompts bool
}

func newApp() *App {
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("warning: loading config: %v (using defaults)", err)
	}

	var sid [4]byte
	_, _ = rand.Read(sid[:])
	sessID := fmt.Sprintf("%08x", sid)

	return &App{
		sessionID:    sessID,
		globalConfig: cfg,
		config:       cfg, // no project config yet; will merge on workspaceMsg
		resultCh:     make(chan any, 16),
		stopCh:       make(chan struct{}),
	}
}

// ─── Rendering (from simple-chat, adapted) ───

func (a *App) buildBlockRows() []string {
	var rows []string
	rows = append(rows, buildLogo()...)
	inCodeBlock := false
	skipNext := false
	for i, msg := range a.messages {
		if skipNext {
			skipNext = false
			// Still emit trailing blank line logic below.
			goto blankLine
		}

		// Tool call + result pair → render as a single box.
		if msg.kind == msgToolCall {
			title := strings.ReplaceAll(msg.content, "\r", "")
			nextIdx := i + 1
			if nextIdx < len(a.messages) && a.messages[nextIdx].kind == msgToolResult {
				// Paired: render full box.
				result := a.messages[nextIdx]
				content := strings.ReplaceAll(result.content, "\r", "")
				box := renderToolBox(title, content, a.width, result.isError)
				if msg.leadBlank {
					rows = append(rows, "")
				}
				for _, logLine := range strings.Split(box, "\n") {
					rows = append(rows, wrapString(logLine, 0, a.width)...)
				}
				skipNext = true
				goto blankLine
			}
			// Unpaired (in-progress): render just the top border (open box).
			if msg.leadBlank {
				rows = append(rows, "")
			}
			box := renderToolBox(title, "", a.width, false)
			// Strip bottom border — only show top border for in-progress.
			boxLines := strings.Split(box, "\n")
			if len(boxLines) > 1 {
				boxLines = boxLines[:len(boxLines)-1] // remove └...┘
			}
			for _, logLine := range boxLines {
				rows = append(rows, wrapString(logLine, 0, a.width)...)
			}
			goto blankLine
		}

		// Tool result without preceding tool call — render as a standalone box.
		if msg.kind == msgToolResult {
			content := strings.ReplaceAll(msg.content, "\r", "")
			box := renderToolBox("~ result", content, a.width, msg.isError)
			if msg.leadBlank {
				rows = append(rows, "")
			}
			for _, logLine := range strings.Split(box, "\n") {
				rows = append(rows, wrapString(logLine, 0, a.width)...)
			}
			goto blankLine
		}

		{
			rendered := renderMessage(msg)
			for _, logLine := range strings.Split(rendered, "\n") {
				wasInCodeBlock := inCodeBlock
				if msg.kind == msgAssistant {
					var skip bool
					logLine, inCodeBlock, skip = processMarkdownLine(logLine, inCodeBlock)
					if skip {
						continue
					}
				}
				wrapped := wrapString(logLine, 0, a.width)
				if wasInCodeBlock && msg.kind == msgAssistant {
					for j := range wrapped {
						wrapped[j] = padCodeBlockRow(wrapped[j], a.width)
					}
				}
				rows = append(rows, wrapped...)
			}
		}

	blankLine:
		// Add blank line after block, unless:
		// - next message already has leadBlank, or
		// - this is an assistant message followed by another assistant message
		//   (consecutive assistant chunks already contain their own newlines)
		// When we consumed a pair (skipNext was just set), look past the result.
		peekIdx := i + 1
		if skipNext {
			peekIdx = i + 2
		}
		peekHasBlank := peekIdx < len(a.messages) && a.messages[peekIdx].leadBlank
		peekIsAssistant := peekIdx < len(a.messages) && a.messages[peekIdx].kind == msgAssistant
		if !peekHasBlank && !(msg.kind == msgAssistant && peekIsAssistant) {
			rows = append(rows, "")
		}
	}
	// Show streaming text or thinking indicator above the input area
	if a.streamingText != "" {
		for _, logLine := range strings.Split(a.streamingText, "\n") {
			wasInCodeBlock := inCodeBlock
			var skip bool
			logLine, inCodeBlock, skip = processMarkdownLine(logLine, inCodeBlock)
			if !skip {
				wrapped := wrapString(logLine, 0, a.width)
				if wasInCodeBlock {
					for j := range wrapped {
						wrapped[j] = padCodeBlockRow(wrapped[j], a.width)
					}
				}
				rows = append(rows, wrapped...)
			}
		}
		rows = append(rows, "")
	} else if a.agentRunning {
		rows = append(rows, "\033[2;3mthinking...\033[0m")
		rows = append(rows, "")
	}
	// Show live sub-agent activity (capped to 3 lines, dim/italic)
	if subLines := a.subAgentDisplayLines(); len(subLines) > 0 {
		for _, line := range subLines {
			rows = append(rows, wrapString(line, 0, a.width)...)
		}
		rows = append(rows, "")
	}
	return collapseBlankRows(rows)
}

// collapseBlankRows reduces consecutive blank rows to at most one.
// A row is "blank" if it is empty or contains only ANSI reset sequences.
func collapseBlankRows(rows []string) []string {
	out := make([]string, 0, len(rows))
	prevBlank := false
	for _, r := range rows {
		blank := isBlankRow(r)
		if blank && prevBlank {
			continue
		}
		out = append(out, r)
		prevBlank = blank
	}
	return out
}

// isBlankRow reports whether a row is visually empty (empty string or only ANSI escapes).
func isBlankRow(s string) bool {
	return strings.TrimSpace(ansiEscRe.ReplaceAllString(s, "")) == ""
}

// subAgentDisplayLines returns up to 3 dim/italic lines showing live sub-agent activity.
func (a *App) subAgentDisplayLines() []string {
	// Collect all available lines: completed lines + current partial line.
	all := a.subAgentLines
	if a.subAgentBuf != "" {
		all = append(append([]string{}, all...), a.subAgentBuf)
	}
	if len(all) == 0 {
		return nil
	}
	// Take the last 3 lines.
	start := 0
	if len(all) > 3 {
		start = len(all) - 3
	}
	visible := all[start:]
	out := make([]string, len(visible))
	for i, line := range visible {
		// dim (2) + italic (3), with [sub-agent] prefix
		out[i] = "\033[2;3m[sub-agent] " + line + "\033[0m"
	}
	return out
}

// subAgentSummaryLine builds a one-line summary from sub-agent output for the committed messages.
func subAgentSummaryLine(lines []string, buf string) string {
	// Find the first non-empty line of text output.
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Skip tool status lines.
		if strings.HasPrefix(line, "tool:") {
			continue
		}
		if line != "" {
			const maxLen = 80
			if len(line) > maxLen {
				line = line[:maxLen] + "…"
			}
			return "[sub-agent] completed: " + line
		}
	}
	if s := strings.TrimSpace(buf); s != "" {
		const maxLen = 80
		if len(s) > maxLen {
			s = s[:maxLen] + "…"
		}
		return "[sub-agent] completed: " + s
	}
	return "[sub-agent] completed"
}

// refreshModelMenu re-sorts and re-formats the model menu after a sort change.
// Preserves the cursor on the same model.
func (a *App) refreshModelMenu() {
	if len(a.menuModels) == 0 {
		return
	}
	// Remember which model the cursor is on
	var cursorID string
	if a.menuCursor >= 0 && a.menuCursor < len(a.menuModels) {
		cursorID = a.menuModels[a.menuCursor].ID
	}
	asc := a.menuSortAsc[a.menuSortCol]
	sortModelsByCol(a.menuModels, a.menuSortCol, asc)
	header, lines := formatModelMenuLines(a.menuModels, a.menuActiveID, a.menuSortCol, asc)
	a.menuHeader = header
	a.menuLines = lines
	// Restore cursor position
	for i, m := range a.menuModels {
		if m.ID == cursorID {
			a.menuCursor = i
			break
		}
	}
	// Adjust scroll to keep cursor visible
	maxVisible := getTerminalHeight() * 60 / 100
	if maxVisible < 1 {
		maxVisible = 1
	}
	if a.menuCursor < a.menuScrollOffset {
		a.menuScrollOffset = a.menuCursor
	} else if a.menuCursor >= a.menuScrollOffset+maxVisible {
		a.menuScrollOffset = a.menuCursor - maxVisible + 1
	}
	// Persist sort preferences (global-only)
	a.globalConfig.ModelSortCol = sortColNames[a.menuSortCol]
	a.globalConfig.ModelSortDirs = sortAscToMap(a.menuSortAsc)
	a.config = mergeConfigs(a.globalConfig, a.projectConfig)
	_ = saveConfig(a.globalConfig)
}

func (a *App) buildInputRows() []string {
	sep := strings.Repeat("─", a.width)
	rows := []string{sep}

	// Config editor mode replaces input area
	if a.cfgActive {
		rows = append(rows, a.buildConfigRows()...)
		rows = append(rows, sep)
		return rows
	}

	// Menu mode replaces input area
	if a.menuActive && len(a.menuLines) > 0 {
		w := a.width
		if a.menuHeader != "" {
			rows = append(rows, fmt.Sprintf("\033[1m%s\033[0m", truncateWithEllipsis(a.menuHeader, w)))
		}
		maxVisible := getTerminalHeight() * 60 / 100
		if maxVisible < 1 {
			maxVisible = 1
		}
		total := len(a.menuLines)
		end := a.menuScrollOffset + maxVisible
		if end > total {
			end = total
		}
		for i := a.menuScrollOffset; i < end; i++ {
			line := a.menuLines[i]
			if i == a.menuCursor {
				rows = append(rows, fmt.Sprintf("\033[36;1m%s ◆\033[0m", truncateWithEllipsis(line, w-2)))
			} else {
				rows = append(rows, truncateWithEllipsis(line, w))
			}
		}
		first := a.menuScrollOffset + 1
		last := end
		indicator := fmt.Sprintf("(%d->%d / %d)", first, last, total)
		rows = append(rows, fmt.Sprintf("\033[2m%s\033[0m", truncateWithEllipsis(indicator, w)))
		if a.menuModels != nil {
			hints := "←/→ sort column  Tab flip order  Enter select  Esc close"
			rows = append(rows, fmt.Sprintf("\033[2m%s\033[0m", truncateWithEllipsis(hints, w)))
		}
		rows = append(rows, sep)
		return rows
	}

	if a.promptLabel != "" {
		rows = append(rows, fmt.Sprintf("\033[33;1m%s\033[0m", a.promptLabel))
	}

	vlines := getVisualLines(a.input, a.cursor, a.width)
	for i, vl := range vlines {
		line := string(a.input[vl.start : vl.start+vl.length])
		if i == 0 {
			line = promptPrefix + line
		}
		rows = append(rows, line)
	}

	// Approval prompt inside input area
	if a.awaitingApproval {
		rows = append(rows, fmt.Sprintf("\033[33;1mAllow %s? [y/n]\033[0m", a.approvalDesc))
	}

	rows = append(rows, sep)

	// Ctrl+C exit hint (below separator, above status)
	if a.ctrlCHint {
		rows = append(rows, fmt.Sprintf("\033[1;38;5;%dmPress Ctrl-C again to exit\033[0m", 4))
	}

	// Autocomplete (shown below input)
	hasAction := false
	if matches := a.autocompleteMatches(); len(matches) > 0 {
		hasAction = true
		for i, cmd := range matches {
			if i == a.autocompleteIdx {
				rows = append(rows, fmt.Sprintf("\033[36;1m%s ◆\033[0m", cmd))
			} else {
				rows = append(rows, cmd)
			}
		}
	}

	// Status indicators (only when no action is active)
	if !hasAction {
		// Line 1: branch: <name> + cost + progress bar on right
		branchLabel := ""
		branchTextWidth := 0
		if a.status.Branch != "" {
			branchLabel = "\033[2mbranch: " + a.status.Branch + "\033[0m"
			branchTextWidth = 8 + len(a.status.Branch) // "branch: " + name
		}
		costLabel := ""
		costTextWidth := 0
		if a.sessionCostUSD > 0 {
			costStr := formatCost(a.sessionCostUSD)
			costLabel = "  \033[2m" + costStr + "\033[0m"
			costTextWidth = 2 + len(costStr)
		}
		contextTokens := a.lastInputTokens + len(a.input)/charsPerToken
		contextWindow := 200000
		if m := findModelByID(a.models, a.config.resolveActiveModel(a.models)); m != nil {
			contextWindow = m.ContextWindow
		}
		bar := progressBar(contextTokens, contextWindow)
		barWidth := 3
		padding := a.width - branchTextWidth - costTextWidth - barWidth - 1
		if padding < 0 {
			padding = 0
		}
		rows = append(rows, branchLabel+costLabel+strings.Repeat(" ", padding)+bar+" ")

		// Line 2: container status (always shown when we have status text)
		if a.containerStatusText != "" {
			style := "\033[2m" // dim
			if a.containerErr != nil {
				style = "\033[31m" // red
			}
			rows = append(rows, style+"container: "+a.containerStatusText+"\033[0m\033[K")
		}

		// Line 3: worktree: <name> (only when actually in a worktree)
		if a.status.WorktreeName != "" && a.isInWorktree() {
			rows = append(rows, "\033[2mworktree: "+a.status.WorktreeName+"\033[0m\033[K")
		}
	}

	return rows
}

func (a *App) positionCursor(buf *strings.Builder) {
	s := a.scrollShift
	if a.cfgActive {
		if a.cfgEditing {
			// Position cursor in the edit field: separator + tab bar (1) + cursor row
			fieldRow := a.sepRow + 1 + a.cfgCursor + 1 // +1 for tab bar row
			fields := a.cfgCurrentFields()
			col := 0
			if a.cfgCursor < len(fields) {
				col = len(fields[a.cfgCursor].label) + 2 // "label: "
			}
			col += a.cfgEditCursor
			buf.WriteString("\033[?25h")
			buf.WriteString(fmt.Sprintf("\033[%d;%dH", fieldRow-s, col+1))
		} else {
			buf.WriteString("\033[?25l")
			buf.WriteString(fmt.Sprintf("\033[%d;1H", a.sepRow+1-s))
		}
		return
	}
	if a.menuActive && len(a.menuLines) > 0 {
		// Menu between separators — hide cursor
		buf.WriteString("\033[?25l")
		buf.WriteString(fmt.Sprintf("\033[%d;1H", a.sepRow+1-s))
		return
	}
	buf.WriteString("\033[?25h")
	curLine, curCol := cursorVisualPos(a.input, a.cursor, a.width)
	buf.WriteString(fmt.Sprintf("\033[%d;%dH", a.inputStartRow+curLine-s, curCol+1))
}

func (a *App) render() {
	blockRows := a.buildBlockRows()

	a.sepRow = len(blockRows) + 1
	a.inputStartRow = a.sepRow + 1

	inputRows := a.buildInputRows()
	allRows := append(blockRows, inputRows...)
	totalRows := len(allRows)

	th := getTerminalHeight()
	newScrollShift := 0
	if totalRows > th {
		newScrollShift = totalRows - th
	}

	var buf strings.Builder

	if newScrollShift > 0 && a.scrollShift > 0 && newScrollShift >= a.scrollShift {
		// Content overflows and grew or stayed same: write only visible portion.
		// Scroll terminal down if content grew, then overwrite visible rows.
		if extra := newScrollShift - a.scrollShift; extra > 0 {
			buf.WriteString(fmt.Sprintf("\033[%d;1H", th))
			for i := 0; i < extra; i++ {
				buf.WriteString("\r\n")
			}
		}
		visibleRows := allRows[newScrollShift:]
		writeRows(&buf, visibleRows, 1)
	} else {
		// No overflow, or content shrank: write from top.
		if a.scrollShift > 0 {
			buf.WriteString("\033[3J") // clear stale scrollback
		}
		writeRows(&buf, allRows, 1)
	}

	buf.WriteString("\033[0m\033[J") // clear from cursor to end of screen

	a.prevRowCount = totalRows
	a.scrollShift = newScrollShift

	a.positionCursor(&buf)
	os.Stdout.WriteString(buf.String())
}

// renderFull clears scrollback and does a full render. Use on resize.
func (a *App) renderFull() {
	a.scrollShift = 0 // reset so render() writes from top
	os.Stdout.WriteString("\033[3J")
	a.render()
}

func (a *App) renderInput() {
	inputRows := a.buildInputRows()
	totalRows := a.sepRow - 1 + len(inputRows)
	th := getTerminalHeight()

	newScrollShift := 0
	if totalRows > th {
		newScrollShift = totalRows - th
	}

	// If content shrank and we need to un-scroll, do a full render
	if newScrollShift < a.scrollShift {
		a.render()
		return
	}

	// Compute screen position of sepRow using current scroll state
	screenSepRow := a.sepRow - a.scrollShift
	if screenSepRow < 1 {
		a.render()
		return
	}

	var buf strings.Builder
	writeRows(&buf, inputRows, screenSepRow)
	buf.WriteString("\033[0m\033[J") // clear remaining lines

	a.scrollShift = newScrollShift
	a.prevRowCount = totalRows

	a.positionCursor(&buf)
	os.Stdout.WriteString(buf.String())
}

// ─── Input helpers (from simple-chat) ───

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
	lineIdx, col := cursorVisualPos(a.input, a.cursor, a.width)
	if lineIdx == 0 {
		return
	}
	vlines := getVisualLines(a.input, a.cursor, a.width)
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
	lineIdx, _ := cursorVisualPos(a.input, a.cursor, a.width)
	vlines := getVisualLines(a.input, a.cursor, a.width)
	if lineIdx >= len(vlines)-1 {
		return
	}
	_, col := cursorVisualPos(a.input, a.cursor, a.width)
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

// ─── Main event loop ───

func (a *App) Run() error {
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("entering raw mode: %w", err)
	}
	a.fd = fd
	a.oldState = oldState

	startTime := time.Now()

	// Panic-safe terminal restoration
	defer func() {
		if r := recover(); r != nil {
			term.Restore(fd, oldState)
			panic(r)
		}
	}()

	// Enter alt-screen, enable bracketed paste, enable modifyOtherKeys mode 1
	fmt.Print("\033[?1049h")
	fmt.Print("\033[?2004h")
	fmt.Print("\033[>4;2m")
	defer func() {
		fmt.Print("\033[?25h")  // ensure cursor visible on exit
		fmt.Print("\033[>4;0m") // disable modifyOtherKeys
		fmt.Print("\033[?2004l")
		fmt.Print("\033[?1049l")
		end := time.Now()
		fmt.Printf("[CPSL %s -> %s]\r\n",
			startTime.Format("Jan 02 15:04"),
			end.Format("Jan 02 15:04"))
		term.Restore(fd, oldState)
	}()

	a.width = getWidth()

	// SIGWINCH handler with debounce
	sigWinch := make(chan os.Signal, 1)
	signal.Notify(sigWinch, syscall.SIGWINCH)
	resizeDb := newDebouncer(150*time.Millisecond, func() {
		a.resultCh <- resizeMsg{}
	})
	go func() {
		for range sigWinch {
			a.width = getWidth()
			resizeDb.Trigger()
		}
	}()

	// Start async initialization
	a.startInit()

	// Initial render
	a.render()

	// Start the stdin reader goroutine
	a.startStdinReader()

	// Main event loop — selects on stdin, agent events, and async results
	for {
		// If agent is running, select on all channels.
		// Otherwise, just wait for stdin or async results.
		if a.agent != nil && a.agentRunning {
			select {
			case ch, ok := <-a.stdinCh:
				if !ok {
					goto done
				}
				a.drainResults()
				a.drainAgentEvents()
				if a.handleByte(ch, a.stdinCh, a.readByte) {
					goto done
				}
			case event, ok := <-a.agent.Events():
				if ok {
					a.handleAgentEvent(event)
				}
				a.drainResults()
				a.drainAgentEvents()
			case result := <-a.resultCh:
				a.handleResult(result)
				a.drainAgentEvents()
			}
		} else {
			select {
			case ch, ok := <-a.stdinCh:
				if !ok {
					goto done
				}
				a.drainResults()
				a.drainAgentEvents()
				if a.handleByte(ch, a.stdinCh, a.readByte) {
					goto done
				}
			case result := <-a.resultCh:
				a.handleResult(result)
			}
		}
	}
done:

	a.cleanup()
	return nil
}

// handleByte processes a single byte from stdin. Returns true if the app should quit.
func (a *App) handleByte(ch byte, stdinCh chan byte, readByte func() (byte, bool)) bool {
	// Config editor mode intercept (unless a model picker menu is active)
	if a.cfgActive && !a.menuActive {
		a.handleConfigByte(ch, stdinCh, readByte)
		return false
	}

	// Escape sequence
	if ch == '\033' {
		a.handleEscapeSequence(stdinCh, readByte)
		return false
	}

	// Ctrl+D: immediate quit
	if ch == 4 {
		return true
	}

	// Ctrl+C: double-tap to exit, also clears input text
	if ch == 3 {
		// Second Ctrl+C within 2 seconds: quit
		if a.ctrlCHint && time.Since(a.ctrlCTime) < 2*time.Second {
			return true
		}
		// First Ctrl+C: clear input if any, show exit hint
		a.input = nil
		a.cursor = 0
		a.ctrlCHint = true
		a.ctrlCTime = time.Now()
		a.renderInput()
		// Schedule hint removal after 2 seconds
		ch := a.resultCh
		go func() {
			time.Sleep(2 * time.Second)
			ch <- ctrlCExpiredMsg{}
		}()
		return false
	}

	// Any other key clears the Ctrl+C hint
	if a.ctrlCHint {
		a.ctrlCHint = false
		a.ctrlCTime = time.Time{}
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

func (a *App) handlePlainEscape() {
	if a.awaitingApproval {
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

func (a *App) handleEscapeSequence(stdinCh chan byte, readByte func() (byte, bool)) {
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

	if b != '[' {
		// ESC followed by non-[ byte (e.g. Alt+key) — treat as plain escape
		a.handlePlainEscape()
		return
	}

	// CSI sequence: ESC [
	b, ok = readByte()
	if !ok {
		return
	}

	// Check for bracketed paste: ESC [ 2 0 0 ~
	// Also handles modifyOtherKeys: ESC [ 2 7 ; <mod> ; <code> ~
	if b == '2' {
		a.handleCSIDigit2(readByte, a.handlePaste)
		return
	}

	// Modified key sequences: ESC [ 1 ; <mod> <letter>
	if b == '1' {
		a.handleModifiedCSI(readByte)
		return
	}

	// Tilde sequences: ESC [ <number> ~
	if b >= '3' && b <= '6' {
		tilde, ok := readByte()
		if !ok {
			return
		}
		if tilde == '~' {
			switch b {
			case '3': // Delete
				if !a.awaitingApproval {
					a.deleteAtCursor()
					a.renderInput()
				}
			}
		}
		return
	}

	if a.awaitingApproval {
		return
	}

	switch b {
	case 'A': // Up
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
			lineIdx, _ := cursorVisualPos(a.input, a.cursor, a.width)
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
			lineIdx, _ := cursorVisualPos(a.input, a.cursor, a.width)
			vlines := getVisualLines(a.input, a.cursor, a.width)
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
		if a.menuActive && a.menuModels != nil {
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
		if a.menuActive && a.menuModels != nil {
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

// handleModifyOtherKeys processes a modifyOtherKeys sequence (CSI 27;mod;code~).
// With modifyOtherKeys mode 2, the terminal encodes modified keys that would
// otherwise be ambiguous (e.g., Ctrl+C as CSI 27;5;99~ instead of byte 0x03).
// We translate these back to the actions the app already handles.
func (a *App) handleModifyOtherKeys(mod, code int) {
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

func (a *App) handleCSIDigit2(readByte func() (byte, bool), onPaste func(string)) {
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
			a.handleModifyOtherKeys(mod, code)
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

// tryAttachFile checks if s is a valid file path, reads and base64-encodes it,
// stores it in the attachment map, and returns the placeholder string.
func (a *App) tryAttachFile(s string) (string, bool) {
	resolved, ok := isFilePath(s)
	if !ok {
		return "", false
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", false
	}
	if info.Size() > maxAttachmentBytes {
		return fmt.Sprintf("[file too large: %s (%d MB limit)]",
			filepath.Base(resolved), maxAttachmentBytes>>20), true
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", false
	}
	if a.attachments == nil {
		a.attachments = make(map[int]Attachment)
	}
	a.attachmentCount++
	isImg := isImageExt(resolved)
	a.attachments[a.attachmentCount] = Attachment{
		Path:      resolved,
		MediaType: mimeForExt(resolved),
		Data:      base64.StdEncoding.EncodeToString(data),
		IsImage:   isImg,
	}

	// Copy file to host attachment dir for container mount.
	if a.worktreePath != "" {
		dir := a.attachmentDir()
		if err := os.MkdirAll(dir, 0o755); err == nil {
			dst := filepath.Join(dir, filepath.Base(resolved))
			if _, err := os.Stat(dst); err == nil {
				// Collision — prepend attachment ID.
				dst = filepath.Join(dir, fmt.Sprintf("%d-%s", a.attachmentCount, filepath.Base(resolved)))
			}
			_ = os.WriteFile(dst, data, 0o644)
		}
	}

	if isImg {
		return fmt.Sprintf("[Image #%d]", a.attachmentCount), true
	}
	return fmt.Sprintf("[File #%d]", a.attachmentCount), true
}

// attachmentDir returns the host path for this session's attachment files.
func (a *App) attachmentDir() string {
	return filepath.Join(a.worktreePath, ".cpsl", "attachments", a.sessionID)
}

// clipboardHasImage checks if the macOS clipboard contains image data.
func clipboardHasImage() bool {
	out, err := exec.Command("osascript", "-e",
		"clipboard info").Output()
	if err != nil {
		return false
	}
	// clipboard info returns lines like "«class PNGf», 12345"
	s := string(out)
	return strings.Contains(s, "PNGf") || strings.Contains(s, "TIFF") ||
		strings.Contains(s, "GIFf") || strings.Contains(s, "JPEG")
}

// clipboardSaveImage writes macOS clipboard image data to a temp PNG file
// under .cpsl/tmp/ and returns the file path.
func (a *App) clipboardSaveImage() (string, error) {
	tmpDir := filepath.Join(a.worktreePath, ".cpsl", "tmp")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("clipboard-%d.png", time.Now().UnixMilli())
	path := filepath.Join(tmpDir, name)

	script := fmt.Sprintf(`
		set f to POSIX file %q
		try
			set img to the clipboard as «class PNGf»
			set fh to open for access f with write permission
			write img to fh
			close access fh
		on error
			try
				close access f
			end try
			error "no image on clipboard"
		end try
	`, path)
	if err := exec.Command("osascript", "-e", script).Run(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// cleanupTmpDir removes files in .cpsl/tmp/ older than 24 hours.
func cleanupTmpDir(worktreePath string) {
	tmpDir := filepath.Join(worktreePath, ".cpsl", "tmp")
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(tmpDir, e.Name()))
		}
	}
}

func (a *App) handleApprovalByte(ch byte) {
	switch ch {
	case 'y', 'Y':
		a.awaitingApproval = false
		if a.agent != nil {
			a.agent.Approve(ApprovalResponse{Approved: true})
		}
		a.messages = append(a.messages, chatMessage{kind: msgSuccess, content: "Approved"})
		a.render()
	case 'n', 'N':
		a.awaitingApproval = false
		if a.agent != nil {
			a.agent.Approve(ApprovalResponse{Approved: false})
		}
		a.messages = append(a.messages, chatMessage{kind: msgError, content: "Denied"})
		a.render()
	}
}

func (a *App) handleEnter() {
	// Text prompt active — submit to callback.
	if a.promptCallback != nil {
		val := strings.TrimSpace(a.inputValue())
		cb := a.promptCallback
		a.promptLabel = ""
		a.promptCallback = nil
		a.resetInput()
		if val != "" {
			cb(val)
		}
		a.renderInput()
		return
	}

	// Autocomplete first
	if matches := a.autocompleteMatches(); len(matches) > 0 {
		idx := a.autocompleteIdx
		if idx >= len(matches) {
			idx = 0
		}
		val := matches[idx]
		a.autocompleteIdx = 0
		a.resetInput()
		a.handleCommand(val)
		return
	}

	if a.agentRunning {
		return
	}

	val := strings.TrimSpace(strings.ReplaceAll(a.inputValue(), "\r", ""))
	if val == "" {
		return
	}

	if a.history != nil {
		a.history.Add(val)
	}

	if strings.HasPrefix(val, "/") {
		a.resetInput()
		a.handleCommand(val)
		return
	}

	display := expandPastes(val, a.pasteStore)
	content := expandAttachments(display, a.attachments)
	a.resetInput()
	a.pasteStore = nil
	a.pasteCount = 0
	a.attachments = nil
	a.attachmentCount = 0

	if a.langdagClient == nil {
		a.messages = append(a.messages, chatMessage{kind: msgUser, content: display, leadBlank: true})
		a.messages = append(a.messages, chatMessage{kind: msgError, content: "No API keys configured. Use /config to add a key first."})
		a.render()
		return
	}

	a.messages = append(a.messages, chatMessage{kind: msgUser, content: display, leadBlank: true})
	if !a.containerReady {
		a.messages = append(a.messages, chatMessage{kind: msgInfo, content: "Container is still starting — the agent won't have bash or file tools until it's ready."})
	}
	a.startAgent(content)
	a.render()
}

// ─── Commands ───

func (a *App) handleCommand(input string) {
	cmd := strings.Fields(input)[0]

	switch cmd {
	case "/clear":
		a.agentNodeID = ""
		a.streamingText = ""
		a.pendingToolCall = ""
		a.messages = nil
		a.scratchpad.Clear()
		a.render()

	case "/compact":
		a.handleCompactCommand(input)

	case "/config":
		a.enterConfigMode()

	case "/model":
		// Open config at the model fields tab. If in a repo, go to Project tab;
		// otherwise Global tab. Cursor starts on Active Model.
		a.enterConfigMode()
		if a.repoRoot != "" {
			a.cfgTab = 2 // Project tab
		} else {
			a.cfgTab = 1 // Global tab
		}
		a.cfgCursor = 0 // Active Model is the first field
		a.renderInput()

	case "/branches":
		if a.worktreePath == "" {
			a.messages = append(a.messages, chatMessage{kind: msgError, content: "No workspace path available."})
			a.render()
			return
		}
		branchCmd := exec.Command("git", "branch", "--format=%(refname:short)")
		branchCmd.Dir = a.worktreePath
		out, err := branchCmd.Output()
		if err != nil {
			a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Error listing branches: %v", err)})
			a.render()
			return
		}
		branches := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(branches) == 0 || (len(branches) == 1 && branches[0] == "") {
			a.messages = append(a.messages, chatMessage{kind: msgInfo, content: "No branches found."})
			a.render()
			return
		}
		a.menuLines = branches
		a.menuCursor = 0
		a.menuScrollOffset = 0
		a.menuActive = true
		a.menuAction = func(idx int) {
			if idx >= 0 && idx < len(branches) {
				selected := branches[idx]
				checkoutCmd := exec.Command("git", "checkout", selected)
				checkoutCmd.Dir = a.worktreePath
				if err := checkoutCmd.Run(); err != nil {
					a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Checkout failed: %v", err)})
				} else {
					a.status.Branch = selected
					a.messages = append(a.messages, chatMessage{kind: msgSuccess, content: fmt.Sprintf("Switched to branch '%s'", selected)})
				}
			}
			a.menuLines = nil
			a.menuHeader = ""
			a.menuActive = false
			a.menuAction = nil
			a.menuScrollOffset = 0
		}
		a.renderInput()

	case "/worktrees":
		repoRoot := gitRepoRoot()
		if repoRoot == "" {
			a.messages = append(a.messages, chatMessage{kind: msgError, content: "Not in a git repository."})
			a.render()
			return
		}
		projectID, err := ensureProjectID(repoRoot)
		if err != nil {
			a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Error reading project: %v", err)})
			a.render()
			return
		}
		baseDir := worktreeBaseDir(projectID)
		wts, err := listWorktrees(baseDir)
		if err != nil {
			a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Error listing worktrees: %v", err)})
			a.render()
			return
		}
		var lines []string
		lines = append(lines, "+ New worktree")
		for _, wt := range wts {
			status := ""
			if wt.Active {
				status = " [active]"
			}
			if !wt.Clean {
				status += " [dirty]"
			}
			lines = append(lines, fmt.Sprintf("%s (%s)%s", filepath.Base(wt.Path), wt.Branch, status))
		}
		a.menuLines = lines
		a.menuCursor = 0
		a.menuScrollOffset = 0
		a.menuActive = true
		a.menuAction = func(idx int) {
			a.menuLines = nil
			a.menuHeader = ""
			a.menuActive = false
			a.menuAction = nil
			a.menuScrollOffset = 0

			if idx == 0 {
				// "New worktree" — prompt for a name.
				a.promptForWorktreeName(repoRoot, baseDir)
				return
			}
			wtIdx := idx - 1
			if wtIdx >= 0 && wtIdx < len(wts) {
				selected := wts[wtIdx]
				a.switchToWorktree(selected.Path, filepath.Base(selected.Path), selected.Branch)
			}
		}
		a.renderInput()

	case "/shell":
		a.enterShellMode()

	case "/session":
		a.handleSessionCommand(input)

	case "/usage":
		a.handleUsageCommand()

	default:
		a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Unknown command: %s", cmd)})
		a.render()
	}
}

// handleCompactCommand handles /compact [focus hint].
func (a *App) handleCompactCommand(input string) {
	if a.langdagClient == nil {
		a.messages = append(a.messages, chatMessage{kind: msgError, content: "No API client available."})
		a.render()
		return
	}
	if a.agentNodeID == "" {
		a.messages = append(a.messages, chatMessage{kind: msgError, content: "No active conversation to compact."})
		a.render()
		return
	}

	// Extract optional focus hint from the command args.
	focusHint := ""
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(input), "/compact"))
	if rest != "" {
		focusHint = rest
	}

	// Use exploration model for cheap summarization.
	model := a.config.resolveExplorationModel(a.models)
	if model == "" {
		model = a.config.resolveActiveModel(a.models)
	}

	a.messages = append(a.messages, chatMessage{kind: msgInfo, content: "Compacting conversation..."})
	a.render()

	result, err := compactConversation(context.Background(), a.langdagClient, a.agentNodeID, model, focusHint)
	if err != nil {
		a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Compact failed: %v", err)})
		a.render()
		return
	}

	a.agentNodeID = result.NewNodeID
	a.messages = append(a.messages, chatMessage{
		kind:    msgSuccess,
		content: fmt.Sprintf("Compacted: %d nodes → summary + %d recent nodes", result.OriginalNodes, result.KeptNodes),
	})
	a.render()
}

// handleUsageCommand shows session and conversation token usage statistics.
func (a *App) handleUsageCommand() {
	var b strings.Builder

	b.WriteString("Session Usage\n")
	b.WriteString(fmt.Sprintf("  LLM calls:     %d\n", a.sessionLLMCalls))
	b.WriteString(fmt.Sprintf("  Input tokens:  %s\n", formatTokenCount(a.sessionInputTokens)))
	b.WriteString(fmt.Sprintf("  Output tokens: %s\n", formatTokenCount(a.sessionOutputTokens)))
	if a.sessionCacheRead > 0 {
		b.WriteString(fmt.Sprintf("  Cache read:    %s\n", formatTokenCount(a.sessionCacheRead)))
	}
	b.WriteString(fmt.Sprintf("  Cost:          %s\n", formatCost(a.sessionCostUSD)))
	b.WriteString(fmt.Sprintf("  Tool calls:    %d (%s result data)\n", a.sessionToolResults, formatBytes(a.sessionToolBytes)))
	toolTokenEst := a.sessionToolBytes / charsPerToken
	if a.sessionInputTokens > 0 && toolTokenEst > 0 {
		pct := float64(toolTokenEst) * 100 / float64(a.sessionInputTokens)
		b.WriteString(fmt.Sprintf("  Tool tokens:   ~%s (%.0f%% of input)\n", formatTokenCount(toolTokenEst), pct))
	}

	// Per-tool breakdown (sorted by bytes descending).
	if len(a.sessionToolStats) > 0 {
		type toolStat struct {
			name       string
			count, bytes int
		}
		var stats []toolStat
		for name, s := range a.sessionToolStats {
			stats = append(stats, toolStat{name, s[0], s[1]})
		}
		sort.Slice(stats, func(i, j int) bool { return stats[i].bytes > stats[j].bytes })
		b.WriteString("\n  Per tool:\n")
		for _, s := range stats {
			est := s.bytes / charsPerToken
			b.WriteString(fmt.Sprintf("    %-12s %3d calls  %6s  ~%s tokens\n",
				s.name, s.count, formatBytes(s.bytes), formatTokenCount(est)))
		}
	}

	// Conversation breakdown from the node tree.
	if a.langdagClient != nil && a.agentNodeID != "" {
		ancestors, err := a.langdagClient.GetAncestors(context.Background(), a.agentNodeID)
		if err == nil && len(ancestors) > 0 {
			b.WriteString("\nConversation (" + fmt.Sprintf("%d nodes", len(ancestors)) + ")\n")
			var convIn, convOut, convCacheRead int
			var convCost float64
			var toolResultBytes int
			var toolResultCount int
			for _, n := range ancestors {
				convIn += n.TokensIn
				convOut += n.TokensOut
				convCacheRead += n.TokensCacheRead
				convCost += a.nodeCost(n)
				if n.NodeType == types.NodeTypeUser && isToolResultContent(n.Content) {
					toolResultBytes += len(n.Content)
					toolResultCount++
				}
			}
			b.WriteString(fmt.Sprintf("  Input tokens:  %s\n", formatTokenCount(convIn)))
			b.WriteString(fmt.Sprintf("  Output tokens: %s\n", formatTokenCount(convOut)))
			if convCacheRead > 0 {
				b.WriteString(fmt.Sprintf("  Cache read:    %s\n", formatTokenCount(convCacheRead)))
			}
			b.WriteString(fmt.Sprintf("  Cost:          %s\n", formatCost(convCost)))
			if toolResultCount > 0 {
				b.WriteString(fmt.Sprintf("  Tool results:  %d (%s stored)\n", toolResultCount, formatBytes(toolResultBytes)))
			}
		}
	}

	// Context window status.
	contextWindow := 0
	if m := findModelByID(a.models, a.config.resolveActiveModel(a.models)); m != nil {
		contextWindow = m.ContextWindow
	}
	if contextWindow > 0 && a.lastInputTokens > 0 {
		pct := float64(a.lastInputTokens) * 100 / float64(contextWindow)
		b.WriteString(fmt.Sprintf("\nContext: %s / %s (%.0f%%)\n",
			formatTokenCount(a.lastInputTokens), formatContextWindow(contextWindow), pct))
	}

	a.messages = append(a.messages, chatMessage{kind: msgInfo, content: b.String()})
	a.render()
}

func (a *App) promptForWorktreeName(repoRoot, baseDir string) {
	a.promptLabel = "Enter worktree name:"
	a.promptCallback = func(name string) {
		wtPath, err := createWorktree(repoRoot, baseDir, name)
		if err != nil {
			a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Failed to create worktree: %v", err)})
			a.render()
			return
		}
		branch := "cpsl-" + name
		a.switchToWorktree(wtPath, name, branch)
	}
	a.resetInput()
	a.renderInput()
}

func (a *App) switchToWorktree(wtPath, name, branch string) {
	a.worktreePath = wtPath
	a.status.WorktreeName = name
	a.status.Branch = branch
	_ = lockWorktree(wtPath, os.Getpid())

	a.messages = append(a.messages, chatMessage{kind: msgSuccess, content: fmt.Sprintf("Switched to worktree '%s' (%s)", name, branch)})

	// Reboot container with new workspace if container is ready.
	if a.containerReady && a.container != nil {
		a.containerReady = false
		a.containerStatusText = "restarting…"
		go func() {
			a.resultCh <- containerStatusMsg{text: "stopping…"}
			_ = a.container.Stop()
			a.resultCh <- containerStatusMsg{text: "starting…"}
			attachDir := filepath.Join(wtPath, ".cpsl", "attachments", a.sessionID)
			_ = os.MkdirAll(attachDir, 0o755)
			mounts := []MountSpec{
				{Source: wtPath, Destination: "/workspace"},
				{Source: attachDir, Destination: "/attachments", ReadOnly: true},
			}
			if err := a.container.Start(wtPath, mounts); err != nil {
				a.resultCh <- containerStatusMsg{text: "start failed"}
				a.resultCh <- containerErrMsg{err: err}
				return
			}
			a.resultCh <- containerReadyMsg{client: a.container}
		}()
	}
	a.render()
}

func (a *App) isInWorktree() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	return strings.HasPrefix(a.worktreePath, filepath.Join(home, ".cpsl", "worktrees"))
}

func maskKey(key string) string {
	if key == "" {
		return "(not set)"
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// ─── Config editor ───

var cfgTabNames = []string{"API Keys", "Global", "Project"}

type cfgField struct {
	label      string
	get        func(Config) string
	display    func(Config) string    // masked display; nil means use get
	set        func(*Config, string)
	toggle     func(*Config)          // if non-nil, Enter toggles instead of opening editor
	globalHint func(Config) string    // if set, shows "(global: X)" when field value is empty
	picker     func(*App)             // if non-nil, Enter opens a picker (e.g. model selector) instead of editor
}

var cfgAPIKeyFields = []cfgField{
	{label: "Anthropic", get: func(c Config) string { return c.AnthropicAPIKey }, display: func(c Config) string { return maskKey(c.AnthropicAPIKey) }, set: func(c *Config, v string) { c.AnthropicAPIKey = v }},
	{label: "OpenAI", get: func(c Config) string { return c.OpenAIAPIKey }, display: func(c Config) string { return maskKey(c.OpenAIAPIKey) }, set: func(c *Config, v string) { c.OpenAIAPIKey = v }},
	{label: "Grok", get: func(c Config) string { return c.GrokAPIKey }, display: func(c Config) string { return maskKey(c.GrokAPIKey) }, set: func(c *Config, v string) { c.GrokAPIKey = v }},
	{label: "Gemini", get: func(c Config) string { return c.GeminiAPIKey }, display: func(c Config) string { return maskKey(c.GeminiAPIKey) }, set: func(c *Config, v string) { c.GeminiAPIKey = v }},
}

func (a *App) enterConfigMode() {
	a.cfgActive = true
	a.cfgTab = 0
	a.cfgCursor = 0
	a.cfgEditing = false
	a.cfgEditBuf = nil
	a.cfgEditCursor = 0
	a.cfgDraft = a.globalConfig
	a.cfgProjectDraft = a.projectConfig
	a.renderInput()
}

func (a *App) exitConfigMode(save bool) {
	if save {
		a.globalConfig = a.cfgDraft
		a.projectConfig = a.cfgProjectDraft
		a.config = mergeConfigs(a.globalConfig, a.projectConfig)
		a.displaySystemPrompts = a.config.DisplaySystemPrompts
		var saveErr bool
		if err := saveConfig(a.globalConfig); err != nil {
			a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Error saving global config: %v", err)})
			saveErr = true
		}
		if a.repoRoot != "" {
			if err := saveProjectConfig(a.repoRoot, a.projectConfig); err != nil {
				a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Error saving project config: %v", err)})
				saveErr = true
			}
		}
		if !saveErr {
			a.messages = append(a.messages, chatMessage{kind: msgSuccess, content: "Config saved."})
		}
		// Reinitialize langdag client with updated config
		go func() {
			client, err := newLangdagClient(a.config)
			a.resultCh <- langdagReadyMsg{client: client, provider: a.config.defaultLangdagProvider(), err: err}
		}()
	}
	a.cfgActive = false
	a.cfgEditing = false
	a.cfgEditBuf = nil
	a.render()
}

// openConfigModelPicker opens an inline model menu within the config editor.
// getCurrentID returns the currently selected model ID (for highlighting).
// onSelect is called with the chosen model ID when the user makes a selection.
func (a *App) openConfigModelPicker(getCurrentID func() string, onSelect func(string)) {
	if a.models == nil {
		return
	}
	available := a.cfgDraft.availableModels(a.models)
	if len(available) == 0 {
		return
	}

	activeID := getCurrentID()
	a.menuModels = available
	a.menuActiveID = activeID
	a.menuSortCol = sortColFromName(a.cfgDraft.ModelSortCol)
	a.menuSortAsc = sortAscFromMap(a.cfgDraft.ModelSortDirs)
	asc := a.menuSortAsc[a.menuSortCol]
	sortModelsByCol(a.menuModels, a.menuSortCol, asc)
	header, lines := formatModelMenuLines(a.menuModels, activeID, a.menuSortCol, asc)

	activeIdx := 0
	for i, m := range a.menuModels {
		if m.ID == activeID {
			activeIdx = i
			break
		}
	}

	a.menuHeader = header
	a.menuLines = lines
	a.menuCursor = activeIdx
	maxVisible := getTerminalHeight() * 60 / 100
	if maxVisible < 1 {
		maxVisible = 1
	}
	if activeIdx >= maxVisible {
		a.menuScrollOffset = activeIdx - maxVisible + 1
	} else {
		a.menuScrollOffset = 0
	}
	a.menuActive = true
	a.menuAction = func(idx int) {
		if idx >= 0 && idx < len(a.menuModels) {
			onSelect(a.menuModels[idx].ID)
		}
		a.menuLines = nil
		a.menuHeader = ""
		a.menuActive = false
		a.menuAction = nil
		a.menuScrollOffset = 0
		a.menuModels = nil
		a.menuActiveID = ""
		// Config mode stays active — renderInput will show config fields again.
	}
	a.renderInput()
}

func (a *App) cfgCurrentFields() []cfgField {
	switch a.cfgTab {
	case 0:
		return cfgAPIKeyFields
	case 1:
		return a.settingsTabFields()
	case 2:
		return a.projectTabFields()
	}
	return nil
}

func (a *App) settingsTabFields() []cfgField {
	return []cfgField{
		{label: "Active Model", get: func(c Config) string { return c.ActiveModel }, set: func(c *Config, v string) { c.ActiveModel = v }, picker: func(a *App) { a.openConfigModelPicker(func() string { return a.cfgDraft.ActiveModel }, func(id string) { a.cfgDraft.ActiveModel = id }) }},
		{label: "Exploration Model", get: func(c Config) string { return c.ExplorationModel }, set: func(c *Config, v string) { c.ExplorationModel = v }, picker: func(a *App) { a.openConfigModelPicker(func() string { return a.cfgDraft.ExplorationModel }, func(id string) { a.cfgDraft.ExplorationModel = id }) }},
		{label: "Paste Collapse", get: func(c Config) string { return strconv.Itoa(c.PasteCollapseMinChars) }, set: func(c *Config, v string) { if n, err := strconv.Atoi(v); err == nil { c.PasteCollapseMinChars = n } }},
		{label: "Show System Prompt", get: func(c Config) string { if c.DisplaySystemPrompts { return "on" }; return "off" }, toggle: func(c *Config) { c.DisplaySystemPrompts = !c.DisplaySystemPrompts }},
		{label: "Sub-Agent Max Turns", get: func(c Config) string { n := c.SubAgentMaxTurns; if n <= 0 { n = 15 }; return strconv.Itoa(n) }, set: func(c *Config, v string) { if n, err := strconv.Atoi(v); err == nil && n > 0 { c.SubAgentMaxTurns = n } }},
		{label: "Personality", get: func(c Config) string { return c.Personality }, set: func(c *Config, v string) { c.Personality = v }},
	}
}

func (a *App) projectTabFields() []cfgField {
	return []cfgField{
		{
			label:      "Active Model",
			get:        func(_ Config) string { return a.cfgProjectDraft.ActiveModel },
			set:        func(_ *Config, v string) { a.cfgProjectDraft.ActiveModel = v },
			globalHint: func(c Config) string { return c.ActiveModel },
			picker:     func(a *App) { a.openConfigModelPicker(func() string { return a.cfgProjectDraft.ActiveModel }, func(id string) { a.cfgProjectDraft.ActiveModel = id }) },
		},
		{
			label:      "Exploration Model",
			get:        func(_ Config) string { return a.cfgProjectDraft.ExplorationModel },
			set:        func(_ *Config, v string) { a.cfgProjectDraft.ExplorationModel = v },
			globalHint: func(c Config) string { return c.ExplorationModel },
			picker:     func(a *App) { a.openConfigModelPicker(func() string { return a.cfgProjectDraft.ExplorationModel }, func(id string) { a.cfgProjectDraft.ExplorationModel = id }) },
		},
		{
			label:      "Personality",
			get:        func(_ Config) string { return a.cfgProjectDraft.Personality },
			set:        func(_ *Config, v string) { a.cfgProjectDraft.Personality = v },
			globalHint: func(c Config) string { return c.Personality },
		},
		{
			label: "Sub-Agent Max Turns",
			get: func(_ Config) string {
				if a.cfgProjectDraft.SubAgentMaxTurns == 0 {
					return ""
				}
				return strconv.Itoa(a.cfgProjectDraft.SubAgentMaxTurns)
			},
			set: func(_ *Config, v string) {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					a.cfgProjectDraft.SubAgentMaxTurns = n
				} else {
					a.cfgProjectDraft.SubAgentMaxTurns = 0
				}
			},
			globalHint: func(c Config) string {
				n := c.SubAgentMaxTurns
				if n <= 0 {
					n = 15
				}
				return strconv.Itoa(n)
			},
		},
	}
}

func (a *App) buildConfigRows() []string {
	var rows []string

	// Tab bar
	var tabParts []string
	for i, name := range cfgTabNames {
		if i == a.cfgTab {
			tabParts = append(tabParts, fmt.Sprintf("\033[36;1m[%s]\033[0m", name))
		} else {
			tabParts = append(tabParts, fmt.Sprintf("\033[2m %s \033[0m", name))
		}
	}
	rows = append(rows, strings.Join(tabParts, " "))

	// No-project message for Project tab
	if a.cfgTab == 2 && a.repoRoot == "" {
		rows = append(rows, "\033[2mNo project detected (not in a git repository)\033[0m")
		rows = append(rows, "\033[2m←/→=tab  Esc=close  Ctrl+S=save & close\033[0m")
		return rows
	}

	// When a model picker menu is active, render it inline below the tab bar
	if a.menuActive && len(a.menuLines) > 0 {
		w := a.width
		if a.menuHeader != "" {
			rows = append(rows, fmt.Sprintf("\033[1m%s\033[0m", truncateWithEllipsis(a.menuHeader, w)))
		}
		maxVisible := getTerminalHeight() * 60 / 100
		if maxVisible < 1 {
			maxVisible = 1
		}
		total := len(a.menuLines)
		end := a.menuScrollOffset + maxVisible
		if end > total {
			end = total
		}
		for i := a.menuScrollOffset; i < end; i++ {
			line := a.menuLines[i]
			if i == a.menuCursor {
				rows = append(rows, fmt.Sprintf("\033[36;1m%s ◆\033[0m", truncateWithEllipsis(line, w-2)))
			} else {
				rows = append(rows, truncateWithEllipsis(line, w))
			}
		}
		first := a.menuScrollOffset + 1
		last := end
		rows = append(rows, fmt.Sprintf("\033[2m(%d->%d / %d)\033[0m", first, last, total))
		rows = append(rows, "\033[2m←/→ sort column  Tab flip order  Enter select  Esc close\033[0m")
		return rows
	}

	// Fields
	fields := a.cfgCurrentFields()
	for i, f := range fields {
		if a.cfgEditing && i == a.cfgCursor {
			// Show editable text input with underline
			editStr := string(a.cfgEditBuf)
			rows = append(rows, fmt.Sprintf("\033[36;1m%s: \033[4m%s\033[0m \033[36;1m◆\033[0m", f.label, editStr))
		} else {
			val := ""
			if f.display != nil {
				val = f.display(a.cfgDraft)
			} else {
				val = f.get(a.cfgDraft)
			}
			if val == "" {
				if f.globalHint != nil {
					hint := f.globalHint(a.cfgDraft)
					if hint == "" {
						hint = "not set"
					}
					val = fmt.Sprintf("(global: %s)", hint)
				} else {
					val = "(not set)"
				}
			}
			if i == a.cfgCursor {
				rows = append(rows, fmt.Sprintf("\033[36;1m%s: %s ◆\033[0m", f.label, val))
			} else {
				rows = append(rows, fmt.Sprintf("%s: %s", f.label, val))
			}
		}
	}

	// Help line
	if a.cfgEditing {
		rows = append(rows, "\033[2mEnter=confirm  Esc=cancel\033[0m")
	} else {
		rows = append(rows, "\033[2m←/→=tab  ↑/↓=select  Enter=edit  Esc=close  Ctrl+S=save & close\033[0m")
	}

	return rows
}

func (a *App) handleConfigByte(ch byte, stdinCh chan byte, readByte func() (byte, bool)) {
	if a.cfgEditing {
		a.handleConfigEditByte(ch, stdinCh, readByte)
		return
	}

	switch {
	case ch == '\033': // Escape sequence
		var b byte
		var ok bool
		select {
		case b, ok = <-stdinCh:
			if !ok {
				return
			}
		case <-time.After(50 * time.Millisecond):
			a.exitConfigMode(false)
			return
		}
		if b != '[' {
			a.exitConfigMode(false)
			return
		}
		b, ok = readByte()
		if !ok {
			return
		}
		switch b {
		case 'A': // Up
			if a.cfgCursor > 0 {
				a.cfgCursor--
			} else {
				fields := a.cfgCurrentFields()
				if len(fields) > 0 {
					a.cfgCursor = len(fields) - 1
				}
			}
			a.renderInput()
		case 'B': // Down
			fields := a.cfgCurrentFields()
			if a.cfgCursor < len(fields)-1 {
				a.cfgCursor++
			} else {
				a.cfgCursor = 0
			}
			a.renderInput()
		case 'C': // Right - next tab
			a.cfgTab++
			if a.cfgTab >= len(cfgTabNames) {
				a.cfgTab = 0
			}
			a.cfgCursor = 0
			a.renderInput()
		case 'D': // Left - prev tab
			a.cfgTab--
			if a.cfgTab < 0 {
				a.cfgTab = len(cfgTabNames) - 1
			}
			a.cfgCursor = 0
			a.renderInput()
		default:
			// Consume modified key sequences (ESC [ 1 ; mod letter)
			if b == '1' {
				readByte() // ;
				readByte() // mod
				readByte() // letter
			}
		}

	case ch == '\r': // Enter - toggle, picker, or start editing current field
		if a.cfgTab == 2 && a.repoRoot == "" {
			break // Project tab non-editable without a repo
		}
		fields := a.cfgCurrentFields()
		if len(fields) > 0 && a.cfgCursor < len(fields) {
			f := fields[a.cfgCursor]
			if f.picker != nil {
				f.picker(a)
			} else if f.toggle != nil {
				f.toggle(&a.cfgDraft)
			} else {
				a.cfgEditing = true
				val := f.get(a.cfgDraft)
				a.cfgEditBuf = []rune(val)
				a.cfgEditCursor = len(a.cfgEditBuf)
			}
		}
		a.renderInput()

	case ch == 0x13: // Ctrl+S - save and close
		a.exitConfigMode(true)

	case ch == 3 || ch == 4: // Ctrl+C/D - exit without saving
		a.exitConfigMode(false)
	}
}

func (a *App) handleConfigEditByte(ch byte, stdinCh chan byte, readByte func() (byte, bool)) {
	switch {
	case ch == '\033': // Escape
		var b byte
		var ok bool
		select {
		case b, ok = <-stdinCh:
			if !ok {
				return
			}
		case <-time.After(50 * time.Millisecond):
			// Plain Escape - cancel edit
			a.cfgEditing = false
			a.cfgEditBuf = nil
			a.renderInput()
			return
		}
		if b != '[' {
			a.cfgEditing = false
			a.cfgEditBuf = nil
			a.renderInput()
			return
		}
		b, ok = readByte()
		if !ok {
			return
		}
		switch b {
		case 'C': // Right
			if a.cfgEditCursor < len(a.cfgEditBuf) {
				a.cfgEditCursor++
				a.renderInput()
			}
		case 'D': // Left
			if a.cfgEditCursor > 0 {
				a.cfgEditCursor--
				a.renderInput()
			}
		case 'H': // Home
			a.cfgEditCursor = 0
			a.renderInput()
		case 'F': // End
			a.cfgEditCursor = len(a.cfgEditBuf)
			a.renderInput()
		case '2': // Bracketed paste or modifyOtherKeys
			a.handleCSIDigit2(readByte, func(s string) {
				pasted := []rune(s)
				tail := make([]rune, len(a.cfgEditBuf[a.cfgEditCursor:]))
				copy(tail, a.cfgEditBuf[a.cfgEditCursor:])
				a.cfgEditBuf = append(a.cfgEditBuf[:a.cfgEditCursor], append(pasted, tail...)...)
				a.cfgEditCursor += len(pasted)
				a.renderInput()
			})
		case '3': // Delete
			if t, ok := readByte(); ok && t == '~' {
				if a.cfgEditCursor < len(a.cfgEditBuf) {
					a.cfgEditBuf = append(a.cfgEditBuf[:a.cfgEditCursor], a.cfgEditBuf[a.cfgEditCursor+1:]...)
					a.renderInput()
				}
			}
		default:
			// consume remaining bytes of sequences
			if b == '1' {
				readByte()
				readByte()
				readByte()
			}
		}

	case ch == '\r': // Enter - confirm edit
		fields := a.cfgCurrentFields()
		if a.cfgCursor < len(fields) {
			fields[a.cfgCursor].set(&a.cfgDraft, string(a.cfgEditBuf))
		}
		a.cfgEditing = false
		a.cfgEditBuf = nil
		a.renderInput()

	case ch == 127 || ch == 0x08: // Backspace
		if a.cfgEditCursor > 0 {
			a.cfgEditCursor--
			a.cfgEditBuf = append(a.cfgEditBuf[:a.cfgEditCursor], a.cfgEditBuf[a.cfgEditCursor+1:]...)
			a.renderInput()
		}

	case ch == 0x01: // Ctrl+A
		a.cfgEditCursor = 0
		a.renderInput()

	case ch == 0x05: // Ctrl+E
		a.cfgEditCursor = len(a.cfgEditBuf)
		a.renderInput()

	case ch == 0x15: // Ctrl+U - kill to start
		a.cfgEditBuf = a.cfgEditBuf[a.cfgEditCursor:]
		a.cfgEditCursor = 0
		a.renderInput()

	case ch == 0x0b: // Ctrl+K - kill to end
		a.cfgEditBuf = a.cfgEditBuf[:a.cfgEditCursor]
		a.renderInput()

	case ch == 0x17: // Ctrl+W - delete word backward
		if a.cfgEditCursor > 0 {
			i := a.cfgEditCursor - 1
			for i > 0 && a.cfgEditBuf[i] == ' ' {
				i--
			}
			for i > 0 && a.cfgEditBuf[i-1] != ' ' {
				i--
			}
			a.cfgEditBuf = append(a.cfgEditBuf[:i], a.cfgEditBuf[a.cfgEditCursor:]...)
			a.cfgEditCursor = i
			a.renderInput()
		}

	case ch >= 0x20: // Printable character
		r := rune(ch)
		if ch >= 0x80 {
			b := []byte{ch}
			n := utf8ByteLen(ch)
			for i := 1; i < n; i++ {
				next, ok := readByte()
				if !ok {
					return
				}
				b = append(b, next)
			}
			r, _ = utf8.DecodeRune(b)
		}
		a.cfgEditBuf = append(a.cfgEditBuf, 0)
		copy(a.cfgEditBuf[a.cfgEditCursor+1:], a.cfgEditBuf[a.cfgEditCursor:])
		a.cfgEditBuf[a.cfgEditCursor] = r
		a.cfgEditCursor++
		a.renderInput()
	}
}

// ─── Shell mode ───

func (a *App) enterShellMode() {
	if a.containerErr != nil {
		a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Container error: %v", a.containerErr)})
		a.render()
		return
	}
	if !a.containerReady {
		a.messages = append(a.messages, chatMessage{kind: msgInfo, content: "Container is starting... please try again in a moment."})
		a.render()
		return
	}

	// Stop the stdin reader so it doesn't compete with the shell
	a.stopStdinReader()

	// Exit alt screen, restore terminal
	fmt.Print("\033[?25h")   // show cursor
	fmt.Print("\033[>4;0m")  // disable modifyOtherKeys
	fmt.Print("\033[?2004l") // disable bracketed paste
	fmt.Print("\033[?1049l") // exit alt screen
	term.Restore(a.fd, a.oldState)

	// Brief pause + flush to discard any stale terminal responses still in-flight.
	flushStdin(a.fd)

	// Run shell synchronously — full TTY control goes to the child process
	shellCmd := a.container.ShellCmd()
	shellErr := shellCmd.Run()

	// Re-enter raw mode
	oldState, err := term.MakeRaw(a.fd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to re-enter raw mode: %v\n", err)
		a.quit = true
		return
	}
	a.oldState = oldState

	// Re-enter alt screen, re-enable bracketed paste, modifyOtherKeys
	fmt.Print("\033[?1049h")
	fmt.Print("\033[?2004h")
	fmt.Print("\033[>4;2m")

	// Restart the stdin reader goroutine
	a.startStdinReader()

	a.width = getWidth()

	if shellErr != nil {
		a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Shell error: %v", shellErr)})
	} else {
		a.messages = append(a.messages, chatMessage{kind: msgInfo, content: "Shell session ended."})
	}

	a.render()
}

// ─── Agent ───

// showModelChange displays an info message when the active model changes.
func (a *App) showModelChange(modelID string) {
	if modelID == "" || modelID == a.lastModelID {
		return
	}
	explorationID := a.config.resolveExplorationModel(a.models)
	line := "Using " + modelID
	if explorationID != "" && explorationID != modelID {
		line += "  exploration: " + explorationID
	}
	a.messages = append(a.messages, chatMessage{kind: msgInfo, content: line})
	a.lastModelID = modelID
}

// maybeShowInitialModels shows the startup model line once both the model
// catalog and the project config have loaded, preventing a double display.
func (a *App) maybeShowInitialModels() {
	if a.shownInitialModel || !a.configReady || a.models == nil {
		return
	}
	a.shownInitialModel = true
	a.showModelChange(a.config.resolveActiveModel(a.models))
}

func (a *App) startAgent(userMessage string) {
	// Move previous attachment files to past/ so /attachments only has current-message files.
	if dir := a.attachmentDir(); dir != "" {
		if entries, err := os.ReadDir(dir); err == nil {
			pastDir := filepath.Join(dir, "past")
			created := false
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				if !created {
					if err := os.MkdirAll(pastDir, 0o755); err != nil {
						break
					}
					created = true
				}
				_ = os.Rename(filepath.Join(dir, e.Name()), filepath.Join(pastDir, e.Name()))
			}
		}
	}

	var tools []Tool
	if a.containerReady && a.container != nil {
		tools = append(tools, NewBashTool(a.container, 120))
		tools = append(tools, NewGlobTool(a.container))
		tools = append(tools, NewGrepTool(a.container))
		tools = append(tools, NewReadFileTool(a.container))
		if a.worktreePath != "" {
			cpslDir := filepath.Join(a.worktreePath, ".cpsl")
			mounts := []MountSpec{
				{Source: a.worktreePath, Destination: "/workspace"},
				{Source: a.attachmentDir(), Destination: "/attachments", ReadOnly: true},
			}
			var projectID string
			if repoRoot := gitRepoRoot(); repoRoot != "" {
				projectID, _ = ensureProjectID(repoRoot)
			}
			onRebuild := func(imageName string) {
				a.containerImage = imageName
			}
			onStatus := func(text string) {
				a.resultCh <- containerStatusMsg{text: text}
			}
			tools = append(tools, NewDevEnvTool(a.container, cpslDir, a.worktreePath, mounts, projectID, onRebuild, onStatus))
		}
	}
	if a.worktreePath != "" {
		tools = append(tools, NewGitTool(a.worktreePath))
	}

	// Server-side tools are handled by the LLM provider, not the client.
	serverTools := []types.ToolDefinition{WebSearchToolDef()}

	modelID := a.config.resolveActiveModel(a.models)
	if modelID == "" {
		a.messages = append(a.messages, chatMessage{kind: msgError, content: "model not found, `/model` to pick a valid one"})
		a.render()
		return
	}

	var modelProvider string
	if modelDef := findModelByID(a.models, modelID); modelDef != nil {
		modelProvider = modelDef.Provider
	}

	if modelProvider != "" && modelProvider != a.langdagProvider {
		if a.langdagClient != nil {
			a.langdagClient.Close()
		}
		client, err := newLangdagClientForProvider(a.config, modelProvider)
		if err != nil {
			a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Error initializing %s provider: %v", modelProvider, err)})
			return
		}
		a.langdagClient = client
		a.langdagProvider = modelProvider
	}

	// Load project-local skills from .cpsl/skills/
	var skills []Skill
	if a.worktreePath != "" {
		skills, _ = loadSkills(filepath.Join(a.worktreePath, ".cpsl", "skills"))
	}

	workDir := "/workspace"

	// Shared scratchpad for inter-agent communication.
	tools = append(tools, NewScratchpadTool(&a.scratchpad))

	containerImage := a.containerImage
	if containerImage == "" {
		containerImage = defaultContainerImage
	}

	// Sub-agent tool: shares the langdag client, available tools (including scratchpad).
	// Uses exploration model if configured, otherwise falls back to active model.
	maxTurns := a.config.SubAgentMaxTurns
	if maxTurns <= 0 {
		maxTurns = 15
	}
	explorationModelID := a.config.resolveExplorationModel(a.models)
	subAgentTool := NewSubAgentTool(a.langdagClient, tools, serverTools, explorationModelID, maxTurns, workDir, a.config.Personality, containerImage)
	tools = append(tools, subAgentTool)

	systemPrompt := buildSystemPrompt(tools, serverTools, skills, workDir, a.config.Personality, containerImage)

	if a.displaySystemPrompts {
		a.messages = append(a.messages, chatMessage{kind: msgSystemPrompt, content: "── System Prompt ──\n" + systemPrompt})
	}

	a.showModelChange(modelID)

	ctxWindow := 0
	if m := findModelByID(a.models, modelID); m != nil {
		ctxWindow = m.ContextWindow
	}
	agent := NewAgent(a.langdagClient, tools, serverTools, systemPrompt, modelID, ctxWindow,
		WithExplorationModel(explorationModelID))
	subAgentTool.parentEvents = agent.events
	a.agent = agent
	a.agentRunning = true
	a.streamingText = ""
	a.needsTextSep = true

	parentNodeID := a.agentNodeID
	go agent.Run(context.Background(), userMessage, parentNodeID)
}

func (a *App) drainAgentEvents() {
	if a.agent == nil || !a.agentRunning {
		return
	}
	for {
		select {
		case event, ok := <-a.agent.Events():
			if !ok {
				a.agentRunning = false
				return
			}
			a.handleAgentEvent(event)
		default:
			return
		}
	}
}

func (a *App) handleAgentEvent(event AgentEvent) {
	debugLog("event=%d text=%q tool=%s err=%v", event.Type, event.Text, event.ToolName, event.Error)

	switch event.Type {
	case EventTextDelta:
		a.streamingText += event.Text
		if idx := strings.LastIndex(a.streamingText, "\n"); idx >= 0 {
			a.messages = append(a.messages, chatMessage{
				kind:      msgAssistant,
				content:   a.streamingText[:idx],
				leadBlank: a.needsTextSep,
			})
			a.needsTextSep = false
			a.streamingText = a.streamingText[idx+1:]
		}
		a.render()

	case EventToolCallStart:
		debugLog("tool_call_start: %s input=%s", event.ToolName, string(event.ToolInput))
		if a.streamingText != "" {
			a.messages = append(a.messages, chatMessage{
				kind:      msgAssistant,
				content:   a.streamingText,
				leadBlank: a.needsTextSep,
			})
			a.needsTextSep = false
			a.streamingText = ""
		}
		a.messages = append(a.messages, chatMessage{kind: msgToolCall, content: toolCallSummary(event.ToolName, event.ToolInput), leadBlank: true})
		a.render()

	case EventToolResult:
		debugLog("tool_result: err=%v result=%q", event.IsError, truncateForLog(event.ToolResult, 500))
		result := collapseToolResult(event.ToolResult)
		a.needsTextSep = true
		a.sessionToolResults++
		a.sessionToolBytes += len(event.ToolResult)
		if a.sessionToolStats == nil {
			a.sessionToolStats = make(map[string][2]int)
		}
		if event.ToolName != "" {
			s := a.sessionToolStats[event.ToolName]
			s[0]++
			s[1] += len(event.ToolResult)
			a.sessionToolStats[event.ToolName] = s
		}
		a.messages = append(a.messages, chatMessage{kind: msgToolResult, content: result, isError: event.IsError})
		a.render()

	case EventUsage:
		if event.Usage != nil {
			a.sessionCostUSD += computeCost(a.models, event.Model, *event.Usage)
			a.lastInputTokens = event.Usage.InputTokens + event.Usage.CacheReadInputTokens + event.Usage.CacheCreationInputTokens
			a.sessionInputTokens += event.Usage.InputTokens
			a.sessionOutputTokens += event.Usage.OutputTokens
			a.sessionCacheRead += event.Usage.CacheReadInputTokens
			a.sessionLLMCalls++
			a.renderInput()
		}

	case EventToolCallDone:
		// Already handled by EventToolResult

	case EventApprovalReq:
		debugLog("approval_req: %s", event.ApprovalDesc)
		a.awaitingApproval = true
		a.approvalDesc = event.ApprovalDesc
		a.renderInput()

	case EventCompacted:
		debugLog("compacted: nodeID=%s", event.NodeID)
		if event.NodeID != "" {
			a.agentNodeID = event.NodeID
		}
		a.messages = append(a.messages, chatMessage{kind: msgInfo, content: event.Text})
		a.render()

	case EventDone:
		debugLog("done: nodeID=%s streamingLen=%d", event.NodeID, len(a.streamingText))
		a.agentRunning = false
		if event.NodeID != "" {
			a.agentNodeID = event.NodeID
		}
		if a.streamingText != "" {
			a.messages = append(a.messages, chatMessage{
				kind:      msgAssistant,
				content:   a.streamingText,
				leadBlank: a.needsTextSep,
			})
			a.streamingText = ""
		}
		a.render()

	case EventSubAgentDelta:
		a.subAgentBuf += event.Text
		// Split completed lines.
		for {
			idx := strings.Index(a.subAgentBuf, "\n")
			if idx < 0 {
				break
			}
			a.subAgentLines = append(a.subAgentLines, a.subAgentBuf[:idx])
			a.subAgentBuf = a.subAgentBuf[idx+1:]
		}
		a.render()

	case EventSubAgentStatus:
		if event.Text == "done" {
			// Collapse live display into a single summary line.
			summary := subAgentSummaryLine(a.subAgentLines, a.subAgentBuf)
			if summary != "" {
				a.messages = append(a.messages, chatMessage{kind: msgInfo, content: summary})
			}
			a.subAgentBuf = ""
			a.subAgentLines = nil
		} else {
			// Tool call status — show as a status line.
			a.subAgentLines = append(a.subAgentLines, event.Text)
		}
		a.render()

	case EventError:
		errMsg := "Agent error"
		if event.Error != nil {
			errMsg = event.Error.Error()
		}
		debugLog("error: %s", errMsg)
		a.messages = append(a.messages, chatMessage{kind: msgError, content: errMsg})
		a.render()
	}
}

// ─── Async results ───

func (a *App) startInit() {
	cfg := a.config
	go func() { a.resultCh <- fetchSWEScoresCmd() }()
	go func() { a.resultCh <- resolveWorkspaceCmd(cfg) }()
	go func() {
		client, err := newLangdagClient(cfg)
		a.resultCh <- langdagReadyMsg{client: client, provider: cfg.defaultLangdagProvider(), err: err}
	}()
	go func() {
		cachePath := catalogCachePath()
		catalog, err := langdag.LoadModelCatalog(cachePath)
		if err != nil {
			log.Printf("warning: loading model catalog: %v", err)
		}
		a.resultCh <- catalogMsg{catalog: catalog}

		// Best-effort background refresh of the cache
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if updated, err := langdag.FetchModelCatalog(ctx, cachePath); err == nil {
			a.resultCh <- catalogMsg{catalog: updated}
		}
	}()
}

func (a *App) drainResults() {
	for {
		select {
		case result := <-a.resultCh:
			a.handleResult(result)
		default:
			return
		}
	}
}

func (a *App) handleResult(result any) {
	switch msg := result.(type) {
	case ctrlCExpiredMsg:
		_ = msg
		if a.ctrlCHint {
			a.ctrlCHint = false
			a.ctrlCTime = time.Time{}
			a.renderInput()
		}
		return

	case sweScoresMsg:
		a.sweLoaded = true
		if msg.err == nil {
			a.sweScores = msg.scores
			if a.models != nil {
				matchSWEScores(a.models, a.sweScores)
			}
		}

	case catalogMsg:
		if msg.catalog != nil {
			a.modelCatalog = msg.catalog
			a.models = modelsFromCatalog(msg.catalog)
			if a.sweLoaded && a.sweScores != nil {
				matchSWEScores(a.models, a.sweScores)
			}
			a.maybeShowInitialModels()
		}

	case langdagReadyMsg:
		if msg.err != nil {
			log.Printf("warning: langdag init: %v", msg.err)
		} else {
			a.langdagClient = msg.client
			a.langdagProvider = msg.provider
		}

	case statusInfoMsg:
		a.status = msg.info

	case workspaceMsg:
		a.worktreePath = msg.worktreePath
		a.repoRoot = msg.worktreePath
		a.projectConfig = loadProjectConfig(a.repoRoot)
		a.config = mergeConfigs(a.globalConfig, a.projectConfig)
		a.configReady = true
		a.history = newHistory(msg.worktreePath, a.config.effectiveMaxHistory())
		a.history.Load()
		a.maybeShowInitialModels()
		wtPath := msg.worktreePath
		go func() { a.resultCh <- fetchStatusCmd(wtPath) }()
		go func() { bootContainerCmd(wtPath, a.sessionID, a.resultCh) }()
		go cleanupTmpDir(wtPath)

	case containerReadyMsg:
		a.container = msg.client
		if msg.worktreePath != "" {
			a.worktreePath = msg.worktreePath
		}
		if msg.imageName != "" {
			a.containerImage = msg.imageName
		}
		a.containerReady = true
		a.containerErr = nil
		if cid := msg.client.ContainerID(); cid != "" {
			shortID := cid
			if len(shortID) > 12 {
				shortID = shortID[:12]
			}
			a.containerStatusText = shortID
		}

	case containerStatusMsg:
		a.containerStatusText = msg.text

	case containerErrMsg:
		a.containerErr = msg.err
		a.messages = append(a.messages, chatMessage{kind: msgError, content: msg.err.Error()})

	case worktreeListMsg:
		if msg.err != nil {
			a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Error listing worktrees: %v", msg.err)})
		}

	case branchListMsg:
		if msg.err != nil {
			a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Error listing branches: %v", msg.err)})
		}

	case branchCheckoutMsg:
		if msg.err != nil {
			a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Checkout failed: %v", msg.err)})
		} else {
			a.status.Branch = msg.branch
			a.messages = append(a.messages, chatMessage{kind: msgSuccess, content: fmt.Sprintf("Switched to branch '%s'", msg.branch)})
		}

	case resizeMsg:
		a.width = getWidth() // re-read in case of further changes
		a.renderFull()
		return
	}

	a.render()
}

// ─── Cleanup ───

func (a *App) cleanup() {
	close(a.stopCh)
	if a.agent != nil {
		a.agent.Cancel()
	}
	if a.container != nil {
		_ = a.container.Stop()
	}
	if a.langdagClient != nil {
		_ = a.langdagClient.Close()
	}
	if a.worktreePath != "" {
		_ = unlockWorktree(a.worktreePath)
	}
}

// ─── Helpers ───

// debouncer delays a function call, resetting the timer on each trigger.
type debouncer struct {
	delay time.Duration
	fire  func()
	timer *time.Timer
}

func newDebouncer(delay time.Duration, fire func()) *debouncer {
	return &debouncer{delay: delay, fire: fire}
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

// ─── main ───

func main() {
	log.SetOutput(io.Discard)

	app := newApp()

	app.displaySystemPrompts = app.config.DisplaySystemPrompts
	for _, arg := range os.Args[1:] {
		if arg == "--display-system-prompts" {
			app.displaySystemPrompts = true
		}
	}

	if err := app.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
