---
name: outline
description: Extract function/type/class signatures from files
runs_on: container
---

Extract function/type/class signatures from one or more files. Returns a compact outline with line numbers — much cheaper than reading the full file (~50-100 tokens per file instead of ~2000-5000). Supports multi-file batching via file_paths to reduce tool calls (max 20).

Use before read_file when exploring unfamiliar files — understand the structure first, then read only the sections you need.
