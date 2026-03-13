# Fun Animated Status Label

## Context

When the agent is running, main.go:1452-1455 shows a static `"thinking..."` in dim italic:

```go
} else if a.agentRunning {
    rows = append(rows, "\033[2;3mthinking...\033[0m")
    rows = append(rows, "")
}
```

This should become a fun animated label with:
1. **Rotating funny texts** instead of just "thinking..."
2. **Smooth pastel rainbow color cycling** (true color RGB, not stepping through discrete colors)
3. **Elapsed time** shown as seconds with 2 decimal digits (e.g. `12.34s`)
4. **On completion**, the time stays visible but the label dims to match tool block styling (`\033[2m`)

### Key files

- **main.go:1452-1455** — current "thinking..." display in `buildBlockRows()`
- **main.go:4031** — `a.agentRunning = true` when agent starts
- **main.go:4151** — `a.agentRunning = false` on `EventDone`
- **main.go:4074-4096** — existing tool timer tick pattern (ticker goroutine → `resultCh` → re-render)
- **main.go:1192-1305** — app struct fields
- **main.go:868-881** — `formatDuration()` for reference

### Existing timer pattern to follow

The tool timer already demonstrates the pattern: a `time.Ticker` fires every 100ms, a goroutine forwards ticks to `resultCh`, the main loop receives them and calls `render()`. The agent status timer should use an identical pattern but with its own ticker and message type.

### Color cycling approach

Use 24-bit true color (`\033[38;2;R;G;Bm`) with HSL→RGB conversion. Cycle hue continuously over time (e.g. 1 full rotation per ~4 seconds). Keep saturation at ~60-70% and lightness at ~75-80% for pastel tones. The hue advances each tick based on `time.Since(agentStartTime)`, producing smooth rotation.

### Funny texts

A static array of whimsical status messages. Rotate to the next one every ~3-4 seconds. Examples: "pondering the cosmos...", "consulting the oracle...", "herding electrons...", etc. The exact texts will be chosen during implementation — aim for ~15-20 entries that are short, varied, and lighthearted.

### Done state

When `agentRunning` becomes false, the final elapsed time should persist in the display. Store it in `a.agentElapsed`. Render it dim (`\033[2m`) to match tool blocks. No funny text — just the time.

## Phase 1: Agent timer infrastructure

Add timing fields and a ticker that drives re-renders while the agent is active.

- [x] 1a: Add fields to app struct: `agentStartTime time.Time`, `agentTicker *time.Ticker`, `agentElapsed time.Duration` (persists final time after agent stops), `agentTextIndex int` (which funny text is showing)
- [x] 1b: Add `agentTickMsg` type. On `agentRunning = true`, record `agentStartTime`, reset `agentElapsed`, start a 50ms ticker (faster than tool timer for smoother color), launch goroutine forwarding ticks to `resultCh`. On `EventDone`, stop ticker, store `agentElapsed = time.Since(agentStartTime)`
- [x] 1c: Handle `agentTickMsg` in the main event loop — call `render()` (same pattern as `toolTimerTickMsg`). Also advance `agentTextIndex` every ~3s based on elapsed time (index = `int(elapsed.Seconds() / 3) % len(funnyTexts)`)

## Phase 2: Funny texts and pastel color rendering

Replace the static "thinking..." with animated colored text and elapsed time.

- [x] 2a: Add a `var funnyTexts = []string{...}` array with ~15-20 short whimsical messages (all lowercase, with trailing "...")
- [x] 2b: Add `hslToRGB(h, s, l float64) (r, g, b int)` helper. Add `pastelColor(elapsed time.Duration) string` that returns `\033[38;2;R;G;Bm` with hue cycling ~1 rotation per 4 seconds, saturation 0.65, lightness 0.78
- [x] 2c: Replace the "thinking..." block in `buildBlockRows()` — while `agentRunning`, show: `{pastelColor}{funnyText}  {elapsed}s{reset}` where elapsed is `fmt.Sprintf("%.2f", time.Since(a.agentStartTime).Seconds())`. Add italic (`\033[3m`) for the text portion

## Phase 3: Done state display

When the agent finishes, show the final elapsed time in dim styling (matching tool blocks).

- [x] 3a: After `agentRunning` becomes false and `agentElapsed > 0`, show a dim line: `\033[2m{elapsed}s\033[0m` in the position where "thinking..." was. Clear `agentElapsed` when the user sends the next message (so it doesn't persist forever)
- [x] 3b: Verify the full lifecycle: agent starts → animated label with color cycling and time → agent stops → time remains in dim → user sends next message → label disappears

## Success Criteria

- While agent is active: a funny rotating status message with smooth pastel rainbow color and `XX.XXs` timer
- Color cycles smoothly through pastel hues (not stepping, not flickering)
- Text rotates to a new funny message every ~3 seconds
- On completion: only the final elapsed time remains, rendered dim (`\033[2m`) like tool blocks
- No goroutine leaks — ticker is always stopped
- 50ms tick rate for smooth color without excessive CPU
- Build passes, existing tests unaffected
