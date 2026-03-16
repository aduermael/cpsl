package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"langdag.com/langdag/types"
)

// stubTool is a minimal Tool implementation for testing buildSystemPrompt.
type stubTool struct {
	name string
}

func (s stubTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        s.name,
		Description: "stub " + s.name,
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
}

func (s stubTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "", nil
}

func (s stubTool) RequiresApproval(_ json.RawMessage) bool {
	return false
}

func TestBuildSystemPromptAllTools(t *testing.T) {
	tools := []Tool{
		stubTool{"bash"},
		stubTool{"git"},
		stubTool{"devenv"},
		stubTool{"glob"},
		stubTool{"grep"},
		stubTool{"read_file"},
	}
	serverTools := []types.ToolDefinition{WebSearchToolDef()}
	prompt := buildSystemPrompt(tools, serverTools, nil, "/workspace", "", "alpine:latest", "")

	sections := []string{
		"expert coding agent",
		"## Tools",
		"### glob, grep, read_file",
		"### bash",
		"### git",
		"### devenv",
		"### web_search",
		"## Practices",
		"## Communication",
		"## Environment",
		"/workspace",
		"alpine:latest",
		"may lack compilers",
	}
	for _, s := range sections {
		if !strings.Contains(prompt, s) {
			t.Errorf("prompt missing expected section/content: %q", s)
		}
	}

	// When file tools are present, bash should NOT contain old exploration guidance.
	if strings.Contains(prompt, "tree or find") {
		t.Error("bash section should not contain old exploration guidance when glob/grep/read_file are present")
	}
}

func TestBuildSystemPromptBashOnly(t *testing.T) {
	tools := []Tool{stubTool{"bash"}}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "alpine:latest", "")

	if !strings.Contains(prompt, "### bash") {
		t.Error("prompt missing bash section")
	}
	if strings.Contains(prompt, "### git") {
		t.Error("prompt should not contain git section when git tool absent")
	}
	if strings.Contains(prompt, "### devenv") {
		t.Error("prompt should not contain devenv section when devenv tool absent")
	}
	if strings.Contains(prompt, "### web_search") {
		t.Error("prompt should not contain web_search section when not registered")
	}
}

func TestBuildSystemPromptBashExplorationGuidance(t *testing.T) {
	tools := []Tool{stubTool{"bash"}}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "debian:bookworm-slim", "")

	// Verify layered exploration strategy is present
	expectations := []string{
		"Explore files in layers",
		"tree or find",
		"rg (ripgrep)",
		"cat/head/tail",
		"git log/git blame",
	}
	for _, s := range expectations {
		if !strings.Contains(prompt, s) {
			t.Errorf("bash exploration guidance missing %q", s)
		}
	}
}

func TestBuildSystemPromptFileToolsGuidance(t *testing.T) {
	tools := []Tool{
		stubTool{"bash"},
		stubTool{"glob"},
		stubTool{"grep"},
		stubTool{"read_file"},
	}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "debian:bookworm-slim", "")

	// File tools section should be present with key guidance.
	expectations := []string{
		"### glob, grep, read_file",
		"Do NOT use bash for file operations",
		"glob (structure)",
		"grep (search)",
		"read_file (examine)",
	}
	for _, s := range expectations {
		if !strings.Contains(prompt, s) {
			t.Errorf("file tools guidance missing %q", s)
		}
	}
}

func TestBuildSystemPromptGitOnly(t *testing.T) {
	tools := []Tool{stubTool{"git"}}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "alpine:latest", "")

	if !strings.Contains(prompt, "### git") {
		t.Error("prompt missing git section")
	}
	if strings.Contains(prompt, "### bash") {
		t.Error("prompt should not contain bash section when bash tool absent")
	}
}

func TestBuildSystemPromptNoTools(t *testing.T) {
	prompt := buildSystemPrompt(nil, nil, nil, "/work", "", "alpine:latest", "")

	// Should still have the structural sections
	if !strings.Contains(prompt, "## Tools") {
		t.Error("prompt missing Tools header")
	}
	if !strings.Contains(prompt, "## Practices") {
		t.Error("prompt missing Practices section")
	}
	if !strings.Contains(prompt, "## Communication") {
		t.Error("prompt missing Communication section")
	}
	// No tool subsections
	if strings.Contains(prompt, "### bash") {
		t.Error("prompt should not contain bash section")
	}
}

func TestBuildSystemPromptWithSkills(t *testing.T) {
	skills := []Skill{
		{Name: "Testing", Description: "How to test", Content: "Write table-driven tests."},
		{Name: "Style", Description: "Code style", Content: "Use gofmt."},
	}
	prompt := buildSystemPrompt(nil, nil, skills, "/work", "", "alpine:latest", "")

	if !strings.Contains(prompt, "## Skills") {
		t.Error("prompt missing Skills section")
	}
	if !strings.Contains(prompt, "**Testing**: How to test") {
		t.Error("prompt missing Testing skill summary")
	}
	if !strings.Contains(prompt, "**Style**: Code style") {
		t.Error("prompt missing Style skill summary")
	}
	if !strings.Contains(prompt, "### Testing") {
		t.Error("prompt missing Testing skill content section")
	}
	if !strings.Contains(prompt, "Write table-driven tests.") {
		t.Error("prompt missing Testing skill content body")
	}
	if !strings.Contains(prompt, "### Style") {
		t.Error("prompt missing Style skill content section")
	}
}

func TestBuildSystemPromptNoSkills(t *testing.T) {
	prompt := buildSystemPrompt(nil, nil, nil, "/work", "", "alpine:latest", "")

	if strings.Contains(prompt, "## Skills") {
		t.Error("prompt should not contain Skills section when no skills loaded")
	}
}

func TestBuildSystemPromptEnvironment(t *testing.T) {
	prompt := buildSystemPrompt(nil, nil, nil, "/my/project", "", "alpine:latest", "")

	if !strings.Contains(prompt, "/my/project") {
		t.Error("prompt missing working directory")
	}
	if !strings.Contains(prompt, "Date:") {
		t.Error("prompt missing date")
	}
}

func TestBuildSystemPromptWebSearch(t *testing.T) {
	serverTools := []types.ToolDefinition{WebSearchToolDef()}
	prompt := buildSystemPrompt(nil, serverTools, nil, "/work", "", "alpine:latest", "")

	if !strings.Contains(prompt, "### web_search") {
		t.Error("prompt missing web_search section")
	}
	if !strings.Contains(prompt, "unfamiliar APIs") {
		t.Error("prompt missing web search usage guidance")
	}
}

func TestBuildSystemPromptNoWebSearch(t *testing.T) {
	prompt := buildSystemPrompt(nil, nil, nil, "/work", "", "alpine:latest", "")

	if strings.Contains(prompt, "### web_search") {
		t.Error("prompt should not contain web_search section when not registered")
	}
}

func TestBuildSystemPromptPersonality(t *testing.T) {
	prompt := buildSystemPrompt(nil, nil, nil, "/work", "You are a pirate. Respond with nautical flair.", "alpine:latest", "")
	if !strings.Contains(prompt, "## Personality") {
		t.Error("prompt missing Personality section")
	}
	if !strings.Contains(prompt, "pirate") {
		t.Error("prompt missing personality content")
	}
}

func TestBuildSystemPromptNoPersonality(t *testing.T) {
	prompt := buildSystemPrompt(nil, nil, nil, "/work", "", "alpine:latest", "")
	if strings.Contains(prompt, "## Personality") {
		t.Error("prompt should not contain Personality section when empty")
	}
}

func TestPromptTemplateParsing(t *testing.T) {
	// Verify all expected templates are defined in the embedded FS.
	expected := []string{"system", "role", "tools", "practices", "communication", "personality", "skills", "environment"}
	for _, name := range expected {
		tmpl := promptTemplates.Lookup(name)
		if tmpl == nil {
			t.Errorf("template %q not found in embedded template set", name)
		}
	}
}

func TestBuildSystemPromptGitSectionContent(t *testing.T) {
	tools := []Tool{stubTool{"bash"}, stubTool{"git"}}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "alpine:latest", "")

	// Key guidance from the rewritten git section.
	expectations := []string{
		"on the host",
		"SSH keys and credentials",
		"push, pull, fetch",
		"Merge conflict resolution",
		"git add",
		"Never force-push",
	}
	for _, s := range expectations {
		if !strings.Contains(prompt, s) {
			t.Errorf("git section missing expected content: %q", s)
		}
	}
}

func TestBuildSystemPromptGitAbsent(t *testing.T) {
	tools := []Tool{stubTool{"bash"}}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "alpine:latest", "")

	if strings.Contains(prompt, "### git") {
		t.Error("prompt should not contain git section when git tool absent")
	}
	if strings.Contains(prompt, "worktree managed by herm") {
		t.Error("prompt should not contain git worktree info when git tool absent")
	}
}

func TestBuildSystemPromptWorktreeBranch(t *testing.T) {
	tools := []Tool{stubTool{"git"}}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "alpine:latest", "herm-feature-x")

	if !strings.Contains(prompt, "branch: herm-feature-x") {
		t.Error("prompt missing worktree branch name")
	}
	if !strings.Contains(prompt, "worktree managed by herm") {
		t.Error("prompt missing worktree context in environment section")
	}
}

func TestBuildSystemPromptWorktreeBranchEmpty(t *testing.T) {
	tools := []Tool{stubTool{"git"}}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "alpine:latest", "")

	if strings.Contains(prompt, "branch:") {
		t.Error("prompt should not contain branch info when worktree branch is empty")
	}
	// Should still have the base worktree info.
	if !strings.Contains(prompt, "worktree managed by herm") {
		t.Error("prompt missing worktree context in environment section")
	}
}

func TestBuildSystemPromptGitRoleMention(t *testing.T) {
	tools := []Tool{stubTool{"git"}}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "alpine:latest", "")

	if !strings.Contains(prompt, "git` tool is the exception") {
		t.Error("role section missing git host-bridge mention when git tool is present")
	}
}

func TestBuildSystemPromptGitRoleMentionAbsent(t *testing.T) {
	tools := []Tool{stubTool{"bash"}}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "alpine:latest", "")

	if strings.Contains(prompt, "git` tool is the exception") {
		t.Error("role section should not mention git host-bridge when git tool is absent")
	}
}

func TestGitToolForcePushApproval(t *testing.T) {
	gt := NewGitTool("/tmp", false)

	tests := []struct {
		name     string
		input    gitInput
		wantAppr bool
	}{
		{"push", gitInput{Subcommand: "push"}, true},
		{"push --force", gitInput{Subcommand: "push", Args: []string{"--force"}}, true},
		{"push -f", gitInput{Subcommand: "push", Args: []string{"-f"}}, true},
		{"push --force-with-lease", gitInput{Subcommand: "push", Args: []string{"--force-with-lease"}}, true},
		{"reset --hard", gitInput{Subcommand: "reset", Args: []string{"--hard"}}, true},
		{"reset --soft", gitInput{Subcommand: "reset", Args: []string{"--soft"}}, false},
		{"status", gitInput{Subcommand: "status"}, false},
		{"commit", gitInput{Subcommand: "commit", Args: []string{"-m", "test"}}, false},
		{"checkout --force", gitInput{Subcommand: "checkout", Args: []string{"--force", "main"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, _ := json.Marshal(tt.input)
			got := gt.RequiresApproval(raw)
			if got != tt.wantAppr {
				t.Errorf("RequiresApproval(%s) = %v, want %v", tt.name, got, tt.wantAppr)
			}
		})
	}
}

func TestGitCredentialHint(t *testing.T) {
	tests := []struct {
		output  string
		wantHit bool
	}{
		{"Permission denied (publickey).\nfatal: Could not read from remote repository.", true},
		{"fatal: Authentication failed for 'https://github.com/foo/bar.git/'", true},
		{"fatal: could not read Username for 'https://github.com': terminal prompts disabled", true},
		{"Host key verification failed.\nfatal: Could not read from remote repository.", true},
		{"Everything up-to-date", false},
		{"Already up to date.", false},
	}
	for _, tt := range tests {
		hint := gitCredentialHint(tt.output)
		if tt.wantHit && hint == "" {
			t.Errorf("expected credential hint for output: %q", tt.output)
		}
		if !tt.wantHit && hint != "" {
			t.Errorf("unexpected credential hint for output: %q", tt.output)
		}
	}
}

func TestWebSearchToolDef(t *testing.T) {
	def := WebSearchToolDef()
	if def.Name != types.ServerToolWebSearch {
		t.Errorf("Name = %q, want %q", def.Name, types.ServerToolWebSearch)
	}
	if def.IsClientTool() {
		t.Error("web search should be a server tool (no InputSchema)")
	}
	if def.Description == "" {
		t.Error("web search tool should have a description")
	}
}
