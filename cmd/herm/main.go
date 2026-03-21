// main.go implements the herm terminal UI, rendering, input handling, and
// program entry point.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
	"langdag.com/langdag"
	"langdag.com/langdag/types"
)

var Version = "dev"

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
	isError   bool          // for tool results
	duration  time.Duration // tool execution duration
	leadBlank bool          // blank line before this message
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
	scrollShift int // rows scrolled off top when content > terminal height

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
	globalConfig     Config        // loaded from ~/.herm/config.json
	projectConfig    ProjectConfig // loaded from <repo>/.herm/config.json
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
	projectSnap      *projectSnapshot
	modelCatalog     *langdag.ModelCatalog
	langdagClient    *langdag.Client
	langdagProvider  string
	agent            *Agent
	agentNodeID      string
	agentRunning     bool
	awaitingApproval bool
	approvalDesc     string
	approvalSummary  string
	autocompleteIdx  int
	streamingText    string
	pendingToolCall  string
	needsTextSep     bool
	sessionCostUSD         float64
	lastInputTokens        int // input tokens from most recent API call (context usage)
	sessionInputTokens     int // cumulative input tokens this session (all agents)
	sessionOutputTokens    int // cumulative output tokens this session (all agents)
	sessionCacheRead       int // cumulative cache read tokens this session
	sessionLLMCalls        int // number of LLM API calls this session (all agents)
	mainAgentInputTokens   int // input tokens from main agent only
	mainAgentOutputTokens  int // output tokens from main agent only
	mainAgentLLMCalls      int // LLM calls from main agent only
	sessionToolResults  int            // count of tool results this session
	sessionToolBytes    int            // cumulative tool result bytes this session
	sessionToolStats    map[string][2]int // tool name → [count, bytes]
	lastModelID    string                       // last model used, for detecting changes
	subAgents      map[string]*subAgentDisplay // per-agent display state keyed by AgentID
	containerImage string                       // runtime container image name (not persisted)
	updateAvailable string   // version tag if update is available

	// Tool timer (live elapsed display)
	toolStartTime time.Time
	toolTimer     *time.Ticker

	// Agent status timer (animated label while agent is running)
	agentStartTime     time.Time
	agentTicker        *time.Ticker
	agentElapsed       time.Duration // persists final time after agent stops
	agentTextIndex     int           // which funny text is showing
	agentDisplayInTok  float64       // lerped display value for input tokens
	agentDisplayOutTok float64       // lerped display value for output tokens

	// Approval timer pause
	approvalPauseStart  time.Time     // when approval wait started
	approvalPausedTotal time.Duration // total time spent waiting for approvals

	// Periodic commit info refresh
	commitInfoTicker *time.Ticker

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

	// ESC double-tap to stop agent
	escTime time.Time
	escHint bool

	// Force-quit: tracks whether Cancel() was already issued so a
	// subsequent double-tap CTRL-C or ESC forces an immediate exit.
	cancelSent bool

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

// agentElapsedTime returns elapsed agent time, excluding approval wait time.
func (a *App) agentElapsedTime() time.Duration {
	elapsed := time.Since(a.agentStartTime)
	elapsed -= a.approvalPausedTotal
	if a.awaitingApproval && !a.approvalPauseStart.IsZero() {
		elapsed -= time.Since(a.approvalPauseStart)
	}
	if elapsed < 0 {
		elapsed = 0
	}
	return elapsed
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

	// Enable bracketed paste and modifyOtherKeys (no alt screen — use main buffer
	// so native terminal scrollback works)
	fmt.Print("\033[?2004h")
	fmt.Print("\033[>4;2m")
	defer func() {
		fmt.Print("\033[?25h")  // ensure cursor visible on exit
		fmt.Print("\033[>4;0m") // disable modifyOtherKeys
		fmt.Print("\033[?2004l")
		// Position cursor below rendered content so shell prompt appears cleanly
		th := getTerminalHeight()
		lastVisRow := a.prevRowCount
		if lastVisRow > th {
			lastVisRow = th
		}
		if lastVisRow > 0 {
			fmt.Printf("\033[%d;1H", lastVisRow)
		}
		fmt.Print("\r\n")
		end := time.Now()
		fmt.Printf("[HERM %s -> %s]\r\n",
			startTime.Format("Jan 02 15:04"),
			end.Format("Jan 02 15:04"))
		term.Restore(fd, oldState)
	}()

	a.width = getWidth()

	// SIGWINCH handler with debounce
	sigWinch := make(chan os.Signal, 1)
	signal.Notify(sigWinch, syscall.SIGWINCH)
	resizeDb := newDebouncer(150*time.Millisecond, func() {
		select {
		case a.resultCh <- resizeMsg{}:
		default:
		}
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
	return filepath.Join(a.worktreePath, ".herm", "attachments", a.sessionID)
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
// under .herm/tmp/ and returns the file path.
func (a *App) clipboardSaveImage() (string, error) {
	tmpDir := filepath.Join(a.worktreePath, ".herm", "tmp")
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

// cleanupTmpDir removes files in .herm/tmp/ older than 24 hours.
func cleanupTmpDir(worktreePath string) {
	tmpDir := filepath.Join(worktreePath, ".herm", "tmp")
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
		if !a.approvalPauseStart.IsZero() {
			a.approvalPausedTotal += time.Since(a.approvalPauseStart)
			a.approvalPauseStart = time.Time{}
		}
		// Restart tool timer ticker (frozen during approval).
		if !a.toolStartTime.IsZero() && a.toolTimer == nil {
			a.toolTimer = time.NewTicker(100 * time.Millisecond)
			go func(ticker *time.Ticker, ch chan any) {
				for range ticker.C {
					select {
					case ch <- toolTimerTickMsg{}:
					default:
					}
				}
			}(a.toolTimer, a.resultCh)
		}
		if a.agent != nil {
			a.agent.Approve(ApprovalResponse{Approved: true})
		}
		a.messages = append(a.messages, chatMessage{kind: msgSuccess, content: "Approved"})
		a.render()
	case 'n', 'N':
		a.awaitingApproval = false
		if !a.approvalPauseStart.IsZero() {
			a.approvalPausedTotal += time.Since(a.approvalPauseStart)
			a.approvalPauseStart = time.Time{}
		}
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

	a.agentElapsed = 0

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
		a.sessionInputTokens = 0
		a.sessionOutputTokens = 0
		a.sessionCacheRead = 0
		a.sessionCostUSD = 0
		a.sessionLLMCalls = 0
		a.mainAgentInputTokens = 0
		a.mainAgentOutputTokens = 0
		a.mainAgentLLMCalls = 0
		a.sessionToolResults = 0
		a.sessionToolBytes = 0
		a.sessionToolStats = nil
		a.lastInputTokens = 0
		a.agentElapsed = 0
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

	case "/update":
		a.handleUpdateCommand()

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
	b.WriteString(fmt.Sprintf("  LLM calls:     %d (main: %d, sub-agents: %d)\n",
		a.sessionLLMCalls, a.mainAgentLLMCalls, a.sessionLLMCalls-a.mainAgentLLMCalls))
	b.WriteString(fmt.Sprintf("  Input tokens:  %s (main: %s)\n",
		formatTokenCount(a.sessionInputTokens), formatTokenCount(a.mainAgentInputTokens)))
	b.WriteString(fmt.Sprintf("  Output tokens: %s (main: %s)\n",
		formatTokenCount(a.sessionOutputTokens), formatTokenCount(a.mainAgentOutputTokens)))
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
		branch := "herm-" + name
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
			attachDir := filepath.Join(wtPath, ".herm", "attachments", a.sessionID)
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
	return strings.HasPrefix(a.worktreePath, filepath.Join(home, ".herm", "worktrees"))
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

	// Clear screen and restore terminal before handing off to shell
	fmt.Print("\033[H\033[2J\033[3J") // clear screen + scrollback
	fmt.Print("\033[?25h")            // show cursor
	fmt.Print("\033[>4;0m")           // disable modifyOtherKeys
	fmt.Print("\033[?2004l")          // disable bracketed paste
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

	// Re-enable bracketed paste, modifyOtherKeys
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

	a.renderFull()
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
	a.messages = append(a.messages, chatMessage{kind: msgInfo, content: "v" + Version + " (container: " + hermImageTag + ")"})
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
		tools = append(tools, NewOutlineTool(a.container))
		tools = append(tools, NewEditFileTool(a.container))
		tools = append(tools, NewWriteFileTool(a.container))
		if a.worktreePath != "" {
			hermDir := filepath.Join(a.worktreePath, ".herm")
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
			tools = append(tools, NewDevEnvTool(a.container, hermDir, a.worktreePath, mounts, projectID, onRebuild, onStatus))
		}
	}
	if a.worktreePath != "" {
		tools = append(tools, NewGitTool(a.worktreePath, a.config.effectiveGitCoAuthor()))
	}

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

	// Server-side tools (e.g. web search) are handled by the LLM provider.
	// Some models don't support them, so we check before including them.
	var serverTools []types.ToolDefinition
	if supportsServerTools(modelProvider, modelID) {
		serverTools = []types.ToolDefinition{WebSearchToolDef()}
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

	// Load project-local skills from .herm/skills/
	var skills []Skill
	if a.worktreePath != "" {
		skills, _ = loadSkills(filepath.Join(a.worktreePath, ".herm", "skills"))
	}

	workDir := "/workspace"

	containerImage := a.containerImage
	if containerImage == "" {
		containerImage = defaultContainerImage
	}

	// Sub-agent tool: output-only communication, no shared memory.
	// Uses exploration model if configured, otherwise falls back to active model.
	maxTurns := a.config.SubAgentMaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultSubAgentMaxTurns
	}
	maxDepth := a.config.MaxAgentDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxAgentDepth
	}
	explorationModelID := a.config.resolveExplorationModel(a.models)
	subAgentServerTools := serverTools
	if !supportsServerTools(modelProvider, explorationModelID) {
		subAgentServerTools = nil
	}
	subAgentTool := NewSubAgentTool(a.langdagClient, tools, subAgentServerTools, modelID, explorationModelID, maxTurns, maxDepth, 0, workDir, a.config.Personality, containerImage)
	tools = append(tools, subAgentTool)

	var wtBranch string
	if a.worktreePath != "" {
		wtBranch = worktreeBranch(a.worktreePath)
	}
	systemPrompt := buildSystemPrompt(tools, serverTools, skills, workDir, a.config.Personality, containerImage, wtBranch, a.projectSnap)

	if a.displaySystemPrompts {
		a.messages = append(a.messages, chatMessage{kind: msgSystemPrompt, content: "── System Prompt ──\n" + systemPrompt})
	}

	a.showModelChange(modelID)

	ctxWindow := 0
	if m := findModelByID(a.models, modelID); m != nil {
		ctxWindow = m.ContextWindow
	}
	mainMaxIter := a.config.MaxToolIterations
	if mainMaxIter <= 0 {
		mainMaxIter = defaultMaxToolIterations
	}
	agent := NewAgent(a.langdagClient, tools, serverTools, systemPrompt, modelID, ctxWindow,
		WithExplorationModel(explorationModelID),
		WithMaxToolIterations(mainMaxIter))
	subAgentTool.parentEvents = agent.events
	a.agent = agent
	a.agentRunning = true
	a.streamingText = ""
	a.needsTextSep = true
	a.agentStartTime = time.Now()
	a.agentElapsed = 0
	a.approvalPausedTotal = 0
	a.agentTextIndex = 0
	a.agentDisplayInTok = float64(a.mainAgentInputTokens)
	a.agentDisplayOutTok = float64(a.mainAgentOutputTokens)
	if a.agentTicker != nil {
		a.agentTicker.Stop()
	}
	a.agentTicker = time.NewTicker(50 * time.Millisecond)
	go func(ticker *time.Ticker, ch chan any) {
		for range ticker.C {
			select {
			case ch <- agentTickMsg{}:
			default:
			}
		}
	}(a.agentTicker, a.resultCh)

	parentNodeID := a.agentNodeID
	go agent.Run(context.Background(), userMessage, parentNodeID)
}

func (a *App) drainAgentEvents() {
	if a.agent == nil || !a.agentRunning {
		return
	}
	// Cap drain iterations to avoid starving stdin processing.
	// The select in the main loop will pick up remaining events next iteration.
	const maxDrain = 50
	for i := 0; i < maxDrain; i++ {
		select {
		case event, ok := <-a.agent.Events():
			if !ok {
				a.agentRunning = false
				a.cancelSent = false
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
		a.toolStartTime = time.Now()
		if a.toolTimer != nil {
			a.toolTimer.Stop()
		}
		a.toolTimer = time.NewTicker(100 * time.Millisecond)
		go func(ticker *time.Ticker, ch chan any) {
			for range ticker.C {
				select {
				case ch <- toolTimerTickMsg{}:
				default:
					// Don't block if resultCh is full — skip this tick.
				}
			}
		}(a.toolTimer, a.resultCh)
		a.render()

	case EventToolResult:
		debugLog("tool_result: err=%v result=%q", event.IsError, truncateForLog(event.ToolResult, 500))
		if a.toolTimer != nil {
			a.toolTimer.Stop()
			a.toolTimer = nil
		}
		a.toolStartTime = time.Time{}
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
		a.messages = append(a.messages, chatMessage{kind: msgToolResult, content: result, isError: event.IsError, duration: event.Duration})
		a.render()

	case EventUsage:
		if event.Usage != nil {
			a.sessionCostUSD += computeCost(a.models, event.Model, *event.Usage)
			a.lastInputTokens = event.Usage.InputTokens + event.Usage.CacheReadInputTokens + event.Usage.CacheCreationInputTokens
			a.sessionInputTokens += event.Usage.InputTokens
			a.sessionOutputTokens += event.Usage.OutputTokens
			a.sessionCacheRead += event.Usage.CacheReadInputTokens
			a.sessionLLMCalls++
			// Track main-agent tokens separately (sub-agent events have a different AgentID).
			if a.agent != nil && event.AgentID == a.agent.ID() {
				a.mainAgentInputTokens += event.Usage.InputTokens
				a.mainAgentOutputTokens += event.Usage.OutputTokens
				a.mainAgentLLMCalls++
			}
			a.renderInput()
		}

	case EventToolCallDone:
		// Already handled by EventToolResult

	case EventApprovalReq:
		debugLog("approval_req: %s", event.ApprovalDesc)
		a.awaitingApproval = true
		a.approvalPauseStart = time.Now()
		a.approvalSummary = approvalShortDesc(event.ToolName, event.ToolInput)
		a.approvalDesc = event.ApprovalDesc
		// Stop tool timer ticker so the tool box timer freezes during approval.
		if a.toolTimer != nil {
			a.toolTimer.Stop()
			a.toolTimer = nil
		}
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
		a.cancelSent = false
		if a.agentTicker != nil {
			a.agentTicker.Stop()
			a.agentTicker = nil
		}
		a.agentElapsed = a.agentElapsedTime()
		a.agentDisplayInTok = float64(a.mainAgentInputTokens)
		a.agentDisplayOutTok = float64(a.mainAgentOutputTokens)
		if event.NodeID != "" {
			a.agentNodeID = event.NodeID
		}
		// Clean up completed sub-agent display entries.
		for id, sa := range a.subAgents {
			if sa.done {
				delete(a.subAgents, id)
			}
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

	case EventSubAgentStart:
		sa := a.getOrCreateSubAgent(event.AgentID)
		sa.task = truncateTaskLabel(event.Task)
		a.render()

	case EventSubAgentDelta:
		// Update the agent's status with a snippet of the streaming text.
		sa := a.getOrCreateSubAgent(event.AgentID)
		snippet := strings.TrimSpace(event.Text)
		if snippet != "" {
			// Show last meaningful text fragment as status.
			if len(snippet) > 60 {
				snippet = snippet[:60] + "…"
			}
			sa.status = snippet
		}
		a.render()

	case EventSubAgentStatus:
		sa := a.getOrCreateSubAgent(event.AgentID)
		if event.Text == "done" {
			sa.done = true
			completionMsg := fmt.Sprintf("[agent %s] completed: %s", shortID(event.AgentID), sa.task)
			if event.Usage != nil && (event.Usage.InputTokens > 0 || event.Usage.OutputTokens > 0) {
				completionMsg += fmt.Sprintf(" (↑%s ↓%s",
					formatTokenCount(event.Usage.InputTokens),
					formatTokenCount(event.Usage.OutputTokens))
				if event.Task != "" {
					completionMsg += ", " + event.Task
				}
				completionMsg += ")"
			}
			a.messages = append(a.messages, chatMessage{
				kind:    msgInfo,
				content: completionMsg,
			})
		} else {
			sa.status = event.Text
		}
		a.render()

	case EventRetry:
		errMsg := "unknown error"
		if event.Error != nil {
			errMsg = event.Error.Error()
		}
		retryMsg := fmt.Sprintf("API error, retrying in %s (attempt %d/%d): %s",
			event.Duration.Truncate(time.Second), event.Attempt, event.MaxRetry, errMsg)
		debugLog("retry: %s", retryMsg)
		a.messages = append(a.messages, chatMessage{kind: msgInfo, content: retryMsg})
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
	go func() { a.resultCh <- checkForUpdate(Version) }()
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
	case toolTimerTickMsg:
		a.render()
		return
	case agentTickMsg:
		if a.agentRunning {
			elapsed := a.agentElapsedTime()
			a.agentTextIndex = int(elapsed.Seconds()/4) % len(funnyTexts)
			// Lerp displayed tokens toward main-agent totals.
			a.agentDisplayInTok += (float64(a.mainAgentInputTokens) - a.agentDisplayInTok) * 0.15
			a.agentDisplayOutTok += (float64(a.mainAgentOutputTokens) - a.agentDisplayOutTok) * 0.15
		}
		if a.awaitingApproval {
			a.renderInput() // Only redraw input area; leave block rows (tool timer) frozen.
		} else {
			a.render()
		}
		return

	case ctrlCExpiredMsg:
		_ = msg
		if a.ctrlCHint {
			a.ctrlCHint = false
			a.ctrlCTime = time.Time{}
			a.renderInput()
		}
		return

	case escExpiredMsg:
		_ = msg
		if a.escHint {
			a.escHint = false
			a.escTime = time.Time{}
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

	case commitInfoMsg:
		a.status.HasUpstream = msg.hasUpstream
		a.status.Behind = msg.behind
		a.status.Ahead = msg.ahead
		a.status.DiffAdd = msg.diffAdd
		a.status.DiffDel = msg.diffDel

	case projectSnapshotMsg:
		a.projectSnap = &msg.snapshot

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
		go func() { a.resultCh <- fetchProjectSnapshot(wtPath) }()
		go func() { bootContainerCmd(wtPath, a.sessionID, a.resultCh) }()
		go cleanupTmpDir(wtPath)
		go cleanupAgentOutputDir(wtPath)
		// Start periodic commit info refresh (only if git is available)
		if _, err := exec.LookPath("git"); err == nil {
			a.commitInfoTicker = time.NewTicker(15 * time.Second)
			go func(ticker *time.Ticker, ch chan any, path string) {
				for range ticker.C {
					ch <- fetchCommitInfo(path)
				}
			}(a.commitInfoTicker, a.resultCh, wtPath)
		}

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

	case updateAvailableMsg:
		if msg.err == nil && msg.version != "" {
			a.updateAvailable = msg.version
			current := Version
			if current == "dev" {
				current = "dev"
			}
			a.messages = append(a.messages, chatMessage{
				kind:    msgInfo,
				content: fmt.Sprintf("Update available: v%s (current: %s). Run /update to upgrade.", msg.version, current),
			})
		}

	case updateCompleteMsg:
		if msg.err != nil {
			a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Update failed: %v", msg.err)})
		} else {
			ver := a.updateAvailable
			a.updateAvailable = ""
			a.messages = append(a.messages, chatMessage{kind: msgSuccess, content: fmt.Sprintf("Updated to v%s. Restart herm to use the new version.", ver)})
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
	if a.commitInfoTicker != nil {
		a.commitInfoTicker.Stop()
		a.commitInfoTicker = nil
	}
	if a.toolTimer != nil {
		a.toolTimer.Stop()
		a.toolTimer = nil
	}
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

// ─── main ───

// handleUpdateCommand handles the /update slash command.
func (a *App) handleUpdateCommand() {
	if Version == "dev" {
		a.messages = append(a.messages, chatMessage{kind: msgInfo, content: "Update check is not available for development builds."})
		a.render()
		return
	}
	if a.updateAvailable == "" {
		a.messages = append(a.messages, chatMessage{kind: msgInfo, content: fmt.Sprintf("Already up to date (v%s).", strings.TrimPrefix(Version, "v"))})
		a.render()
		return
	}
	ver := a.updateAvailable
	a.messages = append(a.messages, chatMessage{kind: msgInfo, content: fmt.Sprintf("Downloading v%s...", ver)})
	a.render()
	go func() { a.resultCh <- performUpdate(ver) }()
}

func main() {
	log.SetOutput(io.Discard)

	for _, arg := range os.Args[1:] {
		if arg == "--version" || arg == "-v" {
			fmt.Println("herm " + Version + " (container: " + hermImageTag + ")")
			os.Exit(0)
		}
	}

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
