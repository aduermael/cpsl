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

// GrepTool searches file contents by regex inside the Docker container.
// It uses rg (ripgrep) under the hood for fast, .gitignore-aware searching.
type GrepTool struct {
	container *ContainerClient
}

// NewGrepTool creates a GrepTool with the given container client.
func NewGrepTool(container *ContainerClient) *GrepTool {
	return &GrepTool{container: container}
}

func (t *GrepTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        "grep",
		Description: "Search file contents by regex pattern. Returns matching files, lines, or counts. Respects .gitignore. Use for finding code patterns, definitions, and usages.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {
					"type": "string",
					"description": "Regex pattern to search for (ripgrep syntax)"
				},
				"path": {
					"type": "string",
					"description": "Directory or file to search in, relative to /workspace (default: /workspace root)"
				},
				"glob": {
					"type": "string",
					"description": "File glob filter (e.g. '*.go', '*.{ts,tsx}')"
				},
				"context": {
					"type": "integer",
					"description": "Lines of context around each match (default: 0)"
				},
				"output_mode": {
					"type": "string",
					"enum": ["files_with_matches", "content", "count"],
					"description": "Output format: files_with_matches (default, file paths only), content (matching lines with line numbers), count (match count per file)"
				}
			},
			"required": ["pattern"]
		}`),
	}
}

type grepInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	Glob       string `json:"glob,omitempty"`
	Context    int    `json:"context,omitempty"`
	OutputMode string `json:"output_mode,omitempty"`
}

// grepMaxLines is the maximum number of output lines returned by GrepTool.
const grepMaxLines = 500

func (t *GrepTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in grepInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid grep input: %w", err)
	}
	if in.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	// Build rg command.
	var args []string
	args = append(args, "rg")

	switch in.OutputMode {
	case "content", "":
		if in.OutputMode == "" {
			// Default to files_with_matches.
			args = append(args, "-l")
		} else {
			args = append(args, "-n") // line numbers
			if in.Context > 0 {
				args = append(args, fmt.Sprintf("-C%d", in.Context))
			}
		}
	case "files_with_matches":
		args = append(args, "-l")
	case "count":
		args = append(args, "-c")
	default:
		return "", fmt.Errorf("unknown output_mode %q", in.OutputMode)
	}

	if in.Glob != "" {
		args = append(args, "-g", shellQuote(in.Glob))
	}

	args = append(args, "--", shellQuote(in.Pattern))

	// Search path.
	searchDir := "/workspace"
	if in.Path != "" {
		p := in.Path
		if !strings.HasPrefix(p, "/") {
			p = "/workspace/" + p
		}
		searchDir = p
	}

	cmd := fmt.Sprintf("cd /workspace && %s %s 2>&1",
		strings.Join(args, " "), shellQuote(searchDir))

	result, err := t.container.Exec(cmd, 30)
	if err != nil {
		return "", err
	}

	output := strings.TrimRight(result.Stdout+result.Stderr, "\n")

	if result.ExitCode == 1 || output == "" {
		return "No matches found.", nil
	}
	if result.ExitCode == 2 {
		return fmt.Sprintf("error: %s", output), nil
	}

	// Truncate if output is too large.
	lines := strings.Split(output, "\n")
	if len(lines) > grepMaxLines {
		output = strings.Join(lines[:grepMaxLines], "\n") +
			fmt.Sprintf("\n[truncated — showing first %d of %d lines]", grepMaxLines, len(lines))
	}

	return output, nil
}

func (t *GrepTool) RequiresApproval(_ json.RawMessage) bool {
	return false
}
