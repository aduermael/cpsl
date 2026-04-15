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

// Sub-agent mode constants.
const (
	ModeExplore = "explore" // fast, cheap model — read-only research
	ModeGeneral = "general" // full orchestrator model — all tools
)

// Per-mode default turn budgets. Explore agents are cheaper (fast model,
// read-only tools) and need fewer turns. General agents are expensive (full
// model, all tools) and keep the legacy default.
const (
	defaultExploreMaxTurns = 15
	defaultGeneralMaxTurns = 20
)

// subAgentIterationBuffer is extra iterations added to the Agent's maxToolIterations
// beyond maxTurns. This ensures the sub-agent's drain-loop turn counting fires first,
// with the Agent's loop cap as a safety backstop. Set to 3 to accommodate the
// synthesis turn (maxTurns + 1) plus one buffer turn.
const subAgentIterationBuffer = 3

// synthesisPrompt returns a structured synthesis message for a tools-disabled
// final LLM call. The reason parameter describes why synthesis is happening
// (e.g. "Turn limit reached", "Tool iteration limit reached"). Used by both
// sub-agent and main-agent graceful exhaustion paths.
func synthesisPrompt(reason string) string {
	return fmt.Sprintf("[SYSTEM: %s. Produce a structured final response:\n"+
		"- Key findings or decisions made\n"+
		"- Files examined or modified\n"+
		"- Unfinished work or open questions\n"+
		"Do not request tools.]", reason)
}

// defaultMaxAgentDepth is the default maximum nesting depth for sub-agents.
// Depth 1 means the main agent can spawn sub-agents, but sub-agents cannot
// spawn their own sub-agents — matching Claude Code's behavior.
const defaultMaxAgentDepth = 1

// subAgentDoneTimeout is the maximum time to wait for a sub-agent goroutine
// to finish after the event stream has ended. This is a safety net — the
// goroutine should finish quickly once the stream is done, but tool execution
// or other paths could hang.
const subAgentDoneTimeout = 5 * time.Minute

// snapshotCacheTTL is how long a cached project snapshot is reused before
// fetching a fresh one. When the main agent spawns multiple sub-agents in
// quick succession, this avoids redundant shell commands against an unchanged repo.
const snapshotCacheTTL = 10 * time.Second

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

// SubAgentConfig holds the configuration for creating a SubAgentTool.
// Zero-value int fields get sensible defaults (per-mode turn budgets,
// max depth). This replaces the 12-parameter constructor.
type SubAgentConfig struct {
	Client           *langdag.Client
	Tools            []Tool
	ServerTools      []types.ToolDefinition
	MainModel        string // full orchestrator model for "general" mode
	ExplorationModel string // cheap model for "explore" mode and summarization; empty = use truncation fallback
	ExploreMaxTurns  int    // turn budget for explore-mode sub-agents; 0 = defaultExploreMaxTurns
	GeneralMaxTurns  int    // turn budget for general-mode sub-agents; 0 = defaultGeneralMaxTurns
	MaxDepth         int    // maximum nesting depth from this level; 0 = defaultMaxAgentDepth
	CurrentDepth     int    // current nesting depth (0 = spawned by main agent)
	WorkDir          string
	Personality      string
	ContainerImage   string
}

// SubAgentTool spawns a sub-agent to handle complex subtasks autonomously.
// Communication is output-only: the sub-agent returns a result string and
// that is the sole information passed back to the caller.
type SubAgentTool struct {
	client           *langdag.Client
	tools            []Tool
	serverTools      []types.ToolDefinition
	mainModel        string // full orchestrator model for "general" mode
	explorationModel string // cheap model for "explore" mode and summarization; empty = use truncation fallback
	exploreMaxTurns  int    // turn budget for explore-mode sub-agents
	generalMaxTurns  int    // turn budget for general-mode sub-agents
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
	bgWg       sync.WaitGroup           // tracks running background goroutines

	snapMu    sync.Mutex        // guards snapshot cache
	snapCache *projectSnapshot  // cached project snapshot; nil = not cached
	snapTime  time.Time         // when snapCache was fetched
}

// NewSubAgentTool creates a SubAgentTool from the given configuration.
// Zero-value int fields in cfg get sensible defaults.
func NewSubAgentTool(cfg SubAgentConfig) *SubAgentTool {
	if cfg.ExploreMaxTurns <= 0 {
		cfg.ExploreMaxTurns = defaultExploreMaxTurns
	}
	if cfg.GeneralMaxTurns <= 0 {
		cfg.GeneralMaxTurns = defaultGeneralMaxTurns
	}
	if cfg.MaxDepth <= 0 {
		cfg.MaxDepth = defaultMaxAgentDepth
	}
	return &SubAgentTool{
		client:           cfg.Client,
		tools:            cfg.Tools,
		serverTools:      cfg.ServerTools,
		mainModel:        cfg.MainModel,
		explorationModel: cfg.ExplorationModel,
		exploreMaxTurns:  cfg.ExploreMaxTurns,
		generalMaxTurns:  cfg.GeneralMaxTurns,
		maxDepth:         cfg.MaxDepth,
		currentDepth:     cfg.CurrentDepth,
		workDir:          cfg.WorkDir,
		personality:      cfg.Personality,
		containerImage:   cfg.ContainerImage,
		doneTimeout:      subAgentDoneTimeout,
		agentNodes:       make(map[string]agentNodeState),
		bgAgents:         make(map[string]*bgAgentState),
	}
}

// maxTurnsForMode returns the resolved turn budget for the given mode.
func (t *SubAgentTool) maxTurnsForMode(mode string) int {
	if mode == ModeExplore {
		return t.exploreMaxTurns
	}
	return t.generalMaxTurns
}

// cachedSnapshot returns a project snapshot, reusing a cached one if it was
// fetched within snapshotCacheTTL. This avoids redundant shell commands when
// multiple sub-agents spawn in rapid succession against an unchanged repo.
func (t *SubAgentTool) cachedSnapshot() projectSnapshot {
	t.snapMu.Lock()
	defer t.snapMu.Unlock()

	if t.snapCache != nil && time.Since(t.snapTime) < snapshotCacheTTL {
		return *t.snapCache
	}

	msg := fetchProjectSnapshot(t.workDir)
	t.snapCache = &msg.snapshot
	t.snapTime = time.Now()
	return msg.snapshot
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
					"enum": ["explore", "general"],
					"description": "The sub-agent mode. Required for new agents. 'explore' uses a fast, cheap model for research, search, and reading tasks. 'general' uses the full orchestrator model for writing code and making changes. Ignored when resuming with agent_id (the original mode is preserved)."
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

// forwardBlockingDoneTimeout is a longer timeout used specifically for "done"
// status events. With 3+ concurrent sub-agents, the 4096-slot channel can stay
// near capacity for several seconds — 30s gives ample drain time.
const forwardBlockingDoneTimeout = 30 * time.Second

// deltaForwardInterval is the minimum time between EventSubAgentDelta forwards
// in runBackground. Text deltas are accumulated and sent as one combined event,
// reducing channel pressure from ~3000 events per stream to ~150.
const deltaForwardInterval = 200 * time.Millisecond

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

// forwardBlockingWithTimeout is like forwardBlocking but accepts a custom timeout.
// Used for "done" status events that need a longer delivery window.
func (t *SubAgentTool) forwardBlockingWithTimeout(e AgentEvent, timeout time.Duration) {
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
	case <-time.After(timeout):
		debugLog("sub-agent critical event TIMED OUT after %v: parent channel full (type=%d, agentID=%s)",
			timeout, e.Type, e.AgentID)
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

// DrainGoroutines blocks until all background sub-agent goroutines have exited
// or the timeout expires. Call this before closing the parent event channel to
// ensure all "done" events are forwarded. Returns true if all goroutines exited.
func (t *SubAgentTool) DrainGoroutines(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		t.bgWg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		debugLog("DrainGoroutines timed out after %v — some sub-agent goroutines still running", timeout)
		return false
	}
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

// modeToolAllowlists maps each mode to its allowed tool set.
// A nil allowlist (e.g. ModeGeneral) means all tools pass through.
// Explore-mode includes read-only tools plus bash (needed for read-only
// commands like ls, tree, and build checks — an accepted escape hatch
// consistent with Claude Code).
var modeToolAllowlists = map[string]map[string]bool{
	ModeExplore: {
		"glob":      true,
		"grep":      true,
		"read_file": true,
		"outline":   true,
		"bash":      true,
	},
	ModeGeneral: nil, // all tools
}

// buildSubAgentTools returns the tools available to the sub-agent.
// Tools are filtered through the mode's allowlist (nil = all tools pass).
// If the current depth allows further nesting, includes a new SubAgentTool
// at the next depth level. Otherwise the sub-agent cannot spawn children.
func (t *SubAgentTool) buildSubAgentTools(mode string) []Tool {
	allowlist := modeToolAllowlists[mode]
	var tools []Tool
	for _, tool := range t.tools {
		if allowlist != nil && !allowlist[tool.Definition().Name] {
			continue
		}
		tools = append(tools, tool)
	}

	nextDepth := t.currentDepth + 1
	if nextDepth < t.maxDepth {
		// Sub-agent is allowed to spawn its own sub-agents.
		child := NewSubAgentTool(SubAgentConfig{
			Client:           t.client,
			Tools:            t.tools,
			ServerTools:      t.serverTools,
			MainModel:        t.mainModel,
			ExplorationModel: t.explorationModel,
			ExploreMaxTurns:  t.exploreMaxTurns,
			GeneralMaxTurns:  t.generalMaxTurns,
			MaxDepth:         t.maxDepth,
			CurrentDepth:     nextDepth,
			WorkDir:          t.workDir,
			Personality:      t.personality,
			ContainerImage:   t.containerImage,
		})
		child.parentEvents = t.parentEvents
		tools = append(tools, child)
	}

	return tools
}

// drainResult holds the accumulated state from a sub-agent's event drain loop.
// Both foreground Execute and background runBackground produce a drainResult.
type drainResult struct {
	textParts         []string // collected text output fragments
	agentErrors       []string // error messages with tool/turn context
	totalInputTokens  int
	totalOutputTokens int
	turns             int    // number of LLM response turns consumed
	lastNodeID        string // last known nodeID for synthesis and resume
	synthesisAttempted bool  // true when the agent exceeded its turn budget while still requesting tools
}

// drainOptions parameterizes the behavioral differences between foreground and
// background event drain loops.
type drainOptions struct {
	agentID        string          // sub-agent's unique ID
	mode           string          // "explore" or "general" — used for saveNodeID
	maxTurns       int             // resolved turn budget for this mode
	agent          *Agent          // running agent (for DoneCh, Events, Cancel, SetTurnProgress, SetTokenProgress)
	traceCollector *TraceCollector // records trace events for this sub-agent
	// deltaForwarder is called for each text delta during the main event loop
	// (not during the doneCh fallback drain). Foreground passes a function that
	// calls t.forward() directly; background passes one that accumulates into a
	// batch buffer and flushes periodically.
	deltaForwarder func(agentID, text string)
}

// drainSubAgentEvents runs the shared event drain loop for both foreground and
// background sub-agents. It processes events from the agent's stream, tracking
// turns, tokens, errors, and text output. The deltaForwarder callback in opts
// parameterizes the one behavioral difference: how text deltas are forwarded to
// the TUI (immediate for foreground, batched for background).
//
// The loop exits when EventDone is received, the event channel closes, or the
// agent's done channel fires (with a fallback drain of remaining buffered events).
func (t *SubAgentTool) drainSubAgentEvents(opts drainOptions) drainResult {
	var r drainResult
	var currentTool string
	maxTurnsExceeded := false
	responseCounted := false
	usageSeen := false

	doneCh := opts.agent.DoneCh()
	eventCh := opts.agent.Events()

	// processEvent handles a single agent event, updating accumulated state.
	// fullProcessing is true in the main event loop and false in the doneCh
	// fallback drain, which skips forwarding, turn enforcement, and progress updates.
	// Returns true when the drain loop should exit (EventDone received).
	processEvent := func(event AgentEvent, fullProcessing bool) bool {
		switch event.Type {
		case EventTextDelta:
			if fullProcessing && usageSeen {
				opts.traceCollector.FinalizeTurn(opts.agentID)
				usageSeen = false
			}
			r.textParts = append(r.textParts, event.Text)
			opts.traceCollector.AddTextDelta(opts.agentID, event.Text)
			if fullProcessing && opts.deltaForwarder != nil {
				opts.deltaForwarder(opts.agentID, event.Text)
			}

		case EventToolCallStart:
			if !responseCounted {
				r.turns++
				responseCounted = true
				if fullProcessing {
					opts.agent.SetTurnProgress(r.turns, opts.maxTurns)
				}
			}
			if fullProcessing {
				currentTool = event.ToolName
				// Two-stage turn enforcement:
				// - At turns > maxTurns+1: hard cancel as safety backstop.
				// - At turns > maxTurns: flag synthesis and cancel current run.
				if r.turns > opts.maxTurns+1 {
					if !maxTurnsExceeded {
						maxTurnsExceeded = true
						r.agentErrors = append(r.agentErrors, fmt.Sprintf("sub-agent exceeded turn budget (%d) — synthesis was attempted", opts.maxTurns))
					}
					opts.agent.Cancel()
				} else if r.turns > opts.maxTurns && !r.synthesisAttempted {
					r.synthesisAttempted = true
					opts.agent.Cancel()
				}
				t.forward(AgentEvent{Type: EventSubAgentStatus, AgentID: opts.agentID, Text: fmt.Sprintf("tool: %s", event.ToolName)})
			} else {
				// doneCh fallback: only flag synthesis, no cancel or forward.
				if r.turns > opts.maxTurns && !r.synthesisAttempted {
					r.synthesisAttempted = true
				}
			}
			opts.traceCollector.StartToolCall(opts.agentID, event.ToolID, event.ToolName, event.ToolInput)

		case EventToolCallDone:
			if fullProcessing {
				currentTool = ""
			}

		case EventToolResult:
			opts.traceCollector.EndToolCall(event.ToolID, event.ToolResult, event.IsError, event.Duration)

		case EventUsage:
			responseCounted = false
			if event.NodeID != "" {
				r.lastNodeID = event.NodeID
			}
			if event.Usage != nil {
				r.totalInputTokens += event.Usage.InputTokens + event.Usage.CacheReadInputTokens
				r.totalOutputTokens += event.Usage.OutputTokens
				if fullProcessing {
					opts.agent.SetTokenProgress(r.totalInputTokens, r.totalOutputTokens)
				}
			}
			opts.traceCollector.SetUsage(opts.agentID, event.Model, "", traceUsageFromTypes(event.Usage), 0, event.StopReason)
			if fullProcessing {
				usageSeen = true
			}
			t.forwardBlocking(event)

		case EventDone:
			if event.NodeID != "" {
				r.lastNodeID = event.NodeID
				t.saveNodeID(opts.agentID, event.NodeID, opts.mode)
			}
			return true

		case EventError:
			if event.Error != nil && event.Error.Error() != "context canceled" {
				errMsg := event.Error.Error()
				if currentTool != "" {
					errMsg = fmt.Sprintf("during tool %q (turn %d): %s", currentTool, r.turns, errMsg)
				} else {
					errMsg = fmt.Sprintf("turn %d: %s", r.turns, errMsg)
				}
				r.agentErrors = append(r.agentErrors, errMsg)
			}
		}
		return false
	}

drainLoop:
	for {
		select {
		case event, ok := <-eventCh:
			if !ok {
				break drainLoop
			}
			if processEvent(event, true) {
				break drainLoop
			}
		case <-doneCh:
			// doneCh closed — agent is done but EventDone may have been
			// dropped from the events channel. Drain remaining buffered events.
			for {
				select {
				case event, ok := <-eventCh:
					if !ok {
						break drainLoop
					}
					if processEvent(event, false) {
						break drainLoop
					}
				default:
					break drainLoop
				}
			}
		}
	}

	return r
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
			in.Mode = ModeExplore
		}
	}

	if in.Mode != ModeExplore && in.Mode != ModeGeneral {
		return "", fmt.Errorf("mode must be %q or %q, got %q", ModeExplore, ModeGeneral, in.Mode)
	}

	if in.Background {
		return t.executeBackground(ctx, in)
	}

	// Select model and turn budget based on mode.
	model := t.explorationModel
	if in.Mode == ModeGeneral {
		model = t.mainModel
	}
	maxTurns := t.maxTurnsForMode(in.Mode)

	// Build the sub-agent's tool set (may include nested agent tool if depth allows).
	// Explore mode gets read-only tools only; general mode gets everything.
	subTools := t.buildSubAgentTools(in.Mode)

	// Get a project snapshot (cached if recently fetched, fresh otherwise).
	// Explore agents are read-only — git status (uncommitted changes) is not
	// actionable for them, so strip it to save tokens.
	snap := t.cachedSnapshot()
	if in.Mode == ModeExplore {
		snap.GitStatus = ""
	}

	// Build a lean sub-agent system prompt: skips communication, personality,
	// skills, and uses a compact role section instead of the full orchestrator framing.
	systemPrompt := buildSubAgentSystemPrompt(subTools, t.serverTools, t.workDir, t.containerImage, &snap)

	agentOpts := []AgentOption{
		WithMaxToolIterations(maxTurns + subAgentIterationBuffer),
		WithMaxTurns(maxTurns),
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
	go func() {
		defer close(done)
		agent.Run(ctx, in.Task, parentNodeID)
	}()

	// Drain events using the shared loop. Foreground forwards each delta immediately.
	r := t.drainSubAgentEvents(drainOptions{
		agentID:        agentID,
		mode:           in.Mode,
		maxTurns:       maxTurns,
		agent:          agent,
		traceCollector: subTC,
		deltaForwarder: func(id, text string) {
			t.forward(AgentEvent{Type: EventSubAgentDelta, AgentID: id, Text: text})
		},
	})

	// Post-drain: attempt synthesis if the agent exceeded its turn budget while
	// still requesting tools.
	synthesisUsed := false
	if r.synthesisAttempted {
		synthText := t.gracefulSubAgentSynthesis(ctx, agent, r.lastNodeID)
		if synthText != "" {
			r.textParts = append(r.textParts, synthText)
			subTC.AddTextDelta(agentID, synthText)
			t.forward(AgentEvent{Type: EventSubAgentDelta, AgentID: agentID, Text: synthText})
			synthesisUsed = true
		}
	}
	subTC.Finalize()
	subTrace := subTC.BuildSubAgentEvent(agentID, in.Task, model, r.turns, maxTurns)
	t.forwardBlocking(AgentEvent{
		Type:     EventSubAgentStatus,
		AgentID:  agentID,
		Text:     "done",
		IsError:  len(r.agentErrors) > 0,
		SubTrace: subTrace,
		Usage: &types.Usage{
			InputTokens:  r.totalInputTokens,
			OutputTokens: r.totalOutputTokens,
		},
		Task: fmt.Sprintf("turns:%d/%d", r.turns, maxTurns),
	})
	// Wait for the goroutine to finish with a timeout safety net.
	select {
	case <-done:
	case <-time.After(t.doneTimeout):
		debugLog("sub-agent %s goroutine hung after stream end, proceeding after %v timeout", agentID, t.doneTimeout)
		r.agentErrors = append(r.agentErrors, fmt.Sprintf("sub-agent goroutine did not exit within %v after stream end", t.doneTimeout))
	}
	return t.buildResult(ctx, agentID, r.textParts, r.agentErrors, r.turns, maxTurns, synthesisUsed), nil
}

// executeBackground sets up and launches a background sub-agent, returning immediately.
func (t *SubAgentTool) executeBackground(_ context.Context, in subAgentInput) (string, error) {
	model := t.explorationModel
	if in.Mode == ModeGeneral {
		model = t.mainModel
	}
	maxTurns := t.maxTurnsForMode(in.Mode)

	subTools := t.buildSubAgentTools(in.Mode)
	snap := t.cachedSnapshot()
	if in.Mode == ModeExplore {
		snap.GitStatus = ""
	}
	systemPrompt := buildSubAgentSystemPrompt(subTools, t.serverTools, t.workDir, t.containerImage, &snap)

	agentOpts := []AgentOption{
		WithMaxToolIterations(maxTurns + subAgentIterationBuffer),
		WithMaxTurns(maxTurns),
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

	t.bgWg.Add(1)
	go func() {
		defer t.bgWg.Done()
		t.runBackground(bgCtx, agent, agentID, in, model, maxTurns, subTC, state)
	}()

	return fmt.Sprintf("[agent_id: %s] Sub-agent started in background. Task: %s. You will be notified when it completes. Do not narrate progress — the user sees live sub-agent status in the UI. Move on to your next action or stop. If an agent fails, you can retry by spawning a new agent with retry_of set to the failed agent's ID.", agentID, in.Task), nil
}

// runBackground runs a background sub-agent to completion, draining events
// and storing the result in bgAgentState.
func (t *SubAgentTool) runBackground(ctx context.Context, agent *Agent, agentID string, in subAgentInput, model string, maxTurns int, subTC *TraceCollector, state *bgAgentState) {
	defer state.cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		agent.Run(ctx, in.Task, "")
	}()

	// Delta batching: accumulate text deltas and forward them at most once
	// per deltaForwardInterval to reduce channel pressure.
	var deltaBuf strings.Builder
	lastDeltaForward := time.Now()
	flushDelta := func() {
		if deltaBuf.Len() == 0 {
			return
		}
		t.forward(AgentEvent{Type: EventSubAgentDelta, AgentID: agentID, Text: deltaBuf.String()})
		deltaBuf.Reset()
		lastDeltaForward = time.Now()
	}

	// Drain events using the shared loop. Background batches deltas to reduce channel pressure.
	r := t.drainSubAgentEvents(drainOptions{
		agentID:        agentID,
		mode:           in.Mode,
		maxTurns:       maxTurns,
		agent:          agent,
		traceCollector: subTC,
		deltaForwarder: func(_ string, text string) {
			deltaBuf.WriteString(text)
			if time.Since(lastDeltaForward) >= deltaForwardInterval {
				flushDelta()
			}
		},
	})

	// Post-drain: attempt synthesis if the agent exceeded its turn budget while
	// still requesting tools.
	synthesisUsed := false
	if r.synthesisAttempted {
		synthText := t.gracefulSubAgentSynthesis(ctx, agent, r.lastNodeID)
		if synthText != "" {
			r.textParts = append(r.textParts, synthText)
			subTC.AddTextDelta(agentID, synthText)
			deltaBuf.WriteString(synthText)
			synthesisUsed = true
		}
	}
	flushDelta()
	subTC.Finalize()
	subTrace := subTC.BuildSubAgentEvent(agentID, in.Task, model, r.turns, maxTurns)
	t.forwardBlockingWithTimeout(AgentEvent{
		Type:     EventSubAgentStatus,
		AgentID:  agentID,
		Text:     "done",
		IsError:  len(r.agentErrors) > 0,
		SubTrace: subTrace,
		Usage: &types.Usage{
			InputTokens:  r.totalInputTokens,
			OutputTokens: r.totalOutputTokens,
		},
		Task: fmt.Sprintf("turns:%d/%d", r.turns, maxTurns),
	}, forwardBlockingDoneTimeout)
	select {
	case <-done:
	case <-time.After(t.doneTimeout):
		debugLog("bg sub-agent %s goroutine hung after stream end, proceeding after %v timeout", agentID, t.doneTimeout)
		r.agentErrors = append(r.agentErrors, fmt.Sprintf("sub-agent goroutine did not exit within %v after stream end", t.doneTimeout))
	}
	result := t.buildResult(ctx, agentID, r.textParts, r.agentErrors, r.turns, maxTurns, synthesisUsed)
	state.mu.Lock()
	state.done = true
	state.result = result
	state.mu.Unlock()
	if t.onBgComplete != nil {
		t.onBgComplete(result)
	}
}

// gracefulSubAgentSynthesis makes a tools-disabled LLM call so the sub-agent
// produces a text summary when it exceeded its turn budget while still requesting
// tools. Returns the synthesis text, or "" on failure. Uses a fresh context since
// the agent's context was canceled.
func (t *SubAgentTool) gracefulSubAgentSynthesis(ctx context.Context, agent *Agent, lastNodeID string) string {
	if lastNodeID == "" || t.client == nil {
		return ""
	}

	// Use a fresh context — the agent's context was canceled.
	synthCtx, synthCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer synthCancel()

	model := agent.model
	// Note: WithSystemPrompt is ignored by PromptFrom (langdag uses the root
	// node's stored prompt), but included for documentation and forward compat.
	opts := []langdag.PromptOption{
		langdag.WithSystemPrompt(agent.systemPrompt),
		langdag.WithMaxTokens(defaultMaxOutputTokens),
		langdag.WithMaxOutputGroupTokens(defaultMaxOutputGroupTokens),
		// No WithTools — forces a text-only response.
	}
	if model != "" {
		opts = append(opts, langdag.WithModel(model))
	}

	// The model is told this is its final turn — budget numbers add nothing.
	synthMsg := synthesisPrompt("Turn limit reached")

	result, err := t.client.PromptFrom(synthCtx, lastNodeID, synthMsg, opts...)
	if err != nil {
		debugLog("gracefulSubAgentSynthesis failed: %v", err)
		return ""
	}

	// Drain the stream to collect text.
	var parts []string
	for chunk := range result.Stream {
		if chunk.Error != nil {
			debugLog("gracefulSubAgentSynthesis stream error: %v", chunk.Error)
			break
		}
		if chunk.Done {
			break
		}
		if chunk.Content != "" {
			parts = append(parts, chunk.Content)
		}
	}
	return strings.Join(parts, "")
}

// buildResult constructs the final tool result from collected sub-agent state.
// When synthesisUsed is true (the agent produced a structured synthesis via
// gracefulSubAgentSynthesis), the output is already summary-shaped and we skip
// the post-hoc model summarization call.
func (t *SubAgentTool) buildResult(ctx context.Context, agentID string, textParts []string, agentErrors []string, turns, maxTurns int, synthesisUsed bool) string {
	result := strings.TrimSpace(strings.Join(textParts, ""))
	if result == "" && len(agentErrors) > 0 {
		// No text output but we have errors — use errors as the result body.
		result = "Sub-agent encountered errors:\n" + strings.Join(agentErrors, "\n")
	} else if result == "" {
		result = "(sub-agent produced no output)"
	}
	outputPath := t.writeOutputFile(agentID, result)
	var summary string
	var usedModel bool
	if synthesisUsed {
		// Synthesis already produced structured output — pass through or truncate.
		summary = summarizeOutput(result)
	} else {
		summary, usedModel = t.summarizeWithModel(ctx, result)
	}
	return formatSubAgentResult(agentID, outputPath, summary, usedModel, turns, maxTurns, agentErrors)
}

// formatSubAgentResult builds a compact tool result header with agent ID, turn
// count, summary quality indicator, and the summary body. Token counts are
// omitted (tracked via EventUsage, not actionable by the main agent).
func formatSubAgentResult(agentID, outputPath, summary string, modelSummary bool, turns, maxTurns int, errors []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[agent:%s turns:%d/%d", agentID, turns, maxTurns)
	if modelSummary {
		b.WriteString(" summary:model")
	} else if outputPath != "" && len(summary) > subAgentSummaryBytes {
		// Only mark truncated when there was actually more content (output file exists).
		b.WriteString(" summary:truncated")
	}
	b.WriteString("]")
	if outputPath != "" {
		fmt.Fprintf(&b, " [output: %s]", outputPath)
	}
	if len(errors) > 0 {
		fmt.Fprintf(&b, " [errors: %s]", strings.Join(errors, "; "))
	}
	fmt.Fprintf(&b, "\n\n%s", summary)
	return b.String()
}

// subAgentSummaryBytes is the max bytes for the inline summary in the tool result.
// Outputs under this threshold pass through verbatim without model summarization.
// Set to 2KB so short results (~25-30 lines) avoid an unnecessary summarization call.
const subAgentSummaryBytes = 2000

// summarizeOutput returns output verbatim if within subAgentSummaryBytes,
// otherwise truncates at a line boundary and appends a note.
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
// to the exploration model for summarization. Set to 8KB so the summarizer sees
// enough context for accurate bullets even on longer outputs.
const summarizeWithModelMaxChars = 8000

// summarizeWithModelPrompt is the prompt sent to the exploration model for
// generating a structured summary of a sub-agent's output. The format gives
// the main agent machine-parseable structure while keeping content human-readable.
const summarizeWithModelPrompt = `Summarize this sub-agent output using exactly this format. No preamble, no extra commentary.

STATUS: success | partial | failure
FILES: <comma-separated key files touched or discovered, or "none">
FINDINGS:
- <bullet 1>
- <bullet 2>
- <bullet 3>
NEXT: <one-line recommendation for the caller, or "none">

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

