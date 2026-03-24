# Debug Mode: File-Based Conversation Logging

**Goal:** Replace in-TUI system prompt display with a file-based debug mode. When enabled, every conversation gets a debug file in `.herm/debug/` that logs everything: system prompts, tool calls/results, agent events, usage stats, user messages, and rendered output. The debug file is regenerated on window resize to match what's displayed. Non-interactive `--prompt --debug` mode enables agents to run herm and read debug files programmatically.

**Prior work:** The `DisplaySystemPrompts` config field and `--display-system-prompts` CLI flag currently inject system prompts as `msgSystemPrompt` messages into the chat panel. The existing `debugLog()` helper (`helpers.go:107-126`) writes to `~/.herm-debug.log` when `HERM_DEBUG=1` is set — this is a low-level diagnostic log, not the structured conversation debug log described here.

**Codebase context:**
- `config.go:15-32` — `Config` struct, includes `DisplaySystemPrompts bool`
- `config.go:164-173` — `ProjectConfig` struct (project-level overrides)
- `main.go:908-931` — CLI entry point, flag parsing (`--version`, `--display-system-prompts`)
- `main.go:74-189` — `App` struct with all state fields
- `agentui.go:206-212` — where `displaySystemPrompts` injects messages into chat
- `agentui.go:300-506` — `handleAgentEvent()` — processes all agent events
- `commands.go:47-68` — `/clear` handler resets conversation state
- `render.go:261-424` — `buildBlockRows()` converts messages → visual rows
- `render.go:525-705` — `buildInputRows()` builds footer/status bar
- `render.go:743-832` — `render()`, `renderFull()` — screen rendering
- `main.go:339-353` — SIGWINCH handler with 150ms debounce → `resizeMsg`
- `main.go:853-856` — `resizeMsg` handler calls `renderFull()`
- `systemprompt.go:45-91` — `buildSystemPrompt()` generates system prompt text
- `agentui.go:176-253` — `submitToAgent()` — sets up agent, tools, system prompt
- `helpers.go:107-126` — existing `debugLog()` (env-gated, appends to `~/.herm-debug.log`)

**Success criteria:**
- `debug_mode: true` in config → debug file created per conversation, path shown in footer
- `/clear` creates a new debug file (new conversation = new file)
- Debug file contains: system prompt, tool definitions, all agent events (text, tool calls, tool results, usage, errors, approvals, retries), user messages, session stats
- Window resize → debug file regenerated to reflect current display state
- Debug file content is NOT word-wrapped (raw content, unlike TUI)
- `herm --prompt "do something" --debug` runs non-interactively and writes debug file
- `DisplaySystemPrompts` continues to work for backward compat but is independent of debug mode
- Existing `HERM_DEBUG` / `debugLog()` is unaffected (separate concern)

**Open questions:**
1. Debug file format: plain text with section headers (like `── System Prompt ──`) or structured (JSON lines)? Plain text is more human-readable; JSONL is more machine-parseable for agent consumption. **Recommendation:** Plain text with clear delimiters — agents can parse headers, humans can read it directly.
2. Should `--prompt` mode reuse the existing `App.Run()` with TUI disabled, or be a separate code path? Reusing `App.Run()` with a headless flag avoids duplicating agent orchestration logic.
3. Debug file naming: `debug-<timestamp>.log` or `debug-<session-id>.log`? Session ID ties it to conversation history; timestamp is simpler for sorting.

---

## Phase 1: Debug File Infrastructure

Add config field, debug file creation/management, and the core logging writer. No TUI changes yet.

**Contract:** `App` gets a `debugFile *os.File` and a `debugWrite(section, content string)` method. Debug files live in `<repo>/.herm/debug/`. Each file is named `debug-<timestamp>.log`. The writer appends section-delimited blocks. When debug mode is off, `debugWrite` is a no-op.

**Failure modes:**
- `.herm/debug/` doesn't exist → create on first use
- File permissions prevent writing → log error to stderr, continue without debug
- Disk full → same graceful degradation

- [x] 1a: **Add `DebugMode` to Config and ProjectConfig** — Add `DebugMode bool` field to both `Config` (`config.go`) and `ProjectConfig`. Update `mergeConfigs()` to handle the new field. Remove `DisplaySystemPrompts` from being the mechanism for debug — it stays as its own independent toggle.

- [x] 1b: **Add `--debug` and `--prompt` CLI flags** — In `main()`, parse `--debug` (enables debug mode regardless of config) and `--prompt "<text>"` (non-interactive mode: submit prompt, wait for agent to finish, exit). Store both on the `App` struct as `cliDebug bool` and `cliPrompt string`.

- [x] 1c: **Create debug file manager** — New file `debuglog.go` with: `initDebugLog(repoRoot string) (*os.File, string, error)` creates `.herm/debug/` dir and opens `debug-<timestamp>.log`. `debugWrite(f *os.File, section string, content string)` writes `\n── <section> ──\n<content>\n` to the file. `closeDebugLog(f *os.File)`. Timestamp format: `20060102-150405` for filename-safe sorting.

- [x] 1d: **Wire debug file lifecycle into App** — Add `debugFile *os.File` and `debugFilePath string` to `App`. In `Run()`, after config is loaded and repo root is known, call `initDebugLog()` if debug mode is active (config or CLI flag). On app exit, close the file. Ensure `.herm/debug/` is gitignored (check/append to `.herm/.gitignore`).

---

## Phase 2: Log Conversation Events to Debug File

Hook into the event flow to write all significant events to the debug file.

**Contract:** Every user message, system prompt, tool call, tool result, agent event, and usage update is written to the debug file as it happens. Sections are clearly delimited. Content is NOT word-wrapped.

- [x] 2a: **Log system prompt and tool definitions** — In `submitToAgent()` (`agentui.go`), after building the system prompt and tools, call `debugWrite` with the full system prompt text and formatted tool definitions. This replaces what `displaySystemPrompts` did for the chat panel (though that flag still works independently).

- [x] 2b: **Log user messages** — In `submitToAgent()`, log the user's message text (and any attachments/pastes metadata) to the debug file before sending to the agent.

- [x] 2c: **Log all agent events** — In `handleAgentEvent()`, after the existing `debugLog()` call, write a structured entry to the debug file for each event type. Include: event type name, timestamp, tool name (if applicable), full content (not truncated — unlike the existing `debugLog` which truncates to 500 chars). For `EventUsage`, include full token breakdown and cost.

- [x] 2d: **Log session summary on agent done** — When `EventDone` is received, write a session stats section: total tokens (input/output/cache), cost, LLM calls, tool call count/bytes, elapsed time, per-tool breakdown from `sessionToolStats`.

---

## Phase 3: Debug File Path in Footer

Show the debug file path in the TUI footer when debug mode is active.

**Contract:** When debug mode is on, a new line appears in the footer area showing `debug: <path>` in dim style. It appears after the branch/cost line and before container status.

- [x] 3a: **Add debug path to footer** — In `buildInputRows()` (`render.go`), after the branch/cost/context line and before the container status line, add a `debug: <relative-path>` row when `a.debugFilePath != ""`. Use dim styling consistent with other footer lines. Show path relative to repo root for brevity.

---

## Phase 4: Regenerate Debug File on Resize

When the terminal window resizes, flush and regenerate the debug file to correspond to what's displayed.

**Contract:** On resize, the debug file is truncated and rewritten with the current conversation state. The system prompt, all messages, and current session stats are re-serialized. This ensures the debug file always reflects what the user sees (minus word-wrapping).

**Failure modes:**
- Regeneration during active streaming → include partial streaming text with a `[streaming...]` marker
- Large conversation → regeneration could be slow. Debounce already handles rapid resizes (150ms). For very large conversations this is bounded by message count, not terminal size.

- [x] 4a: **Add `regenerateDebugFile()` method** — Walks `a.messages`, writes each message to the debug file by section type (system prompt, user, assistant, tool call, tool result, info, error, etc.). Includes current session stats at the end. Includes streaming text if agent is running. Does NOT word-wrap any content.

- [x] 4b: **Call regenerate on resize** — In the `resizeMsg` handler (`main.go`), after `a.renderFull()`, call `a.regenerateDebugFile()` if debug mode is active.

- [x] 4c: **Call regenerate on /clear** — In the `/clear` handler (`commands.go`), close the old debug file, create a new one with `initDebugLog()`, and update `a.debugFilePath`. This gives each conversation its own debug file.

---

## Phase 5: Non-Interactive `--prompt --debug` Mode

Enable headless operation for agent-driven workflows.

**Contract:** `herm --prompt "message" --debug` runs without a TUI: submits the prompt, streams agent execution (logging everything to the debug file), waits for the agent to finish, prints the final assistant response to stdout, and exits with code 0 on success or 1 on error. The debug file path is printed to stderr at startup so the calling agent knows where to find it.

**Failure modes:**
- No API key configured → error message to stderr, exit 1
- Agent hangs → respect existing stream timeouts; after agent done or error, exit
- No repo root (not in a git repo) → use current directory for `.herm/debug/`

- [x] 5a: **Add headless mode to App** — Add `headless bool` field. When `cliPrompt` is set, skip terminal raw mode, skip TUI rendering, skip stdin goroutine. After config/container initialization completes, call `submitToAgent()` with the prompt text.

- [x] 5b: **Headless event loop** — Replace the interactive `Run()` select loop with a simplified loop that only drains agent events (no stdin, no resize, no tick rendering). On `EventDone`, print final assistant text to stdout. On `EventError`, print error to stderr. Exit after agent completes.

- [x] 5c: **Print debug file path to stderr** — At startup in headless mode, `fmt.Fprintf(os.Stderr, "debug: %s\n", debugFilePath)` so the calling process can locate and read the debug file.

---

## Phase 6: Tests

- [x] 6a: **Test debug file creation and writing** — Unit test `initDebugLog` creates the directory and file. Test `debugWrite` produces correctly delimited sections. Test that debug mode off → no file created.

- [x] 6b: **Test debug file regeneration** — Create an App with messages, call `regenerateDebugFile()`, verify the file contains all messages without word-wrapping.

- [x] 6c: **Test /clear creates new debug file** — Simulate `/clear`, verify old file is closed and new file is opened with a different name.

- [x] 6d: **Test headless mode end-to-end** — Use a mock agent/LLM to verify `--prompt --debug` produces debug output file and prints assistant response to stdout.
