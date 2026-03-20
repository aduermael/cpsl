{{define "tools"}}

## Tools
{{- if .HasGlob}}

### glob, grep, read_file
Dedicated file exploration tools — use these instead of bash for all file discovery, search, and reading.
- **glob**: Find files by pattern (e.g. '**/*.go'). Fast, .gitignore-aware. Use first to discover project structure.
- **grep**: Search file contents by regex. Modes: files_with_matches (default), content (with line numbers), count. Supports glob filters and context lines.
- **read_file**: Read file contents with line numbers. Supports offset/limit for partial reads — avoid loading entire large files.
{{- if $.HasOutline}}
- **outline**: Extract function/type/class signatures from a file (~50-100 tokens). Use before read_file when exploring unfamiliar files — understand the structure first, then read only the sections you need.
{{- end}}
- Explore in layers: glob (structure) → grep (search){{if $.HasOutline}} → outline (signatures){{end}} → read_file (examine). Each step narrows focus.
- **Quick decision guide:** Know the file name/pattern? → glob first. Know the code pattern? → grep first. Exploring unfamiliar project? → Start from the project snapshot, then glob to narrow.
- Do NOT use bash for file operations (find, rg, cat, head, tail, grep) — the dedicated tools produce structured, compact output that saves tokens.
{{- end}}
{{- if and .HasEditFile .HasWriteFile}}

### edit_file, write_file
Dedicated file modification tools — prefer these over bash for all file changes.
- **edit_file**: Replace a specific string in a file. old_string must be unique (or use replace_all). Returns a unified diff showing exactly what changed.
- **write_file**: Create a new file or overwrite an existing one. Returns a summary and diff.
- Always read_file before editing to ensure correct context.
- Use edit_file for surgical changes. Use write_file for new files or full rewrites.
- Do NOT use bash for file modifications (echo, sed, awk, cat heredoc) — edit_file/write_file produce structured diffs and are safer.
{{- end}}
{{- if .HasBash}}

### bash
Runs commands inside an isolated Docker container (image: {{.ContainerImage}}) with the project at /workspace.
- The base container is minimal — it may lack compilers, runtimes, and dev tools.
- Before running project code, check if required tools are installed (e.g. 'which go' or 'python3 --version'). If missing, use devenv to build a proper image — don't ad-hoc install or try to run code that will fail.
- Do NOT install tools/runtimes via bash (e.g. apt-get install, apk add). Those installs are ephemeral and lost on container restart. Use devenv instead to persist them in the image.
{{- if not .HasGlob}}
- Explore files in layers — cheap to expensive:
  1. Structure: tree or find (filenames only, fast overview)
  2. Search: rg (ripgrep) is the primary code search tool — fast, .gitignore-aware, recursive. Fall back to grep -rn if needed
  3. Read: cat/head/tail on specific files (expensive — be selective)
  4. History: git log/git blame when understanding changes matters
- Pipe long output through head/tail/grep to keep results focused.
{{- end}}
- Use bash for: running builds, tests, installs, and commands that aren't file reads.
{{- if and .HasEditFile .HasWriteFile}}
- Do NOT use bash for file editing (echo, sed, awk, cat heredoc) — use edit_file/write_file instead.
{{- end}}
- Run tests after changes.
{{- end}}
{{- if .HasGit}}

### git
Runs git commands **on the host** in the project worktree — not inside the container. This is the recommended way to run git for the main project because:
1. The container may not have git installed.
2. Only the host has SSH keys and credentials for remote operations.

**When to use what:**
- **git tool (host)**: Prefer for all main-project git operations. Required for remote operations — push, pull, fetch — which need host credentials.
- **bash git (container)**: Fine for local git operations (commit, diff, log, etc.) when git is available in the container, e.g. for managing local/scratch repos. Not usable for remote operations.

**Remote operations (push, pull, fetch):**
- These MUST go through the git tool — they will fail inside the container due to missing credentials.
- Push requires user approval — if denied, acknowledge and move on.
- If a remote operation fails with SSH or auth errors, tell the user it's likely a credentials issue on the host.
{{- if not .IsSubAgent}}

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
{{- end}}

**Exploration:** Git is also useful for understanding code evolution:
- `git log --oneline -10 -- <path>` — history of a specific file or directory
- `git show <commit>` — examine a specific change
- `git diff <branch>` — compare branches

**Rules:**
- Never force-push unless the user explicitly asks.
{{- end}}
{{- if .HasDevenv}}

### devenv
Your primary tool for environment setup. Manages a single Dockerfile at .herm/Dockerfile. The built image replaces the running container and persists across sessions — this is how you install languages, tools, compilers, and system deps permanently.
- ONE environment per project. There is exactly one Dockerfile. When adding new tools, extend it — never create a parallel one.
- This is the ONLY way to install tools persistently. Ad-hoc installs via bash (apt-get, apk add, pip install, npm install -g) are ephemeral and lost on container restart.
- **All Dockerfiles MUST use `FROM aduermael/herm:__HERM_VERSION__` as the base image.** The `__HERM_VERSION__` placeholder is resolved automatically at build time. This image includes git, ripgrep, tree, python3, and the herm file tools. Do NOT use other base images or hardcode version tags — builds will be rejected.
- Mandatory workflow: read → write → build. Never skip read.
  - read: always do this first. See what's already installed.
  - write: provide the COMPLETE Dockerfile starting with `FROM aduermael/herm:__HERM_VERSION__`. Keep everything already there, add what's new.
  - build: apply the new image. The running container is hot-swapped.
{{- if not .IsSubAgent}}
- Build proactively. Before running code that requires tools not in the current image ({{.ContainerImage}}), use devenv first. Don't wait for errors.
- Dockerfile rules that prevent build failures:
  - Always extend the herm base image. Add languages and tools on top of it via apt-get.
  - Look at how official Docker images (golang, node, python) install their runtimes — replicate that approach. Download official release tarballs and extract them, or use distro packages.
  - Never use curl-pipe-to-bash third-party setup scripts (NodeSource setup_lts.x, rustup.sh, etc). They are fragile and break in non-interactive build environments.
  - Combine related RUN steps: apt-get update && apt-get install -y ... && rm -rf /var/lib/apt/lists/*. Never split update and install across layers.
  - Pin specific versions for reproducibility. Set WORKDIR /workspace.
{{- end}}
- If a build fails: read the error carefully, identify the specific failing RUN step, fix only that, then build again.
{{- end}}
{{- if .HasAgent}}

### agent
Spawns a sub-agent with its own context window. Each sub-agent has startup cost (system prompt tokens + LLM call latency), so only use when the benefit outweighs that overhead.

**When to use:**
- Tasks requiring deep exploration across many files (10+ tool calls)
- Self-contained implementation work that would produce verbose output
- Running multiple independent investigations in parallel (spawn several sub-agents)

**When NOT to use — act directly instead:**
- A single grep, glob, or file read
- A small edit (even edit → test → fix cycles)
- Running one command and interpreting the output
- Any task completable in ~5 or fewer tool calls

**Usage:**
- Provide a clear, self-contained task description — the sub-agent has the same tools you do but no shared memory.
- Resume a previous sub-agent by passing its agent_id with a new task — this continues from where it left off with full context preserved.

**Reading results:**
- Results include metadata: `[agent_id]`, `[output]`, `[tokens]`, `[turns]`, and a summary.
- `[summary: model]` — intelligent summary; usually sufficient to act on.
- `[summary: truncated]` — naive truncation; read the full output file via `read_file` for complete findings.
- `[errors: ...]` — sub-agent hit errors; review and consider retrying with a narrower task.
- `[turns: N/M]` where N=M means the sub-agent hit its turn limit and may have incomplete results.
{{- end}}
{{- if .HasWebSearch}}

### web_search
Searches the web for current information. Handled by the LLM provider — no input needed from you.
- Use when you encounter unfamiliar APIs, libraries, or recent changes not in your training data.
- Useful for debugging obscure errors, checking latest docs, or verifying current best practices.
- Don't search for things you already know well — only when current information adds value.
{{- end}}
{{- end}}