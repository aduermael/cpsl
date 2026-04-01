package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDockerCommand returns a function that replaces dockerCommand in tests.
// It maps (command, args...) to a predefined result via the handler.
// The handler receives the full arg list (e.g. ["docker", "info"]) and returns
// (stdout, stderr, exitCode).
func fakeDockerCommand(handler func(args []string) (string, string, int)) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		fullArgs := append([]string{name}, args...)
		stdout, stderr, exitCode := handler(fullArgs)

		// Use a helper process pattern: run "go test" with a special env var
		// that makes the test binary act as the fake command.
		cs := []string{"-test.run=TestHelperProcess", "--"}
		cs = append(cs, fullArgs...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(os.Environ(),
			"GO_WANT_HELPER_PROCESS=1",
			fmt.Sprintf("FAKE_STDOUT=%s", stdout),
			fmt.Sprintf("FAKE_STDERR=%s", stderr),
			fmt.Sprintf("FAKE_EXIT_CODE=%d", exitCode),
		)
		return cmd
	}
}

// TestHelperProcess is used by fakeDockerCommand to simulate external commands.
// It is not a real test.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	// Capture stdin to file when requested (used by ExecWithStdin tests).
	// Only read stdin for "exec -i" calls to avoid blocking other commands.
	if f := os.Getenv("FAKE_STDIN_FILE"); f != "" {
		cmdLine := strings.Join(os.Args, " ")
		if strings.Contains(cmdLine, "exec") && strings.Contains(cmdLine, "-i") {
			data, _ := io.ReadAll(os.Stdin)
			_ = os.WriteFile(f, data, 0644)
		}
	}
	fmt.Fprint(os.Stdout, os.Getenv("FAKE_STDOUT"))
	fmt.Fprint(os.Stderr, os.Getenv("FAKE_STDERR"))
	code := 0
	fmt.Sscanf(os.Getenv("FAKE_EXIT_CODE"), "%d", &code)
	os.Exit(code)
}

// fakeDockerCommandWithStdin is like fakeDockerCommand but sets FAKE_STDIN_FILE
// so that TestHelperProcess captures stdin bytes for verification.
func fakeDockerCommandWithStdin(handler func(args []string) (string, string, int), stdinFile string) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		fullArgs := append([]string{name}, args...)
		stdout, stderr, exitCode := handler(fullArgs)

		cs := []string{"-test.run=TestHelperProcess", "--"}
		cs = append(cs, fullArgs...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		env := append(os.Environ(),
			"GO_WANT_HELPER_PROCESS=1",
			fmt.Sprintf("FAKE_STDOUT=%s", stdout),
			fmt.Sprintf("FAKE_STDERR=%s", stderr),
			fmt.Sprintf("FAKE_EXIT_CODE=%d", exitCode),
		)
		if stdinFile != "" {
			env = append(env, fmt.Sprintf("FAKE_STDIN_FILE=%s", stdinFile))
		}
		cmd.Env = env
		return cmd
	}
}

func TestContainerClient_CheckDocker_OK(t *testing.T) {
	origCmd := dockerCommand
	origPath := lookPath
	defer func() { dockerCommand = origCmd; lookPath = origPath }()

	lookPath = func(file string) (string, error) { return "/usr/bin/docker", nil }
	dockerCommand = fakeDockerCommand(func(args []string) (string, string, int) {
		if len(args) >= 2 && args[1] == "info" {
			return "", "", 0
		}
		return "", "unknown command", 1
	})

	c := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	if err := c.CheckDocker(); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestContainerClient_CheckDocker_NotInstalled(t *testing.T) {
	origPath := lookPath
	defer func() { lookPath = origPath }()

	lookPath = func(file string) (string, error) {
		return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
	}

	c := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	err := c.CheckDocker()
	if err == nil {
		t.Fatal("expected error when docker is not installed")
	}
	cerr, ok := err.(*ContainerError)
	if !ok {
		t.Fatalf("expected *ContainerError, got %T", err)
	}
	if cerr.Code != ErrDockerNotFound {
		t.Errorf("expected code %s, got %s", ErrDockerNotFound, cerr.Code)
	}
}

func TestContainerClient_CheckDocker_DaemonNotRunning(t *testing.T) {
	origCmd := dockerCommand
	origPath := lookPath
	defer func() { dockerCommand = origCmd; lookPath = origPath }()

	lookPath = func(file string) (string, error) { return "/usr/bin/docker", nil }
	dockerCommand = fakeDockerCommand(func(args []string) (string, string, int) {
		return "", "Cannot connect to the Docker daemon", 1
	})

	c := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	err := c.CheckDocker()
	if err == nil {
		t.Fatal("expected error when daemon is not running")
	}
	cerr, ok := err.(*ContainerError)
	if !ok {
		t.Fatalf("expected *ContainerError, got %T", err)
	}
	if cerr.Code != ErrDockerNotRunning {
		t.Errorf("expected code %s, got %s", ErrDockerNotRunning, cerr.Code)
	}
}

func TestContainerClient_ExecNotRunning(t *testing.T) {
	c := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	_, err := c.Exec("echo hello", 120)
	if err == nil {
		t.Fatal("expected error")
	}
	cerr, ok := err.(*ContainerError)
	if !ok {
		t.Fatalf("expected ContainerError, got %T", err)
	}
	if cerr.Code != ErrNotRunning {
		t.Errorf("expected code %s, got %s", ErrNotRunning, cerr.Code)
	}
}

func TestContainerClient_StopIdempotent(t *testing.T) {
	c := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	// Stop on a non-running client should be a no-op.
	if err := c.Stop(); err != nil {
		t.Fatalf("stop on non-running: %v", err)
	}
}

func TestContainerError_Format(t *testing.T) {
	err := &ContainerError{Code: ErrDockerNotFound, Message: "not found"}
	if err.Error() != "not found" {
		t.Errorf("expected %q, got %q", "not found", err.Error())
	}
}

func TestContainerClient_StartAndExec(t *testing.T) {
	orig := dockerCommand
	defer func() { dockerCommand = orig }()

	dockerCommand = fakeDockerCommand(func(args []string) (string, string, int) {
		if len(args) >= 2 {
			switch args[1] {
			case "run":
				return "abc123def456\n", "", 0
			case "exec":
				return "hello\n", "", 0
			case "stop":
				return "", "", 0
			case "rm":
				return "", "", 0
			}
		}
		return "", "unknown", 1
	})

	c := NewContainerClient(ContainerConfig{Image: "alpine:latest"})

	err := c.Start("/workspace", []MountSpec{{
		Source:      "/workspace",
		Destination: "/workspace",
		ReadOnly:    false,
	}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !c.running {
		t.Error("expected running to be true after Start")
	}
	if c.containerID != "abc123def456" {
		t.Errorf("containerID = %q, want %q", c.containerID, "abc123def456")
	}

	// Exec.
	result, err := c.Exec("echo hello", 120)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.Stdout != "hello\n" {
		t.Errorf("stdout = %q, want %q", result.Stdout, "hello\n")
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}

	// Stop.
	if err := c.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if c.running {
		t.Error("expected running to be false after Stop")
	}
}

func TestContainerClient_StartAlreadyRunning(t *testing.T) {
	orig := dockerCommand
	defer func() { dockerCommand = orig }()

	dockerCommand = fakeDockerCommand(func(args []string) (string, string, int) {
		if len(args) >= 2 && args[1] == "run" {
			return "abc123\n", "", 0
		}
		return "", "", 0
	})

	c := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	if err := c.Start("/workspace", nil); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	err := c.Start("/workspace", nil)
	if err == nil {
		t.Fatal("expected error on second Start")
	}
	cerr, ok := err.(*ContainerError)
	if !ok {
		t.Fatalf("expected ContainerError, got %T", err)
	}
	if cerr.Code != ErrStartFailed {
		t.Errorf("expected code %s, got %s", ErrStartFailed, cerr.Code)
	}
}

func TestContainerClient_StatusNotRunning(t *testing.T) {
	c := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	_, err := c.Status()
	if err == nil {
		t.Fatal("expected error")
	}
	cerr, ok := err.(*ContainerError)
	if !ok {
		t.Fatalf("expected ContainerError, got %T", err)
	}
	if cerr.Code != ErrNotRunning {
		t.Errorf("expected code %s, got %s", ErrNotRunning, cerr.Code)
	}
}

func TestContainerClient_Status(t *testing.T) {
	orig := dockerCommand
	defer func() { dockerCommand = orig }()

	dockerCommand = fakeDockerCommand(func(args []string) (string, string, int) {
		if len(args) >= 2 {
			switch args[1] {
			case "run":
				return "abc123\n", "", 0
			case "inspect":
				return "running\n", "", 0
			case "stop", "rm":
				return "", "", 0
			}
		}
		return "", "", 1
	})

	c := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	if err := c.Start("/workspace", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}

	status, err := c.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != "running" {
		t.Errorf("state = %q, want %q", status.State, "running")
	}

	_ = c.Stop()
}

func TestContainerClient_Rebuild(t *testing.T) {
	orig := dockerCommand
	defer func() { dockerCommand = orig }()

	var calledBuild, calledRmOld, calledRunNew bool
	const oldID = "oldcontainer123"
	const newID = "newcontainer456"

	dockerCommand = fakeDockerCommand(func(args []string) (string, string, int) {
		if len(args) >= 2 {
			switch args[1] {
			case "run":
				if !calledBuild {
					// First run call: initial Start before Rebuild.
					return oldID + "\n", "", 0
				}
				// Second run call: Start inside Rebuild.
				calledRunNew = true
				return newID + "\n", "", 0
			case "build":
				calledBuild = true
				return "", "", 0
			case "rm":
				// docker rm -f <id>
				if len(args) >= 4 && args[2] == "-f" && args[3] == oldID {
					calledRmOld = true
				}
				return "", "", 0
			}
		}
		return "", "unknown", 1
	})

	c := NewContainerClient(ContainerConfig{Image: "alpine:latest"})

	// Start the client so it is already running with oldID.
	if err := c.Start("/workspace", nil); err != nil {
		t.Fatalf("initial Start: %v", err)
	}
	if c.containerID != oldID {
		t.Fatalf("pre-condition: containerID = %q, want %q", c.containerID, oldID)
	}

	mounts := []MountSpec{{Source: "/workspace", Destination: "/workspace"}}
	err := c.Rebuild("myimage:latest", "/workspace/Dockerfile", "/workspace", mounts)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// docker build must have been called with the right args.
	if !calledBuild {
		t.Error("expected docker build to be called")
	}

	// Old container must have been stopped.
	if !calledRmOld {
		t.Errorf("expected docker rm -f %s to be called", oldID)
	}

	// A new container must have been started.
	if !calledRunNew {
		t.Error("expected docker run to be called for new container")
	}

	// Config image must be updated.
	c.mu.Lock()
	gotImage := c.config.Image
	c.mu.Unlock()
	if gotImage != "myimage:latest" {
		t.Errorf("config.Image = %q, want %q", gotImage, "myimage:latest")
	}

	// Client must be running with the new ID.
	if !c.running {
		t.Error("expected client to be running after Rebuild")
	}
	if c.containerID != newID {
		t.Errorf("containerID = %q, want %q", c.containerID, newID)
	}
}

func TestContainerClient_RebuildBuildFailure(t *testing.T) {
	orig := dockerCommand
	defer func() { dockerCommand = orig }()

	var calledRm bool
	const startID = "running789"

	dockerCommand = fakeDockerCommand(func(args []string) (string, string, int) {
		if len(args) >= 2 {
			switch args[1] {
			case "run":
				return startID + "\n", "", 0
			case "build":
				return "", "error: cmd failed: sh -c &amp;&amp; false", 1
			case "rm":
				calledRm = true
				return "", "", 0
			}
		}
		return "", "unknown", 1
	})

	c := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	if err := c.Start("/workspace", nil); err != nil {
		t.Fatalf("initial Start: %v", err)
	}

	err := c.Rebuild("myimage:latest", "/workspace/Dockerfile", "/workspace", nil)
	if err == nil {
		t.Fatal("expected error from Rebuild when build fails")
	}

	// Must return a ContainerError with ErrStartFailed.
	cerr, ok := err.(*ContainerError)
	if !ok {
		t.Fatalf("expected *ContainerError, got %T", err)
	}
	if cerr.Code != ErrStartFailed {
		t.Errorf("error code = %q, want %q", cerr.Code, ErrStartFailed)
	}

	// Error message must include the stderr output with HTML entities unescaped.
	if !strings.Contains(cerr.Message, "&&") {
		t.Errorf("expected HTML-unescaped '&&' in error message, got: %s", cerr.Message)
	}

	// Original container must NOT have been stopped.
	if calledRm {
		t.Error("expected docker rm to NOT be called when build fails")
	}
	if !c.running {
		t.Error("expected client to still be running after build failure")
	}
	if c.containerID != startID {
		t.Errorf("containerID = %q, want %q (original)", c.containerID, startID)
	}
}

func TestContainerClient_RebuildNotRunning(t *testing.T) {
	orig := dockerCommand
	defer func() { dockerCommand = orig }()

	var calledRm bool
	const newID = "freshcontainer"

	dockerCommand = fakeDockerCommand(func(args []string) (string, string, int) {
		if len(args) >= 2 {
			switch args[1] {
			case "build":
				return "", "", 0
			case "run":
				return newID + "\n", "", 0
			case "rm":
				calledRm = true
				return "", "", 0
			}
		}
		return "", "unknown", 1
	})

	// Do NOT call Start — client starts not running.
	c := NewContainerClient(ContainerConfig{Image: "alpine:latest"})

	mounts := []MountSpec{{Source: "/workspace", Destination: "/workspace"}}
	err := c.Rebuild("myimage:latest", "/workspace/Dockerfile", "/workspace", mounts)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// No rm -f should have been issued.
	if calledRm {
		t.Error("expected docker rm to NOT be called when client was not running")
	}

	// Client must be running with the new container ID.
	if !c.running {
		t.Error("expected client to be running after Rebuild")
	}
	if c.containerID != newID {
		t.Errorf("containerID = %q, want %q", c.containerID, newID)
	}
}

func TestContainerClient_ExecWithStdin(t *testing.T) {
	orig := dockerCommand
	defer func() { dockerCommand = orig }()

	stdinFile := filepath.Join(t.TempDir(), "stdin.json")

	dockerCommand = fakeDockerCommandWithStdin(func(args []string) (string, string, int) {
		if len(args) >= 2 {
			switch args[1] {
			case "run":
				return "abc123\n", "", 0
			case "exec":
				return `{"ok":true}`, "", 0
			case "rm":
				return "", "", 0
			}
		}
		return "", "unknown", 1
	}, stdinFile)

	c := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	if err := c.Start("/workspace", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}

	input := []byte(`{"file_path":"/workspace/main.go","old_string":"hello\nworld","new_string":"goodbye"}`)
	result, err := c.ExecWithStdin(input, 30, "edit-file")
	if err != nil {
		t.Fatalf("ExecWithStdin: %v", err)
	}
	if result.Stdout != `{"ok":true}` {
		t.Errorf("stdout = %q, want %q", result.Stdout, `{"ok":true}`)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}

	// Verify stdin was piped correctly.
	captured, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("reading captured stdin: %v", err)
	}
	if string(captured) != string(input) {
		t.Errorf("captured stdin = %q, want %q", captured, input)
	}

	_ = c.Stop()
}

func TestContainerClient_ExecWithStdinNotRunning(t *testing.T) {
	c := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	_, err := c.ExecWithStdin([]byte("test"), 30, "echo")
	if err == nil {
		t.Fatal("expected error")
	}
	cerr, ok := err.(*ContainerError)
	if !ok {
		t.Fatalf("expected ContainerError, got %T", err)
	}
	if cerr.Code != ErrNotRunning {
		t.Errorf("expected code %s, got %s", ErrNotRunning, cerr.Code)
	}
}

func TestContainerClient_ExecDockerFailure(t *testing.T) {
	orig := dockerCommand
	defer func() { dockerCommand = orig }()

	dockerCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		// For exec subcommand, return a command that fails to start (non-ExitError).
		// Simulates docker daemon unreachable / exec binary not found.
		if len(args) > 0 && args[0] == "exec" {
			return exec.CommandContext(ctx, "/nonexistent-docker-test-binary")
		}
		return fakeDockerCommand(func(a []string) (string, string, int) {
			if len(a) >= 2 && a[1] == "run" {
				return "cid123\n", "", 0
			}
			return "", "", 0
		})(ctx, name, args...)
	}

	c := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	if err := c.Start("/workspace", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Stop()

	_, err := c.Exec("echo hello", 5)
	if err == nil {
		t.Fatal("expected error when docker exec binary is unreachable")
	}
	cerr, ok := err.(*ContainerError)
	if !ok {
		t.Fatalf("expected *ContainerError, got %T: %v", err, err)
	}
	if cerr.Code != ErrExecFailed {
		t.Errorf("expected code %s, got %s", ErrExecFailed, cerr.Code)
	}
	// Error message must be descriptive, not just "exec failed"
	if !strings.Contains(cerr.Message, "docker exec:") {
		t.Errorf("error should mention 'docker exec:', got: %s", cerr.Message)
	}
}

func TestContainerClient_ExecWithStdinDockerFailure(t *testing.T) {
	orig := dockerCommand
	defer func() { dockerCommand = orig }()

	dockerCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if len(args) > 0 && args[0] == "exec" {
			return exec.CommandContext(ctx, "/nonexistent-docker-test-binary")
		}
		return fakeDockerCommand(func(a []string) (string, string, int) {
			if len(a) >= 2 && a[1] == "run" {
				return "cid123\n", "", 0
			}
			return "", "", 0
		})(ctx, name, args...)
	}

	c := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	if err := c.Start("/workspace", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Stop()

	_, err := c.ExecWithStdin([]byte("test input"), 5, "cat")
	if err == nil {
		t.Fatal("expected error when docker exec binary is unreachable")
	}
	cerr, ok := err.(*ContainerError)
	if !ok {
		t.Fatalf("expected *ContainerError, got %T: %v", err, err)
	}
	if cerr.Code != ErrExecFailed {
		t.Errorf("expected code %s, got %s", ErrExecFailed, cerr.Code)
	}
	if !strings.Contains(cerr.Message, "docker exec:") {
		t.Errorf("error should mention 'docker exec:', got: %s", cerr.Message)
	}
}

func TestContainerClient_RebuildStartFailure(t *testing.T) {
	orig := dockerCommand
	defer func() { dockerCommand = orig }()

	var calledRmOld bool
	const oldID = "oldrunning"

	dockerCommand = fakeDockerCommand(func(args []string) (string, string, int) {
		if len(args) >= 2 {
			switch args[1] {
			case "run":
				// First call succeeds (initial Start), second fails (Start inside Rebuild).
				if !calledRmOld {
					return oldID + "\n", "", 0
				}
				return "", "cannot start container: out of memory", 1
			case "build":
				return "", "", 0
			case "rm":
				if len(args) >= 4 && args[2] == "-f" && args[3] == oldID {
					calledRmOld = true
				}
				return "", "", 0
			}
		}
		return "", "unknown", 1
	})

	c := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	if err := c.Start("/workspace", nil); err != nil {
		t.Fatalf("initial Start: %v", err)
	}

	err := c.Rebuild("myimage:latest", "/workspace/Dockerfile", "/workspace", nil)
	if err == nil {
		t.Fatal("expected error from Rebuild when docker run fails")
	}

	// Must be a ContainerError.
	cerr, ok := err.(*ContainerError)
	if !ok {
		t.Fatalf("expected *ContainerError, got %T", err)
	}
	if cerr.Code != ErrStartFailed {
		t.Errorf("error code = %q, want %q", cerr.Code, ErrStartFailed)
	}

	// Old container must have been stopped before the failed Start.
	if !calledRmOld {
		t.Errorf("expected docker rm -f %s to be called before new Start", oldID)
	}
}

func TestContainerClient_ConcurrentExecAndRebuild(t *testing.T) {
	orig := dockerCommand
	defer func() { dockerCommand = orig }()

	dockerCommand = fakeDockerCommand(func(args []string) (string, string, int) {
		if len(args) >= 2 {
			switch args[1] {
			case "run":
				return "container-id\n", "", 0
			case "exec":
				return "exec-output\n", "", 0
			case "build":
				return "", "", 0
			case "rm":
				return "", "", 0
			}
		}
		return "", "", 1
	})

	c := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	if err := c.Start("/workspace", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 10)

	// Launch 3 concurrent execs.
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.Exec("echo test", 5)
			if err != nil {
				// ErrNotRunning is expected during the rebuild window
				// (between Rebuild setting running=false and Start completing).
				if cerr, ok := err.(*ContainerError); ok && cerr.Code == ErrNotRunning {
					return
				}
				errCh <- fmt.Errorf("exec: %w", err)
			}
		}()
	}

	// Launch rebuild concurrently.
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := c.Rebuild("new:latest", "/tmp/Dockerfile", "/workspace", nil)
		if err != nil {
			errCh <- fmt.Errorf("rebuild: %w", err)
		}
	}()

	// Wait with timeout to detect deadlock.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All operations completed — no deadlock.
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock: concurrent exec + rebuild did not complete within 10s")
	}

	close(errCh)
	for err := range errCh {
		t.Errorf("unexpected error: %v", err)
	}

	// After rebuild, the container must be running.
	c.mu.Lock()
	running := c.running
	c.mu.Unlock()
	if !running {
		t.Error("expected container to be running after concurrent rebuild")
	}
}
