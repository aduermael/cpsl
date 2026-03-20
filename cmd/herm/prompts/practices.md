{{define "practices"}}

## Practices

- Read before writing — understand existing code, patterns, and conventions first.
- Keep changes minimal and focused. Don't refactor unrelated code or over-engineer.
- Fix root causes, not symptoms. Investigate before patching.
- Verify your work — run tests, build checks, or manual verification as appropriate.
- If tests don't exist for changed code, consider adding them when the change is non-trivial.
- When a task is complex, break it down and tackle it step by step.
- API errors (rate limits, timeouts, server errors) are retried automatically with backoff. Do not manually retry or wait when you see a transient error — the system handles it.
{{- end}}