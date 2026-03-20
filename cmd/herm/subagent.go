package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"langdag.com/langdag"
	"langdag.com/langdag/types"
)

// defaultSubAgentMaxTurns is the default tool-call cap per sub-agent invocation.
const defaultSubAgentMaxTurns = 15

// defaultMaxAgentDepth is the default maximum nesting depth for sub-agents.
// Depth 1 means the main agent can spawn sub-agents, but sub-agents cannot
// spawn their own sub-agents — matching Claude Code's behavior.
const defaultMaxAgentDepth = 1

// SubAgentTool spawns a sub-agent to handle complex subtasks autonomously.
// Communication is output-only: the sub-agent returns a result string and
// that is the sole information passed back to the caller.
type SubAgentTool struct {
	client           *langdag.Client
	tools            []Tool
	serverTools      []types.ToolDefinition
	model            string
	explorationModel string // cheap model for summarization; empty = use truncation fallback
	maxTurns         int
	maxDepth         int    // maximum nesting depth from this level
	currentDepth     int    // current nesting depth (0 = spawned by main agent)
	workDir          string
	personality      string
	containerImage   string
	snapshot         *projectSnapshot // project snapshot for sub-agent system prompts
	parentEvents     chan<- AgentEvent // set after construction; forwards live events to TUI

	mu         sync.Mutex
	agentNodes map[string]string // agentID → last nodeID (for resume)
}

func NewSubAgentTool(client *langdag.Client, tools []Tool, serverTools []types.ToolDefinition, model string, explorationModel string, maxTurns int, maxDepth int, currentDepth int, workDir string, personality string, containerImage string, snapshot *projectSnapshot) *SubAgentTool {
	if maxTurns <= 0 {
		maxTurns = defaultSubAgentMaxTurns
	}
	if maxDepth <= 0 {
		maxDepth = defaultMaxAgentDepth
	}
	return &SubAgentTool{
		client:           client,
		tools:            tools,
		serverTools:      serverTools,
		model:            model,
		explorationModel: explorationModel,
		maxTurns:         maxTurns,
		maxDepth:         maxDepth,
		currentDepth:     currentDepth,
		workDir:          workDir,
		personality:      personality,
		containerImage:   containerImage,
		snapshot:         snapshot,
		agentNodes:       make(map[string]string),
	}
}

func (t *SubAgentTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        "agent",
		Description: "Spawn a sub-agent to handle a complex subtask. The sub-agent has its own context window and communicates only via its output — no shared memory. Use for: multi-step research, implementation tasks, debugging sessions, or any work that would consume too much of your context. You can resume a previous sub-agent by passing its agent_id with a new task.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"task": {
					"type": "string",
					"description": "A clear description of the task for the sub-agent to complete"
				},
				"agent_id": {
					"type": "string",
					"description": "Optional: ID of a previous sub-agent to resume. The sub-agent continues from where it left off with its full context preserved."
				}
			},
			"required": ["task"]
		}`),
	}
}

func (t *SubAgentTool) RequiresApproval(_ json.RawMessage) bool {
	return false
}

type subAgentInput struct {
	Task    string `json:"task"`
	AgentID string `json:"agent_id,omitempty"`
}

// forward sends a sub-agent event to the parent's event channel if set.
func (t *SubAgentTool) forward(e AgentEvent) {
	if t.parentEvents == nil {
		return
	}
	select {
	case t.parentEvents <- e:
	default:
		debugLog("sub-agent event dropped: parent channel full (type=%d)", e.Type)
	}
}

// saveNodeID stores the last nodeID for a sub-agent so it can be resumed.
func (t *SubAgentTool) saveNodeID(agentID, nodeID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.agentNodes[agentID] = nodeID
}

// loadNodeID retrieves the last nodeID for a sub-agent.
func (t *SubAgentTool) loadNodeID(agentID string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	nodeID, ok := t.agentNodes[agentID]
	return nodeID, ok
}

// buildSubAgentTools returns the tools available to the sub-agent.
// If the current depth allows further nesting, includes a new SubAgentTool
// at the next depth level. Otherwise the sub-agent cannot spawn children.
func (t *SubAgentTool) buildSubAgentTools() []Tool {
	tools := make([]Tool, len(t.tools))
	copy(tools, t.tools)

	nextDepth := t.currentDepth + 1
	if nextDepth < t.maxDepth {
		// Sub-agent is allowed to spawn its own sub-agents.
		child := NewSubAgentTool(t.client, t.tools, t.serverTools, t.model, t.explorationModel, t.maxTurns, t.maxDepth, nextDepth, t.workDir, t.personality, t.containerImage, t.snapshot)
		child.parentEvents = t.parentEvents
		tools = append(tools, child)
	}

	return tools
}

// Execute runs a sub-agent synchronously, drains its events, and returns the collected text output.
func (t *SubAgentTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in subAgentInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid agent input: %w", err)
	}
	if in.Task == "" {
		return "", fmt.Errorf("task is required")
	}

	// Determine if we're resuming a previous sub-agent.
	var parentNodeID string
	if in.AgentID != "" {
		nodeID, ok := t.loadNodeID(in.AgentID)
		if !ok {
			return "", fmt.Errorf("unknown agent_id %q: no previous sub-agent with that ID", in.AgentID)
		}
		parentNodeID = nodeID
	}

	// Build the sub-agent's tool set (may include nested agent tool if depth allows).
	subTools := t.buildSubAgentTools()

	// Build a lean sub-agent system prompt: skips communication, personality,
	// skills, and uses a compact role section instead of the full orchestrator framing.
	systemPrompt := buildSubAgentSystemPrompt(subTools, t.serverTools, t.workDir, t.containerImage, t.snapshot)

	agent := NewAgent(t.client, subTools, t.serverTools, systemPrompt, t.model, 0)
	agentID := agent.ID()

	// Notify the TUI that a sub-agent is starting, with its task label.
	t.forward(AgentEvent{Type: EventSubAgentStart, AgentID: agentID, Task: in.Task})

	// Run the sub-agent in a goroutine and drain events.
	done := make(chan struct{})
	var textParts []string

	go func() {
		defer close(done)
		agent.Run(ctx, in.Task, parentNodeID)
	}()

	// Track sub-agent token usage for reporting in the tool result.
	var totalInputTokens, totalOutputTokens int

	turns := 0
	for event := range agent.Events() {
		switch event.Type {
		case EventTextDelta:
			textParts = append(textParts, event.Text)
			t.forward(AgentEvent{Type: EventSubAgentDelta, AgentID: agentID, Text: event.Text})
		case EventToolCallStart:
			turns++
			if turns > t.maxTurns {
				agent.Cancel()
			}
			t.forward(AgentEvent{Type: EventSubAgentStatus, AgentID: agentID, Text: fmt.Sprintf("tool: %s", event.ToolName)})
		case EventUsage:
			if event.Usage != nil {
				totalInputTokens += event.Usage.InputTokens + event.Usage.CacheReadInputTokens
				totalOutputTokens += event.Usage.OutputTokens
			}
			// Forward usage events so sub-agent costs are tracked.
			t.forward(event)
		case EventDone:
			t.forward(AgentEvent{
				Type:    EventSubAgentStatus,
				AgentID: agentID,
				Text:    "done",
				Usage: &types.Usage{
					InputTokens:  totalInputTokens,
					OutputTokens: totalOutputTokens,
				},
				Task: fmt.Sprintf("turns:%d/%d", turns, t.maxTurns),
			})
			// Save the nodeID for potential resume.
			if event.NodeID != "" {
				t.saveNodeID(agentID, event.NodeID)
			}
			// Wait for the goroutine to finish.
			<-done
			result := strings.TrimSpace(strings.Join(textParts, ""))
			if result == "" {
				result = "(sub-agent produced no output)"
			}
			outputPath := t.writeOutputFile(agentID, result)
			summary, usedModel := t.summarizeWithModel(ctx, result)
			return formatSubAgentResult(agentID, outputPath, summary, usedModel, totalInputTokens, totalOutputTokens), nil
		case EventError:
			if event.Error != nil && event.Error.Error() != "context canceled" {
				// Continue — errors are informational in the event stream.
			}
		}
	}

	// Channel closed without EventDone (e.g., EventDone was dropped, or agent
	// exited without emitting it). Handle gracefully.
	t.forward(AgentEvent{
		Type:    EventSubAgentStatus,
		AgentID: agentID,
		Text:    "done",
		Usage: &types.Usage{
			InputTokens:  totalInputTokens,
			OutputTokens: totalOutputTokens,
		},
		Task: fmt.Sprintf("turns:%d/%d", turns, t.maxTurns),
	})
	<-done
	result := strings.TrimSpace(strings.Join(textParts, ""))
	if result == "" {
		result = "(sub-agent produced no output)"
	}
	outputPath := t.writeOutputFile(agentID, result)
	summary, usedModel := t.summarizeWithModel(ctx, result)
	return formatSubAgentResult(agentID, outputPath, summary, usedModel, totalInputTokens, totalOutputTokens), nil
}

// formatSubAgentResult builds the tool result string with agent ID, output file
// path, token usage, summary quality indicator, and the summary. The caller can
// use read_file on the output path for full results and see token usage for cost
// awareness. The modelSummary flag indicates whether the summary was generated by
// a model (true) or by byte-truncation (false).
func formatSubAgentResult(agentID, outputPath, summary string, modelSummary bool, inputTokens, outputTokens int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[agent_id: %s]", agentID)
	if outputPath != "" {
		fmt.Fprintf(&b, " [output: %s]", outputPath)
	}
	if inputTokens > 0 || outputTokens > 0 {
		fmt.Fprintf(&b, " [tokens: input=%d output=%d]", inputTokens, outputTokens)
	}
	if modelSummary {
		b.WriteString(" [summary: model]")
	} else if outputPath != "" && len(summary) > subAgentSummaryBytes {
		// Only mark truncated when there was actually more content (output file exists).
		b.WriteString(" [summary: truncated]")
	}
	fmt.Fprintf(&b, "\n\n%s", summary)
	return b.String()
}

// subAgentSummaryBytes is the max bytes for the inline summary in the tool result.
const subAgentSummaryBytes = 500

// summarizeOutput returns the first ~500 bytes of the output, cutting at a line
// boundary. If the output is longer, a note is appended.
func summarizeOutput(s string) string {
	if len(s) <= subAgentSummaryBytes {
		return s
	}
	cut := s[:subAgentSummaryBytes]
	if i := strings.LastIndex(cut, "\n"); i > 0 {
		cut = cut[:i]
	}
	return cut + "\n[... full output in file above]"
}

// summarizeWithModelMaxChars is the max characters of sub-agent output to send
// to the exploration model for summarization.
const summarizeWithModelMaxChars = 4000

// summarizeWithModelPrompt is the prompt sent to the exploration model for
// generating a structured summary of a sub-agent's output.
const summarizeWithModelPrompt = `Summarize the key findings from this sub-agent's output in 3-5 bullet points. Focus on facts, decisions, and actionable information. Skip preamble. Output only the bullet points.

--- SUB-AGENT OUTPUT ---
`

// summarizeWithModel calls the exploration model to generate a structured
// summary of a sub-agent's output. Falls back to summarizeOutput() if the
// model is not set or the call fails. Returns the summary and whether the
// model was used (true) or truncation fallback (false).
func (t *SubAgentTool) summarizeWithModel(ctx context.Context, output string) (string, bool) {
	// Short outputs don't need model summarization.
	if len(output) <= subAgentSummaryBytes {
		return output, false
	}

	// No exploration model configured — fall back to truncation.
	if t.explorationModel == "" || t.client == nil {
		return summarizeOutput(output), false
	}

	// Truncate the input to the model to keep costs low.
	modelInput := output
	if len(modelInput) > summarizeWithModelMaxChars {
		modelInput = modelInput[:summarizeWithModelMaxChars]
		if i := strings.LastIndex(modelInput, "\n"); i > 0 {
			modelInput = modelInput[:i]
		}
		modelInput += "\n[... truncated]"
	}

	summary, err := callLLMDirect(ctx, t.client, t.explorationModel, summarizeWithModelPrompt+modelInput)
	if err != nil {
		debugLog("summarizeWithModel failed: %v", err)
		return summarizeOutput(output), false
	}

	summary = strings.TrimSpace(summary)
	if summary == "" {
		return summarizeOutput(output), false
	}

	return summary, true
}

// agentOutputDir returns the directory for sub-agent output files.
func agentOutputDir(workDir string) string {
	return filepath.Join(workDir, ".herm", "agents")
}

// writeOutputFile writes the full sub-agent output to .herm/agents/<agentID>.md.
// Returns the file path on success, or empty string on failure (non-fatal).
func (t *SubAgentTool) writeOutputFile(agentID, output string) string {
	dir := agentOutputDir(t.workDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	path := filepath.Join(dir, agentID+".md")
	if err := os.WriteFile(path, []byte(output), 0o644); err != nil {
		return ""
	}
	return path
}

// cleanupAgentOutputDir removes agent output files older than 24 hours.
func cleanupAgentOutputDir(workDir string) {
	dir := agentOutputDir(workDir)
	entries, err := os.ReadDir(dir)
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
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

