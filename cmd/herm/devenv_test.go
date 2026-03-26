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
	tool := NewDevEnvTool(nil, "/tmp/herm", "/tmp/workspace", nil, "", nil, nil)
	def := tool.Definition()
	if def.Name != "devenv" {
		t.Errorf("Name = %q, want %q", def.Name, "devenv")
	}
	if def.Description == "" {
		t.Error("Description should not be empty")
	}
	// Schema must not expose the 'name' parameter.
	if strings.Contains(string(def.InputSchema), `"name"`) {
		t.Error("InputSchema should not expose 'name' parameter")
	}
}

func TestDevEnvTool_ReadNoDockerfile(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")
	workspace := dir

	tool := NewDevEnvTool(nil, hermDir, workspace, nil, "", nil, nil)
	input, _ := json.Marshal(devenvInput{Action: "read"})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "No .herm/Dockerfile exists yet") {
		t.Errorf("expected 'no Dockerfile' message, got: %s", result)
	}
	if !strings.Contains(result, "## Dockerfile guidelines") {
		t.Error("read output should include Dockerfile guidelines even when no Dockerfile exists")
	}
}

func TestDevEnvTool_ReadExistingDockerfile(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")
	os.MkdirAll(hermDir, 0o755)

	content := "FROM alpine:latest\nRUN apk add go\n"
	os.WriteFile(filepath.Join(hermDir, "Dockerfile"), []byte(content), 0o644)

	tool := NewDevEnvTool(nil, hermDir, dir, nil, "", nil, nil)
	input, _ := json.Marshal(devenvInput{Action: "read"})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, content) {
		t.Errorf("got %q, want it to contain %q", result, content)
	}
	if !strings.Contains(result, "## Dockerfile guidelines") {
		t.Error("read output should include Dockerfile guidelines")
	}
}

func TestDevEnvTool_ReadDetectsRootDockerfile(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")

	// No .herm/Dockerfile, but a root Dockerfile exists.
	rootContent := "FROM node:20\nWORKDIR /app\n"
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(rootContent), 0o644)

	tool := NewDevEnvTool(nil, hermDir, dir, nil, "", nil, nil)
	input, _ := json.Marshal(devenvInput{Action: "read"})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "No .herm/Dockerfile exists yet") {
		t.Error("expected 'no Dockerfile' message")
	}
	if !strings.Contains(result, "Dockerfile exists in the project root") {
		t.Error("expected root Dockerfile detection message")
	}
	if !strings.Contains(result, rootContent) {
		t.Error("expected root Dockerfile content in response")
	}
}

func TestDevEnvTool_ReadSurfacesNamedDockerfiles(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")
	os.MkdirAll(hermDir, 0o755)

	// Named Dockerfiles exist but no canonical .herm/Dockerfile.
	os.WriteFile(filepath.Join(hermDir, "go.Dockerfile"), []byte("FROM golang:1.22\n"), 0o644)
	os.WriteFile(filepath.Join(hermDir, "node.Dockerfile"), []byte("FROM node:22\n"), 0o644)

	tool := NewDevEnvTool(nil, hermDir, dir, nil, "", nil, nil)
	input, _ := json.Marshal(devenvInput{Action: "read"})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "go.Dockerfile") {
		t.Error("expected go.Dockerfile in consolidation notice")
	}
	if !strings.Contains(result, "node.Dockerfile") {
		t.Error("expected node.Dockerfile in consolidation notice")
	}
}

func TestDevEnvTool_WriteDockerfile(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")

	tool := NewDevEnvTool(nil, hermDir, dir, nil, "", nil, nil)
	content := "FROM aduermael/herm:__HERM_VERSION__\nRUN apt-get update && apt-get install -y curl && rm -rf /var/lib/apt/lists/*\n"
	input, _ := json.Marshal(devenvInput{Action: "write", Content: content})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "Dockerfile written") {
		t.Errorf("expected success message, got: %s", result)
	}

	// Verify file was written to canonical path.
	data, err := os.ReadFile(filepath.Join(hermDir, "Dockerfile"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != content {
		t.Errorf("file content = %q, want %q", string(data), content)
	}
}

func TestDevEnvTool_WriteRejectsWrongBase(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")

	tool := NewDevEnvTool(nil, hermDir, dir, nil, "", nil, nil)
	content := "FROM ubuntu:22.04\nRUN apt-get update\n"
	input, _ := json.Marshal(devenvInput{Action: "write", Content: content})

	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("expected error for non-herm base image")
	}
	if !strings.Contains(err.Error(), "aduermael/herm") {
		t.Errorf("expected herm base image error, got: %v", err)
	}
}

func TestDevEnvTool_WriteEmptyContent(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")

	tool := NewDevEnvTool(nil, hermDir, dir, nil, "", nil, nil)
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
	hermDir := filepath.Join(dir, ".herm")

	tool := NewDevEnvTool(nil, hermDir, dir, nil, "", nil, nil)
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
	hermDir := filepath.Join(dir, ".herm")
	os.MkdirAll(hermDir, 0o755)
	os.WriteFile(filepath.Join(hermDir, "Dockerfile"), []byte("FROM aduermael/herm:__HERM_VERSION__\nRUN apt-get update && apt-get install -y curl && rm -rf /var/lib/apt/lists/*\n"), 0o644)

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

	container := NewContainerClient(ContainerConfig{Image: defaultContainerImage})
	// Simulate a running container.
	container.running = true
	container.containerID = "oldcontainer456"

	mounts := []MountSpec{{Source: dir, Destination: "/workspace"}}
	tool := NewDevEnvTool(container, hermDir, dir, mounts, "", nil, nil)
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
	tool := NewDevEnvTool(nil, "/tmp", "/tmp", nil, "", nil, nil)
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
	tool := NewDevEnvTool(nil, "/tmp", "/tmp", nil, "", nil, nil)

	_, err := tool.Execute(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestDevEnvTool_RequiresApproval(t *testing.T) {
	tool := NewDevEnvTool(nil, "/tmp", "/tmp", nil, "", nil, nil)
	if tool.RequiresApproval(nil) {
		t.Error("DevEnvTool should not require approval")
	}
}

func TestDevEnvTool_OnRebuildCallback(t *testing.T) {
	orig := dockerCommand
	defer func() { dockerCommand = orig }()

	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")
	os.MkdirAll(hermDir, 0o755)
	dfContent := "FROM aduermael/herm:__HERM_VERSION__\nRUN apt-get update && apt-get install -y golang && rm -rf /var/lib/apt/lists/*\n"
	os.WriteFile(filepath.Join(hermDir, "Dockerfile"), []byte(dfContent), 0o644)

	dockerCommand = fakeDockerCommand(func(args []string) (string, string, int) {
		if len(args) >= 2 {
			switch args[1] {
			case "build":
				return "", "", 0
			case "rm":
				return "", "", 0
			case "run":
				return "container123\n", "", 0
			}
		}
		return "", "", 0
	})

	container := NewContainerClient(ContainerConfig{Image: defaultContainerImage})
	container.running = true
	container.containerID = "old123"

	var callbackImage string
	onRebuild := func(img string) { callbackImage = img }

	mounts := []MountSpec{{Source: dir, Destination: "/workspace"}}
	tool := NewDevEnvTool(container, hermDir, dir, mounts, "abcdef1234567890", onRebuild, nil)
	input, _ := json.Marshal(devenvInput{Action: "build"})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "rebuilt successfully") {
		t.Errorf("expected success message, got: %s", result)
	}

	// Verify callback was called with a valid image name.
	if callbackImage == "" {
		t.Error("onRebuild callback was not called")
	}
	if !strings.HasPrefix(callbackImage, "herm-abcdef12:") {
		t.Errorf("onRebuild called with %q, want prefix %q", callbackImage, "herm-abcdef12:")
	}
	if !strings.Contains(result, callbackImage) {
		t.Errorf("expected image name in result, got: %s", result)
	}
}

func TestResolveDockerfile(t *testing.T) {
	input := "FROM aduermael/herm:__HERM_VERSION__\nRUN apt-get update\n"
	got := resolveDockerfile(input)
	want := "FROM aduermael/herm:" + hermImageTag + "\nRUN apt-get update\n"
	if got != want {
		t.Errorf("resolveDockerfile() = %q, want %q", got, want)
	}
}

func TestResolveDockerfile_NoPlaceholder(t *testing.T) {
	input := "FROM debian:bookworm\nRUN echo hello\n"
	got := resolveDockerfile(input)
	if got != input {
		t.Errorf("expected no change, got %q", got)
	}
}

func TestDockerfileUsesHermBase_Placeholder(t *testing.T) {
	if !dockerfileUsesHermBase("FROM aduermael/herm:__HERM_VERSION__\nRUN echo\n") {
		t.Error("should accept __HERM_VERSION__ placeholder")
	}
}

func TestDockerfileUsesHermBase_HardcodedVersion(t *testing.T) {
	if dockerfileUsesHermBase("FROM aduermael/herm:0.1\nRUN echo\n") {
		t.Error("should reject hardcoded version tag")
	}
}

func TestDockerfileUsesHermBase_WrongBase(t *testing.T) {
	if dockerfileUsesHermBase("FROM ubuntu:22.04\n") {
		t.Error("should reject non-herm base")
	}
}

func TestDockerfileUsesHermBase_CommentBeforeFROM(t *testing.T) {
	content := "# my comment\nFROM aduermael/herm:__HERM_VERSION__\n"
	if !dockerfileUsesHermBase(content) {
		t.Error("should accept placeholder after comment lines")
	}
}

// TestDevEnvTool_NameParamIgnored verifies that passing a 'name' field (from
// old callers) is silently accepted without error and still uses the canonical path.
func TestDevEnvTool_NameParamIgnored(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")

	tool := NewDevEnvTool(nil, hermDir, dir, nil, "", nil, nil)
	content := "FROM aduermael/herm:__HERM_VERSION__\nRUN apt-get update && apt-get install -y golang && rm -rf /var/lib/apt/lists/*\n"
	// Pass name:"go" — it should be ignored.
	input, _ := json.Marshal(devenvInput{Action: "write", Name: "go", Content: content})

	_, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute write: %v", err)
	}

	// File must be at the canonical path, not .herm/go.Dockerfile.
	if _, statErr := os.Stat(filepath.Join(hermDir, "Dockerfile")); statErr != nil {
		t.Error("expected .herm/Dockerfile to exist")
	}
	if _, statErr := os.Stat(filepath.Join(hermDir, "go.Dockerfile")); !os.IsNotExist(statErr) {
		t.Error(".herm/go.Dockerfile must not be created")
	}
}
