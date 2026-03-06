package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/lipgloss/v2"
	"langdag.com/langdag"
	"github.com/rivo/uniseg"
)

const (
	minInputHeight = 1
	maxInputHeight = 12
)

// Purple gradient colors for the logo (smooth dark-to-light-to-dark)
var logoColors = []color.Color{
	lipgloss.Color("#3A0066"),
	lipgloss.Color("#5B1A99"),
	lipgloss.Color("#7B3EC7"),
	lipgloss.Color("#9B6ADE"),
	lipgloss.Color("#B88AFF"),
	lipgloss.Color("#9B6ADE"),
}

var logoLines = []string{
	" ██████╗ ██████╗  ███████╗██╗     ",
	"██╔════╝ ██╔══██╗ ██╔════╝██║     ",
	"██║      ██████╔╝ ███████╗██║     ",
	"██║      ██╔═══╝  ╚════██║██║     ",
	"╚██████╗ ██║      ███████║███████╗",
	" ╚═════╝ ╚═╝      ╚══════╝╚══════╝",
}

func renderLogo() string {
	var rendered []string
	for i, line := range logoLines {
		c := logoColors[i%len(logoColors)]
		style := lipgloss.NewStyle().Foreground(c).Bold(true)
		rendered = append(rendered, style.Render(line))
	}
	return strings.Join(rendered, "\n")
}

var borderGradientColors = []color.Color{
	lipgloss.Color("#6B34B0"),
	lipgloss.Color("#9B82F5"),
	lipgloss.Color("#B8A9FF"),
	lipgloss.Color("#9B82F5"),
	lipgloss.Color("#6B34B0"),
}

type appMode int

const (
	modeChat appMode = iota
	modeConfig
	modeModel
	modeWorktrees
	modeBranches
)

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

// commands is the list of available slash commands.
var commands = []string{"/branches", "/clear", "/config", "/model", "/shell", "/worktrees"}

// filterCommands returns commands matching the given prefix.
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

// expandPastes replaces paste placeholders with actual content from the paste store.
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

type model struct {
	textarea     textarea.Model
	width        int
	height       int
	ready        bool // true after first WindowSizeMsg
	messages     []chatMessage
	config       Config
	pasteCount   int
	pasteStore   map[int]string // paste ID → actual content
	mode         appMode
	configForm   configForm
	modelList    modelList
	worktreeListC worktreeList
	branchListC   branchList
	models       []ModelDef
	modelsErr    error
	modelsLoaded bool
	sweScores      map[string]float64
	sweLoaded      bool
	container      *ContainerClient
	worktreePath   string
	containerReady bool
	containerErr   error
	status         statusInfo
	langdagClient    *langdag.Client
	langdagProvider  string // current provider the langdag client is configured for
	agent            *Agent
	agentNodeID      string // last assistant node ID for conversation continuity
	agentRunning     bool   // true while the agent loop is executing
	awaitingApproval bool   // true when waiting for user y/n on a tool call
	approvalDesc     string // human-readable description of the pending approval
	autocompleteIdx  int    // currently selected autocomplete item
	streamingText    string // accumulated text from EventTextDelta (trailing incomplete line)
	pendingToolCall  string // tool call summary waiting for result
	needsTextSep     bool   // true when next assistant text output needs a blank line before it
	logoPrinted      bool   // true after logo has been printed via tea.Println
	printedMsgCount  int    // number of messages already printed via tea.Println
}

// autocompleteMatches returns matching commands for the current textarea input,
// or nil if autocomplete should not be shown.
func (m model) autocompleteMatches() []string {
	if m.mode != modeChat {
		return nil
	}
	val := m.textarea.Value()
	if !strings.HasPrefix(val, "/") {
		return nil
	}
	return filterCommands(val)
}

func initialModel() model {
	ta := textarea.New()
	ta.Placeholder = "Type a message..."
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.SetHeight(minInputHeight)
	ta.CharLimit = 0
	ta.MaxHeight = 0 // no limit on content; we control visual height ourselves
	ta.SetVirtualCursor(false)
	ta.EndOfBufferCharacter = ' '

	// Enter sends the message. Shift+Enter or Alt+Enter inserts a newline.
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("shift+enter", "alt+enter"),
		key.WithHelp("shift+enter", "new line"),
	)

	s := ta.Styles()
	s.Focused.CursorLine = lipgloss.NewStyle()
	s.Focused.Base = lipgloss.NewStyle()
	s.Focused.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("#666666"))
	s.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color("#E0E0E0"))
	s.Blurred.CursorLine = lipgloss.NewStyle()
	s.Blurred.Base = lipgloss.NewStyle()
	ta.SetStyles(s)
	ta.Focus()

	cfg, err := loadConfig()
	if err != nil {
		log.Printf("warning: loading config: %v (using defaults)", err)
	}

	return model{
		textarea: ta,
		config:   cfg,
	}
}

// --- Styling helpers ---

func styledUserMsg(content string, width int) string {
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#E0E0E0"))
	if width > 0 {
		style = style.Width(width)
	}
	return style.Render("❯ " + content)
}

func styledAssistantText(content string, width int) string {
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#B88AFF"))
	if width > 0 {
		style = style.Width(width)
	}
	return style.Render(content)
}

func styledToolCall(summary string, width int) string {
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#666666")).
		Italic(true)
	if width > 0 {
		style = style.Width(width)
	}
	return style.Render(summary)
}

func styledToolResult(result string, isError bool, width int) string {
	if isError {
		return styledError(result, width)
	}
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#555555"))
	if width > 0 {
		style = style.Width(width)
	}
	return style.Render(result)
}

func styledError(msg string, width int) string {
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FF6B6B")).
		Italic(true)
	if width > 0 {
		style = style.Width(width)
	}
	return style.Render(msg)
}

func styledSuccess(msg string, width int) string {
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6FE7B8")).
		Italic(true)
	if width > 0 {
		style = style.Width(width)
	}
	return style.Render(msg)
}

func styledInfo(msg string, width int) string {
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#8B7BA8")).
		Italic(true)
	if width > 0 {
		style = style.Width(width)
	}
	return style.Render(msg)
}

// renderMessage renders a single chat message with the given width.
func renderMessage(msg chatMessage, width int) string {
	var parts []string
	if msg.leadBlank {
		parts = append(parts, "")
	}
	var rendered string
	switch msg.kind {
	case msgUser:
		rendered = styledUserMsg(msg.content, width)
	case msgAssistant:
		rendered = styledAssistantText(msg.content, width)
	case msgToolCall:
		rendered = styledToolCall(msg.content, width)
	case msgToolResult:
		rendered = styledToolResult(msg.content, msg.isError, width)
	case msgInfo:
		rendered = styledInfo(msg.content, width)
	case msgSuccess:
		rendered = styledSuccess(msg.content, width)
	case msgError:
		rendered = styledError(msg.content, width)
	}
	parts = append(parts, rendered)
	return strings.Join(parts, "\n")
}

// renderMessages renders all stored chat messages with current width.
func (m model) renderMessages() string {
	if len(m.messages) == 0 {
		return ""
	}
	var parts []string
	for _, msg := range m.messages {
		parts = append(parts, renderMessage(msg, m.width))
	}
	return strings.Join(parts, "\n")
}


// renderStreamingText renders the trailing incomplete line with assistant styling.
func (m model) renderStreamingText() string {
	if m.streamingText == "" {
		return ""
	}
	return styledAssistantText(m.streamingText, m.width)
}

// debugLog writes a timestamped event entry to ~/.cpsl-debug.log when CPSL_DEBUG is set.
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

// --- Message types ---

// modelsMsg carries the result of the async model fetch.
type modelsMsg struct {
	models []ModelDef
	err    error
}

// sweScoresMsg carries the result of the async SWE-bench fetch.
type sweScoresMsg struct {
	scores map[string]float64
	err    error
}

// containerReadyMsg signals that the container has started successfully.
type containerReadyMsg struct {
	client       *ContainerClient
	worktreePath string
}

// statusInfo holds cached status bar data.
type statusInfo struct {
	Branch       string
	PRNumber     int // 0 = no PR
	WorktreeName string
	ActiveCount  int
	TotalCount   int
	DiffAdd      int
	DiffDel      int
}

// statusInfoMsg carries the result of the async status bar fetch.
type statusInfoMsg struct {
	info statusInfo
}

// worktreeListMsg carries the result of the async worktree list fetch.
type worktreeListMsg struct {
	items []WorktreeInfo
	err   error
}

// branchListMsg carries the result of the async branch list fetch.
type branchListMsg struct {
	items         []string
	currentBranch string
	err           error
}

// branchCheckoutMsg carries the result of a git checkout operation.
type branchCheckoutMsg struct {
	branch string
	err    error
}

// workspaceMsg carries the resolved workspace path from resolveWorkspaceCmd.
type workspaceMsg struct {
	worktreePath string
}

// containerErrMsg signals that the container failed to start.
type containerErrMsg struct {
	err error
}

// shellExitMsg is sent when the interactive shell session ends.
type shellExitMsg struct {
	err error
}

// langdagReadyMsg signals that the langdag client has been initialized.
type langdagReadyMsg struct {
	client   *langdag.Client
	provider string
	err      error
}

// agentEventMsg wraps an AgentEvent for the bubbletea Update loop.
type agentEventMsg struct {
	event AgentEvent
}

// listenForAgentEvent returns a tea.Cmd that reads the next event from the agent channel.
func listenForAgentEvent(events <-chan AgentEvent) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return agentEventMsg{AgentEvent{Type: EventDone}}
		}
		return agentEventMsg{event}
	}
}

// gitRepoRoot returns the git repository root, or empty string if not in a repo.
func gitRepoRoot() string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// resolveWorkspaceCmd determines the workspace path (worktree selection + lock)
// without starting Docker. Returns workspaceMsg.
func resolveWorkspaceCmd(cfg Config) tea.Msg {
	var workspace string
	repoRoot := gitRepoRoot()
	if repoRoot != "" {
		selected, _, err := selectWorktree(repoRoot)
		if err != nil {
			return containerErrMsg{err: fmt.Errorf("worktree selection: %w", err)}
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

	// Lock the worktree if it's under the worktree base dir.
	if repoRoot != "" && workspace != "" {
		_ = lockWorktree(workspace, os.Getpid())
	}

	return workspaceMsg{worktreePath: workspace}
}

// bootContainerCmd creates a container client and starts the container with
// the given workspace path. Runs as an async tea.Cmd.
func bootContainerCmd(cfg Config, workspace string) tea.Msg {
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

// fetchStatusCmd gathers status bar info: branch name, PR number, worktree
// name, and active worktree count. Runs git/gh commands in the worktree dir.
func fetchStatusCmd(worktreePath string) tea.Msg {
	var info statusInfo

	// Branch name from the worktree.
	info.Branch = worktreeBranch(worktreePath)

	// PR number from gh (optional, fails silently).
	ghCmd := exec.Command("gh", "pr", "view", "--json", "number", "-q", ".number")
	ghCmd.Dir = worktreePath
	if out, err := ghCmd.Output(); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil {
			info.PRNumber = n
		}
	}

	// Diff stats: insertions/deletions vs default branch (main).
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

	// Worktree name is the base directory name.
	info.WorktreeName = filepath.Base(worktreePath)

	// Count active (locked) worktrees.
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

// fetchModelsCmd returns the hardcoded model list.
func fetchModelsCmd() tea.Msg {
	return modelsMsg{models: builtinModels()}
}

// fetchSWEScoresCmd returns a tea.Cmd that fetches SWE-bench scores.
func fetchSWEScoresCmd() tea.Msg {
	scores, err := fetchSWEScores()
	return sweScoresMsg{scores: scores, err: err}
}

func (m model) Init() tea.Cmd {
	cfg := m.config
	return tea.Batch(textarea.Blink, fetchModelsCmd, fetchSWEScoresCmd, func() tea.Msg {
		return resolveWorkspaceCmd(cfg)
	}, func() tea.Msg {
		client, err := newLangdagClient(cfg)
		return langdagReadyMsg{client: client, provider: cfg.defaultLangdagProvider(), err: err}
	})
}

// wrapLineCount reproduces the textarea's internal wrap() function exactly
// to count how many display lines a single logical line produces at the given width.
// This must stay in sync with charm.land/bubbles/v2/textarea wrap().
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

// displayLineCount calculates the total visual display lines for the textarea content.
func (m model) displayLineCount() int {
	val := m.textarea.Value()
	if val == "" {
		return 1
	}
	width := m.textarea.Width()
	if width <= 0 {
		return m.textarea.LineCount()
	}
	logicalLines := strings.Split(val, "\n")
	total := 0
	for _, line := range logicalLines {
		total += wrapLineCount(line, width)
	}
	return total
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	result, cmd := m.update(msg)
	// Print any new messages via tea.Println so they appear in terminal scrollback.
	if mdl, ok := result.(model); ok && mdl.mode == modeChat && mdl.ready {
		if n := len(mdl.messages); n > mdl.printedMsgCount {
			var printCmds []tea.Cmd
			for i := mdl.printedMsgCount; i < n; i++ {
				printCmds = append(printCmds, tea.Println(renderMessage(mdl.messages[i], mdl.width)))
			}
			mdl.printedMsgCount = n
			if cmd != nil {
				printCmds = append(printCmds, cmd)
			}
			return mdl, tea.Batch(printCmds...)
		}
		return mdl, cmd
	}
	return result, cmd
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Handle async model fetch result regardless of mode
	if msg, ok := msg.(modelsMsg); ok {
		m.modelsLoaded = true
		m.modelsErr = msg.err
		if msg.err == nil {
			m.models = msg.models
			if m.sweLoaded && m.sweScores != nil {
				matchSWEScores(m.models, m.sweScores)
			}
		}
		return m, nil
	}

	// Handle async SWE-bench scores result regardless of mode
	if msg, ok := msg.(sweScoresMsg); ok {
		m.sweLoaded = true
		if msg.err == nil {
			m.sweScores = msg.scores
			if m.modelsLoaded && m.models != nil {
				matchSWEScores(m.models, m.sweScores)
			}
		}
		return m, nil
	}

	// Handle langdag client initialization regardless of mode
	if msg, ok := msg.(langdagReadyMsg); ok {
		if msg.err != nil {
			log.Printf("warning: langdag init: %v", msg.err)
		} else {
			m.langdagClient = msg.client
			m.langdagProvider = msg.provider
		}
		return m, nil
	}

	// Handle agent events regardless of mode
	if msg, ok := msg.(agentEventMsg); ok {
		return m.handleAgentEvent(msg.event)
	}

	// Handle async status bar result regardless of mode
	if msg, ok := msg.(statusInfoMsg); ok {
		m.status = msg.info
		return m, nil
	}

	// Handle async worktree list result regardless of mode
	if msg, ok := msg.(worktreeListMsg); ok {
		if msg.err != nil {
			m.mode = modeChat
			m.messages = append(m.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Error listing worktrees: %v", msg.err)})
			return m, m.textarea.Focus()
		}
		m.worktreeListC = newWorktreeList(msg.items, m.worktreePath, m.width, m.height)
		return m, nil
	}

	// Handle async branch list result regardless of mode
	if msg, ok := msg.(branchListMsg); ok {
		if msg.err != nil {
			m.mode = modeChat
			m.messages = append(m.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Error listing branches: %v", msg.err)})
			return m, m.textarea.Focus()
		}
		m.branchListC = newBranchList(msg.items, msg.currentBranch, m.width, m.height)
		return m, nil
	}

	// Handle branch checkout result regardless of mode
	if msg, ok := msg.(branchCheckoutMsg); ok {
		m.mode = modeChat
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Checkout failed: %v", msg.err)})
		} else {
			m.status.Branch = msg.branch
			m.messages = append(m.messages, chatMessage{kind: msgSuccess, content: fmt.Sprintf("Switched to branch '%s'", msg.branch)})
		}
		return m, m.textarea.Focus()
	}

	// Handle workspace resolution result — fires status fetch + container boot in parallel
	if msg, ok := msg.(workspaceMsg); ok {
		m.worktreePath = msg.worktreePath
		cfg := m.config
		wtPath := msg.worktreePath
		return m, tea.Batch(
			func() tea.Msg { return fetchStatusCmd(wtPath) },
			func() tea.Msg { return bootContainerCmd(cfg, wtPath) },
		)
	}

	// Handle async container startup result regardless of mode
	if msg, ok := msg.(containerReadyMsg); ok {
		m.container = msg.client
		m.worktreePath = msg.worktreePath
		m.containerReady = true
		return m, nil
	}
	if msg, ok := msg.(containerErrMsg); ok {
		m.containerErr = msg.err
		return m, nil
	}

	// Handle interactive shell exit
	if msg, ok := msg.(shellExitMsg); ok {
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Shell error: %v", msg.err)})
		} else {
			m.messages = append(m.messages, chatMessage{kind: msgInfo, content: "Shell session ended."})
		}
		return m, nil
	}

	// Config mode: delegate to config form
	if m.mode == modeConfig {
		return m.updateConfigMode(msg)
	}

	// Model selection mode: delegate to model list
	if m.mode == modeModel {
		return m.updateModelMode(msg)
	}

	// Worktree list mode: delegate to worktree list
	if m.mode == modeWorktrees {
		return m.updateWorktreeMode(msg)
	}

	// Branch list mode: delegate to branch list
	if m.mode == modeBranches {
		return m.updateBranchMode(msg)
	}

	// Handle approval y/n input when awaiting user confirmation
	if m.awaitingApproval {
		if msg, ok := msg.(tea.KeyPressMsg); ok {
			switch msg.String() {
			case "y", "Y":
				m.awaitingApproval = false
				if m.agent != nil {
					m.agent.Approve(ApprovalResponse{Approved: true})
				}
				m.messages = append(m.messages, chatMessage{kind: msgSuccess, content: "Approved"})
				return m, listenForAgentEvent(m.agent.Events())
			case "n", "N":
				m.awaitingApproval = false
				if m.agent != nil {
					m.agent.Approve(ApprovalResponse{Approved: false})
				}
				m.messages = append(m.messages, chatMessage{kind: msgError, content: "Denied"})
				return m, listenForAgentEvent(m.agent.Events())
			case "ctrl+c":
				m.cleanup()
				return m, tea.Quit
			}
			// Ignore all other keys while awaiting approval
			return m, nil
		}
	}

	var cmds []tea.Cmd
	taMsg := tea.Msg(msg) // message forwarded to textarea (may be modified)

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		inputAreaWidth := m.width - 2 // border left + right
		if inputAreaWidth < 1 {
			inputAreaWidth = 1
		}
		m.textarea.SetWidth(inputAreaWidth)

		if !m.ready {
			m.ready = true
			// Print logo on first resize
			m.logoPrinted = true
			cmds = append(cmds, tea.Println(renderLogo()))
		} else {
			// Reset renderer line tracking so old View() lines don't
			// leak into scrollback as ghost frames on resize.
			cmds = append(cmds, tea.ClearScreen)
		}
		m.recalcTextareaHeight()

	case tea.PasteMsg:
		if len(msg.Content) >= m.config.PasteCollapseMinChars {
			m.pasteCount++
			if m.pasteStore == nil {
				m.pasteStore = make(map[int]string)
			}
			m.pasteStore[m.pasteCount] = msg.Content
			placeholder := fmt.Sprintf("[pasted #%d | %d chars]", m.pasteCount, len(msg.Content))
			taMsg = tea.PasteMsg{Content: placeholder}
		}

	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			m.cleanup()
			return m, tea.Quit
		case "up":
			if matches := m.autocompleteMatches(); len(matches) > 0 {
				m.autocompleteIdx--
				if m.autocompleteIdx < 0 {
					m.autocompleteIdx = len(matches) - 1
				}
				return m, nil
			}
		case "down":
			if matches := m.autocompleteMatches(); len(matches) > 0 {
				m.autocompleteIdx++
				if m.autocompleteIdx >= len(matches) {
					m.autocompleteIdx = 0
				}
				return m, nil
			}
		case "tab":
			if matches := m.autocompleteMatches(); len(matches) > 0 {
				idx := m.autocompleteIdx
				if idx >= len(matches) {
					idx = 0
				}
				m.textarea.SetValue(matches[idx])
				m.textarea.CursorEnd()
				m.autocompleteIdx = 0
				m.recalcTextareaHeight()
			}
			return m, nil
		case "esc":
			if strings.HasPrefix(m.textarea.Value(), "/") {
				m.textarea.Reset()
				m.textarea.SetHeight(minInputHeight)
				m.autocompleteIdx = 0
			}
			return m, nil
		case "enter":
			if m.agentRunning {
				return m, nil // ignore input while agent is working
			}
			val := strings.TrimSpace(m.textarea.Value())
			if val != "" {
				if strings.HasPrefix(val, "/") {
					if matches := filterCommands(val); len(matches) > 0 {
						idx := m.autocompleteIdx
						if idx >= len(matches) {
							idx = 0
						}
						val = matches[idx]
					}
					m.autocompleteIdx = 0
					return m.handleCommand(val)
				}
				content := expandPastes(val, m.pasteStore)
				m.textarea.Reset()
				m.textarea.SetHeight(minInputHeight)

				// Start agent if an LLM provider is configured
				if m.langdagClient == nil {
					m.messages = append(m.messages, chatMessage{kind: msgUser, content: content, leadBlank: true})
					m.messages = append(m.messages, chatMessage{kind: msgError, content: "No API keys configured. Use /config to add a key first."})
					return m, nil
				}

				m.messages = append(m.messages, chatMessage{kind: msgUser, content: content, leadBlank: true})
				if !m.containerReady {
					m.messages = append(m.messages, chatMessage{kind: msgInfo, content: "Container is still starting — the agent won't have bash or file tools until it's ready."})
				}
				m2, agentCmd := m.startAgent(content)
				return m2, agentCmd
			}
			return m, nil
		}

	}

	// Temporarily expand textarea to max height before Update so the
	// textarea's internal repositionView() doesn't scroll within a too-small
	// viewport. We'll shrink to the real height right after.
	m.textarea.SetHeight(maxInputHeight)

	// Update textarea (may receive modified PasteMsg with placeholder)
	prevVal := m.textarea.Value()
	var taCmd tea.Cmd
	m.textarea, taCmd = m.textarea.Update(taMsg)
	cmds = append(cmds, taCmd)

	// Reset autocomplete selection when input changes
	if m.textarea.Value() != prevVal {
		m.autocompleteIdx = 0
	}

	// Now set the correct height based on actual content
	m.recalcTextareaHeight()

	return m, tea.Batch(cmds...)
}

// enterConfigMode switches to the config editing mode.
func (m model) enterConfigMode() (tea.Model, tea.Cmd) {
	m.mode = modeConfig
	m.configForm = newConfigForm(m.config, m.width, m.height)
	m.textarea.Reset()
	m.textarea.SetHeight(minInputHeight)
	m.textarea.Blur()
	return m, nil
}

// exitConfigMode returns to chat mode, optionally saving config changes.
func (m model) exitConfigMode(save bool) (tea.Model, tea.Cmd) {
	m.mode = modeChat
	var cmds []tea.Cmd
	if save {
		m.configForm.applyTo(&m.config)
		if err := saveConfig(m.config); err != nil {
			m.messages = append(m.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Error saving config: %v", err)})
		} else {
			m.messages = append(m.messages, chatMessage{kind: msgSuccess, content: "Config saved."})
			// Reinitialize langdag client with new API keys
			if m.langdagClient != nil {
				m.langdagClient.Close()
				m.langdagClient = nil
				m.langdagProvider = ""
			}
			cfg := m.config
			cmds = append(cmds, func() tea.Msg {
				client, err := newLangdagClient(cfg)
				return langdagReadyMsg{client: client, provider: cfg.defaultLangdagProvider(), err: err}
			})
		}
	} else {
		m.messages = append(m.messages, chatMessage{kind: msgInfo, content: "Config changes discarded."})
	}
	cmds = append(cmds, m.textarea.Focus())
	return m, tea.Batch(cmds...)
}

// updateConfigMode handles input while the config form is active.
func (m model) updateConfigMode(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.configForm.width = msg.Width
		m.configForm.height = msg.Height
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			return m.exitConfigMode(false)
		case "enter":
			if m.configForm.validate() {
				return m.exitConfigMode(true)
			}
			// validation failed — stay in config mode, errors shown
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.configForm, cmd = m.configForm.Update(msg)
	return m, cmd
}

// enterModelMode switches to the model selection mode.
func (m model) enterModelMode() (tea.Model, tea.Cmd) {
	if !m.modelsLoaded {
		m.textarea.Reset()
		m.textarea.SetHeight(minInputHeight)
		m.messages = append(m.messages, chatMessage{kind: msgInfo, content: "Models are still loading... please try again in a moment."})
		return m, nil
	}
	if m.modelsErr != nil {
		m.textarea.Reset()
		m.textarea.SetHeight(minInputHeight)
		m.messages = append(m.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Failed to load models: %v", m.modelsErr)})
		return m, nil
	}
	available := m.config.availableModels(m.models)
	if len(available) == 0 {
		m.textarea.Reset()
		m.textarea.SetHeight(minInputHeight)
		m.messages = append(m.messages, chatMessage{kind: msgError, content: "No API keys configured. Use /config to add a key first."})
		return m, nil
	}
	m.mode = modeModel
	activeModel := m.config.resolveActiveModel(m.models)
	m.modelList = newModelList(available, activeModel, m.width, m.height, m.config.ModelSortDirs)
	m.textarea.Reset()
	m.textarea.SetHeight(minInputHeight)
	m.textarea.Blur()
	return m, nil
}

// exitModelMode returns to chat mode, optionally saving the selected model.
func (m model) exitModelMode(save bool) (tea.Model, tea.Cmd) {
	m.mode = modeChat
	// Always persist sort preferences
	m.config.ModelSortDirs = m.modelList.sortDirsMap()
	if save {
		selected := m.modelList.selected()
		m.config.ActiveModel = selected.ID
		if err := saveConfig(m.config); err != nil {
			m.messages = append(m.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Error saving model: %v", err)})
		} else {
			m.messages = append(m.messages, chatMessage{kind: msgSuccess, content: fmt.Sprintf("Model set to %s.", selected.DisplayName)})
		}
	} else {
		// Save sort preferences even on cancel
		_ = saveConfig(m.config)
		m.messages = append(m.messages, chatMessage{kind: msgInfo, content: "Model selection cancelled."})
	}
	return m, m.textarea.Focus()
}

// updateModelMode handles input while the model list is active.
func (m model) updateModelMode(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.modelList.width = msg.Width
		m.modelList.height = msg.Height
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			return m.exitModelMode(false)
		case "enter":
			return m.exitModelMode(true)
		}
	}

	var cmd tea.Cmd
	m.modelList, cmd = m.modelList.Update(msg)
	return m, cmd
}

// enterWorktreeMode switches to the worktree list mode.
func (m model) enterWorktreeMode() (tea.Model, tea.Cmd) {
	m.mode = modeWorktrees
	m.worktreeListC = newWorktreeList(nil, m.worktreePath, m.width, m.height)
	m.textarea.Reset()
	m.textarea.SetHeight(minInputHeight)
	m.textarea.Blur()

	// Fetch worktree list async
	repoRoot := gitRepoRoot()
	return m, func() tea.Msg {
		if repoRoot == "" {
			return worktreeListMsg{err: fmt.Errorf("not in a git repository")}
		}
		projectID, err := ensureProjectID(repoRoot)
		if err != nil {
			return worktreeListMsg{err: err}
		}
		baseDir := worktreeBaseDir(projectID)
		items, err := listWorktrees(baseDir)
		return worktreeListMsg{items: items, err: err}
	}
}

// exitWorktreeMode returns to chat mode.
func (m model) exitWorktreeMode() (tea.Model, tea.Cmd) {
	m.mode = modeChat
	return m, m.textarea.Focus()
}

// updateWorktreeMode handles input while the worktree list is active.
func (m model) updateWorktreeMode(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.worktreeListC.width = msg.Width
		m.worktreeListC.height = msg.Height
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			return m.exitWorktreeMode()
		}
	}

	var cmd tea.Cmd
	m.worktreeListC, cmd = m.worktreeListC.Update(msg)
	return m, cmd
}

// viewWorktrees renders the worktree list screen.
func (m model) viewWorktrees() tea.View {
	formBorder := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForegroundBlend(borderGradientColors...).
		Width(m.width).
		Height(m.height - 2).
		Padding(1, 0)

	formContent := m.worktreeListC.View()
	rendered := formBorder.Render(formContent)

	v := tea.NewView(rendered)
	v.AltScreen = true
	return v
}

// enterBranchMode switches to the branch list mode.
func (m model) enterBranchMode() (tea.Model, tea.Cmd) {
	m.mode = modeBranches
	m.branchListC = newBranchList(nil, m.status.Branch, m.width, m.height)
	m.textarea.Reset()
	m.textarea.SetHeight(minInputHeight)
	m.textarea.Blur()

	// Fetch branch list async
	wtPath := m.worktreePath
	return m, func() tea.Msg {
		dir := wtPath
		if dir == "" {
			dir = "."
		}
		// Get current branch
		headCmd := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
		headOut, err := headCmd.Output()
		if err != nil {
			return branchListMsg{err: fmt.Errorf("not in a git repository")}
		}
		currentBranch := strings.TrimSpace(string(headOut))

		// Get all branches
		branchCmd := exec.Command("git", "-C", dir, "branch", "-a", "--format=%(refname:short)")
		branchOut, err := branchCmd.Output()
		if err != nil {
			return branchListMsg{err: fmt.Errorf("failed to list branches: %w", err)}
		}
		var branches []string
		for _, line := range strings.Split(strings.TrimSpace(string(branchOut)), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				branches = append(branches, line)
			}
		}
		return branchListMsg{items: branches, currentBranch: currentBranch}
	}
}

// exitBranchMode returns to chat mode.
func (m model) exitBranchMode() (tea.Model, tea.Cmd) {
	m.mode = modeChat
	return m, m.textarea.Focus()
}

// updateBranchMode handles input while the branch list is active.
func (m model) updateBranchMode(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.branchListC.width = msg.Width
		m.branchListC.height = msg.Height
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			return m.exitBranchMode()
		}

	case branchSelected:
		// Run git checkout async
		branch := msg.name
		wtPath := m.worktreePath
		return m, func() tea.Msg {
			dir := wtPath
			if dir == "" {
				dir = "."
			}
			cmd := exec.Command("git", "-C", dir, "checkout", branch)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return branchCheckoutMsg{
					branch: branch,
					err:    fmt.Errorf("%s", strings.TrimSpace(string(out))),
				}
			}
			return branchCheckoutMsg{branch: branch}
		}
	}

	var cmd tea.Cmd
	m.branchListC, cmd = m.branchListC.Update(msg)
	return m, cmd
}

// viewBranches renders the branch list screen.
func (m model) viewBranches() tea.View {
	formBorder := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForegroundBlend(borderGradientColors...).
		Width(m.width).
		Height(m.height - 2).
		Padding(1, 0)

	formContent := m.branchListC.View()
	rendered := formBorder.Render(formContent)

	v := tea.NewView(rendered)
	v.AltScreen = true
	return v
}

// startAgent creates (or reuses) an Agent and kicks off the agent loop for the given user message.
func (m model) startAgent(userMessage string) (tea.Model, tea.Cmd) {
	// Build tools list based on available resources
	var tools []Tool
	if m.containerReady && m.container != nil {
		tools = append(tools, NewBashTool(m.container, 120))
	}
	if m.worktreePath != "" {
		tools = append(tools, NewGitTool(m.worktreePath))
	}

	// Resolve model and determine its provider
	modelID := ""
	if m.modelsLoaded {
		modelID = m.config.resolveActiveModel(m.models)
	}

	// Look up the model definition to get the provider
	var modelProvider string
	if modelDef := findModelByID(m.models, modelID); modelDef != nil {
		modelProvider = modelDef.Provider
	}

	// Ensure the langdag client is configured for the correct provider.
	// Recreate it if the provider changed (e.g. switching from Anthropic to Grok).
	if modelProvider != "" && modelProvider != m.langdagProvider {
		if m.langdagClient != nil {
			m.langdagClient.Close()
		}
		client, err := newLangdagClientForProvider(m.config, modelProvider)
		if err != nil {
			m.messages = append(m.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Error initializing %s provider: %v", modelProvider, err)})
			return m, nil
		}
		m.langdagClient = client
		m.langdagProvider = modelProvider
	}

	// Build system prompt
	workDir := "/workspace"
	systemPrompt := buildSystemPrompt(tools, workDir)

	// Create a fresh agent for each turn (the agent loop is stateless between turns;
	// conversation continuity is handled by parentNodeID via langdag).
	// Model IDs are already native API format (e.g. "claude-sonnet-4-6-20250801").
	agent := NewAgent(m.langdagClient, tools, systemPrompt, modelID)
	m.agent = agent
	m.agentRunning = true
	m.streamingText = ""
	m.needsTextSep = true

	// Start agent loop in background
	parentNodeID := m.agentNodeID
	go agent.Run(context.Background(), userMessage, parentNodeID)

	// Start listening for agent events
	return m, listenForAgentEvent(agent.Events())
}

// handleAgentEvent processes a single agent event and returns the updated model.
func (m model) handleAgentEvent(event AgentEvent) (tea.Model, tea.Cmd) {
	debugLog("event=%d text=%q tool=%s err=%v", event.Type, event.Text, event.ToolName, event.Error)

	switch event.Type {
	case EventTextDelta:
		m.streamingText += event.Text
		// Flush complete lines to messages store
		if idx := strings.LastIndex(m.streamingText, "\n"); idx >= 0 {
			m.messages = append(m.messages, chatMessage{
				kind:      msgAssistant,
				content:   m.streamingText[:idx],
				leadBlank: m.needsTextSep,
			})
			m.needsTextSep = false
			m.streamingText = m.streamingText[idx+1:]
		}
		return m, listenForAgentEvent(m.agent.Events())

	case EventToolCallStart:
		debugLog("tool_call_start: %s input=%s", event.ToolName, string(event.ToolInput))
		// Flush streaming text if any
		if m.streamingText != "" {
			m.messages = append(m.messages, chatMessage{
				kind:      msgAssistant,
				content:   m.streamingText,
				leadBlank: m.needsTextSep,
			})
			m.needsTextSep = false
			m.streamingText = ""
		}
		// Store tool call summary
		m.messages = append(m.messages, chatMessage{kind: msgToolCall, content: toolCallSummary(event.ToolName, event.ToolInput), leadBlank: true})
		return m, listenForAgentEvent(m.agent.Events())

	case EventToolResult:
		debugLog("tool_result: err=%v result=%q", event.IsError, truncateForLog(event.ToolResult, 500))
		result := collapseToolResult(event.ToolResult)
		m.needsTextSep = true
		m.messages = append(m.messages, chatMessage{kind: msgToolResult, content: result, isError: event.IsError})
		return m, listenForAgentEvent(m.agent.Events())

	case EventToolCallDone:
		// Already handled by EventToolResult; just keep listening
		return m, listenForAgentEvent(m.agent.Events())

	case EventApprovalReq:
		debugLog("approval_req: %s", event.ApprovalDesc)
		// Show approval request in View and wait for user y/n
		m.awaitingApproval = true
		m.approvalDesc = event.ApprovalDesc
		// Don't listen for more events yet — wait for user input in Update()
		return m, nil

	case EventDone:
		debugLog("done: nodeID=%s streamingLen=%d", event.NodeID, len(m.streamingText))
		m.agentRunning = false
		if event.NodeID != "" {
			m.agentNodeID = event.NodeID
		}
		// Flush any remaining streaming text
		if m.streamingText != "" {
			m.messages = append(m.messages, chatMessage{
				kind:      msgAssistant,
				content:   m.streamingText,
				leadBlank: m.needsTextSep,
			})
			m.streamingText = ""
		}
		return m, nil

	case EventError:
		errMsg := "Agent error"
		if event.Error != nil {
			errMsg = event.Error.Error()
		}
		debugLog("error: %s", errMsg)
		m.messages = append(m.messages, chatMessage{kind: msgError, content: errMsg})
		return m, listenForAgentEvent(m.agent.Events())
	}

	// Unknown event type — keep listening
	debugLog("unknown event type: %d", event.Type)
	return m, listenForAgentEvent(m.agent.Events())
}

// handleCommand processes slash commands and returns the updated model.
func (m model) handleCommand(input string) (tea.Model, tea.Cmd) {
	cmd := strings.Fields(input)[0] // e.g. "/config"

	switch cmd {
	case "/branches":
		return m.enterBranchMode()
	case "/clear":
		m.agentNodeID = ""
		m.streamingText = ""
		m.pendingToolCall = ""
		m.messages = nil
		m.printedMsgCount = 0
		m.textarea.Reset()
		m.textarea.SetHeight(minInputHeight)
		// Clear screen+scrollback and re-print logo
		return m, tea.Sequence(
			tea.Raw("\033[2J\033[3J\033[H"),
			tea.ClearScreen,
			tea.Println(renderLogo()),
		)
	case "/config":
		return m.enterConfigMode()
	case "/shell":
		return m.enterShellMode()
	case "/model":
		return m.enterModelMode()
	case "/worktrees":
		return m.enterWorktreeMode()
	default:
		m.textarea.Reset()
		m.textarea.SetHeight(minInputHeight)
		m.messages = append(m.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Unknown command: %s", cmd)})
		return m, nil
	}
}

// cleanup stops the container, closes langdag client, and unlocks the worktree.
func (m *model) cleanup() {
	if m.agent != nil {
		m.agent.Cancel()
	}
	if m.container != nil {
		_ = m.container.Stop()
	}
	if m.langdagClient != nil {
		_ = m.langdagClient.Close()
	}
	if m.worktreePath != "" {
		_ = unlockWorktree(m.worktreePath)
	}
}

// enterShellMode opens an interactive TTY shell session in the container.
func (m model) enterShellMode() (tea.Model, tea.Cmd) {
	// Check container state.
	if m.containerErr != nil {
		m.textarea.Reset()
		m.textarea.SetHeight(minInputHeight)
		m.messages = append(m.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Container error: %v", m.containerErr)})
		return m, nil
	}
	if !m.containerReady {
		m.textarea.Reset()
		m.textarea.SetHeight(minInputHeight)
		m.messages = append(m.messages, chatMessage{kind: msgInfo, content: "Container is starting... please try again in a moment."})
		return m, nil
	}

	m.textarea.Reset()
	m.textarea.SetHeight(minInputHeight)

	// Suspend the TUI and hand terminal control to docker exec -it.
	shellCmd := m.container.ShellCmd()
	return m, tea.ExecProcess(shellCmd, func(err error) tea.Msg {
		return shellExitMsg{err: err}
	})
}

func (m *model) recalcTextareaHeight() {
	newHeight := m.displayLineCount()
	if newHeight < minInputHeight {
		newHeight = minInputHeight
	}
	if newHeight > maxInputHeight {
		newHeight = maxInputHeight
	}
	m.textarea.SetHeight(newHeight)
}

func (m model) inputBoxHeight() int {
	return m.textarea.Height() + 2 // top + bottom border
}

func (m model) autocompleteHeight() int {
	if matches := m.autocompleteMatches(); len(matches) > 0 {
		return len(matches)
	}
	return 0
}

func (m model) statusBarHeight() int {
	if m.status.Branch == "" {
		return 0
	}
	return 1
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

func (m model) renderStatusBar() string {
	if m.status.Branch == "" {
		return ""
	}

	infoStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6FE7B8"))

	// Compute fixed-width elements to determine name budgets
	leftFixedW := 3 // "/b " prefix

	var prText string
	if m.status.PRNumber > 0 {
		prText = fmt.Sprintf(" PR #%d", m.status.PRNumber)
		leftFixedW += len([]rune(prText))
	}

	var diffW int
	if m.status.DiffAdd > 0 || m.status.DiffDel > 0 {
		diffW = len(fmt.Sprintf(" +%d/-%d", m.status.DiffAdd, m.status.DiffDel))
		leftFixedW += diffW
	}

	rightFixedW := 3 // "/w " prefix
	var countText string
	if m.status.TotalCount > 1 {
		countText = fmt.Sprintf(" (%d/%d)", m.status.ActiveCount, m.status.TotalCount)
		rightFixedW += len([]rune(countText))
	}

	// Available space for branch + worktree names
	// 3 = 2 side padding + 1 minimum gap
	overhead := leftFixedW + rightFixedW + 3
	available := m.width - overhead
	if available < 4 {
		available = 4
	}

	// Allocate space between branch and worktree names
	branchNeed := len([]rune(m.status.Branch))
	wtNeed := len([]rune(m.status.WorktreeName))

	branchBudget := branchNeed
	wtBudget := wtNeed
	if branchNeed+wtNeed > available {
		half := available / 3
		if half < 2 {
			half = 2
		}
		if branchNeed <= half {
			wtBudget = available - branchNeed
		} else if wtNeed <= half {
			branchBudget = available - wtNeed
		} else {
			branchBudget = available * 3 / 5
			wtBudget = available - branchBudget
		}
	}

	branchName := truncateWithEllipsis(m.status.Branch, branchBudget)
	wtName := truncateWithEllipsis(m.status.WorktreeName, wtBudget)

	// Left side: branch + optional PR
	left := infoStyle.Render("/b " + branchName)
	if m.status.PRNumber > 0 {
		left += infoStyle.Render(prText)
	}

	// Diff stats next to branch info
	if m.status.DiffAdd > 0 || m.status.DiffDel > 0 {
		addStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6FE7B8"))
		delStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF6B6B"))
		left += " " + addStyle.Render(fmt.Sprintf("+%d", m.status.DiffAdd)) +
			delStyle.Render(fmt.Sprintf("/-%d", m.status.DiffDel))
	}

	// Right side: worktree name + active count
	right := infoStyle.Render("/w " + wtName)
	if m.status.TotalCount > 1 {
		right += infoStyle.Render(countText)
	}

	// Fill the space between left and right
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	gap := m.width - leftW - rightW - 2 // 1 padding each side
	if gap < 1 {
		gap = 1
	}

	bar := " " + left + strings.Repeat(" ", gap) + right + " "

	barStyle := lipgloss.NewStyle().Width(m.width)

	return barStyle.Render(bar)
}

// toolCallSummary returns a human-readable one-line summary of a tool invocation.
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

	// Fallback for unknown tools
	return fmt.Sprintf("▶ %s", toolName)
}

// collapseToolResult truncates long tool output for display.
func collapseToolResult(result string) string {
	lines := strings.Split(result, "\n")
	if len(lines) <= 10 {
		return result
	}
	// Show first 4 and last 3 lines with a separator
	head := strings.Join(lines[:4], "\n")
	tail := strings.Join(lines[len(lines)-3:], "\n")
	return fmt.Sprintf("%s\n  ... (%d lines omitted)\n%s", head, len(lines)-7, tail)
}

func (m model) renderAutocomplete() string {
	matches := m.autocompleteMatches()
	if len(matches) == 0 {
		return ""
	}
	highlightStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#B88AFF")).
		Background(lipgloss.Color("#2A1545")).
		Bold(true).
		PaddingLeft(1).
		PaddingRight(1).
		Width(m.width)
	normalStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#E0E0E0")).
		Background(lipgloss.Color("#2A1545")).
		PaddingLeft(1).
		PaddingRight(1).
		Width(m.width)
	idx := m.autocompleteIdx
	if idx >= len(matches) {
		idx = 0
	}
	var lines []string
	for i, cmd := range matches {
		if i == idx {
			lines = append(lines, highlightStyle.Render(cmd))
		} else {
			lines = append(lines, normalStyle.Render(cmd))
		}
	}
	return strings.Join(lines, "\n")
}

func (m model) View() tea.View {
	if !m.ready {
		return tea.NewView("Initializing...")
	}


	if m.mode == modeConfig {
		return m.viewConfig()
	}

	if m.mode == modeModel {
		return m.viewModel()
	}

	if m.mode == modeWorktrees {
		return m.viewWorktrees()
	}

	if m.mode == modeBranches {
		return m.viewBranches()
	}

	// Build input box
	inputBorderColors := borderGradientColors
	inputBorderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderLeft(false).
		BorderRight(false).
		BorderForegroundBlend(inputBorderColors...).
		Width(m.width)

	prefixStyle := lipgloss.NewStyle().Foreground(inputBorderColors[0])
	prefix := prefixStyle.Render("❯ ")
	textareaLines := strings.Split(m.textarea.View(), "\n")
	for i, line := range textareaLines {
		textareaLines[i] = prefix + line
	}
	inputBox := inputBorderStyle.Render(strings.Join(textareaLines, "\n"))

	autocomplete := m.renderAutocomplete()
	acHeight := m.autocompleteHeight()

	// Sticky bottom: ephemeral content + status/autocomplete + input
	var viewParts []string
	linesAbove := 0

	// Ephemeral streaming text / thinking indicator / approval prompt
	if m.awaitingApproval {
		approvalPrompt := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFD700")).
			Bold(true).
			Render(fmt.Sprintf("Allow %s? [y/n]", m.approvalDesc))
		viewParts = append(viewParts, approvalPrompt)
		linesAbove += lipgloss.Height(approvalPrompt)
	} else if streaming := m.renderStreamingText(); streaming != "" {
		viewParts = append(viewParts, streaming)
		linesAbove += lipgloss.Height(streaming)
	} else if m.agentRunning {
		indicator := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#B88AFF")).
			Italic(true).
			Render("thinking...")
		viewParts = append(viewParts, indicator)
		linesAbove += 1
	}

	if acHeight == 0 {
		if statusBar := m.renderStatusBar(); statusBar != "" {
			viewParts = append(viewParts, statusBar)
			linesAbove += 1
		}
	} else {
		viewParts = append(viewParts, autocomplete)
		linesAbove += acHeight
	}

	viewParts = append(viewParts, inputBox)
	linesAbove += 1 // top border of input box

	fullView := strings.Join(viewParts, "\n")

	v := tea.NewView(fullView)

	c := m.textarea.Cursor()
	if c != nil {
		c.Y += linesAbove
		c.X += 2 // +2 for ❯ prefix
		v.Cursor = c
	}

	return v
}

func (m model) viewConfig() tea.View {
	formBorder := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForegroundBlend(borderGradientColors...).
		Width(m.width).
		Padding(1, 0)

	formContent := m.configForm.View()
	rendered := formBorder.Render(formContent)

	// Center vertically
	formHeight := lipgloss.Height(rendered)
	padding := (m.height - formHeight) / 2
	if padding < 0 {
		padding = 0
	}

	v := tea.NewView(strings.Repeat("\n", padding) + rendered)
	v.AltScreen = true
	return v
}

func (m model) viewModel() tea.View {
	formBorder := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForegroundBlend(borderGradientColors...).
		Width(m.width).
		Height(m.height - 2). // constrain to window height (minus border)
		Padding(1, 0)

	formContent := m.modelList.View()
	rendered := formBorder.Render(formContent)

	v := tea.NewView(rendered)
	v.AltScreen = true
	return v
}

func main() {
	// Silence the default logger so library log.Printf calls (e.g. langdag)
	// don't corrupt the bubbletea TUI output.
	log.SetOutput(io.Discard)

	p := tea.NewProgram(initialModel())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
