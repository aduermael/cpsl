package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevEnvTool_Definition(t *testing.T) {
	tool := NewDevEnvTool(nil, "/tmp/cpsl", "/tmp/workspace", nil, "", nil)
	def := tool.Definition()
	if def.Name != "devenv" {
		t.Errorf("Name = %q, want %q", def.Name, "devenv")
	}
	if def.Description == "" {
		t.Error("Description should not be empty")
	}
}

func TestDevEnvTool_ReadNoDockerfile(t *testing.T) {
	dir := t.TempDir()
	cpslDir := filepath.Join(dir, ".cpsl")
	workspace := dir

	tool := NewDevEnvTool(nil, cpslDir, workspace, nil, "", nil)
	input, _ := json.Marshal(devenvInput{Action: "read"})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "No Dockerfile exists") {
		t.Errorf("expected 'No Dockerfile exists' message, got: %s", result)
	}
}

func TestDevEnvTool_ReadExistingDockerfile(t *testing.T) {
	dir := t.TempDir()
	cpslDir := filepath.Join(dir, ".cpsl")
	os.MkdirAll(cpslDir, 0o755)

	content := "FROM alpine:latest\nRUN apk add go\n"
	os.WriteFile(filepath.Join(cpslDir, "custom.Dockerfile"), []byte(content), 0o644)

	tool := NewDevEnvTool(nil, cpslDir, dir, nil, "", nil)
	input, _ := json.Marshal(devenvInput{Action: "read"})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != content {
		t.Errorf("got %q, want %q", result, content)
	}
}

func TestDevEnvTool_ReadDetectsRootDockerfile(t *testing.T) {
	dir := t.TempDir()
	cpslDir := filepath.Join(dir, ".cpsl")

	// No .cpsl/Dockerfile, but a root Dockerfile exists.
	rootContent := "FROM node:20\nWORKDIR /app\n"
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(rootContent), 0o644)

	tool := NewDevEnvTool(nil, cpslDir, dir, nil, "", nil)
	input, _ := json.Marshal(devenvInput{Action: "read"})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "No Dockerfile exists") {
		t.Error("expected 'No Dockerfile exists' message")
	}
	if !strings.Contains(result, "A Dockerfile exists in the project root") {
		t.Error("expected root Dockerfile detection message")
	}
	if !strings.Contains(result, rootContent) {
		t.Error("expected root Dockerfile content in response")
	}
}

func TestDevEnvTool_WriteDockerfile(t *testing.T) {
	dir := t.TempDir()
	cpslDir := filepath.Join(dir, ".cpsl")

	tool := NewDevEnvTool(nil, cpslDir, dir, nil, "", nil)
	content := "FROM ubuntu:22.04\nRUN apt-get update\n"
	input, _ := json.Marshal(devenvInput{Action: "write", Content: content})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "Dockerfile written") {
		t.Errorf("expected success message, got: %s", result)
	}

	// Verify file was written.
	data, err := os.ReadFile(filepath.Join(cpslDir, "custom.Dockerfile"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != content {
		t.Errorf("file content = %q, want %q", string(data), content)
	}
}

func TestDevEnvTool_WriteEmptyContent(t *testing.T) {
	dir := t.TempDir()
	cpslDir := filepath.Join(dir, ".cpsl")

	tool := NewDevEnvTool(nil, cpslDir, dir, nil, "", nil)
	input, _ := json.Marshal(devenvInput{Action: "write", Content: ""})

	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("expected error for empty content")
	}
	if !strings.Contains(err.Error(), "content is required") {
		t.Errorf("expected 'content is required' error, got: %v", err)
	}
}

func TestDevEnvTool_BuildNoDockerfile(t *testing.T) {
	dir := t.TempDir()
	cpslDir := filepath.Join(dir, ".cpsl")

	tool := NewDevEnvTool(nil, cpslDir, dir, nil, "", nil)
	input, _ := json.Marshal(devenvInput{Action: "build"})

	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("expected error when no Dockerfile exists")
	}
	if !strings.Contains(err.Error(), "no Dockerfile") {
		t.Errorf("expected 'no Dockerfile' error, got: %v", err)
	}
}

func TestDevEnvTool_BuildCallsRebuild(t *testing.T) {
	orig := dockerCommand
	defer func() { dockerCommand = orig }()

	dir := t.TempDir()
	cpslDir := filepath.Join(dir, ".cpsl")
	os.MkdirAll(cpslDir, 0o755)
	os.WriteFile(filepath.Join(cpslDir, "custom.Dockerfile"), []byte("FROM alpine:latest\n"), 0o644)

	dockerCommand = fakeDockerCommand(func(args []string) (string, string, int) {
		if len(args) >= 2 {
			switch args[1] {
			case "build":
				return "", "", 0
			case "rm":
				return "", "", 0
			case "run":
				return "newcontainer123\n", "", 0
			}
		}
		return "", "", 0
	})

	container := NewContainerClient(ContainerConfig{Image: "alpine:latest"})
	// Simulate a running container.
	container.running = true
	container.containerID = "oldcontainer456"

	mounts := []MountSpec{{Source: dir, Destination: "/workspace"}}
	tool := NewDevEnvTool(container, cpslDir, dir, mounts, "", nil)
	input, _ := json.Marshal(devenvInput{Action: "build"})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "rebuilt successfully") {
		t.Errorf("expected success message, got: %s", result)
	}
	if !container.running {
		t.Error("expected container to be running after rebuild")
	}
}

func TestDevEnvTool_InvalidAction(t *testing.T) {
	tool := NewDevEnvTool(nil, "/tmp", "/tmp", nil, "", nil)
	input, _ := json.Marshal(devenvInput{Action: "delete"})

	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("expected error for invalid action")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("expected 'unknown action' error, got: %v", err)
	}
}

func TestDevEnvTool_InvalidJSON(t *testing.T) {
	tool := NewDevEnvTool(nil, "/tmp", "/tmp", nil, "", nil)

	_, err := tool.Execute(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestDevEnvTool_RequiresApproval(t *testing.T) {
	tool := NewDevEnvTool(nil, "/tmp", "/tmp", nil, "", nil)
	if tool.RequiresApproval(nil) {
		t.Error("DevEnvTool should not require approval")
	}
}
