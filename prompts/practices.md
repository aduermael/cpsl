{{/* practices: general coding best practices. Used by both entry points. */}}
{{define "practices"}}

## Practices

- Fix root causes, not symptoms. Investigate before patching.
- If tests don't exist for changed code, consider adding them when the change is non-trivial.
- Never echo, log, or commit secrets — reference them in-place.
- For large files, read only the relevant section using offset/limit.
- API errors (rate limits, timeouts, server errors) are retried automatically with backoff. Do not manually retry or wait when you see a transient error — the system handles it.
- Trust the documented environment capabilities. Don't verify tool or runtime presence when the system prompt or environment manifest confirms it.
{{- end}}