{{define "environment"}}

## Environment

- Date: {{.Date}}
- Working directory: {{.WorkDir}}
- Container image: {{.ContainerImage}}
- Project mounted at: /workspace
{{- if .HasBash}}
- Attachments mounted at: /attachments (files attached to the current message are available here)
{{- end}}
{{- end}}