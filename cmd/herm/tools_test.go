package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// --- truncateOutput tests (head+tail strategy) ---

func TestTruncateOutput_Short(t *testing.T) {
	input := "hello\nworld\n"
	got := truncateOutput(input)
	if got != input {
		t.Errorf("short output should not be truncated, got %q", got)
	}
}

func TestTruncateOutput_Empty(t *testing.T) {
	got := truncateOutput("")
	if got != "" {
		t.Errorf("empty input should return empty, got %q", got)
	}
}

func TestTruncateOutput_ExactLineLimit(t *testing.T) {
	// Exactly bashMaxLines newlines — should not truncate.
	lines := make([]string, bashMaxLines+1) // 81 elements → 80 newlines when joined
	for i := range lines {
		lines[i] = "x"
	}
	input := strings.Join(lines, "\n")
	got := truncateOutput(input)
	if got != input {
		t.Error("exact line limit should not truncate")
	}
}

func TestTruncateOutput_OverLineLimit_HeadTail(t *testing.T) {
	// bashMaxLines + 20 lines → should trigger head+tail truncation.
	total := bashMaxLines + 20
	lines := make([]string, total)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%d", i)
	}
	input := strings.Join(lines, "\n")
	got := truncateOutput(input)

	// Should contain the omission message.
	if !strings.Contains(got, "lines omitted") {
		t.Error("truncated output should contain omission message")
	}

	// Head lines should be present.
	if !strings.Contains(got, "line-0") {
		t.Error("truncated output should contain first line (head)")
	}
	if !strings.Contains(got, fmt.Sprintf("line-%d", truncHeadLines-1)) {
		t.Error("truncated output should contain last head line")
	}

	// Tail lines should be present.
	if !strings.Contains(got, fmt.Sprintf("line-%d", total-1)) {
		t.Error("truncated output should contain last line (tail)")
	}
	if !strings.Contains(got, fmt.Sprintf("line-%d", total-truncTailLines)) {
		t.Error("truncated output should contain first tail line")
	}

	// Lines in the middle should be omitted.
	middleLine := fmt.Sprintf("line-%d", truncHeadLines+5)
	if strings.Contains(got, middleLine) {
		t.Errorf("truncated output should NOT contain middle line %q", middleLine)
	}
}

func TestTruncateOutput_ExactByteLimit(t *testing.T) {
	input := strings.Repeat("a", bashMaxBytes)
	got := truncateOutput(input)
	if got != input {
		t.Error("exact byte limit should not truncate")
	}
}

func TestTruncateOutput_OverByteLimit(t *testing.T) {
	input := strings.Repeat("a", bashMaxBytes+100)
	got := truncateOutput(input)

	if !strings.Contains(got, "truncated") {
		t.Error("byte-truncated output should contain truncation notice")
	}
}

func TestTruncateOutput_BothLimitsExceeded(t *testing.T) {
	// Create content that exceeds both byte and line limits.
	line := strings.Repeat("x", 200) + "\n"
	input := strings.Repeat(line, bashMaxLines+50)
	got := truncateOutput(input)

	if !strings.Contains(got, "omitted") || !strings.Contains(got, "truncated") {
		// Should contain either omission (line truncation) or truncation notice.
		if !strings.Contains(got, "omitted") && !strings.Contains(got, "truncated") {
			t.Error("expected truncation indicator")
		}
	}
}

func TestTruncateOutput_HeadPreserved(t *testing.T) {
	// Verify the first lines (head) are preserved.
	total := bashMaxLines + 50
	lines := make([]string, total)
	for i := range lines {
		lines[i] = fmt.Sprintf("L%04d", i)
	}
	lines[0] = "FIRST_LINE"
	lines[1] = "SECOND_LINE"
	input := strings.Join(lines, "\n")
	got := truncateOutput(input)

	if !strings.Contains(got, "FIRST_LINE") {
		t.Error("head+tail should preserve the first line")
	}
	if !strings.Contains(got, "SECOND_LINE") {
		t.Error("head+tail should preserve the second line")
	}
}

func TestTruncateOutput_TailPreserved(t *testing.T) {
	// Verify the last lines (tail) are preserved.
	total := bashMaxLines + 50
	lines := make([]string, total)
	for i := range lines {
		lines[i] = "old"
	}
	lines[total-1] = "LAST"
	lines[total-2] = "SECOND_LAST"
	input := strings.Join(lines, "\n")
	got := truncateOutput(input)

	if !strings.Contains(got, "LAST") {
		t.Error("truncated output should contain the last line")
	}
	if !strings.Contains(got, "SECOND_LAST") {
		t.Error("truncated output should contain the second-to-last line")
	}
}

func TestTruncateOutput_OmissionCount(t *testing.T) {
	total := bashMaxLines + 30
	lines := make([]string, total)
	for i := range lines {
		lines[i] = "x"
	}
	input := strings.Join(lines, "\n")
	got := truncateOutput(input)

	expected := total - truncHeadLines - truncTailLines
	expectedMsg := fmt.Sprintf("[... %d lines omitted ...]", expected)
	if !strings.Contains(got, expectedMsg) {
		t.Errorf("expected omission message %q in output, got:\n%s", expectedMsg, got)
	}
}

func TestTruncateOutput_SmallPassthrough(t *testing.T) {
	// Output well within both limits should pass through unchanged.
	input := "line1\nline2\nline3\n"
	got := truncateOutput(input)
	if got != input {
		t.Errorf("small output should pass through unchanged, got %q", got)
	}
}

// --- Task 2b: gitArgsContainForce ---

func TestGitArgsContainForce_Force(t *testing.T) {
	if !gitArgsContainForce([]string{"push", "--force"}) {
		t.Error("should detect --force")
	}
}

func TestGitArgsContainForce_ShortFlag(t *testing.T) {
	if !gitArgsContainForce([]string{"push", "-f"}) {
		t.Error("should detect -f")
	}
}

func TestGitArgsContainForce_ForceWithLease(t *testing.T) {
	if !gitArgsContainForce([]string{"push", "--force-with-lease"}) {
		t.Error("should detect --force-with-lease")
	}
}

func TestGitArgsContainForce_NoForce(t *testing.T) {
	if gitArgsContainForce([]string{"push", "origin", "main"}) {
		t.Error("should not detect force in normal args")
	}
}

func TestGitArgsContainForce_Empty(t *testing.T) {
	if gitArgsContainForce(nil) {
		t.Error("nil args should return false")
	}
	if gitArgsContainForce([]string{}) {
		t.Error("empty args should return false")
	}
}

func TestGitArgsContainForce_MixedArgs(t *testing.T) {
	if !gitArgsContainForce([]string{"origin", "main", "--force", "--set-upstream"}) {
		t.Error("should detect --force among other args")
	}
}

func TestGitArgsContainForce_SimilarButNotForce(t *testing.T) {
	// "--forceful" or "-force" should not match.
	if gitArgsContainForce([]string{"--forceful"}) {
		t.Error("--forceful should not match")
	}
}

// --- Task 2c: gitCredentialHint ---

func TestGitCredentialHint_AuthenticationFailed(t *testing.T) {
	output := "fatal: Authentication failed for 'https://github.com/user/repo.git/'"
	hint := gitCredentialHint(output)
	if hint == "" {
		t.Error("should detect authentication failure")
	}
}

func TestGitCredentialHint_PermissionDenied(t *testing.T) {
	output := "git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository."
	hint := gitCredentialHint(output)
	if hint == "" {
		t.Error("should detect permission denied")
	}
}

func TestGitCredentialHint_CouldNotReadUsername(t *testing.T) {
	output := "fatal: could not read Username for 'https://github.com': terminal prompts disabled"
	hint := gitCredentialHint(output)
	if hint == "" {
		t.Error("should detect could not read username")
	}
}

func TestGitCredentialHint_PasswordAuthRemoved(t *testing.T) {
	output := "remote: Support for password authentication was removed on August 13, 2021."
	hint := gitCredentialHint(output)
	if hint == "" {
		t.Error("should detect password auth removal message")
	}
}

func TestGitCredentialHint_HostKeyVerification(t *testing.T) {
	output := "Host key verification failed.\nfatal: Could not read from remote repository."
	hint := gitCredentialHint(output)
	if hint == "" {
		t.Error("should detect host key verification failure")
	}
}

func TestGitCredentialHint_ConnectionRefused(t *testing.T) {
	hint := gitCredentialHint("ssh: connect to host github.com port 22: Connection refused")
	if hint == "" {
		t.Error("should detect connection refused")
	}
}

func TestGitCredentialHint_ConnectionTimedOut(t *testing.T) {
	hint := gitCredentialHint("ssh: connect to host github.com port 22: Connection timed out")
	if hint == "" {
		t.Error("should detect connection timed out")
	}
}

func TestGitCredentialHint_NormalOutput(t *testing.T) {
	outputs := []string{
		"Everything up-to-date",
		"To github.com:user/repo.git\n  abc123..def456  main -> main",
		"Already up to date.",
		"fatal: not a git repository",
		"error: failed to push some refs to 'origin'",
	}
	for _, o := range outputs {
		hint := gitCredentialHint(o)
		if hint != "" {
			t.Errorf("should not trigger on normal output %q, got hint: %q", o, hint)
		}
	}
}

func TestGitCredentialHint_CaseInsensitive(t *testing.T) {
	// The patterns are compared case-insensitively via strings.ToLower.
	hint := gitCredentialHint("AUTHENTICATION FAILED for repo")
	if hint == "" {
		t.Error("should detect case-insensitive match")
	}
}

func TestGitCredentialHint_HintContent(t *testing.T) {
	hint := gitCredentialHint("Permission denied (publickey)")
	if !strings.Contains(hint, "credentials") && !strings.Contains(hint, "SSH") {
		t.Errorf("hint should mention credentials/SSH, got: %q", hint)
	}
}

// --- Task 2d: BashTool.Execute ---

func TestBashToolExecute_BasicCommand(t *testing.T) {
	orig := dockerCommand
	defer func() { dockerCommand = orig }()

	dockerCommand = fakeDockerCommand(func(args []string) (string, string, int) {
		if len(args) >= 2 {
			switch args[1] {
			case "run":
				return "cid123\n", "", 0
			case "exec":
				return "hello world\n", "", 0
			case "stop", "rm":
				return "", "", 0
			}
		}
		return "", "", 1
	})

	container := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	_ = container.Start("/workspace", nil)
	defer container.Stop()

	tool := NewBashTool(container, 120)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"echo hello world"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(result, "hello world") {
		t.Errorf("result = %q, want to contain 'hello world'", result)
	}
}

func TestBashToolExecute_TruncatesOutput(t *testing.T) {
	orig := dockerCommand
	defer func() { dockerCommand = orig }()

	// Return output larger than bashMaxBytes.
	bigOutput := strings.Repeat("x", bashMaxBytes+500)
	dockerCommand = fakeDockerCommand(func(args []string) (string, string, int) {
		if len(args) >= 2 {
			switch args[1] {
			case "run":
				return "cid123\n", "", 0
			case "exec":
				return bigOutput, "", 0
			case "stop", "rm":
				return "", "", 0
			}
		}
		return "", "", 1
	})

	container := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	_ = container.Start("/workspace", nil)
	defer container.Stop()

	tool := NewBashTool(container, 120)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"cat bigfile"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(result, "truncated") {
		t.Error("large output should be truncated")
	}
}

func TestBashToolExecute_NonZeroExitCode(t *testing.T) {
	orig := dockerCommand
	defer func() { dockerCommand = orig }()

	dockerCommand = fakeDockerCommand(func(args []string) (string, string, int) {
		if len(args) >= 2 {
			switch args[1] {
			case "run":
				return "cid123\n", "", 0
			case "exec":
				return "", "not found", 1
			case "stop", "rm":
				return "", "", 0
			}
		}
		return "", "", 1
	})

	container := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	_ = container.Start("/workspace", nil)
	defer container.Stop()

	tool := NewBashTool(container, 120)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"ls /nonexistent"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(result, "exit code: 1") {
		t.Errorf("result = %q, want to contain 'exit code: 1'", result)
	}
}

func TestBashToolExecute_EmptyCommand(t *testing.T) {
	tool := NewBashTool(nil, 120)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"command":""}`))
	if err == nil {
		t.Fatal("expected error for empty command")
	}
	if !strings.Contains(err.Error(), "command is required") {
		t.Errorf("error = %q, want 'command is required'", err.Error())
	}
}

func TestBashToolExecute_InvalidJSON(t *testing.T) {
	tool := NewBashTool(nil, 120)
	_, err := tool.Execute(context.Background(), json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestBashToolExecute_ContainerNotRunning(t *testing.T) {
	container := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	tool := NewBashTool(container, 120)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"echo hi"}`))
	if err == nil {
		t.Fatal("expected error when container not running")
	}
}

func TestBashToolExecute_CustomTimeout(t *testing.T) {
	orig := dockerCommand
	defer func() { dockerCommand = orig }()

	dockerCommand = fakeDockerCommand(func(args []string) (string, string, int) {
		if len(args) >= 2 {
			switch args[1] {
			case "run":
				return "cid123\n", "", 0
			case "exec":
				return "ok", "", 0
			case "stop", "rm":
				return "", "", 0
			}
		}
		return "", "", 1
	})

	container := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	_ = container.Start("/workspace", nil)
	defer container.Stop()

	tool := NewBashTool(container, 120)
	// Custom timeout in input should override default.
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"sleep 1","timeout":300}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result != "ok" {
		t.Errorf("result = %q, want 'ok'", result)
	}
}

func TestBashToolExecute_HTMLUnescape(t *testing.T) {
	orig := dockerCommand
	defer func() { dockerCommand = orig }()

	var capturedCmd string
	dockerCommand = fakeDockerCommand(func(args []string) (string, string, int) {
		if len(args) >= 2 {
			switch args[1] {
			case "run":
				return "cid123\n", "", 0
			case "exec":
				// Capture the command that was passed to exec.
				// args: docker exec -w <workDir> cid123 sh -c <command>
				if len(args) >= 8 {
					capturedCmd = args[7]
				}
				return "ok", "", 0
			case "stop", "rm":
				return "", "", 0
			}
		}
		return "", "", 1
	})

	container := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	_ = container.Start("/workspace", nil)
	defer container.Stop()

	tool := NewBashTool(container, 120)
	// HTML-encoded && should be unescaped before execution.
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"echo a &amp;&amp; echo b"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if capturedCmd != "echo a && echo b" {
		t.Errorf("command not unescaped: got %q, want %q", capturedCmd, "echo a && echo b")
	}
}

func TestBashToolNoApproval(t *testing.T) {
	tool := NewBashTool(nil, 120)
	if tool.RequiresApproval(json.RawMessage(`{"command":"rm -rf /"}`)) {
		t.Error("bash tool should never require approval")
	}
}

func TestBashToolDefaultTimeout(t *testing.T) {
	tool := NewBashTool(nil, 0)
	if tool.timeout != 120 {
		t.Errorf("default timeout = %d, want 120", tool.timeout)
	}
	tool2 := NewBashTool(nil, -10)
	if tool2.timeout != 120 {
		t.Errorf("negative timeout should default to 120, got %d", tool2.timeout)
	}
}

// --- Task 2e: GitTool.Execute and RequiresApproval ---

func TestGitToolExecute_AllowedSubcommand(t *testing.T) {
	// Use a real git repo dir for the test.
	tmp := t.TempDir()

	// Initialize a git repo.
	initCmd := newGitCmd(tmp, "init")
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	tool := NewGitTool(tmp, false)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"subcommand":"status"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	// git status in a fresh repo should contain something about branch.
	if result == "" {
		t.Error("expected non-empty output from git status")
	}
}

func TestGitToolExecute_DisallowedSubcommand(t *testing.T) {
	tool := NewGitTool(t.TempDir(), false)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"subcommand":"bisect"}`))
	if err == nil {
		t.Fatal("expected error for disallowed subcommand")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("error = %q, want to contain 'not allowed'", err.Error())
	}
}

func TestGitToolExecute_EmptySubcommand(t *testing.T) {
	tool := NewGitTool(t.TempDir(), false)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"subcommand":""}`))
	if err == nil {
		t.Fatal("expected error for empty subcommand")
	}
	if !strings.Contains(err.Error(), "subcommand is required") {
		t.Errorf("error = %q, want 'subcommand is required'", err.Error())
	}
}

func TestGitToolExecute_InvalidJSON(t *testing.T) {
	tool := NewGitTool(t.TempDir(), false)
	_, err := tool.Execute(context.Background(), json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestGitToolExecute_CoAuthorOnCommit(t *testing.T) {
	tmp := t.TempDir()

	// Set up a minimal git repo with a file to commit.
	for _, cmd := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		c := newGitCmd(tmp, cmd...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", cmd, err, out)
		}
	}
	// Create and stage a file.
	if err := os.WriteFile(filepath.Join(tmp, "test.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	stageCmd := newGitCmd(tmp, "add", "test.txt")
	if out, err := stageCmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	tool := NewGitTool(tmp, true) // coAuthor enabled
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"subcommand":"commit","args":["-m","test commit"]}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty commit output")
	}

	// Verify the commit has the co-author trailer.
	logCmd := newGitCmd(tmp, "log", "-1", "--format=%B")
	logOut, err := logCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, logOut)
	}
	if !strings.Contains(string(logOut), "Co-authored-by: herm") {
		t.Errorf("commit message should contain co-author trailer, got:\n%s", logOut)
	}
}

func TestGitToolExecute_NoCoAuthorWithoutFlag(t *testing.T) {
	tmp := t.TempDir()

	for _, cmd := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		c := newGitCmd(tmp, cmd...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", cmd, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, "test.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	stageCmd := newGitCmd(tmp, "add", "test.txt")
	if out, err := stageCmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	tool := NewGitTool(tmp, false) // coAuthor disabled
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"subcommand":"commit","args":["-m","test commit"]}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	logCmd := newGitCmd(tmp, "log", "-1", "--format=%B")
	logOut, err := logCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, logOut)
	}
	if strings.Contains(string(logOut), "Co-authored-by") {
		t.Errorf("commit should NOT contain co-author trailer when disabled, got:\n%s", logOut)
	}
}

func TestGitToolExecute_GitError(t *testing.T) {
	tmp := t.TempDir()
	// Not a git repo → git status should fail.
	tool := NewGitTool(tmp, false)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"subcommand":"status"}`))
	if err != nil {
		// The implementation returns (result, nil) for ExitError, only err for other errors.
		// In this case it might be an ExitError.
		t.Logf("got error: %v", err)
		return
	}
	if !strings.Contains(result, "exit code") {
		t.Errorf("expected exit code in result for failed git command, got: %q", result)
	}
}

func TestGitToolRequiresApproval_Push(t *testing.T) {
	tool := NewGitTool("", false)
	if !tool.RequiresApproval(json.RawMessage(`{"subcommand":"push"}`)) {
		t.Error("push should require approval")
	}
}

func TestGitToolRequiresApproval_PushWithArgs(t *testing.T) {
	tool := NewGitTool("", false)
	if !tool.RequiresApproval(json.RawMessage(`{"subcommand":"push","args":["origin","main"]}`)) {
		t.Error("push with args should require approval")
	}
}

func TestGitToolRequiresApproval_ForcePush(t *testing.T) {
	tool := NewGitTool("", false)
	if !tool.RequiresApproval(json.RawMessage(`{"subcommand":"push","args":["--force"]}`)) {
		t.Error("force push should require approval")
	}
}

func TestGitToolRequiresApproval_ResetHard(t *testing.T) {
	tool := NewGitTool("", false)
	if !tool.RequiresApproval(json.RawMessage(`{"subcommand":"reset","args":["--hard","HEAD~1"]}`)) {
		t.Error("reset --hard should require approval")
	}
}

func TestGitToolRequiresApproval_ResetSoft(t *testing.T) {
	tool := NewGitTool("", false)
	if tool.RequiresApproval(json.RawMessage(`{"subcommand":"reset","args":["--soft","HEAD~1"]}`)) {
		t.Error("reset --soft should not require approval")
	}
}

func TestGitToolRequiresApproval_SafeCommands(t *testing.T) {
	tool := NewGitTool("", false)
	safe := []string{
		`{"subcommand":"status"}`,
		`{"subcommand":"diff"}`,
		`{"subcommand":"log"}`,
		`{"subcommand":"add","args":["."]}`,
		`{"subcommand":"commit","args":["-m","fix"]}`,
		`{"subcommand":"branch","args":["feature"]}`,
		`{"subcommand":"checkout","args":["main"]}`,
	}
	for _, input := range safe {
		if tool.RequiresApproval(json.RawMessage(input)) {
			t.Errorf("should not require approval for: %s", input)
		}
	}
}

func TestGitToolRequiresApproval_InvalidJSON(t *testing.T) {
	tool := NewGitTool("", false)
	// Invalid JSON should return false (fail open on parse error).
	if tool.RequiresApproval(json.RawMessage(`not json`)) {
		t.Error("invalid JSON should return false")
	}
}

func TestGitToolRequiresApproval_ForceFlag(t *testing.T) {
	tool := NewGitTool("", false)
	// Any subcommand with --force should require approval.
	if !tool.RequiresApproval(json.RawMessage(`{"subcommand":"rebase","args":["--force"]}`)) {
		t.Error("rebase --force should require approval")
	}
}

// newGitCmd creates a git exec.Cmd in the given directory.
func newGitCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd
}
