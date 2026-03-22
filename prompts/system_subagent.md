{{define "system_subagent" -}}
{{- template "role_subagent" .}}{{template "tools" .}}{{template "practices" .}}{{template "environment" .}}
{{- end}}