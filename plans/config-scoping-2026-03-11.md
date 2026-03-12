# Config Scoping: Global vs Project

Introduce a two-layer config system where global settings (`~/.cpsl/config.json`) serve as defaults and project settings (`<repo>/.cpsl/config.json`) override them. Remove ThemeColor. Make container image naming deterministic from Dockerfile content — no longer stored in config.

## Codebase Context

- **Config struct** (config.go:13-28): Single flat struct with all settings. Loaded from `~/.cpsl/config.json` only.
- **`loadConfig()`** (config.go:134): Reads `~/.cpsl/config.json`, merges onto defaults. `loadConfigFrom(dir)` exists but is test-only.
- **`saveConfig(cfg)`** (config.go:192): Writes to `~/.cpsl/config.json`. `saveConfigTo(dir, cfg)` exists but is test-only.
- **`containerConfig()`** (config.go:104): Returns `ContainerConfig{Image}` from `Config.ContainerImage` or `defaultContainerImage`.
- **`buildContainerImage()`** (main.go:888): Writes embedded base Dockerfile if none exists, tags as `cpsl-<projectID[:8]>:dev`, always runs `docker build`.
- **DevEnvTool.buildAndReplace** (tools.go:353): Same `cpsl-<projectID[:8]>:dev` tag. Calls `onRebuild(imageName)` which saves image name to config.
- **`bootContainerCmd()`** (main.go:848): Uses `cfg.containerConfig()` as starting image, then overrides with built image.
- **`buildLogo(colorIndex)`** (main.go:400): Uses `Config.ThemeColor`, defaults to 4 (blue) if unset.
- **`/config` UI** (main.go:2800-2856): Two tabs — "API Keys" and "Settings". Saves to `~/.cpsl/config.json` only. Settings tab has: Paste Collapse, Container Image, Show System Prompt, Sub-Agent Max Turns, Personality.
- **`/model` command** (main.go:2557): Saves `ActiveModel` to global config.
- **`a.config`** on App struct: The single merged config used everywhere (~20+ access sites in main.go).
- **Project UUID**: `ensureProjectID(repoRoot)` reads/creates `.cpsl/project.json`. Used for worktree paths and image tags.
- **Repo root**: Resolved in `resolveWorkspaceCmd()` during init, stored as `a.worktreePath`.

## Design

### Two-layer config

```
~/.cpsl/config.json          → global config (API keys, global defaults)
<repo>/.cpsl/config.json     → project config (overrides for this repo)
```

**Global-only fields** (never overridden per-project):
- API keys (Anthropic, OpenAI, Grok, Gemini)
- PasteCollapseMinChars
- DisplaySystemPrompts
- HistoryMaxEntries
- ModelSortCol, ModelSortDirs

**Per-project overridable** (project value wins if set, else global):
- ActiveModel
- Personality
- SubAgentMaxTurns

**Removed**:
- ThemeColor (hardcode default in buildLogo — terminal theme suffices)
- ContainerImage (derived deterministically, not stored)

New struct for project overrides:

```go
type ProjectConfig struct {
    ActiveModel      string `json:"active_model,omitempty"`
    Personality      string `json:"personality,omitempty"`
    SubAgentMaxTurns int    `json:"sub_agent_max_turns,omitempty"`
}
```

Merge logic: start from global Config, overlay non-zero ProjectConfig fields. The merged result becomes `a.config` — all existing code reads from it unchanged.

App struct gains:

```go
globalConfig  Config        // loaded from ~/.cpsl/config.json
projectConfig ProjectConfig // loaded from <repo>/.cpsl/config.json
config        Config        // merged effective config (derived)
repoRoot      string        // needed for project config path
```

### Deterministic container image naming

Image tag = `cpsl-<projectID[:8]>:<dockerfileHash[:12]>` where hash is SHA-256 of the Dockerfile content.

On startup (`buildContainerImage`):
1. Determine Dockerfile content (user's `.cpsl/Dockerfile` or write embedded base)
2. Compute SHA-256 hash of content
3. Construct tag: `cpsl-<projectID[:8]>:<hash[:12]>`
4. Check if image exists: `docker image inspect <tag>` (exit 0 = exists)
5. If exists → skip build (cache hit), return tag
6. If not → `docker build`, return tag

Benefits:
- Dockerfile changes → hash changes → rebuild. Same Dockerfile → cache hit → instant.
- No config persistence needed. Image name is always derivable.
- `devenv build` uses the same scheme — after writing new Dockerfile, hash changes, new image built.

The `onRebuild` callback no longer saves to config. It updates `a.containerImage` (new runtime field on App) which feeds the system prompt and sub-agents.

### Config UI restructure

Three tabs: **API Keys** | **Global** | **Project**

- **API Keys**: Same as today (Anthropic, OpenAI, Grok, Gemini).
- **Global**: Global-only fields + global defaults for overridable fields. Contains: Paste Collapse, Show System Prompt, Personality (default), Sub-Agent Max Turns (default).
- **Project**: Per-project overrides. Contains: Active Model, Personality, Sub-Agent Max Turns. Each field shows "(global: X)" when not overridden at project level. Clearing a field falls back to global.

Saving from Global tab → writes `~/.cpsl/config.json`.
Saving from Project tab → writes `<repo>/.cpsl/config.json`.
Both tabs save independently — Ctrl+S saves whichever tab is active? No, simpler: Ctrl+S saves all changes (global fields to global file, project fields to project file).

### `/model` command

Currently saves to global config. Change to: save to project config by default (you're choosing a model for this project). Global default is set via the Global tab in `/config`.

## Failure Modes

- **No repo root** (launched outside a git repo): No project config loaded, all settings are global. Project tab in `/config` shows "no project" and is non-editable.
- **Malformed project config**: Fall back to global values (same pattern as existing malformed global config handling).
- **Dockerfile hash collision**: SHA-256 with 12 hex chars = 48 bits. Practically impossible for a single project's history of Dockerfiles.
- **Stale Docker images**: Old `cpsl-<pid>:<oldhash>` images accumulate. Not a concern — Docker manages disk. Could add cleanup later.
- **Project config in .gitignore**: The `.cpsl/` directory is already gitignored in this project. Users who want shared project config can un-ignore `.cpsl/config.json` specifically.

## Phase 1: Remove ThemeColor

- [x] 1a: Remove `ThemeColor` field from Config struct. Update `buildLogo` to hardcode default color (4, blue). Remove `theme_color` from JSON serialization
- [x] 1b: Update config tests if any reference ThemeColor

## Phase 2: Deterministic container image naming

- [x] 2a: Add `containerImage` field to App struct (runtime, not persisted). Update system prompt and sub-agent code to read from `a.containerImage` instead of `a.config.ContainerImage`
- [x] 2b: Update `buildContainerImage`: read Dockerfile content, compute SHA-256 hash, construct tag as `cpsl-<projectID[:8]>:<hash[:12]>`. Check image existence with `docker image inspect` before building. Remove `cfg Config` parameter (no longer needed)
- [x] 2c: Update `DevEnvTool.buildAndReplace` to use same hash-based naming
- [x] 2d: Update `bootContainerCmd`: no longer needs `cfg.containerConfig()` for initial image. Use `defaultContainerImage` as fallback only when build fails. Update `onRebuild` callback to set `a.containerImage` instead of saving to config
- [x] 2e: Remove `ContainerImage` from Config struct, `containerConfig()` method, `/config` UI field, and `defaultContainerImage` references that are no longer needed. Keep the constant for the fallback image
- [x] 2f: Update tests for buildContainerImage, containerConfig removal, and config serialization

## Phase 3: Project config layer

- [x] 3a: Create `ProjectConfig` struct in config.go with fields: `ActiveModel`, `Personality`, `SubAgentMaxTurns`. Add `loadProjectConfig(repoRoot)` and `saveProjectConfig(repoRoot, cfg)` functions
- [x] 3b: Add `mergeConfigs(global Config, project ProjectConfig) Config` function. For each overridable field: use project value if non-zero, else global
- [x] 3c: Add `globalConfig`, `projectConfig`, `repoRoot` fields to App struct. Update `newApp()` / init to load both configs and merge into `a.config`
- [x] 3d: Update `/model` command to save `ActiveModel` to project config instead of global
- [x] 3e: Add tests for project config loading, saving, merging, and fallback behavior

## Phase 4: Config UI restructure

- [ ] 4a: Add third tab "Project" to cfgTabNames and cfgTabFields. Rename "Settings" to "Global"
- [ ] 4b: Move overridable fields to Project tab: Active Model (new), Personality, Sub-Agent Max Turns. Show "(global: X)" hint when field is not overridden. Keep global defaults for these in Global tab
- [ ] 4c: Update `exitConfigMode` save logic: write global fields to `~/.cpsl/config.json`, project fields to `<repo>/.cpsl/config.json`. Recompute merged config after save
- [ ] 4d: Handle no-repo-root case: Project tab shows "no project detected" and fields are non-editable
- [ ] 4e: Update config UI tests

## Success Criteria

- ThemeColor removed, logo uses hardcoded color
- Container image tag is deterministic from Dockerfile content — same Dockerfile = same tag = no rebuild
- `devenv build` produces tags with the new hash scheme
- ContainerImage is not in config.json (global or project)
- Project-level ActiveModel, Personality, SubAgentMaxTurns override global when set
- `/config` UI clearly shows global vs project scope
- `/model` saves to project config
- Launching outside a git repo works (global config only, project tab disabled)
- Existing `~/.cpsl/config.json` files with `container_image` or `theme_color` are gracefully ignored (omitempty + unused fields)
