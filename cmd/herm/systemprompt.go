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
	HostTools      []string // tool names that execute on the host (e.g. "git")
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
// Structured into: Environment, Role, Tools, Practices, Communication, Personality, Skills.
func buildSystemPrompt(tools []Tool, serverTools []types.ToolDefinition, skills []Skill, workDir string, personality string, containerImage string, worktreeBranch string, snap *projectSnapshot) string {
	toolNames := make(map[string]bool)
	var hostTools []string
	for _, t := range tools {
		name := t.Definition().Name
		toolNames[name] = true
		if t.HostTool() {
			hostTools = append(hostTools, name)
		}
	}
	for _, st := range serverTools {
		toolNames[st.Name] = true
	}

	data := PromptData{
		HasBash:        toolNames["bash"],
		HostTools:      hostTools,
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
// It uses the "system_subagent" entry-point template which chains only
// role, tools, practices, and environment — skipping communication,
// personality, and skills to reduce token overhead.
func buildSubAgentSystemPrompt(tools []Tool, serverTools []types.ToolDefinition, workDir string, containerImage string, snap *projectSnapshot) string {
	toolNames := make(map[string]bool)
	var hostTools []string
	for _, t := range tools {
		name := t.Definition().Name
		toolNames[name] = true
		if t.HostTool() {
			hostTools = append(hostTools, name)
		}
	}
	for _, st := range serverTools {
		toolNames[st.Name] = true
	}

	data := PromptData{
		HasBash:        toolNames["bash"],
		HostTools:      hostTools,
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
	if err := prompts.Templates.ExecuteTemplate(&buf, "system_subagent", data); err != nil {
		panic("systemprompt: " + err.Error())
	}
	return buf.String()
}
