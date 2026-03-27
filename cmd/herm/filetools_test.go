package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newFakeContainer creates a started ContainerClient with a fakeDockerCommand
// that returns the given stdout/stderr/exitCode for any exec call.
// The execHandler receives either:
//   - the shell command string (for Exec calls via "sh -c"), or
//   - the binary name (for ExecWithStdin calls via "exec -i").
func newFakeContainer(t *testing.T, execHandler func(cmd string) (string, string, int)) *ContainerClient {
	t.Helper()
	orig := dockerCommand
	t.Cleanup(func() { dockerCommand = orig })

	dockerCommand = fakeDockerCommand(func(args []string) (string, string, int) {
		if len(args) >= 2 {
			switch args[1] {
			case "run":
				return "fake-container-id\n", "", 0
			case "exec":
				// Direct binary exec: docker exec -i -w <workDir> <id> <binary> [args...]
				if len(args) >= 7 && args[2] == "-i" {
					return execHandler(args[6])
				}
				// Shell exec: docker exec -w <workDir> <id> sh -c <cmd>
				if len(args) >= 8 {
					cmd := args[7]
					return execHandler(cmd)
				}
				return "", "no command", 1
			case "rm":
				return "", "", 0
			}
		}
		return "", "unknown", 1
	})

	c := NewContainerClient(ContainerConfig{Image: "test:latest"})
	if err := c.Start("/workspace", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return c
}

// newFakeContainerWithStdinCapture creates a started ContainerClient that captures
// stdin bytes sent via ExecWithStdin to a temp file. Returns the container and
// a function to read the captured bytes.
func newFakeContainerWithStdinCapture(t *testing.T, execHandler func(cmd string) (string, string, int)) (*ContainerClient, func() []byte) {
	t.Helper()
	orig := dockerCommand
	t.Cleanup(func() { dockerCommand = orig })

	stdinFile := filepath.Join(t.TempDir(), "stdin.json")

	dockerCommand = fakeDockerCommandWithStdin(func(args []string) (string, string, int) {
		if len(args) >= 2 {
			switch args[1] {
			case "run":
				return "fake-container-id\n", "", 0
			case "exec":
				// Direct binary exec: docker exec -i -w <workDir> <id> <binary> [args...]
				if len(args) >= 7 && args[2] == "-i" {
					return execHandler(args[6])
				}
				// Shell exec: docker exec -w <workDir> <id> sh -c <cmd>
				if len(args) >= 8 {
					return execHandler(args[7])
				}
				return "", "no command", 1
			case "rm":
				return "", "", 0
			}
		}
		return "", "unknown", 1
	}, stdinFile)

	c := NewContainerClient(ContainerConfig{Image: "test:latest"})
	if err := c.Start("/workspace", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}

	readCapture := func() []byte {
		data, err := os.ReadFile(stdinFile)
		if err != nil {
			t.Fatalf("reading captured stdin: %v", err)
		}
		return data
	}
	return c, readCapture
}

// --- GlobTool tests ---

func TestGlobTool_Definition(t *testing.T) {
	c := &ContainerClient{}
	tool := NewGlobTool(c)
	def := tool.Definition()

	if def.Name != "glob" {
		t.Errorf("Name = %q, want %q", def.Name, "glob")
	}
	if def.InputSchema == nil {
		t.Error("InputSchema should not be nil")
	}
}

func TestGlobTool_Execute_MatchesFound(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		return "cmd/herm/agent.go\ncmd/herm/tools.go\n", "", 0
	})

	tool := NewGlobTool(container)
	input, _ := json.Marshal(globInput{Pattern: "**/*.go"})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "agent.go") {
		t.Errorf("result should contain agent.go, got: %q", result)
	}
	if !strings.Contains(result, "tools.go") {
		t.Errorf("result should contain tools.go, got: %q", result)
	}
}

func TestGlobTool_Execute_NoMatches(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		return "", "", 1 // rg exit 1 = no matches
	})

	tool := NewGlobTool(container)
	input, _ := json.Marshal(globInput{Pattern: "**/*.xyz"})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result != "No files matched." {
		t.Errorf("expected 'No files matched.', got: %q", result)
	}
}

func TestGlobTool_Execute_WithPath(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		if !strings.Contains(cmd, "/workspace/src") {
			return "", "wrong path", 1
		}
		return "main.go\n", "", 0
	})

	tool := NewGlobTool(container)
	input, _ := json.Marshal(globInput{Pattern: "*.go", Path: "src"})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "main.go") {
		t.Errorf("expected main.go in result, got: %q", result)
	}
}

func TestGlobTool_Execute_EmptyPattern(t *testing.T) {
	c := &ContainerClient{}
	tool := NewGlobTool(c)
	input, _ := json.Marshal(globInput{Pattern: ""})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Error("expected error for empty pattern")
	}
}

func TestGlobTool_Execute_Truncation(t *testing.T) {
	// Generate more than globMaxFiles lines.
	var lines []string
	for i := 0; i < globMaxFiles+100; i++ {
		lines = append(lines, "file"+string(rune('0'+i%10))+".go")
	}
	output := strings.Join(lines, "\n") + "\n"

	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		return output, "", 0
	})

	tool := NewGlobTool(container)
	input, _ := json.Marshal(globInput{Pattern: "**/*.go"})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "showing first") {
		t.Error("expected truncation message in result")
	}
}

// --- GrepTool tests ---

func TestGrepTool_Definition(t *testing.T) {
	c := &ContainerClient{}
	tool := NewGrepTool(c)
	def := tool.Definition()

	if def.Name != "grep" {
		t.Errorf("Name = %q, want %q", def.Name, "grep")
	}
}

func TestGrepTool_Execute_FilesWithMatches(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		if !strings.Contains(cmd, "-l") {
			return "", "expected -l flag", 1
		}
		return "cmd/herm/agent.go\ncmd/herm/tools.go\n", "", 0
	})

	tool := NewGrepTool(container)
	input, _ := json.Marshal(grepInput{Pattern: "func Execute"})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "agent.go") {
		t.Errorf("expected agent.go in result, got: %q", result)
	}
}

func TestGrepTool_Execute_ContentMode(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		if !strings.Contains(cmd, "-n") {
			return "", "expected -n flag", 1
		}
		return "tools.go:64:func (t *BashTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {\n", "", 0
	})

	tool := NewGrepTool(container)
	input, _ := json.Marshal(grepInput{Pattern: "func.*Execute", OutputMode: "content"})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "BashTool") {
		t.Errorf("expected BashTool in result, got: %q", result)
	}
}

func TestGrepTool_Execute_CountMode(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		if !strings.Contains(cmd, "-c") {
			return "", "expected -c flag", 1
		}
		return "tools.go:3\nagent.go:2\n", "", 0
	})

	tool := NewGrepTool(container)
	input, _ := json.Marshal(grepInput{Pattern: "Execute", OutputMode: "count"})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "tools.go:3") {
		t.Errorf("expected count output, got: %q", result)
	}
}

func TestGrepTool_Execute_NoMatches(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		return "", "", 1
	})

	tool := NewGrepTool(container)
	input, _ := json.Marshal(grepInput{Pattern: "nonexistent_xyz"})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result != "No matches found." {
		t.Errorf("expected 'No matches found.', got: %q", result)
	}
}

func TestGrepTool_Execute_WithGlobFilter(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		if !strings.Contains(cmd, "-g") || !strings.Contains(cmd, "*.go") {
			return "", "expected glob filter", 1
		}
		return "main.go\n", "", 0
	})

	tool := NewGrepTool(container)
	input, _ := json.Marshal(grepInput{Pattern: "func main", Glob: "*.go"})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "main.go") {
		t.Errorf("expected main.go in result, got: %q", result)
	}
}

func TestGrepTool_Execute_WithContext(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		if !strings.Contains(cmd, "-C3") {
			return "", "expected context flag", 1
		}
		return "match with context\n", "", 0
	})

	tool := NewGrepTool(container)
	input, _ := json.Marshal(grepInput{Pattern: "test", OutputMode: "content", Context: 3})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "match with context") {
		t.Errorf("expected context output, got: %q", result)
	}
}

func TestGrepTool_Execute_EmptyPattern(t *testing.T) {
	c := &ContainerClient{}
	tool := NewGrepTool(c)
	input, _ := json.Marshal(grepInput{Pattern: ""})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Error("expected error for empty pattern")
	}
}

// --- ReadFileTool tests ---

func TestReadFileTool_Definition(t *testing.T) {
	c := &ContainerClient{}
	tool := NewReadFileTool(c)
	def := tool.Definition()

	if def.Name != "read_file" {
		t.Errorf("Name = %q, want %q", def.Name, "read_file")
	}
}

func TestReadFileTool_Execute_FullFile(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		if strings.Contains(cmd, "awk") {
			return "     1\tpackage main\n     2\t\n     3\tfunc main() {\n", "", 0
		}
		return "", "unexpected command", 1
	})

	tool := NewReadFileTool(container)
	input, _ := json.Marshal(readFileInput{FilePath: "main.go"})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "package main") {
		t.Errorf("expected file content, got: %q", result)
	}
	if !strings.Contains(result, "1\t") {
		t.Errorf("expected line numbers, got: %q", result)
	}
}

func TestReadFileTool_Execute_WithRange(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		if strings.Contains(cmd, "awk") {
			// Verify the awk command includes the correct line range.
			if strings.Contains(cmd, "NR>=10") && strings.Contains(cmd, "NR<=19") {
				return "    10\tline 10\n    11\tline 11\n", "", 0
			}
		}
		return "", "unexpected command", 1
	})

	tool := NewReadFileTool(container)
	input, _ := json.Marshal(readFileInput{FilePath: "main.go", Offset: 10, Limit: 10})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "line 10") {
		t.Errorf("expected line 10, got: %q", result)
	}
}

func TestReadFileTool_Execute_EmptyFile(t *testing.T) {
	callCount := 0
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		callCount++
		if strings.Contains(cmd, "awk") {
			return "", "", 0 // empty output
		}
		if strings.Contains(cmd, "wc -l") {
			return "0\n", "", 0 // 0 lines
		}
		return "", "unexpected", 1
	})

	tool := NewReadFileTool(container)
	input, _ := json.Marshal(readFileInput{FilePath: "empty.txt"})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result != "(empty file)" {
		t.Errorf("expected '(empty file)', got: %q", result)
	}
}

func TestReadFileTool_Execute_FileNotFound(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		if strings.Contains(cmd, "awk") {
			return "awk: can't open file\n", "", 2
		}
		if strings.Contains(cmd, "wc") {
			return "", "No such file", 1
		}
		return "", "", 1
	})

	tool := NewReadFileTool(container)
	input, _ := json.Marshal(readFileInput{FilePath: "nonexistent.go"})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "error") && !strings.Contains(result, "cannot read") {
		t.Errorf("expected error message, got: %q", result)
	}
}

func TestReadFileTool_Execute_EmptyPath(t *testing.T) {
	c := &ContainerClient{}
	tool := NewReadFileTool(c)
	input, _ := json.Marshal(readFileInput{FilePath: ""})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Error("expected error for empty file_path")
	}
}

func TestReadFileTool_Execute_TruncationIndicator(t *testing.T) {
	// Return exactly readFileDefaultLimit lines worth of content.
	var lines []string
	for i := 1; i <= readFileDefaultLimit; i++ {
		lines = append(lines, "     1\tline content")
	}
	output := strings.Join(lines, "\n") + "\n"

	callCount := 0
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		callCount++
		if strings.Contains(cmd, "awk") {
			return output, "", 0
		}
		if strings.Contains(cmd, "wc -l") {
			return "5000\n", "", 0 // total lines > limit
		}
		return "", "", 1
	})

	tool := NewReadFileTool(container)
	input, _ := json.Marshal(readFileInput{FilePath: "big.go"})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "showing lines") {
		t.Errorf("expected truncation indicator, got tail: %q", result[max(0, len(result)-100):])
	}
}

// --- shellQuote tests ---

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"simple", "'simple'"},
		{"**/*.go", "'**/*.go'"},
		{"it's", "'it'\\''s'"},
		{"", "''"},
	}
	for _, tt := range tests {
		got := shellQuote(tt.input)
		if got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- EditFileTool tests ---

func TestEditFileTool_Definition(t *testing.T) {
	c := &ContainerClient{}
	tool := NewEditFileTool(c)
	def := tool.Definition()

	if def.Name != "edit_file" {
		t.Errorf("Name = %q, want %q", def.Name, "edit_file")
	}
	if def.InputSchema == nil {
		t.Error("InputSchema should not be nil")
	}
}

func TestEditFileTool_Execute_Success(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		if !strings.Contains(cmd, "edit-file") {
			return "", "unexpected command", 1
		}
		return `{"ok":true,"diff":"--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new"}`, "", 0
	})

	tool := NewEditFileTool(container)
	input, _ := json.Marshal(editFileInput{
		FilePath:  "main.go",
		OldString: "old",
		NewString: "new",
	})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "--- a/main.go") {
		t.Errorf("expected diff output, got: %q", result)
	}
}

func TestEditFileTool_Execute_NotFound(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		return `{"ok":false,"error":"old_string not found in file"}`, "", 0
	})

	tool := NewEditFileTool(container)
	input, _ := json.Marshal(editFileInput{
		FilePath:  "main.go",
		OldString: "missing",
		NewString: "new",
	})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "error") || !strings.Contains(result, "not found") {
		t.Errorf("expected error about not found, got: %q", result)
	}
}

func TestEditFileTool_Execute_NotUnique(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		return `{"ok":false,"error":"old_string found 3 times (must be unique, or use replace_all)"}`, "", 0
	})

	tool := NewEditFileTool(container)
	input, _ := json.Marshal(editFileInput{
		FilePath:  "main.go",
		OldString: "duplicate",
		NewString: "new",
	})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "3 times") {
		t.Errorf("expected count in error, got: %q", result)
	}
}

func TestEditFileTool_Execute_ReplaceAll(t *testing.T) {
	container, readStdin := newFakeContainerWithStdinCapture(t, func(cmd string) (string, string, int) {
		if cmd == "edit-file" {
			return `{"ok":true,"diff":"@@ multi-replace @@"}`, "", 0
		}
		return "", "unexpected", 1
	})

	tool := NewEditFileTool(container)
	input, _ := json.Marshal(editFileInput{
		FilePath:   "main.go",
		OldString:  "old",
		NewString:  "new",
		ReplaceAll: true,
	})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "multi-replace") {
		t.Errorf("expected diff output, got: %q", result)
	}

	// Verify replace_all was sent in stdin JSON.
	captured := string(readStdin())
	if !strings.Contains(captured, `"replace_all":true`) {
		t.Errorf("expected replace_all in stdin, got: %q", captured)
	}
}

func TestEditFileTool_Execute_InvalidJSON(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		return "not valid json at all", "", 1
	})

	tool := NewEditFileTool(container)
	input, _ := json.Marshal(editFileInput{
		FilePath:  "main.go",
		OldString: "old",
		NewString: "new",
	})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "edit-file error") {
		t.Errorf("expected error fallback, got: %q", result)
	}
}

func TestEditFileTool_Execute_EmptyPath(t *testing.T) {
	c := &ContainerClient{}
	tool := NewEditFileTool(c)
	input, _ := json.Marshal(editFileInput{FilePath: "", OldString: "a", NewString: "b"})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Error("expected error for empty file_path")
	}
}

func TestEditFileTool_Execute_EmptyOldString(t *testing.T) {
	c := &ContainerClient{}
	tool := NewEditFileTool(c)
	input, _ := json.Marshal(editFileInput{FilePath: "main.go", OldString: "", NewString: "b"})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Error("expected error for empty old_string")
	}
}

func TestEditFileTool_Execute_RelativePath(t *testing.T) {
	container, readStdin := newFakeContainerWithStdinCapture(t, func(cmd string) (string, string, int) {
		if cmd == "edit-file" {
			return `{"ok":true,"diff":"ok"}`, "", 0
		}
		return "", "unexpected", 1
	})

	tool := NewEditFileTool(container)
	input, _ := json.Marshal(editFileInput{
		FilePath:  "src/main.go",
		OldString: "old",
		NewString: "new",
	})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result != "ok" {
		t.Errorf("expected 'ok', got: %q", result)
	}

	// Verify the path was resolved to /workspace/src/main.go in stdin JSON.
	captured := string(readStdin())
	if !strings.Contains(captured, "/workspace/src/main.go") {
		t.Errorf("expected resolved path in stdin, got: %q", captured)
	}
}

// --- WriteFileTool tests ---

func TestWriteFileTool_Definition(t *testing.T) {
	c := &ContainerClient{}
	tool := NewWriteFileTool(c)
	def := tool.Definition()

	if def.Name != "write_file" {
		t.Errorf("Name = %q, want %q", def.Name, "write_file")
	}
	if def.InputSchema == nil {
		t.Error("InputSchema should not be nil")
	}
}

func TestWriteFileTool_Execute_CreateNew(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		if !strings.Contains(cmd, "write-file") {
			return "", "unexpected command", 1
		}
		return `{"ok":true,"created":true,"summary":"Created main.go (10 lines, 245 bytes)"}`, "", 0
	})

	tool := NewWriteFileTool(container)
	input, _ := json.Marshal(writeFileInput{
		FilePath: "main.go",
		Content:  "package main\n",
	})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "Created") {
		t.Errorf("expected creation summary, got: %q", result)
	}
}

func TestWriteFileTool_Execute_Overwrite(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		return `{"ok":true,"created":false,"summary":"Wrote main.go (5 lines, 100 bytes)","diff":"--- a/main.go\n+++ b/main.go\n@@ -1,2 +1,2 @@\n-old\n+new"}`, "", 0
	})

	tool := NewWriteFileTool(container)
	input, _ := json.Marshal(writeFileInput{
		FilePath: "main.go",
		Content:  "new content\n",
	})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "Wrote") {
		t.Errorf("expected write summary, got: %q", result)
	}
	if !strings.Contains(result, "--- a/main.go") {
		t.Errorf("expected diff in overwrite result, got: %q", result)
	}
}

func TestWriteFileTool_Execute_Error(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		return `{"ok":false,"error":"permission denied"}`, "", 0
	})

	tool := NewWriteFileTool(container)
	input, _ := json.Marshal(writeFileInput{
		FilePath: "/etc/readonly",
		Content:  "test",
	})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "error") || !strings.Contains(result, "permission denied") {
		t.Errorf("expected error message, got: %q", result)
	}
}

func TestWriteFileTool_Execute_InvalidJSON(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		return "broken output", "", 1
	})

	tool := NewWriteFileTool(container)
	input, _ := json.Marshal(writeFileInput{
		FilePath: "test.txt",
		Content:  "hello",
	})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "write-file error") {
		t.Errorf("expected error fallback, got: %q", result)
	}
}

func TestWriteFileTool_Execute_EmptyPath(t *testing.T) {
	c := &ContainerClient{}
	tool := NewWriteFileTool(c)
	input, _ := json.Marshal(writeFileInput{FilePath: "", Content: "test"})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Error("expected error for empty file_path")
	}
}

func TestWriteFileTool_Execute_EmptyResponse(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		return `{"ok":true}`, "", 0
	})

	tool := NewWriteFileTool(container)
	input, _ := json.Marshal(writeFileInput{
		FilePath: "test.txt",
		Content:  "hello",
	})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result != "File written." {
		t.Errorf("expected 'File written.', got: %q", result)
	}
}

func TestWriteFileTool_Execute_RelativePath(t *testing.T) {
	container, readStdin := newFakeContainerWithStdinCapture(t, func(cmd string) (string, string, int) {
		if cmd == "write-file" {
			return `{"ok":true,"summary":"Created src/new.go"}`, "", 0
		}
		return "", "unexpected", 1
	})

	tool := NewWriteFileTool(container)
	input, _ := json.Marshal(writeFileInput{
		FilePath: "src/new.go",
		Content:  "package src\n",
	})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "Created") {
		t.Errorf("expected success, got: %q", result)
	}

	// Verify the path was resolved to /workspace/src/new.go in stdin JSON.
	captured := string(readStdin())
	if !strings.Contains(captured, "/workspace/src/new.go") {
		t.Errorf("expected resolved path in stdin, got: %q", captured)
	}
}

// --- sanitizeToolJSON tests ---

func TestSanitizeToolJSON_NoControlChars(t *testing.T) {
	input := json.RawMessage(`{"old_string":"hello world","new_string":"goodbye"}`)
	got := sanitizeToolJSON(input)
	if !bytes.Equal(got, input) {
		t.Errorf("expected no change, got %s", got)
	}
}

func TestSanitizeToolJSON_LiteralTab(t *testing.T) {
	// Simulate what LLMs produce: literal 0x09 inside a JSON string value.
	input := json.RawMessage("{\"old_string\":\"before\tafter\"}")
	got := sanitizeToolJSON(input)

	// Should now be valid JSON with the tab escaped as \u0009.
	var parsed map[string]string
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("sanitized JSON should be valid, got error: %v\nJSON: %s", err, got)
	}
	if parsed["old_string"] != "before\tafter" {
		t.Errorf("expected 'before\\tafter', got %q", parsed["old_string"])
	}
}

func TestSanitizeToolJSON_MultipleTabs(t *testing.T) {
	input := json.RawMessage("{\"a\":\"\t\t\",\"b\":\"x\ty\"}")
	got := sanitizeToolJSON(input)

	var parsed map[string]string
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("sanitized JSON should be valid: %v", err)
	}
	if parsed["a"] != "\t\t" {
		t.Errorf("a = %q, want two tabs", parsed["a"])
	}
	if parsed["b"] != "x\ty" {
		t.Errorf("b = %q, want 'x\\ty'", parsed["b"])
	}
}

func TestSanitizeToolJSON_PreservesEscapedBackslash(t *testing.T) {
	// A properly escaped \" inside a string should not confuse the parser.
	input := json.RawMessage(`{"a":"say \"hello\"","b":"ok"}`)
	got := sanitizeToolJSON(input)

	var parsed map[string]string
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("parse error: %v\nJSON: %s", err, got)
	}
	if parsed["a"] != `say "hello"` {
		t.Errorf("a = %q, want 'say \"hello\"'", parsed["a"])
	}
}

func TestSanitizeToolJSON_ControlCharOutsideString(t *testing.T) {
	// Newlines/CRs between keys are common formatting — should be left alone.
	input := json.RawMessage("{\n\"a\": \"b\"\n}")
	got := sanitizeToolJSON(input)
	if !bytes.Equal(got, input) {
		t.Errorf("should not modify newlines outside strings, got %s", got)
	}
}

func TestSanitizeToolJSON_AllControlChars(t *testing.T) {
	// Test all control chars 0x00-0x1F except \n and \r inside a string.
	var raw bytes.Buffer
	raw.WriteString(`{"v":"`)
	for b := byte(0); b < 0x20; b++ {
		if b == '\n' || b == '\r' || b == '"' || b == '\\' {
			continue
		}
		raw.WriteByte(b)
	}
	raw.WriteString(`"}`)

	got := sanitizeToolJSON(json.RawMessage(raw.Bytes()))
	var parsed map[string]string
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("sanitized JSON with all control chars should be valid: %v\nJSON: %s", err, got)
	}
}

func TestEditFileTool_Execute_LiteralTabInInput(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		if !strings.Contains(cmd, "edit-file") {
			return "", "unexpected command", 1
		}
		return `{"ok":true,"diff":"--- a/main.go\n+++ b/main.go\n@@ ok @@"}`, "", 0
	})

	tool := NewEditFileTool(container)
	// Simulate LLM sending literal tab bytes in JSON (invalid JSON per spec).
	input := json.RawMessage("{\"file_path\":\"main.go\",\"old_string\":\"before\tafter\",\"new_string\":\"fixed\"}")

	// Without sanitization this would fail with "invalid character '\t'"
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute should succeed with literal tabs, got: %v", err)
	}
	if !strings.Contains(result, "--- a/main.go") {
		t.Errorf("expected diff output, got: %q", result)
	}
}

func TestWriteFileTool_Execute_LiteralTabInInput(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		if !strings.Contains(cmd, "write-file") {
			return "", "unexpected command", 1
		}
		return `{"ok":true,"created":true,"summary":"Created test.go (3 lines, 50 bytes)"}`, "", 0
	})

	tool := NewWriteFileTool(container)
	// Simulate LLM sending literal tab bytes in content.
	input := json.RawMessage("{\"file_path\":\"test.go\",\"content\":\"func main() {\t\n\treturn\n}\"}")

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute should succeed with literal tabs, got: %v", err)
	}
	if !strings.Contains(result, "Created") {
		t.Errorf("expected creation summary, got: %q", result)
	}
}

// --- ExecWithStdin pipeline tests (7d) ---

// TestEditFileTool_Execute_MultiLineContent verifies that content containing
// \n, \t, \\, \", and single quotes survives json.Marshal → ExecWithStdin → stdin.
// With the shell removed from the path, this is correct by construction, but the
// test documents the guarantee.
func TestEditFileTool_Execute_MultiLineContent(t *testing.T) {
	container, readStdin := newFakeContainerWithStdinCapture(t, func(cmd string) (string, string, int) {
		return `{"ok":true,"diff":"@@ ok @@"}`, "", 0
	})

	tool := NewEditFileTool(container)
	input, _ := json.Marshal(editFileInput{
		FilePath:  "main.go",
		OldString: "line1\nline2\ttab\n\t\"quoted\"\n\\backslash\n'single'",
		NewString: "replaced\nwith\nnewlines\tand\ttabs",
	})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "@@ ok @@") {
		t.Errorf("expected diff output, got: %q", result)
	}

	// Verify the JSON sent to the binary is valid and contains the special chars.
	captured := readStdin()
	var parsed editFileInput
	if err := json.Unmarshal(captured, &parsed); err != nil {
		t.Fatalf("stdin JSON should be valid: %v\nJSON: %s", err, captured)
	}
	if !strings.Contains(parsed.OldString, "\n") {
		t.Error("expected newline in old_string")
	}
	if !strings.Contains(parsed.OldString, "\t") {
		t.Error("expected tab in old_string")
	}
	if !strings.Contains(parsed.OldString, `"quoted"`) {
		t.Error("expected quotes in old_string")
	}
	if !strings.Contains(parsed.OldString, `\`) {
		t.Error("expected backslash in old_string")
	}
	if !strings.Contains(parsed.OldString, "'") {
		t.Error("expected single quote in old_string")
	}
}

func TestWriteFileTool_Execute_MultiLineContent(t *testing.T) {
	container, readStdin := newFakeContainerWithStdinCapture(t, func(cmd string) (string, string, int) {
		return `{"ok":true,"created":true,"summary":"Created test.go"}`, "", 0
	})

	tool := NewWriteFileTool(container)
	content := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\\nworld\")\n\tfmt.Println('x')\n}\n"
	input, _ := json.Marshal(writeFileInput{
		FilePath: "test.go",
		Content:  content,
	})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "Created") {
		t.Errorf("expected creation summary, got: %q", result)
	}

	// Verify the content survived the pipeline intact.
	captured := readStdin()
	var parsed writeFileInput
	if err := json.Unmarshal(captured, &parsed); err != nil {
		t.Fatalf("stdin JSON should be valid: %v\nJSON: %s", err, captured)
	}
	if parsed.Content != content {
		t.Errorf("content mismatch:\ngot:  %q\nwant: %q", parsed.Content, content)
	}
}

// --- OutlineTool tests ---

func TestOutlineTool_Definition(t *testing.T) {
	c := &ContainerClient{}
	tool := NewOutlineTool(c)
	def := tool.Definition()

	if def.Name != "outline" {
		t.Errorf("Name = %q, want %q", def.Name, "outline")
	}
	if def.InputSchema == nil {
		t.Error("InputSchema should not be nil")
	}
}

func TestOutlineTool_Execute_Binary(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		if strings.Contains(cmd, "outline") {
			return "1\tpackage main\n5\tfunc main()\n10\ttype Config struct\n", "", 0
		}
		return "", "unexpected", 1
	})

	tool := NewOutlineTool(container)
	input, _ := json.Marshal(outlineInput{FilePath: "main.go"})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "func main()") {
		t.Errorf("expected func main in outline, got: %q", result)
	}
	if !strings.Contains(result, "type Config struct") {
		t.Errorf("expected type Config in outline, got: %q", result)
	}
}

func TestOutlineTool_Execute_BinaryNotFound_Fallback(t *testing.T) {
	callCount := 0
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		callCount++
		if callCount == 1 && strings.Contains(cmd, "outline") {
			// Binary not found — exit code 127.
			return "", "sh: outline: not found", 127
		}
		// Fallback grep should be called.
		if strings.Contains(cmd, "grep") {
			return "1:package main\n5:func main() {\n", "", 0
		}
		return "", "unexpected", 1
	})

	tool := NewOutlineTool(container)
	input, _ := json.Marshal(outlineInput{FilePath: "main.go"})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "func main") {
		t.Errorf("expected fallback to produce output, got: %q", result)
	}
}

func TestOutlineTool_Execute_FileNotFound(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		return "", "error: stat /workspace/nonexistent.go: no such file or directory", 1
	})

	tool := NewOutlineTool(container)
	input, _ := json.Marshal(outlineInput{FilePath: "nonexistent.go"})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "error") {
		t.Errorf("expected error message, got: %q", result)
	}
}

func TestOutlineTool_Execute_EmptyOutput(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		return "(no declarations found)\n", "", 0
	})

	tool := NewOutlineTool(container)
	input, _ := json.Marshal(outlineInput{FilePath: "empty.txt"})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "no declarations") {
		t.Errorf("expected 'no declarations', got: %q", result)
	}
}

func TestOutlineTool_Execute_EmptyPath(t *testing.T) {
	c := &ContainerClient{}
	tool := NewOutlineTool(c)
	input, _ := json.Marshal(outlineInput{FilePath: ""})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Error("expected error for empty file_path")
	}
}

func TestOutlineTool_Execute_RelativePath(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		// Verify the path was resolved to /workspace/src/main.go.
		if strings.Contains(cmd, "/workspace/src/main.go") {
			return "1\tpackage main\n", "", 0
		}
		return "", "wrong path", 1
	})

	tool := NewOutlineTool(container)
	input, _ := json.Marshal(outlineInput{FilePath: "src/main.go"})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "package main") {
		t.Errorf("expected output, got: %q", result)
	}
}

func TestOutlineTool_Execute_MultipleFiles(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		if strings.Contains(cmd, "main.go") {
			return "1\tpackage main\n5\tfunc main()\n", "", 0
		}
		if strings.Contains(cmd, "util.go") {
			return "1\tpackage main\n3\tfunc helper()\n", "", 0
		}
		return "", "unexpected", 1
	})

	tool := NewOutlineTool(container)
	input, _ := json.Marshal(outlineInput{FilePaths: []string{"main.go", "util.go"}})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "=== main.go ===") {
		t.Errorf("expected main.go header, got: %q", result)
	}
	if !strings.Contains(result, "=== util.go ===") {
		t.Errorf("expected util.go header, got: %q", result)
	}
	if !strings.Contains(result, "func main()") {
		t.Errorf("expected func main in output, got: %q", result)
	}
	if !strings.Contains(result, "func helper()") {
		t.Errorf("expected func helper in output, got: %q", result)
	}
}

func TestOutlineTool_Execute_MultipleFiles_PartialError(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		if strings.Contains(cmd, "good.go") {
			return "1\tpackage main\n", "", 0
		}
		if strings.Contains(cmd, "bad.go") {
			return "", "no such file or directory", 1
		}
		return "", "unexpected", 1
	})

	tool := NewOutlineTool(container)
	input, _ := json.Marshal(outlineInput{FilePaths: []string{"good.go", "bad.go"}})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// good.go should succeed.
	if !strings.Contains(result, "=== good.go ===") {
		t.Errorf("expected good.go header, got: %q", result)
	}
	if !strings.Contains(result, "package main") {
		t.Errorf("expected good.go content, got: %q", result)
	}
	// bad.go should show an inline error.
	if !strings.Contains(result, "=== bad.go ===") {
		t.Errorf("expected bad.go header, got: %q", result)
	}
	if !strings.Contains(result, "error") {
		t.Errorf("expected error for bad.go, got: %q", result)
	}
}

func TestOutlineTool_Execute_MultipleFiles_TooMany(t *testing.T) {
	c := &ContainerClient{}
	tool := NewOutlineTool(c)

	paths := make([]string, outlineMaxFiles+1)
	for i := range paths {
		paths[i] = fmt.Sprintf("file%d.go", i)
	}
	input, _ := json.Marshal(outlineInput{FilePaths: paths})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Error("expected error for too many files")
	}
	if !strings.Contains(err.Error(), "too many files") {
		t.Errorf("expected 'too many files' in error, got: %v", err)
	}
}

func TestOutlineTool_Execute_BothInputs(t *testing.T) {
	container := newFakeContainer(t, func(cmd string) (string, string, int) {
		if strings.Contains(cmd, "a.go") {
			return "1\tpackage a\n", "", 0
		}
		if strings.Contains(cmd, "b.go") {
			return "1\tpackage b\n", "", 0
		}
		if strings.Contains(cmd, "c.go") {
			return "1\tpackage c\n", "", 0
		}
		return "", "unexpected", 1
	})

	tool := NewOutlineTool(container)
	// Both file_path and file_paths provided — should merge (a.go + b.go + c.go).
	input, _ := json.Marshal(outlineInput{FilePath: "a.go", FilePaths: []string{"b.go", "c.go"}})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !strings.Contains(result, "=== a.go ===") {
		t.Errorf("expected a.go header, got: %q", result)
	}
	if !strings.Contains(result, "=== b.go ===") {
		t.Errorf("expected b.go header, got: %q", result)
	}
	if !strings.Contains(result, "=== c.go ===") {
		t.Errorf("expected c.go header, got: %q", result)
	}
}

func TestOutlineTool_NoApproval(t *testing.T) {
	c := &ContainerClient{}
	tool := NewOutlineTool(c)
	if tool.RequiresApproval(nil) {
		t.Error("outline should not require approval")
	}
}

// --- RequiresApproval tests ---

func TestFileTools_NoApproval(t *testing.T) {
	c := &ContainerClient{}
	tools := []Tool{
		NewGlobTool(c),
		NewGrepTool(c),
		NewReadFileTool(c),
		NewEditFileTool(c),
		NewWriteFileTool(c),
	}
	for _, tool := range tools {
		if tool.RequiresApproval(nil) {
			t.Errorf("%s should not require approval", tool.Definition().Name)
		}
	}
}
