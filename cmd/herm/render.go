// render.go implements terminal screen painting, visual line wrapping, and
// the main render loop for the herm TUI. ANSI styling helpers live in style.go.
package main

import (
	"cmp"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"github.com/rivo/uniseg"
)

// ─── Visual line wrapping (from simple-chat) ───

type vline struct {
	start    int // rune index of first char
	length   int // number of runes
	startCol int // visual column where text starts
}

// getVisualLines splits the input runes into visual lines, accounting for
// the prompt prefix on the first line and terminal-width wrapping.
// It prefers word boundaries (spaces) for wrapping, falling back to
// character-level breaks for words longer than the available width.
func getVisualLines(input []rune, cursor int, width int) []vline {
	var lines []vline
	start := 0
	startCol := promptPrefixCols
	length := 0
	lastSpaceIdx := -1

	for i, r := range input {
		if r == '\n' {
			lines = append(lines, vline{start, length, startCol})
			start = i + 1
			startCol = 0
			length = 0
			lastSpaceIdx = -1
			continue
		}
		length++
		if r == ' ' {
			lastSpaceIdx = i
		}
		if startCol+length >= width {
			if lastSpaceIdx >= start {
				// Word wrap: break after the last space
				wrapLen := lastSpaceIdx - start + 1
				lines = append(lines, vline{start, wrapLen, startCol})
				start = lastSpaceIdx + 1
				length = length - wrapLen
				startCol = 0
				lastSpaceIdx = -1
			} else {
				// No space found, fall back to character-level wrap
				lines = append(lines, vline{start, length, startCol})
				start = i + 1
				startCol = 0
				length = 0
				lastSpaceIdx = -1
			}
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
			// At a line boundary: if this was a soft wrap (not a newline),
			// the cursor belongs on the next line.
			if cursor == end && i < len(vlines)-1 && (end >= len(input) || input[end] != '\n') {
				continue
			}
			return i, vl.startCol + (cursor - vl.start)
		}
	}
	last := len(vlines) - 1
	vl := vlines[last]
	return last, vl.startCol + vl.length
}

// padCodeBlockRow pads a code block row with spaces so the background fills
// the full terminal width. It ensures \033[0m comes after the padding.
func padCodeBlockRow(row string, width int) string {
	const reset = "\033[0m"
	stripped := row
	hasReset := strings.HasSuffix(row, reset)
	if hasReset {
		stripped = row[:len(row)-len(reset)]
	}
	vw := visibleWidth(stripped)
	if pad := width - vw; pad > 0 {
		stripped += strings.Repeat(" ", pad)
	}
	return stripped + reset
}

// wrapString splits a string into visual rows of at most `w` columns.
// It is ANSI-aware: escape sequences don't count toward visual width, and
// active styling is re-emitted on continuation lines. Character widths are
// measured with uniseg.StringWidth (so wide chars like emoji count as 2).
// Wrapping prefers word boundaries (spaces); words longer than `w` columns
// fall back to character-level breaking.
func wrapString(s string, startCol int, w int) []string {
	if w <= 0 {
		return []string{s}
	}

	// Split into tokens: ANSI sequences and printable segments.
	type token struct {
		text  string
		isSeq bool
	}
	var tokens []token
	rest := s
	for rest != "" {
		loc := ansiEscRe.FindStringIndex(rest)
		if loc == nil {
			tokens = append(tokens, token{rest, false})
			break
		}
		if loc[0] > 0 {
			tokens = append(tokens, token{rest[:loc[0]], false})
		}
		tokens = append(tokens, token{rest[loc[0]:loc[1]], true})
		rest = rest[loc[1]:]
	}

	var rows []string
	var curLine strings.Builder
	col := startCol
	var activeSeqs []string // stack of active ANSI sequences for re-emit

	flush := func() {
		rows = append(rows, curLine.String())
		curLine.Reset()
		col = 0
		for _, seq := range activeSeqs {
			curLine.WriteString(seq)
		}
	}

	applyANSI := func(seq string) {
		curLine.WriteString(seq)
		if seq == "\033[0m" || seq == "\033[m" {
			activeSeqs = nil
		} else {
			activeSeqs = append(activeSeqs, seq)
		}
	}

	// Word buffer: accumulates parts (text chunks and ANSI sequences) that
	// form a single visual word spanning across tokens.
	type wordPart struct {
		text  string
		isSeq bool
	}
	var wordParts []wordPart
	var wordBuf strings.Builder // accumulates current run of non-space chars
	wordWidth := 0

	flushWordBuf := func() {
		if wordBuf.Len() > 0 {
			wordParts = append(wordParts, wordPart{wordBuf.String(), false})
			wordBuf.Reset()
		}
	}

	commitWord := func() {
		flushWordBuf()
		if wordWidth == 0 {
			// Only ANSI sequences — apply them directly
			for _, p := range wordParts {
				if p.isSeq {
					applyANSI(p.text)
				}
			}
			wordParts = wordParts[:0]
			return
		}
		if wordWidth <= w {
			// Word fits on a full line — move to next line if needed
			if col+wordWidth > w {
				flush()
			}
			for _, p := range wordParts {
				if p.isSeq {
					applyANSI(p.text)
				} else {
					curLine.WriteString(p.text)
				}
			}
			col += wordWidth
		} else {
			// Word wider than line — character-break
			for _, p := range wordParts {
				if p.isSeq {
					applyANSI(p.text)
				} else {
					for _, r := range p.text {
						rw := uniseg.StringWidth(string(r))
						if col+rw > w {
							flush()
						}
						curLine.WriteRune(r)
						col += rw
					}
				}
			}
		}
		wordParts = wordParts[:0]
		wordWidth = 0
	}

	for _, tok := range tokens {
		if tok.isSeq {
			flushWordBuf()
			wordParts = append(wordParts, wordPart{tok.text, true})
			continue
		}
		for _, r := range tok.text {
			if unicode.IsSpace(r) {
				commitWord()
				rw := uniseg.StringWidth(string(r))
				if col+rw > w {
					flush()
				} else {
					curLine.WriteRune(r)
					col += rw
				}
			} else {
				wordBuf.WriteRune(r)
				wordWidth += uniseg.StringWidth(string(r))
			}
		}
	}
	commitWord()
	rows = append(rows, curLine.String())

	if len(rows) == 0 {
		return []string{""}
	}
	return rows
}

// toolGroupEntry represents a single tool call (and optional result) within a grouped block.
type toolGroupEntry struct {
	summary  string        // tool call summary text (e.g. "~ $ ls", "~ edit foo.go")
	toolName string        // original tool name for output filtering rules
	result   string        // collapsed tool result content (empty if no result)
	isError  bool          // whether the result is an error
	duration time.Duration // tool execution time
}

// toolGroup represents a sequence of consecutive tool call/result pairs.
type toolGroup struct {
	entries    []toolGroupEntry
	consumed   int  // total messages consumed from the message list
	inProgress bool // last entry has no result (tool still running)
}

// collectToolGroup scans messages starting at startIdx and collects consecutive
// tool call/result pairs into a group. It first gathers all consecutive
// msgToolCall messages, then pairs them with following msgToolResult messages
// positionally. This handles both interleaved (call, result, call, result) and
// parallel (call, call, result, result) patterns. The group continues as long
// as tool calls or tool results appear without a different message kind in
// between. inProgress is set when there are more calls than results.
func collectToolGroup(messages []chatMessage, startIdx int) toolGroup {
	var g toolGroup
	i := startIdx

	// Phase 1: collect all consecutive tool calls and results (no other kinds).
	var calls []int  // indices of msgToolCall messages
	var results []int // indices of msgToolResult messages
	for i < len(messages) {
		switch messages[i].kind {
		case msgToolCall:
			calls = append(calls, i)
			g.consumed++
			i++
		case msgToolResult:
			results = append(results, i)
			g.consumed++
			i++
		default:
			goto done
		}
	}
done:

	// Phase 2: build entries by pairing calls with results positionally.
	for ci, callIdx := range calls {
		entry := toolGroupEntry{
			summary:  strings.ReplaceAll(messages[callIdx].content, "\r", ""),
			toolName: messages[callIdx].toolName,
		}
		if ci < len(results) {
			resIdx := results[ci]
			entry.result = strings.ReplaceAll(messages[resIdx].content, "\r", "")
			entry.isError = messages[resIdx].isError
			entry.duration = messages[resIdx].duration
		}
		g.entries = append(g.entries, entry)
	}

	// More calls than results means the trailing calls are in-progress.
	if len(calls) > len(results) {
		g.inProgress = true
	}

	return g
}

func (a *App) buildBlockRows() []string {
	var rows []string
	for _, line := range buildLogo(a.width) {
		rows = append(rows, wrapString(line, 0, a.width)...)
	}
	inCodeBlock := false
	skipUntil := 0
	for i, msg := range a.messages {
		if i < skipUntil {
			continue
		}

		// Consecutive tool calls → collect into a group and render as a single block.
		if msg.kind == msgToolCall {
			group := collectToolGroup(a.messages, i)
			skipUntil = i + group.consumed
			if msg.leadBlank {
				rows = append(rows, "")
			}
			var liveDur string
			if group.inProgress && !a.toolStartTime.IsZero() {
				liveDur = formatDuration(time.Since(a.toolStartTime))
			}
			block := renderToolGroup(group.entries, a.width, group.inProgress, liveDur)
			for _, logLine := range strings.Split(block, "\n") {
				rows = append(rows, wrapString(logLine, 0, a.width)...)
			}
			// Blank line after group unless next message has leadBlank.
			if skipUntil >= len(a.messages) || !a.messages[skipUntil].leadBlank {
				rows = append(rows, "")
			}
			continue
		}

		// Standalone tool result (no preceding tool call) — render as box.
		if msg.kind == msgToolResult {
			content := strings.ReplaceAll(msg.content, "\r", "")
			box := renderToolBox("~ result", content, a.width, msg.isError, formatDuration(msg.duration))
			if msg.leadBlank {
				rows = append(rows, "")
			}
			for _, logLine := range strings.Split(box, "\n") {
				rows = append(rows, wrapString(logLine, 0, a.width)...)
			}
			nextIdx := i + 1
			if nextIdx >= len(a.messages) || !a.messages[nextIdx].leadBlank {
				rows = append(rows, "")
			}
			continue
		}

		// Sub-agent group anchor — render live sub-agent display inline.
		if msg.kind == msgSubAgentGroup {
			if subLines := a.subAgentDisplayLines(); len(subLines) > 0 {
				for _, line := range subLines {
					rows = append(rows, wrapString(line, 0, a.width)...)
				}
				rows = append(rows, "")
			}
			continue
		}

		{
			rendered := renderMessage(msg)
			for _, logLine := range strings.Split(rendered, "\n") {
				wasInCodeBlock := inCodeBlock
				if msg.kind == msgAssistant {
					var skip bool
					logLine, inCodeBlock, skip = processMarkdownLine(logLine, inCodeBlock)
					if skip {
						continue
					}
				}
				wrapped := wrapString(logLine, 0, a.width)
				if wasInCodeBlock && msg.kind == msgAssistant {
					for j := range wrapped {
						wrapped[j] = padCodeBlockRow(wrapped[j], a.width)
					}
				}
				rows = append(rows, wrapped...)
			}
		}

		// Add blank line after block, unless:
		// - next message already has leadBlank, or
		// - this is an assistant message followed by another assistant message
		//   (consecutive assistant chunks already contain their own newlines)
		peekIdx := i + 1
		peekHasBlank := peekIdx < len(a.messages) && a.messages[peekIdx].leadBlank
		peekIsAssistant := peekIdx < len(a.messages) && a.messages[peekIdx].kind == msgAssistant
		if !peekHasBlank && !(msg.kind == msgAssistant && peekIsAssistant) {
			rows = append(rows, "")
		}
	}
	// Show streaming text above the input area
	if a.streamingText != "" {
		for _, logLine := range strings.Split(a.streamingText, "\n") {
			wasInCodeBlock := inCodeBlock
			var skip bool
			logLine, inCodeBlock, skip = processMarkdownLine(logLine, inCodeBlock)
			if !skip {
				wrapped := wrapString(logLine, 0, a.width)
				if wasInCodeBlock {
					for j := range wrapped {
						wrapped[j] = padCodeBlockRow(wrapped[j], a.width)
					}
				}
				rows = append(rows, wrapped...)
			}
		}
		rows = append(rows, "")
	}
	// Show animated status line while agent is running, or dim elapsed when done
	if a.agentRunning && a.awaitingApproval {
		// Paused: show dim elapsed while waiting for user approval
		elapsed := a.agentElapsedTime()
		text := funnyTexts[a.agentTextIndex]
		label := fmt.Sprintf("\033[2;3m⏸ %s | %d 🛠️  | %.2fs | ↑%s ↓%s\033[0m",
			text, a.mainAgentToolCount, elapsed.Seconds(),
			formatTokenCount(int(math.Round(a.agentDisplayInTok))),
			formatTokenCount(int(math.Round(a.agentDisplayOutTok))))
		rows = append(rows, wrapString(label, 0, a.width)...)
		rows = append(rows, "")
	} else if a.agentRunning {
		elapsed := a.agentElapsedTime()
		text := funnyTexts[a.agentTextIndex]
		spinner := brailleSpinner(elapsed)
		color := pastelColor(elapsed)
		label := fmt.Sprintf("%s %s\033[3m%s | %d 🛠️  | %.2fs | ↑%s ↓%s\033[0m",
			spinner, color, text, a.mainAgentToolCount, elapsed.Seconds(),
			formatTokenCount(int(math.Round(a.agentDisplayInTok))),
			formatTokenCount(int(math.Round(a.agentDisplayOutTok))))
		rows = append(rows, wrapString(label, 0, a.width)...)
		rows = append(rows, "")
	} else if a.agentElapsed > 0 {
		elapsed := fmt.Sprintf("\033[32m✓\033[2m %d 🛠️  | %.2fs | ↑%s ↓%s\033[0m",
			a.mainAgentToolCount, a.agentElapsed.Seconds(),
			formatTokenCount(a.mainAgentInputTokens),
			formatTokenCount(a.mainAgentOutputTokens))
		rows = append(rows, wrapString(elapsed, 0, a.width)...)
		rows = append(rows, "")
	}
	return collapseBlankRows(rows)
}

// collapseBlankRows reduces consecutive blank rows to at most one.
// A row is "blank" if it is empty or contains only ANSI reset sequences.
func collapseBlankRows(rows []string) []string {
	out := make([]string, 0, len(rows))
	prevBlank := false
	for _, r := range rows {
		blank := isBlankRow(r)
		if blank && prevBlank {
			continue
		}
		out = append(out, r)
		prevBlank = blank
	}
	return out
}

// isBlankRow reports whether a row is visually empty (empty string or only ANSI escapes).
func isBlankRow(s string) bool {
	return strings.TrimSpace(ansiEscRe.ReplaceAllString(s, "")) == ""
}

// subAgentDisplay tracks per-agent display state for live TUI rendering.
type subAgentDisplay struct {
	task         string    // task label (first ~40 chars of the task description)
	status       string    // current activity (tool name or text snippet)
	done         bool
	mode         string    // "explore" or "implement"
	toolCount    int       // number of tool calls executed
	startTime    time.Time // when this sub-agent started
	completedAt  time.Time // when this sub-agent finished (zero if still running)
	inputTokens  int       // total input tokens consumed
	outputTokens int       // total output tokens consumed
	failed       bool      // true if the sub-agent failed
	replacedBy   string    // ID of the retry agent that supersedes this one (empty if not replaced)
}

// toolGroupOverflowThreshold is the entry count above which tool groups collapse middle entries.
const toolGroupOverflowThreshold = 6

// toolGroupShowEdge is the number of entries shown at each end when collapsing.
const toolGroupShowEdge = 3

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

	// Stable ordering: explore first, then implement, then other.
	modeOrder := []string{"explore", "implement"}
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

func (a *App) buildInputRows() []string {
	sep := strings.Repeat("─", a.width)

	// Approval mode: animated yellow gradient borders + centered message
	if a.awaitingApproval {
		t := time.Since(a.approvalPauseStart)
		color := approvalGradientColor(t)
		shortMsg := fmt.Sprintf("Allow %s? [y/n]", a.approvalSummary)
		if visibleWidth(shortMsg) > a.width {
			shortMsg = truncateVisual(shortMsg, a.width)
		}
		shortPad := (a.width - visibleWidth(shortMsg)) / 2
		if shortPad < 0 {
			shortPad = 0
		}
		detail := a.approvalDesc
		if detail == a.approvalSummary {
			detail = ""
		}
		approvalRows := []string{sep}
		approvalRows = append(approvalRows, fmt.Sprintf("%s%s%s[0m", color, strings.Repeat(" ", shortPad), shortMsg))
		if detail != "" {
			if visibleWidth(detail) > a.width {
				detail = truncateVisual(detail, a.width)
			}
			detailPad := (a.width - visibleWidth(detail)) / 2
			if detailPad < 0 {
				detailPad = 0
			}
			approvalRows = append(approvalRows, fmt.Sprintf("[2m%s%s[0m", strings.Repeat(" ", detailPad), detail))
		}
		approvalRows = append(approvalRows, sep)
		return approvalRows
	}

	rows := []string{sep}

	// Config editor mode replaces input area
	if a.cfgActive {
		rows = append(rows, a.buildConfigRows()...)
		rows = append(rows, sep)
		return rows
	}

	// Menu mode replaces input area
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
		indicator := fmt.Sprintf("(%d->%d / %d)", first, last, total)
		rows = append(rows, fmt.Sprintf("\033[2m%s\033[0m", truncateWithEllipsis(indicator, w)))
		if a.menuModels != nil {
			hints := "←/→ sort column  Tab flip order  Enter select  Esc close"
			rows = append(rows, fmt.Sprintf("\033[2m%s\033[0m", truncateWithEllipsis(hints, w)))
		}
		rows = append(rows, sep)
		return rows
	}

	if a.promptLabel != "" {
		rows = append(rows, fmt.Sprintf("\033[33;1m%s\033[0m", a.promptLabel))
	}

	vlines := getVisualLines(a.input, a.cursor, a.width)
	for i, vl := range vlines {
		line := string(a.input[vl.start : vl.start+vl.length])
		if i == 0 {
			line = promptPrefix + line
		}
		rows = append(rows, line)
	}

	rows = append(rows, sep)

	// Ctrl+C / ESC hint (below separator, above status)
	if a.ctrlCHint {
		if a.agentRunning {
			rows = append(rows, fmt.Sprintf("\033[1;38;5;%dmPress Ctrl-C again to stop the agent\033[0m", 4))
		} else {
			rows = append(rows, fmt.Sprintf("\033[1;38;5;%dmPress Ctrl-C again to exit\033[0m", 4))
		}
	}
	if a.escHint {
		rows = append(rows, fmt.Sprintf("\033[1;38;5;%dmPress ESC again to stop the agent\033[0m", 4))
	}

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
		// Line 1: branch: <name> -del/+add ↓behind↑ahead $cost [progress]
		branchLabel := ""
		branchTextWidth := 0
		if a.status.Branch != "" {
			branchLabel = "\033[2mbranch: " + a.status.Branch + "\033[0m"
			branchTextWidth = 8 + len(a.status.Branch) // "branch: " + name
		}
		diffLabel := ""
		diffTextWidth := 0
		if a.status.DiffDel > 0 || a.status.DiffAdd > 0 {
			delStr := fmt.Sprintf("-%d", a.status.DiffDel)
			addStr := fmt.Sprintf("+%d", a.status.DiffAdd)
			// red for deletions, green for additions, dim
			diffLabel = " \033[2;31m" + delStr + "\033[0m\033[2m/\033[0m\033[2;32m" + addStr + "\033[0m"
			diffTextWidth = 1 + len(delStr) + 1 + len(addStr) // space + del + "/" + add
		}
		commitLabel := ""
		commitTextWidth := 0
		if a.status.HasUpstream {
			commitStr := fmt.Sprintf(" ↓%d↑%d", a.status.Behind, a.status.Ahead)
			commitLabel = "\033[2m" + commitStr + "\033[0m"
			commitTextWidth = uniseg.StringWidth(commitStr)
		}
		costLabel := ""
		costTextWidth := 0
		if a.sessionCostUSD > 0 {
			costStr := formatCost(a.sessionCostUSD)
			costLabel = " \033[2m" + costStr + "\033[0m"
			costTextWidth = 1 + len(costStr)
		}
		contextTokens := a.lastInputTokens + len(a.input)/charsPerToken
		contextWindow := 200000
		if m := findModelByID(a.models, a.config.resolveActiveModel(a.models)); m != nil {
			contextWindow = m.ContextWindow
		}
		bar := progressBar(contextTokens, contextWindow)
		barWidth := 3
		padding := a.width - branchTextWidth - diffTextWidth - commitTextWidth - costTextWidth - barWidth - 1
		if padding < 0 {
			padding = 0
		}
		rows = append(rows, branchLabel+diffLabel+commitLabel+costLabel+strings.Repeat(" ", padding)+bar+" ")

		// Debug mode: show trace file path
		if a.traceFilePath != "" {
			relPath := a.traceFilePath
			if a.repoRoot != "" {
				if r, err := filepath.Rel(a.repoRoot, a.traceFilePath); err == nil {
					relPath = r
				}
			}
			rows = append(rows, "\033[2mdebug: "+relPath+"\033[0m\033[K")
		}

		// Line 2: container status (always shown when we have status text)
		if a.containerStatusText != "" {
			style := "\033[2m" // dim
			if a.containerErr != nil {
				style = "\033[31m" // red
			}
			rows = append(rows, style+"container: "+a.containerStatusText+"\033[0m\033[K")
		}

		// Line 3: worktree: <name> (only when actually in a worktree)
		if a.status.WorktreeName != "" && a.isInWorktree() {
			rows = append(rows, "\033[2mworktree: "+a.status.WorktreeName+"\033[0m\033[K")
		}
	}

	return rows
}

func (a *App) positionCursor(buf *strings.Builder) {
	s := a.scrollShift
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
			buf.WriteString("\033[?25h")
			buf.WriteString(fmt.Sprintf("\033[%d;%dH", fieldRow-s, col+1))
		} else {
			buf.WriteString("\033[?25l")
			buf.WriteString(fmt.Sprintf("\033[%d;1H", a.sepRow+1-s))
		}
		return
	}
	if a.menuActive && len(a.menuLines) > 0 {
		// Menu between separators — hide cursor
		buf.WriteString("\033[?25l")
		buf.WriteString(fmt.Sprintf("\033[%d;1H", a.sepRow+1-s))
		return
	}
	if a.awaitingApproval {
		buf.WriteString("\033[?25l")
		buf.WriteString(fmt.Sprintf("\033[%d;1H", a.sepRow+1-s))
		return
	}
	buf.WriteString("\033[?25h")
	curLine, curCol := cursorVisualPos(a.input, a.cursor, a.width)
	buf.WriteString(fmt.Sprintf("\033[%d;%dH", a.inputStartRow+curLine-s, curCol+1))
}

func (a *App) render() {
	if a.headless {
		return
	}
	blockRows := a.buildBlockRows()

	a.sepRow = len(blockRows) + 1
	a.inputStartRow = a.sepRow + 1
	if a.promptLabel != "" {
		a.inputStartRow++
	}

	inputRows := a.buildInputRows()
	allRows := append(blockRows, inputRows...)
	totalRows := len(allRows)

	th := getTerminalHeight()
	newScrollShift := 0
	if totalRows > th {
		newScrollShift = totalRows - th
	}

	var buf strings.Builder

	if newScrollShift > 0 && a.scrollShift > 0 && newScrollShift >= a.scrollShift {
		// Content overflows and grew or stayed same: write only visible portion.
		// Scroll terminal down if content grew, then overwrite visible rows.
		if extra := newScrollShift - a.scrollShift; extra > 0 {
			buf.WriteString(fmt.Sprintf("\033[%d;1H", th))
			for i := 0; i < extra; i++ {
				buf.WriteString("\r\n")
			}
		}
		visibleRows := allRows[newScrollShift:]
		writeRows(&buf, visibleRows, 1)
	} else {
		// No overflow, or content shrank: write from top.
		if a.scrollShift > 0 {
			buf.WriteString("\033[H\033[2J\033[3J") // clear screen + scrollback
		}
		writeRows(&buf, allRows, 1)
	}

	buf.WriteString("\033[0m\033[J") // clear from cursor to end of screen

	a.prevRowCount = totalRows
	a.scrollShift = newScrollShift

	a.positionCursor(&buf)
	os.Stdout.WriteString(buf.String())
}

// renderFull clears the visible screen and scrollback, then does a full render.
// Use on resize (SIGWINCH) for an artifact-free re-render.
func (a *App) renderFull() {
	if a.headless {
		return
	}
	a.scrollShift = 0 // reset so render() writes from top
	os.Stdout.WriteString("\033[?25l\033[H\033[2J\033[3J") // hide cursor, clear screen + scrollback
	a.render() // render() → positionCursor() restores cursor visibility
}

func (a *App) renderInput() {
	if a.headless {
		return
	}
	inputRows := a.buildInputRows()
	totalRows := a.sepRow - 1 + len(inputRows)
	th := getTerminalHeight()

	newScrollShift := 0
	if totalRows > th {
		newScrollShift = totalRows - th
	}

	// If content shrank and we need to un-scroll, do a full render
	if newScrollShift < a.scrollShift {
		a.render()
		return
	}

	// Compute screen position of sepRow using current scroll state
	screenSepRow := a.sepRow - a.scrollShift
	if screenSepRow < 1 {
		a.render()
		return
	}

	var buf strings.Builder
	writeRows(&buf, inputRows, screenSepRow)
	buf.WriteString("\033[0m\033[J") // clear remaining lines

	a.scrollShift = newScrollShift
	a.prevRowCount = totalRows

	a.positionCursor(&buf)
	os.Stdout.WriteString(buf.String())
}

