# Test Coverage Improvement Plan

**Goal:** Bring all major features to solid test coverage. Currently config.go and models.go are well-tested (~85%), but several critical files have thin or zero coverage.

## Current State

| File | Lines | Current Coverage | Priority |
|------|-------|-----------------|----------|
| agent.go | 629 | **0%** — no tests at all | Urgent |
| tools.go | 469 | ~30% — helper funcs untested | High |
| subagent.go | 247 | ~20% — only definition tests | High |
| compact.go | 193 | ~60% — missing helper funcs | Medium |
| container.go | 300 | ~50% — no Rebuild test | Medium |
| tree.go | 568 | ~35% — thin on complex cases | Medium |
| markdown.go | 215 | ~40% — missing edge cases | Medium |
| systemprompt.go | 72 | ~30% — thin on edge cases | Low |
| history.go | 214 | ~65% — missing Save() test | Low |
| worktree.go | 286 | ~70% — decent but some gaps | Low |

Files NOT in scope: main.go (TUI — hard to unit test, would need separate integration test approach), term.go (trivial), dockerfiles.go (constant).

---

## Phase 1: Agent Core (agent.go)

This is the most critical untested file — the entire agent orchestration layer.

- [x] 1a: Test `NewAgent()` with various option funcs (`WithContextWindow`, `WithExplorationModel`, `WithMaxToolIterations`) — verify agent fields are set correctly
- [x] 1b: Test `newLangdagClient()` — verify it selects the correct provider client based on model ID, test fallback when provider keys are missing, test error paths
- [x] 1c: Test `newLangdagClientForProvider()` — verify each provider branch (Anthropic, OpenAI, Grok, Gemini) and invalid provider error
- [x] 1d: Test `generateAgentID()` — verify format, uniqueness across calls
- [x] 1e: Test `langdagStoragePath()` — verify path construction, directory creation
- [x] 1f: Test `Run()` and `runLoop()` with a mock langdag client — verify: (1) text streaming emits TextDelta events, (2) tool calls dispatch to correct tool and emit ToolCallStart/ToolCallDone events, (3) approval-required tools pause and resume on Approve(), (4) Cancel() stops the loop, (5) context window exceeded triggers clearing, (6) max tool iterations terminates loop
- [x] 1g: Test `emit()` and `emitUsage()` — verify events are delivered to the channel, verify AgentID is set

## Phase 2: Tools & Sub-Agent

- [x] 2a: Test `truncateOutput()` in tools.go — verify line limit (200 lines), byte limit (30KB), exact boundary behavior, empty input
- [x] 2b: Test `gitArgsContainForce()` — verify detection of `--force`, `-f`, `--force-with-lease` in various argument positions, verify false for normal args
- [x] 2c: Test `gitCredentialHint()` — verify detection of common credential error patterns (authentication failed, could not read username, permission denied), verify no false positives on normal error output

**Parallel Tasks: 2d, 2e**

- [x] 2d: Test `BashTool.Execute()` — verify command is passed to container exec, output truncation is applied, error handling for container failures
- [x] 2e: Test `GitTool.Execute()` and `GitTool.RequiresApproval()` — verify allowed subcommands pass, disallowed subcommands rejected, force-push requires approval, co-author trailer appended on commit

- [x] 2f: Test `SubAgentTool.Execute()` — verify sub-agent is spawned with correct parameters, output is returned, events forwarded to parent channel, agent_id resume works, depth exceeded error, output truncation at 30KB

## Phase 3: Compact & Tree

- [x] 3a: Test `extractAssistantText()` — verify extraction from various content block formats (text, tool_use, mixed)
- [x] 3b: Test `parseAssistantContent()` — verify parsing of assistant message content with tool calls and text blocks
- [x] 3c: Test `parseToolResults()` and `isToolResultContent()` — verify tool result JSON parsing, edge cases with malformed content
- [x] 3d: Test `compactConversation()` error paths — storage read failure, LLM call failure mid-compaction, empty conversation

**Parallel Tasks: 3e, 3f**

- [x] 3e: Test `renderTree()` with complex structures — multiple tool call chains, deeply nested conversations, cost/token display accuracy, model name shortening edge cases
- [x] 3f: Test `rebuildChatMessages()` — verify correct reconstruction of langdag messages from node chain, handle old-format tool nodes (NodeTypeToolCall/NodeTypeToolResult)

## Phase 4: Container, Markdown & Remaining Gaps

**Parallel Tasks: 4a, 4b, 4c**

- [x] 4a: Test `ContainerClient.Rebuild()` — verify old container stopped, new container started with new image, error handling for build/start failures
- [x] 4b: Test markdown edge cases — nested formatting (bold inside italic), unclosed markers, empty markers (`****`), multi-line code blocks with language tags, link formatting, ANSI sequence preservation across wrapping
- [x] 4c: Test `buildSystemPrompt()` edge cases — empty tools list, nil skills, missing template files, personality with special characters

- [x] 4d: Test `History.Save()` — verify file persistence, round-trip with Load(), handle write errors
- [x] 4e: Test worktree gaps — `listWorktrees()` with mixed clean/dirty/active states, `createWorktree()` error paths (git command failures, directory already exists), lock contention between processes

## Success Criteria

- `go test ./... -cover` shows ≥70% coverage for every file in `cmd/herm/` (excluding main.go)
- All new tests pass in CI (`go test ./...`)
- No test relies on real Docker, real API keys, or network access — mock all external dependencies
- Each phase's tests catch at least one real edge case or latent bug
