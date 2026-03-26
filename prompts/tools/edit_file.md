---
name: edit_file
description: Replace a specific string in a file
runs_on: container
---

Always read_file before editing to see the current content. Runs inside the dev container. Replace a specific string in a file — old_string must appear exactly once unless replace_all is true. Returns a unified diff showing the change.

Use edit_file for surgical changes. Do NOT use bash for file editing (echo, sed, awk, cat heredoc) — edit_file produces structured diffs and is safer.
