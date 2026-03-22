// Package prompts embeds all system prompt templates and tool description
// markdown files. It exports the parsed template set and the tool description
// filesystem so that cmd/herm can use them without embedding files itself.
package prompts

import (
	"embed"
	"text/template"
)

//go:embed *.md
var templateFS embed.FS

// funcMap provides helper functions available in all prompt templates.
var funcMap = template.FuncMap{
	// containsStr reports whether s appears in the given string slice.
	"containsStr": func(slice []string, s string) bool {
		for _, v := range slice {
			if v == s {
				return true
			}
		}
		return false
	},
}

// Templates is the parsed prompt template set (system, role, tools, etc.).
var Templates = template.Must(template.New("").Funcs(funcMap).ParseFS(templateFS, "*.md"))

//go:embed tools/*.md
var ToolDescFS embed.FS
