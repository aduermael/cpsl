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
		stubTool{"edit_file"},
		stubTool{"write_file"},
	}
	serverTools := []types.ToolDefinition{WebSearchToolDef()}
	prompt := buildSystemPrompt(tools, serverTools, nil, "/workspace", "", "alpine:latest", "", nil)

	// Structural sections that must always be present.
	sections := []string{
		"expert coding agent",
		"## Tools",
		"## Practices",
		"## Communication",
		"## Environment",
		"/workspace",
		"alpine:latest",
	}
	for _, s := range sections {
		if !strings.Contains(prompt, s) {
			t.Errorf("prompt missing expected section/content: %q", s)
		}
	}

	// Cross-tool guidance from slimmed tools.md should be present.
	if !strings.Contains(prompt, "Explore in layers") {
		t.Error("prompt missing cross-tool exploration guidance")
	}
	if !strings.Contains(prompt, "Quick decision guide") {
		t.Error("prompt missing cross-tool quick decision guide")
	}

	// Per-tool ### sections should NOT be in the system prompt (moved to Description fields).
	for _, sub := range []string{"### bash", "### git", "### devenv", "### web_search", "### glob, grep, read_file", "### edit_file, write_file", "### agent"} {
		if strings.Contains(prompt, sub) {
			t.Errorf("prompt should not contain per-tool subsection %q (now in tool Description)", sub)
		}
	}
}

func TestBuildSystemPromptNoTools(t *testing.T) {
	prompt := buildSystemPrompt(nil, nil, nil, "/work", "", "alpine:latest", "", nil)

	// Should still have the structural sections.
	if !strings.Contains(prompt, "## Tools") {
		t.Error("prompt missing Tools header")
	}
	if !strings.Contains(prompt, "## Practices") {
		t.Error("prompt missing Practices section")
	}
	if !strings.Contains(prompt, "## Communication") {
		t.Error("prompt missing Communication section")
	}

	// No per-tool subsections.
	for _, sub := range []string{"### bash", "### git", "### devenv"} {
		if strings.Contains(prompt, sub) {
			t.Errorf("prompt should not contain %q", sub)
		}
	}

	// Cross-tool exploration guidance requires HasGlob — should be absent.
	if strings.Contains(prompt, "Explore in layers") {
		t.Error("prompt should not contain exploration guidance when glob tool absent")
	}
}

func TestBuildSystemPromptCrossToolGuidanceRequiresGlob(t *testing.T) {
	// Cross-tool guidance only renders when glob is available.
	toolsWithGlob := []Tool{stubTool{"glob"}}
	toolsWithoutGlob := []Tool{stubTool{"bash"}}

	promptWith := buildSystemPrompt(toolsWithGlob, nil, nil, "/work", "", "alpine:latest", "", nil)
	promptWithout := buildSystemPrompt(toolsWithoutGlob, nil, nil, "/work", "", "alpine:latest", "", nil)

	if !strings.Contains(promptWith, "Explore in layers") {
		t.Error("prompt with glob should contain exploration guidance")
	}
	if strings.Contains(promptWithout, "Explore in layers") {
		t.Error("prompt without glob should not contain exploration guidance")
	}
}

func TestBuildSystemPromptWithSkills(t *testing.T) {
	skills := []Skill{
		{Name: "Testing", Description: "How to test", Content: "Write table-driven tests."},
		{Name: "Style", Description: "Code style", Content: "Use gofmt."},
	}
	prompt := buildSystemPrompt(nil, nil, skills, "/work", "", "alpine:latest", "", nil)

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
	prompt := buildSystemPrompt(nil, nil, nil, "/work", "", "alpine:latest", "", nil)

	if strings.Contains(prompt, "## Skills") {
		t.Error("prompt should not contain Skills section when no skills loaded")
	}
}

func TestBuildSystemPromptEnvironment(t *testing.T) {
	prompt := buildSystemPrompt(nil, nil, nil, "/my/project", "", "alpine:latest", "", nil)

	if !strings.Contains(prompt, "/my/project") {
		t.Error("prompt missing working directory")
	}
	if !strings.Contains(prompt, "Date:") {
		t.Error("prompt missing date")
	}
}

func TestBuildSystemPromptPersonality(t *testing.T) {
	prompt := buildSystemPrompt(nil, nil, nil, "/work", "You are a pirate. Respond with nautical flair.", "alpine:latest", "", nil)
	if !strings.Contains(prompt, "## Personality") {
		t.Error("prompt missing Personality section")
	}
	if !strings.Contains(prompt, "pirate") {
		t.Error("prompt missing personality content")
	}
}

func TestBuildSystemPromptNoPersonality(t *testing.T) {
	prompt := buildSystemPrompt(nil, nil, nil, "/work", "", "alpine:latest", "", nil)
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

func TestBuildSystemPromptGitAbsent(t *testing.T) {
	tools := []Tool{stubTool{"bash"}}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "alpine:latest", "", nil)

	if strings.Contains(prompt, "worktree managed by herm") {
		t.Error("prompt should not contain git worktree info when git tool absent")
	}
}

func TestBuildSystemPromptWorktreeBranch(t *testing.T) {
	tools := []Tool{stubTool{"git"}}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "alpine:latest", "herm-feature-x", nil)

	if !strings.Contains(prompt, "branch: herm-feature-x") {
		t.Error("prompt missing worktree branch name")
	}
	if !strings.Contains(prompt, "worktree managed by herm") {
		t.Error("prompt missing worktree context in environment section")
	}
}

func TestBuildSystemPromptWorktreeBranchEmpty(t *testing.T) {
	tools := []Tool{stubTool{"git"}}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "alpine:latest", "", nil)

	if strings.Contains(prompt, "branch:") {
		t.Error("prompt should not contain branch info when worktree branch is empty")
	}
	if !strings.Contains(prompt, "worktree managed by herm") {
		t.Error("prompt missing worktree context in environment section")
	}
}

func TestBuildSystemPromptGitRoleMention(t *testing.T) {
	tools := []Tool{stubTool{"git"}}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "alpine:latest", "", nil)

	if !strings.Contains(prompt, "git` tool is the exception") {
		t.Error("role section missing git host-bridge mention when git tool is present")
	}
}

func TestBuildSystemPromptGitRoleMentionAbsent(t *testing.T) {
	tools := []Tool{stubTool{"bash"}}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "alpine:latest", "", nil)

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

func TestBuildSystemPromptEmptyToolsList(t *testing.T) {
	prompt := buildSystemPrompt([]Tool{}, []types.ToolDefinition{}, nil, "/work", "", "alpine:latest", "", nil)

	for _, section := range []string{"## Tools", "## Practices", "## Communication"} {
		if !strings.Contains(prompt, section) {
			t.Errorf("prompt missing structural section %q", section)
		}
	}
}

func TestBuildSystemPromptNilSkillsVsEmpty(t *testing.T) {
	promptNil := buildSystemPrompt(nil, nil, nil, "/work", "", "alpine:latest", "", nil)
	promptEmpty := buildSystemPrompt(nil, nil, []Skill{}, "/work", "", "alpine:latest", "", nil)

	if strings.Contains(promptNil, "## Skills") {
		t.Error("nil skills: prompt should not contain Skills section")
	}
	if strings.Contains(promptEmpty, "## Skills") {
		t.Error("empty skills slice: prompt should not contain Skills section")
	}
	if promptNil != promptEmpty {
		t.Error("nil skills and empty skills slice should produce identical prompts")
	}
}

func TestBuildSystemPromptPersonalitySpecialChars(t *testing.T) {
	personality := `You like "quotes" & <angle brackets> and {{curly braces}}`
	prompt := buildSystemPrompt(nil, nil, nil, "/work", personality, "alpine:latest", "", nil)

	if !strings.Contains(prompt, "## Personality") {
		t.Error("prompt missing Personality section")
	}
	for _, fragment := range []string{`"quotes"`, `& <angle brackets>`, `{{curly braces}}`} {
		if !strings.Contains(prompt, fragment) {
			t.Errorf("prompt missing personality fragment %q — special chars may have been escaped or dropped", fragment)
		}
	}
}

func TestBuildSystemPromptRetryGuidance(t *testing.T) {
	prompt := buildSystemPrompt(nil, nil, nil, "/work", "", "alpine:latest", "", nil)

	if !strings.Contains(prompt, "retried automatically") {
		t.Error("practices section should contain automatic retry guidance")
	}
}

func TestBuildSystemPromptMultipleSkills(t *testing.T) {
	longContent := strings.Repeat("This is a detailed guideline for writing idiomatic Go code. ", 9)
	skills := []Skill{
		{Name: "EmptySkill", Description: "A skill with no body content", Content: ""},
		{Name: "LongSkill", Description: "A skill with a very long body", Content: longContent},
		{Name: "NormalSkill", Description: "A typical skill", Content: "Keep functions small and focused."},
	}
	prompt := buildSystemPrompt(nil, nil, skills, "/work", "", "alpine:latest", "", nil)

	if !strings.Contains(prompt, "## Skills") {
		t.Error("prompt missing Skills section")
	}
	for _, skill := range skills {
		summaryLine := "**" + skill.Name + "**: " + skill.Description
		if !strings.Contains(prompt, summaryLine) {
			t.Errorf("prompt missing skill summary line: %q", summaryLine)
		}
		header := "### " + skill.Name
		if !strings.Contains(prompt, header) {
			t.Errorf("prompt missing skill subsection header: %q", header)
		}
	}
	if !strings.Contains(prompt, longContent) {
		t.Error("prompt missing long skill content")
	}
	if !strings.Contains(prompt, "Keep functions small and focused.") {
		t.Error("prompt missing normal skill content")
	}
}

func TestBuildSubAgentSystemPrompt(t *testing.T) {
	tools := []Tool{
		stubTool{"bash"},
		stubTool{"glob"},
		stubTool{"grep"},
		stubTool{"read_file"},
	}
	prompt := buildSubAgentSystemPrompt(tools, nil, "/work", "alpine:latest", nil)

	if !strings.Contains(prompt, "You are a sub-agent") {
		t.Error("sub-agent prompt missing preamble")
	}
	if !strings.Contains(prompt, "## Tools") {
		t.Error("sub-agent prompt missing Tools section")
	}
	if !strings.Contains(prompt, "## Practices") {
		t.Error("sub-agent prompt missing Practices section")
	}
	if !strings.Contains(prompt, "## Environment") {
		t.Error("sub-agent prompt missing Environment section")
	}

	// Sections skipped for sub-agents.
	if strings.Contains(prompt, "## Communication") {
		t.Error("sub-agent prompt should not contain Communication section")
	}
	if strings.Contains(prompt, "## Personality") {
		t.Error("sub-agent prompt should not contain Personality section")
	}
	if strings.Contains(prompt, "## Skills") {
		t.Error("sub-agent prompt should not contain Skills section")
	}

	if strings.Contains(prompt, "expert coding agent") {
		t.Error("sub-agent prompt should not contain main-agent role")
	}
	if strings.Contains(prompt, "When to Delegate") {
		t.Error("sub-agent prompt should not contain delegation guidance")
	}
}

func TestSubAgentPromptSmallerThanMain(t *testing.T) {
	tools := []Tool{
		stubTool{"bash"},
		stubTool{"git"},
		stubTool{"devenv"},
		stubTool{"agent"},
		stubTool{"glob"},
		stubTool{"grep"},
		stubTool{"read_file"},
	}
	serverTools := []types.ToolDefinition{WebSearchToolDef()}
	skills := []Skill{
		{Name: "Testing", Description: "How to test", Content: "Write table-driven tests."},
		{Name: "Style", Description: "Code style", Content: "Use gofmt."},
	}

	mainPrompt := buildSystemPrompt(tools, serverTools, skills, "/work", "Be helpful.", "alpine:latest", "feature-branch", nil)

	subTools := []Tool{
		stubTool{"bash"},
		stubTool{"git"},
		stubTool{"devenv"},
		stubTool{"glob"},
		stubTool{"grep"},
		stubTool{"read_file"},
	}
	subPrompt := buildSubAgentSystemPrompt(subTools, serverTools, "/work", "alpine:latest", nil)

	ratio := float64(len(subPrompt)) / float64(len(mainPrompt))
	t.Logf("main prompt: %d bytes, sub-agent prompt: %d bytes, ratio: %.1f%%", len(mainPrompt), len(subPrompt), ratio*100)

	if ratio > 0.85 {
		t.Errorf("sub-agent prompt should be <85%% of main prompt, got %.1f%%", ratio*100)
	}
}

// --- Tool Description tests ---

func TestLoadToolDescriptions(t *testing.T) {
	descs := loadToolDescriptions("test-image:latest")
	if descs == nil {
		t.Fatal("loadToolDescriptions returned nil")
	}

	// All expected tools should be present.
	expectedTools := []string{"bash", "git", "devenv", "agent", "glob", "grep", "read_file", "edit_file", "write_file", "outline"}
	for _, name := range expectedTools {
		td, ok := descs[name]
		if !ok {
			t.Errorf("missing tool description for %q", name)
			continue
		}
		if td.Name != name {
			t.Errorf("tool %q: Name = %q, want %q", name, td.Name, name)
		}
		if td.Brief == "" {
			t.Errorf("tool %q: Brief is empty", name)
		}
		if td.Full == "" {
			t.Errorf("tool %q: Full is empty", name)
		}
	}
}

func TestLoadToolDescriptionsPlaceholderReplacement(t *testing.T) {
	descs := loadToolDescriptions("my-custom:v1.2.3")

	// bash.md and devenv.md use __CONTAINER_IMAGE__.
	for _, name := range []string{"bash", "devenv"} {
		td, ok := descs[name]
		if !ok {
			t.Errorf("missing tool description for %q", name)
			continue
		}
		if strings.Contains(td.Full, "__CONTAINER_IMAGE__") {
			t.Errorf("tool %q: Full still contains __CONTAINER_IMAGE__ placeholder", name)
		}
		if strings.Contains(td.Full, "my-custom:v1.2.3") {
			// Good — placeholder was replaced.
		} else {
			t.Errorf("tool %q: Full does not contain replaced container image", name)
		}
	}
}

func TestToolDescriptionContainsGuidance(t *testing.T) {
	descs := loadToolDescriptions("alpine:latest")

	// Each tool description should contain key guidance keywords.
	tests := []struct {
		tool     string
		keywords []string
	}{
		{"bash", []string{"dev container", "builds, tests"}},
		{"git", []string{"on the host", "SSH keys", "Merge conflict", "Never force-push"}},
		{"devenv", []string{"Dockerfile", "herm base image", "read", "write", "build"}},
		{"agent", []string{"sub-agent", "explore", "implement", "agent_id", "[summary: model]", "[turns:"}},
		{"glob", []string{"glob pattern", ".gitignore"}},
		{"grep", []string{"regex pattern", ".gitignore"}},
		{"read_file", []string{"line numbers", "line ranges"}},
		{"edit_file", []string{"old_string", "unified diff", "read_file before"}},
		{"write_file", []string{"overwrite", "edit_file for targeted"}},
		{"outline", []string{"signatures", "line numbers", "cheaper than reading"}},
	}

	for _, tt := range tests {
		td, ok := descs[tt.tool]
		if !ok {
			t.Errorf("missing tool description for %q", tt.tool)
			continue
		}
		for _, kw := range tt.keywords {
			if !strings.Contains(td.Full, kw) {
				t.Errorf("tool %q description missing keyword %q", tt.tool, kw)
			}
		}
	}
}

func TestParseToolDesc(t *testing.T) {
	input := `---
name: test_tool
description: A test tool
---

This is the body with extended guidance.

It has multiple paragraphs.`

	td, ok := parseToolDesc(input)
	if !ok {
		t.Fatal("parseToolDesc returned ok=false")
	}
	if td.Name != "test_tool" {
		t.Errorf("Name = %q, want %q", td.Name, "test_tool")
	}
	if td.Brief != "A test tool" {
		t.Errorf("Brief = %q, want %q", td.Brief, "A test tool")
	}
	if !strings.Contains(td.Full, "extended guidance") {
		t.Error("Full should contain body content")
	}
	if !strings.Contains(td.Full, "multiple paragraphs") {
		t.Error("Full should contain all body content")
	}
}

func TestParseToolDescMissingName(t *testing.T) {
	input := `---
description: No name field
---

Body content.`

	_, ok := parseToolDesc(input)
	if ok {
		t.Error("parseToolDesc should return ok=false when name is missing")
	}
}

func TestParseToolDescNoFrontmatter(t *testing.T) {
	_, ok := parseToolDesc("Just plain text, no frontmatter")
	if ok {
		t.Error("parseToolDesc should return ok=false for content without frontmatter")
	}
}

func TestParseToolDescEmptyBody(t *testing.T) {
	input := `---
name: minimal
description: Just a brief description
---`

	td, ok := parseToolDesc(input)
	if !ok {
		t.Fatal("parseToolDesc returned ok=false")
	}
	if td.Full != "Just a brief description" {
		t.Errorf("Full = %q, want brief description as fallback", td.Full)
	}
}

func TestGetToolDescriptionFallback(t *testing.T) {
	// With nil toolDescriptions, should return fallback.
	old := toolDescriptions
	toolDescriptions = nil
	defer func() { toolDescriptions = old }()

	result := getToolDescription("bash", "fallback description")
	if result != "fallback description" {
		t.Errorf("getToolDescription with nil map should return fallback, got %q", result)
	}
}

func TestGetToolDescriptionLoaded(t *testing.T) {
	old := toolDescriptions
	toolDescriptions = loadToolDescriptions("alpine:latest")
	defer func() { toolDescriptions = old }()

	result := getToolDescription("bash", "fallback")
	if result == "fallback" {
		t.Error("getToolDescription should return loaded description, not fallback")
	}
	if !strings.Contains(result, "dev container") {
		t.Error("loaded bash description should contain 'dev container'")
	}
}

func TestGetToolDescriptionMissingTool(t *testing.T) {
	old := toolDescriptions
	toolDescriptions = loadToolDescriptions("alpine:latest")
	defer func() { toolDescriptions = old }()

	result := getToolDescription("nonexistent_tool", "my fallback")
	if result != "my fallback" {
		t.Errorf("getToolDescription for missing tool should return fallback, got %q", result)
	}
}

func TestToolsMDCrossToolGuidanceOnly(t *testing.T) {
	// The slimmed tools.md should only contain cross-tool guidance.
	tools := []Tool{
		stubTool{"bash"},
		stubTool{"glob"},
		stubTool{"grep"},
		stubTool{"read_file"},
		stubTool{"outline"},
		stubTool{"edit_file"},
		stubTool{"write_file"},
		stubTool{"git"},
		stubTool{"devenv"},
		stubTool{"agent"},
	}
	serverTools := []types.ToolDefinition{WebSearchToolDef()}
	prompt := buildSystemPrompt(tools, serverTools, nil, "/work", "", "alpine:latest", "", nil)

	// Cross-tool guidance should be present.
	if !strings.Contains(prompt, "Prefer dedicated tools over bash") {
		t.Error("prompt missing 'prefer dedicated tools' guidance")
	}
	if !strings.Contains(prompt, "Explore in layers") {
		t.Error("prompt missing 'explore in layers' guidance")
	}

	// Per-tool sections should NOT be present.
	for _, sub := range []string{
		"### bash", "### git", "### devenv", "### agent",
		"### glob, grep, read_file", "### edit_file, write_file",
		"### web_search",
	} {
		if strings.Contains(prompt, sub) {
			t.Errorf("prompt should not contain per-tool section %q", sub)
		}
	}
}
