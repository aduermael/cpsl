package main

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Container error codes.
const (
	ErrDockerNotFound = "DockerNotFound"
	ErrStartFailed    = "StartFailed"
	ErrExecFailed     = "ExecFailed"
	ErrStopFailed     = "StopFailed"
	ErrNotRunning     = "NotRunning"
)

// ContainerError is a typed error from the container client.
type ContainerError struct {
	Code    string
	Message string
}

func (e *ContainerError) Error() string {
	return fmt.Sprintf("container %s: %s", e.Code, e.Message)
}

// ContainerConfig holds configuration for the Docker container.
type ContainerConfig struct {
	Image string // Docker image (default: "alpine:latest")
}

// MountSpec describes a filesystem mount into the container.
type MountSpec struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"read_only"`
}

// CommandResult holds the output of a command executed in the container.
type CommandResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// ContainerStatus holds the current state of the container.
type ContainerStatus struct {
	State string `json:"state"`
}

// dockerCommand is a function variable for exec.CommandContext, replaceable in tests.
var dockerCommand = exec.CommandContext

// ContainerClient manages a Docker container lifecycle.
type ContainerClient struct {
	config      ContainerConfig
	containerID string
	mu          sync.Mutex
	running     bool
}

// NewContainerClient creates a new client with the given config.
func NewContainerClient(config ContainerConfig) *ContainerClient {
	return &ContainerClient{config: config}
}

// IsAvailable returns true if Docker is installed and running.
func (c *ContainerClient) IsAvailable() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := dockerCommand(ctx, "docker", "info")
	return cmd.Run() == nil
}

// Start runs a Docker container with the given workspace and mounts.
func (c *ContainerClient) Start(workspace string, mounts []MountSpec) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		return &ContainerError{Code: ErrStartFailed, Message: "container already running"}
	}

	name := fmt.Sprintf("cpsl-%s", randomID())

	args := []string{"run", "-d", "--name", name}
	for _, m := range mounts {
		vol := fmt.Sprintf("%s:%s", m.Source, m.Destination)
		if m.ReadOnly {
			vol += ":ro"
		}
		args = append(args, "-v", vol)
	}
	args = append(args, c.config.Image, "sleep", "infinity")

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := dockerCommand(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return &ContainerError{
			Code:    ErrStartFailed,
			Message: fmt.Sprintf("docker run: %s", strings.TrimSpace(stderr.String())),
		}
	}

	c.containerID = strings.TrimSpace(stdout.String())
	c.running = true
	return nil
}

// Exec runs a command inside the container and returns the result.
func (c *ContainerClient) Exec(command string, timeout int) (CommandResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.running {
		return CommandResult{}, &ContainerError{Code: ErrNotRunning, Message: "container not running"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := dockerCommand(ctx, "docker", "exec", c.containerID, "sh", "-c", command)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return CommandResult{}, &ContainerError{
				Code:    ErrExecFailed,
				Message: fmt.Sprintf("docker exec: %v", err),
			}
		}
	}

	return CommandResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}, nil
}

// ContainerID returns the Docker container ID.
func (c *ContainerClient) ContainerID() string {
	return c.containerID
}

// ShellCmd returns an exec.Cmd that opens an interactive shell in the container.
// Busybox ash (Alpine's /bin/sh) sends \033[6n (cursor position query) on every
// prompt. The CPR response travels through docker's double-PTY proxy chain with
// enough latency to arrive after the prompt, leaking as visible "[row;colR" text.
// Bash/readline does NOT have this issue, so we prefer bash when available.
// For ash, we pre-consume a CPR response to warm up the PTY chain before exec-ing
// the interactive shell.
func (c *ContainerClient) ShellCmd() *exec.Cmd {
	// Prefer bash (no CPR issue), fall back to ash with CPR pre-consumption.
	script := `if command -v bash >/dev/null 2>&1; then
  exec bash -l
else
  stty raw -echo 2>/dev/null
  printf '\033[6n'
  dd bs=32 count=1 >/dev/null 2>&1
  stty sane 2>/dev/null
  exec /bin/sh -l
fi`
	cmd := exec.Command("docker", "exec", "-it", "-w", "/workspace", c.containerID,
		"/bin/sh", "-c", script)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

// Stop stops and removes the Docker container.
func (c *ContainerClient) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.running {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Force-remove the container in one step (kills and removes).
	rm := dockerCommand(ctx, "docker", "rm", "-f", c.containerID)
	_ = rm.Run()

	c.running = false
	c.containerID = ""
	return nil
}

// Status queries the container's current status.
func (c *ContainerClient) Status() (ContainerStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.running {
		return ContainerStatus{}, &ContainerError{Code: ErrNotRunning, Message: "container not running"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := dockerCommand(ctx, "docker", "inspect", "--format", "{{.State.Status}}", c.containerID)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return ContainerStatus{}, &ContainerError{
			Code:    ErrNotRunning,
			Message: fmt.Sprintf("docker inspect: %v", err),
		}
	}

	return ContainerStatus{
		State: strings.TrimSpace(stdout.String()),
	}, nil
}

// Rebuild builds a Docker image from the given Dockerfile, stops the current
// container, and starts a new one with the built image. The workspace is used
// as the build context directory.
func (c *ContainerClient) Rebuild(dockerfilePath, workspace string, mounts []MountSpec) error {
	// Build the custom image.
	imageName := fmt.Sprintf("cpsl-custom-%s", randomID())

	buildCtx, buildCancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer buildCancel()

	buildCmd := dockerCommand(buildCtx, "docker", "build",
		"-t", imageName,
		"-f", dockerfilePath,
		workspace,
	)
	var buildStderr bytes.Buffer
	buildCmd.Stderr = &buildStderr

	if err := buildCmd.Run(); err != nil {
		return &ContainerError{
			Code:    ErrStartFailed,
			Message: fmt.Sprintf("docker build: %s", strings.TrimSpace(buildStderr.String())),
		}
	}

	// Stop the current container.
	c.mu.Lock()
	wasRunning := c.running
	oldID := c.containerID
	c.mu.Unlock()

	if wasRunning {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		rm := dockerCommand(stopCtx, "docker", "rm", "-f", oldID)
		_ = rm.Run()

		c.mu.Lock()
		c.running = false
		c.containerID = ""
		c.mu.Unlock()
	}

	// Update config to use the new image and start a new container.
	c.mu.Lock()
	c.config.Image = imageName
	c.mu.Unlock()

	return c.Start(workspace, mounts)
}

// randomID generates a short random hex string for container naming.
func randomID() string {
	const chars = "abcdef0123456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}
