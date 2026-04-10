// agent.go defines the Agent type and its streaming tool-call loop for
// communicating with LLM providers via langdag.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

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
	dbDir := filepath.Join(home, ".herm")
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
	if cfg.GemmaAPIKey != "" {
		return newLangdagClientForProvider(cfg, ProviderGemma)
	}
	if cfg.OllamaBaseURL != "" {
		return newLangdagClientForProvider(cfg, ProviderOllama)
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
	case ProviderGemma:
		langdagCfg.Provider = "gemma"
		langdagCfg.APIKeys = map[string]string{"gemma": cfg.GemmaAPIKey}
	case ProviderOllama:
		langdagCfg.Provider = "ollama"
		langdagCfg.OllamaConfig = &langdag.OllamaConfig{BaseURL: cfg.OllamaBaseURL}
	default:
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}

	return langdag.New(langdagCfg)
}

// defaultMaxOutputTokens is the per-response output token limit sent to the
// provider. 16384 is a middle ground: high enough for large single-file writes,
// low enough to avoid wasting tokens on runaway generation.
const defaultMaxOutputTokens = 16384

// defaultMaxOutputGroupTokens is the total output token budget across all
// continuation calls when a response is automatically continued after hitting
// max_tokens. Set to 4× the per-call limit (65536) to allow multi-part
// generation while bounding total cost.
const defaultMaxOutputGroupTokens = defaultMaxOutputTokens * 4

// defaultMaxToolIterations caps the agent loop to prevent runaway tool calls.
// Real workloads with sub-agents routinely exceed 25 iterations; 200 allows
// complex multi-agent tasks while still bounding runaway loops.
const defaultMaxToolIterations = 200

// defaultStreamChunkTimeout is the maximum time to wait for the next stream
// chunk before treating the stream as stalled. Resets on every chunk received.
const defaultStreamChunkTimeout = 90 * time.Second

// Tool is the interface that all agent tools must implement.
type Tool interface {
	// Definition returns the langdag tool definition for LLM consumption.
	Definition() types.ToolDefinition
	// Execute runs the tool with the given JSON input and returns a result string.
	Execute(ctx context.Context, input json.RawMessage) (string, error)
	// RequiresApproval returns true if this invocation needs user confirmation.
	RequiresApproval(input json.RawMessage) bool
	// HostTool returns true if the tool executes on the host rather than in the container.
	HostTool() bool
}

// BackgroundWaiter is an optional interface implemented by tools that manage
// background sub-agents. Used by runLoop during graceful exhaustion and
// background completion to wait for running background agents.
type BackgroundWaiter interface {
	WaitForBackgroundAgents(timeout time.Duration) []string
	HasPendingBackgroundAgents() bool
	// DrainGoroutines blocks until all background goroutines have exited or
	// the timeout expires. Called before closing the event channel to ensure
	// all sub-agent "done" events are forwarded. Returns true if all exited.
	DrainGoroutines(timeout time.Duration) bool
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
	EventCompacted                             // conversation was auto-compacted
	EventSubAgentDelta                         // sub-agent streaming text
	EventSubAgentStatus                        // sub-agent status (tool calls, completion)
	EventSubAgentStart                         // sub-agent started (carries task label)
	EventLLMStart                              // LLM API call starting (for trace timing)
	EventRetry                                 // API call being retried
	EventStreamClear                           // TUI should discard in-progress streaming text (before stream retry)
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
	Usage      *types.Usage
	Model      string
	StopReason string // API stop_reason (e.g. "end_turn", "tool_use")

	// EventToolCallDone / EventToolResult
	Duration time.Duration

	// EventUsage / EventDone
	NodeID string // assistant node ID

	// EventSubAgentStart
	Task    string // sub-agent task description
	Mode    string // sub-agent mode ("explore" or "implement")
	RetryOf string // ID of the failed agent being retried (empty for new agents)

	// EventSubAgentStatus (done)
	SubTrace *TraceSubAgent // nested trace for the sub-agent

	// EventRetry
	Attempt  int // current retry attempt (1-based)
	MaxRetry int // maximum number of retries
}

// ApprovalResponse is sent back to the agent when the user approves/denies a tool call.
type ApprovalResponse struct {
	Approved bool
}

// Agent orchestrates LLM calls and tool execution.
type Agent struct {
	id               string
	client           *langdag.Client
	tools            map[string]Tool
	toolDefs         []types.ToolDefinition
	systemPrompt     string
	model            string
	contextWindow     int    // model's context window in tokens; 0 = unknown (no clearing)
	explorationModel  string // cheap model for compaction summaries; empty = use main model
	maxToolIterations int    // tool-call loop cap; 0 = use defaultMaxToolIterations
	thinking          *bool  // nil = provider default, true/false = explicit

	events   chan AgentEvent
	approval chan ApprovalResponse
	doneCh   chan struct{} // closed when EventDone is emitted; backup signal for TUI
	doneOnce sync.Once    // prevents double-close of doneCh

	streamChunkTimeout time.Duration // max time to wait for the next stream chunk; 0 = use default

	mu       sync.Mutex
	running  bool
	cancelFn context.CancelFunc

	// Session-level stats for token budget awareness (5b).
	sessionInputTokens  int
	sessionOutputTokens int
	sessionAgentCalls   int
	lastInputTokens     int     // input tokens from most recent LLM call (context usage estimate)
	sessionCostUSD      float64 // cumulative session cost, set by TUI via SetSessionCost

	// Tool iteration tracking for budget awareness (9a).
	currentIteration int

	// Turn budget tracking for sub-agent budget consciousness.
	// Updated by the drain loop in SubAgentTool via SetTurnProgress.
	turnMu       sync.Mutex
	maxTurns     int // 0 = no turn budget (main agent); >0 = sub-agent turn limit
	turnsUsed    int
	turnTokensIn int // cumulative input tokens reported via SetTokenProgress
	turnTokensOut int // cumulative output tokens reported via SetTokenProgress

	// Background sub-agent completions, injected into the next LLM call.
	bgMu          sync.Mutex
	bgCompletions []string
}

// AgentOption configures optional Agent parameters.
type AgentOption func(*Agent)

// WithContextWindow sets the model's context window size for clearing/compaction.
func WithContextWindow(n int) AgentOption {
	return func(a *Agent) { a.contextWindow = n }
}

// WithExplorationModel sets the model used for compaction summaries.
func WithExplorationModel(model string) AgentOption {
	return func(a *Agent) { a.explorationModel = model }
}

// WithMaxToolIterations sets the tool-call loop cap for the agent.
func WithMaxToolIterations(n int) AgentOption {
	return func(a *Agent) { a.maxToolIterations = n }
}

// WithStreamChunkTimeout sets the per-chunk inactivity timeout for streaming.
func WithStreamChunkTimeout(d time.Duration) AgentOption {
	return func(a *Agent) { a.streamChunkTimeout = d }
}

// WithThinking controls extended thinking for LLM calls.
func WithThinking(think *bool) AgentOption {
	return func(a *Agent) { a.thinking = think }
}

// WithMaxTurns sets the turn budget for the agent. When maxTurns > 0, the
// system prompt includes turn progress and pacing guidance. Used for sub-agents.
func WithMaxTurns(n int) AgentOption {
	return func(a *Agent) { a.maxTurns = n }
}

// SetTurnProgress updates the agent's turn progress from the external drain loop.
// Thread-safe — called by the SubAgentTool drain loop on each turn boundary.
func (a *Agent) SetTurnProgress(used, max int) {
	a.turnMu.Lock()
	a.turnsUsed = used
	a.maxTurns = max
	a.turnMu.Unlock()
}

// SetTokenProgress updates the agent's cumulative token counts from the external drain loop.
// Thread-safe — called by the SubAgentTool drain loop on each EventUsage.
func (a *Agent) SetTokenProgress(inputTokens, outputTokens int) {
	a.turnMu.Lock()
	a.turnTokensIn = inputTokens
	a.turnTokensOut = outputTokens
	a.turnMu.Unlock()
}

// SetSessionCost updates the agent's cumulative session cost.
// Thread-safe — called by the TUI's EventUsage handler after computing cost.
func (a *Agent) SetSessionCost(cost float64) {
	a.mu.Lock()
	a.sessionCostUSD = cost
	a.mu.Unlock()
}

// NewAgent creates an agent with the given langdag client, tools, and configuration.
// serverTools are provider-side tools (e.g. web search) that are declared in the
// tool list but executed by the LLM provider, not the client.
func NewAgent(client *langdag.Client, tools []Tool, serverTools []types.ToolDefinition, systemPrompt, model string, contextWindow int, opts ...AgentOption) *Agent {
	toolMap := make(map[string]Tool, len(tools))
	toolDefs := make([]types.ToolDefinition, 0, len(tools)+len(serverTools))
	for _, t := range tools {
		def := t.Definition()
		toolMap[def.Name] = t
		toolDefs = append(toolDefs, def)
	}
	toolDefs = append(toolDefs, serverTools...)

	a := &Agent{
		id:                 generateAgentID(),
		client:             client,
		tools:              toolMap,
		toolDefs:           toolDefs,
		systemPrompt:       systemPrompt,
		model:              model,
		contextWindow:      contextWindow,
		streamChunkTimeout: defaultStreamChunkTimeout,
		events:             make(chan AgentEvent, 4096),
		approval:           make(chan ApprovalResponse, 1),
		doneCh:             make(chan struct{}),
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Events returns the channel that receives agent events.
func (a *Agent) Events() <-chan AgentEvent {
	return a.events
}

// DoneCh returns a channel that is closed when EventDone is emitted.
// This provides a reliable completion signal even if the EventDone event
// is dropped from the main events channel due to backpressure.
func (a *Agent) DoneCh() <-chan struct{} {
	return a.doneCh
}

// Approve sends an approval response to the agent.
func (a *Agent) Approve(resp ApprovalResponse) {
	select {
	case a.approval <- resp:
	default:
	}
}

// InjectBackgroundResult adds a background sub-agent's result to the pending
// queue. The result will be included in the next LLM call as a text content block
// alongside the tool results. Thread-safe; can be called from any goroutine.
func (a *Agent) InjectBackgroundResult(result string) {
	a.bgMu.Lock()
	defer a.bgMu.Unlock()
	a.bgCompletions = append(a.bgCompletions, result)
}

// drainBackgroundResults returns and clears all pending background sub-agent
// completions. Returns nil if there are none.
func (a *Agent) drainBackgroundResults() []string {
	a.bgMu.Lock()
	defer a.bgMu.Unlock()
	if len(a.bgCompletions) == 0 {
		return nil
	}
	results := a.bgCompletions
	a.bgCompletions = nil
	return results
}

// findBackgroundWaiter returns the BackgroundWaiter from the agent's tool set,
// or nil if none of the registered tools implement the interface.
func (a *Agent) findBackgroundWaiter() BackgroundWaiter {
	for _, tool := range a.tools {
		if bw, ok := tool.(BackgroundWaiter); ok {
			return bw
		}
	}
	return nil
}

// backgroundCompletionTimeout is the maximum time to wait for background
// sub-agents in a single background-completion cycle.
const backgroundCompletionTimeout = 2 * time.Minute

// maxBackgroundCompletionCycles caps how many wait-and-resume cycles the
// background completion path can perform, preventing infinite loops if the
// model keeps spawning background agents and then stopping.
const maxBackgroundCompletionCycles = 3

// backgroundCompletion is called when the LLM chose to stop (end_turn) but
// background sub-agents are still running. It waits for them, injects their
// results, and re-calls the LLM with tools enabled so it can continue working.
// remainingIter is the number of tool-loop iterations still available.
// Returns the final node ID.
func (a *Agent) backgroundCompletion(ctx context.Context, lastNodeID string, remainingIter int) string {
	bw := a.findBackgroundWaiter()
	if bw == nil {
		return lastNodeID
	}

	nodeID := lastNodeID
	for cycle := 0; cycle < maxBackgroundCompletionCycles && remainingIter > 0; cycle++ {
		if !bw.HasPendingBackgroundAgents() {
			break
		}

		debugLog("background completion cycle %d: waiting for pending agents", cycle+1)
		bw.WaitForBackgroundAgents(backgroundCompletionTimeout)

		bgResults := a.drainBackgroundResults()
		if len(bgResults) == 0 {
			break
		}

		// Build a message informing the model that background agents finished.
		var msg strings.Builder
		if reminder := a.budgetReminderBlock(); reminder.Text != "" {
			msg.WriteString(reminder.Text)
			msg.WriteString("\n\n")
		}
		msg.WriteString("[SYSTEM: Background agents have completed. Incorporate their results and continue.]")
		msg.WriteString("\n\n[Background agent results:]\n")
		for i, r := range bgResults {
			fmt.Fprintf(&msg, "\n--- Background agent %d ---\n%s\n", i+1, r)
		}

		// Re-call LLM with tools enabled so the model can continue working.
		opts := a.buildPromptOpts()
		a.emit(AgentEvent{Type: EventLLMStart})
		toolCalls, newNodeID, stopReason, err := a.retryableStream(ctx, defaultRetryConfig, func() (*langdag.PromptResult, error) {
			return a.client.PromptFrom(ctx, nodeID, msg.String(), opts...)
		})
		if err != nil {
			a.emit(AgentEvent{Type: EventError, Error: fmt.Errorf("background completion: %w", err)})
			return nodeID
		}
		if newNodeID == "" {
			return nodeID
		}
		a.emitUsage(ctx, newNodeID, stopReason)
		nodeID = newNodeID

		// If the model requested tools, run a mini tool loop with the remaining budget.
		for remainingIter > 0 && len(toolCalls) > 0 {
			if err := ctx.Err(); err != nil {
				a.emit(AgentEvent{Type: EventError, Error: err})
				return nodeID
			}

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

				toolStart := time.Now()
				output, execErr := tool.Execute(ctx, tc.Input)
				toolDur := time.Since(toolStart)
				isErr := execErr != nil
				if execErr != nil {
					output = execErr.Error()
				}

				a.emit(AgentEvent{
					Type:       EventToolCallDone,
					ToolName:   tc.Name,
					ToolID:     tc.ID,
					ToolResult: output,
					Duration:   toolDur,
				})
				a.emit(AgentEvent{
					Type:       EventToolResult,
					ToolName:   tc.Name,
					ToolID:     tc.ID,
					ToolResult: output,
					IsError:    isErr,
					Duration:   toolDur,
				})

				toolResults = append(toolResults, types.ContentBlock{
					Type:       "tool_result",
					ToolUseID:  tc.ID,
					Content:    output,
					IsError:    isErr,
					DurationMs: int(toolDur.Milliseconds()),
				})
			}

			opts = a.buildPromptOpts()
			if reminder := a.budgetReminderBlock(); reminder.Text != "" {
				toolResults = append(toolResults, reminder)
			}
			toolResultJSON, marshalErr := json.Marshal(toolResults)
			if marshalErr != nil {
				a.emit(AgentEvent{Type: EventError, Error: fmt.Errorf("marshal tool results: %w", marshalErr)})
				return nodeID
			}
			a.emit(AgentEvent{Type: EventLLMStart})
			toolCalls, newNodeID, stopReason, err = a.retryableStream(ctx, defaultRetryConfig, func() (*langdag.PromptResult, error) {
				return a.client.PromptFrom(ctx, nodeID, string(toolResultJSON), opts...)
			})
			if err != nil {
				a.emit(AgentEvent{Type: EventError, Error: fmt.Errorf("background completion (tool results): %w", err)})
				return nodeID
			}
			if newNodeID == "" {
				return nodeID
			}
			a.emitUsage(ctx, newNodeID, stopReason)
			nodeID = newNodeID
			remainingIter--
		}
	}

	return nodeID
}

// gracefulExhaustionTimeout is the maximum time to wait for background
// sub-agents during graceful exhaustion before proceeding with partial results.
const gracefulExhaustionTimeout = 2 * time.Minute

// gracefulExhaustion is called when the tool loop exhausts its iteration
// budget while the LLM is still requesting tool calls. It waits for any
// running background sub-agents, collects their results, and makes one final
// tools-disabled LLM call so the model can synthesize a response from what
// it has. The text response is emitted as EventTextDelta by drainStream.
// Returns the node ID from the final call, or lastNodeID on error.
func (a *Agent) gracefulExhaustion(ctx context.Context, lastNodeID string) string {
	// Wait for any running background sub-agents to finish.
	if bw := a.findBackgroundWaiter(); bw != nil {
		bw.WaitForBackgroundAgents(gracefulExhaustionTimeout)
	}

	// Drain background results that arrived since the last LLM call.
	// These haven't been shown to the model yet.
	bgResults := a.drainBackgroundResults()

	// Build the final synthesis message. Budget stats go in the user message
	// as a <system-reminder> so the model sees its budget state for scoping.
	var msg strings.Builder
	if reminder := a.budgetReminderBlock(); reminder.Text != "" {
		msg.WriteString(reminder.Text)
		msg.WriteString("\n\n")
	}
	msg.WriteString("[SYSTEM: Tool iteration limit reached. Synthesize a final response from the conversation so far. Do NOT request any tools — this is your last turn.]")
	if len(bgResults) > 0 {
		msg.WriteString("\n\n[Background agent results:]\n")
		for i, r := range bgResults {
			fmt.Fprintf(&msg, "\n--- Background agent %d ---\n%s\n", i+1, r)
		}
	}

	// Make one final LLM call without tools so the model can only produce text.
	// Note: WithSystemPrompt is ignored by PromptFrom (langdag uses the root
	// node's stored prompt), but included for documentation and forward compat.
	finalOpts := []langdag.PromptOption{
		langdag.WithSystemPrompt(a.systemPrompt),
		langdag.WithMaxTokens(defaultMaxOutputTokens),
		langdag.WithMaxOutputGroupTokens(defaultMaxOutputGroupTokens),
		// Deliberately no WithTools — forces a text-only response.
	}
	if a.model != "" {
		finalOpts = append(finalOpts, langdag.WithModel(a.model))
	}
	if a.thinking != nil {
		finalOpts = append(finalOpts, langdag.WithThink(*a.thinking))
	}

	a.emit(AgentEvent{Type: EventLLMStart})
	_, finalNodeID, _, err := a.retryableStream(ctx, defaultRetryConfig, func() (*langdag.PromptResult, error) {
		return a.client.PromptFrom(ctx, lastNodeID, msg.String(), finalOpts...)
	})
	if err != nil {
		a.emit(AgentEvent{Type: EventError, Error: fmt.Errorf("final synthesis: %w", err)})
		return lastNodeID
	}
	if finalNodeID != "" {
		return finalNodeID
	}
	return lastNodeID
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

	// Close the event channel last (first defer = last to execute in LIFO order).
	// Wait for background sub-agent goroutines to finish forwarding their final
	// events before closing, so "done" status events are not lost.
	defer func() {
		if bw := a.findBackgroundWaiter(); bw != nil {
			bw.DrainGoroutines(drainGoroutinesTimeout)
		}
		close(a.events)
	}()

	defer func() {
		a.mu.Lock()
		a.running = false
		a.cancelFn = nil
		a.mu.Unlock()
	}()

	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			a.emit(AgentEvent{
				Type:  EventError,
				Error: fmt.Errorf("agent panic: %v\n%s", r, buf[:n]),
			})
			a.emit(AgentEvent{Type: EventDone})
		}
	}()

	a.runLoop(ctx, userMessage, parentNodeID)
}

// ID returns the unique identifier for this agent instance.
func (a *Agent) ID() string {
	return a.id
}

// drainGoroutinesTimeout is the maximum time Run() will wait for background
// sub-agent goroutines to finish before closing the event channel. This ensures
// "done" status events are forwarded even when backgroundCompletion() timed out.
const drainGoroutinesTimeout = 60 * time.Second

// eventDoneDeliveryTimeout is the maximum time emit() will block trying to
// deliver EventDone before falling back to the doneCh backup signal.
const eventDoneDeliveryTimeout = 5 * time.Second

func (a *Agent) emit(e AgentEvent) {
	e.AgentID = a.id
	if e.Type == EventDone {
		// Critical lifecycle event — try harder to deliver it through the
		// channel so the TUI gets the nodeID and full state transition.
		select {
		case a.events <- e:
		default:
			// Channel full on first try. Block with a timeout to give the
			// TUI time to drain buffered events.
			select {
			case a.events <- e:
			case <-time.After(eventDoneDeliveryTimeout):
				debugLog("EventDone delivery timed out after %v", eventDoneDeliveryTimeout)
			}
		}
		// Close doneCh as a last-resort backup signal. Even if the channel
		// send above succeeded, doneCh is harmless and ensures the TUI
		// always learns the agent has finished.
		a.doneOnce.Do(func() { close(a.doneCh) })
	} else {
		select {
		case a.events <- e:
		default:
			// Channel full — drop non-critical event to prevent deadlock.
			debugLog("event dropped: channel full (type=%d)", e.Type)
		}
	}
}

// emitUsage fetches the node by ID, emits an EventUsage with token counts,
// accumulates session stats, and returns the input token count (for context
// management decisions).
func (a *Agent) emitUsage(ctx context.Context, nodeID, stopReason string) int {
	if nodeID == "" {
		return 0
	}
	node, err := a.client.GetNode(ctx, nodeID)
	if err != nil || node == nil {
		return 0
	}
	a.emit(AgentEvent{
		Type:       EventUsage,
		Model:      node.Model,
		NodeID:     nodeID,
		StopReason: stopReason,
		Usage: &types.Usage{
			InputTokens:              node.TokensIn,
			OutputTokens:             node.TokensOut,
			CacheReadInputTokens:     node.TokensCacheRead,
			CacheCreationInputTokens: node.TokensCacheCreation,
			ReasoningTokens:          node.TokensReasoning,
		},
	})
	// Accumulate session-level stats for budget awareness.
	inputTokens := node.TokensIn + node.TokensCacheRead
	a.sessionInputTokens += inputTokens
	a.sessionOutputTokens += node.TokensOut
	a.lastInputTokens = inputTokens
	return inputTokens
}

// Graduated iteration warning thresholds for the main agent.
// These are fractions of remaining iterations (not used iterations).
const (
	// iterationMidThreshold: past halfway (< 50% remaining) — start focusing.
	iterationMidThreshold = 0.50
	// iterationLowThreshold: running low (< 25% remaining) — plan efficiently.
	iterationLowThreshold = 0.25
)

// Turn budget thresholds for tiered pacing messages in sub-agent system prompts.
const (
	// turnBudgetMidThreshold: past 50% of turns — start narrowing focus.
	turnBudgetMidThreshold = 0.50
	// turnBudgetLateThreshold: past 75% of turns — wrap up, stop exploring.
	turnBudgetLateThreshold = 0.75
	// turnBudgetFinalThreshold: past 90% of turns — synthesize NOW.
	turnBudgetFinalThreshold = 0.90
)

// turnBudgetLine returns the turn budget status line for the system prompt.
// Returns "" when no turn budget is set (main agent).
func (a *Agent) turnBudgetLine() string {
	a.turnMu.Lock()
	maxT := a.maxTurns
	used := a.turnsUsed
	tokIn := a.turnTokensIn
	tokOut := a.turnTokensOut
	a.turnMu.Unlock()

	if maxT <= 0 {
		return ""
	}

	totalTokens := tokIn + tokOut
	remaining := maxT - used
	if remaining < 0 {
		remaining = 0
	}
	progress := float64(used) / float64(maxT)

	switch {
	case progress >= turnBudgetFinalThreshold:
		return fmt.Sprintf("Budget: Turn %d/%d — %d turn remaining | %d tokens used\n"+
			"🛑 FINAL TURN: Produce your complete summary NOW. Do not make any more tool calls. Write your findings as a final response.",
			used, maxT, remaining, totalTokens)
	case progress >= turnBudgetLateThreshold:
		return fmt.Sprintf("Budget: Turn %d/%d — %d turns remaining | %d tokens used\n"+
			"⚠️ Wrap up: You're running low on turns. Stop exploring and begin synthesizing your findings. Your next response should start producing output.",
			used, maxT, remaining, totalTokens)
	case progress >= turnBudgetMidThreshold:
		return fmt.Sprintf("Budget: Turn %d/%d | %d tokens used — you're past halfway. Start narrowing your focus.",
			used, maxT, totalTokens)
	default:
		return fmt.Sprintf("Budget: Turn %d/%d | %d tokens used",
			used, maxT, totalTokens)
	}
}

// budgetReminderBlock returns a text ContentBlock wrapped in <system-reminder>
// tags containing dynamic per-turn stats: session stats, context window %,
// iteration warnings, and turn budget. Returns a zero-value ContentBlock when
// there's nothing to show (e.g. first call before any stats exist).
//
// This block is prepended to the user message (tool results) on follow-up LLM
// calls, keeping the system prompt fully static for prompt caching.
func (a *Agent) budgetReminderBlock() types.ContentBlock {
	var extra []string

	// Session stats line: tokens, agent calls, and cost.
	totalTokens := a.sessionInputTokens + a.sessionOutputTokens
	a.mu.Lock()
	cost := a.sessionCostUSD
	a.mu.Unlock()
	if totalTokens > 0 || a.sessionAgentCalls > 0 {
		statsLine := fmt.Sprintf("Session: %d tokens used, %d agent calls", totalTokens, a.sessionAgentCalls)
		if cost > 0 {
			statsLine += fmt.Sprintf(", ~%s", formatCost(cost))
		}
		extra = append(extra, statsLine)
	}

	// Context window utilization for the main agent.
	if a.contextWindow > 0 && a.lastInputTokens > 0 {
		pct := float64(a.lastInputTokens) * 100 / float64(a.contextWindow)
		extra = append(extra, fmt.Sprintf("Context: ~%.0f%% full (%d/%d tokens)", pct, a.lastInputTokens, a.contextWindow))
	}

	// Graduated iteration warnings for the main agent.
	maxIter := a.maxToolIterations
	if maxIter <= 0 {
		maxIter = defaultMaxToolIterations
	}
	remaining := maxIter - a.currentIteration
	if remaining < 0 {
		remaining = 0
	}
	remainingFraction := float64(remaining) / float64(maxIter)
	switch {
	case remainingFraction < iterationLowThreshold:
		extra = append(extra, fmt.Sprintf("⚠️ You have %d tool iterations remaining out of %d. Plan your remaining work efficiently.", remaining, maxIter))
	case remainingFraction < iterationMidThreshold:
		extra = append(extra, fmt.Sprintf("You're past halfway through your tool iterations (%d/%d remaining). Start focusing on your most important tasks.", remaining, maxIter))
	}

	// Turn budget display for sub-agents.
	if budget := a.turnBudgetLine(); budget != "" {
		extra = append(extra, budget)
	}

	if len(extra) == 0 {
		return types.ContentBlock{}
	}
	return types.ContentBlock{
		Type: "text",
		Text: "<system-reminder>\n" + strings.Join(extra, "\n") + "\n</system-reminder>",
	}
}

// clearThresholdFraction is the fraction of context window at which old tool
// results start getting cleared. 0.8 = clear when input tokens > 80% of window.
const clearThresholdFraction = 0.8

// clearKeepRecent is the number of most-recent tool result nodes to keep intact.
const clearKeepRecent = 4

// clearOldToolResults replaces old tool result content with a short placeholder
// when input tokens exceed a threshold of the context window. This reduces
// context usage for long conversations while allowing the agent to re-read
// files if needed. Only tool result nodes are cleared; the tool_use blocks in
// assistant messages are left intact (providers accept tool_result with any
// content string).
func (a *Agent) clearOldToolResults(ctx context.Context, nodeID string, inputTokens int) {
	if a.contextWindow <= 0 || inputTokens <= 0 {
		return
	}
	threshold := int(float64(a.contextWindow) * clearThresholdFraction)
	if inputTokens < threshold {
		return
	}

	ancestors, err := a.client.GetAncestors(ctx, nodeID)
	if err != nil {
		return
	}

	// Collect tool result nodes (User nodes with tool_result content).
	type toolResultNode struct {
		node    *types.Node
		size    int
		origIdx int // position in ancestors
	}
	var candidates []toolResultNode
	for i, n := range ancestors {
		if n.NodeType == types.NodeTypeUser && isToolResultContent(n.Content) {
			candidates = append(candidates, toolResultNode{
				node:    n,
				size:    len(n.Content),
				origIdx: i,
			})
		}
	}

	// Keep the most recent clearKeepRecent tool result nodes.
	if len(candidates) <= clearKeepRecent {
		return
	}
	clearable := candidates[:len(candidates)-clearKeepRecent]

	// Sort by content size descending — clear the biggest first.
	sort.Slice(clearable, func(i, j int) bool {
		return clearable[i].size > clearable[j].size
	})

	storage := a.client.Storage()
	for _, c := range clearable {
		// Already cleared?
		if strings.Contains(c.node.Content, `"[output cleared]"`) {
			continue
		}
		// Replace each tool_result content with a placeholder, preserving structure.
		replaced := replaceToolResultContent(c.node.Content)
		if replaced == c.node.Content {
			continue
		}
		c.node.Content = replaced
		_ = storage.UpdateNode(ctx, c.node)
	}
}

// replaceToolResultContent takes a JSON array of tool_result content blocks and
// replaces each result's content with "[output cleared]".
func replaceToolResultContent(content string) string {
	var blocks []types.ContentBlock
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &blocks); err != nil {
		return content
	}
	changed := false
	for i := range blocks {
		if blocks[i].Type == "tool_result" && blocks[i].Content != "[output cleared]" {
			blocks[i].Content = "[output cleared]"
			blocks[i].ContentJSON = nil
			changed = true
		}
	}
	if !changed {
		return content
	}
	out, err := json.Marshal(blocks)
	if err != nil {
		return content
	}
	return string(out)
}

// maybeCompact triggers auto-compaction if input tokens exceed the compaction
// threshold. Returns the (possibly new) nodeID to continue from.
func (a *Agent) maybeCompact(ctx context.Context, nodeID string, inputTokens int) string {
	if a.contextWindow <= 0 || inputTokens <= 0 {
		return nodeID
	}
	threshold := int(float64(a.contextWindow) * compactThresholdFraction)
	if inputTokens < threshold {
		return nodeID
	}

	// Use exploration model for cheap summarization, fall back to main model.
	summaryModel := a.explorationModel
	if summaryModel == "" {
		summaryModel = a.model
	}

	result, err := compactConversation(ctx, a.client, nodeID, summaryModel, "")
	if err != nil {
		return nodeID // compaction failed — continue with the original node
	}

	a.emit(AgentEvent{
		Type:   EventCompacted,
		NodeID: result.NewNodeID,
		Text:   fmt.Sprintf("Conversation compacted: %d nodes → summary + %d recent nodes", result.OriginalNodes, result.KeptNodes),
	})

	return result.NewNodeID
}

// buildPromptOpts returns LLM call options with the static system prompt.
// Note: WithSystemPrompt is required for the initial Prompt() call (sets the
// root node's system prompt). On follow-up PromptFrom() calls, langdag ignores
// it (always uses the root node's stored prompt), but it's harmless to include
// and documents intent. Dynamic per-turn stats are injected via
// budgetReminderBlock() as a user-message <system-reminder> instead.
func (a *Agent) buildPromptOpts() []langdag.PromptOption {
	opts := []langdag.PromptOption{
		langdag.WithSystemPrompt(a.systemPrompt),
		langdag.WithMaxTokens(defaultMaxOutputTokens),
		langdag.WithMaxOutputGroupTokens(defaultMaxOutputGroupTokens),
		langdag.WithTools(a.toolDefs),
	}
	if a.model != "" {
		opts = append(opts, langdag.WithModel(a.model))
	}
	if a.thinking != nil {
		opts = append(opts, langdag.WithThink(*a.thinking))
	}
	return opts
}

// retryConfig controls retry behavior for LLM API calls.
type retryConfig struct {
	maxAttempts int           // total attempts (1 = no retry)
	baseDelay   time.Duration // initial delay, doubled each attempt
}

// defaultRetryConfig is 3 attempts with 2s/4s/8s backoff.
var defaultRetryConfig = retryConfig{maxAttempts: 3, baseDelay: 2 * time.Second}

// isRetryableError checks whether an error should trigger a retry.
// Retryable: rate limits (429), server errors (500/502/503/529), network issues.
// Non-retryable: bad request (400), auth (401/403), context canceled.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// Context canceled or deadline exceeded — don't retry.
	if strings.Contains(msg, "context canceled") || strings.Contains(msg, "context deadline exceeded") {
		return false
	}
	// HTTP status codes that warrant retry.
	for _, code := range []string{"429", "500", "502", "503", "529"} {
		if strings.Contains(msg, code) {
			return true
		}
	}
	// Network-level errors.
	for _, pattern := range []string{
		"connection reset",
		"connection refused",
		"no such host",
		"timeout",
		"EOF",
		"broken pipe",
		"TLS handshake",
	} {
		if strings.Contains(msg, pattern) {
			return true
		}
	}
	// "overloaded" / "rate limit" in error text.
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "overloaded") || strings.Contains(lower, "rate limit") {
		return true
	}
	return false
}

// retryablePrompt wraps client.Prompt or client.PromptFrom with retry logic.
// It calls promptFn, and if the error is retryable, waits with exponential
// backoff and retries. Emits EventRetry events so the TUI shows retry status.
func (a *Agent) retryablePrompt(ctx context.Context, cfg retryConfig, promptFn func() (*langdag.PromptResult, error)) (*langdag.PromptResult, error) {
	var lastErr error
	delay := cfg.baseDelay
	for attempt := 1; attempt <= cfg.maxAttempts; attempt++ {
		result, err := promptFn()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isRetryableError(err) || attempt == cfg.maxAttempts {
			return nil, err
		}
		a.emit(AgentEvent{
			Type:     EventRetry,
			Error:    err,
			Attempt:  attempt,
			MaxRetry: cfg.maxAttempts,
			Duration: delay,
		})
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
	return nil, lastErr
}

// maxStreamRetries is the number of additional attempts when a stream fails
// mid-response (on top of the connection-level retries inside retryablePrompt).
const maxStreamRetries = 1

// retryableStream wraps retryablePrompt + drainStream into a single call that
// retries on stream failure. If drainStream returns streamOK=false and the
// context is not canceled, it emits EventStreamClear (so the TUI discards
// partial text) and EventRetry, then re-calls the prompt. Returns the tool
// calls, assistant node ID, and any error. A nil error with a non-empty nodeID
// means the stream completed successfully.
func (a *Agent) retryableStream(ctx context.Context, cfg retryConfig, promptFn func() (*langdag.PromptResult, error)) ([]types.ContentBlock, string, string, error) {
	for streamAttempt := 0; streamAttempt <= maxStreamRetries; streamAttempt++ {
		result, err := a.retryablePrompt(ctx, cfg, promptFn)
		if err != nil {
			return nil, "", "", err
		}

		toolCalls, nodeID, stopReason, streamOK := a.drainStream(ctx, result)
		if streamOK {
			return toolCalls, nodeID, stopReason, nil
		}

		// Stream failed. Don't retry if the context was canceled.
		if ctx.Err() != nil {
			return nil, "", "", ctx.Err()
		}

		if streamAttempt < maxStreamRetries {
			a.emit(AgentEvent{Type: EventStreamClear})
			a.emit(AgentEvent{
				Type:     EventRetry,
				Error:    fmt.Errorf("stream interrupted, retrying"),
				Attempt:  streamAttempt + 1,
				MaxRetry: maxStreamRetries + 1,
			})
			continue
		}
	}
	return nil, "", "", fmt.Errorf("stream interrupted: closed without completion")
}

// drainStream reads all chunks from a prompt result's stream, emitting text
// deltas and collecting tool calls. Returns the tool calls, the assistant node
// ID from the Done chunk, and whether the stream completed normally.
//
// A per-chunk inactivity timeout (a.streamChunkTimeout) prevents indefinite
// blocking when the provider stops sending chunks mid-stream. The timer resets
// on every chunk received, so long responses with steady chunk flow are fine.
//
// The NodeID is extracted from the Done chunk rather than from result.NodeID
// to avoid a race with the background goroutine that sets result.NodeID.
func (a *Agent) drainStream(ctx context.Context, result *langdag.PromptResult) ([]types.ContentBlock, string, string, bool) {
	timeout := a.streamChunkTimeout
	if timeout <= 0 {
		timeout = defaultStreamChunkTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var toolCalls []types.ContentBlock
	for {
		select {
		case <-ctx.Done():
			return nil, "", "", false
		case <-timer.C:
			a.emit(AgentEvent{Type: EventError, Error: fmt.Errorf("stream stalled: no chunk received for %s", timeout)})
			// Drain any remaining buffered chunks (non-blocking).
			for {
				select {
				case _, ok := <-result.Stream:
					if !ok {
						return nil, "", "", false
					}
				default:
					return nil, "", "", false
				}
			}
		case chunk, ok := <-result.Stream:
			if !ok {
				return toolCalls, "", "", false // channel closed without Done
			}
			// Reset the inactivity timer on every chunk.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(timeout)

			if chunk.Error != nil {
				a.emit(AgentEvent{Type: EventError, Error: chunk.Error})
				return nil, "", "", false
			}
			if chunk.Done {
				return toolCalls, chunk.NodeID, chunk.StopReason, true
			}
			if chunk.Content != "" {
				a.emit(AgentEvent{Type: EventTextDelta, Text: chunk.Content})
			}
			if chunk.ContentBlock != nil && chunk.ContentBlock.Type == "tool_use" {
				toolCalls = append(toolCalls, *chunk.ContentBlock)
			}
		}
	}
}

// runLoop is the core agent loop: call LLM, handle tool calls, repeat.
func (a *Agent) runLoop(ctx context.Context, userMessage string, parentNodeID string) {
	opts := a.buildPromptOpts()

	// Initial LLM call with connection-level and stream-level retry.
	a.emit(AgentEvent{Type: EventLLMStart})
	toolCalls, nodeID, stopReason, err := a.retryableStream(ctx, defaultRetryConfig, func() (*langdag.PromptResult, error) {
		if parentNodeID == "" {
			return a.client.Prompt(ctx, userMessage, opts...)
		}
		return a.client.PromptFrom(ctx, parentNodeID, userMessage, opts...)
	})
	if err != nil {
		a.emit(AgentEvent{Type: EventError, Error: fmt.Errorf("prompt: %w", err)})
		a.emit(AgentEvent{Type: EventDone})
		return
	}

	// Handle max_tokens truncation with no usable content. When langdag
	// skips node creation for an empty max_tokens response (Phase 4b), the
	// error flows through drainStream and retryableStream retries. But as a
	// defence-in-depth, also check here in case a max_tokens stop reaches
	// us with an empty nodeID and no tool calls.
	if stopReason == "max_tokens" && len(toolCalls) == 0 && nodeID == "" {
		a.emit(AgentEvent{
			Type:  EventError,
			Error: fmt.Errorf("response was truncated — output exceeded max_tokens with no usable content; try breaking the task into smaller steps"),
		})
		a.emit(AgentEvent{Type: EventDone})
		return
	}

	if nodeID == "" {
		a.emit(AgentEvent{Type: EventDone})
		return
	}
	a.emitUsage(ctx, nodeID, stopReason)

	// Tool loop
	maxIter := a.maxToolIterations
	if maxIter <= 0 {
		maxIter = defaultMaxToolIterations
	}
	iteration := 0
	for iteration < maxIter && len(toolCalls) > 0 {
		if err := ctx.Err(); err != nil {
			a.emit(AgentEvent{Type: EventError, Error: err})
			break
		}

		// Partition tool calls: agent tools run in parallel, others sequentially.
		var seqCalls, agentCalls []types.ContentBlock
		for _, tc := range toolCalls {
			if tc.Name == "agent" {
				agentCalls = append(agentCalls, tc)
			} else {
				seqCalls = append(seqCalls, tc)
			}
		}

		// Execute non-agent tools sequentially (may have side effects and need approval).
		var toolResults []types.ContentBlock
		for _, tc := range seqCalls {
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
					ApprovalDesc: approvalCmdDesc(tc.Name, tc.Input),
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
			toolStart := time.Now()
			output, execErr := tool.Execute(ctx, tc.Input)
			toolDur := time.Since(toolStart)
			isErr := execErr != nil
			if execErr != nil {
				output = execErr.Error()
			}

			a.emit(AgentEvent{
				Type:       EventToolCallDone,
				ToolName:   tc.Name,
				ToolID:     tc.ID,
				ToolResult: output,
				Duration:   toolDur,
			})
			a.emit(AgentEvent{
				Type:       EventToolResult,
				ToolName:   tc.Name,
				ToolID:     tc.ID,
				ToolResult: output,
				IsError:    isErr,
				Duration:   toolDur,
			})

			toolResults = append(toolResults, types.ContentBlock{
				Type:       "tool_result",
				ToolUseID:  tc.ID,
				Content:    output,
				IsError:    isErr,
				DurationMs: int(toolDur.Milliseconds()),
			})
		}

		// Execute agent tools in parallel (each sub-agent is independent).
		if len(agentCalls) > 0 {
			a.sessionAgentCalls += len(agentCalls)
			agentResults := make([]types.ContentBlock, len(agentCalls))
			var wg sync.WaitGroup
			for i, tc := range agentCalls {
				wg.Add(1)
				go func(idx int, tc types.ContentBlock) {
					defer wg.Done()

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
						agentResults[idx] = types.ContentBlock{
							Type:      "tool_result",
							ToolUseID: tc.ID,
							Content:   errResult,
							IsError:   true,
						}
						return
					}

					toolStart := time.Now()
					output, execErr := tool.Execute(ctx, tc.Input)
					toolDur := time.Since(toolStart)
					isErr := execErr != nil
					if execErr != nil {
						output = execErr.Error()
					}

					a.emit(AgentEvent{
						Type:       EventToolCallDone,
						ToolName:   tc.Name,
						ToolID:     tc.ID,
						ToolResult: output,
						Duration:   toolDur,
					})
					a.emit(AgentEvent{
						Type:       EventToolResult,
						ToolName:   tc.Name,
						ToolID:     tc.ID,
						ToolResult: output,
						IsError:    isErr,
						Duration:   toolDur,
					})

					agentResults[idx] = types.ContentBlock{
						Type:       "tool_result",
						ToolUseID:  tc.ID,
						Content:    output,
						IsError:    isErr,
						DurationMs: int(toolDur.Milliseconds()),
					}
				}(i, tc)
			}
			wg.Wait()
			toolResults = append(toolResults, agentResults...)
		}

		// Inject any completed background sub-agent results as text blocks
		// alongside tool results. The LLM sees them in the same user message.
		if bgResults := a.drainBackgroundResults(); len(bgResults) > 0 {
			for _, bgr := range bgResults {
				toolResults = append(toolResults, types.ContentBlock{
					Type: "text",
					Text: "[Background agent completed]\n" + bgr,
				})
			}
		}

		// Build tool results message and re-call LLM.
		// Budget stats are injected as a <system-reminder> text block in the
		// user message (not the system prompt) to preserve prompt caching.
		a.currentIteration = iteration
		opts = a.buildPromptOpts()
		if reminder := a.budgetReminderBlock(); reminder.Text != "" {
			toolResults = append(toolResults, reminder)
		}
		toolResultJSON, marshalErr := json.Marshal(toolResults)
		if marshalErr != nil {
			a.emit(AgentEvent{Type: EventError, Error: fmt.Errorf("marshal tool results: %w", marshalErr)})
			break
		}
		toolResultStr := string(toolResultJSON)
		a.emit(AgentEvent{Type: EventLLMStart})
		toolCalls, nodeID, stopReason, err = a.retryableStream(ctx, defaultRetryConfig, func() (*langdag.PromptResult, error) {
			return a.client.PromptFrom(ctx, nodeID, toolResultStr, opts...)
		})
		if err != nil {
			a.emit(AgentEvent{Type: EventError, Error: fmt.Errorf("prompt (tool results): %w", err)})
			break
		}

		if stopReason == "max_tokens" && len(toolCalls) == 0 && nodeID == "" {
			a.emit(AgentEvent{
				Type:  EventError,
				Error: fmt.Errorf("response was truncated — output exceeded max_tokens with no usable content; try breaking the task into smaller steps"),
			})
			break
		}

		if nodeID == "" {
			break
		}
		inputTokens := a.emitUsage(ctx, nodeID, stopReason)
		a.clearOldToolResults(ctx, nodeID, inputTokens)
		nodeID = a.maybeCompact(ctx, nodeID, inputTokens)
		iteration++
	}

	// When the LLM chose to stop (len(toolCalls) == 0) but background
	// sub-agents are still running, wait for them and re-call the LLM with
	// their results so it can incorporate them. Tools remain enabled so the
	// model can continue working. Capped to prevent infinite loops if the
	// model keeps spawning background agents and stopping.
	if len(toolCalls) == 0 && iteration < maxIter {
		nodeID = a.backgroundCompletion(ctx, nodeID, maxIter-iteration)
	}

	// When the loop exhausts maxToolIterations while the LLM still wants
	// tool calls, perform a graceful wind-down: wait for any background
	// sub-agents, then make one final tools-disabled LLM call so the model
	// can synthesize a response from accumulated context.
	if iteration >= maxIter && len(toolCalls) > 0 {
		a.emit(AgentEvent{
			Type:  EventError,
			Error: fmt.Errorf("reached maximum tool iterations (%d) — synthesizing final response", maxIter),
		})
		nodeID = a.gracefulExhaustion(ctx, nodeID)
	}

	a.emit(AgentEvent{Type: EventDone, NodeID: nodeID})
}
