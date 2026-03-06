package main

import (
	"strings"
	"testing"
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

// appWithContainer creates a ready App with a mock container.
func appWithContainer(t *testing.T, stdout, stderr string, exitCode int) *App {
	t.Helper()
	client := mockContainerClient(t, stdout, stderr, exitCode)
	a := newTestApp(80, 24)
	a.container = client
	a.containerReady = true
	a.worktreePath = "/tmp/test-worktree"
	return a
}

func TestShellContainerNotReady(t *testing.T) {
	a := newTestApp(80, 24)
	// containerReady is false by default

	simType(a, "/shell")
	simKey(a, KeyEnter)

	// Should show info message about container starting
	foundInfo := false
	for _, msg := range a.messages {
		if msg.kind == msgInfo && strings.Contains(msg.content, "starting") {
			foundInfo = true
			break
		}
	}
	if !foundInfo {
		t.Errorf("should show container starting message, messages: %+v", a.messages)
	}

	// Should stay in chat mode
	if a.mode != modeChat {
		t.Errorf("mode = %d, want modeChat when container not ready", a.mode)
	}
}

func TestShellContainerError(t *testing.T) {
	a := newTestApp(80, 24)

	// Simulate container error from startup
	simResult(a, containerErrMsg{err: &ContainerError{Code: ErrDockerNotFound, Message: "not found"}})

	simType(a, "/shell")
	simKey(a, KeyEnter)

	// Should show container error
	foundErr := false
	for _, msg := range a.messages {
		if msg.kind == msgError && strings.Contains(msg.content, "not found") {
			foundErr = true
			break
		}
	}
	if !foundErr {
		t.Errorf("should show container error, messages: %+v", a.messages)
	}

	// Should stay in chat mode
	if a.mode != modeChat {
		t.Errorf("mode = %d, want modeChat when container has error", a.mode)
	}
}

func TestShellAutocomplete(t *testing.T) {
	a := newTestApp(80, 24)

	simType(a, "/sh")
	matches := a.autocompleteMatches()
	found := false
	for _, match := range matches {
		if match == "/shell" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("autocompleteMatches = %v, should contain /shell", matches)
	}
}

func TestContainerIDGetter(t *testing.T) {
	orig := dockerCommand
	t.Cleanup(func() { dockerCommand = orig })

	dockerCommand = fakeDockerCommand(func(args []string) (string, string, int) {
		if len(args) >= 2 && args[1] == "run" {
			return "test-container-123\n", "", 0
		}
		return "", "", 0
	})

	c := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	if err := c.Start("/workspace", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if got := c.ContainerID(); got != "test-container-123" {
		t.Errorf("ContainerID() = %q, want %q", got, "test-container-123")
	}
}

func TestShutdownCleanup(t *testing.T) {
	a := appWithContainer(t, "", "", 0)

	// Verify container is running
	if !a.containerReady {
		t.Fatal("container should be ready")
	}

	a.cleanup()

	// Container should be stopped
	if a.container.running {
		t.Error("container should be stopped after cleanup")
	}
}

func TestContainerReadyMsg(t *testing.T) {
	a := newTestApp(80, 24)

	if a.containerReady {
		t.Fatal("should not be ready initially")
	}

	client := &ContainerClient{
		config:  ContainerConfig{Image: "alpine:latest"},
		running: true,
	}

	simResult(a, containerReadyMsg{client: client, worktreePath: "/tmp/test-wt"})

	if !a.containerReady {
		t.Error("should be ready after containerReadyMsg")
	}
	if a.container != client {
		t.Error("container should be set")
	}
	if a.worktreePath != "/tmp/test-wt" {
		t.Errorf("worktreePath = %q, want /tmp/test-wt", a.worktreePath)
	}
}
