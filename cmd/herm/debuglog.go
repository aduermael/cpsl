// debuglog.go implements file-based conversation debug logging.
// When debug mode is enabled, every conversation gets a debug file in
// .herm/debug/ that logs system prompts, tool calls/results, agent events,
// usage stats, user messages, and session summary.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const debugDir = "debug"

// initDebugLog creates the .herm/debug/ directory under repoRoot and opens a
// new debug log file named debug-<timestamp>.log. Returns the open file, the
// file path, and any error.
func initDebugLog(repoRoot string) (*os.File, string, error) {
	dir := filepath.Join(repoRoot, configDir, debugDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", fmt.Errorf("creating debug dir: %w", err)
	}

	name := fmt.Sprintf("debug-%s.log", time.Now().Format("20060102-150405"))
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, "", fmt.Errorf("opening debug file: %w", err)
	}
	return f, path, nil
}

// debugWrite appends a delimited section to the debug file.
// If f is nil, it's a no-op.
func debugWrite(f *os.File, section string, content string) {
	if f == nil {
		return
	}
	fmt.Fprintf(f, "\n── %s ──\n%s\n", section, content)
}

// closeDebugLog closes the debug file if it's open.
func closeDebugLog(f *os.File) {
	if f != nil {
		f.Close()
	}
}

// debugActive returns true if debug mode is enabled via config or CLI flag.
func (a *App) debugActive() bool {
	return a.config.DebugMode || a.cliDebug
}

// debugWriteSection is a convenience method that writes to the app's debug file.
func (a *App) debugWriteSection(section, content string) {
	debugWrite(a.debugFile, section, content)
}

// initAppDebugLog initializes the debug log file for the app if debug mode is active.
// Should be called after repoRoot is known (i.e. after workspaceMsg).
func (a *App) initAppDebugLog() {
	if !a.debugActive() {
		return
	}
	root := a.repoRoot
	if root == "" {
		// Fallback to current directory if not in a git repo.
		root, _ = os.Getwd()
	}
	f, path, err := initDebugLog(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "debug: failed to create debug log: %v\n", err)
		return
	}
	a.debugFile = f
	a.debugFilePath = path
}

// debugWriteSessionSummary writes a session stats section to the debug file.
func (a *App) debugWriteSessionSummary() {
	if a.debugFile == nil {
		return
	}
	var b fmt.Stringer = &sessionSummaryBuilder{a: a}
	a.debugWriteSection("Session Summary", b.String())
}

type sessionSummaryBuilder struct {
	a *App
}

func (s *sessionSummaryBuilder) String() string {
	a := s.a
	var out string
	out += fmt.Sprintf("Elapsed: %s\n", a.agentElapsed.Truncate(time.Second))
	out += fmt.Sprintf("LLM calls: %d (main: %d, sub-agents: %d)\n",
		a.sessionLLMCalls, a.mainAgentLLMCalls, a.sessionLLMCalls-a.mainAgentLLMCalls)
	out += fmt.Sprintf("Input tokens: %s (main: %s)\n",
		formatTokenCount(a.sessionInputTokens), formatTokenCount(a.mainAgentInputTokens))
	out += fmt.Sprintf("Output tokens: %s (main: %s)\n",
		formatTokenCount(a.sessionOutputTokens), formatTokenCount(a.mainAgentOutputTokens))
	if a.sessionCacheRead > 0 {
		out += fmt.Sprintf("Cache read: %s\n", formatTokenCount(a.sessionCacheRead))
	}
	out += fmt.Sprintf("Cost: %s\n", formatCost(a.sessionCostUSD))
	out += fmt.Sprintf("Tool calls: %d (%s result data)\n", a.sessionToolResults, formatBytes(a.sessionToolBytes))

	if len(a.sessionToolStats) > 0 {
		type toolStat struct {
			name         string
			count, bytes int
		}
		var stats []toolStat
		for name, s := range a.sessionToolStats {
			stats = append(stats, toolStat{name, s[0], s[1]})
		}
		sort.Slice(stats, func(i, j int) bool { return stats[i].bytes > stats[j].bytes })
		out += "\nPer tool:\n"
		for _, s := range stats {
			out += fmt.Sprintf("  %-12s %3d calls  %6s\n", s.name, s.count, formatBytes(s.bytes))
		}
	}
	return out
}

// regenerateDebugFile truncates and rewrites the debug file from the current
// conversation state. Called on resize and /clear to keep the debug file in
// sync with what the user sees.
func (a *App) regenerateDebugFile() {
	if a.debugFile == nil {
		return
	}
	// Truncate the file
	a.debugFile.Truncate(0)
	a.debugFile.Seek(0, 0)

	// Rewrite all messages
	for _, msg := range a.messages {
		switch msg.kind {
		case msgUser:
			debugWrite(a.debugFile, "User Message", msg.content)
		case msgAssistant:
			debugWrite(a.debugFile, "Assistant Text", msg.content)
		case msgToolCall:
			debugWrite(a.debugFile, "Tool Call", msg.content)
		case msgToolResult:
			label := "Tool Result"
			if msg.isError {
				label = "Tool Result [ERROR]"
			}
			debugWrite(a.debugFile, label, msg.content)
		case msgSystemPrompt:
			debugWrite(a.debugFile, "System Prompt", msg.content)
		case msgInfo:
			debugWrite(a.debugFile, "Info", msg.content)
		case msgSuccess:
			debugWrite(a.debugFile, "Success", msg.content)
		case msgError:
			debugWrite(a.debugFile, "Error", msg.content)
		}
	}

	// Include streaming text if agent is running
	if a.streamingText != "" {
		debugWrite(a.debugFile, "Assistant Text [streaming...]", a.streamingText)
	}

	// Append current session stats
	var b fmt.Stringer = &sessionSummaryBuilder{a: a}
	debugWrite(a.debugFile, "Session Summary", b.String())
}
