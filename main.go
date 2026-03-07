package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"
	"langdag.com/langdag"
	"github.com/rivo/uniseg"
)

// ─── Constants ───

const (
	promptPrefix     = "❯ "
	promptPrefixCols = 2
	charLimit        = 250
)

// ─── Block and message types ───

type chatMsgKind int

const (
	msgUser chatMsgKind = iota
	msgAssistant
	msgToolCall
	msgToolResult
	msgInfo
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
func getVisualLines(input []rune, cursor int, width int) []vline {
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

func cursorVisualPos(input []rune, cursor int, width int) (int, int) {
	vlines := getVisualLines(input, cursor, width)
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

// wrapString splits a string into visual rows of at most `w` runes.
func wrapString(s string, startCol int, w int) []string {
	runes := []rune(s)
	if len(runes) == 0 {
		return []string{""}
	}
	var rows []string
	col := startCol
	lineStart := 0
	for i := range runes {
		col++
		if col >= w {
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
	dimFg := "\033[38;5;240m"
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
			buf.WriteString(dimBg + fillFg + string(partials[8-cellFilled]) + reset)
		}
	}
	return buf.String()
}

// ─── ANSI rendering helpers (from simple-chat) ───

func writeRows(buf *strings.Builder, rows []string, from int) {
	for i, row := range rows {
		buf.WriteString(fmt.Sprintf("\033[%d;1H\033[0m\033[2K%s", from+i, row))
	}
}

// ─── Logo (from simple-chat) ───

var logo = []string{
	"",
	"    \033[38;5;75m▄███▄\033[0m ░▄▀▀▒█▀▄░▄▀▀░█▒░",
	"  \033[38;5;75m▄██\033[38;5;255m• •\033[38;5;75m█\033[0m ░▀▄▄░█▀▒▒▄██▒█▄▄",
	" \033[38;5;75m▀███▄█▄█\033[0m Contained Coding Agent",
	"",
}

// ─── Styling helpers ───

func styledUserMsg(content string) string {
	return "\033[38;5;253m❯ " + content + "\033[0m"
}

func styledAssistantText(content string) string {
	return "\033[38;5;141m" + content + "\033[0m"
}

func styledToolCall(summary string) string {
	return "\033[38;5;242;3m" + summary + "\033[0m"
}

func styledToolResult(result string, isError bool) string {
	if isError {
		return styledError(result)
	}
	return "\033[38;5;240m" + result + "\033[0m"
}

func styledError(msg string) string {
	return "\033[38;5;203;3m" + msg + "\033[0m"
}

func styledSuccess(msg string) string {
	return "\033[38;5;114;3m" + msg + "\033[0m"
}

func styledInfo(msg string) string {
	return "\033[38;5;103;3m" + msg + "\033[0m"
}

func renderMessage(msg chatMessage) string {
	var parts []string
	if msg.leadBlank {
		parts = append(parts, "")
	}
	var rendered string
	switch msg.kind {
	case msgUser:
		rendered = styledUserMsg(msg.content)
	case msgAssistant:
		rendered = styledAssistantText(msg.content)
	case msgToolCall:
		rendered = styledToolCall(msg.content)
	case msgToolResult:
		rendered = styledToolResult(msg.content, msg.isError)
	case msgInfo:
		rendered = styledInfo(msg.content)
	case msgSuccess:
		rendered = styledSuccess(msg.content)
	case msgError:
		rendered = styledError(msg.content)
	}
	parts = append(parts, rendered)
	return strings.Join(parts, "\n")
}

// ─── Commands and autocomplete ───

var commands = []string{"/branches", "/clear", "/config", "/model", "/shell", "/worktrees"}

func filterCommands(prefix string) []string {
	var matches []string
	for _, cmd := range commands {
		if strings.HasPrefix(cmd, prefix) {
			matches = append(matches, cmd)
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
			return fmt.Sprintf("▶ bash: %s", cmd)
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
			return fmt.Sprintf("▶ %s", cmd)
		}
	}
	return fmt.Sprintf("▶ %s", toolName)
}

func collapseToolResult(result string) string {
	lines := strings.Split(result, "\n")
	if len(lines) <= 10 {
		return result
	}
	head := strings.Join(lines[:4], "\n")
	tail := strings.Join(lines[len(lines)-3:], "\n")
	return fmt.Sprintf("%s\n  ... (%d lines omitted)\n%s", head, len(lines)-7, tail)
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

type modelsMsg struct {
	models []ModelDef
	err    error
}

type sweScoresMsg struct {
	scores map[string]float64
	err    error
}

type containerReadyMsg struct {
	client       *ContainerClient
	worktreePath string
}

type containerErrMsg struct {
	err error
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

// ─── wrapLineCount (used by wrap_test.go) ───

func wrapLineCount(line string, width int) int {
	if width <= 0 {
		return 1
	}
	runes := []rune(line)
	if len(runes) == 0 {
		return 1
	}

	var (
		lines  = [][]rune{{}}
		word   = []rune{}
		row    int
		spaces int
	)

	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}

		if spaces > 0 {
			if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces > width {
				row++
				lines = append(lines, []rune{})
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], []rune(strings.Repeat(" ", spaces))...)
				spaces = 0
				word = nil
			} else {
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], []rune(strings.Repeat(" ", spaces))...)
				spaces = 0
				word = nil
			}
		} else if len(word) > 0 {
			lastCharLen := uniseg.StringWidth(string(word[len(word)-1:]))
			if uniseg.StringWidth(string(word))+lastCharLen > width {
				if len(lines[row]) > 0 {
					row++
					lines = append(lines, []rune{})
				}
				lines[row] = append(lines[row], word...)
				word = nil
			}
		}
	}

	if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces >= width {
		lines = append(lines, []rune{})
	}

	return len(lines)
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
	var workspace string
	repoRoot := gitRepoRoot()
	if repoRoot != "" {
		selected, _, err := selectWorktree(repoRoot)
		if err != nil {
			return workspaceMsg{}
		}
		if selected != "" {
			workspace = selected
		} else {
			cwd, _ := os.Getwd()
			workspace = cwd
		}
	} else {
		cwd, _ := os.Getwd()
		workspace = cwd
	}

	if repoRoot != "" && workspace != "" {
		_ = lockWorktree(workspace, os.Getpid())
	}

	return workspaceMsg{worktreePath: workspace}
}

func bootContainerCmd(cfg Config, workspace string) any {
	ccfg := cfg.containerConfig()
	client := NewContainerClient(ccfg)

	if !client.IsAvailable() {
		return containerErrMsg{err: fmt.Errorf(
			"Docker is not running. Please start Docker Desktop and try again.")}
	}

	mounts := []MountSpec{{
		Source:      workspace,
		Destination: "/workspace",
		ReadOnly:    false,
	}}

	if err := client.Start(workspace, mounts); err != nil {
		return containerErrMsg{err: fmt.Errorf("starting container: %w", err)}
	}

	return containerReadyMsg{client: client, worktreePath: workspace}
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

func fetchModelsCmd() modelsMsg {
	return modelsMsg{models: builtinModels()}
}

func fetchSWEScoresCmd() sweScoresMsg {
	scores, err := fetchSWEScores()
	return sweScoresMsg{scores: scores, err: err}
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

	// Input buffer (from simple-chat)
	input  []rune
	cursor int

	// Event channels
	resultCh chan any
	stopCh   chan struct{}
	quit     bool

	// Chat state
	messages         []chatMessage
	config           Config
	pasteCount       int
	pasteStore       map[int]string
	mode             appMode
	models           []ModelDef
	modelsErr        error
	modelsLoaded     bool
	sweScores        map[string]float64
	sweLoaded        bool
	container        *ContainerClient
	worktreePath     string
	containerReady   bool
	containerErr     error
	status           statusInfo
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

	// Menu state (for inline menus below input - Phase 3)
	menuLines  []string
	menuCursor int
	menuActive bool
	menuAction func(int)
}

func newApp() *App {
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("warning: loading config: %v (using defaults)", err)
	}

	return &App{
		config:   cfg,
		resultCh: make(chan any, 16),
		stopCh:   make(chan struct{}),
	}
}

// ─── Rendering (from simple-chat, adapted) ───

func (a *App) buildBlockRows() []string {
	var rows []string
	rows = append(rows, logo...)
	for _, msg := range a.messages {
		rendered := renderMessage(msg)
		for _, logLine := range strings.Split(rendered, "\n") {
			rows = append(rows, wrapString(logLine, 0, a.width)...)
		}
		rows = append(rows, "") // empty line after block
	}
	return rows
}

func (a *App) buildInputRows() []string {
	sep := strings.Repeat("─", a.width)
	rows := []string{sep}

	vlines := getVisualLines(a.input, a.cursor, a.width)
	for i, vl := range vlines {
		line := string(a.input[vl.start : vl.start+vl.length])
		if i == 0 {
			line = promptPrefix + line
		}
		rows = append(rows, line)
	}

	// Ephemeral indicators above bottom separator
	if a.awaitingApproval {
		rows = append(rows, fmt.Sprintf("\033[38;5;220;1mAllow %s? [y/n]\033[0m", a.approvalDesc))
	} else if a.streamingText != "" {
		rows = append(rows, styledAssistantText(a.streamingText))
	} else if a.agentRunning {
		rows = append(rows, "\033[38;5;141;3mthinking...\033[0m")
	}

	rows = append(rows, sep)

	// Progress bar row
	totalChars := 0
	for _, msg := range a.messages {
		totalChars += len([]rune(msg.content))
	}
	totalChars += len(a.input)
	bar := progressBar(totalChars, charLimit)
	barWidth := 3
	padding := a.width - barWidth
	if padding < 0 {
		padding = 0
	}
	rows = append(rows, strings.Repeat(" ", padding)+bar+"\033[0m\033[K")

	// Autocomplete / status below progress bar
	if matches := a.autocompleteMatches(); len(matches) > 0 {
		for i, cmd := range matches {
			if i == a.autocompleteIdx {
				rows = append(rows, fmt.Sprintf("\033[38;5;141;1m  > %s\033[0m", cmd))
			} else {
				rows = append(rows, fmt.Sprintf("\033[38;5;253m    %s\033[0m", cmd))
			}
		}
	}

	// Menu lines (Phase 3)
	for i, line := range a.menuLines {
		if a.menuActive && i == a.menuCursor {
			rows = append(rows, fmt.Sprintf("\033[38;5;141;1m  > %s\033[0m", line))
		} else {
			rows = append(rows, fmt.Sprintf("\033[38;5;253m    %s\033[0m", line))
		}
	}

	return rows
}

func (a *App) positionCursor(buf *strings.Builder) {
	curLine, curCol := cursorVisualPos(a.input, a.cursor, a.width)
	buf.WriteString(fmt.Sprintf("\033[%d;%dH", a.inputStartRow+curLine, curCol+1))
}

func (a *App) render() {
	blockRows := a.buildBlockRows()

	a.sepRow = len(blockRows) + 1
	a.inputStartRow = a.sepRow + 1

	inputRows := a.buildInputRows()
	totalRows := len(blockRows) + len(inputRows)

	var buf strings.Builder
	writeRows(&buf, blockRows, 1)
	writeRows(&buf, inputRows, a.sepRow)

	for i := totalRows; i < a.prevRowCount; i++ {
		buf.WriteString(fmt.Sprintf("\033[%d;1H\033[2K", i+1))
	}
	a.prevRowCount = totalRows

	a.positionCursor(&buf)
	os.Stdout.WriteString(buf.String())
}

func (a *App) renderInput() {
	inputRows := a.buildInputRows()
	totalRows := a.sepRow - 1 + len(inputRows)

	var buf strings.Builder
	writeRows(&buf, inputRows, a.sepRow)

	for i := totalRows; i < a.prevRowCount; i++ {
		buf.WriteString(fmt.Sprintf("\033[%d;1H\033[2K", i+1))
	}
	a.prevRowCount = totalRows

	a.positionCursor(&buf)
	os.Stdout.WriteString(buf.String())
}

// ─── Input helpers (from simple-chat) ───

func (a *App) insertAtCursor(r rune) {
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
	if a.cursor <= 0 {
		return
	}
	a.cursor--
	copy(a.input[a.cursor:], a.input[a.cursor+1:])
	a.input = a.input[:len(a.input)-1]
}

func (a *App) deleteAtCursor() {
	if a.cursor >= len(a.input) {
		return
	}
	copy(a.input[a.cursor:], a.input[a.cursor+1:])
	a.input = a.input[:len(a.input)-1]
}

func (a *App) deleteWordBackward() {
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
	// Delete from cursor to end of current line (or end of input)
	end := a.cursor
	for end < len(a.input) && a.input[end] != '\n' {
		end++
	}
	a.input = append(a.input[:a.cursor], a.input[end:]...)
}

func (a *App) killToStart() {
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

	// Enter alt-screen, enable bracketed paste
	fmt.Print("\033[?1049h")
	fmt.Print("\033[?2004h")
	defer func() {
		fmt.Print("\033[?2004l")
		fmt.Print("\033[?1049l")
		end := time.Now()
		fmt.Printf("[CPSL %s -> %s]\r\n",
			startTime.Format("Jan 02 15:04"),
			end.Format("Jan 02 15:04"))
		term.Restore(fd, oldState)
	}()

	a.width = getWidth()

	// SIGWINCH handler
	sigWinch := make(chan os.Signal, 1)
	signal.Notify(sigWinch, syscall.SIGWINCH)
	go func() {
		for range sigWinch {
			a.width = getWidth()
			a.render()
		}
	}()

	// Start async initialization
	a.startInit()

	// Initial render
	a.render()

	// Main byte-reading loop (from simple-chat, extended)
	raw := make([]byte, 1)
	for {
		_, err := os.Stdin.Read(raw)
		if err != nil {
			break
		}
		ch := raw[0]

		// Check for async results (non-blocking)
		a.drainResults()

		// Check for agent events (non-blocking)
		a.drainAgentEvents()

		// Escape sequence
		if ch == '\033' {
			a.handleEscapeSequence(raw)
			continue
		}

		// Ctrl+C / Ctrl+D
		if ch == 3 || ch == 4 {
			break
		}

		// Handle approval mode
		if a.awaitingApproval {
			a.handleApprovalByte(ch)
			continue
		}

		// Ctrl+W: delete word backward
		if ch == 0x17 {
			a.deleteWordBackward()
			a.autocompleteIdx = 0
			a.renderInput()
			continue
		}

		// Ctrl+U: kill to start of line
		if ch == 0x15 {
			a.killToStart()
			a.renderInput()
			continue
		}

		// Ctrl+K: kill to end of line
		if ch == 0x0b {
			a.killLine()
			a.renderInput()
			continue
		}

		// Ctrl+A: home
		if ch == 0x01 {
			a.cursor = 0
			a.renderInput()
			continue
		}

		// Ctrl+E: end
		if ch == 0x05 {
			a.cursor = len(a.input)
			a.renderInput()
			continue
		}

		// Tab
		if ch == '\t' {
			if matches := a.autocompleteMatches(); len(matches) > 0 {
				idx := a.autocompleteIdx
				if idx >= len(matches) {
					idx = 0
				}
				a.setInputValue(matches[idx])
				a.autocompleteIdx = 0
			}
			a.renderInput()
			continue
		}

		// Shift+Enter (LF) — insert newline
		if ch == '\n' {
			a.insertAtCursor('\n')
			a.renderInput()
			continue
		}

		// Enter (CR) — submit or menu select
		if ch == '\r' {
			if a.menuActive && a.menuAction != nil {
				a.menuAction(a.menuCursor)
				a.render()
				continue
			}
			a.handleEnter()
			continue
		}

		// Backspace
		if ch == 127 || ch == 0x08 {
			if a.cursor > 0 {
				a.deleteBeforeCursor()
				a.autocompleteIdx = 0
				a.renderInput()
			}
			continue
		}

		// Regular character (possibly multi-byte UTF-8)
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

		prevVal := a.inputValue()
		a.insertAtCursor(r)
		if a.inputValue() != prevVal {
			a.autocompleteIdx = 0
		}
		a.renderInput()
	}

	a.cleanup()
	return nil
}

func (a *App) handleEscapeSequence(raw []byte) {
	os.Stdin.Read(raw)

	// Alt+Enter: ESC CR
	if raw[0] == '\r' {
		if !a.awaitingApproval {
			a.insertAtCursor('\n')
			a.renderInput()
		}
		return
	}

	if raw[0] != '[' {
		// Escape key
		if a.awaitingApproval {
			return
		}
		if a.menuActive {
			a.menuLines = nil
			a.menuActive = false
			a.menuAction = nil
			a.menuCursor = 0
			a.renderInput()
			return
		}
		if strings.HasPrefix(a.inputValue(), "/") {
			a.resetInput()
			a.autocompleteIdx = 0
			a.renderInput()
		}
		return
	}

	// CSI sequence: ESC [
	os.Stdin.Read(raw)

	// Check for bracketed paste: ESC [ 2 0 0 ~
	if raw[0] == '2' {
		a.handlePossibleBracketedPaste(raw)
		return
	}

	// Modified key sequences: ESC [ 1 ; <mod> <letter>
	if raw[0] == '1' {
		a.handleModifiedCSI(raw)
		return
	}

	// Tilde sequences: ESC [ <number> ~
	if raw[0] >= '3' && raw[0] <= '6' {
		// Read the tilde
		var tilde [1]byte
		os.Stdin.Read(tilde[:])
		if tilde[0] == '~' {
			switch raw[0] {
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

	switch raw[0] {
	case 'A': // Up
		if a.menuActive {
			a.menuCursor--
			if a.menuCursor < 0 {
				a.menuCursor = len(a.menuLines) - 1
			}
		} else if matches := a.autocompleteMatches(); len(matches) > 0 {
			a.autocompleteIdx--
			if a.autocompleteIdx < 0 {
				a.autocompleteIdx = len(matches) - 1
			}
		} else {
			a.moveUp()
		}
		a.renderInput()
	case 'B': // Down
		if a.menuActive {
			a.menuCursor++
			if a.menuCursor >= len(a.menuLines) {
				a.menuCursor = 0
			}
		} else if matches := a.autocompleteMatches(); len(matches) > 0 {
			a.autocompleteIdx++
			if a.autocompleteIdx >= len(matches) {
				a.autocompleteIdx = 0
			}
		} else {
			a.moveDown()
		}
		a.renderInput()
	case 'C': // Right
		if a.cursor < len(a.input) {
			a.cursor++
			a.renderInput()
		}
	case 'D': // Left
		if a.cursor > 0 {
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

func (a *App) handlePossibleBracketedPaste(raw []byte) {
	// We've read ESC [ 2, check for 0 0 ~
	var buf [3]byte
	os.Stdin.Read(buf[0:1])
	os.Stdin.Read(buf[1:2])
	os.Stdin.Read(buf[2:3])

	if buf[0] == '0' && buf[1] == '0' && buf[2] == '~' {
		// Bracketed paste start - read until ESC [ 2 0 1 ~
		var content []byte
		for {
			os.Stdin.Read(raw)
			if raw[0] == '\033' {
				// Check for paste end sequence
				var end [5]byte
				os.Stdin.Read(end[0:1])
				if end[0] == '[' {
					os.Stdin.Read(end[1:2])
					os.Stdin.Read(end[2:3])
					os.Stdin.Read(end[3:4])
					os.Stdin.Read(end[4:5])
					if end[1] == '2' && end[2] == '0' && end[3] == '1' && end[4] == '~' {
						break
					}
					// Not end sequence, include the bytes
					content = append(content, '\033', end[0], end[1], end[2], end[3], end[4])
				} else {
					content = append(content, '\033', end[0])
				}
			} else {
				content = append(content, raw[0])
			}
		}
		a.handlePaste(string(content))
		return
	}

	// Not a bracketed paste, might be another sequence starting with 2
	// e.g., Insert key (ESC [ 2 ~)
	if buf[0] == '~' {
		// ESC [ 2 ~ = Insert
		return
	}
}

func (a *App) handleModifiedCSI(raw []byte) {
	// We've read ESC [ 1, expect ; <mod> <letter>
	var buf [3]byte
	n := 0
	os.Stdin.Read(buf[0:1]) // ;
	if buf[0] == ';' {
		os.Stdin.Read(buf[1:2]) // mod digit
		os.Stdin.Read(buf[2:3]) // letter
		n = 3
	}
	if n < 3 {
		return
	}

	if a.awaitingApproval {
		return
	}

	modNum := int(buf[1] - '0')
	modNum-- // CSI encoding
	isCtrl := modNum&4 != 0

	switch buf[2] {
	case 'C': // Right
		if isCtrl {
			a.moveWordRight()
		} else if a.cursor < len(a.input) {
			a.cursor++
		}
		a.renderInput()
	case 'D': // Left
		if isCtrl {
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

	val := strings.TrimSpace(a.inputValue())
	if val == "" {
		return
	}

	if strings.HasPrefix(val, "/") {
		a.resetInput()
		a.handleCommand(val)
		return
	}

	content := expandPastes(val, a.pasteStore)
	a.resetInput()

	if a.langdagClient == nil {
		a.messages = append(a.messages, chatMessage{kind: msgUser, content: content, leadBlank: true})
		a.messages = append(a.messages, chatMessage{kind: msgError, content: "No API keys configured. Use /config to add a key first."})
		a.render()
		return
	}

	a.messages = append(a.messages, chatMessage{kind: msgUser, content: content, leadBlank: true})
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
		a.render()

	case "/config":
		// Phase 3: inline config display
		a.messages = append(a.messages, chatMessage{kind: msgInfo, content: fmt.Sprintf(
			"Config: paste_collapse=%d, anthropic=%s, openai=%s, grok=%s, gemini=%s, model=%s",
			a.config.PasteCollapseMinChars,
			maskKey(a.config.AnthropicAPIKey),
			maskKey(a.config.OpenAIAPIKey),
			maskKey(a.config.GrokAPIKey),
			maskKey(a.config.GeminiAPIKey),
			a.config.ActiveModel,
		)})
		a.render()

	case "/model":
		if !a.modelsLoaded {
			a.messages = append(a.messages, chatMessage{kind: msgInfo, content: "Models are still loading... please try again in a moment."})
			a.render()
			return
		}
		if a.modelsErr != nil {
			a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Failed to load models: %v", a.modelsErr)})
			a.render()
			return
		}
		available := a.config.availableModels(a.models)
		if len(available) == 0 {
			a.messages = append(a.messages, chatMessage{kind: msgError, content: "No API keys configured. Use /config to add a key first."})
			a.render()
			return
		}
		// Phase 3: inline model selection with menu
		var lines []string
		for _, m := range available {
			lines = append(lines, fmt.Sprintf("%s (%s)", m.DisplayName, m.Provider))
		}
		a.menuLines = lines
		a.menuCursor = 0
		a.menuActive = true
		a.menuAction = func(idx int) {
			if idx >= 0 && idx < len(available) {
				selected := available[idx]
				a.config.ActiveModel = selected.ID
				if err := saveConfig(a.config); err != nil {
					a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Error saving model: %v", err)})
				} else {
					a.messages = append(a.messages, chatMessage{kind: msgSuccess, content: fmt.Sprintf("Model set to %s.", selected.DisplayName)})
				}
			}
			a.menuLines = nil
			a.menuActive = false
			a.menuAction = nil
		}
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
			a.menuActive = false
			a.menuAction = nil
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
		if len(wts) == 0 {
			a.messages = append(a.messages, chatMessage{kind: msgInfo, content: "No worktrees found."})
			a.render()
			return
		}
		var lines []string
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
		a.menuActive = true
		a.menuAction = func(idx int) {
			if idx >= 0 && idx < len(wts) {
				selected := wts[idx]
				a.worktreePath = selected.Path
				a.status.WorktreeName = filepath.Base(selected.Path)
				a.status.Branch = selected.Branch
				a.messages = append(a.messages, chatMessage{kind: msgSuccess, content: fmt.Sprintf("Switched to worktree '%s' (%s)", filepath.Base(selected.Path), selected.Branch)})
			}
			a.menuLines = nil
			a.menuActive = false
			a.menuAction = nil
		}
		a.renderInput()

	case "/shell":
		a.enterShellMode()

	default:
		a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Unknown command: %s", cmd)})
		a.render()
	}
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

	// Exit alt screen, restore terminal
	fmt.Print("\033[?2004l") // disable bracketed paste
	fmt.Print("\033[?1049l") // exit alt screen
	term.Restore(a.fd, a.oldState)

	// Run shell synchronously
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

	// Re-enter alt screen, re-enable bracketed paste
	fmt.Print("\033[?1049h")
	fmt.Print("\033[?2004h")

	a.width = getWidth()

	if shellErr != nil {
		a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Shell error: %v", shellErr)})
	} else {
		a.messages = append(a.messages, chatMessage{kind: msgInfo, content: "Shell session ended."})
	}

	a.render()
}

// ─── Agent ───

func (a *App) startAgent(userMessage string) {
	var tools []Tool
	if a.containerReady && a.container != nil {
		tools = append(tools, NewBashTool(a.container, 120))
	}
	if a.worktreePath != "" {
		tools = append(tools, NewGitTool(a.worktreePath))
	}

	modelID := ""
	if a.modelsLoaded {
		modelID = a.config.resolveActiveModel(a.models)
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

	workDir := "/workspace"
	systemPrompt := buildSystemPrompt(tools, workDir)

	agent := NewAgent(a.langdagClient, tools, systemPrompt, modelID)
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
		a.messages = append(a.messages, chatMessage{kind: msgToolResult, content: result, isError: event.IsError})
		a.render()

	case EventToolCallDone:
		// Already handled by EventToolResult

	case EventApprovalReq:
		debugLog("approval_req: %s", event.ApprovalDesc)
		a.awaitingApproval = true
		a.approvalDesc = event.ApprovalDesc
		a.renderInput()

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
	go func() { a.resultCh <- fetchModelsCmd() }()
	go func() { a.resultCh <- fetchSWEScoresCmd() }()
	go func() { a.resultCh <- resolveWorkspaceCmd(cfg) }()
	go func() {
		client, err := newLangdagClient(cfg)
		a.resultCh <- langdagReadyMsg{client: client, provider: cfg.defaultLangdagProvider(), err: err}
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
	case modelsMsg:
		a.modelsLoaded = true
		a.modelsErr = msg.err
		if msg.err == nil {
			a.models = msg.models
			if a.sweLoaded && a.sweScores != nil {
				matchSWEScores(a.models, a.sweScores)
			}
		}

	case sweScoresMsg:
		a.sweLoaded = true
		if msg.err == nil {
			a.sweScores = msg.scores
			if a.modelsLoaded && a.models != nil {
				matchSWEScores(a.models, a.sweScores)
			}
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
		cfg := a.config
		wtPath := msg.worktreePath
		go func() { a.resultCh <- fetchStatusCmd(wtPath) }()
		go func() { a.resultCh <- bootContainerCmd(cfg, wtPath) }()

	case containerReadyMsg:
		a.container = msg.client
		a.worktreePath = msg.worktreePath
		a.containerReady = true

	case containerErrMsg:
		a.containerErr = msg.err

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
	if err := app.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
