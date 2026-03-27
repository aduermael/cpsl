package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManifestFromDockerfile_WithEnvAndPackages(t *testing.T) {
	dockerfile := `FROM aduermael/herm:__HERM_VERSION__
ENV GOLANG_VERSION=1.22.5
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget \
    && wget -qO go.tar.gz "https://go.dev/dl/go${GOLANG_VERSION}.linux-amd64.tar.gz" \
    && tar -C /usr/local -xzf go.tar.gz && rm go.tar.gz \
    && rm -rf /var/lib/apt/lists/*
ENV PATH="/usr/local/go/bin:/root/go/bin:$PATH"
`
	got := manifestFromDockerfile(dockerfile)

	if !strings.Contains(got, "Pre-installed: git, ripgrep (rg), tree, python3") {
		t.Error("missing base image info")
	}
	if !strings.Contains(got, "GOLANG_VERSION=1.22.5") {
		t.Errorf("missing Go version env: %s", got)
	}
	if !strings.Contains(got, "wget") {
		t.Errorf("missing wget package: %s", got)
	}
	// ca-certificates is a base package, should be filtered out.
	if strings.Contains(got, "Packages:") && strings.Contains(got, "ca-certificates") {
		// ca-certificates IS a base package, so it should be filtered
		t.Errorf("should filter base package ca-certificates: %s", got)
	}
}

func TestManifestFromDockerfile_NodeAndPython(t *testing.T) {
	dockerfile := `FROM aduermael/herm:__HERM_VERSION__
ENV NODE_VERSION=22.14.0
RUN apt-get update && apt-get install -y --no-install-recommends wget xz-utils \
    && wget -qO node.tar.xz "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-x64.tar.xz" \
    && tar -xJf node.tar.xz -C /usr/local --strip-components=1 && rm node.tar.xz \
    && rm -rf /var/lib/apt/lists/*
RUN apt-get update && apt-get install -y --no-install-recommends python3-pip python3-venv \
    && rm -rf /var/lib/apt/lists/*
`
	got := manifestFromDockerfile(dockerfile)

	if !strings.Contains(got, "NODE_VERSION=22.14.0") {
		t.Errorf("missing Node version: %s", got)
	}
	if !strings.Contains(got, "python3-pip") {
		t.Errorf("missing python3-pip: %s", got)
	}
	if !strings.Contains(got, "python3-venv") {
		t.Errorf("missing python3-venv: %s", got)
	}
	if !strings.Contains(got, "xz-utils") {
		t.Errorf("missing xz-utils: %s", got)
	}
}

func TestManifestFromDockerfile_NoCustomizations(t *testing.T) {
	dockerfile := "FROM aduermael/herm:__HERM_VERSION__\n"
	got := manifestFromDockerfile(dockerfile)

	if !strings.Contains(got, "Pre-installed:") {
		t.Error("should always include base image info")
	}
	if strings.Contains(got, "Environment:") {
		t.Errorf("should not have Environment section: %s", got)
	}
	if strings.Contains(got, "Packages:") {
		t.Errorf("should not have Packages section: %s", got)
	}
}

func TestManifestFromDockerfile_MultipleEnvVars(t *testing.T) {
	dockerfile := `FROM aduermael/herm:__HERM_VERSION__
ENV GOLANG_VERSION=1.22.5
ENV NODE_VERSION=22.14.0
ENV PATH="/usr/local/go/bin:$PATH"
`
	got := manifestFromDockerfile(dockerfile)

	if !strings.Contains(got, "GOLANG_VERSION=1.22.5") {
		t.Error("missing Go version")
	}
	if !strings.Contains(got, "NODE_VERSION=22.14.0") {
		t.Error("missing Node version")
	}
}

func TestExtractAptPackages_FiltersBasePackages(t *testing.T) {
	dockerfile := `RUN apt-get update && apt-get install -y git tree curl wget && rm -rf /var/lib/apt/lists/*`
	pkgs := extractAptPackages(dockerfile)

	// git and tree are base packages, should be filtered.
	for _, pkg := range pkgs {
		if pkg == "git" || pkg == "tree" {
			t.Errorf("base package %q should be filtered out", pkg)
		}
	}
	// curl and wget are custom, should be present.
	found := map[string]bool{}
	for _, pkg := range pkgs {
		found[pkg] = true
	}
	if !found["curl"] {
		t.Error("missing curl")
	}
	if !found["wget"] {
		t.Error("missing wget")
	}
}

func TestExtractEnvVars(t *testing.T) {
	dockerfile := `FROM x
ENV GOLANG_VERSION=1.22.5
ENV NODE_VERSION=22.14.0
RUN echo hello
ENV PATH="/usr/local/go/bin:$PATH"
`
	envs := extractEnvVars(dockerfile)
	if len(envs) != 3 {
		t.Fatalf("expected 3 env vars, got %d: %v", len(envs), envs)
	}
	if envs[0] != "GOLANG_VERSION=1.22.5" {
		t.Errorf("envs[0] = %q", envs[0])
	}
	if envs[1] != "NODE_VERSION=22.14.0" {
		t.Errorf("envs[1] = %q", envs[1])
	}
}

func TestManifestPath(t *testing.T) {
	tool := NewDevEnvTool(nil, "/tmp/.herm", "/tmp", nil, "", nil, nil)
	got := tool.manifestPath()
	if got != "/tmp/.herm/environment.md" {
		t.Errorf("manifestPath() = %q, want /tmp/.herm/environment.md", got)
	}
}

func TestGenerateManifest_NoDockerfile(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")

	tool := NewDevEnvTool(nil, hermDir, dir, nil, "", nil, nil)
	if err := tool.generateManifest(); err != nil {
		t.Fatalf("generateManifest: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(hermDir, manifestFile))
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "Pre-installed:") {
		t.Error("base manifest should include Pre-installed")
	}
	if !strings.Contains(content, "edit-file") {
		t.Error("base manifest should list herm tools")
	}
}

func TestGenerateManifest_WithDockerfile(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")
	os.MkdirAll(hermDir, 0o755)

	dockerfile := "FROM aduermael/herm:__HERM_VERSION__\nENV GOLANG_VERSION=1.22.5\nRUN apt-get update && apt-get install -y wget && rm -rf /var/lib/apt/lists/*\n"
	os.WriteFile(filepath.Join(hermDir, "Dockerfile"), []byte(dockerfile), 0o644)

	tool := NewDevEnvTool(nil, hermDir, dir, nil, "", nil, nil)
	if err := tool.generateManifest(); err != nil {
		t.Fatalf("generateManifest: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(hermDir, manifestFile))
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "GOLANG_VERSION=1.22.5") {
		t.Errorf("manifest should include Go version: %s", content)
	}
	if !strings.Contains(content, "wget") {
		t.Errorf("manifest should include wget: %s", content)
	}
}

func TestManifestStale_Missing(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")
	os.MkdirAll(hermDir, 0o755)
	os.WriteFile(filepath.Join(hermDir, "Dockerfile"), []byte("FROM x\n"), 0o644)

	tool := NewDevEnvTool(nil, hermDir, dir, nil, "", nil, nil)
	if !tool.manifestStale() {
		t.Error("manifest should be stale when missing")
	}
}

func TestManifestStale_OlderThanDockerfile(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")
	os.MkdirAll(hermDir, 0o755)

	os.WriteFile(filepath.Join(hermDir, manifestFile), []byte("old\n"), 0o644)
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(filepath.Join(hermDir, "Dockerfile"), []byte("FROM x\n"), 0o644)

	tool := NewDevEnvTool(nil, hermDir, dir, nil, "", nil, nil)
	if !tool.manifestStale() {
		t.Error("manifest should be stale when older than Dockerfile")
	}
}

func TestManifestStale_Fresh(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")
	os.MkdirAll(hermDir, 0o755)

	os.WriteFile(filepath.Join(hermDir, "Dockerfile"), []byte("FROM x\n"), 0o644)
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(filepath.Join(hermDir, manifestFile), []byte("fresh\n"), 0o644)

	tool := NewDevEnvTool(nil, hermDir, dir, nil, "", nil, nil)
	if tool.manifestStale() {
		t.Error("manifest should not be stale when newer than Dockerfile")
	}
}

func TestManifestStale_NothingExists(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")

	tool := NewDevEnvTool(nil, hermDir, dir, nil, "", nil, nil)
	if !tool.manifestStale() {
		t.Error("manifest should be stale when nothing exists")
	}
}
