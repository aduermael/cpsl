// systemprompt.go builds the system prompt for the coding agent and sub-agents
// by rendering embedded Go templates with tool availability and project context.
package main

import (
	"bytes"
	"time"

	"herm/prompts"

	"langdag.com/langdag/types"
)

// PromptData holds all values passed to the system prompt templates.
type PromptData struct {
	HasBash        bool
	RunsOnHost     bool
	HasDevenv      bool
	HasAgent       bool
	HasWebSearch   bool
	HasGlob        bool
	HasGrep        bool
	HasReadFile    bool
	HasOutline     bool
	HasEditFile    bool
	HasWriteFile   bool
	IsSubAgent     bool   // true for sub-agent prompts: skips communication, personality, skills
	ContainerImage string
	WorkDir        string
	WorktreeBranch string // current branch in the git worktree, if known
	Date           string
	Personality    string
	Skills         []Skill

	// Project snapshot fields (populated at startup, injected into environment section).
	TopLevelListing string
	RecentCommits   string
	GitStatus       string
}

// buildSystemPrompt constructs the system prompt for the coding agent.
// Tool-specific guidelines are included only when the corresponding tool is available.
// serverTools are provider-side tools (e.g. web search) declared but not executed by the client.
// Structured into: Role, Tools, Practices, Communication, Skills, Environment.
func buildSystemPrompt(tools []Tool, serverTools []types.ToolDefinition, skills []Skill, workDir string, personality string, containerImage string, worktreeBranch string, snap *projectSnapshot) string {
	toolNames := make(map[string]bool)
	for _, t := range tools {
		toolNames[t.Definition().Name] = true
	}
	for _, st := range serverTools {
		toolNames[st.Name] = true
	}

	data := PromptData{
		HasBash:        toolNames["bash"],
		RunsOnHost:     toolNames["git"],
		HasDevenv:      toolNames["devenv"],
		HasAgent:       toolNames["agent"],
		HasWebSearch:   toolNames[types.ServerToolWebSearch],
		HasGlob:        toolNames["glob"],
		HasGrep:        toolNames["grep"],
		HasReadFile:    toolNames["read_file"],
		HasOutline:     toolNames["outline"],
		HasEditFile:    toolNames["edit_file"],
		HasWriteFile:   toolNames["write_file"],
		ContainerImage: containerImage,
		WorkDir:        workDir,
		WorktreeBranch: worktreeBranch,
		Date:           time.Now().Format("2006-01-02 15:04 MST"),
		Personality:    personality,
		Skills:         skills,
	}

	if snap != nil {
		data.TopLevelListing = snap.TopLevel
		data.RecentCommits = snap.RecentCommits
		data.GitStatus = snap.GitStatus
	}

	var buf bytes.Buffer
	if err := prompts.Templates.ExecuteTemplate(&buf, "system", data); err != nil {
		// Templates are embedded and tested; a failure here is a bug.
		panic("systemprompt: " + err.Error())
	}
	return buf.String()
}

// buildSubAgentSystemPrompt constructs a leaner system prompt for sub-agents.
// It reuses the same template infrastructure but sets IsSubAgent=true, which
// uses the sub-agent preamble instead of the main agent role and skips
// communication, personality, and skills sections to reduce token overhead.
func buildSubAgentSystemPrompt(tools []Tool, serverTools []types.ToolDefinition, workDir string, containerImage string, snap *projectSnapshot) string {
	toolNames := make(map[string]bool)
	for _, t := range tools {
		toolNames[t.Definition().Name] = true
	}
	for _, st := range serverTools {
		toolNames[st.Name] = true
	}

	data := PromptData{
		HasBash:        toolNames["bash"],
		RunsOnHost:     toolNames["git"],
		HasDevenv:      toolNames["devenv"],
		HasAgent:       toolNames["agent"],
		HasWebSearch:   toolNames[types.ServerToolWebSearch],
		HasGlob:        toolNames["glob"],
		HasGrep:        toolNames["grep"],
		HasReadFile:    toolNames["read_file"],
		HasOutline:     toolNames["outline"],
		HasEditFile:    toolNames["edit_file"],
		HasWriteFile:   toolNames["write_file"],
		IsSubAgent:     true,
		ContainerImage: containerImage,
		WorkDir:        workDir,
		Date:           time.Now().Format("2006-01-02 15:04 MST"),
		// Personality, Skills, WorktreeBranch intentionally omitted for sub-agents.
	}

	if snap != nil {
		data.TopLevelListing = snap.TopLevel
		data.RecentCommits = snap.RecentCommits
		data.GitStatus = snap.GitStatus
	}

	var buf bytes.Buffer
	if err := prompts.Templates.ExecuteTemplate(&buf, "system", data); err != nil {
		panic("systemprompt: " + err.Error())
	}
	return buf.String()
}
