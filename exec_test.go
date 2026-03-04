package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// mockContainerClient creates a ContainerClient backed by fakeDockerCommand
// that returns the given stdout/stderr/exitCode for exec calls.
func mockContainerClient(t *testing.T, stdout, stderr string, exitCode int) *ContainerClient {
	t.Helper()
	orig := dockerCommand
	t.Cleanup(func() { dockerCommand = orig })

	dockerCommand = fakeDockerCommand(func(args []string) (string, string, int) {
		if len(args) >= 2 {
			switch args[1] {
			case "run":
				return "mock-container-id\n", "", 0
			case "exec":
				return stdout, stderr, exitCode
			case "stop", "rm":
				return "", "", 0
			}
		}
		return "", "unknown", 1
	})

	c := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	if err := c.Start("/workspace", []MountSpec{{
		Source:      "/workspace",
		Destination: "/workspace",
	}}); err != nil {
		t.Fatalf("mockContainerClient Start: %v", err)
	}
	return c
}

// modelWithContainer creates a ready model with a mock container.
func modelWithContainer(t *testing.T, stdout, stderr string, exitCode int) model {
	t.Helper()
	client := mockContainerClient(t, stdout, stderr, exitCode)
	m := initialModel()
	m = resize(m, 80, 24)
	m.container = client
	m.containerReady = true
	m.worktreePath = "/tmp/test-worktree"
	return m
}

// enterShell enters /container-shell mode on a model with a ready container.
func enterShell(t *testing.T, m model) model {
	t.Helper()
	m = typeString(m, "/container-shell")
	m = sendKey(m, tea.KeyEnter)
	if m.mode != modeShell {
		t.Fatalf("mode = %d, want modeShell", m.mode)
	}
	return m
}

func TestShellModeEchoHello(t *testing.T) {
	m := modelWithContainer(t, "hello\n", "", 0)
	m = enterShell(t, m)

	// Type command in shell mode and send
	m = typeString(m, "echo hello")
	result, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = result.(model)

	// Should show the command being run
	found := false
	for _, msg := range m.messages {
		if msg.content == "$ echo hello" {
			found = true
			break
		}
	}
	if !found {
		t.Error("should show '$ echo hello' message")
	}

	// Should still be in shell mode
	if m.mode != modeShell {
		t.Errorf("mode = %d, want modeShell (should stay in shell mode)", m.mode)
	}

	// Execute the async cmd to get the result
	if cmd == nil {
		t.Fatal("expected non-nil cmd for async exec")
	}
	msg := cmd()

	// Feed the result back
	result, _ = m.Update(msg)
	m = result.(model)

	// Should have the output message
	foundOutput := false
	for _, msg := range m.messages {
		if msg.kind == msgSuccess && strings.Contains(msg.content, "hello") {
			foundOutput = true
			break
		}
	}
	if !foundOutput {
		t.Errorf("should show exec output 'hello', messages: %+v", m.messages)
	}

	// Should still be in shell mode after result
	if m.mode != modeShell {
		t.Errorf("mode = %d, want modeShell after exec result", m.mode)
	}
}

func TestShellModeNonZeroExit(t *testing.T) {
	m := modelWithContainer(t, "", "file not found\n", 1)
	m = enterShell(t, m)

	m = typeString(m, "cat missing.txt")
	result, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = result.(model)

	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}
	msg := cmd()

	result, _ = m.Update(msg)
	m = result.(model)

	// Should show error-style output with exit code
	foundError := false
	for _, msg := range m.messages {
		if msg.kind == msgError && strings.Contains(msg.content, "exit 1") {
			foundError = true
			break
		}
	}
	if !foundError {
		t.Errorf("should show error with exit code, messages: %+v", m.messages)
	}

	// Should still be in shell mode
	if m.mode != modeShell {
		t.Errorf("mode = %d, want modeShell after error result", m.mode)
	}
}

func TestShellModeContainerNotReady(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)
	// containerReady is false by default

	m = typeString(m, "/container-shell")
	result, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = result.(model)

	// Should show info message about container starting
	foundInfo := false
	for _, msg := range m.messages {
		if msg.kind == msgInfo && strings.Contains(msg.content, "starting") {
			foundInfo = true
			break
		}
	}
	if !foundInfo {
		t.Errorf("should show container starting message, messages: %+v", m.messages)
	}

	// Should stay in chat mode
	if m.mode != modeChat {
		t.Errorf("mode = %d, want modeChat when container not ready", m.mode)
	}

	// No async cmd should be returned
	if cmd != nil {
		t.Error("should not return cmd when container not ready")
	}
}

func TestShellModeContainerError(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	// Simulate container error from startup
	result, _ := m.Update(containerErrMsg{err: &ContainerError{Code: ErrDockerNotFound, Message: "not found"}})
	m = result.(model)

	m = typeString(m, "/container-shell")
	result, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = result.(model)

	// Should show container error
	foundErr := false
	for _, msg := range m.messages {
		if msg.kind == msgError && strings.Contains(msg.content, "not found") {
			foundErr = true
			break
		}
	}
	if !foundErr {
		t.Errorf("should show container error, messages: %+v", m.messages)
	}

	// Should stay in chat mode
	if m.mode != modeChat {
		t.Errorf("mode = %d, want modeChat when container has error", m.mode)
	}

	if cmd != nil {
		t.Error("should not return cmd when container has error")
	}
}

func TestShellModeCtrlCExits(t *testing.T) {
	m := modelWithContainer(t, "", "", 0)
	m = enterShell(t, m)

	// Ctrl+C should exit shell mode
	result, _ := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = result.(model)

	if m.mode != modeChat {
		t.Errorf("mode = %d, want modeChat after Ctrl+C", m.mode)
	}

	// Should show exit message
	foundExit := false
	for _, msg := range m.messages {
		if msg.kind == msgInfo && strings.Contains(msg.content, "Exited") {
			foundExit = true
			break
		}
	}
	if !foundExit {
		t.Error("should show exit message after Ctrl+C")
	}

	// Placeholder should be restored
	if m.textarea.Placeholder != "Type a message..." {
		t.Errorf("placeholder = %q, want 'Type a message...'", m.textarea.Placeholder)
	}
}

func TestShellModeAutocomplete(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	m = typeString(m, "/co")
	matches := m.autocompleteMatches()
	found := false
	for _, match := range matches {
		if match == "/container-shell" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("autocompleteMatches = %v, should contain /container-shell", matches)
	}
}

func TestShellModePlaceholderChanges(t *testing.T) {
	m := modelWithContainer(t, "", "", 0)
	m = enterShell(t, m)

	if m.textarea.Placeholder != "container $" {
		t.Errorf("placeholder = %q, want 'container $'", m.textarea.Placeholder)
	}
}

func TestShellModeStatusBarShowsShell(t *testing.T) {
	m := modelWithContainer(t, "", "", 0)
	m.status = statusInfo{Branch: "main", WorktreeName: "test-wt"}
	m = enterShell(t, m)

	bar := m.renderStatusBar()
	if !strings.Contains(bar, "SHELL") {
		t.Errorf("status bar should contain 'SHELL', got %q", bar)
	}
}

func TestShellModeStaysAfterExecResult(t *testing.T) {
	m := modelWithContainer(t, "output\n", "", 0)
	m = enterShell(t, m)

	// Send a command
	m = typeString(m, "ls")
	result, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = result.(model)

	if m.mode != modeShell {
		t.Fatalf("mode = %d, want modeShell before exec result", m.mode)
	}

	// Process exec result
	if cmd != nil {
		msg := cmd()
		result, _ = m.Update(msg)
		m = result.(model)
	}

	// Should still be in shell mode after result
	if m.mode != modeShell {
		t.Errorf("mode = %d, want modeShell after exec result", m.mode)
	}
}

func TestShellModeEmptyCommandIgnored(t *testing.T) {
	m := modelWithContainer(t, "", "", 0)
	m = enterShell(t, m)

	msgCount := len(m.messages)

	// Press Enter with empty input
	result, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = result.(model)

	// Should not add any messages
	if len(m.messages) != msgCount {
		t.Errorf("empty Enter should not add messages, got %d want %d", len(m.messages), msgCount)
	}
	if cmd != nil {
		t.Error("empty Enter should not return a cmd")
	}
}

func TestShutdownCleanup(t *testing.T) {
	m := modelWithContainer(t, "", "", 0)

	// Verify container is running
	if !m.containerReady {
		t.Fatal("container should be ready")
	}

	// Simulate ctrl+c — calls cleanup() then tea.Quit
	m.cleanup()

	// Container should be stopped
	if m.container.running {
		t.Error("container should be stopped after cleanup")
	}
}

func TestContainerReadyMsg(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	if m.containerReady {
		t.Fatal("should not be ready initially")
	}

	client := &ContainerClient{
		config:  ContainerConfig{Image: "alpine:latest"},
		running: true,
	}

	result, _ := m.Update(containerReadyMsg{client: client, worktreePath: "/tmp/test-wt"})
	m = result.(model)

	if !m.containerReady {
		t.Error("should be ready after containerReadyMsg")
	}
	if m.container != client {
		t.Error("container should be set")
	}
	if m.worktreePath != "/tmp/test-wt" {
		t.Errorf("worktreePath = %q, want /tmp/test-wt", m.worktreePath)
	}
}
