{{define "skills"}}{{if .Skills}}

## Skills

{{range .Skills}}- **{{.Name}}**: {{.Description}}
{{end}}{{range .Skills}}
### {{.Name}}

{{.Content}}
{{end}}{{end}}{{end}}