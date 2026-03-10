package main

import (
	"context"
	"fmt"
	"strings"

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

// buildConversationTree retrieves the full subtree for the current
// conversation and renders it as a text tree. Returns empty string if
// there is no active conversation.
func (a *App) buildConversationTree(ctx context.Context) (string, error) {
	if a.langdagClient == nil {
		return "", fmt.Errorf("no API client available")
	}
	if a.agentNodeID == "" {
		return "", fmt.Errorf("no active conversation")
	}

	// Walk up to find the root.
	ancestors, err := a.langdagClient.GetAncestors(ctx, a.agentNodeID)
	if err != nil {
		return "", fmt.Errorf("GetAncestors: %w", err)
	}
	var rootID string
	if len(ancestors) > 0 {
		rootID = ancestors[0].ID
	} else {
		rootID = a.agentNodeID
	}

	// Get full subtree from root.
	nodes, err := a.langdagClient.GetSubtree(ctx, rootID)
	if err != nil {
		return "", fmt.Errorf("GetSubtree: %w", err)
	}
	if len(nodes) == 0 {
		return "", fmt.Errorf("empty conversation tree")
	}

	return a.renderTree(nodes, rootID), nil
}

// renderTree builds a text tree from a flat list of nodes.
// The main conversation spine (user/assistant) renders flat.
// Only tool_call/tool_result nodes are indented. Genuine branches
// (multiple children) get tree connectors.
func (a *App) renderTree(nodes []*types.Node, rootID string) string {
	byID := make(map[string]*types.Node, len(nodes))
	children := make(map[string][]string)
	for _, n := range nodes {
		byID[n.ID] = n
		if n.ParentID != "" {
			children[n.ParentID] = append(children[n.ParentID], n.ID)
		}
	}

	var b strings.Builder
	var totalCost float64

	// branchPrefix: indentation from genuine branches (multiple children).
	// Within that prefix, tool nodes get an extra "  " indent.
	var walk func(id, branchPrefix string, inBranch, isLast bool)
	walk = func(id, branchPrefix string, inBranch, isLast bool) {
		n := byID[id]
		if n == nil {
			return
		}

		totalCost += a.nodeCost(n)
		isToolNode := n.NodeType == types.NodeTypeToolCall || n.NodeType == types.NodeTypeToolResult

		if line := a.formatTreeNode(n); line != "" {
			prefix := branchPrefix
			if inBranch {
				if isLast {
					prefix += "└─ "
				} else {
					prefix += "├─ "
				}
			} else if isToolNode {
				prefix += "  "
			}
			b.WriteString(prefix + line + "\n")
		}

		// Compute prefix for children within this branch.
		childBranchPrefix := branchPrefix
		if inBranch {
			if isLast {
				childBranchPrefix += "   "
			} else {
				childBranchPrefix += "│  "
			}
		}

		kids := children[id]
		if len(kids) > 1 {
			// Genuine branch — use tree connectors for children.
			for i, kid := range kids {
				walk(kid, childBranchPrefix, true, i == len(kids)-1)
			}
		} else if len(kids) == 1 {
			// Linear — continue flat.
			walk(kids[0], childBranchPrefix, false, true)
		}
	}

	walk(rootID, "", false, true)

	if totalCost > 0 {
		b.WriteString(fmt.Sprintf("\nTotal: %s", formatCost(totalCost)))
	}

	return b.String()
}

// formatTreeNode returns a one-line summary for a node.
// Returns empty string for node types we want to skip.
func (a *App) formatTreeNode(n *types.Node) string {
	switch n.NodeType {
	case types.NodeTypeUser:
		return "\033[1mYou:\033[0m " + truncate(firstLine(n.Content), 80)

	case types.NodeTypeAssistant:
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
		preview := truncate(firstLine(n.Content), 60)
		if preview != "" {
			label += " " + preview
		}
		return label

	case types.NodeTypeToolCall:
		// Show tool name from content (which is typically JSON with "name" field).
		name := extractToolName(n.Content)
		if name != "" {
			return "\033[33m⚙ " + name + "\033[0m"
		}
		return "\033[33m⚙ tool_call\033[0m"

	case types.NodeTypeToolResult:
		status := "\033[32m✓\033[0m"
		if strings.Contains(n.Content, "\"is_error\":true") || strings.Contains(n.Content, "error") {
			status = "\033[31m✗\033[0m"
		}
		return status + " \033[2mresult\033[0m"

	default:
		return ""
	}
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
