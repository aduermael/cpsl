{{/* system_subagent: sub-agent entry point. Chains environment, role_subagent, tools, practices. Omits communication, personality, skills. */}}
{{define "system_subagent" -}}
{{- template "environment" .}}{{template "role_subagent" .}}{{template "tools" .}}{{template "practices" .}}{{template "instructions" .}}
{{- end}}