# Container Attachment Mounts

Copy attached files to `.cpsl/attachments/<sessionID>/` on the host, mount at `/attachments` in the container, and clean up between agent runs.

## Codebase Context

- **Session identity**: Each CLI process is one `App` instance. `newUUID()` in `worktree.go` generates random UUIDs; we need a shorter 8-hex-char session ID.
- **Attachment storage**: `tryAttachFile()` in `main.go:2261` reads files and stores base64 in `App.attachments` map. This is the single point where files become attachments — the copy to disk should happen here.
- **Mount sites**: `[]MountSpec` is constructed in 3 places:
  - `bootContainerCmd()` (main.go:861) — initial container start. Receives `workspace` string from caller.
  - Worktree switch handler (main.go:2674) — restarts container with new workspace.
  - `startAgent()` (main.go:3115) — builds mounts for `DevEnvTool` (used for container rebuilds).
- **Container lifecycle**: `bootContainerCmd()` is called from the `workspaceMsg` handler (main.go:3390) which passes `wtPath`. The attachment dir must exist before mount, but can be empty.
- **System prompt**: `buildSystemPrompt()` in `systemprompt.go` — structured sections. Environment section at end includes mount info.

## Contracts & Interfaces

### Session ID
- 8 random hex characters generated via `crypto/rand` at `newApp()` time
- Stored as `App.sessionID string`

### Host directory layout
```
.cpsl/attachments/<sessionID>/        ← current run's files
.cpsl/attachments/<sessionID>/past/   ← files from previous runs
```

### Container mount
- Source: `.cpsl/attachments/<sessionID>/`
- Destination: `/attachments`
- Read-only: true (agent shouldn't modify originals)

### Per-run cleanup
- At the start of `startAgent()`, move all non-directory entries in the session's attachment dir into `past/` subfolder
- This gives the agent a clean `/attachments` that only contains files from the current message
- `past/` accumulates across runs within the same CLI session (acceptable — cleaned up when session ends)

### System prompt addition
- In the Environment section, add: `- Attachments mounted at: /attachments (files attached to the current message)`
- Only include this line when the bash tool is available (container is running)

## Phase 1: Session ID and attachment directory
- [x] 1a: Add `sessionID` field to `App` struct, generate 8 random hex chars in `newApp()` using `crypto/rand`
- [x] 1b: In `tryAttachFile()`, after storing the attachment in memory, copy the original file to `.cpsl/attachments/<sessionID>/` (create dir if needed, preserve original filename). Handle filename collisions by prepending the attachment ID
- [x] 1c: Add `attachmentDir()` helper on `App` that returns `filepath.Join(worktreePath, ".cpsl", "attachments", sessionID)` — used by mount construction and cleanup

## Phase 2: Container mount wiring
- [x] 2a: In `bootContainerCmd()`, add the attachment mount to the mounts slice. The function receives `workspace` — derive the attachment dir from it using the session ID (pass session ID as parameter)
- [x] 2b: In the worktree switch handler (main.go:2674), add the attachment mount alongside the workspace mount
- [x] 2c: In `startAgent()` mounts for `DevEnvTool`, add the attachment mount so container rebuilds preserve it

## Phase 3: Per-run cleanup and system prompt
- [x] 3a: At the top of `startAgent()`, move existing files in the session's attachment dir to `past/` subfolder (create `past/` if needed, skip if dir doesn't exist yet)
- [x] 3b: In `buildSystemPrompt()`, add a line in the Environment section: `- Attachments mounted at: /attachments (files attached to the current message are available here)` — only when bash tool is present

## Success Criteria
- Dragging a PNG into the input copies it to `.cpsl/attachments/<sessionID>/` on the host
- The container can `ls /attachments` and see the file
- A second CLI instance on the same project uses a different sessionID and doesn't see/conflict with the first's files
- Starting a new agent run moves previous files to `past/`, so `/attachments` only has current-message files
- System prompt mentions `/attachments` so the agent knows where to find them
