package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

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
	client         *langdag.Client
	tools          []Tool
	serverTools    []types.ToolDefinition
	model          string
	maxTurns       int
	maxDepth       int    // maximum nesting depth from this level
	currentDepth   int    // current nesting depth (0 = spawned by main agent)
	workDir        string
	personality    string
	containerImage string
	parentEvents   chan<- AgentEvent // set after construction; forwards live events to TUI

	mu         sync.Mutex
	agentNodes map[string]string // agentID → last nodeID (for resume)
}

func NewSubAgentTool(client *langdag.Client, tools []Tool, serverTools []types.ToolDefinition, model string, maxTurns int, maxDepth int, currentDepth int, workDir string, personality string, containerImage string) *SubAgentTool {
	if maxTurns <= 0 {
		maxTurns = defaultSubAgentMaxTurns
	}
	if maxDepth <= 0 {
		maxDepth = defaultMaxAgentDepth
	}
	return &SubAgentTool{
		client:         client,
		tools:          tools,
		serverTools:    serverTools,
		model:          model,
		maxTurns:       maxTurns,
		maxDepth:       maxDepth,
		currentDepth:   currentDepth,
		workDir:        workDir,
		personality:    personality,
		containerImage: containerImage,
		agentNodes:     make(map[string]string),
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
	if t.parentEvents != nil {
		t.parentEvents <- e
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
		child := NewSubAgentTool(t.client, t.tools, t.serverTools, t.model, t.maxTurns, t.maxDepth, nextDepth, t.workDir, t.personality, t.containerImage)
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

	// Build a sub-agent system prompt: reuse buildSystemPrompt with a preamble.
	basePrompt := buildSystemPrompt(subTools, t.serverTools, nil, t.workDir, t.personality, t.containerImage, "")
	systemPrompt := subAgentPreamble + "\n\n" + basePrompt

	agent := NewAgent(t.client, subTools, t.serverTools, systemPrompt, t.model, 0)
	agentID := agent.ID()

	// Run the sub-agent in a goroutine and drain events.
	done := make(chan struct{})
	var textParts []string

	go func() {
		defer close(done)
		agent.Run(ctx, in.Task, parentNodeID)
	}()

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
			// Forward usage events so sub-agent costs are tracked.
			t.forward(event)
		case EventDone:
			t.forward(AgentEvent{Type: EventSubAgentStatus, AgentID: agentID, Text: "done"})
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
			return formatSubAgentResult(agentID, truncateSubAgentOutput(result)), nil
		case EventError:
			if event.Error != nil && event.Error.Error() != "context canceled" {
				// Continue — errors are informational in the event stream.
			}
		}
	}

	// Channel closed without EventDone — shouldn't happen, but handle gracefully.
	<-done
	result := strings.TrimSpace(strings.Join(textParts, ""))
	if result == "" {
		result = "(sub-agent produced no output)"
	}
	return formatSubAgentResult(agentID, truncateSubAgentOutput(result)), nil
}

// formatSubAgentResult prepends the agent ID to the output so the caller
// can reference it for resume.
func formatSubAgentResult(agentID, output string) string {
	return fmt.Sprintf("[agent_id: %s]\n\n%s", agentID, output)
}

// subAgentMaxOutputBytes caps sub-agent output to prevent bloated tool results.
// 30KB is enough for detailed summaries while keeping context manageable.
const subAgentMaxOutputBytes = 30 * 1024

// truncateSubAgentOutput trims output to subAgentMaxOutputBytes, cutting at
// a line boundary and appending a truncation note.
func truncateSubAgentOutput(s string) string {
	if len(s) <= subAgentMaxOutputBytes {
		return s
	}
	// Cut at the last newline before the limit.
	cut := s[:subAgentMaxOutputBytes]
	if i := strings.LastIndex(cut, "\n"); i > 0 {
		cut = cut[:i]
	}
	return cut + "\n[output truncated at 30KB]"
}

const subAgentPreamble = `You are a sub-agent working on a specific task delegated by a parent orchestrator. Focus entirely on completing the assigned task. Be thorough but concise — your output will be returned to the parent agent as a tool result.

Key guidelines:
- Complete the task fully, then provide a clear summary of what you did and found.
- Use tools as needed to accomplish the task.
- Do not ask questions — make reasonable decisions and note any assumptions.
- Keep your final response focused on results, not process narration.`
