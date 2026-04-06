{{/* instructions: project-specific instructions from .herm/instructions.md. Renders only when content is present. */}}
{{define "instructions"}}{{if .ProjectInstructions}}

## Project Instructions

{{.ProjectInstructions}}
{{- end}}{{end}}