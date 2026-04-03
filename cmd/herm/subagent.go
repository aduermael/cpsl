// subagent.go implements the sub-agent tool, which spawns autonomous child
// agents to handle complex subtasks with their own context windows.
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

// defaultSubAgentMaxTurns is the default response-cycle cap per sub-agent invocation.
// A "turn" is one LLM response cycle, which may contain multiple tool calls.
const defaultSubAgentMaxTurns = 20

// subAgentIterationBuffer is extra iterations added to the Agent's maxToolIterations
// beyond maxTurns. This ensures the sub-agent's drain-loop turn counting fires first,
// with the Agent's loop cap as a safety backstop.
const subAgentIterationBuffer = 2

// defaultMaxAgentDepth is the default maximum nesting depth for sub-agents.
// Depth 1 means the main agent can spawn sub-agents, but sub-agents cannot
// spawn their own sub-agents — matching Claude Code's behavior.
const defaultMaxAgentDepth = 1

// subAgentDoneTimeout is the maximum time to wait for a sub-agent goroutine
// to finish after the event stream has ended. This is a safety net — the
// goroutine should finish quickly once the stream is done, but tool execution
// or other paths could hang.
const subAgentDoneTimeout = 5 * time.Minute

// bgAgentState tracks a background sub-agent's lifecycle.
type bgAgentState struct {
	mu      sync.Mutex
	task    string
	model   string
	done    bool
	result  string
	cancel  context.CancelFunc
	started time.Time
}

// agentNodeState stores the last nodeID and original mode for a sub-agent so
// that resumed agents inherit their original mode.
type agentNodeState struct {
	nodeID string
	mode   string
}

// SubAgentTool spawns a sub-agent to handle complex subtasks autonomously.
// Communication is output-only: the sub-agent returns a result string and
// that is the sole information passed back to the caller.
type SubAgentTool struct {
	client           *langdag.Client
	tools            []Tool
	serverTools      []types.ToolDefinition
	mainModel        string // full orchestrator model for "implement" mode
	explorationModel string // cheap model for "explore" mode and summarization; empty = use truncation fallback
	maxTurns         int
	maxDepth       int    // maximum nesting depth from this level
	currentDepth   int    // current nesting depth (0 = spawned by main agent)
	workDir        string
	personality    string
	containerImage string
	doneTimeout    time.Duration    // max time to wait for goroutine after stream ends
	streamTimeout  time.Duration    // stream chunk inactivity timeout for inner agents; 0 = default
	parentEvents   chan<- AgentEvent // set after construction; forwards live events to TUI
	onBgComplete   func(string)     // set after construction; called when a background sub-agent finishes

	mu         sync.Mutex
	agentNodes map[string]agentNodeState // agentID → state (nodeID + mode for resume)
	bgAgents   map[string]*bgAgentState // background sub-agents
}

func NewSubAgentTool(client *langdag.Client, tools []Tool, serverTools []types.ToolDefinition, mainModel string, explorationModel string, maxTurns int, maxDepth int, currentDepth int, workDir string, personality string, containerImage string) *SubAgentTool {
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
		mainModel:        mainModel,
		explorationModel: explorationModel,
		maxTurns:         maxTurns,
		maxDepth:         maxDepth,
		currentDepth:     currentDepth,
		workDir:          workDir,
		personality:      personality,
		containerImage:   containerImage,
		doneTimeout:      subAgentDoneTimeout,
		agentNodes:       make(map[string]agentNodeState),
		bgAgents:         make(map[string]*bgAgentState),
	}
}

func (t *SubAgentTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        "agent",
		Description: getToolDescription("agent", "Spawn a sub-agent to handle a complex subtask. The sub-agent has its own context window and communicates only via its output — no shared memory."),
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"task": {
					"type": "string",
					"description": "A clear description of the task for the sub-agent to complete"
				},
				"mode": {
					"type": "string",
					"enum": ["explore", "implement"],
					"description": "The sub-agent mode. Required for new agents. 'explore' uses a fast, cheap model for research, search, and reading tasks. 'implement' uses the full orchestrator model for writing code and making changes. Ignored when resuming with agent_id (the original mode is preserved)."
				},
				"agent_id": {
					"type": "string",
					"description": "Optional: ID of a previous sub-agent to resume. The sub-agent continues from where it left off with its full context preserved."
				},
				"background": {
					"type": "boolean",
					"description": "If true, the sub-agent runs in the background and returns immediately. You will be notified when it completes."
				},
				"retry_of": {
					"type": "string",
					"description": "Optional: ID of a previously failed sub-agent. The failed entry is replaced in the display by this new agent. Use when retrying a task that a previous agent failed."
				}
			},
			"required": ["task"]
		}`),
	}
}

func (t *SubAgentTool) RequiresApproval(_ json.RawMessage) bool {
	return false
}

func (t *SubAgentTool) HostTool() bool { return false }

type subAgentInput struct {
	Task       string `json:"task"`
	Mode       string `json:"mode"`
	AgentID    string `json:"agent_id,omitempty"`
	Background bool   `json:"background,omitempty"`
	RetryOf    string `json:"retry_of,omitempty"`
}

// forwardBlockingTimeout is the maximum time forwardBlocking will wait for the
// parent channel to accept a critical event before giving up.
const forwardBlockingTimeout = 5 * time.Second

// forward sends a sub-agent event to the parent's event channel if set.
// Non-blocking: drops the event if the channel is full. Use for high-frequency
// display events (EventSubAgentDelta, EventSubAgentStatus with tool/text updates)
// where dropping is acceptable.
func (t *SubAgentTool) forward(e AgentEvent) {
	if t.parentEvents == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			debugLog("sub-agent event dropped: parent channel closed (type=%d)", e.Type)
		}
	}()
	select {
	case t.parentEvents <- e:
	default:
		debugLog("sub-agent event dropped: parent channel full (type=%d)", e.Type)
	}
}

// forwardBlocking sends a critical sub-agent event to the parent's event channel,
// blocking until the send succeeds or a timeout expires. Use for events that carry
// state callers depend on: completion status (EventSubAgentStatus "done") and
// token counts (EventUsage). Logs an error when the send times out.
func (t *SubAgentTool) forwardBlocking(e AgentEvent) {
	if t.parentEvents == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			debugLog("sub-agent critical event dropped: parent channel closed (type=%d)", e.Type)
		}
	}()
	select {
	case t.parentEvents <- e:
	case <-time.After(forwardBlockingTimeout):
		debugLog("sub-agent critical event TIMED OUT after %v: parent channel full (type=%d, agentID=%s)",
			forwardBlockingTimeout, e.Type, e.AgentID)
	}
}

// saveNodeID stores the last nodeID and mode for a sub-agent so it can be resumed.
func (t *SubAgentTool) saveNodeID(agentID, nodeID, mode string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.agentNodes[agentID] = agentNodeState{nodeID: nodeID, mode: mode}
}

// loadNodeID retrieves the stored state for a sub-agent.
func (t *SubAgentTool) loadNodeID(agentID string) (agentNodeState, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	state, ok := t.agentNodes[agentID]
	return state, ok
}

// lookupBgAgent returns the background agent state for the given ID, or nil.
func (t *SubAgentTool) lookupBgAgent(agentID string) *bgAgentState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bgAgents[agentID]
}

// bgAgentStatus returns the current state of a background sub-agent.
func (t *SubAgentTool) bgAgentStatus(agentID string) (string, error) {
	state := t.lookupBgAgent(agentID)
	if state == nil {
		return "", fmt.Errorf("agent_id %q is not a background agent; provide a task description to resume it", agentID)
	}

	state.mu.Lock()
	done := state.done
	result := state.result
	state.mu.Unlock()

	if done {
		return fmt.Sprintf("[agent_id: %s] [status: completed]\n\n%s", agentID, result), nil
	}

	elapsed := time.Since(state.started).Truncate(time.Second)
	return fmt.Sprintf("[agent_id: %s] [status: running] Task: %s (elapsed: %s)", agentID, state.task, elapsed), nil
}

// HasPendingBackgroundAgents returns true if any background sub-agent has not
// yet completed. Thread-safe; non-blocking.
func (t *SubAgentTool) HasPendingBackgroundAgents() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, st := range t.bgAgents {
		st.mu.Lock()
		done := st.done
		st.mu.Unlock()
		if !done {
			return true
		}
	}
	return false
}

// bgWaitPollInterval is the polling interval for WaitForBackgroundAgents.
const bgWaitPollInterval = 250 * time.Millisecond

// WaitForBackgroundAgents blocks until all background sub-agents are done or
// the timeout expires, returning their results. Results are appended in
// completion order (the order they are observed as done during polling).
// Returns nil if there are no background agents.
func (t *SubAgentTool) WaitForBackgroundAgents(timeout time.Duration) []string {
	t.mu.Lock()
	agents := make(map[string]*bgAgentState, len(t.bgAgents))
	for id, st := range t.bgAgents {
		agents[id] = st
	}
	t.mu.Unlock()

	if len(agents) == 0 {
		return nil
	}

	deadline := time.After(timeout)
	var results []string
	collected := make(map[string]bool)

	for len(collected) < len(agents) {
		for id, st := range agents {
			if collected[id] {
				continue
			}
			st.mu.Lock()
			done := st.done
			result := st.result
			st.mu.Unlock()
			if done {
				results = append(results, result)
				collected[id] = true
			}
		}
		if len(collected) == len(agents) {
			break
		}
		select {
		case <-deadline:
			// Timeout: collect whatever is done, note the rest as timed out.
			for id, st := range agents {
				if collected[id] {
					continue
				}
				st.mu.Lock()
				done := st.done
				result := st.result
				task := st.task
				st.mu.Unlock()
				if done {
					results = append(results, result)
				} else {
					results = append(results, fmt.Sprintf("[agent %s] timed out waiting — task: %s", id, task))
				}
			}
			return results
		case <-time.After(bgWaitPollInterval):
			// Poll again.
		}
	}

	return results
}

// exploreToolAllowlist is the set of tools available to explore-mode sub-agents.
// These are read-only tools plus bash (needed for read-only commands like ls,
// tree, and build checks — an accepted escape hatch consistent with Claude Code).
var exploreToolAllowlist = map[string]bool{
	"glob":      true,
	"grep":      true,
	"read_file": true,
	"outline":   true,
	"bash":      true,
}

// buildSubAgentTools returns the tools available to the sub-agent.
// When mode is "explore", only tools in exploreToolAllowlist are included.
// When mode is "implement", the full tool set is included.
// If the current depth allows further nesting, includes a new SubAgentTool
// at the next depth level. Otherwise the sub-agent cannot spawn children.
func (t *SubAgentTool) buildSubAgentTools(mode string) []Tool {
	var tools []Tool
	for _, tool := range t.tools {
		if mode == "explore" && !exploreToolAllowlist[tool.Definition().Name] {
			continue
		}
		tools = append(tools, tool)
	}

	nextDepth := t.currentDepth + 1
	if nextDepth < t.maxDepth {
		// Sub-agent is allowed to spawn its own sub-agents.
		child := NewSubAgentTool(t.client, t.tools, t.serverTools, t.mainModel, t.explorationModel, t.maxTurns, t.maxDepth, nextDepth, t.workDir, t.personality, t.containerImage)
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

	// Status check for background sub-agents (mode not required).
	if in.AgentID != "" && in.Task == "status" {
		return t.bgAgentStatus(in.AgentID)
	}
	if in.Background && in.AgentID != "" {
		return "", fmt.Errorf("background mode cannot be used with agent_id (resume)")
	}

	// For resumed agents, inherit the original mode before validation.
	// Mode is immutable once set at spawn time.
	var parentNodeID string
	if in.AgentID != "" {
		state, ok := t.loadNodeID(in.AgentID)
		if !ok {
			return "", fmt.Errorf("unknown agent_id %q: no previous sub-agent with that ID", in.AgentID)
		}
		parentNodeID = state.nodeID
		if state.mode != "" {
			in.Mode = state.mode
		} else {
			in.Mode = "explore"
		}
	}

	if in.Mode != "explore" && in.Mode != "implement" {
		return "", fmt.Errorf("mode must be \"explore\" or \"implement\", got %q", in.Mode)
	}

	if in.Background {
		return t.executeBackground(ctx, in)
	}

	// Select model based on mode: explore uses the cheap model, implement uses the full model.
	model := t.explorationModel
	if in.Mode == "implement" {
		model = t.mainModel
	}

	// Build the sub-agent's tool set (may include nested agent tool if depth allows).
	// Explore mode gets read-only tools only; implement mode gets everything.
	subTools := t.buildSubAgentTools(in.Mode)

	// Fetch a fresh project snapshot so the sub-agent sees the current state
	// of the worktree (files, commits, status) rather than a stale startup copy.
	snap := fetchProjectSnapshot(t.workDir)

	// Build a lean sub-agent system prompt: skips communication, personality,
	// skills, and uses a compact role section instead of the full orchestrator framing.
	systemPrompt := buildSubAgentSystemPrompt(subTools, t.serverTools, t.workDir, t.containerImage, &snap.snapshot)

	agentOpts := []AgentOption{
		WithMaxToolIterations(t.maxTurns + subAgentIterationBuffer),
	}
	if t.streamTimeout > 0 {
		agentOpts = append(agentOpts, WithStreamChunkTimeout(t.streamTimeout))
	}
	agent := NewAgent(t.client, subTools, t.serverTools, systemPrompt, model, 0, agentOpts...)
	agentID := agent.ID()

	// Create a local trace collector for this sub-agent's events.
	subTC := NewTraceCollector("")
	subTC.SetMainAgentID(agentID)

	// Notify the TUI that a sub-agent is starting, with its task label and mode.
	t.forward(AgentEvent{Type: EventSubAgentStart, AgentID: agentID, Task: in.Task, Mode: in.Mode, RetryOf: in.RetryOf})

	// Run the sub-agent in a goroutine and drain events.
	done := make(chan struct{})
	var textParts []string

	go func() {
		defer close(done)
		agent.Run(ctx, in.Task, parentNodeID)
	}()

	// Track sub-agent token usage for reporting in the tool result.
	var totalInputTokens, totalOutputTokens int

	// Track errors with context for actionable error reporting (5a).
	var agentErrors []string
	var currentTool string
	maxTurnsExceeded := false // prevents duplicate max-turns error messages

	turns := 0
	responseCounted := false // tracks whether the current LLM response has been counted as a turn
	usageSeen := false       // tracks whether usage was received for current turn (for trace turn boundaries)
	doneCh := agent.DoneCh()
	eventCh := agent.Events()
	drainLoop:
	for {
		select {
		case event, ok := <-eventCh:
			if !ok {
				// Channel closed — fall through to finalize.
				break drainLoop
			}
			switch event.Type {
			case EventTextDelta:
				if usageSeen {
					subTC.FinalizeTurn(agentID)
					usageSeen = false
				}
				textParts = append(textParts, event.Text)
				subTC.AddTextDelta(agentID, event.Text)
				t.forward(AgentEvent{Type: EventSubAgentDelta, AgentID: agentID, Text: event.Text})
			case EventToolCallStart:
				if !responseCounted {
					turns++
					responseCounted = true
				}
				currentTool = event.ToolName
				if turns > t.maxTurns {
					if !maxTurnsExceeded {
						maxTurnsExceeded = true
						agentErrors = append(agentErrors, fmt.Sprintf("sub-agent reached maximum turns (%d) — partial output returned", t.maxTurns))
					}
					agent.Cancel()
				}
				subTC.StartToolCall(agentID, event.ToolID, event.ToolName, event.ToolInput)
				t.forward(AgentEvent{Type: EventSubAgentStatus, AgentID: agentID, Text: fmt.Sprintf("tool: %s", event.ToolName)})
			case EventToolCallDone:
				currentTool = ""
			case EventToolResult:
				subTC.EndToolCall(event.ToolID, event.ToolResult, event.IsError, event.Duration)
			case EventUsage:
				// EventUsage fires once per LLM response — reset the flag so the
				// next batch of tool calls counts as a new turn.
				responseCounted = false
				if event.Usage != nil {
					totalInputTokens += event.Usage.InputTokens + event.Usage.CacheReadInputTokens
					totalOutputTokens += event.Usage.OutputTokens
				}
				subTC.SetUsage(agentID, event.Model, "", traceUsageFromTypes(event.Usage), 0, event.StopReason)
				usageSeen = true
				// Forward usage events so sub-agent costs are tracked (blocking — critical for token accounting).
				t.forwardBlocking(event)
			case EventDone:
				subTC.Finalize()
				subTrace := subTC.BuildSubAgentEvent(agentID, in.Task, model, turns, t.maxTurns)
				t.forwardBlocking(AgentEvent{
					Type:     EventSubAgentStatus,
					AgentID:  agentID,
					Text:     "done",
					IsError:  len(agentErrors) > 0,
					SubTrace: subTrace,
					Usage: &types.Usage{
						InputTokens:  totalInputTokens,
						OutputTokens: totalOutputTokens,
					},
					Task: fmt.Sprintf("turns:%d/%d", turns, t.maxTurns),
				})
				// Save the nodeID for potential resume.
				if event.NodeID != "" {
					t.saveNodeID(agentID, event.NodeID, in.Mode)
				}
				// Wait for the goroutine to finish with a timeout safety net.
				select {
				case <-done:
				case <-time.After(t.doneTimeout):
					debugLog("sub-agent %s goroutine hung after EventDone, proceeding after %v timeout", agentID, t.doneTimeout)
					agentErrors = append(agentErrors, fmt.Sprintf("sub-agent goroutine did not exit within %v after completion", t.doneTimeout))
				}
				return t.buildResult(ctx, agentID, textParts, agentErrors, totalInputTokens, totalOutputTokens, turns), nil
			case EventError:
				if event.Error != nil && event.Error.Error() != "context canceled" {
					errMsg := event.Error.Error()
					if currentTool != "" {
						errMsg = fmt.Sprintf("during tool %q (turn %d): %s", currentTool, turns, errMsg)
					} else {
						errMsg = fmt.Sprintf("turn %d: %s", turns, errMsg)
					}
					agentErrors = append(agentErrors, errMsg)
				}
			}
		case <-doneCh:
			// doneCh closed — agent is done but EventDone may have been
			// dropped from the events channel. Drain any remaining buffered
			// events, then finalize.
			for {
				select {
				case event, ok := <-eventCh:
					if !ok {
						break drainLoop
					}
					switch event.Type {
					case EventDone:
						if event.NodeID != "" {
							t.saveNodeID(agentID, event.NodeID, in.Mode)
						}
					case EventError:
						if event.Error != nil && event.Error.Error() != "context canceled" {
							errMsg := event.Error.Error()
							if currentTool != "" {
								errMsg = fmt.Sprintf("during tool %q (turn %d): %s", currentTool, turns, errMsg)
							} else {
								errMsg = fmt.Sprintf("turn %d: %s", turns, errMsg)
							}
							agentErrors = append(agentErrors, errMsg)
						}
					case EventUsage:
						if event.Usage != nil {
							totalInputTokens += event.Usage.InputTokens + event.Usage.CacheReadInputTokens
							totalOutputTokens += event.Usage.OutputTokens
						}
						subTC.SetUsage(agentID, event.Model, "", traceUsageFromTypes(event.Usage), 0, event.StopReason)
						t.forwardBlocking(event)
					case EventTextDelta:
						textParts = append(textParts, event.Text)
						subTC.AddTextDelta(agentID, event.Text)
					case EventToolCallStart:
						subTC.StartToolCall(agentID, event.ToolID, event.ToolName, event.ToolInput)
					case EventToolResult:
						subTC.EndToolCall(event.ToolID, event.ToolResult, event.IsError, event.Duration)
					}
				default:
					break drainLoop
				}
			}
		}
	}

	// Channel closed or doneCh fired without EventDone in the events channel.
	// Handle gracefully.
	subTC.Finalize()
	subTrace := subTC.BuildSubAgentEvent(agentID, in.Task, model, turns, t.maxTurns)
	t.forwardBlocking(AgentEvent{
		Type:     EventSubAgentStatus,
		AgentID:  agentID,
		Text:     "done",
		IsError:  len(agentErrors) > 0,
		SubTrace: subTrace,
		Usage: &types.Usage{
			InputTokens:  totalInputTokens,
			OutputTokens: totalOutputTokens,
		},
		Task: fmt.Sprintf("turns:%d/%d", turns, t.maxTurns),
	})
	// Wait for the goroutine to finish with a timeout safety net.
	select {
	case <-done:
	case <-time.After(t.doneTimeout):
		debugLog("sub-agent %s goroutine hung after stream end, proceeding after %v timeout", agentID, t.doneTimeout)
		agentErrors = append(agentErrors, fmt.Sprintf("sub-agent goroutine did not exit within %v after stream end", t.doneTimeout))
	}
	return t.buildResult(ctx, agentID, textParts, agentErrors, totalInputTokens, totalOutputTokens, turns), nil
}

// executeBackground sets up and launches a background sub-agent, returning immediately.
func (t *SubAgentTool) executeBackground(_ context.Context, in subAgentInput) (string, error) {
	model := t.explorationModel
	if in.Mode == "implement" {
		model = t.mainModel
	}

	subTools := t.buildSubAgentTools(in.Mode)
	snap := fetchProjectSnapshot(t.workDir)
	systemPrompt := buildSubAgentSystemPrompt(subTools, t.serverTools, t.workDir, t.containerImage, &snap.snapshot)

	agentOpts := []AgentOption{
		WithMaxToolIterations(t.maxTurns + subAgentIterationBuffer),
	}
	if t.streamTimeout > 0 {
		agentOpts = append(agentOpts, WithStreamChunkTimeout(t.streamTimeout))
	}
	agent := NewAgent(t.client, subTools, t.serverTools, systemPrompt, model, 0, agentOpts...)
	agentID := agent.ID()

	subTC := NewTraceCollector("")
	subTC.SetMainAgentID(agentID)

	bgCtx, bgCancel := context.WithCancel(context.Background())
	state := &bgAgentState{
		task:    in.Task,
		model:   model,
		cancel:  bgCancel,
		started: time.Now(),
	}

	t.mu.Lock()
	t.bgAgents[agentID] = state
	t.mu.Unlock()

	t.forward(AgentEvent{Type: EventSubAgentStart, AgentID: agentID, Task: in.Task, Mode: in.Mode, RetryOf: in.RetryOf})

	go t.runBackground(bgCtx, agent, agentID, in, model, subTC, state)

	return fmt.Sprintf("[agent_id: %s] Sub-agent started in background. Task: %s. You will be notified when it completes. Do not narrate progress — the user sees live sub-agent status in the UI. Move on to your next action or stop. If an agent fails, you can retry by spawning a new agent with retry_of set to the failed agent's ID.", agentID, in.Task), nil
}

// runBackground runs a background sub-agent to completion, draining events
// and storing the result in bgAgentState. The event drain mirrors Execute's
// foreground logic but stores the result instead of returning it.
func (t *SubAgentTool) runBackground(ctx context.Context, agent *Agent, agentID string, in subAgentInput, model string, subTC *TraceCollector, state *bgAgentState) {
	defer state.cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		agent.Run(ctx, in.Task, "")
	}()

	var textParts []string
	var totalInputTokens, totalOutputTokens int
	var agentErrors []string
	var currentTool string
	maxTurnsExceeded := false
	turns := 0
	responseCounted := false
	usageSeen := false

	doneCh := agent.DoneCh()
	eventCh := agent.Events()

drainLoop:
	for {
		select {
		case event, ok := <-eventCh:
			if !ok {
				break drainLoop
			}
			switch event.Type {
			case EventTextDelta:
				if usageSeen {
					subTC.FinalizeTurn(agentID)
					usageSeen = false
				}
				textParts = append(textParts, event.Text)
				subTC.AddTextDelta(agentID, event.Text)
				t.forward(AgentEvent{Type: EventSubAgentDelta, AgentID: agentID, Text: event.Text})
			case EventToolCallStart:
				if !responseCounted {
					turns++
					responseCounted = true
				}
				currentTool = event.ToolName
				if turns > t.maxTurns {
					if !maxTurnsExceeded {
						maxTurnsExceeded = true
						agentErrors = append(agentErrors, fmt.Sprintf("sub-agent reached maximum turns (%d) — partial output returned", t.maxTurns))
					}
					agent.Cancel()
				}
				subTC.StartToolCall(agentID, event.ToolID, event.ToolName, event.ToolInput)
				t.forward(AgentEvent{Type: EventSubAgentStatus, AgentID: agentID, Text: fmt.Sprintf("tool: %s", event.ToolName)})
			case EventToolCallDone:
				currentTool = ""
			case EventToolResult:
				subTC.EndToolCall(event.ToolID, event.ToolResult, event.IsError, event.Duration)
			case EventUsage:
				responseCounted = false
				if event.Usage != nil {
					totalInputTokens += event.Usage.InputTokens + event.Usage.CacheReadInputTokens
					totalOutputTokens += event.Usage.OutputTokens
				}
				subTC.SetUsage(agentID, event.Model, "", traceUsageFromTypes(event.Usage), 0, event.StopReason)
				usageSeen = true
				t.forwardBlocking(event)
			case EventDone:
				subTC.Finalize()
				subTrace := subTC.BuildSubAgentEvent(agentID, in.Task, model, turns, t.maxTurns)
				t.forwardBlocking(AgentEvent{
					Type:     EventSubAgentStatus,
					AgentID:  agentID,
					Text:     "done",
					IsError:  len(agentErrors) > 0,
					SubTrace: subTrace,
					Usage: &types.Usage{
						InputTokens:  totalInputTokens,
						OutputTokens: totalOutputTokens,
					},
					Task: fmt.Sprintf("turns:%d/%d", turns, t.maxTurns),
				})
				if event.NodeID != "" {
					t.saveNodeID(agentID, event.NodeID, in.Mode)
				}
				select {
				case <-done:
				case <-time.After(t.doneTimeout):
					debugLog("bg sub-agent %s goroutine hung after EventDone, proceeding after %v timeout", agentID, t.doneTimeout)
					agentErrors = append(agentErrors, fmt.Sprintf("sub-agent goroutine did not exit within %v after completion", t.doneTimeout))
				}
				result := t.buildResult(ctx, agentID, textParts, agentErrors, totalInputTokens, totalOutputTokens, turns)
				state.mu.Lock()
				state.done = true
				state.result = result
				state.mu.Unlock()
				if t.onBgComplete != nil {
					t.onBgComplete(result)
				}
				return
			case EventError:
				if event.Error != nil && event.Error.Error() != "context canceled" {
					errMsg := event.Error.Error()
					if currentTool != "" {
						errMsg = fmt.Sprintf("during tool %q (turn %d): %s", currentTool, turns, errMsg)
					} else {
						errMsg = fmt.Sprintf("turn %d: %s", turns, errMsg)
					}
					agentErrors = append(agentErrors, errMsg)
				}
			}
		case <-doneCh:
			for {
				select {
				case event, ok := <-eventCh:
					if !ok {
						break drainLoop
					}
					switch event.Type {
					case EventDone:
						if event.NodeID != "" {
							t.saveNodeID(agentID, event.NodeID, in.Mode)
						}
					case EventError:
						if event.Error != nil && event.Error.Error() != "context canceled" {
							errMsg := event.Error.Error()
							if currentTool != "" {
								errMsg = fmt.Sprintf("during tool %q (turn %d): %s", currentTool, turns, errMsg)
							} else {
								errMsg = fmt.Sprintf("turn %d: %s", turns, errMsg)
							}
							agentErrors = append(agentErrors, errMsg)
						}
					case EventUsage:
						if event.Usage != nil {
							totalInputTokens += event.Usage.InputTokens + event.Usage.CacheReadInputTokens
							totalOutputTokens += event.Usage.OutputTokens
						}
						subTC.SetUsage(agentID, event.Model, "", traceUsageFromTypes(event.Usage), 0, event.StopReason)
						t.forwardBlocking(event)
					case EventTextDelta:
						textParts = append(textParts, event.Text)
						subTC.AddTextDelta(agentID, event.Text)
					case EventToolCallStart:
						subTC.StartToolCall(agentID, event.ToolID, event.ToolName, event.ToolInput)
					case EventToolResult:
						subTC.EndToolCall(event.ToolID, event.ToolResult, event.IsError, event.Duration)
					}
				default:
					break drainLoop
				}
			}
		}
	}

	// Fallback: channel closed or doneCh fired without EventDone.
	subTC.Finalize()
	subTrace := subTC.BuildSubAgentEvent(agentID, in.Task, model, turns, t.maxTurns)
	t.forwardBlocking(AgentEvent{
		Type:     EventSubAgentStatus,
		AgentID:  agentID,
		Text:     "done",
		IsError:  len(agentErrors) > 0,
		SubTrace: subTrace,
		Usage: &types.Usage{
			InputTokens:  totalInputTokens,
			OutputTokens: totalOutputTokens,
		},
		Task: fmt.Sprintf("turns:%d/%d", turns, t.maxTurns),
	})
	select {
	case <-done:
	case <-time.After(t.doneTimeout):
		debugLog("bg sub-agent %s goroutine hung after stream end, proceeding after %v timeout", agentID, t.doneTimeout)
		agentErrors = append(agentErrors, fmt.Sprintf("sub-agent goroutine did not exit within %v after stream end", t.doneTimeout))
	}
	result := t.buildResult(ctx, agentID, textParts, agentErrors, totalInputTokens, totalOutputTokens, turns)
	state.mu.Lock()
	state.done = true
	state.result = result
	state.mu.Unlock()
	if t.onBgComplete != nil {
		t.onBgComplete(result)
	}
}

// buildResult constructs the final tool result from collected sub-agent state.
func (t *SubAgentTool) buildResult(ctx context.Context, agentID string, textParts []string, agentErrors []string, inputTokens, outputTokens, turns int) string {
	result := strings.TrimSpace(strings.Join(textParts, ""))
	if result == "" && len(agentErrors) > 0 {
		// No text output but we have errors — use errors as the result body.
		result = "Sub-agent encountered errors:\n" + strings.Join(agentErrors, "\n")
	} else if result == "" {
		result = "(sub-agent produced no output)"
	}
	outputPath := t.writeOutputFile(agentID, result)
	summary, usedModel := t.summarizeWithModel(ctx, result)
	return formatSubAgentResult(agentID, outputPath, summary, usedModel, inputTokens, outputTokens, turns, t.maxTurns, agentErrors)
}

// formatSubAgentResult builds the tool result string with agent ID, output file
// path, token usage, turn count, error context, summary quality indicator, and
// the summary. The caller can use read_file on the output path for full results.
func formatSubAgentResult(agentID, outputPath, summary string, modelSummary bool, inputTokens, outputTokens, turns, maxTurns int, errors []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[agent_id: %s]", agentID)
	if outputPath != "" {
		fmt.Fprintf(&b, " [output: %s]", outputPath)
	}
	if inputTokens > 0 || outputTokens > 0 {
		fmt.Fprintf(&b, " [tokens: input=%d output=%d]", inputTokens, outputTokens)
	}
	fmt.Fprintf(&b, " [turns: %d/%d]", turns, maxTurns)
	if modelSummary {
		b.WriteString(" [summary: model]")
	} else if outputPath != "" && len(summary) > subAgentSummaryBytes {
		// Only mark truncated when there was actually more content (output file exists).
		b.WriteString(" [summary: truncated]")
	}
	if len(errors) > 0 {
		fmt.Fprintf(&b, " [errors: %s]", strings.Join(errors, "; "))
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

