---
name: read_file
description: Read file contents with line numbers
runs_on: container
---

Runs inside the dev container. Read file contents with line numbers. Supports reading specific line ranges to avoid loading entire large files. Use after glob/grep to examine specific files or sections.

Do NOT use bash for file reading (cat, head, tail) — read_file produces structured output with line numbers that saves tokens.
