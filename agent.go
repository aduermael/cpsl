package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/langdag/langdag/pkg/langdag"
	"github.com/langdag/langdag/pkg/types"
)

// maxToolIterations caps the agent loop to prevent runaway tool calls.
const maxToolIterations = 25

// Tool is the interface that all agent tools must implement.
type Tool interface {
	// Definition returns the langdag tool definition for LLM consumption.
	Definition() types.ToolDefinition
	// Execute runs the tool with the given JSON input and returns a result string.
	Execute(ctx context.Context, input json.RawMessage) (string, error)
	// RequiresApproval returns true if this invocation needs user confirmation.
	RequiresApproval(input json.RawMessage) bool
}

// AgentEventType identifies the kind of agent event.
type AgentEventType int

const (
	EventTextDelta     AgentEventType = iota // streaming text chunk
	EventToolCallStart                       // tool invocation beginning
	EventToolCallDone                        // tool execution finished
	EventToolResult                          // tool result available
	EventApprovalReq                         // tool needs user approval
	EventDone                                // agent loop finished
	EventError                               // error occurred
)

// AgentEvent carries a single event from the agent loop to the TUI.
type AgentEvent struct {
	Type AgentEventType

	// EventTextDelta
	Text string

	// EventToolCallStart / EventToolCallDone
	ToolName  string
	ToolID    string
	ToolInput json.RawMessage

	// EventToolResult
	ToolResult string
	IsError    bool

	// EventApprovalReq
	ApprovalDesc string // human-readable description of what needs approval

	// EventError
	Error error

	// EventDone
	NodeID string // final assistant node ID
}

// ApprovalResponse is sent back to the agent when the user approves/denies a tool call.
type ApprovalResponse struct {
	Approved bool
}

// Agent orchestrates LLM calls and tool execution.
type Agent struct {
	client       *langdag.Client
	tools        map[string]Tool
	toolDefs     []types.ToolDefinition
	systemPrompt string
	model        string

	events   chan AgentEvent
	approval chan ApprovalResponse

	mu       sync.Mutex
	running  bool
	cancelFn context.CancelFunc
}

// NewAgent creates an agent with the given langdag client, tools, and configuration.
func NewAgent(client *langdag.Client, tools []Tool, systemPrompt, model string) *Agent {
	toolMap := make(map[string]Tool, len(tools))
	toolDefs := make([]types.ToolDefinition, 0, len(tools))
	for _, t := range tools {
		def := t.Definition()
		toolMap[def.Name] = t
		toolDefs = append(toolDefs, def)
	}

	return &Agent{
		client:       client,
		tools:        toolMap,
		toolDefs:     toolDefs,
		systemPrompt: systemPrompt,
		model:        model,
		events:       make(chan AgentEvent, 64),
		approval:     make(chan ApprovalResponse, 1),
	}
}

// Events returns the channel that receives agent events.
func (a *Agent) Events() <-chan AgentEvent {
	return a.events
}

// Approve sends an approval response to the agent.
func (a *Agent) Approve(resp ApprovalResponse) {
	select {
	case a.approval <- resp:
	default:
	}
}

// Cancel stops the running agent loop.
func (a *Agent) Cancel() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancelFn != nil {
		a.cancelFn()
	}
}

// Run starts the agent loop for a user message. It streams LLM responses,
// executes tool calls, and persists nodes via langdag. The parentNodeID is
// empty for new conversations or the last assistant node ID for continuations.
// This method blocks until the loop completes; call it in a goroutine.
func (a *Agent) Run(ctx context.Context, userMessage string, parentNodeID string) {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		a.emit(AgentEvent{Type: EventError, Error: fmt.Errorf("agent already running")})
		return
	}
	a.running = true
	ctx, a.cancelFn = context.WithCancel(ctx)
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		a.running = false
		a.cancelFn = nil
		a.mu.Unlock()
	}()

	a.runLoop(ctx, userMessage, parentNodeID)
}

func (a *Agent) emit(e AgentEvent) {
	a.events <- e
}

// runLoop is the core agent loop: call LLM, handle tool calls, repeat.
func (a *Agent) runLoop(ctx context.Context, userMessage string, parentNodeID string) {
	opts := []langdag.PromptOption{
		langdag.WithSystemPrompt(a.systemPrompt),
		langdag.WithMaxTokens(8192),
	}
	if a.model != "" {
		opts = append(opts, langdag.WithModel(a.model))
	}

	// Initial LLM call
	var result *langdag.PromptResult
	var err error
	if parentNodeID == "" {
		result, err = a.client.Prompt(ctx, userMessage, opts...)
	} else {
		result, err = a.client.PromptFrom(ctx, parentNodeID, userMessage, opts...)
	}
	if err != nil {
		a.emit(AgentEvent{Type: EventError, Error: fmt.Errorf("prompt: %w", err)})
		a.emit(AgentEvent{Type: EventDone})
		return
	}

	// Process streaming response
	var toolCalls []types.ContentBlock
	for chunk := range result.Stream {
		if chunk.Error != nil {
			a.emit(AgentEvent{Type: EventError, Error: chunk.Error})
			a.emit(AgentEvent{Type: EventDone})
			return
		}
		if chunk.Done {
			break
		}
		if chunk.Content != "" {
			a.emit(AgentEvent{Type: EventTextDelta, Text: chunk.Content})
		}
	}

	// After streaming, get the full node to check for tool calls
	nodeID := result.NodeID
	if nodeID == "" {
		// NodeID may come from the last stream chunk
		a.emit(AgentEvent{Type: EventDone})
		return
	}

	node, err := a.client.GetNode(ctx, nodeID)
	if err != nil {
		a.emit(AgentEvent{Type: EventError, Error: fmt.Errorf("get node: %w", err)})
		a.emit(AgentEvent{Type: EventDone})
		return
	}

	// Extract tool calls from the assistant response content blocks
	toolCalls = extractToolCalls(node)

	// Tool loop
	for iteration := 0; iteration < maxToolIterations && len(toolCalls) > 0; iteration++ {
		if err := ctx.Err(); err != nil {
			a.emit(AgentEvent{Type: EventError, Error: err})
			break
		}

		// Execute each tool call
		var toolResults []types.ContentBlock
		for _, tc := range toolCalls {
			a.emit(AgentEvent{
				Type:      EventToolCallStart,
				ToolName:  tc.Name,
				ToolID:    tc.ID,
				ToolInput: tc.Input,
			})

			tool, ok := a.tools[tc.Name]
			if !ok {
				errResult := fmt.Sprintf("unknown tool: %s", tc.Name)
				a.emit(AgentEvent{
					Type:       EventToolResult,
					ToolName:   tc.Name,
					ToolID:     tc.ID,
					ToolResult: errResult,
					IsError:    true,
				})
				toolResults = append(toolResults, types.ContentBlock{
					Type:      "tool_result",
					ToolUseID: tc.ID,
					Content:   errResult,
					IsError:   true,
				})
				continue
			}

			// Check if approval is needed
			if tool.RequiresApproval(tc.Input) {
				a.emit(AgentEvent{
					Type:         EventApprovalReq,
					ToolName:     tc.Name,
					ToolID:       tc.ID,
					ToolInput:    tc.Input,
					ApprovalDesc: fmt.Sprintf("%s: %s", tc.Name, string(tc.Input)),
				})

				// Wait for approval
				select {
				case <-ctx.Done():
					a.emit(AgentEvent{Type: EventError, Error: ctx.Err()})
					a.emit(AgentEvent{Type: EventDone, NodeID: nodeID})
					return
				case resp := <-a.approval:
					if !resp.Approved {
						deniedResult := "Tool call denied by user"
						a.emit(AgentEvent{
							Type:       EventToolResult,
							ToolName:   tc.Name,
							ToolID:     tc.ID,
							ToolResult: deniedResult,
							IsError:    true,
						})
						toolResults = append(toolResults, types.ContentBlock{
							Type:      "tool_result",
							ToolUseID: tc.ID,
							Content:   deniedResult,
							IsError:   true,
						})
						continue
					}
				}
			}

			// Execute the tool
			output, execErr := tool.Execute(ctx, tc.Input)
			isErr := execErr != nil
			if execErr != nil {
				output = execErr.Error()
			}

			a.emit(AgentEvent{
				Type:       EventToolCallDone,
				ToolName:   tc.Name,
				ToolID:     tc.ID,
				ToolResult: output,
			})
			a.emit(AgentEvent{
				Type:       EventToolResult,
				ToolName:   tc.Name,
				ToolID:     tc.ID,
				ToolResult: output,
				IsError:    isErr,
			})

			toolResults = append(toolResults, types.ContentBlock{
				Type:      "tool_result",
				ToolUseID: tc.ID,
				Content:   output,
				IsError:   isErr,
			})
		}

		// Build tool results message and re-call LLM
		// Use PromptFrom with the current nodeID to continue the conversation
		// The tool results need to be sent as the next user message
		toolResultJSON, _ := json.Marshal(toolResults)
		result, err = a.client.PromptFrom(ctx, nodeID, string(toolResultJSON), opts...)
		if err != nil {
			a.emit(AgentEvent{Type: EventError, Error: fmt.Errorf("prompt (tool results): %w", err)})
			break
		}

		// Stream the follow-up response
		for chunk := range result.Stream {
			if chunk.Error != nil {
				a.emit(AgentEvent{Type: EventError, Error: chunk.Error})
				a.emit(AgentEvent{Type: EventDone, NodeID: nodeID})
				return
			}
			if chunk.Done {
				break
			}
			if chunk.Content != "" {
				a.emit(AgentEvent{Type: EventTextDelta, Text: chunk.Content})
			}
		}

		nodeID = result.NodeID
		if nodeID == "" {
			break
		}

		node, err = a.client.GetNode(ctx, nodeID)
		if err != nil {
			a.emit(AgentEvent{Type: EventError, Error: fmt.Errorf("get node: %w", err)})
			break
		}

		toolCalls = extractToolCalls(node)
	}

	a.emit(AgentEvent{Type: EventDone, NodeID: nodeID})
}

// extractToolCalls pulls tool_use content blocks from a node.
func extractToolCalls(node *types.Node) []types.ContentBlock {
	if node == nil || node.Content == "" {
		return nil
	}

	// The node content may be a JSON array of content blocks or plain text.
	var blocks []types.ContentBlock
	if err := json.Unmarshal([]byte(node.Content), &blocks); err != nil {
		return nil
	}

	var calls []types.ContentBlock
	for _, b := range blocks {
		if b.Type == "tool_use" {
			calls = append(calls, b)
		}
	}
	return calls
}
