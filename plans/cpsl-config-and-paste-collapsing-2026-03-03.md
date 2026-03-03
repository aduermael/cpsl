# Plan: .cpsl Config Folder, Paste Collapsing & /config Command

## Context

**Current state:** All settings are hardcoded constants in `main.go`. There is no config system, no `.cpsl/` folder, and no command handling. The app is a single-file bubbletea TUI (`main.go` ~360 lines) with a textarea input, viewport message feed, and expandable input height logic. Messages are stored as plain `[]string`.

**Key files:**
- `main.go` — entire app (model, Update, View, helpers)
- `model_test.go` — 24 tests using bubbletea's Update loop pattern
- `wrap_test.go` — 10 table-driven tests for `wrapLineCount`
- `go.mod` — uses `charm.land/bubbletea/v2`, `charm.land/bubbles/v2`, `charm.land/lipgloss/v2`

**New dependency needed:** `charm.land/huh/v2` (charm's form library for the `/config` command)

---

## Phase 1: .cpsl Config Folder & Config File

Create the `.cpsl/` directory and `config.json` on startup, and load config into the model.

- [x] 1a: Add a `config.go` file with the config struct, default values, load/save functions, and `.cpsl/` directory initialization. The config struct should include a `PasteCollapseMinChars int` field (default: 200). Use `os.UserConfigDir()` or current working directory (`.cpsl/config.json`) for path resolution — match Claude Code's per-project pattern (`.cpsl/` in CWD). Ensure `config.json` is created with defaults if it doesn't exist, and existing files are loaded and merged with defaults for forward-compat.
- [x] 1b: Integrate config loading into the app startup (`main()` / `initialModel()`). Store the loaded config in the `model` struct so it's available throughout the Update/View cycle. Handle errors gracefully (log warning, use defaults).
- [x] 1c: Add `.cpsl/` to `.gitignore` so it's not committed.
- [x] 1d: Add tests for config load/save — test default creation, round-trip (save then load), missing file fallback, malformed JSON fallback, and field merging when new fields are added.

---

## Phase 2: Paste Collapsing (Long Paste Detection)

Detect long pastes in the textarea and display them collapsed in the message feed. In the input textarea, pasted text appears normally (user can edit before sending). When sent, messages exceeding the configured character threshold show as `[pasted text | N chars]` in the viewport, with the full content revealed inline when the message is posted/displayed.

**Design decisions:**
- Detection: bubbletea v2's `tea.PasteMsg` (or `tea.PasteStartMsg`/`tea.PasteEndMsg` bracketed paste sequence) is the signal. When a paste event delivers text whose char count ≥ `PasteCollapseMinChars`, flag it.
- The model needs a richer message type than plain `string` — introduce a `message` struct with fields like `content string`, `isPaste bool`, `charCount int`.
- In the textarea (while composing): pasted text is inserted normally so the user can edit it.
- In the viewport (after sending): if `isPaste && charCount >= threshold`, render as `[pasted text #N | M chars]` with a muted style. Increment a paste counter per session.
- Full content is still stored and displayed below the collapsed header, wrapped normally, when the message is shown in the feed. The collapsed label acts as a header/annotation, not a hide/show toggle (keep it simple for now).

- [x] 2a: Introduce a `message` struct to replace the plain `[]string` in the model. Fields: `content string`, `isPaste bool`, `charCount int`. Update all existing code that reads/writes `m.messages` and the viewport content renderer.
- [x] 2b: Handle paste detection in `Update`. Listen for bubbletea's paste message type. When a paste arrives with char count ≥ config threshold, set a flag on the model (e.g., `pendingPaste bool`). When the user sends (Enter), check the flag to construct the message struct appropriately. Reset the flag after send.
- [x] 2c: Update `updateViewportContent()` to render paste messages with the collapsed style: a muted `[pasted text #N | M chars]` header line above the content. Non-paste messages render as before.
- [ ] 2d: Add tests for paste collapsing — test paste detection (above/below threshold), message struct creation, viewport rendering of collapsed vs normal messages, paste counter incrementing, and config threshold respected.

---

## Phase 3: /config Command with Huh Forms

Add a `/config` slash command that opens an interactive form (using `charm.land/huh/v2`) for editing `config.json` directly from the CLI.

**Design decisions:**
- Command parsing: when the user types `/config` and presses Enter, intercept it before treating it as a chat message. This is the first command, so keep the parsing simple — check if trimmed input starts with `/`.
- Mode switching: the model gets a `mode` field (e.g., `modeChat`, `modeConfig`). In `modeConfig`, the huh form owns the Update/View cycle. On form completion or abort (Esc/Ctrl+C on form), switch back to `modeChat`.
- The huh form is embedded as `*huh.Form` in the model. It's created fresh each time `/config` is entered, pre-populated with current config values.
- Form fields: `PasteCollapseMinChars` as a text input with integer validation (and any future config fields).
- On form completion: save updated config to `.cpsl/config.json` and update the in-memory config. On abort: discard changes.
- Theme: create a custom huh theme that matches cpsl's purple gradient aesthetic.

- [ ] 3a: Add `charm.land/huh/v2` dependency. Create a command parsing layer in `Update` — when input starts with `/`, route to command handling instead of sending as a message. Start with `/config` as the only command. Show an error message in the viewport for unknown commands.
- [ ] 3b: Add mode switching to the model (`modeChat` / `modeConfig`). When `/config` is invoked, create a huh form pre-populated with current config values, switch to `modeConfig`. In `modeConfig`, delegate `Update` and `View` to the huh form. On form completion, save config and return to `modeChat`. On form abort, discard and return to `modeChat`.
- [ ] 3c: Style the huh form with a custom theme matching cpsl's purple aesthetic (use the existing `borderGradientColors` and `logoColors` as reference). Ensure the form renders cleanly in the alt-screen context.
- [ ] 3d: Add tests for command parsing (recognizes `/config`, rejects unknown `/foo`, doesn't treat `/` in normal text as command), mode switching (entering and exiting config mode), and config form completion (values saved on confirm, discarded on abort).

---

## Phase 4: Integration Testing & Polish

- [ ] 4a: Add end-to-end integration tests that exercise the full flow: app starts → creates `.cpsl/` → paste a long text → see collapsed display → run `/config` → change threshold → verify new threshold applies. Use bubbletea's programmatic Update loop (same pattern as existing `model_test.go`).
- [ ] 4b: Ensure all existing tests still pass and update any that break due to the new `message` struct or config loading changes.

---

## Open Questions

1. **Paste detection mechanism:** Need to verify which exact message type bubbletea v2 uses for paste events (`tea.PasteMsg`, `tea.PasteStartMsg`/`tea.PasteEndMsg`, or content arriving via `tea.KeyMsg` with bracketed paste). This will be discovered during implementation by checking the bubbletea v2 API.
2. **Config file location:** Plan uses `.cpsl/config.json` in CWD (per-project, like Claude Code's `.claude/`). Should there also be a global config in `~/.cpsl/config.json`? For now, CWD-only keeps it simple.

## Success Criteria

- `cpsl` creates `.cpsl/config.json` on first run in any directory
- Long pasted text (≥ threshold chars) shows `[pasted text #N | M chars]` header in the message feed
- `/config` opens a smooth, purple-themed interactive form to edit settings
- Config changes persist to disk and take effect immediately
- All new behavior is covered by tests
- All existing tests continue to pass
