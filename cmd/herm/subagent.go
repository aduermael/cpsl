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

// defaultMaxAgentDepth is the default maximum nesting depth for sub-agents.
// Depth 1 means the main agent can spawn sub-agents, but sub-agents cannot
// spawn their own sub-agents — matching Claude Code's behavior.
const defaultMaxAgentDepth = 1

// subAgentDoneTimeout is the maximum time to wait for a sub-agent goroutine
// to finish after the event stream has ended. This is a safety net — the
// goroutine should finish quickly once the stream is done, but tool execution
// or other paths could hang.
const subAgentDoneTimeout = 5 * time.Minute

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
	parentEvents   chan<- AgentEvent // set after construction; forwards live events to TUI

	mu         sync.Mutex
	agentNodes map[string]string // agentID → last nodeID (for resume)
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
		agentNodes:       make(map[string]string),
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
					"description": "The sub-agent mode. 'explore' uses a fast, cheap model for research, search, and reading tasks. 'implement' uses the full orchestrator model for writing code and making changes."
				},
				"agent_id": {
					"type": "string",
					"description": "Optional: ID of a previous sub-agent to resume. The sub-agent continues from where it left off with its full context preserved."
				}
			},
			"required": ["task", "mode"]
		}`),
	}
}

func (t *SubAgentTool) RequiresApproval(_ json.RawMessage) bool {
	return false
}

func (t *SubAgentTool) HostTool() bool { return false }

type subAgentInput struct {
	Task    string `json:"task"`
	Mode    string `json:"mode"`
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
	if in.Mode != "explore" && in.Mode != "implement" {
		return "", fmt.Errorf("mode must be \"explore\" or \"implement\", got %q", in.Mode)
	}

	// Select model based on mode: explore uses the cheap model, implement uses the full model.
	model := t.explorationModel
	if in.Mode == "implement" {
		model = t.mainModel
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
	// Explore mode gets read-only tools only; implement mode gets everything.
	subTools := t.buildSubAgentTools(in.Mode)

	// Fetch a fresh project snapshot so the sub-agent sees the current state
	// of the worktree (files, commits, status) rather than a stale startup copy.
	snap := fetchProjectSnapshot(t.workDir)

	// Build a lean sub-agent system prompt: skips communication, personality,
	// skills, and uses a compact role section instead of the full orchestrator framing.
	systemPrompt := buildSubAgentSystemPrompt(subTools, t.serverTools, t.workDir, t.containerImage, &snap.snapshot)

	agent := NewAgent(t.client, subTools, t.serverTools, systemPrompt, model, 0)
	agentID := agent.ID()

	// Create a local trace collector for this sub-agent's events.
	subTC := NewTraceCollector("", nil)
	subTC.SetMainAgentID(agentID)

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

	// Track errors with context for actionable error reporting (5a).
	var agentErrors []string
	var currentTool string

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
					agent.Cancel()
				}
				t.forward(AgentEvent{Type: EventSubAgentStatus, AgentID: agentID, Text: fmt.Sprintf("tool: %s", event.ToolName)})
			case EventToolCallDone:
				currentTool = ""
			case EventUsage:
				// EventUsage fires once per LLM response — reset the flag so the
				// next batch of tool calls counts as a new turn.
				responseCounted = false
				if event.Usage != nil {
					totalInputTokens += event.Usage.InputTokens + event.Usage.CacheReadInputTokens
					totalOutputTokens += event.Usage.OutputTokens
				}
				subTC.SetUsage(agentID, event.Model, "", traceUsageFromTypes(event.Usage), 0)
				usageSeen = true
				// Forward usage events so sub-agent costs are tracked.
				t.forward(event)
			case EventDone:
				subTC.Finalize()
				subTrace := subTC.BuildSubAgentEvent(agentID, in.Task, model, turns, t.maxTurns)
				t.forward(AgentEvent{
					Type:     EventSubAgentStatus,
					AgentID:  agentID,
					Text:     "done",
					SubTrace: subTrace,
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
							t.saveNodeID(agentID, event.NodeID)
						}
					case EventUsage:
						if event.Usage != nil {
							totalInputTokens += event.Usage.InputTokens + event.Usage.CacheReadInputTokens
							totalOutputTokens += event.Usage.OutputTokens
						}
						subTC.SetUsage(agentID, event.Model, "", traceUsageFromTypes(event.Usage), 0)
						t.forward(event)
					case EventTextDelta:
						textParts = append(textParts, event.Text)
						subTC.AddTextDelta(agentID, event.Text)
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
	t.forward(AgentEvent{
		Type:     EventSubAgentStatus,
		AgentID:  agentID,
		Text:     "done",
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

