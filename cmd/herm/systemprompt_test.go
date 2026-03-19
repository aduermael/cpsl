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

	sections := []string{
		"expert coding agent",
		"## Tools",
		"### glob, grep, read_file",
		"### edit_file, write_file",
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
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "alpine:latest", "", nil)

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
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "debian:bookworm-slim", "", nil)

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
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "debian:bookworm-slim", "", nil)

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

func TestBuildSystemPromptEditWriteToolsGuidance(t *testing.T) {
	tools := []Tool{
		stubTool{"bash"},
		stubTool{"glob"},
		stubTool{"grep"},
		stubTool{"read_file"},
		stubTool{"edit_file"},
		stubTool{"write_file"},
	}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "debian:bookworm-slim", "", nil)

	expectations := []string{
		"### edit_file, write_file",
		"edit_file",
		"write_file",
		"read_file before editing",
		"Do NOT use bash for file modifications",
		"Do NOT use bash for file editing",
	}
	for _, s := range expectations {
		if !strings.Contains(prompt, s) {
			t.Errorf("edit/write tools guidance missing %q", s)
		}
	}
}

func TestBuildSystemPromptEditWriteToolsAbsent(t *testing.T) {
	// Only read tools, no edit/write — the edit_file/write_file section should be absent.
	tools := []Tool{
		stubTool{"bash"},
		stubTool{"glob"},
		stubTool{"grep"},
		stubTool{"read_file"},
	}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "debian:bookworm-slim", "", nil)

	if strings.Contains(prompt, "### edit_file, write_file") {
		t.Error("prompt should not contain edit_file/write_file section when tools are absent")
	}
	if strings.Contains(prompt, "Do NOT use bash for file editing") {
		t.Error("bash section should not contain edit/write redirect when those tools are absent")
	}
}

func TestBuildSubAgentSystemPromptEditWriteTools(t *testing.T) {
	tools := []Tool{
		stubTool{"bash"},
		stubTool{"glob"},
		stubTool{"grep"},
		stubTool{"read_file"},
		stubTool{"edit_file"},
		stubTool{"write_file"},
	}
	prompt := buildSubAgentSystemPrompt(tools, nil, "/work", "alpine:latest", nil)

	if !strings.Contains(prompt, "### edit_file, write_file") {
		t.Error("sub-agent prompt should include edit_file/write_file section when tools are present")
	}
	if !strings.Contains(prompt, "Do NOT use bash for file modifications") {
		t.Error("sub-agent prompt should include file modification guidance")
	}
}

func TestBuildSystemPromptGitOnly(t *testing.T) {
	tools := []Tool{stubTool{"git"}}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "alpine:latest", "", nil)

	if !strings.Contains(prompt, "### git") {
		t.Error("prompt missing git section")
	}
	if strings.Contains(prompt, "### bash") {
		t.Error("prompt should not contain bash section when bash tool absent")
	}
}

func TestBuildSystemPromptNoTools(t *testing.T) {
	prompt := buildSystemPrompt(nil, nil, nil, "/work", "", "alpine:latest", "", nil)

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

func TestBuildSystemPromptWebSearch(t *testing.T) {
	serverTools := []types.ToolDefinition{WebSearchToolDef()}
	prompt := buildSystemPrompt(nil, serverTools, nil, "/work", "", "alpine:latest", "", nil)

	if !strings.Contains(prompt, "### web_search") {
		t.Error("prompt missing web_search section")
	}
	if !strings.Contains(prompt, "unfamiliar APIs") {
		t.Error("prompt missing web search usage guidance")
	}
}

func TestBuildSystemPromptNoWebSearch(t *testing.T) {
	prompt := buildSystemPrompt(nil, nil, nil, "/work", "", "alpine:latest", "", nil)

	if strings.Contains(prompt, "### web_search") {
		t.Error("prompt should not contain web_search section when not registered")
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

func TestBuildSystemPromptGitSectionContent(t *testing.T) {
	tools := []Tool{stubTool{"bash"}, stubTool{"git"}}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "alpine:latest", "", nil)

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
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "alpine:latest", "", nil)

	if strings.Contains(prompt, "### git") {
		t.Error("prompt should not contain git section when git tool absent")
	}
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
	// Should still have the base worktree info.
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
	// Non-nil but empty slices — no tools registered at all.
	prompt := buildSystemPrompt([]Tool{}, []types.ToolDefinition{}, nil, "/work", "", "alpine:latest", "", nil)

	// Structural sections must still be present.
	for _, section := range []string{"## Tools", "## Practices", "## Communication"} {
		if !strings.Contains(prompt, section) {
			t.Errorf("prompt missing structural section %q", section)
		}
	}

	// No tool-specific subsections should appear.
	for _, sub := range []string{"### bash", "### git", "### devenv", "### web_search", "### glob, grep, read_file", "### edit_file, write_file", "### agent"} {
		if strings.Contains(prompt, sub) {
			t.Errorf("prompt should not contain tool subsection %q when no tools are registered", sub)
		}
	}
}

func TestBuildSystemPromptNilSkillsVsEmpty(t *testing.T) {
	// Both nil and an empty slice should produce no Skills section.
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
	// text/template inserts data values as plain strings — it does not re-parse them as
	// template syntax. This means curly braces in the personality value are safe: they are
	// written to the output buffer verbatim and will NOT cause a panic or template error.
	// HTML special characters (&, <, >) are also safe because text/template (unlike
	// html/template) does not HTML-escape output.
	personality := `You like "quotes" & <angle brackets> and {{curly braces}}`
	prompt := buildSystemPrompt(nil, nil, nil, "/work", personality, "alpine:latest", "", nil)

	if !strings.Contains(prompt, "## Personality") {
		t.Error("prompt missing Personality section")
	}
	// All special characters should appear verbatim in the output.
	for _, fragment := range []string{`"quotes"`, `& <angle brackets>`, `{{curly braces}}`} {
		if !strings.Contains(prompt, fragment) {
			t.Errorf("prompt missing personality fragment %q — special chars may have been escaped or dropped", fragment)
		}
	}
}

func TestBuildSystemPromptAgentTool(t *testing.T) {
	tools := []Tool{stubTool{"agent"}}
	prompt := buildSystemPrompt(tools, nil, nil, "/work", "", "alpine:latest", "", nil)

	// The agent tool triggers the orchestrator role in the role template.
	if !strings.Contains(prompt, "orchestrator") {
		t.Error("role section should describe orchestrator role when agent tool is present")
	}
	// The agent subsection should appear in the Tools section.
	if !strings.Contains(prompt, "### agent") {
		t.Error("prompt missing ### agent subsection when agent tool is present")
	}
	// Key guidance from the agent subsection.
	for _, fragment := range []string{"sub-agent", "agent_id", "context window"} {
		if !strings.Contains(prompt, fragment) {
			t.Errorf("agent subsection missing expected content: %q", fragment)
		}
	}
}

func TestBuildSystemPromptAllServerTools(t *testing.T) {
	// Provide only server tools — no client tools at all.
	serverTools := []types.ToolDefinition{WebSearchToolDef()}
	prompt := buildSystemPrompt(nil, serverTools, nil, "/work", "", "alpine:latest", "", nil)

	// The server tool should be reflected in the prompt.
	if !strings.Contains(prompt, "### web_search") {
		t.Error("prompt missing web_search section when registered as a server tool")
	}
	// No client-tool subsections should appear.
	for _, sub := range []string{"### bash", "### git", "### devenv", "### glob, grep, read_file", "### agent"} {
		if strings.Contains(prompt, sub) {
			t.Errorf("prompt should not contain client tool subsection %q when only server tools are registered", sub)
		}
	}
	// Structural sections must still be present.
	for _, section := range []string{"## Tools", "## Practices", "## Communication"} {
		if !strings.Contains(prompt, section) {
			t.Errorf("prompt missing structural section %q", section)
		}
	}
}

func TestBuildSystemPromptMultipleSkills(t *testing.T) {
	longContent := strings.Repeat("This is a detailed guideline for writing idiomatic Go code. ", 9) // ~504 chars
	skills := []Skill{
		{Name: "EmptySkill", Description: "A skill with no body content", Content: ""},
		{Name: "LongSkill", Description: "A skill with a very long body", Content: longContent},
		{Name: "NormalSkill", Description: "A typical skill", Content: "Keep functions small and focused."},
	}
	prompt := buildSystemPrompt(nil, nil, skills, "/work", "", "alpine:latest", "", nil)

	if !strings.Contains(prompt, "## Skills") {
		t.Error("prompt missing Skills section")
	}
	// All skill names must appear in the summary list and as subsection headers.
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
	// Non-empty content must appear verbatim.
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

	// Should contain the sub-agent role preamble.
	if !strings.Contains(prompt, "You are a sub-agent") {
		t.Error("sub-agent prompt missing preamble")
	}

	// Should contain tool instructions.
	if !strings.Contains(prompt, "## Tools") {
		t.Error("sub-agent prompt missing Tools section")
	}
	if !strings.Contains(prompt, "### bash") {
		t.Error("sub-agent prompt missing bash section")
	}

	// Should contain practices and environment.
	if !strings.Contains(prompt, "## Practices") {
		t.Error("sub-agent prompt missing Practices section")
	}
	if !strings.Contains(prompt, "## Environment") {
		t.Error("sub-agent prompt missing Environment section")
	}

	// Should NOT contain sections skipped for sub-agents.
	if strings.Contains(prompt, "## Communication") {
		t.Error("sub-agent prompt should not contain Communication section")
	}
	if strings.Contains(prompt, "## Personality") {
		t.Error("sub-agent prompt should not contain Personality section")
	}
	if strings.Contains(prompt, "## Skills") {
		t.Error("sub-agent prompt should not contain Skills section")
	}

	// Should NOT contain orchestrator framing.
	if strings.Contains(prompt, "orchestrator") {
		t.Error("sub-agent prompt should not contain orchestrator role")
	}

	// Should NOT contain delegation guidance.
	if strings.Contains(prompt, "When to Delegate") {
		t.Error("sub-agent prompt should not contain delegation guidance")
	}
}

func TestBuildSubAgentSystemPromptNoAgentSection(t *testing.T) {
	// When sub-agent has no agent tool (depth limit), the ### agent section should be absent.
	tools := []Tool{stubTool{"bash"}, stubTool{"glob"}, stubTool{"grep"}, stubTool{"read_file"}}
	prompt := buildSubAgentSystemPrompt(tools, nil, "/work", "alpine:latest", nil)

	if strings.Contains(prompt, "### agent") {
		t.Error("sub-agent prompt should not contain agent tool section when agent tool is absent")
	}
}

func TestBuildSubAgentSystemPromptWithAgentTool(t *testing.T) {
	// When depth allows nesting, the agent tool section should be present
	// but the role should still be sub-agent, not orchestrator.
	tools := []Tool{stubTool{"bash"}, stubTool{"agent"}}
	prompt := buildSubAgentSystemPrompt(tools, nil, "/work", "alpine:latest", nil)

	if !strings.Contains(prompt, "### agent") {
		t.Error("sub-agent with agent tool should have agent tool section")
	}
	if !strings.Contains(prompt, "You are a sub-agent") {
		t.Error("sub-agent with agent tool should still have sub-agent preamble")
	}
}

func TestSubAgentPromptSmallerThanMain(t *testing.T) {
	// The sub-agent prompt should be significantly smaller than the main agent prompt.
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

	// Sub-agent gets same tools minus agent (depth limited).
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

	if ratio > 0.60 {
		t.Errorf("sub-agent prompt should be <60%% of main prompt, got %.1f%%", ratio*100)
	}
}
