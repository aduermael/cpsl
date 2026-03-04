package main

import (
	"fmt"
	"image/color"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
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
	modeShell
)

type msgKind int

const (
	msgUser    msgKind = iota // normal user message
	msgError                  // error feedback (e.g. unknown command)
	msgSuccess                // success feedback (e.g. config saved)
	msgInfo                   // informational feedback (e.g. config discarded)
)

type message struct {
	content string
	kind    msgKind
}

// commands is the list of available slash commands.
var commands = []string{"/branches", "/config", "/model", "/shell", "/worktrees"}

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
	viewport     viewport.Model
	width        int
	height       int
	messages     []message
	ready        bool
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
		messages: []message{},
		config:   cfg,
	}
}

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

// containerErrMsg signals that the container failed to start.
type containerErrMsg struct {
	err error
}

// execResultMsg carries the result of a /exec command.
type execResultMsg struct {
	result CommandResult
	err    error
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

// bootContainerCmd selects a worktree, creates a container client, and starts
// the container. Runs as an async tea.Cmd from Init().
func bootContainerCmd(cfg Config) tea.Msg {
	ccfg := cfg.containerConfig()
	client := NewContainerClient(ccfg)

	if !client.IsAvailable() {
		return containerErrMsg{err: fmt.Errorf(
			"Docker is not running. Please start Docker Desktop and try again.")}
	}

	// Determine workspace path.
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

// fetchModelsCmd returns a tea.Cmd that fetches models from OpenRouter.
func fetchModelsCmd() tea.Msg {
	models, err := fetchModels()
	return modelsMsg{models: models, err: err}
}

// fetchSWEScoresCmd returns a tea.Cmd that fetches SWE-bench scores.
func fetchSWEScoresCmd() tea.Msg {
	scores, err := fetchSWEScores()
	return sweScoresMsg{scores: scores, err: err}
}

func (m model) Init() tea.Cmd {
	cfg := m.config
	return tea.Batch(textarea.Blink, fetchModelsCmd, fetchSWEScoresCmd, func() tea.Msg {
		return bootContainerCmd(cfg)
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

	// Handle async status bar result regardless of mode
	if msg, ok := msg.(statusInfoMsg); ok {
		m.status = msg.info
		return m, nil
	}

	// Handle async worktree list result regardless of mode
	if msg, ok := msg.(worktreeListMsg); ok {
		if msg.err != nil {
			m.messages = append(m.messages, message{
				content: fmt.Sprintf("Error listing worktrees: %v", msg.err),
				kind:    msgError,
			})
			m.mode = modeChat
			if m.ready {
				m.viewport.SetHeight(m.viewportHeight())
				m.updateViewportContent()
				m.viewport.GotoBottom()
			}
			return m, m.textarea.Focus()
		}
		m.worktreeListC = newWorktreeList(msg.items, m.worktreePath, m.width, m.height)
		return m, nil
	}

	// Handle async branch list result regardless of mode
	if msg, ok := msg.(branchListMsg); ok {
		if msg.err != nil {
			m.messages = append(m.messages, message{
				content: fmt.Sprintf("Error listing branches: %v", msg.err),
				kind:    msgError,
			})
			m.mode = modeChat
			if m.ready {
				m.viewport.SetHeight(m.viewportHeight())
				m.updateViewportContent()
				m.viewport.GotoBottom()
			}
			return m, m.textarea.Focus()
		}
		m.branchListC = newBranchList(msg.items, msg.currentBranch, m.width, m.height)
		return m, nil
	}

	// Handle branch checkout result regardless of mode
	if msg, ok := msg.(branchCheckoutMsg); ok {
		if msg.err != nil {
			m.messages = append(m.messages, message{
				content: fmt.Sprintf("Checkout failed: %v", msg.err),
				kind:    msgError,
			})
		} else {
			m.messages = append(m.messages, message{
				content: fmt.Sprintf("Switched to branch '%s'", msg.branch),
				kind:    msgSuccess,
			})
			m.status.Branch = msg.branch
		}
		m.mode = modeChat
		if m.ready {
			m.viewport.SetHeight(m.viewportHeight())
			m.updateViewportContent()
			m.viewport.GotoBottom()
		}
		return m, m.textarea.Focus()
	}

	// Handle async container startup result regardless of mode
	if msg, ok := msg.(containerReadyMsg); ok {
		m.container = msg.client
		m.worktreePath = msg.worktreePath
		m.containerReady = true
		wtPath := msg.worktreePath
		return m, func() tea.Msg {
			return fetchStatusCmd(wtPath)
		}
	}
	if msg, ok := msg.(containerErrMsg); ok {
		m.containerErr = msg.err
		return m, nil
	}

	// Handle async exec result regardless of mode
	if msg, ok := msg.(execResultMsg); ok {
		if msg.err != nil {
			m.messages = append(m.messages, message{
				content: fmt.Sprintf("Exec error: %v", msg.err),
				kind:    msgError,
			})
		} else {
			kind := msgSuccess
			if msg.result.ExitCode != 0 {
				kind = msgError
			}
			output := strings.TrimRight(msg.result.Stdout, "\n")
			if msg.result.Stderr != "" {
				if output != "" {
					output += "\n"
				}
				output += strings.TrimRight(msg.result.Stderr, "\n")
			}
			if output == "" {
				output = fmt.Sprintf("(exit %d)", msg.result.ExitCode)
			} else if msg.result.ExitCode != 0 {
				output += fmt.Sprintf("\n(exit %d)", msg.result.ExitCode)
			}
			m.messages = append(m.messages, message{
				content: output,
				kind:    msgKind(kind),
			})
		}
		if m.ready {
			m.updateViewportContent()
			m.viewport.GotoBottom()
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

	// Shell mode: delegate to shell mode handler
	if m.mode == modeShell {
		return m.updateShellMode(msg)
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
			m.viewport = viewport.New(
				viewport.WithWidth(m.width),
				viewport.WithHeight(m.viewportHeight()),
			)
			m.viewport.KeyMap.Up.SetEnabled(false)
			m.viewport.KeyMap.Down.SetEnabled(false)
			m.viewport.KeyMap.Left.SetEnabled(false)
			m.viewport.KeyMap.Right.SetEnabled(false)
			m.viewport.KeyMap.PageUp.SetEnabled(false)
			m.viewport.KeyMap.PageDown.SetEnabled(false)
			m.viewport.KeyMap.HalfPageUp.SetEnabled(false)
			m.viewport.KeyMap.HalfPageDown.SetEnabled(false)
			m.ready = true
		} else {
			m.viewport.SetWidth(m.width)
			m.viewport.SetHeight(m.viewportHeight())
		}
		m.recalcTextareaHeight()
		m.updateViewportContent()

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
		case "tab":
			if matches := m.autocompleteMatches(); len(matches) > 0 {
				m.textarea.SetValue(matches[0])
				m.textarea.CursorEnd()
				m.recalcTextareaHeight()
				if m.ready {
					m.viewport.SetHeight(m.viewportHeight())
				}
			}
			return m, nil
		case "esc":
			if strings.HasPrefix(m.textarea.Value(), "/") {
				m.textarea.Reset()
				m.textarea.SetHeight(minInputHeight)
				if m.ready {
					m.viewport.SetHeight(m.viewportHeight())
				}
			}
			return m, nil
		case "enter":
			val := strings.TrimSpace(m.textarea.Value())
			if val != "" {
				if strings.HasPrefix(val, "/") {
					return m.handleCommand(val)
				}
				content := expandPastes(val, m.pasteStore)
				m.messages = append(m.messages, message{content: content})
				m.textarea.Reset()
				m.textarea.SetHeight(minInputHeight)
				if m.ready {
					m.viewport.SetHeight(m.viewportHeight())
					m.updateViewportContent()
					m.viewport.GotoBottom()
				}
			}
			return m, nil
		}
	}

	// Temporarily expand textarea to max height before Update so the
	// textarea's internal repositionView() doesn't scroll within a too-small
	// viewport. We'll shrink to the real height right after.
	m.textarea.SetHeight(maxInputHeight)

	// Update textarea (may receive modified PasteMsg with placeholder)
	var taCmd tea.Cmd
	m.textarea, taCmd = m.textarea.Update(taMsg)
	cmds = append(cmds, taCmd)

	// Now set the correct height based on actual content
	m.recalcTextareaHeight()

	// Update viewport
	if m.ready {
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		cmds = append(cmds, vpCmd)
	}

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
	if save {
		m.configForm.applyTo(&m.config)
		if err := saveConfig(m.config); err != nil {
			m.messages = append(m.messages, message{
				content: fmt.Sprintf("Error saving config: %v", err),
				kind:    msgError,
			})
		} else {
			m.messages = append(m.messages, message{
				content: "Config saved.",
				kind:    msgSuccess,
			})
		}
	} else {
		m.messages = append(m.messages, message{
			content: "Config changes discarded.",
			kind:    msgInfo,
		})
	}
	if m.ready {
		m.viewport.SetHeight(m.viewportHeight())
		m.updateViewportContent()
		m.viewport.GotoBottom()
	}
	return m, m.textarea.Focus()
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
		m.messages = append(m.messages, message{
			content: "Models are still loading... please try again in a moment.",
			kind:    msgInfo,
		})
		m.textarea.Reset()
		m.textarea.SetHeight(minInputHeight)
		if m.ready {
			m.viewport.SetHeight(m.viewportHeight())
			m.updateViewportContent()
			m.viewport.GotoBottom()
		}
		return m, nil
	}
	if m.modelsErr != nil {
		m.messages = append(m.messages, message{
			content: fmt.Sprintf("Failed to load models: %v", m.modelsErr),
			kind:    msgError,
		})
		m.textarea.Reset()
		m.textarea.SetHeight(minInputHeight)
		if m.ready {
			m.viewport.SetHeight(m.viewportHeight())
			m.updateViewportContent()
			m.viewport.GotoBottom()
		}
		return m, nil
	}
	available := m.config.availableModels(m.models)
	if len(available) == 0 {
		m.messages = append(m.messages, message{
			content: "No API keys configured. Use /config to add a key first.",
			kind:    msgError,
		})
		m.textarea.Reset()
		m.textarea.SetHeight(minInputHeight)
		if m.ready {
			m.viewport.SetHeight(m.viewportHeight())
			m.updateViewportContent()
			m.viewport.GotoBottom()
		}
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
			m.messages = append(m.messages, message{
				content: fmt.Sprintf("Error saving model: %v", err),
				kind:    msgError,
			})
		} else {
			m.messages = append(m.messages, message{
				content: fmt.Sprintf("Model set to %s.", selected.DisplayName),
				kind:    msgSuccess,
			})
		}
	} else {
		// Save sort preferences even on cancel
		_ = saveConfig(m.config)
		m.messages = append(m.messages, message{
			content: "Model selection cancelled.",
			kind:    msgInfo,
		})
	}
	if m.ready {
		m.viewport.SetHeight(m.viewportHeight())
		m.updateViewportContent()
		m.viewport.GotoBottom()
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
	if m.ready {
		m.viewport.SetHeight(m.viewportHeight())
		m.updateViewportContent()
		m.viewport.GotoBottom()
	}
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
	if m.ready {
		m.viewport.SetHeight(m.viewportHeight())
		m.updateViewportContent()
		m.viewport.GotoBottom()
	}
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

// handleCommand processes slash commands and returns the updated model.
func (m model) handleCommand(input string) (tea.Model, tea.Cmd) {
	cmd := strings.Fields(input)[0] // e.g. "/config"

	switch cmd {
	case "/branches":
		return m.enterBranchMode()
	case "/config":
		return m.enterConfigMode()
	case "/shell":
		return m.enterShellMode()
	case "/model":
		return m.enterModelMode()
	case "/worktrees":
		return m.enterWorktreeMode()
	default:
		m.messages = append(m.messages, message{
			content: fmt.Sprintf("Unknown command: %s", cmd),
			kind:    msgError,
		})
		m.textarea.Reset()
		m.textarea.SetHeight(minInputHeight)
		if m.ready {
			m.viewport.SetHeight(m.viewportHeight())
			m.updateViewportContent()
			m.viewport.GotoBottom()
		}
		return m, nil
	}
}

// cleanup stops the container and unlocks the worktree.
func (m *model) cleanup() {
	if m.container != nil {
		_ = m.container.Stop()
	}
	if m.worktreePath != "" {
		_ = unlockWorktree(m.worktreePath)
	}
}

// enterShellMode switches to the shell mode.
func (m model) enterShellMode() (tea.Model, tea.Cmd) {
	// Check container state.
	if m.containerErr != nil {
		m.messages = append(m.messages, message{
			content: fmt.Sprintf("Container error: %v", m.containerErr),
			kind:    msgError,
		})
		m.textarea.Reset()
		m.textarea.SetHeight(minInputHeight)
		if m.ready {
			m.viewport.SetHeight(m.viewportHeight())
			m.updateViewportContent()
			m.viewport.GotoBottom()
		}
		return m, nil
	}
	if !m.containerReady {
		m.messages = append(m.messages, message{
			content: "Container is starting... please try again in a moment.",
			kind:    msgInfo,
		})
		m.textarea.Reset()
		m.textarea.SetHeight(minInputHeight)
		if m.ready {
			m.viewport.SetHeight(m.viewportHeight())
			m.updateViewportContent()
			m.viewport.GotoBottom()
		}
		return m, nil
	}

	m.mode = modeShell
	m.textarea.Reset()
	m.textarea.SetHeight(minInputHeight)
	m.textarea.Placeholder = "shell $"
	m.messages = append(m.messages, message{
		content: "Entering shell mode. Ctrl+C to exit.",
		kind:    msgInfo,
	})
	if m.ready {
		m.viewport.SetHeight(m.viewportHeight())
		m.updateViewportContent()
		m.viewport.GotoBottom()
	}
	return m, nil
}

// exitShellMode returns to chat mode.
func (m model) exitShellMode() (tea.Model, tea.Cmd) {
	m.mode = modeChat
	m.textarea.Reset()
	m.textarea.SetHeight(minInputHeight)
	m.textarea.Placeholder = "Type a message..."
	m.messages = append(m.messages, message{
		content: "Exited shell mode.",
		kind:    msgInfo,
	})
	if m.ready {
		m.viewport.SetHeight(m.viewportHeight())
		m.updateViewportContent()
		m.viewport.GotoBottom()
	}
	return m, m.textarea.Focus()
}

// updateShellMode handles input while in shell mode.
func (m model) updateShellMode(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		inputAreaWidth := m.width - 2
		if inputAreaWidth < 1 {
			inputAreaWidth = 1
		}
		m.textarea.SetWidth(inputAreaWidth)

		if m.ready {
			m.viewport.SetWidth(m.width)
			m.viewport.SetHeight(m.viewportHeight())
		}
		m.recalcTextareaHeight()
		m.updateViewportContent()
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return m.exitShellMode()
		case "enter":
			val := strings.TrimSpace(m.textarea.Value())
			if val == "" {
				return m, nil
			}

			// Show the command being run.
			m.messages = append(m.messages, message{
				content: fmt.Sprintf("$ %s", val),
				kind:    msgUser,
			})
			m.textarea.Reset()
			m.textarea.SetHeight(minInputHeight)
			if m.ready {
				m.viewport.SetHeight(m.viewportHeight())
				m.updateViewportContent()
				m.viewport.GotoBottom()
			}

			// Fire async exec.
			client := m.container
			execCmd := val
			return m, func() tea.Msg {
				result, err := client.Exec(execCmd, 120)
				return execResultMsg{result: result, err: err}
			}
		}
	}

	// Temporarily expand textarea to max height before Update
	m.textarea.SetHeight(maxInputHeight)

	var taCmd tea.Cmd
	m.textarea, taCmd = m.textarea.Update(msg)

	m.recalcTextareaHeight()

	if m.ready {
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		return m, tea.Batch(taCmd, vpCmd)
	}

	return m, taCmd
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
	if m.ready {
		m.viewport.SetHeight(m.viewportHeight())
	}
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

func (m model) viewportHeight() int {
	h := m.height - m.inputBoxHeight() - m.autocompleteHeight() - m.statusBarHeight()
	if h < 1 {
		h = 1
	}
	return h
}

func (m *model) updateViewportContent() {
	if !m.ready {
		return
	}

	logo := renderLogo()
	logoHeight := lipgloss.Height(logo)

	var content string
	if len(m.messages) == 0 {
		vpHeight := m.viewportHeight()
		padding := (vpHeight - logoHeight) / 2
		if padding < 0 {
			padding = 0
		}
		centeredLogo := lipgloss.PlaceHorizontal(m.width, lipgloss.Center, logo)
		content = strings.Repeat("\n", padding) + centeredLogo
	} else {
		centeredLogo := lipgloss.PlaceHorizontal(m.width, lipgloss.Center, logo)

		msgStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E0E0E0")).
			PaddingLeft(2)

		errorStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF6B6B")).
			PaddingLeft(2).
			Italic(true)

		successStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6FE7B8")).
			PaddingLeft(2).
			Italic(true)

		infoStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8B7BA8")).
			PaddingLeft(2).
			Italic(true)

		var parts []string
		parts = append(parts, centeredLogo, "")
		for _, msg := range m.messages {
			wrapped := lipgloss.NewStyle().Width(m.width - 4).Render(msg.content)
			var style lipgloss.Style
			switch msg.kind {
			case msgError:
				style = errorStyle
			case msgSuccess:
				style = successStyle
			case msgInfo:
				style = infoStyle
			default:
				style = msgStyle
			}
			parts = append(parts, style.Render(wrapped), "")
		}
		content = strings.Join(parts, "\n")
	}

	m.viewport.SetContent(content)
}

func (m model) statusBarHeight() int {
	if m.status.Branch == "" && m.mode != modeShell {
		return 0
	}
	return 1
}

func (m model) renderStatusBar() string {
	if m.status.Branch == "" && m.mode != modeShell {
		return ""
	}

	leftStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#9B6ADE")).
		Bold(true)
	dimStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6B34B0"))

	// Left side: shell mode indicator or branch + optional PR
	var left string
	if m.mode == modeShell {
		shellStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6FE7B8")).
			Bold(true)
		left = shellStyle.Render("SHELL")
		if m.status.Branch != "" {
			left += dimStyle.Render("  " + m.status.Branch)
		}
	} else {
		left = leftStyle.Render(" " + m.status.Branch)
		if m.status.PRNumber > 0 {
			left += dimStyle.Render(fmt.Sprintf(" PR #%d", m.status.PRNumber))
		}
	}

	// Right side: worktree name + active count
	right := dimStyle.Render(" " + m.status.WorktreeName)
	if m.status.TotalCount > 1 {
		right += dimStyle.Render(fmt.Sprintf(" (%d/%d)", m.status.ActiveCount, m.status.TotalCount))
	}

	// Fill the space between left and right
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	gap := m.width - leftW - rightW - 2 // 1 padding each side
	if gap < 1 {
		gap = 1
	}

	bar := " " + left + strings.Repeat(" ", gap) + right + " "

	bgColor := "#1A0A2E"
	if m.mode == modeShell {
		bgColor = "#0A2E1A"
	}
	barStyle := lipgloss.NewStyle().
		Background(lipgloss.Color(bgColor)).
		Width(m.width)

	return barStyle.Render(bar)
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
	var lines []string
	for i, cmd := range matches {
		if i == 0 {
			lines = append(lines, highlightStyle.Render(cmd))
		} else {
			lines = append(lines, normalStyle.Render(cmd))
		}
	}
	return strings.Join(lines, "\n")
}

func (m model) View() tea.View {
	if !m.ready {
		v := tea.NewView("Initializing...")
		v.AltScreen = true
		return v
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

	var inputBorderColors []color.Color
	if m.mode == modeShell {
		inputBorderColors = []color.Color{
			lipgloss.Color("#1A6B34"),
			lipgloss.Color("#2E9B55"),
			lipgloss.Color("#4EC77B"),
			lipgloss.Color("#2E9B55"),
			lipgloss.Color("#1A6B34"),
		}
	} else {
		inputBorderColors = borderGradientColors
	}
	inputBorderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForegroundBlend(inputBorderColors...).
		Width(m.width)

	inputBox := inputBorderStyle.Render(m.textarea.View())

	statusBar := m.renderStatusBar()
	autocomplete := m.renderAutocomplete()
	acHeight := m.autocompleteHeight()

	var fullView string
	viewportView := m.viewport.View()
	if statusBar != "" {
		viewportView += "\n" + statusBar
	}
	if autocomplete != "" {
		fullView = viewportView + "\n" + autocomplete + "\n" + inputBox
	} else {
		fullView = viewportView + "\n" + inputBox
	}

	v := tea.NewView(fullView)

	c := m.textarea.Cursor()
	if c != nil {
		c.Y += m.viewport.Height() + m.statusBarHeight() + acHeight + 1 // +1 for top border
		c.X += 1                                    // +1 for left border
		v.Cursor = c
	}

	v.AltScreen = true
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
	p := tea.NewProgram(initialModel())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
