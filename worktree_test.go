package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// initTestRepo creates a temporary git repo with one commit and returns its path.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "initial")
	return dir
}

func TestEnsureProjectID(t *testing.T) {
	repo := initTestRepo(t)

	// First call creates a UUID.
	id1, err := ensureProjectID(repo)
	if err != nil {
		t.Fatalf("ensureProjectID: %v", err)
	}
	if id1 == "" {
		t.Fatal("expected non-empty UUID")
	}

	// Second call returns the same UUID.
	id2, err := ensureProjectID(repo)
	if err != nil {
		t.Fatalf("ensureProjectID second call: %v", err)
	}
	if id1 != id2 {
		t.Errorf("UUID changed: %q != %q", id1, id2)
	}

	// Verify file contents.
	data, err := os.ReadFile(filepath.Join(repo, ".cpsl", "project.json"))
	if err != nil {
		t.Fatal(err)
	}
	var proj projectJSON
	if err := json.Unmarshal(data, &proj); err != nil {
		t.Fatal(err)
	}
	if proj.UUID != id1 {
		t.Errorf("file UUID %q != returned UUID %q", proj.UUID, id1)
	}
}

func TestCreateWorktree(t *testing.T) {
	repo := initTestRepo(t)
	baseDir := t.TempDir()

	wtPath, err := createWorktree(repo, baseDir)
	if err != nil {
		t.Fatalf("createWorktree: %v", err)
	}

	// Worktree directory should exist.
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("worktree path does not exist: %v", err)
	}

	// Should contain a .git file (worktree link).
	if _, err := os.Stat(filepath.Join(wtPath, ".git")); err != nil {
		t.Fatal("worktree missing .git")
	}

	// Should have the README from the repo.
	if _, err := os.Stat(filepath.Join(wtPath, "README.md")); err != nil {
		t.Fatal("worktree missing README.md")
	}
}

func TestListWorktrees_Empty(t *testing.T) {
	baseDir := t.TempDir()

	wts, err := listWorktrees(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 0 {
		t.Errorf("expected 0 worktrees, got %d", len(wts))
	}
}

func TestListWorktrees_NonexistentDir(t *testing.T) {
	wts, err := listWorktrees("/nonexistent/path/12345")
	if err != nil {
		t.Fatal(err)
	}
	if wts != nil {
		t.Errorf("expected nil, got %v", wts)
	}
}

func TestListWorktrees_CleanAndDirty(t *testing.T) {
	repo := initTestRepo(t)
	baseDir := t.TempDir()

	// Create two worktrees.
	wt1, err := createWorktree(repo, baseDir)
	if err != nil {
		t.Fatal(err)
	}
	wt2, err := createWorktree(repo, baseDir)
	if err != nil {
		t.Fatal(err)
	}

	// Make wt2 dirty by creating an untracked file.
	if err := os.WriteFile(filepath.Join(wt2, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}

	wts, err := listWorktrees(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 2 {
		t.Fatalf("expected 2 worktrees, got %d", len(wts))
	}

	// Find each worktree by path.
	var info1, info2 WorktreeInfo
	for _, wt := range wts {
		switch wt.Path {
		case wt1:
			info1 = wt
		case wt2:
			info2 = wt
		}
	}

	if !info1.Clean {
		t.Error("wt1 should be clean")
	}
	if info2.Clean {
		t.Error("wt2 should be dirty")
	}
}

func TestLockUnlock(t *testing.T) {
	dir := t.TempDir()

	// Initially unlocked.
	locked, _ := isWorktreeLocked(dir)
	if locked {
		t.Error("expected unlocked initially")
	}

	// Lock with current PID.
	pid := os.Getpid()
	if err := lockWorktree(dir, pid); err != nil {
		t.Fatal(err)
	}

	locked, gotPID := isWorktreeLocked(dir)
	if !locked {
		t.Error("expected locked")
	}
	if gotPID != pid {
		t.Errorf("expected PID %d, got %d", pid, gotPID)
	}

	// Unlock.
	if err := unlockWorktree(dir); err != nil {
		t.Fatal(err)
	}

	locked, _ = isWorktreeLocked(dir)
	if locked {
		t.Error("expected unlocked after unlock")
	}
}

func TestStaleLockCleanup(t *testing.T) {
	dir := t.TempDir()

	// Write a lock with a PID that doesn't exist (99999999).
	deadPID := 99999999
	lockPath := filepath.Join(dir, lockFileName)
	if err := os.WriteFile(lockPath, []byte(strconv.Itoa(deadPID)), 0o644); err != nil {
		t.Fatal(err)
	}

	// isWorktreeLocked should detect the stale lock and clean it.
	locked, _ := isWorktreeLocked(dir)
	if locked {
		t.Error("expected stale lock to be detected and cleaned")
	}

	// Lock file should be removed.
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Error("expected lock file to be removed")
	}
}

func TestSelectWorktree_CreatesFirst(t *testing.T) {
	repo := initTestRepo(t)

	// Override worktreeBaseDir to use a temp directory.
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	selected, dirty, err := selectWorktree(repo)
	if err != nil {
		t.Fatalf("selectWorktree: %v", err)
	}
	if selected == "" {
		t.Fatal("expected a selected worktree path")
	}
	if len(dirty) != 0 {
		t.Errorf("expected no dirty worktrees, got %d", len(dirty))
	}

	// Verify the worktree exists.
	if _, err := os.Stat(selected); err != nil {
		t.Fatalf("selected worktree does not exist: %v", err)
	}
}

func TestSelectWorktree_ReusesClean(t *testing.T) {
	repo := initTestRepo(t)

	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// First call creates a worktree.
	selected1, _, err := selectWorktree(repo)
	if err != nil {
		t.Fatal(err)
	}

	// Second call should reuse the same clean worktree.
	selected2, _, err := selectWorktree(repo)
	if err != nil {
		t.Fatal(err)
	}

	if selected1 != selected2 {
		t.Errorf("expected same worktree, got %q and %q", selected1, selected2)
	}
}

func TestSelectWorktree_ReturnsDirty(t *testing.T) {
	repo := initTestRepo(t)

	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Create a worktree and make it dirty.
	selected, _, err := selectWorktree(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(selected, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Next call should return empty selected + dirty list.
	selected2, dirty, err := selectWorktree(repo)
	if err != nil {
		t.Fatal(err)
	}
	if selected2 != "" {
		t.Errorf("expected empty selected, got %q", selected2)
	}
	if len(dirty) != 1 {
		t.Fatalf("expected 1 dirty worktree, got %d", len(dirty))
	}
	if dirty[0].Clean {
		t.Error("dirty worktree should not be Clean")
	}
}

func TestWorktreeBaseDir(t *testing.T) {
	dir := worktreeBaseDir("test-uuid-123")
	if !filepath.IsAbs(dir) {
		t.Errorf("expected absolute path, got %q", dir)
	}
	if filepath.Base(dir) != "test-uuid-123" {
		t.Errorf("expected base dir name 'test-uuid-123', got %q", filepath.Base(dir))
	}
}

func TestNewUUID(t *testing.T) {
	id, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 36 { // 8-4-4-4-12 with dashes
		t.Errorf("expected 36 char UUID, got %d: %q", len(id), id)
	}

	// Should be unique.
	id2, _ := newUUID()
	if id == id2 {
		t.Error("two UUIDs should not be equal")
	}
}
