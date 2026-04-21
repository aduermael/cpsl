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
	if cfg.OllamaBaseURL != "" {
		return newLangdagClientForProvider(cfg, ProviderOllama)
	}
	return nil, nil
}

// newLangdagClientForProvider creates a langdag client configured for a specific provider.
func newLangdagClientForProvider(cfg Config, provider string) (*langdag.Client, error) {
	langdagCfg := langdag.Config{
		StoragePath: langdagStoragePath(),
		RetryConfig: &langdag.RetryConfig{
			BaseDelay: 2 * time.Second,
		},
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
	Mode    string // sub-agent mode ("explore" or "general")
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
		return fmt.Sprintf("Budget: Turn %d/%d — FINAL, produce summary, no tools.",
			used, maxT)
	case progress >= turnBudgetLateThreshold:
		return fmt.Sprintf("Budget: Turn %d/%d — %d left, wrap up NOW.",
			used, maxT, remaining)
	case progress >= turnBudgetMidThreshold:
		return fmt.Sprintf("Budget: Turn %d/%d — past halfway, narrow focus.",
			used, maxT)
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
	// Sub-agent fast path: only emit the turn budget line. Sub-agents have
	// maxTurns > 0 and contextWindow == 0, so session stats, context window
	// utilization, and iteration warnings are all dead/redundant for them.
	if a.maxTurns > 0 && a.contextWindow == 0 {
		budget := a.turnBudgetLine()
		if budget == "" {
			return types.ContentBlock{}
		}
		return types.ContentBlock{
			Type: "text",
			Text: "<system-reminder>\n" + budget + "\n</system-reminder>",
		}
	}

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

	// Turn budget display for sub-agents (main agent path — maxTurns == 0 so
	// this is a no-op, but kept for completeness if maxTurns is ever used for
	// main agents with a context window).
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

// runLoop and friends (backgroundCompletion, gracefulExhaustion,
// clearOldToolResults, replaceToolResultContent, maybeCompact, buildPromptOpts,
// retryCtx, retryableStream, drainStream) live in agent_loops.go.
