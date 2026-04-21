// agent_loops.go owns the agent's streaming inner loop, tool-iteration budget,
// graceful exhaustion, background-completion, retry/stream drain, and context
// compaction helpers — all the pieces that drive Run() but sit below it.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"langdag.com/langdag"
	"langdag.com/langdag/types"
)

// backgroundCompletionTimeout is the maximum time to wait for background
// sub-agents in a single background-completion cycle.
const backgroundCompletionTimeout = 2 * time.Minute

// maxBackgroundCompletionCycles caps how many wait-and-resume cycles the
// background completion path can perform, preventing infinite loops if the
// model keeps spawning background agents and then stopping.
const maxBackgroundCompletionCycles = 3

// backgroundCompletionOptions is the parameter bundle for (*Agent).backgroundCompletion.
type backgroundCompletionOptions struct {
	lastNodeID    string
	remainingIter int
}

// backgroundCompletion is called when the LLM chose to stop (end_turn) but
// background sub-agents are still running. It waits for them, injects their
// results, and re-calls the LLM with tools enabled so it can continue working.
// remainingIter is the number of tool-loop iterations still available.
// Returns the final node ID.
func (a *Agent) backgroundCompletion(ctx context.Context, opts backgroundCompletionOptions) string {
	ctx = a.retryCtx(ctx)
	bw := a.findBackgroundWaiter()
	if bw == nil {
		return opts.lastNodeID
	}

	nodeID := opts.lastNodeID
	remainingIter := opts.remainingIter
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
		promptOpts := a.buildPromptOpts()
		a.emit(AgentEvent{Type: EventLLMStart})
		toolCalls, newNodeID, stopReason, err := a.retryableStream(ctx, func() (*langdag.PromptResult, error) {
			return a.client.PromptFrom(ctx, nodeID, msg.String(), promptOpts...)
		})
		if err != nil {
			a.emit(AgentEvent{Type: EventError, Error: fmt.Errorf("background completion: %w", err)})
			return nodeID
		}
		if newNodeID == "" {
			return nodeID
		}
		a.emitUsage(ctx, emitUsageOptions{nodeID: newNodeID, stopReason: stopReason})
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

			promptOpts = a.buildPromptOpts()
			if reminder := a.budgetReminderBlock(); reminder.Text != "" {
				toolResults = append(toolResults, reminder)
			}
			toolResultJSON, marshalErr := json.Marshal(toolResults)
			if marshalErr != nil {
				a.emit(AgentEvent{Type: EventError, Error: fmt.Errorf("marshal tool results: %w", marshalErr)})
				return nodeID
			}
			a.emit(AgentEvent{Type: EventLLMStart})
			toolCalls, newNodeID, stopReason, err = a.retryableStream(ctx, func() (*langdag.PromptResult, error) {
				return a.client.PromptFrom(ctx, nodeID, string(toolResultJSON), promptOpts...)
			})
			if err != nil {
				a.emit(AgentEvent{Type: EventError, Error: fmt.Errorf("background completion (tool results): %w", err)})
				return nodeID
			}
			if newNodeID == "" {
				return nodeID
			}
			a.emitUsage(ctx, emitUsageOptions{nodeID: newNodeID, stopReason: stopReason})
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
	ctx = a.retryCtx(ctx)
	// Wait for any running background sub-agents to finish.
	if bw := a.findBackgroundWaiter(); bw != nil {
		bw.WaitForBackgroundAgents(gracefulExhaustionTimeout)
	}

	// Drain background results that arrived since the last LLM call.
	// These haven't been shown to the model yet.
	bgResults := a.drainBackgroundResults()

	// Build the final synthesis message.
	var msg strings.Builder
	msg.WriteString(synthesisPrompt("Tool iteration limit reached"))
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
	_, finalNodeID, _, err := a.retryableStream(ctx, func() (*langdag.PromptResult, error) {
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

// clearThresholdFraction is the fraction of context window at which old tool
// results start getting cleared. 0.8 = clear when input tokens > 80% of window.
const clearThresholdFraction = 0.8

// clearKeepRecent is the number of most-recent tool result nodes to keep intact.
const clearKeepRecent = 4

// clearOldToolResultsOptions is the parameter bundle for (*Agent).clearOldToolResults.
type clearOldToolResultsOptions struct {
	nodeID      string
	inputTokens int
}

// clearOldToolResults replaces old tool result content with a short placeholder
// when input tokens exceed a threshold of the context window. This reduces
// context usage for long conversations while allowing the agent to re-read
// files if needed. Only tool result nodes are cleared; the tool_use blocks in
// assistant messages are left intact (providers accept tool_result with any
// content string).
func (a *Agent) clearOldToolResults(ctx context.Context, opts clearOldToolResultsOptions) {
	if a.contextWindow <= 0 || opts.inputTokens <= 0 {
		return
	}
	threshold := int(float64(a.contextWindow) * clearThresholdFraction)
	if opts.inputTokens < threshold {
		return
	}

	ancestors, err := a.client.GetAncestors(ctx, opts.nodeID)
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

	// Track estimated input tokens; stop clearing once below threshold.
	estimatedTokens := opts.inputTokens
	storage := a.client.Storage()
	for _, c := range clearable {
		if estimatedTokens < threshold {
			break
		}
		// Already cleared?
		if strings.Contains(c.node.Content, `"[output cleared]"`) {
			continue
		}
		// Replace each tool_result content with a placeholder, preserving structure.
		replaced := replaceToolResultContent(c.node.Content)
		if replaced == c.node.Content {
			continue
		}
		// Estimate tokens freed: ~4 bytes per token.
		freedTokens := (c.size - len(replaced)) / 4
		c.node.Content = replaced
		_ = storage.UpdateNode(ctx, c.node)
		estimatedTokens -= freedTokens
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

// maybeCompactOptions is the parameter bundle for (*Agent).maybeCompact.
type maybeCompactOptions struct {
	nodeID      string
	inputTokens int
}

// maybeCompact triggers auto-compaction if input tokens exceed the compaction
// threshold. Returns the (possibly new) nodeID to continue from.
func (a *Agent) maybeCompact(ctx context.Context, opts maybeCompactOptions) string {
	if a.contextWindow <= 0 || opts.inputTokens <= 0 {
		return opts.nodeID
	}
	threshold := int(float64(a.contextWindow) * compactThresholdFraction)
	if opts.inputTokens < threshold {
		return opts.nodeID
	}

	// Use exploration model for cheap summarization, fall back to main model.
	summaryModel := a.explorationModel
	if summaryModel == "" {
		summaryModel = a.model
	}

	result, err := compactConversation(ctx, a.client, opts.nodeID, summaryModel, "")
	if err != nil {
		return opts.nodeID // compaction failed — continue with the original node
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

// retryCtx returns a child context that routes langdag retry events to the
// agent's event channel so the TUI can display retry status.
func (a *Agent) retryCtx(ctx context.Context) context.Context {
	return langdag.ContextWithRetryCallback(ctx, func(ev langdag.RetryEvent) {
		a.emit(AgentEvent{
			Type:     EventRetry,
			Error:    ev.Err,
			Attempt:  ev.Attempt,
			MaxRetry: ev.MaxRetries,
			Duration: ev.Delay,
		})
	})
}

// maxStreamRetries is the number of additional attempts when a stream fails
// mid-response. If drainStream returns streamOK=false and the context is not
// canceled, it emits EventStreamClear and EventRetry, then re-calls the prompt.
const maxStreamRetries = 1

// retryableStream calls promptFn, drains the resulting stream, and retries once
// if the stream is interrupted mid-response. Connection-level retries (rate
// limits, server errors) are handled by langdag's retry provider; this function
// only handles stream-level failures. Returns the tool calls, assistant node ID,
// stop reason, and any error.
func (a *Agent) retryableStream(ctx context.Context, promptFn func() (*langdag.PromptResult, error)) ([]types.ContentBlock, string, string, error) {
	for streamAttempt := 0; streamAttempt <= maxStreamRetries; streamAttempt++ {
		result, err := promptFn()
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

// runLoopOptions is the parameter bundle for (*Agent).runLoop.
type runLoopOptions struct {
	userMessage  string
	parentNodeID string
}

// runLoop is the core agent loop: call LLM, handle tool calls, repeat.
func (a *Agent) runLoop(ctx context.Context, opts runLoopOptions) {
	ctx = a.retryCtx(ctx)
	promptOpts := a.buildPromptOpts()

	// Initial LLM call (langdag handles connection-level retries).
	a.emit(AgentEvent{Type: EventLLMStart})
	toolCalls, nodeID, stopReason, err := a.retryableStream(ctx, func() (*langdag.PromptResult, error) {
		if opts.parentNodeID == "" {
			return a.client.Prompt(ctx, opts.userMessage, promptOpts...)
		}
		return a.client.PromptFrom(ctx, opts.parentNodeID, opts.userMessage, promptOpts...)
	})
	if err != nil {
		a.emit(AgentEvent{Type: EventError, Error: fmt.Errorf("prompt: %w", err)})
		a.emit(AgentEvent{Type: EventDone})
		return
	}

	// Handle max_tokens truncation with no usable content. When langdag
	// skips node creation for an empty max_tokens response (Phase 4b), the
	// error flows through drainStream and retryableStream. As a
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
	a.emitUsage(ctx, emitUsageOptions{nodeID: nodeID, stopReason: stopReason})

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
		promptOpts = a.buildPromptOpts()
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
		toolCalls, nodeID, stopReason, err = a.retryableStream(ctx, func() (*langdag.PromptResult, error) {
			return a.client.PromptFrom(ctx, nodeID, toolResultStr, promptOpts...)
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
		inputTokens := a.emitUsage(ctx, emitUsageOptions{nodeID: nodeID, stopReason: stopReason})
		a.clearOldToolResults(ctx, clearOldToolResultsOptions{nodeID: nodeID, inputTokens: inputTokens})
		nodeID = a.maybeCompact(ctx, maybeCompactOptions{nodeID: nodeID, inputTokens: inputTokens})
		iteration++
	}

	// When the LLM chose to stop (len(toolCalls) == 0) but background
	// sub-agents are still running, wait for them and re-call the LLM with
	// their results so it can incorporate them. Tools remain enabled so the
	// model can continue working. Capped to prevent infinite loops if the
	// model keeps spawning background agents and stopping.
	if len(toolCalls) == 0 && iteration < maxIter {
		nodeID = a.backgroundCompletion(ctx, backgroundCompletionOptions{lastNodeID: nodeID, remainingIter: maxIter - iteration})
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
