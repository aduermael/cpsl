{{/* practices: general coding best practices. Used by both entry points. */}}
{{define "practices"}}

## Practices

- Read before writing — understand existing code, patterns, and conventions first.
- Keep changes minimal and focused. Don't refactor unrelated code or over-engineer.
- Fix root causes, not symptoms. Investigate before patching.
- If tests don't exist for changed code, consider adding them when the change is non-trivial.
- Never echo, log, or commit secrets — reference them in-place.
- If a requested action is destructive and irreversible, confirm with the user before proceeding.
- For large files, read only the relevant section using offset/limit.
- API errors (rate limits, timeouts, server errors) are retried automatically with backoff. Do not manually retry or wait when you see a transient error — the system handles it.
{{- end}}