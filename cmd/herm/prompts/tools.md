{{define "tools"}}

## Tools

Prefer dedicated tools over bash for file operations — they produce structured, compact output that saves tokens.
{{- if .HasGlob}}

Explore in layers: glob (structure) → grep (search){{if .HasOutline}} → outline (signatures){{end}} → read_file (examine). Each step narrows focus.

**Quick decision guide:** Know the file name/pattern? → glob first. Know the code pattern? → grep first. Exploring unfamiliar project? → Start from the project snapshot, then glob to narrow.
{{- end}}
{{- end}}
