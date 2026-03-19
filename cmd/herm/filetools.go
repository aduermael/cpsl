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
// Kept low to encourage focused searches and reduce context usage.
const grepMaxLines = 200

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

// ReadFileTool reads file contents (with optional line range) inside the Docker container.
type ReadFileTool struct {
	container *ContainerClient
}

// NewReadFileTool creates a ReadFileTool with the given container client.
func NewReadFileTool(container *ContainerClient) *ReadFileTool {
	return &ReadFileTool{container: container}
}

func (t *ReadFileTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        "read_file",
		Description: "Read file contents with line numbers. Supports reading specific line ranges to avoid loading entire large files. Use after glob/grep to examine specific files or sections.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_path": {
					"type": "string",
					"description": "Path to the file, relative to /workspace (e.g. 'src/main.go')"
				},
				"offset": {
					"type": "integer",
					"description": "Start line number (1-based, default: 1)"
				},
				"limit": {
					"type": "integer",
					"description": "Maximum lines to read (default: 2000)"
				}
			},
			"required": ["file_path"]
		}`),
	}
}

type readFileInput struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// readFileDefaultLimit is the default maximum number of lines returned.
// Set to 500 to encourage targeted reads with offset/limit. Most code
// files are readable at this length; larger files should be read in sections.
const readFileDefaultLimit = 500

// readFileMaxLineWidth truncates individual lines longer than this.
const readFileMaxLineWidth = 2000

func (t *ReadFileTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in readFileInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid read_file input: %w", err)
	}
	if in.FilePath == "" {
		return "", fmt.Errorf("file_path is required")
	}

	// Resolve path relative to /workspace.
	filePath := in.FilePath
	if !strings.HasPrefix(filePath, "/") {
		filePath = "/workspace/" + filePath
	}

	offset := in.Offset
	if offset < 1 {
		offset = 1
	}
	limit := in.Limit
	if limit <= 0 {
		limit = readFileDefaultLimit
	}

	// Use awk for line range extraction with line numbers.
	// awk is universally available and handles the offset+limit+numbering in one pass.
	endLine := offset + limit - 1
	cmd := fmt.Sprintf("awk 'NR>=%d && NR<=%d { printf \"%%6d\\t%%s\\n\", NR, (length>%d ? substr($0,1,%d)\"…\" : $0) } NR>%d { exit }' %s 2>&1",
		offset, endLine, readFileMaxLineWidth, readFileMaxLineWidth, endLine, shellQuote(filePath))

	result, err := t.container.Exec(cmd, 30)
	if err != nil {
		return "", err
	}

	output := result.Stdout + result.Stderr
	if result.ExitCode != 0 {
		return fmt.Sprintf("error: %s", strings.TrimRight(output, "\n")), nil
	}

	output = strings.TrimRight(output, "\n")
	if output == "" {
		// Check if file exists but range is past end, or file is empty.
		checkCmd := fmt.Sprintf("wc -l < %s 2>&1", shellQuote(filePath))
		checkResult, checkErr := t.container.Exec(checkCmd, 5)
		if checkErr != nil || checkResult.ExitCode != 0 {
			return fmt.Sprintf("error: cannot read %s", in.FilePath), nil
		}
		totalLines := strings.TrimSpace(checkResult.Stdout)
		if totalLines == "0" {
			return "(empty file)", nil
		}
		return fmt.Sprintf("(no content in range — file has %s lines)", totalLines), nil
	}

	// Count total lines to indicate if there's more.
	outputLines := strings.Count(output, "\n") + 1
	if outputLines >= limit {
		wcCmd := fmt.Sprintf("wc -l < %s 2>&1", shellQuote(filePath))
		wcResult, wcErr := t.container.Exec(wcCmd, 5)
		if wcErr == nil && wcResult.ExitCode == 0 {
			total := strings.TrimSpace(wcResult.Stdout)
			output += fmt.Sprintf("\n[showing lines %d-%d of %s]", offset, offset+outputLines-1, total)
		}
	}

	return output, nil
}

func (t *ReadFileTool) RequiresApproval(_ json.RawMessage) bool {
	return false
}

// EditFileTool performs exact string replacement in a file inside the Docker container.
// It pipes JSON to the edit-file CLI tool and returns the unified diff.
type EditFileTool struct {
	container *ContainerClient
}

// NewEditFileTool creates an EditFileTool with the given container client.
func NewEditFileTool(container *ContainerClient) *EditFileTool {
	return &EditFileTool{container: container}
}

func (t *EditFileTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        "edit_file",
		Description: "Replace a specific string in a file. old_string must appear exactly once unless replace_all is true. Returns a unified diff showing the change. Always read_file before editing to ensure correct context.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_path": {
					"type": "string",
					"description": "Path to the file, relative to /workspace (e.g. 'src/main.go')"
				},
				"old_string": {
					"type": "string",
					"description": "The exact string to find and replace (must be unique in the file unless replace_all is true)"
				},
				"new_string": {
					"type": "string",
					"description": "The replacement string"
				},
				"replace_all": {
					"type": "boolean",
					"description": "Replace all occurrences instead of requiring uniqueness (default: false)"
				}
			},
			"required": ["file_path", "old_string", "new_string"]
		}`),
	}
}

type editFileInput struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// editFileResponse matches the JSON output from the edit-file CLI tool.
type editFileResponse struct {
	OK    bool   `json:"ok"`
	Diff  string `json:"diff,omitempty"`
	Error string `json:"error,omitempty"`
}

func (t *EditFileTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in editFileInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid edit_file input: %w", err)
	}
	if in.FilePath == "" {
		return "", fmt.Errorf("file_path is required")
	}
	if in.OldString == "" {
		return "", fmt.Errorf("old_string is required")
	}

	// Resolve path relative to /workspace.
	filePath := in.FilePath
	if !strings.HasPrefix(filePath, "/") {
		filePath = "/workspace/" + filePath
	}

	// Build JSON input for the CLI tool with the resolved path.
	cliInput := editFileInput{
		FilePath:   filePath,
		OldString:  in.OldString,
		NewString:  in.NewString,
		ReplaceAll: in.ReplaceAll,
	}
	inputJSON, err := json.Marshal(cliInput)
	if err != nil {
		return "", fmt.Errorf("marshalling edit-file input: %w", err)
	}

	cmd := fmt.Sprintf("echo %s | edit-file", shellQuote(string(inputJSON)))
	result, err := t.container.Exec(cmd, 30)
	if err != nil {
		return "", err
	}

	output := strings.TrimSpace(result.Stdout + result.Stderr)

	var resp editFileResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		// CLI didn't return valid JSON — return raw output as error.
		return fmt.Sprintf("edit-file error: %s", output), nil
	}

	if !resp.OK {
		return fmt.Sprintf("error: %s", resp.Error), nil
	}
	return resp.Diff, nil
}

func (t *EditFileTool) RequiresApproval(_ json.RawMessage) bool {
	return false
}

// WriteFileTool creates or overwrites a file inside the Docker container.
// It pipes JSON to the write-file CLI tool and returns a summary with diff.
type WriteFileTool struct {
	container *ContainerClient
}

// NewWriteFileTool creates a WriteFileTool with the given container client.
func NewWriteFileTool(container *ContainerClient) *WriteFileTool {
	return &WriteFileTool{container: container}
}

func (t *WriteFileTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        "write_file",
		Description: "Create a new file or overwrite an existing one. Returns a summary (line count, byte count) and a unified diff if overwriting. Use for new files or complete rewrites; prefer edit_file for targeted changes.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_path": {
					"type": "string",
					"description": "Path to the file, relative to /workspace (e.g. 'src/main.go')"
				},
				"content": {
					"type": "string",
					"description": "The full content to write to the file"
				}
			},
			"required": ["file_path", "content"]
		}`),
	}
}

type writeFileInput struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// writeFileResponse matches the JSON output from the write-file CLI tool.
type writeFileResponse struct {
	OK      bool   `json:"ok"`
	Created bool   `json:"created,omitempty"`
	Diff    string `json:"diff,omitempty"`
	Summary string `json:"summary,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (t *WriteFileTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in writeFileInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid write_file input: %w", err)
	}
	if in.FilePath == "" {
		return "", fmt.Errorf("file_path is required")
	}

	// Resolve path relative to /workspace.
	filePath := in.FilePath
	if !strings.HasPrefix(filePath, "/") {
		filePath = "/workspace/" + filePath
	}

	// Build JSON input for the CLI tool with the resolved path.
	cliInput := writeFileInput{
		FilePath: filePath,
		Content:  in.Content,
	}
	inputJSON, err := json.Marshal(cliInput)
	if err != nil {
		return "", fmt.Errorf("marshalling write-file input: %w", err)
	}

	cmd := fmt.Sprintf("echo %s | write-file", shellQuote(string(inputJSON)))
	result, err := t.container.Exec(cmd, 30)
	if err != nil {
		return "", err
	}

	output := strings.TrimSpace(result.Stdout + result.Stderr)

	var resp writeFileResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		return fmt.Sprintf("write-file error: %s", output), nil
	}

	if !resp.OK {
		return fmt.Sprintf("error: %s", resp.Error), nil
	}

	// Build result: summary first, then diff if present.
	var parts []string
	if resp.Summary != "" {
		parts = append(parts, resp.Summary)
	}
	if resp.Diff != "" {
		parts = append(parts, resp.Diff)
	}
	if len(parts) == 0 {
		return "File written.", nil
	}
	return strings.Join(parts, "\n"), nil
}

func (t *WriteFileTool) RequiresApproval(_ json.RawMessage) bool {
	return false
}
