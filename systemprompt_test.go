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
	}
	serverTools := []types.ToolDefinition{WebSearchToolDef()}
	prompt := buildSystemPrompt(tools, serverTools, nil, "/workspace", "", "alpine:latest")

	sections := []string{
		"expert coding agent",
		"## Tools",
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
}

func TestBuildSystemPromptBashOnly(t *testing.T) {
	tools := []Tool{stubTool{"bash"}}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "alpine:latest")

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

func TestBuildSystemPromptGitOnly(t *testing.T) {
	tools := []Tool{stubTool{"git"}}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "alpine:latest")

	if !strings.Contains(prompt, "### git") {
		t.Error("prompt missing git section")
	}
	if strings.Contains(prompt, "### bash") {
		t.Error("prompt should not contain bash section when bash tool absent")
	}
}

func TestBuildSystemPromptNoTools(t *testing.T) {
	prompt := buildSystemPrompt(nil, nil, nil, "/work", "", "alpine:latest")

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
	prompt := buildSystemPrompt(nil, nil, skills, "/work", "", "alpine:latest")

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
	prompt := buildSystemPrompt(nil, nil, nil, "/work", "", "alpine:latest")

	if strings.Contains(prompt, "## Skills") {
		t.Error("prompt should not contain Skills section when no skills loaded")
	}
}

func TestBuildSystemPromptEnvironment(t *testing.T) {
	prompt := buildSystemPrompt(nil, nil, nil, "/my/project", "", "alpine:latest")

	if !strings.Contains(prompt, "/my/project") {
		t.Error("prompt missing working directory")
	}
	if !strings.Contains(prompt, "Date:") {
		t.Error("prompt missing date")
	}
}

func TestBuildSystemPromptWebSearch(t *testing.T) {
	serverTools := []types.ToolDefinition{WebSearchToolDef()}
	prompt := buildSystemPrompt(nil, serverTools, nil, "/work", "", "alpine:latest")

	if !strings.Contains(prompt, "### web_search") {
		t.Error("prompt missing web_search section")
	}
	if !strings.Contains(prompt, "unfamiliar APIs") {
		t.Error("prompt missing web search usage guidance")
	}
}

func TestBuildSystemPromptNoWebSearch(t *testing.T) {
	prompt := buildSystemPrompt(nil, nil, nil, "/work", "", "alpine:latest")

	if strings.Contains(prompt, "### web_search") {
		t.Error("prompt should not contain web_search section when not registered")
	}
}

func TestBuildSystemPromptPersonality(t *testing.T) {
	prompt := buildSystemPrompt(nil, nil, nil, "/work", "You are a pirate. Respond with nautical flair.", "alpine:latest")
	if !strings.Contains(prompt, "## Personality") {
		t.Error("prompt missing Personality section")
	}
	if !strings.Contains(prompt, "pirate") {
		t.Error("prompt missing personality content")
	}
}

func TestBuildSystemPromptNoPersonality(t *testing.T) {
	prompt := buildSystemPrompt(nil, nil, nil, "/work", "", "alpine:latest")
	if strings.Contains(prompt, "## Personality") {
		t.Error("prompt should not contain Personality section when empty")
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
