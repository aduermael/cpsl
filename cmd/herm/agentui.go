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

// formatToolDefinitionsOptions is the parameter bundle for formatToolDefinitions.
type formatToolDefinitionsOptions struct {
	tools       []Tool
	serverTools []types.ToolDefinition
}

// formatToolDefinitions builds a compact display of all tool definitions
// the LLM receives, including client tools and server tools.
func formatToolDefinitions(opts formatToolDefinitionsOptions) string {
	var b strings.Builder
	b.WriteString("── Tool Definitions ──\n")
	for _, t := range opts.tools {
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
	for _, st := range opts.serverTools {
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
	offline := a.ollamaFetched && a.config.OllamaBaseURL != "" && a.isOllamaOffline(modelID)
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
		tools = append(tools, NewBashTool(NewBashToolOptions{Container: a.container, Timeout: 120}))
		tools = append(tools, NewGlobTool(a.container))
		tools = append(tools, NewGrepTool(a.container))
		tools = append(tools, NewReadFileTool(a.container))
		tools = append(tools, NewOutlineTool(a.container))
		tools = append(tools, NewEditFileTool(a.container))
		tools = append(tools, NewWriteFileTool(a.container))
		if a.worktreePath != "" {
			hermDir := filepath.Join(a.worktreePath, ".herm")
			cacheDir := filepath.Join(a.worktreePath, ".herm", "cache")
			mounts := []MountSpec{
				{Source: a.worktreePath, Destination: a.worktreePath},
				{Source: a.attachmentDir(), Destination: "/attachments", ReadOnly: true},
				{Source: cacheDir, Destination: "/cache", ReadOnly: false},
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
			tools = append(tools, NewDevEnvTool(NewDevEnvToolOptions{
				Container: a.container,
				HermDir:   hermDir,
				Workspace: a.worktreePath,
				Mounts:    mounts,
				ProjectID: projectID,
				OnRebuild: onRebuild,
				OnStatus:  onStatus,
			}))
		}
	}
	if a.worktreePath != "" {
		tools = append(tools, NewGitTool(NewGitToolOptions{WorkDir: a.worktreePath, CoAuthor: a.config.effectiveGitCoAuthor()}))
	}

	modelID := a.config.resolveActiveModel(a.models)
	if modelID == "" {
		a.messages = append(a.messages, chatMessage{kind: msgError, content: "model not found, `/model` to pick a valid one"})
		a.render()
		return
	}

	var modelProvider string
	if modelDef := findModelByID(findModelByIDOptions{models: a.models, id: modelID}); modelDef != nil {
		modelProvider = modelDef.Provider
	}

	// Server-side tools (e.g. web search) are handled by the LLM provider.
	// Some models don't support them, so we check before including them.
	var serverTools []types.ToolDefinition
	if supportsServerTools(supportsServerToolsOptions{provider: modelProvider, modelID: modelID, models: a.models}) {
		serverTools = []types.ToolDefinition{WebSearchToolDef()}
	}

	if modelProvider != "" && modelProvider != a.langdagProvider {
		if a.langdagClient != nil {
			a.langdagClient.Close()
		}
		client, err := newLangdagClientForProvider(newLangdagClientForProviderOptions{cfg: a.config, provider: modelProvider})
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

	workDir := a.worktreePath

	containerImage := a.containerImage
	if containerImage == "" {
		containerImage = defaultContainerImage
	}

	// Sub-agent tool: output-only communication, no shared memory.
	// Uses exploration model if configured, otherwise falls back to active model.
	exploreMaxTurns := a.config.ExploreMaxTurns
	if exploreMaxTurns <= 0 {
		exploreMaxTurns = a.config.SubAgentMaxTurns // legacy fallback
	}
	generalMaxTurns := a.config.GeneralMaxTurns
	if generalMaxTurns <= 0 {
		generalMaxTurns = a.config.SubAgentMaxTurns // legacy fallback
	}

	// Load tool descriptions from embedded markdown files, replacing dynamic placeholders.
	toolDescriptions = loadToolDescriptions(loadToolDescriptionsOptions{
		containerImage:  containerImage,
		workDir:         workDir,
		exploreMaxTurns: exploreMaxTurns,
		generalMaxTurns: generalMaxTurns,
	})
	maxDepth := a.config.MaxAgentDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxAgentDepth
	}
	explorationModelID := a.config.resolveExplorationModel(a.models)
	subAgentServerTools := serverTools
	if !supportsServerTools(supportsServerToolsOptions{provider: modelProvider, modelID: explorationModelID, models: a.models}) {
		subAgentServerTools = nil
	}
	subAgentTool := NewSubAgentTool(SubAgentConfig{
		Client:           a.langdagClient,
		Tools:            tools,
		ServerTools:      subAgentServerTools,
		MainModel:        modelID,
		ExplorationModel: explorationModelID,
		ExploreMaxTurns:  exploreMaxTurns,
		GeneralMaxTurns:  generalMaxTurns,
		MaxDepth:         maxDepth,
		WorkDir:          workDir,
		Personality:      a.config.Personality,
		ContainerImage:   containerImage,
	})
	tools = append(tools, subAgentTool)

	var wtBranch string
	if a.worktreePath != "" {
		wtBranch = worktreeBranch(a.worktreePath)
	}
	systemPrompt := buildSystemPrompt(buildSystemPromptOptions{
		tools:          tools,
		serverTools:    serverTools,
		skills:         skills,
		workDir:        workDir,
		personality:    a.config.Personality,
		containerImage: containerImage,
		worktreeBranch: wtBranch,
		snap:           a.projectSnap,
	})

	// Feed system prompt, tool definitions, and user message to trace collector.
	if a.traceCollector != nil {
		a.traceCollector.SetSystemPrompt(systemPrompt)
		var traceTools []TraceTool
		for _, t := range tools {
			def := t.Definition()
			traceTools = append(traceTools, TraceTool{
				Name:        def.Name,
				Description: def.Description,
				Parameters:  def.InputSchema,
			})
		}
		for _, st := range serverTools {
			traceTools = append(traceTools, TraceTool{
				Name:        st.Name,
				Description: st.Description,
				Parameters:  st.InputSchema,
			})
		}
		a.traceCollector.SetTools(traceTools)
		a.traceCollector.AddUserMessage(userMessage)
	}

	a.showModelChange(modelID)

	ctxWindow := 0
	if m := findModelByID(findModelByIDOptions{models: a.models, id: modelID}); m != nil {
		ctxWindow = m.ContextWindow
	}
	mainMaxIter := a.config.MaxToolIterations
	if mainMaxIter <= 0 {
		mainMaxIter = defaultMaxToolIterations
	}
	agent := NewAgent(NewAgentOptions{
		Client:        a.langdagClient,
		Tools:         tools,
		ServerTools:   serverTools,
		SystemPrompt:  systemPrompt,
		Model:         modelID,
		ContextWindow: ctxWindow,
	},
		WithExplorationModel(explorationModelID),
		WithMaxToolIterations(mainMaxIter),
		WithThinking(a.config.Thinking))
	subAgentTool.parentEvents = agent.events
	subAgentTool.onBgComplete = agent.InjectBackgroundResult
	a.agent = agent
	if a.traceCollector != nil {
		a.traceCollector.SetMainAgentID(agent.ID())
	}
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
	go agent.Run(context.Background(), RunOptions{UserMessage: userMessage, ParentNodeID: parentNodeID})
}

// hasActiveSubAgents returns true if any sub-agent in the display map is still running.
func (a *App) hasActiveSubAgents() bool {
	for _, sa := range a.subAgents {
		if !sa.done {
			return true
		}
	}
	return false
}

// forceCompleteSubAgents marks all active sub-agents as done. Called when the
// parent event channel is closed while sub-agents are still tracked as active,
// which means their "done" events were lost or the channel closed before they
// could be forwarded.
func (a *App) forceCompleteSubAgents() {
	for _, sa := range a.subAgents {
		if !sa.done {
			sa.done = true
			sa.completedAt = time.Now()
		}
	}
	if a.agentTicker != nil {
		a.agentTicker.Stop()
		a.agentTicker = nil
	}
	a.render()
}

// finalizeAgentTurn performs all state transitions needed when the agent finishes
// a turn. Called by both the EventDone handler (with the event's nodeID) and the
// doneCh backup path (with empty nodeID, since EventDone was dropped).
func (a *App) finalizeAgentTurn(nodeID string) {
	if !a.agentRunning {
		return // already finalized
	}
	a.agentRunning = false
	a.cancelSent = false
	a.traceUsageSeen = false
	// Keep the ticker running if sub-agents are still active so their
	// spinners and elapsed times keep updating.
	if a.agentTicker != nil && !a.hasActiveSubAgents() {
		a.agentTicker.Stop()
		a.agentTicker = nil
	}
	a.agentElapsed = a.agentElapsedTime()
	a.agentDisplayInTok = float64(a.mainAgentInputTokens)
	a.agentDisplayOutTok = float64(a.mainAgentOutputTokens)
	if nodeID != "" {
		a.agentNodeID = nodeID
	}
	// Don't delete completed sub-agents here — they persist in the
	// display until the next agent turn starts (cleared in startAgent).
	if a.streamingText != "" {
		a.messages = append(a.messages, chatMessage{
			kind:      msgAssistant,
			content:   a.streamingText,
			leadBlank: a.needsTextSep,
		})
		a.streamingText = ""
	}
	if a.traceCollector != nil {
		agentID := ""
		if a.agent != nil {
			agentID = a.agent.ID()
		}
		a.traceCollector.FinalizeTurn(agentID)
		a.traceCollector.Finalize()
		if err := a.traceCollector.FlushToFile(a.traceFilePath); err != nil {
			fmt.Fprintf(os.Stderr, "debug: failed to write trace: %v\n", err)
		}
	}
	a.render()
}

func (a *App) drainAgentEvents() {
	if a.agent == nil || (!a.agentRunning && !a.hasActiveSubAgents()) {
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
				if a.hasActiveSubAgents() {
					a.forceCompleteSubAgents()
				}
				return
			}
			a.handleAgentEvent(event)
		default:
			// No more buffered events. Check if doneCh signals completion
			// (backup for when EventDone was dropped from the full channel).
			select {
			case <-a.agent.DoneCh():
				// Agent is done. Drain any final events that arrived between
				// the default case above and now, then finalize the turn.
				for {
					select {
					case event, ok := <-a.agent.Events():
						if !ok {
							a.finalizeAgentTurn("")
							if a.hasActiveSubAgents() {
								a.forceCompleteSubAgents()
							}
							return
						}
						a.handleAgentEvent(event)
					default:
						a.finalizeAgentTurn("")
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
	case EventLLMStart:
		if a.traceCollector != nil {
			a.traceCollector.StartLLMResponse(event.AgentID)
		}

	case EventTextDelta:
		if a.traceCollector != nil {
			if a.traceUsageSeen {
				a.traceCollector.FinalizeTurn(event.AgentID)
				a.traceUsageSeen = false
			}
			a.traceCollector.AddTextDelta(AddTextDeltaOptions{agentID: event.AgentID, text: event.Text})
		}
		// Suppress main-agent narration while background sub-agents are
		// still running. The UI already shows live sub-agent status, so
		// filler text like "Hang tight..." is redundant noise.
		if a.hasPendingBackgroundAgents() {
			break
		}
			a.streamingText += event.Text
			if idx := strings.LastIndex(a.streamingText, "\n"); idx >= 0 {
				a.messages = append(a.messages, chatMessage{
					kind:      msgAssistant,
					content:   a.streamingText[:idx+1],
					leadBlank: a.needsTextSep,
				})
				a.needsTextSep = false
				a.streamingText = a.streamingText[idx+1:]
			}
		a.render()

	case EventToolCallStart:
		debugLog("tool_call_start: %s input=%s", event.ToolName, string(event.ToolInput))
		if a.streamingText != "" {
			a.messages = append(a.messages, chatMessage{
				kind:      msgAssistant,
				content:   a.streamingText,
				leadBlank: a.needsTextSep,
			})
			a.needsTextSep = false
			a.streamingText = ""
		}
		if a.traceCollector != nil {
			a.traceCollector.StartToolCall(StartToolCallOptions{agentID: event.AgentID, toolID: event.ToolID, toolName: event.ToolName, input: event.ToolInput})
		}
		// Suppress internal tool calls (agent status checks, sleep waits, background agent spawns) from the UI.
		if isAgentStatusCheck(isAgentStatusCheckOptions{toolName: event.ToolName, input: event.ToolInput}) || isSleepWaitCommand(isSleepWaitCommandOptions{toolName: event.ToolName, input: event.ToolInput}) || isBackgroundAgentCall(isBackgroundAgentCallOptions{toolName: event.ToolName, input: event.ToolInput}) {
			if a.suppressedToolIDs == nil {
				a.suppressedToolIDs = make(map[string]bool)
			}
			a.suppressedToolIDs[event.ToolID] = true
			break
		}
		a.messages = append(a.messages, chatMessage{kind: msgToolCall, content: toolCallSummary(toolCallSummaryOptions{toolName: event.ToolName, input: event.ToolInput}), leadBlank: true, toolName: event.ToolName})
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
		debugLog("tool_result: err=%v result=%q", event.IsError, truncateForLog(truncateForLogOptions{s: event.ToolResult, max: 500}))
		if a.toolTimer != nil {
			a.toolTimer.Stop()
			a.toolTimer = nil
		}
		a.toolStartTime = time.Time{}
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
		if a.agent != nil && event.AgentID == a.agent.ID() {
			a.mainAgentToolCount++
		}
		if a.traceCollector != nil {
			a.traceCollector.EndToolCall(EndToolCallOptions{toolID: event.ToolID, result: event.ToolResult, isError: event.IsError, duration: event.Duration})
			a.traceCollector.FlushToFile(a.traceFilePath)
		}
		// Skip UI message for suppressed tool calls (e.g., agent status checks).
		if a.suppressedToolIDs[event.ToolID] {
			delete(a.suppressedToolIDs, event.ToolID)
			break
		}
		result := collapseToolResult(event.ToolResult)
		a.messages = append(a.messages, chatMessage{kind: msgToolResult, content: result, isError: event.IsError, duration: event.Duration, toolName: event.ToolName})
		a.render()

	case EventUsage:
		if a.traceCollector != nil && a.traceUsageSeen {
			a.traceCollector.FinalizeTurn(event.AgentID)
			a.traceUsageSeen = false
		}
		if event.Usage != nil {
			cost := computeCost(computeCostOptions{models: a.models, modelID: event.Model, usage: *event.Usage})
			a.sessionCostUSD += cost
			a.lastInputTokens = event.Usage.InputTokens + event.Usage.CacheReadInputTokens + event.Usage.CacheCreationInputTokens
			// Propagate cost to the main agent for system prompt budget display.
			if a.agent != nil {
				a.agent.SetSessionCost(a.sessionCostUSD)
			}
			a.sessionInputTokens += event.Usage.InputTokens
			a.sessionOutputTokens += event.Usage.OutputTokens
			a.sessionCacheRead += event.Usage.CacheReadInputTokens
			a.sessionLLMCalls++
			// Track main-agent tokens separately (sub-agent events have a different AgentID).
			if a.agent != nil && event.AgentID == a.agent.ID() {
				a.mainAgentInputTokens += event.Usage.InputTokens
				a.mainAgentOutputTokens += event.Usage.OutputTokens
				a.mainAgentLLMCalls++
			} else if a.agent != nil && event.AgentID != a.agent.ID() {
				// Accumulate live token counts for sub-agents.
				sa := a.getOrCreateSubAgent(event.AgentID)
				sa.inputTokens += event.Usage.InputTokens
				sa.outputTokens += event.Usage.OutputTokens
			}
			if a.traceCollector != nil {
				a.traceCollector.SetUsage(SetUsageOptions{
					agentID:    event.AgentID,
					model:      event.Model,
					nodeID:     event.NodeID,
					usage:      traceUsageFromTypes(event.Usage),
					costUSD:    cost,
					stopReason: event.StopReason,
				})
				a.traceCollector.FlushToFile(a.traceFilePath)
			}
			if a.agent != nil && event.AgentID == a.agent.ID() {
				a.traceUsageSeen = true
			}
			a.renderInput()
		}

	case EventToolCallDone:
		// Already handled by EventToolResult

	case EventApprovalReq:
		debugLog("approval_req: %s", event.ApprovalDesc)
		a.awaitingApproval = true
		a.approvalPauseStart = time.Now()
		a.approvalToolID = event.ToolID
		a.approvalSummary = approvalShortDesc(approvalShortDescOptions{toolName: event.ToolName, input: event.ToolInput})
		a.approvalDesc = event.ApprovalDesc
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
		if a.traceCollector != nil {
			a.traceCollector.AddCompaction(AddCompactionOptions{nodeID: event.NodeID, summary: event.Text})
		}
		a.messages = append(a.messages, chatMessage{kind: msgInfo, content: event.Text})
		a.render()

	case EventDone:
		debugLog("done: nodeID=%s streamingLen=%d", event.NodeID, len(a.streamingText))
		a.finalizeAgentTurn(event.NodeID)

	case EventSubAgentStart:
		sa := a.getOrCreateSubAgent(event.AgentID)
		sa.task = truncateTaskLabel(event.Task)
		sa.mode = event.Mode
		sa.startTime = time.Now()
		// If this is a retry, mark the old agent as replaced and inherit its task label.
		if event.RetryOf != "" {
			if old, ok := a.subAgents[event.RetryOf]; ok {
				old.replacedBy = event.AgentID
				if sa.task == "" {
					sa.task = old.task
				}
			}
		}
		// Insert a positional anchor for the sub-agent display group in the
		// message flow so that it renders between pre-spawn and post-spawn text.
		if !a.subAgentGroupInserted {
			a.messages = append(a.messages, chatMessage{kind: msgSubAgentGroup})
			a.subAgentGroupInserted = true
		}
		a.render()

	case EventSubAgentDelta:
		// Update the agent's status with a snippet of the streaming text.
		// No a.render() here — the 50ms agentTickMsg ticker handles display
		// updates. This avoids ~3000 renders per sub-agent response stream.
		sa := a.getOrCreateSubAgent(event.AgentID)
		snippet := strings.TrimSpace(event.Text)
		if snippet != "" {
			// Show last meaningful text fragment as status.
			if len(snippet) > 60 {
				snippet = snippet[:60] + "…"
			}
			sa.status = snippet
		}

	case EventSubAgentStatus:
		sa := a.getOrCreateSubAgent(event.AgentID)
		if event.Text == "done" {
			sa.done = true
			sa.completedAt = time.Now()
			sa.failed = event.IsError
			if event.Usage != nil {
				sa.inputTokens = event.Usage.InputTokens
				sa.outputTokens = event.Usage.OutputTokens
			}
			if a.traceCollector != nil && event.SubTrace != nil {
				a.traceCollector.AddSubAgent(event.SubTrace)
			}
			// Only emit a chat message if the agent failed — successful
			// completions are shown inline in the grouped display.
			if event.IsError {
				failMsg := fmt.Sprintf("[agent %s] failed: %s", shortID(event.AgentID), sa.task)
				a.messages = append(a.messages, chatMessage{
					kind:    msgInfo,
					content: failMsg,
				})
			}
			// If the main agent already stopped and this was the last active
			// sub-agent, stop the ticker that was kept alive for animations.
			if !a.agentRunning && !a.hasActiveSubAgents() && a.agentTicker != nil {
				a.agentTicker.Stop()
				a.agentTicker = nil
			}
			a.render()
		} else {
			// Tool start notifications and other status updates — no immediate
			// render needed. The 50ms agentTickMsg ticker will pick these up.
			sa.status = event.Text
			if strings.HasPrefix(event.Text, "tool:") {
				sa.toolCount++
			}
		}

	case EventStreamClear:
		// Discard in-progress streaming text before a stream retry so the
		// user doesn't see duplicate partial content.
		a.streamingText = ""
		if a.traceCollector != nil {
			a.traceCollector.AddStreamClear()
		}
		a.render()

	case EventRetry:
		errMsg := "unknown error"
		if event.Error != nil {
			errMsg = event.Error.Error()
		}
		retryMsg := fmt.Sprintf("API error, retrying in %s (attempt %d/%d): %s",
			event.Duration.Truncate(time.Second), event.Attempt, event.MaxRetry, errMsg)
		debugLog("retry: %s", retryMsg)
		if a.traceCollector != nil {
			a.traceCollector.AddRetry(AddRetryOptions{attempt: event.Attempt, maxAttempts: event.MaxRetry, delay: event.Duration, errMsg: errMsg})
		}
		a.messages = append(a.messages, chatMessage{kind: msgInfo, content: retryMsg})
		a.render()

	case EventError:
		errMsg := "Agent error"
		if event.Error != nil {
			errMsg = event.Error.Error()
		}
		debugLog("error: %s", errMsg)
		if a.traceCollector != nil {
			a.traceCollector.AddError(errMsg)
		}
		a.messages = append(a.messages, chatMessage{kind: msgError, content: errMsg})
		a.render()
	}
}
