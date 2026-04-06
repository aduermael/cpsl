package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// --- Phase 4: fetchProjectSnapshot tests ---

func TestFetchProjectSnapshot_NormalRepo(t *testing.T) {
	tmp := t.TempDir()

	// Initialize a git repo with a commit.
	for _, cmd := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		c := exec.Command("git", cmd...)
		c.Dir = tmp
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", cmd, err, out)
		}
	}

	// Create a file and commit.
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range [][]string{
		{"add", "main.go"},
		{"commit", "-m", "initial commit"},
	} {
		c := exec.Command("git", cmd...)
		c.Dir = tmp
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", cmd, err, out)
		}
	}

	msg := fetchProjectSnapshot(tmp)
	snap := msg.snapshot

	if snap.TopLevel == "" {
		t.Error("TopLevel should not be empty for a directory with files")
	}
	if !strings.Contains(snap.TopLevel, "main.go") {
		t.Errorf("TopLevel should contain main.go, got: %q", snap.TopLevel)
	}

	if snap.RecentCommits == "" {
		t.Error("RecentCommits should not be empty for a repo with commits")
	}
	if !strings.Contains(snap.RecentCommits, "initial commit") {
		t.Errorf("RecentCommits should contain commit message, got: %q", snap.RecentCommits)
	}

	// Clean repo — GitStatus should be empty.
	if snap.GitStatus != "" {
		t.Errorf("GitStatus should be empty for clean repo, got: %q", snap.GitStatus)
	}
}

func TestFetchProjectSnapshot_DirtyRepo(t *testing.T) {
	tmp := t.TempDir()

	for _, cmd := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		c := exec.Command("git", cmd...)
		c.Dir = tmp
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", cmd, err, out)
		}
	}

	// Create an untracked file.
	if err := os.WriteFile(filepath.Join(tmp, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}

	msg := fetchProjectSnapshot(tmp)
	snap := msg.snapshot

	if snap.GitStatus == "" {
		t.Error("GitStatus should not be empty when there are uncommitted changes")
	}
	if !strings.Contains(snap.GitStatus, "dirty.txt") {
		t.Errorf("GitStatus should contain dirty.txt, got: %q", snap.GitStatus)
	}
}

func TestFetchProjectSnapshot_NonGitDir(t *testing.T) {
	tmp := t.TempDir()

	// Create a file so ls has output.
	if err := os.WriteFile(filepath.Join(tmp, "readme.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	msg := fetchProjectSnapshot(tmp)
	snap := msg.snapshot

	// ls should still work.
	if snap.TopLevel == "" {
		t.Error("TopLevel should not be empty even in a non-git directory")
	}

	// Git commands should fail gracefully → empty strings.
	if snap.RecentCommits != "" {
		t.Errorf("RecentCommits should be empty for non-git dir, got: %q", snap.RecentCommits)
	}
	if snap.GitStatus != "" {
		t.Errorf("GitStatus should be empty for non-git dir, got: %q", snap.GitStatus)
	}
}

func TestFetchProjectSnapshot_SparseDir(t *testing.T) {
	tmp := t.TempDir()

	// Create fewer than 8 entries to trigger tree fallback.
	for _, name := range []string{"src", "docs"} {
		os.MkdirAll(filepath.Join(tmp, name), 0o755)
	}
	os.WriteFile(filepath.Join(tmp, "README.md"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(tmp, "src", "main.go"), []byte("package main"), 0o644)

	msg := fetchProjectSnapshot(tmp)
	snap := msg.snapshot

	if snap.TopLevel == "" {
		t.Error("TopLevel should not be empty")
	}

	// If tree is available, the output should include subdirectory contents.
	// tree may not be installed in all environments, so we just check TopLevel is non-empty.
	t.Logf("TopLevel output:\n%s", snap.TopLevel)
}

// --- Phase 4b: snapshot injection in system prompt ---

func TestBuildSystemPromptWithSnapshot(t *testing.T) {
	snap := &projectSnapshot{
		TopLevel:      "cmd/\ngo.mod\nREADME.md",
		RecentCommits: "abc123 initial commit\ndef456 add feature",
		GitStatus:     "M main.go",
	}

	prompt := buildSystemPrompt(nil, nil, nil, "/work", "", "", "alpine:latest", "", snap)

	if !strings.Contains(prompt, "## Project context") {
		t.Error("prompt should contain Project context section when snapshot is provided")
	}
	if !strings.Contains(prompt, "Top-level:") {
		t.Error("prompt should contain Top-level listing")
	}
	if !strings.Contains(prompt, "cmd/") {
		t.Error("prompt should contain snapshot listing content")
	}
	if !strings.Contains(prompt, "Recent commits:") {
		t.Error("prompt should contain Recent commits")
	}
	if !strings.Contains(prompt, "abc123 initial commit") {
		t.Error("prompt should contain commit messages")
	}
	if !strings.Contains(prompt, "Uncommitted changes:") {
		t.Error("prompt should contain Uncommitted changes")
	}
	if !strings.Contains(prompt, "M main.go") {
		t.Error("prompt should contain git status content")
	}
}

func TestBuildSystemPromptWithoutSnapshot(t *testing.T) {
	prompt := buildSystemPrompt(nil, nil, nil, "/work", "", "", "alpine:latest", "", nil)

	if strings.Contains(prompt, "## Project context") {
		t.Error("prompt should NOT contain Project context section when snapshot is nil")
	}
}

func TestBuildSystemPromptCleanRepo(t *testing.T) {
	snap := &projectSnapshot{
		TopLevel:      "cmd/\ngo.mod",
		RecentCommits: "abc123 initial commit",
		GitStatus:     "", // clean
	}

	prompt := buildSystemPrompt(nil, nil, nil, "/work", "", "", "alpine:latest", "", snap)

	if !strings.Contains(prompt, "## Project context") {
		t.Error("prompt should contain Project context section")
	}
	if strings.Contains(prompt, "Uncommitted changes") {
		t.Error("prompt should omit 'Uncommitted changes' section when GitStatus is empty")
	}
}

// --- Phase 4c: sub-agent receives snapshot ---

func TestBuildSubAgentSystemPromptWithSnapshot(t *testing.T) {
	snap := &projectSnapshot{
		TopLevel:      "src/\npackage.json",
		RecentCommits: "aaa111 fix bug\nbbb222 add tests",
		GitStatus:     "",
	}

	tools := []Tool{stubTool{"bash"}}
	prompt := buildSubAgentSystemPrompt(tools, nil, "/work", "", "alpine:latest", snap)

	if !strings.Contains(prompt, "## Project context") {
		t.Error("sub-agent prompt should contain Project context when snapshot is provided")
	}
	if !strings.Contains(prompt, "src/") {
		t.Error("sub-agent prompt should contain snapshot listing")
	}
	if !strings.Contains(prompt, "fix bug") {
		t.Error("sub-agent prompt should contain commit messages")
	}
}

func TestBuildSubAgentSystemPromptWithoutSnapshot(t *testing.T) {
	tools := []Tool{stubTool{"bash"}}
	prompt := buildSubAgentSystemPrompt(tools, nil, "/work", "", "alpine:latest", nil)

	if strings.Contains(prompt, "## Project context") {
		t.Error("sub-agent prompt should NOT contain Project context when snapshot is nil")
	}
}

// --- Phase 10: buildProjectTree tests ---

func TestBuildProjectTree_TwoLevel(t *testing.T) {
	tmp := t.TempDir()

	// Create a small project structure.
	os.MkdirAll(filepath.Join(tmp, "cmd", "app"), 0o755)
	os.MkdirAll(filepath.Join(tmp, "pkg", "util"), 0o755)
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module test"), 0o644)
	os.WriteFile(filepath.Join(tmp, "README.md"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(tmp, "cmd", "main.go"), []byte("package main"), 0o644)
	os.WriteFile(filepath.Join(tmp, "pkg", "lib.go"), []byte("package pkg"), 0o644)

	result := buildProjectTree(tmp, 20, 8)

	if result == "" {
		t.Fatal("expected non-empty tree output")
	}

	// Top-level entries.
	if !strings.Contains(result, "cmd/") {
		t.Error("should contain cmd/")
	}
	if !strings.Contains(result, "pkg/") {
		t.Error("should contain pkg/")
	}
	if !strings.Contains(result, "go.mod") {
		t.Error("should contain go.mod")
	}
	if !strings.Contains(result, "README.md") {
		t.Error("should contain README.md")
	}

	// Sub-entries (indented).
	if !strings.Contains(result, "  main.go") {
		t.Errorf("should contain indented sub-entry main.go, got:\n%s", result)
	}
	if !strings.Contains(result, "  app/") {
		t.Errorf("should contain indented sub-directory app/, got:\n%s", result)
	}
}

func TestBuildProjectTree_TopLevelTruncation(t *testing.T) {
	tmp := t.TempDir()

	// Create 25 top-level files — more than maxTopLevel=5.
	for i := 0; i < 25; i++ {
		os.WriteFile(filepath.Join(tmp, fmt.Sprintf("file%02d.txt", i)), []byte("x"), 0o644)
	}
	// Add an important file that should survive truncation.
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module test"), 0o644)

	result := buildProjectTree(tmp, 5, 8)

	if !strings.Contains(result, "go.mod") {
		t.Errorf("important file go.mod should be preserved during truncation, got:\n%s", result)
	}
	if !strings.Contains(result, "+") || !strings.Contains(result, "more") {
		t.Errorf("should contain +N more truncation indicator, got:\n%s", result)
	}
	// Count non-"+N more" lines — should be at most maxTopLevel.
	lines := strings.Split(result, "\n")
	entryCount := 0
	for _, line := range lines {
		if !strings.HasPrefix(line, "+") {
			entryCount++
		}
	}
	if entryCount > 5 {
		t.Errorf("expected at most 5 top-level entries, got %d:\n%s", entryCount, result)
	}
}

func TestBuildProjectTree_SubdirTruncation(t *testing.T) {
	tmp := t.TempDir()

	// Create a directory with 15 sub-entries — more than maxPerSubdir=3.
	dir := filepath.Join(tmp, "bigdir")
	os.MkdirAll(dir, 0o755)
	for i := 0; i < 15; i++ {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("item%02d.go", i)), []byte("x"), 0o644)
	}

	result := buildProjectTree(tmp, 20, 3)

	if !strings.Contains(result, "bigdir/") {
		t.Errorf("should contain bigdir/, got:\n%s", result)
	}
	if !strings.Contains(result, "  +12 more") {
		t.Errorf("should contain sub-entry truncation '+12 more', got:\n%s", result)
	}
}

func TestBuildProjectTree_HiddenFilesExcluded(t *testing.T) {
	tmp := t.TempDir()

	os.WriteFile(filepath.Join(tmp, ".hidden"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(tmp, "visible.txt"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(tmp, ".git"), 0o755)
	os.WriteFile(filepath.Join(tmp, ".git", "config"), []byte("x"), 0o644)

	result := buildProjectTree(tmp, 20, 8)

	if strings.Contains(result, ".hidden") {
		t.Errorf("hidden files should be excluded, got:\n%s", result)
	}
	if strings.Contains(result, ".git") {
		t.Errorf(".git directory should be excluded, got:\n%s", result)
	}
	if !strings.Contains(result, "visible.txt") {
		t.Error("visible files should be included")
	}
}
