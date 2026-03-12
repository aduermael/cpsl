package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// WorktreeInfo describes a worktree in the base directory.
type WorktreeInfo struct {
	Path   string
	Branch string
	Clean  bool
	Active bool // locked by a live PID
}

// projectJSON is the on-disk format for .cpsl/project.json.
type projectJSON struct {
	UUID string `json:"uuid"`
}

// ensureProjectID reads or creates a project UUID in <repoRoot>/.cpsl/project.json.
func ensureProjectID(repoRoot string) (string, error) {
	dir := filepath.Join(repoRoot, ".cpsl")
	path := filepath.Join(dir, "project.json")

	data, err := os.ReadFile(path)
	if err == nil {
		var proj projectJSON
		if err := json.Unmarshal(data, &proj); err == nil && proj.UUID != "" {
			return proj.UUID, nil
		}
	}

	// Generate a new UUID v4.
	id, err := newUUID()
	if err != nil {
		return "", fmt.Errorf("generating UUID: %w", err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating .cpsl dir: %w", err)
	}

	proj := projectJSON{UUID: id}
	data, err = json.MarshalIndent(proj, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling project.json: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("writing project.json: %w", err)
	}

	return id, nil
}

// newUUID generates a random UUID v4 string.
func newUUID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	buf[6] = (buf[6] & 0x0f) | 0x40 // version 4
	buf[8] = (buf[8] & 0x3f) | 0x80 // variant 1
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}

// worktreeBaseDir returns ~/.cpsl/worktrees/<projectUUID>/.
func worktreeBaseDir(projectUUID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return filepath.Join(home, ".cpsl", "worktrees", projectUUID)
}

// createWorktree creates a new git worktree in baseDir from repoRoot.
// Returns the path to the new worktree.
func createWorktree(repoRoot, baseDir, name string) (string, error) {
	branchName := "cpsl-" + name
	wtPath := filepath.Join(baseDir, name)

	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return "", fmt.Errorf("creating worktree base dir: %w", err)
	}

	cmd := exec.Command("git", "worktree", "add", "-b", branchName, wtPath)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git worktree add: %s: %w", strings.TrimSpace(string(out)), err)
	}

	return wtPath, nil
}

// listWorktrees scans baseDir for worktree directories and returns info about each.
func listWorktrees(baseDir string) ([]WorktreeInfo, error) {
	entries, err := os.ReadDir(baseDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading worktree dir: %w", err)
	}

	var worktrees []WorktreeInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		wtPath := filepath.Join(baseDir, entry.Name())

		// Check if this looks like a git worktree.
		if _, err := os.Stat(filepath.Join(wtPath, ".git")); err != nil {
			continue
		}

		branch := worktreeBranch(wtPath)
		clean := isWorktreeClean(wtPath)
		locked, _ := isWorktreeLocked(wtPath)

		worktrees = append(worktrees, WorktreeInfo{
			Path:   wtPath,
			Branch: branch,
			Clean:  clean,
			Active: locked,
		})
	}

	return worktrees, nil
}

// worktreeBranch returns the current branch name for a worktree path.
func worktreeBranch(wtPath string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = wtPath
	out, err := cmd.Output()
	if err != nil {
		return filepath.Base(wtPath)
	}
	return strings.TrimSpace(string(out))
}

// isWorktreeClean returns true if the worktree has no uncommitted changes.
func isWorktreeClean(wtPath string) bool {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = wtPath
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == ""
}

const lockFileName = ".cpsl-lock"

// lockWorktree writes a lock file containing the given PID.
func lockWorktree(wtPath string, pid int) error {
	lockPath := filepath.Join(wtPath, lockFileName)
	return os.WriteFile(lockPath, []byte(strconv.Itoa(pid)), 0o644)
}

// unlockWorktree removes the lock file from a worktree.
func unlockWorktree(wtPath string) error {
	lockPath := filepath.Join(wtPath, lockFileName)
	err := os.Remove(lockPath)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// isWorktreeLocked checks if a worktree has a live lock.
// Returns (locked, pid). Stale locks (dead PIDs) are auto-cleaned.
func isWorktreeLocked(wtPath string) (bool, int) {
	lockPath := filepath.Join(wtPath, lockFileName)
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return false, 0
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		// Corrupt lock file — remove it.
		_ = os.Remove(lockPath)
		return false, 0
	}

	if !isProcessAlive(pid) {
		// Stale lock — clean it up.
		_ = os.Remove(lockPath)
		return false, 0
	}

	return true, pid
}

// ensureGitignoreLock ensures .cpsl-lock is listed in the repo's .gitignore.
// If .gitignore exists, it appends .cpsl-lock if missing.
// If .gitignore doesn't exist, it creates one with .cpsl-lock.
func ensureGitignoreLock(repoRoot string) {
	gitignorePath := filepath.Join(repoRoot, ".gitignore")
	entry := ".cpsl-lock"

	data, err := os.ReadFile(gitignorePath)
	if err == nil {
		// .gitignore exists — check if entry is already present.
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == entry {
				return
			}
		}
		// Append entry, ensuring a newline before it.
		suffix := "\n" + entry + "\n"
		if len(data) > 0 && data[len(data)-1] != '\n' {
			suffix = "\n" + suffix
		}
		_ = os.WriteFile(gitignorePath, append(data, []byte(suffix)...), 0o644)
		return
	}

	// No .gitignore — create one.
	_ = os.WriteFile(gitignorePath, []byte(entry+"\n"), 0o644)
}

// isProcessAlive checks if a process with the given PID is running.
func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks if process exists without actually sending a signal.
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

// selectWorktree picks a worktree for this session.
// If a clean, unlocked worktree exists, it returns that path.
// Otherwise it returns empty string and the list of dirty worktrees for user selection.
// If no worktrees exist at all, it creates one.
func selectWorktree(repoRoot string) (selected string, dirty []WorktreeInfo, err error) {
	projectID, err := ensureProjectID(repoRoot)
	if err != nil {
		return "", nil, fmt.Errorf("ensuring project ID: %w", err)
	}

	baseDir := worktreeBaseDir(projectID)
	worktrees, err := listWorktrees(baseDir)
	if err != nil {
		return "", nil, fmt.Errorf("listing worktrees: %w", err)
	}

	// No worktrees exist — create one.
	if len(worktrees) == 0 {
		path, err := createWorktree(repoRoot, baseDir, fmt.Sprintf("auto-%d", time.Now().UnixNano()))
		if err != nil {
			return "", nil, fmt.Errorf("creating initial worktree: %w", err)
		}
		return path, nil, nil
	}

	// Find first clean, unlocked worktree.
	for _, wt := range worktrees {
		if wt.Clean && !wt.Active {
			return wt.Path, nil, nil
		}
	}

	// All worktrees are dirty or locked — collect dirty ones for user prompt.
	for _, wt := range worktrees {
		if !wt.Active {
			dirty = append(dirty, wt)
		}
	}

	return "", dirty, nil
}
