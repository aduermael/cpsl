// render_subagent.go owns the live sub-agent display: the per-agent state
// struct, braille spinner, and line-formatting helpers used by the TUI.
package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"
)

// subAgentDisplay tracks per-agent display state for live TUI rendering.
type subAgentDisplay struct {
	task         string    // task label (first ~40 chars of the task description)
	status       string    // current activity (tool name or text snippet)
	done         bool
	mode         string    // "explore" or "general"
	toolCount    int       // number of tool calls executed
	startTime    time.Time // when this sub-agent started
	completedAt  time.Time // when this sub-agent finished (zero if still running)
	inputTokens  int       // total input tokens consumed
	outputTokens int       // total output tokens consumed
	failed       bool      // true if the sub-agent failed
	replacedBy   string    // ID of the retry agent that supersedes this one (empty if not replaced)
}

// maxSubAgentDisplayLines is the maximum number of agent lines shown per group.
const maxSubAgentDisplayLines = 5

// brailleSpinnerFrames is the 8-frame braille spinner animation sequence.
var brailleSpinnerFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}

// brailleSpinnerFrameCount is the number of frames in the braille spinner.
const brailleSpinnerFrameCount = 8

// brailleSpinner returns a colored braille spinner character for the given elapsed time.
func brailleSpinner(elapsed time.Duration) string {
	frameIdx := int(elapsed.Milliseconds()/50) % brailleSpinnerFrameCount
	color := pastelColor(elapsed)
	return fmt.Sprintf("%s%s\033[0m", color, brailleSpinnerFrames[frameIdx])
}

// subAgentDisplayLines returns grouped sub-agent display lines with per-agent metrics.
// Agents are grouped by mode. Each group has a header ("Running N Explore agents…")
// and per-agent lines showing spinner/✓/✗ + task + metrics.
func (a *App) subAgentDisplayLines() []string {
	if len(a.subAgents) == 0 {
		return nil
	}

	// Collect all visible agents (active + recently completed within the group).
	// Skip agents that have been replaced by a retry.
	var visible []*subAgentDisplay
	for _, sa := range a.subAgents {
		if sa.replacedBy != "" {
			continue
		}
		visible = append(visible, sa)
	}
	if len(visible) == 0 {
		return nil
	}

	// Stable ordering: completed agents first (by completedAt ascending),
	// then running agents (by startTime ascending).
	slices.SortFunc(visible, func(a, b *subAgentDisplay) int {
		// Completed before running.
		if a.done != b.done {
			if a.done {
				return -1
			}
			return 1
		}
		if a.done {
			// Both completed — sort by completedAt ascending.
			return cmp.Compare(a.completedAt.UnixNano(), b.completedAt.UnixNano())
		}
		// Both running — sort by startTime ascending.
		return cmp.Compare(a.startTime.UnixNano(), b.startTime.UnixNano())
	})

	// Group by mode.
	groups := make(map[string][]*subAgentDisplay)
	for _, sa := range visible {
		mode := sa.mode
		if mode == "" {
			mode = "agent"
		}
		groups[mode] = append(groups[mode], sa)
	}

	// Stable ordering: explore first, then general, then other.
	modeOrder := []string{ModeExplore, ModeGeneral}
	for mode := range groups {
		found := false
		for _, m := range modeOrder {
			if mode == m {
				found = true
				break
			}
		}
		if !found {
			modeOrder = append(modeOrder, mode)
		}
	}

	var out []string
	for _, mode := range modeOrder {
		agents, ok := groups[mode]
		if !ok {
			continue
		}

		// Count active agents in this group.
		activeCount := 0
		for _, sa := range agents {
			if !sa.done {
				activeCount++
			}
		}

		// Header line: "Running N Explore agents…" while active,
		// "N Explore agents" when all done.
		modeLabel := strings.ToUpper(mode[:1]) + mode[1:]
		total := len(agents)
		var header string
		if activeCount > 0 {
			if total == 1 {
				header = fmt.Sprintf("\033[2;3mRunning %s agent…\033[0m", modeLabel)
			} else {
				header = fmt.Sprintf("\033[2;3mRunning %d %s agents…\033[0m", total, modeLabel)
			}
		} else {
			if total == 1 {
				header = fmt.Sprintf("\033[2;3m%s agent\033[0m", modeLabel)
			} else {
				header = fmt.Sprintf("\033[2;3m%d %s agents\033[0m", total, modeLabel)
			}
		}
		out = append(out, header)

		// Per-agent lines (capped).
		shown := agents
		if len(shown) > maxSubAgentDisplayLines {
			shown = shown[:maxSubAgentDisplayLines]
		}
		for _, sa := range shown {
			out = append(out, formatSubAgentLine(sa))
		}
		if len(agents) > maxSubAgentDisplayLines {
			out = append(out, fmt.Sprintf("\033[2;3m  …and %d more\033[0m", len(agents)-maxSubAgentDisplayLines))
		}
	}
	return out
}

// formatSubAgentLine renders a single sub-agent status line:
// spinner/✓/✗ + task + | N 🛠️ | Xs | ↑in ↓out
func formatSubAgentLine(sa *subAgentDisplay) string {
	var prefix string
	if sa.done {
		if sa.failed {
			prefix = "\033[31m✗\033[0m" // red ✗
		} else {
			prefix = "\033[32m✓\033[0m" // green ✓
		}
	} else {
		elapsed := time.Since(sa.startTime)
		prefix = brailleSpinner(elapsed)
	}

	var metrics []string
	if sa.toolCount > 0 {
		metrics = append(metrics, fmt.Sprintf("%d 🛠️ ", sa.toolCount))
	}
	var elapsed time.Duration
	if !sa.startTime.IsZero() {
		if sa.done && !sa.completedAt.IsZero() {
			elapsed = sa.completedAt.Sub(sa.startTime)
		} else {
			elapsed = time.Since(sa.startTime)
		}
		metrics = append(metrics, fmt.Sprintf("%.2fs", elapsed.Seconds()))
	}
	if sa.inputTokens > 0 || sa.outputTokens > 0 {
		metrics = append(metrics, fmt.Sprintf("↑%s ↓%s",
			formatTokenCount(sa.inputTokens),
			formatTokenCount(sa.outputTokens)))
	}

	line := fmt.Sprintf("%s \033[2m%s\033[0m", prefix, sa.task)
	if len(metrics) > 0 {
		line += fmt.Sprintf(" \033[2m| %s\033[0m", strings.Join(metrics, " | "))
	}
	return line
}

// truncateTaskLabel returns the first ~40 chars of a task description for display.
func truncateTaskLabel(task string) string {
	// Take first line only.
	if idx := strings.IndexByte(task, '\n'); idx >= 0 {
		task = task[:idx]
	}
	const maxLen = 40
	if len(task) > maxLen {
		task = task[:maxLen] + "…"
	}
	return task
}

// shortID returns the first 8 characters of an agent ID for display.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// hasPendingBackgroundAgents returns true if any non-replaced sub-agent is
// still running. Used to suppress chatty main-agent narration while the UI
// already shows live sub-agent status.
func (a *App) hasPendingBackgroundAgents() bool {
	for _, sa := range a.subAgents {
		if !sa.done && sa.replacedBy == "" {
			return true
		}
	}
	return false
}

// getOrCreateSubAgent returns the display state for the given agent ID, creating it if needed.
func (a *App) getOrCreateSubAgent(agentID string) *subAgentDisplay {
	if a.subAgents == nil {
		a.subAgents = make(map[string]*subAgentDisplay)
	}
	sa, ok := a.subAgents[agentID]
	if !ok {
		sa = &subAgentDisplay{task: "unknown task"}
		a.subAgents[agentID] = sa
	}
	return sa
}
