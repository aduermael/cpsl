package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"langdag.com/langdag"
	"langdag.com/langdag/types"
)

// generateAgentID returns a short random hex string for agent identification.
func generateAgentID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// langdagStoragePath returns the path to the langdag SQLite database.
func langdagStoragePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	dbDir := filepath.Join(home, ".cpsl")
	_ = os.MkdirAll(dbDir, 0o755)
	return filepath.Join(dbDir, "conversations.db")
}

// newLangdagClient creates a langdag client configured from the app config.
// Returns nil if no API keys are configured.
func newLangdagClient(cfg Config) (*langdag.Client, error) {
	// Use the first available provider as default.
	if cfg.AnthropicAPIKey != "" {
		return newLangdagClientForProvider(cfg, ProviderAnthropic)
	}
	if cfg.OpenAIAPIKey != "" {
		return newLangdagClientForProvider(cfg, ProviderOpenAI)
	}
	if cfg.GrokAPIKey != "" {
		return newLangdagClientForProvider(cfg, ProviderGrok)
	}
	if cfg.GeminiAPIKey != "" {
		return newLangdagClientForProvider(cfg, ProviderGemini)
	}
	return nil, nil
}

// newLangdagClientForProvider creates a langdag client configured for a specific provider.
func newLangdagClientForProvider(cfg Config, provider string) (*langdag.Client, error) {
	langdagCfg := langdag.Config{
		StoragePath: langdagStoragePath(),
	}

	switch provider {
	case ProviderAnthropic:
		langdagCfg.Provider = "anthropic"
		langdagCfg.APIKeys = map[string]string{"anthropic": cfg.AnthropicAPIKey}
	case ProviderOpenAI:
		langdagCfg.Provider = "openai"
		langdagCfg.APIKeys = map[string]string{"openai": cfg.OpenAIAPIKey}
	case ProviderGrok:
		langdagCfg.Provider = "grok"
		langdagCfg.APIKeys = map[string]string{"grok": cfg.GrokAPIKey}
	case ProviderGemini:
		langdagCfg.Provider = "gemini"
		langdagCfg.APIKeys = map[string]string{"gemini": cfg.GeminiAPIKey}
	default:
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}

	return langdag.New(langdagCfg)
}

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
	EventTextDelta       AgentEventType = iota // streaming text chunk
	EventToolCallStart                         // tool invocation beginning
	EventToolCallDone                          // tool execution finished
	EventToolResult                            // tool result available
	EventApprovalReq                           // tool needs user approval
	EventUsage                                 // token usage from an LLM call
	EventDone                                  // agent loop finished
	EventError                                 // error occurred
	EventSubAgentDelta                         // sub-agent streaming text
	EventSubAgentStatus                        // sub-agent status (tool calls, completion)
)

// AgentEvent carries a single event from the agent loop to the TUI.
type AgentEvent struct {
	Type    AgentEventType
	AgentID string // unique ID of the agent that emitted this event

	// EventTextDelta / EventSubAgentDelta
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

	// EventUsage
	Usage *types.Usage
	Model string

	// EventDone
	NodeID string // final assistant node ID
}

// ApprovalResponse is sent back to the agent when the user approves/denies a tool call.
type ApprovalResponse struct {
	Approved bool
}

// Agent orchestrates LLM calls and tool execution.
type Agent struct {
	id           string
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
// serverTools are provider-side tools (e.g. web search) that are declared in the
// tool list but executed by the LLM provider, not the client.
func NewAgent(client *langdag.Client, tools []Tool, serverTools []types.ToolDefinition, systemPrompt, model string) *Agent {
	toolMap := make(map[string]Tool, len(tools))
	toolDefs := make([]types.ToolDefinition, 0, len(tools)+len(serverTools))
	for _, t := range tools {
		def := t.Definition()
		toolMap[def.Name] = t
		toolDefs = append(toolDefs, def)
	}
	toolDefs = append(toolDefs, serverTools...)

	return &Agent{
		id:           generateAgentID(),
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

// ID returns the unique identifier for this agent instance.
func (a *Agent) ID() string {
	return a.id
}

func (a *Agent) emit(e AgentEvent) {
	e.AgentID = a.id
	a.events <- e
}

// emitUsage fetches the node by ID and emits an EventUsage with token counts.
func (a *Agent) emitUsage(ctx context.Context, nodeID string) {
	if nodeID == "" {
		return
	}
	node, err := a.client.GetNode(ctx, nodeID)
	if err != nil || node == nil {
		return
	}
	a.emit(AgentEvent{
		Type:  EventUsage,
		Model: node.Model,
		Usage: &types.Usage{
			InputTokens:              node.TokensIn,
			OutputTokens:             node.TokensOut,
			CacheReadInputTokens:     node.TokensCacheRead,
			CacheCreationInputTokens: node.TokensCacheCreation,
			ReasoningTokens:          node.TokensReasoning,
		},
	})
}

// runLoop is the core agent loop: call LLM, handle tool calls, repeat.
func (a *Agent) runLoop(ctx context.Context, userMessage string, parentNodeID string) {
	opts := []langdag.PromptOption{
		langdag.WithSystemPrompt(a.systemPrompt),
		langdag.WithMaxTokens(8192),
		langdag.WithTools(a.toolDefs),
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

	// Process streaming response, collecting tool calls from content blocks.
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
		if chunk.ContentBlock != nil && chunk.ContentBlock.Type == "tool_use" {
			toolCalls = append(toolCalls, *chunk.ContentBlock)
		}
	}

	nodeID := result.NodeID
	if nodeID == "" {
		a.emit(AgentEvent{Type: EventDone})
		return
	}
	a.emitUsage(ctx, nodeID)

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

		// Stream the follow-up response, collecting tool calls.
		toolCalls = nil
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
			if chunk.ContentBlock != nil && chunk.ContentBlock.Type == "tool_use" {
				toolCalls = append(toolCalls, *chunk.ContentBlock)
			}
		}

		nodeID = result.NodeID
		if nodeID == "" {
			break
		}
		a.emitUsage(ctx, nodeID)
	}

	a.emit(AgentEvent{Type: EventDone, NodeID: nodeID})
}
