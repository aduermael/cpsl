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
	data, err := os.ReadFile(filepath.Join(repo, ".herm", "project.json"))
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

	wtPath, err := createWorktree(createWorktreeOptions{repoRoot: repo, baseDir: baseDir, name: "test"})
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
	wt1, err := createWorktree(createWorktreeOptions{repoRoot: repo, baseDir: baseDir, name: "clean"})
	if err != nil {
		t.Fatal(err)
	}
	wt2, err := createWorktree(createWorktreeOptions{repoRoot: repo, baseDir: baseDir, name: "dirty"})
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
	if err := lockWorktree(lockWorktreeOptions{wtPath: dir, pid: pid}); err != nil {
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

func TestEnsureGitignoreLock_CreatesFile(t *testing.T) {
	dir := t.TempDir()

	ensureGitignoreLock(dir)

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != ".herm-lock\n" {
		t.Errorf("expected '.herm-lock\\n', got %q", string(data))
	}
}

func TestEnsureGitignoreLock_AppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	existing := "node_modules/\n.env\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	ensureGitignoreLock(dir)

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if content != "node_modules/\n.env\n\n.herm-lock\n" {
		t.Errorf("unexpected content: %q", content)
	}
}

func TestEnsureGitignoreLock_AppendsToExistingNoTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	existing := "node_modules/\n.env"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	ensureGitignoreLock(dir)

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if content != "node_modules/\n.env\n\n.herm-lock\n" {
		t.Errorf("unexpected content: %q", content)
	}
}

func TestEnsureGitignoreLock_AlreadyPresent(t *testing.T) {
	dir := t.TempDir()
	existing := "node_modules/\n.herm-lock\n.env\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	ensureGitignoreLock(dir)

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != existing {
		t.Errorf("file should be unchanged, got %q", string(data))
	}
}

func TestEnsureGitignoreLock_Idempotent(t *testing.T) {
	dir := t.TempDir()

	ensureGitignoreLock(dir)
	ensureGitignoreLock(dir)
	ensureGitignoreLock(dir)

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != ".herm-lock\n" {
		t.Errorf("expected single entry, got %q", string(data))
	}
}

func TestListWorktrees_MixedCleanDirtyActive(t *testing.T) {
	repo := initTestRepo(t)
	baseDir := t.TempDir()

	// Create three worktrees: clean, dirty, and active (locked).
	wtClean, err := createWorktree(createWorktreeOptions{repoRoot: repo, baseDir: baseDir, name: "clean"})
	if err != nil {
		t.Fatal(err)
	}
	wtDirty, err := createWorktree(createWorktreeOptions{repoRoot: repo, baseDir: baseDir, name: "dirty"})
	if err != nil {
		t.Fatal(err)
	}
	wtActive, err := createWorktree(createWorktreeOptions{repoRoot: repo, baseDir: baseDir, name: "active"})
	if err != nil {
		t.Fatal(err)
	}

	// Make dirty worktree dirty.
	if err := os.WriteFile(filepath.Join(wtDirty, "untracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Lock the active worktree with current PID (alive).
	if err := lockWorktree(lockWorktreeOptions{wtPath: wtActive, pid: os.Getpid()}); err != nil {
		t.Fatal(err)
	}

	wts, err := listWorktrees(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 3 {
		t.Fatalf("expected 3 worktrees, got %d", len(wts))
	}

	byPath := map[string]WorktreeInfo{}
	for _, wt := range wts {
		byPath[wt.Path] = wt
	}

	// Clean worktree: clean=true, active=false
	if info, ok := byPath[wtClean]; !ok {
		t.Error("missing clean worktree")
	} else {
		if !info.Clean {
			t.Error("clean worktree should be Clean")
		}
		if info.Active {
			t.Error("clean worktree should not be Active")
		}
		if info.Branch == "" {
			t.Error("clean worktree should have a branch name")
		}
	}

	// Dirty worktree: clean=false, active=false
	if info, ok := byPath[wtDirty]; !ok {
		t.Error("missing dirty worktree")
	} else {
		if info.Clean {
			t.Error("dirty worktree should not be Clean")
		}
		if info.Active {
			t.Error("dirty worktree should not be Active")
		}
	}

	// Active worktree: active=true
	if info, ok := byPath[wtActive]; !ok {
		t.Error("missing active worktree")
	} else {
		if !info.Active {
			t.Error("active worktree should be Active")
		}
	}
}

func TestListWorktrees_SkipsNonGitDirs(t *testing.T) {
	baseDir := t.TempDir()

	// Create a regular directory (not a git worktree).
	if err := os.MkdirAll(filepath.Join(baseDir, "not-a-worktree"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a file (not a directory).
	if err := os.WriteFile(filepath.Join(baseDir, "some-file"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	wts, err := listWorktrees(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 0 {
		t.Errorf("expected 0 worktrees (non-git dirs skipped), got %d", len(wts))
	}
}

func TestCreateWorktree_DuplicateName(t *testing.T) {
	repo := initTestRepo(t)
	baseDir := t.TempDir()

	// Create the first worktree.
	_, err := createWorktree(createWorktreeOptions{repoRoot: repo, baseDir: baseDir, name: "dup"})
	if err != nil {
		t.Fatal(err)
	}

	// Creating a second worktree with the same name should fail
	// because git would try to create a branch that already exists.
	_, err = createWorktree(createWorktreeOptions{repoRoot: repo, baseDir: baseDir, name: "dup"})
	if err == nil {
		t.Fatal("expected error when creating duplicate worktree")
	}
}

func TestCreateWorktree_InvalidRepoRoot(t *testing.T) {
	baseDir := t.TempDir()

	// repoRoot is not a git repository.
	_, err := createWorktree(createWorktreeOptions{repoRoot: "/nonexistent/repo/path", baseDir: baseDir, name: "test"})
	if err == nil {
		t.Fatal("expected error for invalid repo root")
	}
}

func TestCorruptLockFile(t *testing.T) {
	dir := t.TempDir()

	// Write non-numeric content to the lock file.
	lockPath := filepath.Join(dir, lockFileName)
	if err := os.WriteFile(lockPath, []byte("not-a-pid"), 0o644); err != nil {
		t.Fatal(err)
	}

	locked, pid := isWorktreeLocked(dir)
	if locked {
		t.Error("expected corrupt lock to be treated as unlocked")
	}
	if pid != 0 {
		t.Errorf("expected pid=0 for corrupt lock, got %d", pid)
	}

	// Corrupt lock file should be removed.
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Error("expected corrupt lock file to be removed")
	}
}

func TestUnlockWorktree_AlreadyUnlocked(t *testing.T) {
	dir := t.TempDir()

	// Unlock on a directory with no lock file should not error.
	if err := unlockWorktree(dir); err != nil {
		t.Fatalf("unlockWorktree on unlocked dir: %v", err)
	}
}

func TestSelectWorktree_SkipsLockedWorktree(t *testing.T) {
	repo := initTestRepo(t)

	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Create a worktree via selectWorktree.
	selected, _, err := selectWorktree(repo)
	if err != nil {
		t.Fatal(err)
	}

	// Lock it with the current PID.
	if err := lockWorktree(lockWorktreeOptions{wtPath: selected, pid: os.Getpid()}); err != nil {
		t.Fatal(err)
	}

	// Next selectWorktree call: the worktree is clean but locked (Active).
	// selectWorktree only selects worktrees that are Clean && !Active,
	// so it should return empty selected and no dirty (locked ones are excluded from dirty).
	selected2, dirty, err := selectWorktree(repo)
	if err != nil {
		t.Fatal(err)
	}
	if selected2 != "" {
		t.Errorf("expected empty selected when only worktree is locked, got %q", selected2)
	}
	// Dirty list should be empty since the only worktree is Active (locked).
	if len(dirty) != 0 {
		t.Errorf("expected 0 dirty worktrees (locked ones excluded), got %d", len(dirty))
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
