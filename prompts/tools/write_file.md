---
name: write_file
description: Create a new file or overwrite an existing one
runs_on: container
---

Runs inside the dev container. Create a new file or overwrite an existing one. Returns a summary (line count, byte count) and a unified diff if overwriting. Use for new files or complete rewrites; prefer edit_file for targeted changes.

Do NOT use bash for file creation (echo, cat heredoc) — write_file produces structured diffs and summaries.
