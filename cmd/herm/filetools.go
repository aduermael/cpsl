// filetools.go implements container-based file tools (glob, grep, read, edit,
// write, and outline) that execute inside the Docker container via ripgrep and
// shell commands.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"langdag.com/langdag/types"
)

// sanitizeToolJSON escapes literal control characters (U+0000–U+001F) inside
// JSON string values. LLMs sometimes emit raw tabs or newlines in tool_use
// input, which violates the JSON spec and causes Go's json.Unmarshal to fail.
func sanitizeToolJSON(raw json.RawMessage) json.RawMessage {
	// Quick scan: skip allocation if no control chars are present.
	needsFix := false
	for _, b := range raw {
		if b < 0x20 && b != '\n' && b != '\r' {
			needsFix = true
			break
		}
	}
	if !needsFix {
		return raw
	}

	var buf bytes.Buffer
	buf.Grow(len(raw))
	inString := false
	escaped := false
	for _, b := range raw {
		if escaped {
			buf.WriteByte(b)
			escaped = false
			continue
		}
		if inString && b == '\\' {
			buf.WriteByte(b)
			escaped = true
			continue
		}
		if b == '"' {
			inString = !inString
		}
		if inString && b < 0x20 {
			fmt.Fprintf(&buf, "\\u%04x", b)
		} else {
			buf.WriteByte(b)
		}
	}
	return json.RawMessage(buf.Bytes())
}

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
		Description: getToolDescription("glob", "Find files by glob pattern. Returns matching paths sorted alphabetically, one per line. Respects .gitignore."),
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {
					"type": "string",
					"description": "Glob pattern (supports ** for recursive matching, e.g. '**/*.go', 'src/**/*.ts')"
				},
				"path": {
					"type": "string",
					"description": "Directory to search in, relative to the project root (default: project root)"
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
	searchDir := t.container.WorkDir()
	if in.Path != "" {
		p := in.Path
		// Make relative paths resolve against the project root.
		if !strings.HasPrefix(p, "/") {
			p = t.container.WorkDir() + "/" + p
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

func (t *GlobTool) HostTool() bool { return false }

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
		Description: getToolDescription("grep", "Search file contents by regex pattern. Returns matching files, lines, or counts. Respects .gitignore."),
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {
					"type": "string",
					"description": "Regex pattern to search for (ripgrep syntax)"
				},
				"path": {
					"type": "string",
					"description": "Directory or file to search in, relative to the project root (default: project root)"
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
				},
				"case_insensitive": {
					"type": "boolean",
					"description": "Case-insensitive search (default: false)"
				}
			},
			"required": ["pattern"]
		}`),
	}
}

type grepInput struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path,omitempty"`
	Glob            string `json:"glob,omitempty"`
	Context         int    `json:"context,omitempty"`
	OutputMode      string `json:"output_mode,omitempty"`
	CaseInsensitive bool   `json:"case_insensitive,omitempty"`
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
	if in.CaseInsensitive {
		args = append(args, "-i")
	}

	args = append(args, "--", shellQuote(in.Pattern))

	// Search path.
	searchDir := t.container.WorkDir()
	if in.Path != "" {
		p := in.Path
		if !strings.HasPrefix(p, "/") {
			p = t.container.WorkDir() + "/" + p
		}
		searchDir = p
	}

	cmd := fmt.Sprintf("cd %s && %s %s 2>&1",
		shellQuote(t.container.WorkDir()), strings.Join(args, " "), shellQuote(searchDir))

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

func (t *GrepTool) HostTool() bool { return false }

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
		Description: getToolDescription("read_file", "Read file contents with line numbers. Supports reading specific line ranges to avoid loading entire large files."),
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_path": {
					"type": "string",
					"description": "Path to the file, relative to the project root (e.g. 'src/main.go')"
				},
				"offset": {
					"type": "integer",
					"description": "Start line number (1-based, default: 1)"
				},
				"limit": {
					"type": "integer",
					"description": "Maximum lines to read (default: 500)"
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

	// Resolve path relative to the project root.
	filePath := in.FilePath
	if !strings.HasPrefix(filePath, "/") {
		filePath = t.container.WorkDir() + "/" + filePath
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

func (t *ReadFileTool) HostTool() bool { return false }

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
		Description: getToolDescription("edit_file", "Replace a specific string in a file. old_string must appear exactly once unless replace_all is true. Returns a unified diff showing the change."),
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_path": {
					"type": "string",
					"description": "Path to the file, relative to the project root (e.g. 'src/main.go')"
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
	if err := json.Unmarshal(sanitizeToolJSON(input), &in); err != nil {
		return "", fmt.Errorf("invalid edit_file input: %w", err)
	}
	if in.FilePath == "" {
		return "", fmt.Errorf("file_path is required")
	}
	if in.OldString == "" {
		return "", fmt.Errorf("old_string is required")
	}

	// Resolve path relative to project root.
	filePath := in.FilePath
	if !strings.HasPrefix(filePath, "/") {
		filePath = t.container.WorkDir() + "/" + filePath
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

	result, err := t.container.ExecWithStdin(inputJSON, 30, "edit-file")
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

func (t *EditFileTool) HostTool() bool { return false }

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
		Description: getToolDescription("write_file", "Create a new file or overwrite an existing one. Returns a summary (line count, byte count) and a unified diff if overwriting."),
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_path": {
					"type": "string",
					"description": "Path to the file, relative to the project root (e.g. 'src/main.go')"
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
	if err := json.Unmarshal(sanitizeToolJSON(input), &in); err != nil {
		return "", fmt.Errorf("invalid write_file input: %w", err)
	}
	if in.FilePath == "" {
		return "", fmt.Errorf("file_path is required")
	}

	// Resolve path relative to project root.
	filePath := in.FilePath
	if !strings.HasPrefix(filePath, "/") {
		filePath = t.container.WorkDir() + "/" + filePath
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

	result, err := t.container.ExecWithStdin(inputJSON, 30, "write-file")
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

func (t *WriteFileTool) HostTool() bool { return false }

// OutlineTool extracts function/type signatures from a file without reading
// the full content. Returns a compact outline with line numbers (~50-100 tokens
// instead of ~2000-5000 for a full read).
type OutlineTool struct {
	container *ContainerClient
}

// NewOutlineTool creates an OutlineTool with the given container client.
func NewOutlineTool(container *ContainerClient) *OutlineTool {
	return &OutlineTool{container: container}
}

func (t *OutlineTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        "outline",
		Description: getToolDescription("outline", "Extract function/type/class signatures from one or more files. Returns a compact outline with line numbers — much cheaper than reading the full file."),
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_path": {
					"type": "string",
					"description": "Path to a single file, relative to the project root (e.g. 'src/main.go')"
				},
				"file_paths": {
					"type": "array",
					"items": {"type": "string"},
					"description": "Paths to multiple files, relative to the project root. Use instead of file_path to outline several files in one call (max 20)."
				}
			}
		}`),
	}
}

// outlineMaxFiles is the maximum number of files allowed in a single multi-file outline call.
const outlineMaxFiles = 20

type outlineInput struct {
	FilePath  string   `json:"file_path"`
	FilePaths []string `json:"file_paths,omitempty"`
}

func (t *OutlineTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in outlineInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid outline input: %w", err)
	}

	// Build the list of files to outline.
	var paths []string
	if in.FilePath != "" {
		paths = append(paths, in.FilePath)
	}
	paths = append(paths, in.FilePaths...)

	if len(paths) == 0 {
		return "", fmt.Errorf("file_path or file_paths is required")
	}
	if len(paths) > outlineMaxFiles {
		return "", fmt.Errorf("too many files: %d (max %d)", len(paths), outlineMaxFiles)
	}

	// Single file — return directly (no header).
	if len(paths) == 1 {
		return t.outlineOne(paths[0])
	}

	// Multiple files — combine with headers.
	var parts []string
	for _, p := range paths {
		result, err := t.outlineOne(p)
		if err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("=== %s ===\n%s", p, result))
	}
	return strings.Join(parts, "\n\n"), nil
}

// outlineOne outlines a single file and returns its output or an inline error string.
func (t *OutlineTool) outlineOne(displayPath string) (string, error) {
	filePath := displayPath
	if !strings.HasPrefix(filePath, "/") {
		filePath = t.container.WorkDir() + "/" + filePath
	}

	result, err := t.container.Exec(fmt.Sprintf("outline %s", shellQuote(filePath)), 15)
	if err != nil {
		return "", err
	}

	output := strings.TrimRight(result.Stdout, "\n")
	stderr := strings.TrimSpace(result.Stderr)

	// Binary not found (old container image) — fall back to grep.
	if result.ExitCode == 127 || (result.ExitCode != 0 && strings.Contains(stderr, "not found")) {
		return t.outlineFallback(filePath, displayPath)
	}

	if result.ExitCode != 0 {
		if stderr != "" {
			return fmt.Sprintf("error: %s", stderr), nil
		}
		return fmt.Sprintf("error: cannot read %s", displayPath), nil
	}

	if output == "" {
		return "(no declarations found)", nil
	}

	return output, nil
}

// outlineFallback uses grep when the outline binary is missing (old container image).
func (t *OutlineTool) outlineFallback(filePath, displayPath string) (string, error) {
	ext := ""
	if dot := strings.LastIndex(displayPath, "."); dot >= 0 {
		ext = displayPath[dot:]
	}

	// Try a basic grep for common patterns.
	pattern := ""
	switch ext {
	case ".go":
		pattern = `^(func |type |var |const |package )`
	case ".py":
		pattern = `^(class |def |async def |\s+def |\s+async def )`
	case ".js", ".jsx":
		pattern = `^(export |function |class |const |let |var |async function )`
	case ".ts", ".tsx":
		pattern = `^(export |function |class |const |let |var |interface |type |enum |async function )`
	case ".rs":
		pattern = `^(pub |fn |struct |enum |trait |impl |mod |type |use )`
	}

	if pattern != "" {
		cmd := fmt.Sprintf("grep -n -E %s %s 2>&1 | head -n 101", shellQuote(pattern), shellQuote(filePath))
		result, err := t.container.Exec(cmd, 15)
		if err != nil {
			return "", err
		}
		output := strings.TrimRight(result.Stdout+result.Stderr, "\n")
		if result.ExitCode == 1 || output == "" {
			return "(no declarations found)", nil
		}
		return output, nil
	}

	// Unknown language — head + tail.
	cmd := fmt.Sprintf("(head -n 20 %s && echo '---' && tail -n 20 %s) 2>&1", shellQuote(filePath), shellQuote(filePath))
	result, err := t.container.Exec(cmd, 10)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(result.Stdout+result.Stderr, "\n"), nil
}

func (t *OutlineTool) RequiresApproval(_ json.RawMessage) bool {
	return false
}

func (t *OutlineTool) HostTool() bool { return false }
