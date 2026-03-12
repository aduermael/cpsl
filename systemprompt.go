package main

import (
	"bytes"
	"embed"
	"text/template"
	"time"

	"langdag.com/langdag/types"
)

//go:embed prompts/*.md
var promptFS embed.FS

// promptTemplates is the parsed prompt template set, initialized once.
var promptTemplates = template.Must(template.ParseFS(promptFS, "prompts/*.md"))

// PromptData holds all values passed to the system prompt templates.
type PromptData struct {
	HasBash        bool
	HasGit         bool
	HasDevenv      bool
	HasScratchpad  bool
	HasAgent       bool
	HasWebSearch   bool
	ContainerImage string
	WorkDir        string
	Date           string
	Personality    string
	Skills         []Skill
}

// buildSystemPrompt constructs the system prompt for the coding agent.
// Tool-specific guidelines are included only when the corresponding tool is available.
// serverTools are provider-side tools (e.g. web search) declared but not executed by the client.
// Structured into: Role, Tools, Practices, Communication, Skills, Environment.
func buildSystemPrompt(tools []Tool, serverTools []types.ToolDefinition, skills []Skill, workDir string, personality string, containerImage string) string {
	toolNames := make(map[string]bool)
	for _, t := range tools {
		toolNames[t.Definition().Name] = true
	}
	for _, st := range serverTools {
		toolNames[st.Name] = true
	}

	data := PromptData{
		HasBash:        toolNames["bash"],
		HasGit:         toolNames["git"],
		HasDevenv:      toolNames["devenv"],
		HasScratchpad:  toolNames["scratchpad"],
		HasAgent:       toolNames["agent"],
		HasWebSearch:   toolNames[types.ServerToolWebSearch],
		ContainerImage: containerImage,
		WorkDir:        workDir,
		Date:           time.Now().Format("2006-01-02 15:04 MST"),
		Personality:    personality,
		Skills:         skills,
	}

	var buf bytes.Buffer
	if err := promptTemplates.ExecuteTemplate(&buf, "system", data); err != nil {
		// Templates are embedded and tested; a failure here is a bug.
		panic("systemprompt: " + err.Error())
	}
	return buf.String()
}
