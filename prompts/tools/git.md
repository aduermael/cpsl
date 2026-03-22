---
name: git
description: Run git commands on the host in the project worktree
runs_on: host
---

Runs on the host (not in the container). Run git commands in the project worktree. This is the recommended way to run git for the main project because:
1. The container may not have git installed.
2. Only the host has SSH keys and credentials for remote operations.

Allowed subcommands: status, diff, log, show, branch, checkout, add, commit, pull, push, fetch, stash, rebase, merge, reset, tag.

**When to use what:**
- **git tool (host)**: Prefer for all main-project git operations. Required for remote operations — push, pull, fetch — which need host credentials.
- **bash git (container)**: Fine for local git operations (commit, diff, log, etc.) when git is available in the container, e.g. for managing local/scratch repos. Not usable for remote operations.

**Remote operations (push, pull, fetch):**
- These MUST go through the git tool — they will fail inside the container due to missing credentials.
- Push requires user approval — if denied, acknowledge and move on.
- If a remote operation fails with SSH or auth errors, tell the user it's likely a credentials issue on the host.

**Merge conflict resolution:**
1. Start the merge or rebase via the git tool (e.g. `git merge main` or `git rebase main`).
2. Edit conflicted files to resolve them (via bash or file editing tools in the container).
3. Stage resolved files via the git tool (`git add <file>`).
4. Complete the merge/rebase via the git tool (`git commit` or `git rebase --continue`).

**Commit messages:**
- Subject line: short imperative summary, ~50 chars (e.g. "fix pagination bug in user list")
- No description body unless the change is non-obvious or the user asks for one
- Never write long, multi-paragraph commit messages
- Use lowercase, no trailing period
- Review status/diff before committing

**Exploration:** Git is also useful for understanding code evolution:
- `git log --oneline -10 -- <path>` — history of a specific file or directory
- `git show <commit>` — examine a specific change
- `git diff <branch>` — compare branches

**Rules:**
- Never force-push unless the user explicitly asks.
