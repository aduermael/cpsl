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
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
	"langdag.com/langdag"
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
	msgSubAgentGroup // positional anchor for the sub-agent display block
)

type chatMessage struct {
	kind      chatMsgKind
	content   string
	isError   bool          // for tool results
	duration  time.Duration // tool execution duration
	leadBlank bool          // blank line before this message
	toolName  string        // original tool name (for tool call grouping/output rules)
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
	ollamaFetched       bool // true after the initial Ollama model fetch completes (or was skipped)
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
	mainAgentToolCount     int // tool results from main agent only
	sessionToolResults  int            // count of tool results this session
	sessionToolBytes    int            // cumulative tool result bytes this session
	sessionToolStats    map[string][2]int // tool name → [count, bytes]
	lastModelID    string                       // last model used, for detecting changes
	subAgents              map[string]*subAgentDisplay // per-agent display state keyed by AgentID
	subAgentGroupInserted  bool                        // true after a msgSubAgentGroup marker has been added to messages
	suppressedToolIDs map[string]bool              // tool IDs whose UI messages should be hidden
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
	approvalToolID      string        // tool ID of pending approval (for trace)

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
	cfgTabCursor  [3]int // remembered cursor per config tab (API Keys/Global/Project)
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
	cliDebug             bool   // --debug flag
	cliPrompt            string // --prompt flag (non-interactive mode)
	headless             bool   // true when running in --prompt mode (no TUI)

	// JSON trace debug file
	traceCollector *TraceCollector
	traceFilePath  string
	traceUsageSeen bool // true after EventUsage, reset on turn boundary or EventDone
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
		} else if a.agent != nil && a.hasActiveSubAgents() {
			// Main agent stopped but sub-agents are still running —
			// keep draining their events so the display stays live.
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
				} else {
					// Channel closed while sub-agents are tracked as active.
					// Their "done" events were lost — force-complete them so
					// the UI stops showing spinners.
					a.forceCompleteSubAgents()
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


// RunHeadless runs herm in non-interactive mode: submits the --prompt text,
// waits for the agent to finish, and exits. No TUI, no stdin, no resize handling.
func (a *App) RunHeadless() error {
	a.startInit()

	// Wait for essential initialization: config, API client, models, and container.
	timeout := time.After(60 * time.Second)
	for {
		select {
		case result := <-a.resultCh:
			a.handleResult(result)
			if a.configReady && a.langdagClient != nil && a.models != nil &&
				(a.containerReady || a.containerErr != nil) {
				goto ready
			}
		case <-timeout:
			if a.langdagClient == nil {
				fmt.Fprintln(os.Stderr, "error: timed out waiting for initialization")
				return fmt.Errorf("initialization timeout")
			}
			goto ready
		}
	}
ready:

	if a.langdagClient == nil {
		fmt.Fprintln(os.Stderr, "error: no API key configured — use herm /config to add one")
		return fmt.Errorf("no API key configured")
	}

	// Print debug file path to stderr so the calling process can locate it.
	if a.traceFilePath != "" {
		fmt.Fprintf(os.Stderr, "debug: %s\n", a.traceFilePath)
	}

	// Submit the prompt.
	a.messages = append(a.messages, chatMessage{kind: msgUser, content: a.cliPrompt, leadBlank: true})
	a.startAgent(a.cliPrompt)

	if !a.agentRunning {
		// startAgent failed (e.g. no model found).
		for _, msg := range a.messages {
			if msg.kind == msgError {
				fmt.Fprintln(os.Stderr, "error: "+msg.content)
			}
		}
		a.cleanup()
		return fmt.Errorf("agent failed to start")
	}

	// Process agent events and async results until the agent is done
	// and all sub-agents have finished.
	for a.agentRunning || a.hasActiveSubAgents() {
		select {
		case event, ok := <-a.agent.Events():
			if !ok {
				a.agentRunning = false
				break
			}
			a.handleAgentEvent(event)
		case result := <-a.resultCh:
			a.handleResult(result)
			a.drainAgentEvents()
		}
	}

	// Collect assistant text and print to stdout.
	var out strings.Builder
	for _, msg := range a.messages {
		if msg.kind == msgAssistant {
			if out.Len() > 0 {
				out.WriteString("\n")
			}
			out.WriteString(msg.content)
		}
	}
	if out.Len() > 0 {
		fmt.Println(out.String())
	}

	// Print any agent errors to stderr.
	var hasError bool
	for _, msg := range a.messages {
		if msg.kind == msgError {
			fmt.Fprintln(os.Stderr, "error: "+msg.content)
			hasError = true
		}
	}

	a.cleanup()

	if hasError {
		return fmt.Errorf("agent encountered errors")
	}
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

// tryAttachPaths checks if val is one or more file paths (e.g. from
// drag-and-drop in terminals that don't use bracketed paste) and attaches
// them. Returns the modified string with attachment placeholders, or the
// original string unchanged if no paths were detected.
func (a *App) tryAttachPaths(val string) string {
	// Single file path.
	if placeholder, ok := a.tryAttachFile(val); ok {
		return placeholder
	}
	// Multiple newline-separated file paths.
	lines := strings.Split(val, "\n")
	if len(lines) <= 1 {
		return val
	}
	var placeholders []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		p, ok := a.tryAttachFile(line)
		if !ok {
			return val // not all lines are file paths — return unchanged
		}
		placeholders = append(placeholders, p)
	}
	if len(placeholders) > 0 {
		return strings.Join(placeholders, " ")
	}
	return val
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
		var waitDur time.Duration
		if !a.approvalPauseStart.IsZero() {
			waitDur = time.Since(a.approvalPauseStart)
			a.approvalPausedTotal += waitDur
			a.approvalPauseStart = time.Time{}
		}
		if a.traceCollector != nil && a.approvalToolID != "" {
			a.traceCollector.AddApproval(a.approvalToolID, a.approvalDesc, true, waitDur)
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
		var waitDur time.Duration
		if !a.approvalPauseStart.IsZero() {
			waitDur = time.Since(a.approvalPauseStart)
			a.approvalPausedTotal += waitDur
			a.approvalPauseStart = time.Time{}
		}
		if a.traceCollector != nil && a.approvalToolID != "" {
			a.traceCollector.AddApproval(a.approvalToolID, a.approvalDesc, false, waitDur)
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

	// Try to attach file paths that weren't caught by bracketed paste.
	// Some terminals (e.g. Zed) send drag-and-drop paths as regular input
	// instead of wrapping them in paste markers.
	val = a.tryAttachPaths(val)

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
			// Fetch Ollama models asynchronously if configured. catalogMsg can
			// arrive twice (once from local cache, once from network refresh),
			// so ollamaFetched ensures we only start one probe per session.
			if a.config.OllamaBaseURL != "" && !a.ollamaFetched {
				go func() { a.resultCh <- fetchOllamaModelsCmd(a.config.OllamaBaseURL) }()
			}
		}

	case ollamaModelsMsg:
		a.ollamaFetched = true
		if len(msg.models) > 0 {
			base := modelsFromCatalog(a.modelCatalog)
			a.models = append(base, msg.models...)
			if a.sweLoaded && a.sweScores != nil {
				matchSWEScores(a.models, a.sweScores)
			}
		}
		alreadyShown := a.shownInitialModel
		a.maybeShowInitialModels()
		// Once Ollama responds, show the offline warning if the model line
		// was already displayed. If maybeShowInitialModels runs here for the
		// first time, showModelChange handles the warning directly.
		if alreadyShown {
			activeID := a.config.resolveActiveModel(a.models)
			if a.config.OllamaBaseURL != "" && a.isOllamaOffline(activeID) {
				msg := fmt.Sprintf("\033[33m⚠\033[34;3m Ollama unreachable at \033[36m%s\033[34;3m — run '\033[32;3mollama serve\033[34;3m' to continue", a.config.OllamaBaseURL)
				providers := a.config.configuredProviders()
				delete(providers, ProviderOllama)
				if len(providers) > 0 {
					msg = fmt.Sprintf("\033[33m⚠\033[34;3m Ollama unreachable at \033[36m%s\033[34;3m — run '\033[32;3mollama serve\033[34;3m' or switch to another provider (/config)", a.config.OllamaBaseURL)
				}
				a.messages = append(a.messages, chatMessage{kind: msgInfo, content: msg})
			}
		}

	case openPickerMsg:
		if a.cfgActive {
			a.doOpenConfigModelPicker(a.models, msg.getCurrentID, msg.onSelect)
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
		a.status.Branch = msg.branch
		a.status.HasUpstream = msg.hasUpstream
		a.status.Behind = msg.behind
		a.status.Ahead = msg.ahead
		a.status.DiffAdd = msg.diffAdd
		a.status.DiffDel = msg.diffDel

	case projectSnapshotMsg:
		a.projectSnap = &msg.snapshot

	case workspaceMsg:
		a.worktreePath = msg.worktreePath
		a.repoRoot = msg.repoRoot
		if a.repoRoot == "" {
			a.repoRoot = msg.worktreePath
		}
		a.projectConfig = loadProjectConfig(a.repoRoot)
		a.config = mergeConfigs(a.globalConfig, a.projectConfig)
		a.configReady = true
		a.initAppDebugLog()
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
	if a.traceCollector != nil {
		a.traceCollector.Finalize()
		if err := a.traceCollector.FlushToFile(a.traceFilePath); err != nil {
			fmt.Fprintf(os.Stderr, "debug: failed to write trace: %v\n", err)
		}
		a.traceCollector = nil
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

	for i, arg := range os.Args[1:] {
		switch arg {
		case "--debug":
			app.cliDebug = true
		case "--prompt":
			if i+1 < len(os.Args[1:]) {
				app.cliPrompt = os.Args[i+2] // i is 0-based in the slice, +2 to get next arg in os.Args
			}
		}
	}

	if app.cliPrompt != "" {
		app.headless = true
		if err := app.RunHeadless(); err != nil {
			os.Exit(1)
		}
		return
	}

	if err := app.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
