{{/* environment: runtime context (date, paths, image, project snapshot). Used by both entry points. */}}
{{define "environment"}}

## Environment

- Date: {{.Date}}
- Working directory: {{.WorkDir}}
- Container image: {{.ContainerImage}}
- Project mounted at: /workspace
{{- if .HostTools}}
- Host tools: {{range $i, $t := .HostTools}}{{if $i}}, {{end}}{{$t}}{{end}}{{if containsStr .HostTools "git"}} (worktree{{if .WorktreeBranch}}: {{.WorktreeBranch}}{{end}}){{end}}
{{- end}}
{{- if .HasBash}}
- Attachments mounted at: /attachments (files attached to the current message are available here)
{{- end}}
{{- if or .TopLevelListing .RecentCommits .GitStatus}}

## Project context

{{- if .TopLevelListing}}

Top-level:
{{.TopLevelListing}}
{{- end}}
{{- if .RecentCommits}}

Recent commits:
{{.RecentCommits}}
{{- end}}

Uncommitted changes:
{{- if .GitStatus}}
{{.GitStatus}}
{{- else}}
clean
{{- end}}
{{- end}}
{{- end}}