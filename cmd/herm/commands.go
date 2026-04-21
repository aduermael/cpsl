// commands.go implements slash command dispatch, worktree management,
// and shell mode for the herm TUI.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/term"
	"langdag.com/langdag/types"
)

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
		a.mainAgentToolCount = 0
		a.sessionToolResults = 0
		a.sessionToolBytes = 0
		a.sessionToolStats = nil
		a.lastInputTokens = 0
		a.agentElapsed = 0
		a.shownInitialModel = false
		a.lastModelID = ""
		a.subAgents = nil
		a.subAgentGroupInserted = false
		// Finalize old trace and create a new one for the new conversation.
		if a.traceCollector != nil {
			a.traceCollector.Finalize()
			if err := a.traceCollector.FlushToFile(a.traceFilePath); err != nil {
				fmt.Fprintf(os.Stderr, "debug: failed to write trace: %v\n", err)
			}
			a.traceCollector = nil
			a.traceFilePath = ""
			a.initAppDebugLog()
		}
		a.maybeShowInitialModels()
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
	if m := findModelByID(findModelByIDOptions{models: a.models, id: a.config.resolveActiveModel(a.models)}); m != nil {
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
				{Source: wtPath, Destination: wtPath},
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

	// Clear screen and disable herm-specific terminal features before the shell.
	// IMPORTANT: stay in raw mode — do NOT term.Restore to cooked mode.
	// Docker saves and restores whatever terminal state it receives.  If we
	// switched to cooked (canonical) mode here, any character the user types
	// between Docker's restore and our MakeRaw would land in the canonical
	// buffer, which macOS/BSD silently discards when ICANON is cleared —
	// causing the "first character lost" bug.  Keeping raw mode throughout
	// eliminates that gap entirely.  The container's own PTY handles line
	// editing, echo, and signal generation independently of the host terminal.
	fmt.Print("\033[H\033[2J\033[3J") // clear screen + scrollback
	fmt.Print("\033[?25h")            // show cursor
	fmt.Print("\033[>4;0m")           // disable modifyOtherKeys
	fmt.Print("\033[?2004l")          // disable bracketed paste

	// Brief pause + flush to discard any stale terminal responses still in-flight.
	flushStdin(a.fd)

	// Run shell synchronously — full TTY control goes to the child process
	shellCmd := a.container.ShellCmd()
	shellErr := shellCmd.Run()

	// Docker preserved our raw-mode state.  Re-apply MakeRaw defensively
	// (Docker or a crashed child may have altered flags) but do NOT overwrite
	// a.oldState — that must remain the original cooked-mode snapshot so the
	// terminal is properly restored when the app exits.
	if _, err := term.MakeRaw(a.fd); err != nil {
		fmt.Fprintf(os.Stderr, "failed to re-enter raw mode: %v\n", err)
		a.quit = true
		return
	}

	// Flush any stale bytes left by the shell session (e.g. CPR responses
	// still in-flight through Docker's PTY chain).
	flushStdin(a.fd)

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
