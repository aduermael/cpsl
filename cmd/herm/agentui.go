// agentui.go bridges the App and Agent types, handling agent lifecycle
// (start, event dispatch, drain) and model-change display.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"langdag.com/langdag/types"
)

// formatToolDefinitions builds a compact display of all tool definitions
// the LLM receives, including client tools and server tools.
func formatToolDefinitions(tools []Tool, serverTools []types.ToolDefinition) string {
	var b strings.Builder
	b.WriteString("── Tool Definitions ──\n")
	for _, t := range tools {
		def := t.Definition()
		b.WriteString("\n")
		b.WriteString(def.Name)
		b.WriteString(": ")
		// Use the brief description from loaded tool descriptions if available,
		// otherwise use the first line of the full description.
		if td, ok := toolDescriptions[def.Name]; ok && td.Brief != "" {
			b.WriteString(td.Brief)
		} else {
			brief := def.Description
			if idx := strings.IndexByte(brief, '\n'); idx > 0 {
				brief = brief[:idx]
			}
			b.WriteString(brief)
		}
		b.WriteString("\n  params: ")
		params := toolParamNames(def.InputSchema)
		if len(params) > 0 {
			b.WriteString(strings.Join(params, ", "))
		} else {
			b.WriteString("(none)")
		}
		b.WriteString("\n")
	}
	for _, st := range serverTools {
		b.WriteString("\n")
		b.WriteString(st.Name)
		b.WriteString(": ")
		if st.Description != "" {
			b.WriteString(st.Description)
		}
		b.WriteString("\n  params: (server-side)\n")
	}
	return b.String()
}

// showModelChange displays an info message when the active model changes.
func (a *App) showModelChange(modelID string) {
	if modelID == "" || modelID == a.lastModelID {
		return
	}
	explorationID := a.config.resolveExplorationModel(a.models)
	line := "Using " + modelID
	offline := a.config.OllamaBaseURL != "" && a.isOllamaOffline(modelID)
	if offline {
		line += " \033[33m(offline)\033[34;3m"
	}
	if explorationID != "" && explorationID != modelID {
		line += "  exploration: " + explorationID
	}
	a.messages = append(a.messages, chatMessage{kind: msgInfo, content: line})
	if offline {
		msg := fmt.Sprintf("\033[33m⚠\033[34;3m Ollama unreachable at \033[36m%s\033[34;3m — run '\033[32;3mollama serve\033[34;3m' to continue", a.config.OllamaBaseURL)
		providers := a.config.configuredProviders()
		delete(providers, ProviderOllama)
		if len(providers) > 0 {
			msg = fmt.Sprintf("\033[33m⚠\033[34;3m Ollama unreachable at \033[36m%s\033[34;3m — run '\033[32;3mollama serve\033[34;3m' or switch to another provider (/config)", a.config.OllamaBaseURL)
		}
		a.messages = append(a.messages, chatMessage{kind: msgInfo, content: msg})
	}
	a.lastModelID = modelID
}

// maybeShowInitialModels shows the startup model line once both the model
// catalog and the project config have loaded, preventing a double display.
func (a *App) maybeShowInitialModels() {
	if a.shownInitialModel || !a.configReady || a.models == nil {
		return
	}
	a.shownInitialModel = true
	a.messages = append(a.messages, chatMessage{kind: msgInfo, content: "v" + Version + " (container: " + hermImageTag + ")"})
	a.showModelChange(a.config.resolveActiveModel(a.models))
}

func (a *App) startAgent(userMessage string) {
	// Move previous attachment files to past/ so /attachments only has current-message files.
	if dir := a.attachmentDir(); dir != "" {
		if entries, err := os.ReadDir(dir); err == nil {
			pastDir := filepath.Join(dir, "past")
			created := false
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				if !created {
					if err := os.MkdirAll(pastDir, 0o755); err != nil {
						break
					}
					created = true
				}
				_ = os.Rename(filepath.Join(dir, e.Name()), filepath.Join(pastDir, e.Name()))
			}
		}
	}

	var tools []Tool
	if a.containerReady && a.container != nil {
		tools = append(tools, NewBashTool(a.container, 120))
		tools = append(tools, NewGlobTool(a.container))
		tools = append(tools, NewGrepTool(a.container))
		tools = append(tools, NewReadFileTool(a.container))
		tools = append(tools, NewOutlineTool(a.container))
		tools = append(tools, NewEditFileTool(a.container))
		tools = append(tools, NewWriteFileTool(a.container))
		if a.worktreePath != "" {
			hermDir := filepath.Join(a.worktreePath, ".herm")
			mounts := []MountSpec{
				{Source: a.worktreePath, Destination: "/workspace"},
				{Source: a.attachmentDir(), Destination: "/attachments", ReadOnly: true},
			}
			var projectID string
			if repoRoot := gitRepoRoot(); repoRoot != "" {
				projectID, _ = ensureProjectID(repoRoot)
			}
			onRebuild := func(imageName string) {
				a.containerImage = imageName
			}
			onStatus := func(text string) {
				a.resultCh <- containerStatusMsg{text: text}
			}
			tools = append(tools, NewDevEnvTool(a.container, hermDir, a.worktreePath, mounts, projectID, onRebuild, onStatus))
		}
	}
	if a.worktreePath != "" {
		tools = append(tools, NewGitTool(a.worktreePath, a.config.effectiveGitCoAuthor()))
	}

	modelID := a.config.resolveActiveModel(a.models)
	if modelID == "" {
		a.messages = append(a.messages, chatMessage{kind: msgError, content: "model not found, `/model` to pick a valid one"})
		a.render()
		return
	}

	var modelProvider string
	if modelDef := findModelByID(a.models, modelID); modelDef != nil {
		modelProvider = modelDef.Provider
	}

	// Server-side tools (e.g. web search) are handled by the LLM provider.
	// Some models don't support them, so we check before including them.
	var serverTools []types.ToolDefinition
	if supportsServerTools(modelProvider, modelID, a.models) {
		serverTools = []types.ToolDefinition{WebSearchToolDef()}
	}

	if modelProvider != "" && modelProvider != a.langdagProvider {
		if a.langdagClient != nil {
			a.langdagClient.Close()
		}
		client, err := newLangdagClientForProvider(a.config, modelProvider)
		if err != nil {
			a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Error initializing %s provider: %v", modelProvider, err)})
			return
		}
		a.langdagClient = client
		a.langdagProvider = modelProvider
	}

	// Load project-local skills from .herm/skills/
	var skills []Skill
	if a.worktreePath != "" {
		skills, _ = loadSkills(filepath.Join(a.worktreePath, ".herm", "skills"))
	}

	workDir := "/workspace"

	containerImage := a.containerImage
	if containerImage == "" {
		containerImage = defaultContainerImage
	}

	// Load tool descriptions from embedded markdown files, replacing dynamic placeholders.
	toolDescriptions = loadToolDescriptions(containerImage)

	// Sub-agent tool: output-only communication, no shared memory.
	// Uses exploration model if configured, otherwise falls back to active model.
	maxTurns := a.config.SubAgentMaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultSubAgentMaxTurns
	}
	maxDepth := a.config.MaxAgentDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxAgentDepth
	}
	explorationModelID := a.config.resolveExplorationModel(a.models)
	subAgentServerTools := serverTools
	if !supportsServerTools(modelProvider, explorationModelID, a.models) {
		subAgentServerTools = nil
	}
	subAgentTool := NewSubAgentTool(a.langdagClient, tools, subAgentServerTools, modelID, explorationModelID, maxTurns, maxDepth, 0, workDir, a.config.Personality, containerImage)
	tools = append(tools, subAgentTool)

	var wtBranch string
	if a.worktreePath != "" {
		wtBranch = worktreeBranch(a.worktreePath)
	}
	systemPrompt := buildSystemPrompt(tools, serverTools, skills, workDir, a.config.Personality, containerImage, wtBranch, a.projectSnap)

	if a.displaySystemPrompts {
		a.messages = append(a.messages, chatMessage{kind: msgSystemPrompt, content: "── System Prompt ──\n" + systemPrompt})
		a.messages = append(a.messages, chatMessage{kind: msgSystemPrompt, content: formatToolDefinitions(tools, serverTools)})
		a.messages = append(a.messages, chatMessage{kind: msgSystemPrompt, content: "── Session Stats ──\n(appended at runtime: \"Session: N tokens used, N agent calls\" — zero at startup)"})
	}

	// Debug file: log system prompt, tool definitions, and user message
	a.debugWriteSection("System Prompt", systemPrompt)
	a.debugWriteSection("Tool Definitions", formatToolDefinitions(tools, serverTools))
	a.debugWriteSection("User Message", userMessage)

	a.showModelChange(modelID)

	ctxWindow := 0
	if m := findModelByID(a.models, modelID); m != nil {
		ctxWindow = m.ContextWindow
	}
	mainMaxIter := a.config.MaxToolIterations
	if mainMaxIter <= 0 {
		mainMaxIter = defaultMaxToolIterations
	}
	agent := NewAgent(a.langdagClient, tools, serverTools, systemPrompt, modelID, ctxWindow,
		WithExplorationModel(explorationModelID),
		WithMaxToolIterations(mainMaxIter))
	subAgentTool.parentEvents = agent.events
	a.agent = agent
	a.agentRunning = true
	a.streamingText = ""
	a.needsTextSep = true
	a.agentStartTime = time.Now()
	a.agentElapsed = 0
	a.approvalPausedTotal = 0
	a.agentTextIndex = 0
	a.agentDisplayInTok = float64(a.mainAgentInputTokens)
	a.agentDisplayOutTok = float64(a.mainAgentOutputTokens)
	if a.agentTicker != nil {
		a.agentTicker.Stop()
	}
	a.agentTicker = time.NewTicker(50 * time.Millisecond)
	go func(ticker *time.Ticker, ch chan any) {
		for range ticker.C {
			select {
			case ch <- agentTickMsg{}:
			default:
			}
		}
	}(a.agentTicker, a.resultCh)

	parentNodeID := a.agentNodeID
	go agent.Run(context.Background(), userMessage, parentNodeID)
}

func (a *App) drainAgentEvents() {
	if a.agent == nil || !a.agentRunning {
		return
	}
	// Cap drain iterations to avoid starving stdin processing.
	// The select in the main loop will pick up remaining events next iteration.
	const maxDrain = 50
	for i := 0; i < maxDrain; i++ {
		select {
		case event, ok := <-a.agent.Events():
			if !ok {
				a.agentRunning = false
				a.cancelSent = false
				return
			}
			a.handleAgentEvent(event)
		default:
			// No more buffered events. Check if doneCh signals completion
			// (backup for when EventDone was dropped from the full channel).
			select {
			case <-a.agent.DoneCh():
				// Agent is done. Drain any final events that arrived between
				// the default case above and now, then mark as not running.
				for {
					select {
					case event, ok := <-a.agent.Events():
						if !ok {
							a.agentRunning = false
							a.cancelSent = false
							return
						}
						a.handleAgentEvent(event)
					default:
						a.agentRunning = false
						a.cancelSent = false
						return
					}
				}
			default:
			}
			return
		}
	}
}

func (a *App) handleAgentEvent(event AgentEvent) {
	debugLog("event=%d text=%q tool=%s err=%v", event.Type, event.Text, event.ToolName, event.Error)

	switch event.Type {
	case EventTextDelta:
		a.streamingText += event.Text
		if idx := strings.LastIndex(a.streamingText, "\n"); idx >= 0 {
			a.messages = append(a.messages, chatMessage{
				kind:      msgAssistant,
				content:   a.streamingText[:idx],
				leadBlank: a.needsTextSep,
			})
			a.needsTextSep = false
			a.streamingText = a.streamingText[idx+1:]
		}
		a.render()

	case EventToolCallStart:
		debugLog("tool_call_start: %s input=%s", event.ToolName, string(event.ToolInput))
		if a.streamingText != "" {
			a.debugWriteSection("Assistant Text", a.streamingText)
			a.messages = append(a.messages, chatMessage{
				kind:      msgAssistant,
				content:   a.streamingText,
				leadBlank: a.needsTextSep,
			})
			a.needsTextSep = false
			a.streamingText = ""
		}
		a.messages = append(a.messages, chatMessage{kind: msgToolCall, content: toolCallSummary(event.ToolName, event.ToolInput), leadBlank: true})
		a.debugWriteSection("Tool Call", fmt.Sprintf("[%s] %s\n%s",
			time.Now().Format("15:04:05"), event.ToolName, string(event.ToolInput)))
		a.toolStartTime = time.Now()
		if a.toolTimer != nil {
			a.toolTimer.Stop()
		}
		a.toolTimer = time.NewTicker(100 * time.Millisecond)
		go func(ticker *time.Ticker, ch chan any) {
			for range ticker.C {
				select {
				case ch <- toolTimerTickMsg{}:
				default:
					// Don't block if resultCh is full — skip this tick.
				}
			}
		}(a.toolTimer, a.resultCh)
		a.render()

	case EventToolResult:
		debugLog("tool_result: err=%v result=%q", event.IsError, truncateForLog(event.ToolResult, 500))
		if a.toolTimer != nil {
			a.toolTimer.Stop()
			a.toolTimer = nil
		}
		a.toolStartTime = time.Time{}
		result := collapseToolResult(event.ToolResult)
		a.needsTextSep = true
		a.sessionToolResults++
		a.sessionToolBytes += len(event.ToolResult)
		if a.sessionToolStats == nil {
			a.sessionToolStats = make(map[string][2]int)
		}
		if event.ToolName != "" {
			s := a.sessionToolStats[event.ToolName]
			s[0]++
			s[1] += len(event.ToolResult)
			a.sessionToolStats[event.ToolName] = s
		}
		errLabel := ""
		if event.IsError {
			errLabel = " [ERROR]"
		}
		a.debugWriteSection("Tool Result", fmt.Sprintf("[%s] %s%s (%s)\n%s",
			time.Now().Format("15:04:05"), event.ToolName, errLabel,
			formatDuration(event.Duration), event.ToolResult))
		a.messages = append(a.messages, chatMessage{kind: msgToolResult, content: result, isError: event.IsError, duration: event.Duration})
		a.render()

	case EventUsage:
		if event.Usage != nil {
			a.sessionCostUSD += computeCost(a.models, event.Model, *event.Usage)
			a.lastInputTokens = event.Usage.InputTokens + event.Usage.CacheReadInputTokens + event.Usage.CacheCreationInputTokens
			a.sessionInputTokens += event.Usage.InputTokens
			a.sessionOutputTokens += event.Usage.OutputTokens
			a.sessionCacheRead += event.Usage.CacheReadInputTokens
			a.sessionLLMCalls++
			// Track main-agent tokens separately (sub-agent events have a different AgentID).
			if a.agent != nil && event.AgentID == a.agent.ID() {
				a.mainAgentInputTokens += event.Usage.InputTokens
				a.mainAgentOutputTokens += event.Usage.OutputTokens
				a.mainAgentLLMCalls++
			}
			a.debugWriteSection("Usage", fmt.Sprintf("[%s] model=%s input=%d output=%d cache_read=%d cache_create=%d cost=%s",
				time.Now().Format("15:04:05"), event.Model,
				event.Usage.InputTokens, event.Usage.OutputTokens,
				event.Usage.CacheReadInputTokens, event.Usage.CacheCreationInputTokens,
				formatCost(computeCost(a.models, event.Model, *event.Usage))))
			a.renderInput()
		}

	case EventToolCallDone:
		// Already handled by EventToolResult

	case EventApprovalReq:
		debugLog("approval_req: %s", event.ApprovalDesc)
		a.awaitingApproval = true
		a.approvalPauseStart = time.Now()
		a.approvalSummary = approvalShortDesc(event.ToolName, event.ToolInput)
		a.approvalDesc = event.ApprovalDesc
		a.debugWriteSection("Approval Request", fmt.Sprintf("[%s] %s: %s",
			time.Now().Format("15:04:05"), event.ToolName, event.ApprovalDesc))
		// Stop tool timer ticker so the tool box timer freezes during approval.
		if a.toolTimer != nil {
			a.toolTimer.Stop()
			a.toolTimer = nil
		}
		a.renderInput()

	case EventCompacted:
		debugLog("compacted: nodeID=%s", event.NodeID)
		if event.NodeID != "" {
			a.agentNodeID = event.NodeID
		}
		a.debugWriteSection("Compacted", fmt.Sprintf("[%s] nodeID=%s %s",
			time.Now().Format("15:04:05"), event.NodeID, event.Text))
		a.messages = append(a.messages, chatMessage{kind: msgInfo, content: event.Text})
		a.render()

	case EventDone:
		debugLog("done: nodeID=%s streamingLen=%d", event.NodeID, len(a.streamingText))
		a.agentRunning = false
		a.cancelSent = false
		if a.agentTicker != nil {
			a.agentTicker.Stop()
			a.agentTicker = nil
		}
		a.agentElapsed = a.agentElapsedTime()
		a.agentDisplayInTok = float64(a.mainAgentInputTokens)
		a.agentDisplayOutTok = float64(a.mainAgentOutputTokens)
		if event.NodeID != "" {
			a.agentNodeID = event.NodeID
		}
		// Clean up completed sub-agent display entries.
		for id, sa := range a.subAgents {
			if sa.done {
				delete(a.subAgents, id)
			}
		}
		if a.streamingText != "" {
			a.debugWriteSection("Assistant Text", a.streamingText)
			a.messages = append(a.messages, chatMessage{
				kind:      msgAssistant,
				content:   a.streamingText,
				leadBlank: a.needsTextSep,
			})
			a.streamingText = ""
		}
		a.debugWriteSessionSummary()
		a.render()

	case EventSubAgentStart:
		sa := a.getOrCreateSubAgent(event.AgentID)
		sa.task = truncateTaskLabel(event.Task)
		a.debugWriteSection("Sub-Agent Start", fmt.Sprintf("[%s] agent=%s task=%s",
			time.Now().Format("15:04:05"), shortID(event.AgentID), event.Task))
		a.render()

	case EventSubAgentDelta:
		// Update the agent's status with a snippet of the streaming text.
		sa := a.getOrCreateSubAgent(event.AgentID)
		snippet := strings.TrimSpace(event.Text)
		if snippet != "" {
			// Show last meaningful text fragment as status.
			if len(snippet) > 60 {
				snippet = snippet[:60] + "…"
			}
			sa.status = snippet
		}
		a.render()

	case EventSubAgentStatus:
		sa := a.getOrCreateSubAgent(event.AgentID)
		if event.Text == "done" {
			sa.done = true
			completionMsg := fmt.Sprintf("[agent %s] completed: %s", shortID(event.AgentID), sa.task)
			if event.Usage != nil && (event.Usage.InputTokens > 0 || event.Usage.OutputTokens > 0) {
				completionMsg += fmt.Sprintf(" (↑%s ↓%s",
					formatTokenCount(event.Usage.InputTokens),
					formatTokenCount(event.Usage.OutputTokens))
				if event.Task != "" {
					completionMsg += ", " + event.Task
				}
				completionMsg += ")"
			}
			a.debugWriteSection("Sub-Agent Done", fmt.Sprintf("[%s] agent=%s %s",
				time.Now().Format("15:04:05"), shortID(event.AgentID), completionMsg))
			a.messages = append(a.messages, chatMessage{
				kind:    msgInfo,
				content: completionMsg,
			})
		} else {
			sa.status = event.Text
		}
		a.render()

	case EventStreamClear:
		// Discard in-progress streaming text before a stream retry so the
		// user doesn't see duplicate partial content.
		a.streamingText = ""
		a.debugWriteSection("Stream Clear", fmt.Sprintf("[%s] discarded partial streaming text",
			time.Now().Format("15:04:05")))
		a.render()

	case EventRetry:
		errMsg := "unknown error"
		if event.Error != nil {
			errMsg = event.Error.Error()
		}
		retryMsg := fmt.Sprintf("API error, retrying in %s (attempt %d/%d): %s",
			event.Duration.Truncate(time.Second), event.Attempt, event.MaxRetry, errMsg)
		debugLog("retry: %s", retryMsg)
		a.debugWriteSection("Retry", fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), retryMsg))
		a.messages = append(a.messages, chatMessage{kind: msgInfo, content: retryMsg})
		a.render()

	case EventError:
		errMsg := "Agent error"
		if event.Error != nil {
			errMsg = event.Error.Error()
		}
		debugLog("error: %s", errMsg)
		a.debugWriteSection("Error", fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), errMsg))
		a.messages = append(a.messages, chatMessage{kind: msgError, content: errMsg})
		a.render()
	}
}
