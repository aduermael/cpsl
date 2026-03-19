# Edit/Write File Tools and Herm Base Docker Image

Add dedicated `edit_file` and `write_file` tools to replace bash-based file editing, and ship a single official herm Docker image with all base tooling pre-installed.

## Codebase Context

- **Current tools** (filetools.go, tools.go): 7 tools — `bash`, `git`, `glob`, `grep`, `read_file`, `devenv`, `agent`. No file editing/writing tool exists. All file modifications go through bash.
- **Tool interface** (agent.go:87-95): `Definition() ToolDefinition`, `Execute(ctx, input) (string, error)`, `RequiresApproval(input) bool`. All container tools take a `*ContainerClient` and run commands via `container.Exec()`.
- **Base Dockerfile** (dockerfiles/base.Dockerfile): `FROM debian:bookworm-slim` + git, tree, ca-certificates, ripgrep. Embedded via `//go:embed` in dockerfiles.go.
- **Default image** (config.go:197): `defaultContainerImage = "debian:bookworm-slim"` — used as fallback when no built image exists.
- **Startup flow** (main.go:1210-1310): `bootContainerCmd()` → writes embedded BaseDockerfile to `.herm/Dockerfile` if missing → builds image → starts container.
- **DevEnv tool** (tools.go:284-460): Manages `.herm/Dockerfile` with read/write/build cycle. Image tag: `herm-<projectID>:<hash>`.
- **TUI rendering** (main.go:836-937): `renderToolBox()` draws bordered boxes with dim styling. `collapseToolResult()` truncates to first 2 + last 2 lines for >5 line output.
- **System prompt** (systemprompt.go, prompts/tools.md): Template-driven, tool sections included conditionally via `Has*` flags in `PromptData`.
- **Tool registration** (main.go:4363-4445): Tools created in `handleStartAgent()`, appended to `[]Tool`, passed to `NewAgent()`.

## Design

### CLI tools: `edit-file` and `write-file`

Two standalone Go binaries installed in the herm base image at `/usr/local/bin/`. Each reads JSON from stdin, performs the operation, and writes JSON to stdout.

**`edit-file`** — exact string replacement:
- Input: `{"file_path": "...", "old_string": "...", "new_string": "...", "replace_all": false}`
- Logic: read file → find `old_string` (must be unique unless `replace_all`) → replace → write atomically → compute unified diff
- Output: `{"ok": true, "diff": "--- a/path\n+++ b/path\n@@ ... @@\n..."}` or `{"ok": false, "error": "old_string not found"}`
- Error cases: file not found, old_string not found, old_string not unique (report count), old_string == new_string, write permission denied

**`write-file`** — create or overwrite:
- Input: `{"file_path": "...", "content": "..."}`
- Logic: check if file exists → create parent dirs → write atomically → compute diff (against old content if existed, or show "new file" summary)
- Output: `{"ok": true, "created": true, "diff": "..."}` or error
- Reports: line count, byte count, created vs overwritten

Both tools use Go's `os` package for file I/O and generate unified diffs using a pure-Go diff library (or a minimal inline implementation — unified diff of two strings is ~50 lines).

Source location: `tools/edit-file/main.go`, `tools/write-file/main.go` — separate Go modules with minimal dependencies.

### Herm base Docker image

One official image published to Docker Hub. Replaces `debian:bookworm-slim` as the default.

**Image contents** (debian:bookworm-slim base):
- `git`, `tree`, `ca-certificates`, `ripgrep` (existing)
- `python3` (new — useful for scripting, data tasks)
- `edit-file`, `write-file` (new — compiled Go binaries)
- `WORKDIR /workspace`

**Build**: Multi-stage Dockerfile at repo root (`Dockerfile`):
```
FROM golang:1.22-bookworm AS builder
COPY tools/ /build/tools/
RUN cd /build/tools/edit-file && go build -o /out/edit-file .
RUN cd /build/tools/write-file && go build -o /out/write-file .

FROM debian:bookworm-slim
COPY --from=builder /out/edit-file /out/write-file /usr/local/bin/
RUN apt-get update && apt-get install -y --no-install-recommends \
    git tree ca-certificates ripgrep python3 \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /workspace
```

**Naming**: `aduermael/herm:<tag>` on Docker Hub. Each herm binary version has a compile-time constant `hermImageTag` (e.g., `"0.1"`) that resolves to `aduermael/herm:0.1`. This ensures a given herm CLI always pulls a compatible image — new images can't break old CLI versions.

**Image publishing**: Manual `docker build` + `docker push` for now. CI automation deferred to a later plan.

**Integration changes**:
- `config.go`: Add `const hermImageTag = "0.1"` and change `defaultContainerImage` to `"aduermael/herm:" + hermImageTag`
- `dockerfiles/base.Dockerfile`: Change to `FROM aduermael/herm:0.1` + `WORKDIR /workspace` — a single-line passthrough since the base image already has everything. The tag here must match `hermImageTag`.
- `bootContainerCmd()` (main.go:1254): When no `.herm/Dockerfile` exists, skip the build entirely — pull and run the default image directly. Only build when a custom Dockerfile is present.
- DevEnv guidance (prompts/tools.md, .herm/skills/devenv.md): Recommend `FROM aduermael/herm:<tag>` as the base for all custom Dockerfiles.

### EditFileTool and WriteFileTool (Go agent side)

Follow the existing tool pattern in filetools.go. Each tool:
1. Accepts JSON input from the LLM
2. Pipes it to the CLI tool in the container via `container.Exec()`
3. Parses the JSON response
4. Returns the diff string (or error) as the tool result

**EditFileTool**:
- Name: `edit_file`
- Schema: `file_path` (required), `old_string` (required), `new_string` (required), `replace_all` (optional bool)
- Approval: No (same as bash — container is sandboxed)
- Result: The unified diff, or error message

**WriteFileTool**:
- Name: `write_file`
- Schema: `file_path` (required), `content` (required)
- Approval: No
- Result: Summary ("Created file.go (42 lines)") + diff if overwrite

### TUI: diff-aware rendering

Enhance `renderToolBox()` or add a post-processing step in the event handler to colorize unified diff output:
- Lines starting with `+` (not `+++`): green (`\033[32m`)
- Lines starting with `-` (not `---`): red (`\033[31m`)
- Lines starting with `@@`: cyan (`\033[36m`)
- `---`/`+++` header lines: bold

Update `toolCallSummary()` (main.go:713) for display:
- `edit_file`: `"~ edit file_path"` (truncated)
- `write_file`: `"~ write file_path"` (truncated)

Update `collapseToolResult()` to be smarter for diffs: show the `@@` hunk header + a few context lines rather than arbitrary first 2 / last 2 lines.

### System prompt updates

Add `HasEditFile` and `HasWriteFile` flags to `PromptData` (systemprompt.go:19).

New section in `prompts/tools.md`:
```
### edit_file, write_file
Dedicated file modification tools — prefer these over bash for all file changes.
- **edit_file**: Replace a specific string in a file. old_string must be unique (or use replace_all). Returns a unified diff showing exactly what changed.
- **write_file**: Create a new file or overwrite an existing one. Returns a summary and diff.
- Always read_file before editing to ensure correct context.
- Use edit_file for surgical changes. Use write_file for new files or full rewrites.
- Do NOT use bash for file modifications (echo, sed, awk, cat heredoc) — edit_file/write_file produce structured diffs and are safer.
```

Update bash section to reinforce: "Use bash for: running builds, tests, installs, and commands. Do NOT use bash for file editing — use edit_file/write_file instead."

### DevEnv: enforce herm base image

All container images must be based on `aduermael/herm:<tag>`. No degraded mode — the edit_file/write_file tools are always expected to be available.

**Enforcement in DevEnv tool**:
- When the agent writes a custom `.herm/Dockerfile`, the devenv tool validates that the `FROM` line references `aduermael/herm:<tag>` (where `<tag>` matches the current `hermImageTag`).
- If the FROM line uses a different base (e.g., `debian:bookworm-slim`, `node:20`, `alpine:3`), the devenv `build` action rejects it with a clear error: "Dockerfile must use FROM aduermael/herm:0.1 as the base image. Add your custom tools on top of it."
- The system prompt and devenv skill doc reinforce this — all custom Dockerfiles extend the herm base, never replace it.

**Enforcement at startup**:
- `buildContainerImage()` validates that `.herm/Dockerfile` (if present) starts with `FROM aduermael/herm:`. If not, it rewrites the file with the embedded template and logs a warning.

This guarantees that edit-file, write-file, ripgrep, git, and python3 are always present in every herm container. No probing or graceful degradation needed.

## Failure Modes

- **`edit-file` old_string not unique**: Return error with match count so the agent can provide more context. The agent retries with a longer old_string.
- **`edit-file` old_string not found**: Return error. Agent may have stale context — should re-read the file.
- **Docker Hub pull fails** (network/registry down): `docker pull` fails, container cannot start. User gets a clear error message to check their network/Docker setup. No fallback to a different image.
- **Custom Dockerfile with wrong base**: Rejected at build time with actionable error message pointing to the correct FROM line.
- **Large file diffs**: The diff output from the CLI tools should be truncated server-side (e.g., max 200 lines) to avoid blowing up context.

---

## Phase 1: CLI tools — edit-file and write-file
- [x] 1a: Create `tools/edit-file/` Go module with main.go — reads JSON from stdin, performs exact string replacement on the target file, writes JSON result with unified diff to stdout. Handle errors: file not found, string not found, string not unique, no-op (old == new)
- [x] 1b: Create `tools/write-file/` Go module with main.go — reads JSON from stdin, writes file content (creating parent dirs), outputs JSON result with diff (if overwrite) or creation summary
- [x] 1c: Unit tests for both CLI tools covering: successful edit, not-found, not-unique, replace_all, write new file, overwrite existing file, empty content, binary-safe paths with spaces

## Phase 2: Herm base Docker image
- [x] 2a: Create top-level `Dockerfile` for the herm base image: multi-stage build compiling edit-file and write-file from `tools/`, then debian:bookworm-slim with git, tree, ca-certificates, ripgrep, python3, and the compiled binaries
- [x] 2b: Add `const hermImageTag = "0.1"` to config.go. Update `defaultContainerImage` to `"aduermael/herm:" + hermImageTag`. Update `dockerfiles/base.Dockerfile` to `FROM aduermael/herm:0.1` + `WORKDIR /workspace`
- [x] 2c: Update `buildContainerImage()` in main.go: when no `.herm/Dockerfile` exists or when it matches the embedded template, skip the build — pull and run the default image directly. Only build when a custom Dockerfile is present
- [x] 2d: Add base image enforcement in devenv tool: validate that any `.herm/Dockerfile` uses `FROM aduermael/herm:<tag>` as its base. Reject builds with wrong base and return actionable error. Also validate at startup in `buildContainerImage()`
- [x] 2e: Update devenv skill doc and prompts/tools.md devenv section to require `FROM aduermael/herm:<tag>` as the base for all custom Dockerfiles, removing alpine references and `debian:bookworm-slim` recommendations

## Phase 3: EditFileTool and WriteFileTool
- [x] 3a: Add `EditFileTool` struct in filetools.go following existing pattern — pipes JSON input to `edit-file` CLI in container via `container.Exec()`, parses JSON output, returns diff string. Schema: file_path (required), old_string (required), new_string (required), replace_all (optional bool)
- [x] 3b: Add `WriteFileTool` struct in filetools.go — pipes JSON input to `write-file` CLI in container, returns creation summary or diff. Schema: file_path (required), content (required)
- [x] 3c: Register EditFileTool and WriteFileTool in `handleStartAgent()` alongside the other container tools (no probing needed — the herm base image always has them)

## Phase 4: TUI rendering for diffs
- [x] 4a: Add diff colorization in tool result rendering — detect unified diff format in edit_file/write_file results and apply ANSI colors (green for +, red for -, cyan for @@, bold for ---/+++ headers)
- [x] 4b: Add `toolCallSummary()` cases for edit_file (`"~ edit <path>"`) and write_file (`"~ write <path>"`)
- [x] 4c: Improve `collapseToolResult()` for diff output — show hunk headers and a balanced sample of changes rather than arbitrary first/last lines

## Phase 5: System prompt and guidance
- [x] 5a: Add `HasEditFile` and `HasWriteFile` flags to `PromptData`, set them in `buildSystemPrompt()` based on tool presence
- [x] 5b: Add `edit_file, write_file` section to prompts/tools.md with usage guidance (prefer over bash, read before edit, edit for changes, write for new files)
- [x] 5c: Update bash section in tools.md to explicitly say "Do NOT use bash for file editing — use edit_file/write_file instead"

## Phase 6: Tests
- [x] 6a: Unit tests for EditFileTool and WriteFileTool Go structs (mock container exec, verify command construction and JSON parsing)
- [x] 6b: Test that system prompt includes edit_file/write_file guidance when tools are present, and excludes it when not
- [ ] 6c: Test diff colorization and collapse logic with sample unified diff inputs

## Success Criteria
- Agent uses `edit_file` for targeted code changes instead of bash sed/echo — tool result shows a clean unified diff in the TUI with color
- Agent uses `write_file` for new files — result shows creation summary
- Fresh project startup pulls `aduermael/herm:0.1` without a build step — container has git, python3, ripgrep, edit-file, write-file available immediately
- Custom `.herm/Dockerfile` with `FROM aduermael/herm:0.1` + additional tools builds and works as before via devenv
- Attempting to use a non-herm base image in `.herm/Dockerfile` is rejected with a clear error
- Each herm CLI version pins a specific image tag — no `latest` drift
