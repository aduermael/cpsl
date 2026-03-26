{{/* system: main agent entry point. Chains environment, role, tools, practices, communication, personality, skills. */}}
{{define "system" -}}
{{- template "environment" .}}{{template "role" .}}{{template "tools" .}}{{template "practices" .}}{{template "communication" .}}{{template "personality" .}}{{template "skills" .}}
{{- end}}