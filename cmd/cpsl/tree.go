package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"langdag.com/langdag/types"
)

// nodeCost computes the USD cost for a single node using its stored token
// counts and the app's model catalog. This mirrors computeCost but takes
// token counts directly from a types.Node instead of types.Usage.
func (a *App) nodeCost(n *types.Node) float64 {
	if n.Model == "" {
		return 0
	}
	usage := types.Usage{
		InputTokens:              n.TokensIn,
		OutputTokens:             n.TokensOut,
		CacheReadInputTokens:     n.TokensCacheRead,
		CacheCreationInputTokens: n.TokensCacheCreation,
	}
	return computeCost(a.models, n.Model, usage)
}

// buildConversationTree retrieves the active conversation path (root to
// current node) and renders it as flat text. User/Assistant at column 0,
// tool calls/results indented beneath their assistant.
func (a *App) buildConversationTree(ctx context.Context) (string, error) {
	if a.langdagClient == nil {
		return "", fmt.Errorf("no API client available")
	}
	if a.agentNodeID == "" {
		return "", fmt.Errorf("no active conversation")
	}

	ancestors, err := a.langdagClient.GetAncestors(ctx, a.agentNodeID)
	if err != nil {
		return "", fmt.Errorf("GetAncestors: %w", err)
	}
	if len(ancestors) == 0 {
		return "", fmt.Errorf("empty conversation tree")
	}

	return a.renderTree(ancestors), nil
}

// handleSessionCommand dispatches /session subcommands.
func (a *App) handleSessionCommand(input string) {
	fields := strings.Fields(input)
	// bare /session — show active conversation tree
	if len(fields) == 1 {
		tree, err := a.buildConversationTree(context.Background())
		if err != nil {
			a.messages = append(a.messages, chatMessage{kind: msgError, content: err.Error()})
		} else {
			a.messages = append(a.messages, chatMessage{kind: msgInfo, content: tree})
		}
		a.render()
		return
	}

	sub := fields[1]
	switch sub {
	case "list":
		a.showConversationList()
	case "show":
		if len(fields) < 3 {
			a.messages = append(a.messages, chatMessage{kind: msgError, content: "Usage: /session show <id>"})
			a.render()
			return
		}
		a.showConversationByID(fields[2])
	case "load":
		if len(fields) < 3 {
			a.messages = append(a.messages, chatMessage{kind: msgError, content: "Usage: /session load <id>"})
			a.render()
			return
		}
		a.loadConversation(fields[2])
	default:
		a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Unknown subcommand: %s", sub)})
		a.render()
	}
}

// showConversationList opens a scrollable menu of all conversations.
func (a *App) showConversationList() {
	if a.langdagClient == nil {
		a.messages = append(a.messages, chatMessage{kind: msgError, content: "No API client available."})
		a.render()
		return
	}
	roots, err := a.langdagClient.ListConversations(context.Background())
	if err != nil {
		a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("ListConversations: %v", err)})
		a.render()
		return
	}
	if len(roots) == 0 {
		a.messages = append(a.messages, chatMessage{kind: msgInfo, content: "No conversations found."})
		a.render()
		return
	}

	var lines []string
	for _, r := range roots {
		title := r.Title
		if title == "" {
			title = truncate(firstLine(r.Content), 50)
		}
		if title == "" {
			title = "(empty)"
		}
		age := formatRelativeTime(r.CreatedAt)
		line := fmt.Sprintf("%s  %s  %s", r.ID[:8], truncate(title, 50), age)
		lines = append(lines, line)
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

		if idx >= 0 && idx < len(roots) {
			selected := roots[idx]
			a.loadConversation(selected.ID)
		}
	}
	a.renderInput()
}

// showConversationByID renders the tree for a specific conversation root.
func (a *App) showConversationByID(id string) {
	if a.langdagClient == nil {
		a.messages = append(a.messages, chatMessage{kind: msgError, content: "No API client available."})
		a.render()
		return
	}
	ctx := context.Background()
	// Get the subtree to find the last node, then get ancestors from it.
	subtree, err := a.langdagClient.GetSubtree(ctx, id)
	if err != nil {
		a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("GetSubtree: %v", err)})
		a.render()
		return
	}
	if len(subtree) == 0 {
		a.messages = append(a.messages, chatMessage{kind: msgError, content: "Conversation not found."})
		a.render()
		return
	}
	// Subtree is ordered by sequence ASC; last element is the deepest node.
	// Get ancestors from that leaf to show the full linear path.
	leaf := subtree[len(subtree)-1]
	ancestors, err := a.langdagClient.GetAncestors(ctx, leaf.ID)
	if err != nil {
		a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("GetAncestors: %v", err)})
		a.render()
		return
	}
	tree := a.renderTree(ancestors)
	a.messages = append(a.messages, chatMessage{kind: msgInfo, content: tree})
	a.render()
}

// loadConversation loads a conversation, setting the current node to its last node.
func (a *App) loadConversation(id string) {
	if a.langdagClient == nil {
		a.messages = append(a.messages, chatMessage{kind: msgError, content: "No API client available."})
		a.render()
		return
	}
	ctx := context.Background()
	subtree, err := a.langdagClient.GetSubtree(ctx, id)
	if err != nil {
		a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("GetSubtree: %v", err)})
		a.render()
		return
	}
	if len(subtree) == 0 {
		a.messages = append(a.messages, chatMessage{kind: msgError, content: "Conversation not found."})
		a.render()
		return
	}

	leaf := subtree[len(subtree)-1]
	a.agentNodeID = leaf.ID
	a.streamingText = ""
	a.pendingToolCall = ""
	a.messages = nil
	a.scratchpad.Clear()

	// Rebuild chat messages from ancestors so the user sees the conversation.
	ancestors, err := a.langdagClient.GetAncestors(ctx, leaf.ID)
	if err != nil {
		a.messages = append(a.messages, chatMessage{kind: msgInfo, content: fmt.Sprintf("Loaded conversation (could not render history: %v)", err)})
		a.render()
		return
	}

	a.messages = a.rebuildChatMessages(ancestors)
	a.messages = append(a.messages, chatMessage{kind: msgSuccess, content: fmt.Sprintf("Loaded conversation %s — continuing from node %s", id[:min(8, len(id))], leaf.ID[:min(8, len(leaf.ID))])})
	a.render()
}

// rebuildChatMessages converts a node ancestor chain into displayable chat messages.
func (a *App) rebuildChatMessages(nodes []*types.Node) []chatMessage {
	var msgs []chatMessage
	for _, n := range nodes {
		switch {
		case n.NodeType == types.NodeTypeUser && isToolResultContent(n.Content):
			// Skip tool result nodes — they're internal.
			continue
		case n.NodeType == types.NodeTypeToolCall || n.NodeType == types.NodeTypeToolResult:
			continue
		case n.NodeType == types.NodeTypeAssistant:
			text := extractAssistantText(n.Content)
			if text != "" {
				msgs = append(msgs, chatMessage{kind: msgAssistant, content: text})
			}
		case n.NodeType == types.NodeTypeUser:
			msgs = append(msgs, chatMessage{kind: msgUser, content: n.Content, leadBlank: true})
		}
	}
	return msgs
}

// formatRelativeTime returns a human-readable relative time string.
func formatRelativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1 min ago"
		}
		return fmt.Sprintf("%d mins ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", h)
	case d < 30*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		return t.Format("Jan 2, 2006")
	}
}

// renderTree renders a linear node path. User/Assistant lines are at column 0.
// Tool calls from assistant nodes are merged with subsequent tool results into
// compact "✓ toolname" / "✗ toolname" lines, indented under the assistant.
func (a *App) renderTree(nodes []*types.Node) string {
	var b strings.Builder
	var totalCost float64
	// pendingTools holds tool names from the last assistant's tool_use blocks,
	// keyed by tool_use ID so we can match them with results.
	var pendingTools []toolUseInfo

	for _, n := range nodes {
		totalCost += a.nodeCost(n)

		switch {
		case n.NodeType == types.NodeTypeUser && isToolResultContent(n.Content):
			// Merge tool results with pending tool names from the assistant.
			results := parseToolResults(n.Content)
			for _, r := range results {
				name := "tool"
				for _, pt := range pendingTools {
					if pt.id == r.toolUseID {
						name = pt.name
						break
					}
				}
				status := "\033[32m✓\033[0m"
				if r.isError {
					status = "\033[31m✗\033[0m"
				}
				b.WriteString("  " + status + " " + name + "\n")
			}
			pendingTools = nil

		case n.NodeType == types.NodeTypeToolCall:
			name := extractToolName(n.Content)
			if name == "" {
				name = "tool"
			}
			b.WriteString("  \033[33m⚙ " + name + "\033[0m\n")

		case n.NodeType == types.NodeTypeToolResult:
			status := "\033[32m✓\033[0m"
			if strings.Contains(n.Content, "\"is_error\":true") || strings.Contains(n.Content, "error") {
				status = "\033[31m✗\033[0m"
			}
			b.WriteString("  " + status + " \033[2mresult\033[0m\n")

		case n.NodeType == types.NodeTypeAssistant:
			pendingTools = nil
			label := "\033[1;34mAssistant\033[0m"
			var meta []string
			if n.Model != "" {
				meta = append(meta, shortModel(n.Model))
			}
			if cost := a.nodeCost(n); cost > 0 {
				meta = append(meta, formatCost(cost))
			}
			if n.TokensIn > 0 || n.TokensOut > 0 {
				meta = append(meta, fmt.Sprintf("%dtok in, %dtok out", n.TokensIn+n.TokensCacheRead, n.TokensOut))
			}
			if len(meta) > 0 {
				label += " \033[2m(" + strings.Join(meta, ", ") + ")\033[0m"
			}
			preview, tools := parseAssistantContent(n.Content)
			if preview != "" {
				label += " " + truncate(preview, 60)
			}
			pendingTools = tools
			b.WriteString(label + "\n")

		default:
			pendingTools = nil
			lines, _ := a.formatTreeNode(n)
			for _, line := range lines {
				b.WriteString(line + "\n")
			}
		}
	}

	if totalCost > 0 {
		b.WriteString(fmt.Sprintf("\nTotal: %s", formatCost(totalCost)))
	}

	return b.String()
}

type toolUseInfo struct {
	id   string
	name string
}

type toolResultInfo struct {
	toolUseID string
	isError   bool
}

// formatTreeNode returns display lines for a node.
// Only used for user nodes (real messages) in the default renderTree path.
func (a *App) formatTreeNode(n *types.Node) (lines []string, toolLike bool) {
	switch n.NodeType {
	case types.NodeTypeUser:
		return []string{"\033[1mYou:\033[0m " + truncate(firstLine(n.Content), 80)}, false
	default:
		return nil, false
	}
}

// parseAssistantContent extracts a text preview and tool_use info from
// assistant node content. If content is plain text, returns it as preview.
// If content is a JSON content block array, extracts text and tool info.
func parseAssistantContent(content string) (preview string, tools []toolUseInfo) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || trimmed[0] != '[' {
		return firstLine(trimmed), nil
	}
	var blocks []types.ContentBlock
	if err := json.Unmarshal([]byte(trimmed), &blocks); err != nil {
		return "", nil
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if preview == "" && b.Text != "" {
				preview = firstLine(b.Text)
			}
		case "tool_use":
			name := b.Name
			if name == "" {
				name = "tool"
			}
			tools = append(tools, toolUseInfo{id: b.ID, name: name})
		}
	}
	return preview, tools
}

// extractAssistantText returns the full text from assistant node content
// (not truncated to first line like parseAssistantContent).
func extractAssistantText(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || trimmed[0] != '[' {
		return trimmed
	}
	var blocks []types.ContentBlock
	if err := json.Unmarshal([]byte(trimmed), &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// isToolResultContent returns true if content is a JSON array containing
// tool_result blocks (stored by PromptFrom when sending tool results back).
func isToolResultContent(content string) bool {
	trimmed := strings.TrimSpace(content)
	return len(trimmed) > 0 && trimmed[0] == '[' &&
		strings.Contains(trimmed, `"type":"tool_result"`)
}

// parseToolResults extracts tool result info from a JSON content block array.
func parseToolResults(content string) []toolResultInfo {
	var blocks []types.ContentBlock
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &blocks); err != nil {
		return nil
	}
	var results []toolResultInfo
	for _, b := range blocks {
		if b.Type == "tool_result" {
			results = append(results, toolResultInfo{
				toolUseID: b.ToolUseID,
				isError:   b.IsError,
			})
		}
	}
	return results
}

// shortModel returns a shortened model name for display.
func shortModel(model string) string {
	// Strip common prefixes for brevity.
	for _, prefix := range []string{"anthropic/", "openai/", "google/", "x-ai/"} {
		model = strings.TrimPrefix(model, prefix)
	}
	return model
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// extractToolName tries to pull the tool name from a tool_call node's content.
func extractToolName(content string) string {
	// Quick extraction without full JSON parse — look for "name":"..."
	const marker = `"name":"`
	idx := strings.Index(content, marker)
	if idx < 0 {
		return ""
	}
	start := idx + len(marker)
	end := strings.IndexByte(content[start:], '"')
	if end < 0 {
		return ""
	}
	return content[start : start+end]
}
