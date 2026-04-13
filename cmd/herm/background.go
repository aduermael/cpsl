// background.go contains async initialization commands and background task
// functions that run in goroutines during startup and operation, along with
// the message types they produce.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"langdag.com/langdag"
)

// ─── Async message types ───

type sweScoresMsg struct {
	scores map[string]float64
	err    error
}

type ctrlCExpiredMsg struct{}
type escExpiredMsg struct{}

type containerReadyMsg struct {
	client       *ContainerClient
	worktreePath string
	imageName    string
}

type containerErrMsg struct {
	err error
}

type containerStatusMsg struct {
	text string
}

type statusInfo struct {
	Branch       string
	PRNumber     int
	WorktreeName string
	ActiveCount  int
	TotalCount   int
	DiffAdd      int
	DiffDel      int
	Behind       int // commits on remote tracking branch missing locally
	Ahead        int // local commits missing on remote tracking branch
	HasUpstream  bool
}

type statusInfoMsg struct {
	info statusInfo
}

type commitInfoMsg struct {
	branch      string
	behind      int
	ahead       int
	hasUpstream bool
	diffAdd     int
	diffDel     int
}

type worktreeListMsg struct {
	items []WorktreeInfo
	err   error
}

type branchListMsg struct {
	items         []string
	currentBranch string
	err           error
}

type branchCheckoutMsg struct {
	branch string
	err    error
}

type workspaceMsg struct {
	worktreePath string
	repoRoot     string
}

type langdagReadyMsg struct {
	client   *langdag.Client
	provider string
	err      error
}

type catalogMsg struct {
	catalog *langdag.ModelCatalog
}

type resizeMsg struct{}

type toolTimerTickMsg struct{}

type agentTickMsg struct{}

// projectSnapshot holds a lightweight project context gathered at startup.
type projectSnapshot struct {
	TopLevel      string // ls -1 of worktree root
	RecentCommits string // git log --oneline -10
	GitStatus     string // git status --short
}

type projectSnapshotMsg struct {
	snapshot projectSnapshot
}

type updateAvailableMsg struct {
	version string
	err     error
}

type updateCompleteMsg struct {
	err error
}

type ollamaModelsMsg struct {
	models []ModelDef
}

type openRouterModelsMsg struct {
	models []ModelDef
}

// openPickerMsg is sent after an async Ollama fetch completes to open the
// model picker in the config editor with the freshly fetched model list.
type openPickerMsg struct {
	getCurrentID func() string
	onSelect     func(string)
}

// ─── Async init commands ───

func resolveWorkspaceCmd(cfg Config) workspaceMsg {
	cwd, _ := os.Getwd()
	repoRoot := gitRepoRoot()
	if repoRoot != "" {
		ensureGitignoreLock(repoRoot)
	}
	return workspaceMsg{worktreePath: cwd, repoRoot: repoRoot}
}

func bootContainerCmd(workspace string, sessionID string, ch chan<- any) {
	ch <- containerStatusMsg{text: "checking docker…"}

	client := NewContainerClient(ContainerConfig{Image: defaultContainerImage})

	if err := client.CheckDocker(); err != nil {
		if cerr, ok := err.(*ContainerError); ok && cerr.Code == ErrDockerNotFound {
			ch <- containerStatusMsg{text: "docker not installed"}
		} else {
			ch <- containerStatusMsg{text: "docker not running"}
		}
		ch <- containerErrMsg{err: err}
		return
	}

	// Build from .herm/Dockerfile (write base template if none exists).
	imageName := buildContainerImage(workspace, ch)
	if imageName != "" {
		client.mu.Lock()
		client.config.Image = imageName
		client.mu.Unlock()
	}

	// Ensure the image is available locally (pull if needed).
	finalImage := client.config.Image
	if err := ensureImageLocal(finalImage, ch); err != nil {
		ch <- containerStatusMsg{text: "image pull failed"}
		ch <- containerErrMsg{err: fmt.Errorf("pulling %s: %w", finalImage, err)}
		return
	}

	ch <- containerStatusMsg{text: "starting…"}

	attachDir := filepath.Join(workspace, ".herm", "attachments", sessionID)
	_ = os.MkdirAll(attachDir, 0o755)

	cacheDir := filepath.Join(workspace, ".herm", "cache")
	_ = os.MkdirAll(cacheDir, 0o755)

	mounts := []MountSpec{
		{Source: workspace, Destination: workspace, ReadOnly: false},
		{Source: attachDir, Destination: "/attachments", ReadOnly: true},
		{Source: cacheDir, Destination: "/cache", ReadOnly: false},
	}

	if err := client.Start(workspace, mounts); err != nil {
		ch <- containerStatusMsg{text: "start failed"}
		ch <- containerErrMsg{err: fmt.Errorf("starting container: %w", err)}
		return
	}

	ch <- containerReadyMsg{client: client, worktreePath: workspace, imageName: imageName}
}

// ensureImageLocal checks whether a Docker image exists locally. If not, it
// pulls it from the registry. Status updates are sent via ch. Returns nil on
// success, or the pull error if the image cannot be obtained.
func ensureImageLocal(image string, ch chan<- any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if dockerCommand(ctx, "docker", "image", "inspect", image).Run() == nil {
		return nil // already available
	}

	ch <- containerStatusMsg{text: "pulling image…"}

	pullCtx, pullCancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer pullCancel()

	pullCmd := dockerCommand(pullCtx, "docker", "pull", image)
	var stderr bytes.Buffer
	pullCmd.Stderr = &stderr

	if err := pullCmd.Run(); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

// buildContainerImage builds a Docker image from .herm/Dockerfile in the workspace.
// If no Dockerfile exists, or it matches the embedded base template, the build is
// skipped and the caller uses the default image (aduermael/herm:<tag>) directly.
// Image tag is deterministic: herm-<projectID[:8]>:<sha256[:12]> based on Dockerfile content.
// If the image already exists (docker image inspect), the build is skipped.
// Returns the built image name, or empty string on failure (caller falls back to raw image).
func buildContainerImage(workspace string, ch chan<- any) string {
	hermDir := filepath.Join(workspace, ".herm")
	dockerfilePath := filepath.Join(hermDir, "Dockerfile")

	// No .herm/Dockerfile — use the default image directly. No build needed.
	if _, err := os.Stat(dockerfilePath); os.IsNotExist(err) {
		return ""
	}

	// Read Dockerfile content.
	content, err := os.ReadFile(dockerfilePath)
	if err != nil {
		return ""
	}

	// If the Dockerfile matches the embedded base template, skip the build.
	// The default image already has everything.
	if string(content) == BaseDockerfile {
		return ""
	}

	// If the Dockerfile uses an outdated or wrong base image, back it up
	// so devenv can later help the user migrate their customizations.
	// This is expected when the herm base image is updated.
	if !dockerfileUsesHermBase(string(content)) {
		backupPath := filepath.Join(hermDir, "Dockerfile.old")
		_ = os.Rename(dockerfilePath, backupPath)
		ch <- containerStatusMsg{text: "migrating to new base image…"}
		return ""
	}

	// Resolve __HERM_VERSION__ placeholder to actual version.
	resolved := resolveDockerfile(string(content))

	// Hash the resolved content so version bumps trigger rebuilds.
	hash := sha256.Sum256([]byte(resolved))
	hashStr := hex.EncodeToString(hash[:])[:12]

	// Derive image name from project ID + content hash.
	imageName := "herm-local:" + hashStr
	if repoRoot := gitRepoRoot(); repoRoot != "" {
		if projectID, err := ensureProjectID(repoRoot); err == nil && len(projectID) >= 8 {
			imageName = "herm-" + projectID[:8] + ":" + hashStr
		}
	}

	// Check if image already exists — skip build if so (cache hit).
	inspectCtx, inspectCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer inspectCancel()
	inspectCmd := dockerCommand(inspectCtx, "docker", "image", "inspect", imageName)
	if inspectCmd.Run() == nil {
		return imageName
	}

	ch <- containerStatusMsg{text: "building image…"}

	// Write resolved Dockerfile to a temp file for docker build.
	tmpFile, tmpErr := os.CreateTemp("", "herm-dockerfile-*")
	if tmpErr != nil {
		return ""
	}
	defer os.Remove(tmpFile.Name())
	if _, writeErr := tmpFile.WriteString(resolved); writeErr != nil {
		tmpFile.Close()
		return ""
	}
	tmpFile.Close()

	buildCtx, buildCancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer buildCancel()

	buildCmd := dockerCommand(buildCtx, "docker", "build",
		"-t", imageName,
		"-f", tmpFile.Name(),
		workspace,
	)
	var buildStderr bytes.Buffer
	buildCmd.Stderr = &buildStderr

	if err := buildCmd.Run(); err != nil {
		// Build failed — fall back to raw default image.
		ch <- containerStatusMsg{text: "build failed, using default image"}
		return ""
	}

	return imageName
}

// dockerfileUsesHermBase checks if a Dockerfile's first FROM instruction uses
// aduermael/herm:__HERM_VERSION__ as the base image. Dockerfiles with hardcoded
// version tags (e.g. aduermael/herm:0.1) are rejected so the migration flow
// backs them up and the user can re-create with the placeholder.
func dockerfileUsesHermBase(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, "FROM ") {
			return strings.Contains(line, "aduermael/herm:__HERM_VERSION__")
		}
		// First non-comment, non-empty line isn't FROM — invalid but not our problem.
		break
	}
	return false
}

func fetchStatusCmd(worktreePath string) statusInfoMsg {
	var info statusInfo

	info.Branch = worktreeBranch(worktreePath)

	// ahead/behind relative to upstream tracking branch
	revListCmd := exec.Command("git", "rev-list", "--count", "--left-right", "@{upstream}...HEAD")
	revListCmd.Dir = worktreePath
	if out, err := revListCmd.Output(); err == nil {
		parts := strings.Split(strings.TrimSpace(string(out)), "\t")
		if len(parts) == 2 {
			info.HasUpstream = true
			if n, err := strconv.Atoi(parts[0]); err == nil {
				info.Behind = n
			}
			if n, err := strconv.Atoi(parts[1]); err == nil {
				info.Ahead = n
			}
		}
	}

	ghCmd := exec.Command("gh", "pr", "view", "--json", "number", "-q", ".number")
	ghCmd.Dir = worktreePath
	if out, err := ghCmd.Output(); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil {
			info.PRNumber = n
		}
	}

	diffCmd := exec.Command("git", "diff", "--shortstat", "HEAD")
	diffCmd.Dir = worktreePath
	if out, err := diffCmd.Output(); err == nil {
		line := strings.TrimSpace(string(out))
		if re := regexp.MustCompile(`(\d+) insertion`); re.MatchString(line) {
			if n, err := strconv.Atoi(re.FindStringSubmatch(line)[1]); err == nil {
				info.DiffAdd = n
			}
		}
		if re := regexp.MustCompile(`(\d+) deletion`); re.MatchString(line) {
			if n, err := strconv.Atoi(re.FindStringSubmatch(line)[1]); err == nil {
				info.DiffDel = n
			}
		}
	}

	info.WorktreeName = filepath.Base(worktreePath)

	repoRoot := gitRepoRoot()
	if repoRoot != "" {
		if projectID, err := ensureProjectID(repoRoot); err == nil {
			baseDir := worktreeBaseDir(projectID)
			if wts, err := listWorktrees(baseDir); err == nil {
				info.TotalCount = len(wts)
				for _, wt := range wts {
					if wt.Active {
						info.ActiveCount++
					}
				}
			}
		}
	}

	return statusInfoMsg{info: info}
}

func fetchCommitInfo(worktreePath string) commitInfoMsg {
	var msg commitInfoMsg
	msg.branch = worktreeBranch(worktreePath)
	cmd := exec.Command("git", "rev-list", "--count", "--left-right", "@{upstream}...HEAD")
	cmd.Dir = worktreePath
	if out, err := cmd.Output(); err == nil {
		parts := strings.Split(strings.TrimSpace(string(out)), "	")
		if len(parts) == 2 {
			msg.hasUpstream = true
			if n, err := strconv.Atoi(parts[0]); err == nil {
				msg.behind = n
			}
			if n, err := strconv.Atoi(parts[1]); err == nil {
				msg.ahead = n
			}
		}
	}

	diffCmd := exec.Command("git", "diff", "--shortstat", "HEAD")
	diffCmd.Dir = worktreePath
	if out, err := diffCmd.Output(); err == nil {
		line := strings.TrimSpace(string(out))
		if re := regexp.MustCompile(`(\d+) insertion`); re.MatchString(line) {
			if n, err := strconv.Atoi(re.FindStringSubmatch(line)[1]); err == nil {
				msg.diffAdd = n
			}
		}
		if re := regexp.MustCompile(`(\d+) deletion`); re.MatchString(line) {
			if n, err := strconv.Atoi(re.FindStringSubmatch(line)[1]); err == nil {
				msg.diffDel = n
			}
		}
	}
	return msg
}

// buildProjectTree creates a two-level tree view of the project directory with
// smart truncation. Important files (README, go.mod, package.json, Makefile,
// Dockerfile) are preserved when truncating top-level entries. Hidden entries
// (starting with ".") are excluded.
func buildProjectTree(rootPath string, maxTopLevel, maxPerSubdir int) string {
	if maxTopLevel <= 0 {
		maxTopLevel = 20
	}
	if maxPerSubdir <= 0 {
		maxPerSubdir = 8
	}

	entries, err := os.ReadDir(rootPath)
	if err != nil {
		return ""
	}

	// Filter out hidden entries.
	var visible []os.DirEntry
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			visible = append(visible, e)
		}
	}

	importantNames := map[string]bool{
		"readme": true, "readme.md": true, "readme.txt": true, "readme.rst": true,
		"go.mod": true, "go.sum": true, "package.json": true,
		"makefile": true, "dockerfile": true,
	}
	isImportant := func(name string) bool {
		return importantNames[strings.ToLower(name)]
	}

	// If we need to truncate, keep important files and fill remaining slots.
	var selected []os.DirEntry
	truncated := 0
	if len(visible) > maxTopLevel {
		var imp, other []os.DirEntry
		for _, e := range visible {
			if isImportant(e.Name()) {
				imp = append(imp, e)
			} else {
				other = append(other, e)
			}
		}
		if len(imp) >= maxTopLevel {
			selected = imp[:maxTopLevel]
			truncated = len(visible) - maxTopLevel
		} else {
			selected = append(selected, imp...)
			remaining := maxTopLevel - len(imp)
			if len(other) > remaining {
				selected = append(selected, other[:remaining]...)
				truncated = len(other) - remaining
			} else {
				selected = append(selected, other...)
			}
		}
	} else {
		selected = visible
	}

	var buf strings.Builder
	for _, e := range selected {
		name := e.Name()
		if e.IsDir() {
			buf.WriteString(name + "/\n")
			subEntries, err := os.ReadDir(filepath.Join(rootPath, name))
			if err != nil {
				continue
			}
			var visibleSub []os.DirEntry
			for _, se := range subEntries {
				if !strings.HasPrefix(se.Name(), ".") {
					visibleSub = append(visibleSub, se)
				}
			}
			if len(visibleSub) > maxPerSubdir {
				for _, se := range visibleSub[:maxPerSubdir] {
					subName := se.Name()
					if se.IsDir() {
						subName += "/"
					}
					buf.WriteString("  " + subName + "\n")
				}
				buf.WriteString(fmt.Sprintf("  +%d more\n", len(visibleSub)-maxPerSubdir))
			} else {
				for _, se := range visibleSub {
					subName := se.Name()
					if se.IsDir() {
						subName += "/"
					}
					buf.WriteString("  " + subName + "\n")
				}
			}
		} else {
			buf.WriteString(name + "\n")
		}
	}
	if truncated > 0 {
		buf.WriteString(fmt.Sprintf("+%d more\n", truncated))
	}

	return strings.TrimSpace(buf.String())
}

// fetchProjectSnapshot gathers a lightweight project snapshot for the system prompt.
// Each sub-command has a 2s timeout and fails gracefully to empty string.
func fetchProjectSnapshot(worktreePath string) projectSnapshotMsg {
	var snap projectSnapshot

	type result struct {
		field string
		value string
	}

	ch := make(chan result, 3)

	// Two-level tree view of project root.
	go func() {
		val := buildProjectTree(worktreePath, 20, 8)
		ch <- result{"ls", val}
	}()

	// git log --oneline -10
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "log", "--oneline", "-10")
		cmd.Dir = worktreePath
		out, err := cmd.Output()
		val := ""
		if err == nil {
			val = strings.TrimSpace(string(out))
		}
		ch <- result{"log", val}
	}()

	// git status --short
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "status", "--short")
		cmd.Dir = worktreePath
		out, err := cmd.Output()
		val := ""
		if err == nil {
			val = strings.TrimSpace(string(out))
		}
		ch <- result{"status", val}
	}()

	for i := 0; i < 3; i++ {
		r := <-ch
		switch r.field {
		case "ls":
			snap.TopLevel = r.value
		case "log":
			snap.RecentCommits = r.value
		case "status":
			snap.GitStatus = r.value
		}
	}

	return projectSnapshotMsg{snapshot: snap}
}

func fetchSWEScoresCmd() sweScoresMsg {
	scores, err := fetchSWEScores()
	return sweScoresMsg{scores: scores, err: err}
}

func fetchOllamaModelsCmd(baseURL string) ollamaModelsMsg {
	return ollamaModelsMsg{models: fetchOllamaModels(baseURL)}
}

func fetchOpenRouterModelsCmd(apiKey string) openRouterModelsMsg {
	return openRouterModelsMsg{models: fetchOpenRouterModels(apiKey)}
}

// checkForUpdate queries the GitHub API for the latest release and compares
// it against the current binary version.
func checkForUpdate(currentVersion string) updateAvailableMsg {
	if currentVersion == "dev" {
		return updateAvailableMsg{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/aduermael/herm/releases/latest", nil)
	if err != nil {
		return updateAvailableMsg{err: err}
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return updateAvailableMsg{err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return updateAvailableMsg{err: fmt.Errorf("GitHub API returned %d", resp.StatusCode)}
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return updateAvailableMsg{err: err}
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	current := strings.TrimPrefix(currentVersion, "v")
	if latest != "" && latest != current {
		return updateAvailableMsg{version: latest}
	}
	return updateAvailableMsg{}
}

// performUpdate downloads and installs the specified version of herm,
// replacing the current binary in-place.
func performUpdate(version string) updateCompleteMsg {
	exePath, err := os.Executable()
	if err != nil {
		return updateCompleteMsg{err: fmt.Errorf("cannot determine executable path: %w", err)}
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return updateCompleteMsg{err: fmt.Errorf("cannot resolve executable path: %w", err)}
	}

	osName := runtime.GOOS
	archName := runtime.GOARCH

	url := fmt.Sprintf("https://github.com/aduermael/herm/releases/download/v%s/herm_%s_%s_%s.tar.gz",
		version, version, osName, archName)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return updateCompleteMsg{err: err}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return updateCompleteMsg{err: fmt.Errorf("download failed: %w", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return updateCompleteMsg{err: fmt.Errorf("download returned HTTP %d", resp.StatusCode)}
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return updateCompleteMsg{err: fmt.Errorf("gzip error: %w", err)}
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var binData []byte
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return updateCompleteMsg{err: fmt.Errorf("tar error: %w", err)}
		}
		if filepath.Base(hdr.Name) == "herm" && !hdr.FileInfo().IsDir() {
			binData, err = io.ReadAll(tr)
			if err != nil {
				return updateCompleteMsg{err: fmt.Errorf("reading binary from archive: %w", err)}
			}
			break
		}
	}
	if binData == nil {
		return updateCompleteMsg{err: fmt.Errorf("binary not found in archive")}
	}

	// Write to a temp file in the same directory, then atomic rename
	dir := filepath.Dir(exePath)
	tmp, err := os.CreateTemp(dir, "herm-update-*")
	if err != nil {
		return updateCompleteMsg{err: fmt.Errorf("creating temp file: %w", err)}
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(binData); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return updateCompleteMsg{err: fmt.Errorf("writing temp file: %w", err)}
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return updateCompleteMsg{err: fmt.Errorf("chmod: %w", err)}
	}
	tmp.Close()

	if err := os.Rename(tmpPath, exePath); err != nil {
		os.Remove(tmpPath)
		return updateCompleteMsg{err: fmt.Errorf("replacing binary: %w", err)}
	}

	return updateCompleteMsg{}
}
