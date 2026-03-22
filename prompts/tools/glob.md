---
name: glob
description: Find files by glob pattern
runs_on: container
---

Runs inside the dev container. Find files by glob pattern. Returns matching paths sorted alphabetically, one per line. Respects .gitignore. Use for quick file discovery before reading or searching contents.

Do NOT use bash for file discovery (find, ls) — glob produces structured, compact output that saves tokens.
