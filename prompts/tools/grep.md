---
name: grep
description: Search file contents by regex pattern
runs_on: container
---

Runs inside the dev container. Search file contents by regex pattern. Returns matching files, lines, or counts. Respects .gitignore. Use for finding code patterns, definitions, and usages.

Do NOT use bash for code search (grep, rg) — this tool produces structured, compact output that saves tokens.
