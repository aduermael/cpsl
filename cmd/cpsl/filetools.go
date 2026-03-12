package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"langdag.com/langdag/types"
)

// shellQuote wraps s in single quotes, escaping embedded single quotes.
// Used to safely pass arguments in shell commands executed inside the container.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// GlobTool finds files by glob pattern inside the Docker container.
// It uses rg --files under the hood for .gitignore-aware matching.
type GlobTool struct {
	container *ContainerClient
}

// NewGlobTool creates a GlobTool with the given container client.
func NewGlobTool(container *ContainerClient) *GlobTool {
	return &GlobTool{container: container}
}

func (t *GlobTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        "glob",
		Description: "Find files by glob pattern. Returns matching paths sorted alphabetically, one per line. Respects .gitignore. Use for quick file discovery before reading or searching contents.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {
					"type": "string",
					"description": "Glob pattern (supports ** for recursive matching, e.g. '**/*.go', 'src/**/*.ts')"
				},
				"path": {
					"type": "string",
					"description": "Directory to search in, relative to /workspace (default: /workspace root)"
				}
			},
			"required": ["pattern"]
		}`),
	}
}

type globInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

// globMaxFiles is the maximum number of file paths returned by GlobTool.
const globMaxFiles = 1000

func (t *GlobTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in globInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid glob input: %w", err)
	}
	if in.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	// Build command: cd into search dir, run rg --files with glob filter.
	// Running from the directory gives relative paths in output (compact).
	searchDir := "/workspace"
	if in.Path != "" {
		p := in.Path
		// Make relative paths resolve against /workspace.
		if !strings.HasPrefix(p, "/") {
			p = "/workspace/" + p
		}
		searchDir = p
	}

	cmd := fmt.Sprintf("cd %s && rg --files -g %s --sort path 2>&1",
		shellQuote(searchDir), shellQuote(in.Pattern))

	result, err := t.container.Exec(cmd, 30)
	if err != nil {
		return "", err
	}

	output := strings.TrimRight(result.Stdout+result.Stderr, "\n")

	// rg exit code 1 = no matches, 2 = error.
	if result.ExitCode == 1 || output == "" {
		return "No files matched.", nil
	}
	if result.ExitCode == 2 {
		return fmt.Sprintf("error: %s", output), nil
	}

	// Truncate if too many results.
	lines := strings.Split(output, "\n")
	if len(lines) > globMaxFiles {
		output = strings.Join(lines[:globMaxFiles], "\n") +
			fmt.Sprintf("\n[%d files matched, showing first %d]", len(lines), globMaxFiles)
	}

	return output, nil
}

func (t *GlobTool) RequiresApproval(_ json.RawMessage) bool {
	return false
}
