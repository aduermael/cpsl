package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"langdag.com/langdag"
	"langdag.com/langdag/types"
)

// SubAgentTool spawns a sub-agent to handle complex subtasks autonomously.
type SubAgentTool struct {
	client       *langdag.Client
	tools        []Tool
	serverTools  []types.ToolDefinition
	model        string
	maxTurns     int
	workDir      string
}

func NewSubAgentTool(client *langdag.Client, tools []Tool, serverTools []types.ToolDefinition, model string, maxTurns int, workDir string) *SubAgentTool {
	if maxTurns <= 0 {
		maxTurns = 15
	}
	return &SubAgentTool{
		client:      client,
		tools:       tools,
		serverTools: serverTools,
		model:       model,
		maxTurns:    maxTurns,
		workDir:     workDir,
	}
}

func (t *SubAgentTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        "agent",
		Description: "Spawn a sub-agent to handle a complex subtask. The sub-agent has its own context window and can use tools independently. Use for: multi-step research, implementation tasks, debugging sessions, or any work that would consume too much of your context. The sub-agent runs to completion and returns a summary of its work.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"task": {
					"type": "string",
					"description": "A clear description of the task for the sub-agent to complete"
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
	Task string `json:"task"`
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

	// Build a sub-agent system prompt: reuse buildSystemPrompt with a preamble.
	basePrompt := buildSystemPrompt(t.tools, t.serverTools, nil, t.workDir)
	systemPrompt := subAgentPreamble + "\n\n" + basePrompt

	agent := NewAgent(t.client, t.tools, t.serverTools, systemPrompt, t.model)

	// Run the sub-agent in a goroutine and drain events.
	done := make(chan struct{})
	var textParts []string

	go func() {
		defer close(done)
		agent.Run(ctx, in.Task, "")
	}()

	turns := 0
	for event := range agent.Events() {
		switch event.Type {
		case EventTextDelta:
			textParts = append(textParts, event.Text)
		case EventToolCallStart:
			turns++
			if turns > t.maxTurns {
				agent.Cancel()
			}
		case EventDone:
			// Wait for the goroutine to finish.
			<-done
			result := strings.TrimSpace(strings.Join(textParts, ""))
			if result == "" {
				return "(sub-agent produced no output)", nil
			}
			return result, nil
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
		return "(sub-agent produced no output)", nil
	}
	return result, nil
}

const subAgentPreamble = `You are a sub-agent working on a specific task delegated by a parent orchestrator. Focus entirely on completing the assigned task. Be thorough but concise — your output will be returned to the parent agent as a tool result.

Key guidelines:
- Complete the task fully, then provide a clear summary of what you did and found.
- Use tools as needed to accomplish the task.
- Do not ask questions — make reasonable decisions and note any assumptions.
- Keep your final response focused on results, not process narration.`
