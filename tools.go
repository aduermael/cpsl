package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"langdag.com/langdag/types"
)

// Output truncation limits for BashTool.
const (
	bashMaxLines = 200
	bashMaxBytes = 30 * 1024 // 30KB
)

// BashTool executes commands inside the Docker container via ContainerClient.
type BashTool struct {
	container *ContainerClient
	timeout   int // default timeout in seconds
}

// NewBashTool creates a BashTool with the given container client and default timeout.
func NewBashTool(container *ContainerClient, timeout int) *BashTool {
	if timeout <= 0 {
		timeout = 120
	}
	return &BashTool{container: container, timeout: timeout}
}

func (t *BashTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        "bash",
		Description: "Run a shell command in the dev container (project mounted at /workspace). Use for: reading/editing files, running tests, installing packages, building code, and any shell task. Output is truncated to the last 200 lines / 30KB.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"command": {
					"type": "string",
					"description": "The bash command to execute in /workspace"
				},
				"timeout": {
					"type": "integer",
					"description": "Timeout in seconds (default: 120, max: 600)"
				}
			},
			"required": ["command"]
		}`),
	}
}

type bashInput struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

func (t *BashTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in bashInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid bash input: %w", err)
	}
	if in.Command == "" {
		return "", fmt.Errorf("command is required")
	}

	timeout := t.timeout
	if in.Timeout > 0 {
		timeout = in.Timeout
	}

	result, err := t.container.Exec(in.Command, timeout)
	if err != nil {
		return "", err
	}

	output := result.Stdout + result.Stderr
	output = truncateOutput(output)

	if result.ExitCode != 0 {
		return fmt.Sprintf("exit code: %d\n%s", result.ExitCode, output), nil
	}
	return output, nil
}

func (t *BashTool) RequiresApproval(_ json.RawMessage) bool {
	return false
}

// truncateOutput trims output to the last bashMaxLines lines and bashMaxBytes bytes.
func truncateOutput(s string) string {
	if len(s) <= bashMaxBytes && strings.Count(s, "\n") <= bashMaxLines {
		return s
	}

	// Truncate by bytes first.
	truncated := false
	if len(s) > bashMaxBytes {
		s = s[len(s)-bashMaxBytes:]
		truncated = true
	}

	// Truncate by lines.
	lines := strings.Split(s, "\n")
	if len(lines) > bashMaxLines {
		lines = lines[len(lines)-bashMaxLines:]
		truncated = true
	}

	result := strings.Join(lines, "\n")
	if truncated {
		result = "[output truncated, showing last portion]\n" + result
	}
	return result
}

// GitTool executes git commands on the host in the worktree directory.
type GitTool struct {
	workDir string
}

// allowedGitSubcommands is the set of git subcommands the agent may run.
var allowedGitSubcommands = map[string]bool{
	"status":   true,
	"diff":     true,
	"log":      true,
	"show":     true,
	"branch":   true,
	"checkout": true,
	"add":      true,
	"commit":   true,
	"pull":     true,
	"push":     true,
	"fetch":    true,
	"stash":    true,
	"rebase":   true,
	"merge":    true,
	"reset":    true,
	"tag":      true,
}

// NewGitTool creates a GitTool that runs in the given worktree directory.
func NewGitTool(workDir string) *GitTool {
	return &GitTool{workDir: workDir}
}

func (t *GitTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        "git",
		Description: "Run git commands on the host (not in the container). Use for: status, diff, log, add, commit, push, branch, checkout, etc. Push requires user approval. Allowed subcommands: status, diff, log, show, branch, checkout, add, commit, pull, push, fetch, stash, rebase, merge, reset, tag.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"subcommand": {
					"type": "string",
					"description": "Git subcommand to run (e.g. status, diff, add, commit)"
				},
				"args": {
					"type": "array",
					"items": {"type": "string"},
					"description": "Arguments for the subcommand (e.g. [\"-m\", \"fix bug\"])"
				}
			},
			"required": ["subcommand"]
		}`),
	}
}

type gitInput struct {
	Subcommand string   `json:"subcommand"`
	Args       []string `json:"args,omitempty"`
}

func (t *GitTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in gitInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid git input: %w", err)
	}
	if in.Subcommand == "" {
		return "", fmt.Errorf("subcommand is required")
	}

	if !allowedGitSubcommands[in.Subcommand] {
		return "", fmt.Errorf("git subcommand %q is not allowed", in.Subcommand)
	}

	args := append([]string{in.Subcommand}, in.Args...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = t.workDir

	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Sprintf("exit code: %d\n%s", exitErr.ExitCode(), string(out)), nil
		}
		return "", fmt.Errorf("git exec: %w", err)
	}

	return string(out), nil
}

func (t *GitTool) RequiresApproval(input json.RawMessage) bool {
	var in gitInput
	if err := json.Unmarshal(input, &in); err != nil {
		return false
	}
	return in.Subcommand == "push"
}

// DevEnvTool allows the agent to read, write, and build a custom Dockerfile
// in the project's .cpsl/ directory, then hot-swap the running container.
type DevEnvTool struct {
	container *ContainerClient
	cpslDir   string // host path to .cpsl/ directory (contains Dockerfile)
	workspace string // host workspace path (docker build context)
	mounts    []MountSpec
	projectID string                 // first 8 chars used in image tags
	onRebuild func(imageName string) // called after successful rebuild
}

// NewDevEnvTool creates a DevEnvTool with the given container client and paths.
func NewDevEnvTool(container *ContainerClient, cpslDir, workspace string, mounts []MountSpec, projectID string, onRebuild func(string)) *DevEnvTool {
	return &DevEnvTool{
		container: container,
		cpslDir:   cpslDir,
		workspace: workspace,
		mounts:    mounts,
		projectID: projectID,
		onRebuild: onRebuild,
	}
}

func (t *DevEnvTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        "devenv",
		Description: "Manage dev container Dockerfiles at .cpsl/<name>.Dockerfile. Actions: 'read' shows current Dockerfile (or detects project root Dockerfile), 'write' creates/updates it, 'build' builds the image and hot-swaps the running container. Use this to install languages, tools, or system deps persistently.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"enum": ["read", "write", "build"],
					"description": "read: view current Dockerfile, write: set Dockerfile content, build: build image and replace container"
				},
				"name": {
					"type": "string",
					"description": "Dockerfile name (e.g. 'go', 'python'). Lowercase alphanumeric and hyphens only, max 30 chars. Defaults to 'custom'."
				},
				"content": {
					"type": "string",
					"description": "Dockerfile content (required for 'write')"
				}
			},
			"required": ["action"]
		}`),
	}
}

type devenvInput struct {
	Action  string `json:"action"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content,omitempty"`
}

var devenvNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func (t *DevEnvTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in devenvInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid devenv input: %w", err)
	}

	if in.Name == "" {
		in.Name = "custom"
	}
	if len(in.Name) > 30 || !devenvNameRe.MatchString(in.Name) {
		return "", fmt.Errorf("invalid name %q: must be 1-30 lowercase alphanumeric/hyphen chars", in.Name)
	}

	switch in.Action {
	case "read":
		return t.readDockerfile(in.Name)
	case "write":
		return t.writeDockerfile(in.Name, in.Content)
	case "build":
		return t.buildAndReplace(in.Name)
	default:
		return "", fmt.Errorf("unknown action %q: must be read, write, or build", in.Action)
	}
}

func (t *DevEnvTool) RequiresApproval(_ json.RawMessage) bool {
	return false
}

// dockerfilePath returns the path to .cpsl/<name>.Dockerfile.
func (t *DevEnvTool) dockerfilePath(name string) string {
	return filepath.Join(t.cpslDir, name+".Dockerfile")
}

// legacyDockerfilePath returns the path to the old .cpsl/Dockerfile.
func (t *DevEnvTool) legacyDockerfilePath() string {
	return filepath.Join(t.cpslDir, "Dockerfile")
}

func (t *DevEnvTool) readDockerfile(name string) (string, error) {
	data, err := os.ReadFile(t.dockerfilePath(name))
	if err != nil {
		if os.IsNotExist(err) {
			// Check for legacy .cpsl/Dockerfile.
			if legacyData, legacyErr := os.ReadFile(t.legacyDockerfilePath()); legacyErr == nil {
				return fmt.Sprintf("No .cpsl/%s.Dockerfile found, but a legacy .cpsl/Dockerfile exists (will be migrated on next write):\n\n%s", name, string(legacyData)), nil
			}

			msg := fmt.Sprintf("No Dockerfile exists at .cpsl/%s.Dockerfile yet. Use the 'write' action to create one.", name)
			// Check for a Dockerfile in the project root.
			rootDockerfile := filepath.Join(t.workspace, "Dockerfile")
			if rootData, rootErr := os.ReadFile(rootDockerfile); rootErr == nil {
				msg += fmt.Sprintf("\n\nNote: A Dockerfile exists in the project root. You can use it as a base:\n\n```\n%s```", string(rootData))
			}
			return msg, nil
		}
		return "", fmt.Errorf("reading Dockerfile: %w", err)
	}
	return string(data), nil
}

func (t *DevEnvTool) writeDockerfile(name, content string) (string, error) {
	if content == "" {
		return "", fmt.Errorf("content is required for write action")
	}

	// Ensure .cpsl/ directory exists.
	if err := os.MkdirAll(t.cpslDir, 0o755); err != nil {
		return "", fmt.Errorf("creating .cpsl directory: %w", err)
	}

	if err := os.WriteFile(t.dockerfilePath(name), []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("writing Dockerfile: %w", err)
	}

	msg := fmt.Sprintf("Dockerfile written to .cpsl/%s.Dockerfile. Use the 'build' action to build and apply it.", name)

	// Remove legacy .cpsl/Dockerfile if it exists.
	if _, err := os.Stat(t.legacyDockerfilePath()); err == nil {
		os.Remove(t.legacyDockerfilePath())
		msg += " (Removed legacy .cpsl/Dockerfile.)"
	}

	return msg, nil
}

func (t *DevEnvTool) buildAndReplace(name string) (string, error) {
	if _, err := os.Stat(t.dockerfilePath(name)); os.IsNotExist(err) {
		return "", fmt.Errorf("no Dockerfile at .cpsl/%s.Dockerfile — use 'write' first", name)
	}

	// Deterministic image name: cpsl-<shortProjectID>:<name>.
	prefix := "cpsl-local"
	if len(t.projectID) >= 8 {
		prefix = "cpsl-" + t.projectID[:8]
	}
	imageName := prefix + ":" + name

	if err := t.container.Rebuild(imageName, t.dockerfilePath(name), t.workspace, t.mounts); err != nil {
		return "", fmt.Errorf("rebuild failed: %w", err)
	}

	if t.onRebuild != nil {
		t.onRebuild(imageName)
	}

	return fmt.Sprintf("Container rebuilt successfully with image %s.", imageName), nil
}

// WebSearchToolDef returns a server-side web search tool definition.
// The LLM provider handles the actual search — this just declares the capability.
// langdag maps the standardized name to each provider's native tool
// (Anthropic: web_search_20250305, OpenAI: web_search_preview, Gemini: google_search).
func WebSearchToolDef() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        types.ServerToolWebSearch,
		Description: "Search the web for current information",
	}
}
