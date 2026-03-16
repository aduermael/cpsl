# Git Tool Design: Container/Host Boundary

## Context

Herm runs an AI agent inside Docker containers. The workspace is bind-mounted at `/workspace`, so changes are visible on both sides. The agent has **two ways** to run git:

1. **`bash` tool** — runs inside the container via `docker exec`. Has access to `/workspace/.git` so local git operations work (status, diff, log, add, commit, rebase on local branches). **Cannot** do remote operations (push, pull, fetch) because no SSH keys or credentials are mounted.

2. **`git` tool** — runs on the host in the worktree directory. Has full access to host SSH keys and credentials. Only `push` requires user approval.

### The Problem

The system prompts don't clearly communicate this split to the agent:

- **role.md** says "You are running in a sandboxed container" but doesn't mention the git tool runs on the host
- **tools.md** says git "runs on the host (not in the container)" but doesn't explain *why* or what the agent should use bash-git vs host-git for
- **No guidance** on merge conflict resolution workflow
- **No guidance** on what happens when remote operations fail due to credentials
- The agent could naively run `git push` via bash inside the container, get a cryptic SSH error, and not understand why
- The agent might duplicate work by running `git status` via both bash and the git tool

### Current Files

| File | Role |
|------|------|
| `cmd/herm/tools.go:127-218` | GitTool implementation — runs on host, 16 allowed subcommands, push requires approval |
| `cmd/herm/prompts/tools.md:32-39` | Git section of system prompt — 4 lines, minimal guidance |
| `cmd/herm/prompts/role.md` | Agent role — mentions sandbox, doesn't mention git host boundary |
| `cmd/herm/prompts/environment.md` | Environment context — no git info |
| `cmd/herm/systemprompt.go` | Template assembly — `HasGit` flag controls git section inclusion |
| `cmd/herm/worktree.go` | Worktree management — agent doesn't know it's in a worktree |

### Design Principles (from user)

- Container isolation is intentional and good — no credentials should leak into containers
- The git tool running on the host is the correct design for credential-requiring operations
- Merge conflicts must be resolvable inside the container (editing files) with the git tool for finalizing
- The user wants control over push operations (approval gate)
- **Local git in the container is fine** — `git commit`, `git log`, `git diff`, etc. via bash are perfectly valid. The container may manage local repos or do local-only git work. We should NOT forbid this.
- However, the dev env may not have git installed, so the git tool is the **reliable default** for the main project
- The git tool should be well-documented enough to be the natural first choice, especially for anything credential-related (push, pull, fetch), without being the mandated-only way

---

## Phase 1: Improve git section in system prompt

Rewrite the git section in `prompts/tools.md` to clearly communicate the container/host boundary, when to use bash-git vs the git tool, and how to handle common workflows including merge conflicts.

- [ ] 1a: Rewrite `prompts/tools.md` git section (lines 32-39) with clear guidance on: (1) the git tool runs on host with full credentials — prefer it for the main project since the container may not have git installed; (2) remote operations (push, pull, fetch) MUST use the git tool — these require credentials that only exist on the host; (3) local git operations via bash are fine (commit, diff, log, etc.) when git is available in the container, e.g. for managing local repos or scratch work; (4) merge conflict resolution workflow (git merge/rebase via git tool -> edit conflicts via bash -> git add + git commit via git tool); (5) what to do if push/pull/fetch fails (likely credentials issue — inform the user); (6) force-push restriction
- [ ] 1b: Add git context to `prompts/environment.md` — add a line indicating the project is in a git worktree managed by herm, so the agent understands the workspace context
- [ ] 1c: Update `systemprompt.go` `PromptData` if any new template variables are needed (e.g., worktree branch name)

## Phase 2: Harden the git tool implementation

Small improvements to the GitTool to make the agent's experience cleaner.

- [ ] 2a: Improve the git tool `Description` in `tools.go:160` to clarify that this is the recommended way to run git for the main project, and the only way to run remote operations (push/pull/fetch) since credentials are only available on the host
- [ ] 2b: Consider adding approval for `pull` and `fetch` too (or at minimum for `reset` and `push --force`) — review current `RequiresApproval` logic and decide if any other subcommands should gate on approval. At minimum, detect `--force` flag in push args
- [ ] 2c: Add better error handling for credential failures — detect common SSH/HTTPS auth error patterns in git output and append a helpful hint (e.g., "This may be a credentials issue. Ensure SSH keys are configured on the host.")

## Phase 3: Update role prompt for clarity

- [ ] 3a: Adjust `prompts/role.md` to mention that the `git` tool bridges to the host for version control — it has access to credentials the container doesn't. Don't overstate it; the agent already knows it's in a container. Just make clear that remote git operations (push/pull/fetch) go through the host tool.

## Phase 4: Tests

- [ ] 4a: Add or update tests in `systemprompt_test.go` to verify the git section renders correctly with the new content when `HasGit` is true/false
- [ ] 4b: Add a test for GitTool force-push detection if 2b adds that logic
- [ ] 4c: Add a test for credential error hint detection if 2c adds that logic

---

## Success Criteria

1. An agent reading the system prompt should understand: prefer the `git` tool for the main project (reliable, has credentials), and know that remote operations (push/pull/fetch) MUST use it
2. The prompt should contain a clear merge conflict resolution workflow
3. The prompt should explain what to do when remote operations fail
4. Force-push should be detectable and gateable
5. All existing tests pass, new behavior is covered

## Open Questions

- Should `pull` and `fetch` require approval? They modify the working tree but don't expose credentials to the container. Current design: no approval needed. User preference may differ.
- Should we add a dedicated merge conflict resolution skill, or is prompt guidance sufficient? Starting with prompt guidance seems right — a skill can come later if needed.
- Should we pass the worktree branch name into the environment section so the agent knows which branch it's on? Low cost, potentially useful.
