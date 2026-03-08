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
	return "\033[1m❯ " + content + "\033[0m"
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

	// Config editor state
	cfgActive     bool
	cfgTab        int
	cfgCursor     int
	cfgEditing    bool
	cfgEditBuf    []rune
	cfgEditCursor int
	cfgDraft      Config
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
	for i, msg := range a.messages {
		rendered := renderMessage(msg)
		for _, logLine := range strings.Split(rendered, "\n") {
			rows = append(rows, wrapString(logLine, 0, a.width)...)
		}
		// Add blank line after block, unless next message already has leadBlank
		nextHasBlank := i+1 < len(a.messages) && a.messages[i+1].leadBlank
		if !nextHasBlank {
			rows = append(rows, "")
		}
	}
	// Show streaming text or thinking indicator above the input area
	if a.streamingText != "" {
		for _, logLine := range strings.Split(styledAssistantText(a.streamingText), "\n") {
			rows = append(rows, wrapString(logLine, 0, a.width)...)
		}
		rows = append(rows, "")
	} else if a.agentRunning {
		rows = append(rows, "\033[2;3mthinking...\033[0m")
		rows = append(rows, "")
	}
	return rows
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
		for i, line := range a.menuLines {
			if i == a.menuCursor {
				rows = append(rows, fmt.Sprintf("\033[36;1m%s ◆\033[0m", line))
			} else {
				rows = append(rows, line)
			}
		}
		rows = append(rows, sep)
		return rows
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
		// Line 1: /b branch + progress bar on right
		branchLabel := ""
		if a.status.Branch != "" {
			branchLabel = "\033[2m/b " + a.status.Branch + "\033[0m"
		}
		totalChars := 0
		for _, msg := range a.messages {
			totalChars += len([]rune(msg.content))
		}
		totalChars += len(a.input)
		bar := progressBar(totalChars, charLimit)
		barWidth := 3
		branchTextWidth := 0
		if a.status.Branch != "" {
			branchTextWidth = 3 + len(a.status.Branch) // "/b " + branch name
		}
		padding := a.width - branchTextWidth - barWidth - 1 // -1 for trailing space after bar
		if padding < 0 {
			padding = 0
		}
		rows = append(rows, branchLabel+strings.Repeat(" ", padding)+bar+" ")

		// Line 2: /w worktree
		if a.status.WorktreeName != "" {
			rows = append(rows, "\033[2m/w "+a.status.WorktreeName+"\033[0m\033[K")
		}
	}

	return rows
}

func (a *App) positionCursor(buf *strings.Builder) {
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
			buf.WriteString(fmt.Sprintf("\033[%d;%dH", fieldRow, col+1))
		} else {
			buf.WriteString(fmt.Sprintf("\033[%d;1H", a.sepRow+1))
		}
		return
	}
	if a.menuActive && len(a.menuLines) > 0 {
		// Menu between separators — hide cursor
		buf.WriteString(fmt.Sprintf("\033[%d;1H", a.sepRow+1))
		return
	}
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

	// Read stdin in a goroutine so we can select on it alongside channels
	stdinCh := make(chan byte, 64)
	go func() {
		buf := make([]byte, 1)
		for {
			_, err := os.Stdin.Read(buf)
			if err != nil {
				close(stdinCh)
				return
			}
			stdinCh <- buf[0]
		}
	}()

	// readByte reads a single byte from the stdin channel (blocking).
	readByte := func() (byte, bool) {
		b, ok := <-stdinCh
		return b, ok
	}

	// Main event loop — selects on stdin, agent events, and async results
	for {
		// If agent is running, select on all channels.
		// Otherwise, just wait for stdin or async results.
		if a.agent != nil && a.agentRunning {
			select {
			case ch, ok := <-stdinCh:
				if !ok {
					goto done
				}
				a.drainResults()
				a.drainAgentEvents()
				if a.handleByte(ch, stdinCh, readByte) {
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
			case ch, ok := <-stdinCh:
				if !ok {
					goto done
				}
				a.drainResults()
				a.drainAgentEvents()
				if a.handleByte(ch, stdinCh, readByte) {
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
	// Config editor mode intercept
	if a.cfgActive {
		a.handleConfigByte(ch, stdinCh, readByte)
		return false
	}

	// Escape sequence
	if ch == '\033' {
		a.handleEscapeSequence(stdinCh, readByte)
		return false
	}

	// Ctrl+C / Ctrl+D
	if ch == 3 || ch == 4 {
		return true
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
	if b == '2' {
		a.handlePossibleBracketedPaste(readByte)
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

func (a *App) handlePossibleBracketedPaste(readByte func() (byte, bool)) {
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
		// Bracketed paste start - read until ESC [ 2 0 1 ~
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
					e1, _ := readByte()
					e2, _ := readByte()
					e3, _ := readByte()
					e4, _ := readByte()
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
		a.handlePaste(string(content))
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
	isCtrl := modNum&4 != 0

	switch letter {
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
		a.enterConfigMode()

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

// ─── Config editor ───

var cfgTabNames = []string{"API Keys", "Settings"}

type cfgField struct {
	label   string
	get     func(Config) string
	display func(Config) string // masked display; nil means use get
	set     func(*Config, string)
}

var cfgTabFields = [][]cfgField{
	{ // Tab 0: API Keys
		{label: "Anthropic", get: func(c Config) string { return c.AnthropicAPIKey }, display: func(c Config) string { return maskKey(c.AnthropicAPIKey) }, set: func(c *Config, v string) { c.AnthropicAPIKey = v }},
		{label: "OpenAI", get: func(c Config) string { return c.OpenAIAPIKey }, display: func(c Config) string { return maskKey(c.OpenAIAPIKey) }, set: func(c *Config, v string) { c.OpenAIAPIKey = v }},
		{label: "Grok", get: func(c Config) string { return c.GrokAPIKey }, display: func(c Config) string { return maskKey(c.GrokAPIKey) }, set: func(c *Config, v string) { c.GrokAPIKey = v }},
		{label: "Gemini", get: func(c Config) string { return c.GeminiAPIKey }, display: func(c Config) string { return maskKey(c.GeminiAPIKey) }, set: func(c *Config, v string) { c.GeminiAPIKey = v }},
	},
	{ // Tab 1: Settings
		{label: "Paste Collapse", get: func(c Config) string { return strconv.Itoa(c.PasteCollapseMinChars) }, set: func(c *Config, v string) { if n, err := strconv.Atoi(v); err == nil { c.PasteCollapseMinChars = n } }},
		{label: "Container Image", get: func(c Config) string { if c.ContainerImage == "" { return defaultContainerImage }; return c.ContainerImage }, set: func(c *Config, v string) { c.ContainerImage = v }},
	},
}

func (a *App) enterConfigMode() {
	a.cfgActive = true
	a.cfgTab = 0
	a.cfgCursor = 0
	a.cfgEditing = false
	a.cfgEditBuf = nil
	a.cfgEditCursor = 0
	a.cfgDraft = a.config
	a.renderInput()
}

func (a *App) exitConfigMode(save bool) {
	if save {
		a.config = a.cfgDraft
		if err := saveConfig(a.config); err != nil {
			a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Error saving config: %v", err)})
		} else {
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

func (a *App) cfgCurrentFields() []cfgField {
	if a.cfgTab >= 0 && a.cfgTab < len(cfgTabFields) {
		return cfgTabFields[a.cfgTab]
	}
	return nil
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
				val = "(not set)"
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

	case ch == '\r': // Enter - start editing current field
		fields := a.cfgCurrentFields()
		if len(fields) > 0 && a.cfgCursor < len(fields) {
			f := fields[a.cfgCursor]
			a.cfgEditing = true
			val := f.get(a.cfgDraft)
			a.cfgEditBuf = []rune(val)
			a.cfgEditCursor = len(a.cfgEditBuf)
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
