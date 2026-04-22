// compact.go implements conversation compaction, which summarizes old history
// and creates a fresh conversation branch to reclaim context window space.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"langdag.com/langdag"
	"langdag.com/langdag/types"
)

// compactThresholdFraction is the fraction of context window at which
// auto-compaction triggers. Must be higher than clearThresholdFraction
// since compaction is a more aggressive measure.
const compactThresholdFraction = 0.95

// compactKeepRecent is the number of most-recent nodes to preserve
// verbatim after compaction. These are copied after the summary.
const compactKeepRecent = 6

const compactSummaryPrompt = `Summarize the following conversation between a user and an AI coding assistant. Focus on:
1. What task/goal the user is working on
2. Key decisions made and why
3. Current state: what has been done, what files were modified
4. Any important context the assistant would need to continue the work
5. Pending tasks or plan steps not yet completed
6. Errors encountered and their resolution status

Be concise but complete. Write in second person ("you" = the assistant). Output only the summary, no preamble.`

// CompactResult holds the outcome of a compaction operation.
type CompactResult struct {
	NewNodeID     string // leaf node ID of the compacted conversation
	Summary       string // the generated summary text
	OriginalNodes int    // how many nodes were in the original chain
	KeptNodes     int    // how many recent nodes were preserved verbatim
}

// compactConversationOptions is the parameter bundle for compactConversation.
type compactConversationOptions struct {
	client    *langdag.Client
	nodeID    string
	model     string
	focusHint string
}

// compactConversation summarizes old conversation history and creates a new
// conversation branch from the summary + recent nodes. The summary is generated
// using the specified model (typically a cheap/fast exploration model).
//
// focusHint is an optional string appended to the summary prompt to guide
// what the summary should emphasize (e.g., "focus on the auth changes").
func compactConversation(ctx context.Context, opts compactConversationOptions) (*CompactResult, error) {
	client, nodeID, model, focusHint := opts.client, opts.nodeID, opts.model, opts.focusHint
	ancestors, err := client.GetAncestors(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("get ancestors: %w", err)
	}
	if len(ancestors) <= compactKeepRecent {
		return nil, fmt.Errorf("conversation too short to compact (%d nodes)", len(ancestors))
	}

	// Split: old nodes to summarize, recent nodes to keep verbatim.
	splitIdx := len(ancestors) - compactKeepRecent
	oldNodes := ancestors[:splitIdx]
	recentNodes := ancestors[splitIdx:]

	// Extract system prompt from original root.
	systemPrompt := ""
	if len(ancestors) > 0 {
		systemPrompt = ancestors[0].SystemPrompt
	}

	// Build a transcript of the old conversation and summarize it.
	transcript := buildTranscript(oldNodes)

	prompt := compactSummaryPrompt
	if focusHint != "" {
		prompt += "\n\nFocus especially on: " + focusHint
	}
	prompt += "\n\n--- CONVERSATION ---\n" + transcript

	summary, err := callLLMDirect(ctx, callLLMDirectOptions{client: client, model: model, prompt: prompt})
	if err != nil {
		return nil, fmt.Errorf("summarize: %w", err)
	}

	// Create new conversation: root → (copies of recent nodes).
	storage := client.Storage()
	rootID := uuid.New().String()
	summaryContent := "[Conversation compacted — summary of prior " +
		fmt.Sprintf("%d", len(oldNodes)) + " messages]\n\n" + summary

	rootNode := &types.Node{
		ID:           rootID,
		RootID:       rootID,
		Sequence:     0,
		NodeType:     types.NodeTypeUser,
		Content:      summaryContent,
		Title:        "Compacted: " + truncate(truncateOptions{s: summary, max: 40}),
		SystemPrompt: systemPrompt,
		CreatedAt:    time.Now(),
	}
	if err := storage.CreateNode(ctx, rootNode); err != nil {
		return nil, fmt.Errorf("create root: %w", err)
	}

	// Copy recent nodes as children of the new root, preserving content and types.
	parentID := rootID
	var lastNodeID string
	for i, n := range recentNodes {
		newID := uuid.New().String()
		copied := &types.Node{
			ID:                  newID,
			ParentID:            parentID,
			RootID:              rootID,
			Sequence:            i + 1,
			NodeType:            n.NodeType,
			Content:             n.Content,
			Provider:            n.Provider,
			Model:               n.Model,
			TokensIn:            n.TokensIn,
			TokensOut:           n.TokensOut,
			TokensCacheRead:     n.TokensCacheRead,
			TokensCacheCreation: n.TokensCacheCreation,
			TokensReasoning:     n.TokensReasoning,
			LatencyMs:           n.LatencyMs,
			CreatedAt:           time.Now(),
		}
		if err := storage.CreateNode(ctx, copied); err != nil {
			// Best effort — return what we have.
			break
		}
		parentID = newID
		lastNodeID = newID
	}

	if lastNodeID == "" {
		lastNodeID = rootID
	}

	return &CompactResult{
		NewNodeID:     lastNodeID,
		Summary:       summary,
		OriginalNodes: len(ancestors),
		KeptNodes:     len(recentNodes),
	}, nil
}

// callLLMDirectOptions is the parameter bundle for callLLMDirect.
type callLLMDirectOptions struct {
	client *langdag.Client
	model  string
	prompt string
}

// callLLMDirect makes a single LLM call without creating conversation nodes.
func callLLMDirect(ctx context.Context, opts callLLMDirectOptions) (string, error) {
	resp, err := opts.client.Provider().Complete(ctx, &types.CompletionRequest{
		Model: opts.model,
		Messages: []types.Message{
			{Role: "user", Content: json.RawMessage(fmt.Sprintf("%q", opts.prompt))},
		},
		MaxTokens: 2048,
	})
	if err != nil {
		return "", err
	}

	var parts []string
	for _, block := range resp.Content {
		if block.Type == "text" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, ""), nil
}

// buildTranscript formats conversation nodes as a readable transcript.
func buildTranscript(nodes []*types.Node) string {
	var b strings.Builder
	for _, n := range nodes {
		switch {
		case n.NodeType == types.NodeTypeUser && isToolResultContent(n.Content):
			results := parseToolResults(n.Content)
			for _, r := range results {
				status := "ok"
				if r.isError {
					status = "error"
				}
				b.WriteString(fmt.Sprintf("[Tool result: %s]\n", status))
			}
		case n.NodeType == types.NodeTypeUser:
			b.WriteString("User: " + truncate(truncateOptions{s: n.Content, max: 500}) + "\n\n")
		case n.NodeType == types.NodeTypeAssistant:
			text := extractAssistantText(n.Content)
			_, tools := parseAssistantContent(n.Content)
			if text != "" {
				b.WriteString("Assistant: " + truncate(truncateOptions{s: text, max: 500}) + "\n")
			}
			for _, t := range tools {
				b.WriteString(fmt.Sprintf("  [called %s]\n", t.name))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}
