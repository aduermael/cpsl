// subagent_drain.go owns the event-drain loop shared by foreground and
// background sub-agents: drainResult/drainOptions plus drainSubAgentEvents.
package main

import "fmt"

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
					opts.agent.SetTurnProgress(SetTurnProgressOptions{Used: r.turns, Max: opts.maxTurns})
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
					opts.agent.SetTokenProgress(SetTokenProgressOptions{InputTokens: r.totalInputTokens, OutputTokens: r.totalOutputTokens})
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
				t.saveNodeID(saveNodeIDOptions{agentID: opts.agentID, nodeID: event.NodeID, mode: opts.mode})
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
