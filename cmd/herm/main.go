// main.go implements the herm terminal UI, rendering, input handling, and
// program entry point.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
	"langdag.com/langdag"
	"langdag.com/langdag/types"
	"github.com/rivo/uniseg"
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

// ─── Commands and autocomplete ───

var commands = []string{"/branches", "/clear", "/compact", "/config", "/model", "/session", "/shell", "/update", "/usage", "/worktrees"}
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

// truncateVisual truncates s to at most maxCols visible terminal columns,
// appending "…" if truncation occurs. It is ANSI-escape-aware: escape sequences
// do not count toward visible width.
func truncateVisual(s string, maxCols int) string {
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

// ─── Async message types ───

type sweScoresMsg struct {
	scores map[string]float64
	err    error
}

type ctrlCExpiredMsg struct{}
type escExpiredMsg struct{}

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
	Behind       int // commits on remote tracking branch missing locally
	Ahead        int // local commits missing on remote tracking branch
	HasUpstream  bool
}

type statusInfoMsg struct {
	info statusInfo
}

type commitInfoMsg struct {
	behind      int
	ahead       int
	hasUpstream bool
	diffAdd     int
	diffDel     int
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

type toolTimerTickMsg struct{}

type agentTickMsg struct{}

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

func truncateForLog(s string, max int) string {
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

	// Build from .herm/Dockerfile (write base template if none exists).
	imageName := buildContainerImage(workspace, ch)
	if imageName != "" {
		client.mu.Lock()
		client.config.Image = imageName
		client.mu.Unlock()
	}

	// Ensure the image is available locally (pull if needed).
	finalImage := client.config.Image
	if err := ensureImageLocal(finalImage, ch); err != nil {
		ch <- containerStatusMsg{text: "image pull failed"}
		ch <- containerErrMsg{err: fmt.Errorf("pulling %s: %w", finalImage, err)}
		return
	}

	ch <- containerStatusMsg{text: "starting…"}

	attachDir := filepath.Join(workspace, ".herm", "attachments", sessionID)
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

// ensureImageLocal checks whether a Docker image exists locally. If not, it
// pulls it from the registry. Status updates are sent via ch. Returns nil on
// success, or the pull error if the image cannot be obtained.
func ensureImageLocal(image string, ch chan<- any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if dockerCommand(ctx, "docker", "image", "inspect", image).Run() == nil {
		return nil // already available
	}

	ch <- containerStatusMsg{text: "pulling image…"}

	pullCtx, pullCancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer pullCancel()

	pullCmd := dockerCommand(pullCtx, "docker", "pull", image)
	var stderr bytes.Buffer
	pullCmd.Stderr = &stderr

	if err := pullCmd.Run(); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

// buildContainerImage builds a Docker image from .herm/Dockerfile in the workspace.
// If no Dockerfile exists, or it matches the embedded base template, the build is
// skipped and the caller uses the default image (aduermael/herm:<tag>) directly.
// Image tag is deterministic: herm-<projectID[:8]>:<sha256[:12]> based on Dockerfile content.
// If the image already exists (docker image inspect), the build is skipped.
// Returns the built image name, or empty string on failure (caller falls back to raw image).
func buildContainerImage(workspace string, ch chan<- any) string {
	hermDir := filepath.Join(workspace, ".herm")
	dockerfilePath := filepath.Join(hermDir, "Dockerfile")

	// No .herm/Dockerfile — use the default image directly. No build needed.
	if _, err := os.Stat(dockerfilePath); os.IsNotExist(err) {
		return ""
	}

	// Read Dockerfile content.
	content, err := os.ReadFile(dockerfilePath)
	if err != nil {
		return ""
	}

	// If the Dockerfile matches the embedded base template, skip the build.
	// The default image already has everything.
	if string(content) == BaseDockerfile {
		return ""
	}

	// If the Dockerfile uses an outdated or wrong base image, back it up
	// so devenv can later help the user migrate their customizations.
	// This is expected when the herm base image is updated.
	if !dockerfileUsesHermBase(string(content)) {
		backupPath := filepath.Join(hermDir, "Dockerfile.old")
		_ = os.Rename(dockerfilePath, backupPath)
		ch <- containerStatusMsg{text: "migrating to new base image…"}
		return ""
	}

	// Resolve __HERM_VERSION__ placeholder to actual version.
	resolved := resolveDockerfile(string(content))

	// Hash the resolved content so version bumps trigger rebuilds.
	hash := sha256.Sum256([]byte(resolved))
	hashStr := hex.EncodeToString(hash[:])[:12]

	// Derive image name from project ID + content hash.
	imageName := "herm-local:" + hashStr
	if repoRoot := gitRepoRoot(); repoRoot != "" {
		if projectID, err := ensureProjectID(repoRoot); err == nil && len(projectID) >= 8 {
			imageName = "herm-" + projectID[:8] + ":" + hashStr
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

	// Write resolved Dockerfile to a temp file for docker build.
	tmpFile, tmpErr := os.CreateTemp("", "herm-dockerfile-*")
	if tmpErr != nil {
		return ""
	}
	defer os.Remove(tmpFile.Name())
	if _, writeErr := tmpFile.WriteString(resolved); writeErr != nil {
		tmpFile.Close()
		return ""
	}
	tmpFile.Close()

	buildCtx, buildCancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer buildCancel()

	buildCmd := dockerCommand(buildCtx, "docker", "build",
		"-t", imageName,
		"-f", tmpFile.Name(),
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

// dockerfileUsesHermBase checks if a Dockerfile's first FROM instruction uses
// aduermael/herm:__HERM_VERSION__ as the base image. Dockerfiles with hardcoded
// version tags (e.g. aduermael/herm:0.1) are rejected so the migration flow
// backs them up and the user can re-create with the placeholder.
func dockerfileUsesHermBase(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, "FROM ") {
			return strings.Contains(line, "aduermael/herm:__HERM_VERSION__")
		}
		// First non-comment, non-empty line isn't FROM — invalid but not our problem.
		break
	}
	return false
}

func fetchStatusCmd(worktreePath string) statusInfoMsg {
	var info statusInfo

	info.Branch = worktreeBranch(worktreePath)

	// ahead/behind relative to upstream tracking branch
	revListCmd := exec.Command("git", "rev-list", "--count", "--left-right", "@{upstream}...HEAD")
	revListCmd.Dir = worktreePath
	if out, err := revListCmd.Output(); err == nil {
		parts := strings.Split(strings.TrimSpace(string(out)), "\t")
		if len(parts) == 2 {
			info.HasUpstream = true
			if n, err := strconv.Atoi(parts[0]); err == nil {
				info.Behind = n
			}
			if n, err := strconv.Atoi(parts[1]); err == nil {
				info.Ahead = n
			}
		}
	}

	ghCmd := exec.Command("gh", "pr", "view", "--json", "number", "-q", ".number")
	ghCmd.Dir = worktreePath
	if out, err := ghCmd.Output(); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil {
			info.PRNumber = n
		}
	}

	diffCmd := exec.Command("git", "diff", "--shortstat", "HEAD")
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

func fetchCommitInfo(worktreePath string) commitInfoMsg {
	var msg commitInfoMsg
	cmd := exec.Command("git", "rev-list", "--count", "--left-right", "@{upstream}...HEAD")
	cmd.Dir = worktreePath
	if out, err := cmd.Output(); err == nil {
		parts := strings.Split(strings.TrimSpace(string(out)), "	")
		if len(parts) == 2 {
			msg.hasUpstream = true
			if n, err := strconv.Atoi(parts[0]); err == nil {
				msg.behind = n
			}
			if n, err := strconv.Atoi(parts[1]); err == nil {
				msg.ahead = n
			}
		}
	}

	diffCmd := exec.Command("git", "diff", "--shortstat", "HEAD")
	diffCmd.Dir = worktreePath
	if out, err := diffCmd.Output(); err == nil {
		line := strings.TrimSpace(string(out))
		if re := regexp.MustCompile(`(\d+) insertion`); re.MatchString(line) {
			if n, err := strconv.Atoi(re.FindStringSubmatch(line)[1]); err == nil {
				msg.diffAdd = n
			}
		}
		if re := regexp.MustCompile(`(\d+) deletion`); re.MatchString(line) {
			if n, err := strconv.Atoi(re.FindStringSubmatch(line)[1]); err == nil {
				msg.diffDel = n
			}
		}
	}
	return msg
}

// projectSnapshot holds a lightweight project context gathered at startup.
type projectSnapshot struct {
	TopLevel      string // ls -1 of worktree root
	RecentCommits string // git log --oneline -20
	GitStatus     string // git status --short
}

type projectSnapshotMsg struct {
	snapshot projectSnapshot
}

type updateAvailableMsg struct {
	version string
	err     error
}

type updateCompleteMsg struct {
	err error
}

// fetchProjectSnapshot gathers a lightweight project snapshot for the system prompt.
// Each sub-command has a 2s timeout and fails gracefully to empty string.
func fetchProjectSnapshot(worktreePath string) projectSnapshotMsg {
	var snap projectSnapshot

	type result struct {
		field string
		value string
	}

	ch := make(chan result, 3)

	// ls -1 of root (with tree fallback for sparse dirs).
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "ls", "-1", worktreePath)
		out, err := cmd.Output()
		val := ""
		if err == nil {
			val = strings.TrimSpace(string(out))
			// If sparse (<= 8 entries), try tree for richer structure.
			if strings.Count(val, "\n") < 8 {
				treeCtx, treeCancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer treeCancel()
				treeCmd := exec.CommandContext(treeCtx, "tree", "-L", "2", "--noreport", worktreePath)
				if treeOut, treeErr := treeCmd.Output(); treeErr == nil {
					lines := strings.Split(strings.TrimSpace(string(treeOut)), "\n")
					if len(lines) <= 50 {
						val = strings.TrimSpace(string(treeOut))
					}
				}
			}
		}
		ch <- result{"ls", val}
	}()

	// git log --oneline -20
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "log", "--oneline", "-20")
		cmd.Dir = worktreePath
		out, err := cmd.Output()
		val := ""
		if err == nil {
			val = strings.TrimSpace(string(out))
		}
		ch <- result{"log", val}
	}()

	// git status --short
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "status", "--short")
		cmd.Dir = worktreePath
		out, err := cmd.Output()
		val := ""
		if err == nil {
			val = strings.TrimSpace(string(out))
		}
		ch <- result{"status", val}
	}()

	for i := 0; i < 3; i++ {
		r := <-ch
		switch r.field {
		case "ls":
			snap.TopLevel = r.value
		case "log":
			snap.RecentCommits = r.value
		case "status":
			snap.GitStatus = r.value
		}
	}

	return projectSnapshotMsg{snapshot: snap}
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

	// Ctrl+C: double-tap to stop agent (when running) or exit (when idle)
	if ch == 3 {
		if a.agentRunning {
			// When agent is running, double-tap Ctrl+C stops the agent.
			// If Cancel was already sent and agent is still running,
			// force-quit immediately (safety net for stuck agents).
			if a.ctrlCHint && time.Since(a.ctrlCTime) < 2*time.Second {
				if a.cancelSent {
					return true // force quit — Cancel() didn't work
				}
				a.agent.Cancel()
				a.cancelSent = true
				a.ctrlCHint = false
				a.ctrlCTime = time.Time{}
				return false
			}
		} else {
			// Second Ctrl+C within 2 seconds: quit
			if a.ctrlCHint && time.Since(a.ctrlCTime) < 2*time.Second {
				return true
			}
		}
		// First press: clear input if any, show hint
		a.input = nil
		a.cursor = 0
		a.ctrlCHint = true
		a.ctrlCTime = time.Now()
		a.renderInput()
		// Schedule hint removal after 2 seconds
		ch := a.resultCh
		go func() {
			time.Sleep(2 * time.Second)
			select {
			case ch <- ctrlCExpiredMsg{}:
			default:
			}
		}()
		return false
	}

	// Any other key clears the Ctrl+C / ESC hints
	if a.ctrlCHint {
		a.ctrlCHint = false
		a.ctrlCTime = time.Time{}
	}
	if a.escHint {
		a.escHint = false
		a.escTime = time.Time{}
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
		a.handleApprovalByte('n')
	}
	// When agent is running, double-tap ESC to stop it.
	// If Cancel was already sent and agent is still running, cancel again
	// (force the context) so the user is never truly stuck.
	if a.agentRunning {
		if a.escHint && time.Since(a.escTime) < 2*time.Second {
			a.agent.Cancel()
			a.cancelSent = true
			a.escHint = false
			a.escTime = time.Time{}
			return
		}
		a.escHint = true
		a.escTime = time.Now()
		a.renderInput()
		ch := a.resultCh
		go func() {
			time.Sleep(2 * time.Second)
			select {
			case ch <- escExpiredMsg{}:
			default:
			}
		}()
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

	// SS3 sequence: ESC O <letter>
	// Some terminals send arrow keys as SS3 instead of CSI, especially when
	// application cursor key mode (DECCKM) is active. Scroll events converted
	// via DECSET 1007 may also arrive in this format.
	if b == 'O' {
		ss3, ok := readByte()
		if !ok {
			return
		}
		if !a.awaitingApproval {
			a.handleNavKey(ss3, nil)
		}
		return
	}

	if b != '[' {
		// ESC followed by non-[ byte (e.g. Alt+key) — treat as plain escape
		a.handlePlainEscape()
		return
	}

	// CSI sequence: ESC [
	// Per ECMA-48, collect parameter bytes (0x30–0x3F) and intermediate bytes
	// (0x20–0x2F) until a final byte (0x40–0x7E) arrives. This ensures that
	// unknown or unexpected CSI sequences are fully consumed and never leak
	// trailing bytes into the input buffer (e.g. mouse/scroll events from
	// terminals like Zed that send sequences herm does not handle).
	var params []byte
	var final byte
	for {
		b, ok = readByte()
		if !ok {
			return
		}
		if isCSIFinal(b) {
			final = b
			break
		}
		params = append(params, b)
		if len(params) > 128 {
			return // safety limit for malformed sequences
		}
	}

	// Tilde-terminated sequences
	if final == '~' {
		ps := string(params)
		switch {
		case ps == "200": // Bracketed paste: ESC [ 200 ~
			a.handlePaste(readBracketedPaste(readByte))
		case ps == "3": // Delete: ESC [ 3 ~
			if !a.awaitingApproval {
				a.deleteAtCursor()
				a.renderInput()
			}
		default:
			// modifyOtherKeys: ESC [ 27 ; <mod> ; <code> ~
			if strings.HasPrefix(ps, "27;") {
				var mod, code int
				if n, _ := fmt.Sscanf(ps[3:], "%d;%d", &mod, &code); n == 2 {
					a.handleModifyOtherKeys(mod, code)
				}
			}
			// All other tilde sequences (Insert, PgUp, etc.) silently consumed
		}
		return
	}

	if a.awaitingApproval {
		return
	}

	// Navigation keys (arrows, Home, End) — shared by CSI and SS3
	a.handleNavKey(final, params)
	// Unknown final bytes (mode responses, etc.): already fully consumed by
	// the collection loop above — silently discarded.
}

// handleNavKey dispatches a navigation key (A/B/C/D/H/F) with optional CSI
// parameters. Called for both CSI (ESC [ ... X) and SS3 (ESC O X) sequences.
func (a *App) handleNavKey(final byte, params []byte) {
	// Parse modifier from params (e.g. "1;5" in ESC [ 1;5 C → mod 5 = Ctrl).
	// CSI modifier encoding: value = 1 + bitmask (Shift=1, Alt=2, Ctrl=4).
	mod := 0
	if i := strings.LastIndexByte(string(params), ';'); i >= 0 {
		fmt.Sscanf(string(params[i+1:]), "%d", &mod)
	}
	isCtrl := mod > 0 && (mod-1)&4 != 0
	isAlt := mod > 0 && (mod-1)&2 != 0

	switch final {
	case 'A': // Up
		if len(params) > 0 {
			return // parameterized/modified up — no action defined
		}
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
		if len(params) > 0 {
			return // parameterized/modified down — no action defined
		}
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
		if isCtrl || isAlt {
			a.moveWordRight()
			a.renderInput()
		} else if a.menuActive && a.menuModels != nil {
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
		if isCtrl || isAlt {
			a.moveWordLeft()
			a.renderInput()
		} else if a.menuActive && a.menuModels != nil {
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
		// Show updated model if it changed
		if a.models != nil {
			a.showModelChange(a.config.resolveActiveModel(a.models))
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
		{label: "Git Co-Author", get: func(c Config) string { if c.effectiveGitCoAuthor() { return "on" }; return "off" }, toggle: func(c *Config) { if c.GitCoAuthor == nil { f := false; c.GitCoAuthor = &f } else { v := !*c.GitCoAuthor; c.GitCoAuthor = &v } }},
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
		case '2': // modifyOtherKeys (Ctrl+S, Ctrl+C, etc.)
			a.handleCSIDigit2(readByte, func(string) {})
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

// checkForUpdate queries the GitHub API for the latest release and compares
// it against the current binary version.
func checkForUpdate(currentVersion string) updateAvailableMsg {
	if currentVersion == "dev" {
		return updateAvailableMsg{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/aduermael/herm/releases/latest", nil)
	if err != nil {
		return updateAvailableMsg{err: err}
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return updateAvailableMsg{err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return updateAvailableMsg{err: fmt.Errorf("GitHub API returned %d", resp.StatusCode)}
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return updateAvailableMsg{err: err}
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	current := strings.TrimPrefix(currentVersion, "v")
	if latest != "" && latest != current {
		return updateAvailableMsg{version: latest}
	}
	return updateAvailableMsg{}
}

// performUpdate downloads and installs the specified version of herm,
// replacing the current binary in-place.
func performUpdate(version string) updateCompleteMsg {
	exePath, err := os.Executable()
	if err != nil {
		return updateCompleteMsg{err: fmt.Errorf("cannot determine executable path: %w", err)}
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return updateCompleteMsg{err: fmt.Errorf("cannot resolve executable path: %w", err)}
	}

	osName := runtime.GOOS
	archName := runtime.GOARCH

	url := fmt.Sprintf("https://github.com/aduermael/herm/releases/download/v%s/herm_%s_%s_%s.tar.gz",
		version, version, osName, archName)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return updateCompleteMsg{err: err}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return updateCompleteMsg{err: fmt.Errorf("download failed: %w", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return updateCompleteMsg{err: fmt.Errorf("download returned HTTP %d", resp.StatusCode)}
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return updateCompleteMsg{err: fmt.Errorf("gzip error: %w", err)}
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var binData []byte
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return updateCompleteMsg{err: fmt.Errorf("tar error: %w", err)}
		}
		if filepath.Base(hdr.Name) == "herm" && !hdr.FileInfo().IsDir() {
			binData, err = io.ReadAll(tr)
			if err != nil {
				return updateCompleteMsg{err: fmt.Errorf("reading binary from archive: %w", err)}
			}
			break
		}
	}
	if binData == nil {
		return updateCompleteMsg{err: fmt.Errorf("binary not found in archive")}
	}

	// Write to a temp file in the same directory, then atomic rename
	dir := filepath.Dir(exePath)
	tmp, err := os.CreateTemp(dir, "herm-update-*")
	if err != nil {
		return updateCompleteMsg{err: fmt.Errorf("creating temp file: %w", err)}
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(binData); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return updateCompleteMsg{err: fmt.Errorf("writing temp file: %w", err)}
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return updateCompleteMsg{err: fmt.Errorf("chmod: %w", err)}
	}
	tmp.Close()

	if err := os.Rename(tmpPath, exePath); err != nil {
		os.Remove(tmpPath)
		return updateCompleteMsg{err: fmt.Errorf("replacing binary: %w", err)}
	}

	return updateCompleteMsg{}
}

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
