# Fix max_tokens crash chain

Root cause: when an LLM response hits max_tokens mid-tool-call, langdag saves an empty/broken assistant node. Follow-up messages include this empty node, producing `{"type":"text","text":""}` which the Anthropic API rejects with 400. The conversation is permanently broken.

Trace: `.herm/debug/debug-20260327-142925.json` (session 4ca76a1f, $1.10 wasted).

## Codebase context

**langdag** (local at `external-deps-workspace/langdag/`):
- `internal/conversation/conversation.go` — `streamResponse()` hardcodes `MaxTokens: 4096` (line 195), ignoring `WithMaxTokens()`. Saves assistant node unconditionally when `response != nil || fullText != ""` (line 223). `toContentBlockArray()` (line 349) wraps empty strings as `{"type":"text","text":""}`.
- `langdag.go` — `Prompt()` and `PromptFrom()` accept `WithMaxTokens()` but never pass `o.maxTokens` to the conversation manager.
- `internal/provider/anthropic/protocol.go` — `convertMessages()` (line 95) creates text blocks without checking for empty text. `processStreamEvents()` may finalize a response with partial content blocks on max_tokens truncation.

**herm** (at `cmd/herm/`):
- `agent.go` — `buildPromptOpts()` sets `WithMaxTokens(8192)` (line 562) which is currently ignored. `runLoop()` doesn't handle `stop_reason=max_tokens`. `drainStream()` returns it as a string but the loop doesn't check.

## Phase 1: Wire WithMaxTokens through langdag

The option exists but is silently ignored. This is the foundation — all other fixes depend on the model actually getting the right limit.

- [ ] 1a: Add `maxTokens int` parameter to `conversation.Manager.Prompt()` and `PromptFrom()` signatures, pass it through to `streamResponse()` which uses it instead of the hardcoded 4096 (fall back to 4096 if 0)
- [ ] 1b: Update `langdag.Client.Prompt()` and `PromptFrom()` to pass `o.maxTokens` to the conversation manager
- [ ] 1c: Add tests in `conversation_test.go` — verify the `CompletionRequest.MaxTokens` passed to the provider matches the value set via option (use mock provider to capture the request)
- [ ] 1d: Add test in `langdag_test.go` — end-to-end test that `WithMaxTokens(N)` propagates to the provider

**Success criteria:** `WithMaxTokens(8192)` from herm results in `CompletionRequest.MaxTokens == 8192` at the provider.

## Phase 2: Filter empty text blocks in message construction

Prevents the 400 crash even if an empty node somehow gets persisted.

- [ ] 2a: In `toContentBlockArray()`, skip creating a text block when the text is empty (return empty slice instead). In `convertMessages()`, skip text blocks where `block.Text == ""` (line 95) and skip empty-string assistant messages (line 152). Both Anthropic-side and conversation-side need protection.
- [ ] 2b: Add tests: `toContentBlockArray` with empty string input returns empty slice. `convertMessages` with an empty-text assistant message produces a valid (non-empty) message or is omitted. `buildMessages` with an empty-content assistant node produces valid output.

**Success criteria:** An assistant node with empty content never produces a 400 API error on the next call.

## Phase 3: Handle max_tokens truncation in langdag streamResponse

When `stop_reason=max_tokens`, the response may contain partial/incomplete content. The current code saves whatever it got (which can be empty or broken). Instead: save what we have (it may contain valid text or complete tool_use blocks), but expose the stop_reason so callers can detect truncation.

- [ ] 3a: In `streamResponse()`, include `StopReason` on the `CompletionResponse` in the saved node metadata (it already exists on the response struct but isn't used downstream). Expose it through the `StreamEventDone` event so `StreamChunk.StopReason` reflects it. Currently `StopReason` on the Done chunk comes from langdag.go `buildResult()` — verify the full chain from `processStreamEvents` → `StreamEventDone.Response.StopReason` → `StreamChunk.StopReason`.
- [ ] 3b: In `streamResponse()`, when `stop_reason=max_tokens` and nodeContent is empty (no text, no complete tool_use blocks), skip node creation entirely and emit a `StreamEventError` with a clear message like "response truncated at max_tokens with no usable content". This prevents the empty-node scenario.
- [ ] 3c: Add tests using mock provider: simulate a `max_tokens` response with empty content → verify no node is saved and an error event is emitted. Simulate a `max_tokens` response with partial text → verify the text node IS saved (partial text is still useful). Simulate a `max_tokens` response with complete tool_use blocks → verify node is saved normally.

**Success criteria:** A max_tokens response with empty content does not create a broken node. A max_tokens response with useful content is still saved.

## Phase 4: Handle max_tokens in herm agent loop

Even with langdag fixes, herm should gracefully handle truncation rather than silently dropping content.

- [ ] 4a: In `runLoop()`, after `retryableStream()` returns, check if `stopReason == "max_tokens"`. If there are no tool calls and no text was emitted, emit an informative error event (e.g., "Response was truncated — output exceeded max_tokens. Try breaking the task into smaller steps.") and break the loop. If there IS text (the model produced partial text before hitting the limit), continue normally — the user sees the partial text, and the conversation state is valid for follow-up.
- [ ] 4b: Add test in `agent_test.go`: mock a `max_tokens` stop reason with empty response → verify agent emits an error event with guidance. Mock a `max_tokens` stop reason with partial text → verify agent continues normally.

**Success criteria:** Users see a helpful message instead of a cryptic 400 error chain.

## Phase 5: Reproduce the original bug and verify the fix

Build a deterministic reproduction that exercises the full crash chain, then verify all fixes together.

- [ ] 5a: In `langdag` — add an integration test that reproduces the exact scenario: mock provider returns a response with `stop_reason=max_tokens` and empty content on the first call, then the caller does a `PromptFrom()` to continue. Before the fix: this would produce an empty node and the follow-up call would fail. After the fix: no empty node is created, follow-up call succeeds.
- [ ] 5b: In `herm` — add an integration test in `agent_test.go` that simulates the full trace scenario: agent sends prompt → mock returns max_tokens with no content → verify agent emits appropriate error → verify the conversation state is not corrupted (a follow-up prompt works).

**Success criteria:** Tests fail on the old code and pass on the fixed code, covering the exact crash chain from the trace.

## Open questions

- **Should max_tokens auto-continue?** Some frameworks detect `stop_reason=max_tokens` and automatically re-prompt the model to continue. This would be valuable for large write operations but adds complexity to langdag (it doesn't have a tool loop). Decision: defer to a follow-up plan — for now, surface the truncation clearly so herm can handle it.
- **Should herm raise max_tokens for write operations?** The agent could detect when a `write_file` tool call is likely (context suggests large output) and bump max_tokens. Decision: defer — 8192 is reasonable once it's actually wired through. The model hit 4096 because the option was ignored.
- **Conversation-level prompt caching:** Only system prompt + tools are cached; conversation history is re-sent at full cost each turn. This is a significant cost issue but orthogonal to the crash bug. Defer to a separate plan.
