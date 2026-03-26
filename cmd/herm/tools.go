// tools.go defines the agent tool implementations (BashTool, GitTool, DevEnvTool)
// that execute commands in containers or on the host.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"langdag.com/langdag/types"
)

// Output truncation limits for BashTool.
const (
	bashMaxLines    = 80
	bashMaxBytes    = 12 * 1024 // 12KB
	truncHeadLines  = 20        // lines to keep from the beginning
	truncTailLines  = 60        // lines to keep from the end
)

// BashTool executes commands inside the Docker container via ContainerClient.
type BashTool struct {
	container *ContainerClient
	timeout   int // default timeout in seconds
}

// NewBashTool creates a BashTool with the given container client and default timeout.
func NewBashTool(container *ContainerClient, timeout int) *BashTool {
	if timeout <= 0 {
		timeout = 120
	}
	return &BashTool{container: container, timeout: timeout}
}

func (t *BashTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        "bash",
		Description: getToolDescription("bash", "Run a shell command in the dev container (project mounted at /workspace). Output is truncated to 80 lines / 12KB (head+tail)."),
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"command": {
					"type": "string",
					"description": "The bash command to execute in /workspace"
				},
				"timeout": {
					"type": "integer",
					"description": "Timeout in seconds (default: 120, max: 600)"
				}
			},
			"required": ["command"]
		}`),
	}
}

type bashInput struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

func (t *BashTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in bashInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid bash input: %w", err)
	}
	if in.Command == "" {
		return "", fmt.Errorf("command is required")
	}

	timeout := t.timeout
	if in.Timeout > 0 {
		timeout = in.Timeout
	}
	if timeout > 600 {
		timeout = 600
	}

	// LLMs (notably Gemini) sometimes HTML-encode characters in tool args
	// (e.g. && → &amp;&amp;). Unescape before execution.
	command := html.UnescapeString(in.Command)

	result, err := t.container.Exec(command, timeout)
	if err != nil {
		return "", err
	}

	output := result.Stdout + result.Stderr
	output = truncateOutput(output)

	if result.ExitCode != 0 {
		return fmt.Sprintf("exit code: %d\n%s", result.ExitCode, output), nil
	}
	return output, nil
}

func (t *BashTool) RequiresApproval(_ json.RawMessage) bool {
	return false
}

func (t *BashTool) HostTool() bool { return false }

// truncateOutput trims output to bashMaxLines lines and bashMaxBytes bytes using
// a head+tail strategy: keep the first truncHeadLines and last truncTailLines,
// inserting a "[... N lines omitted ...]" separator in between.
func truncateOutput(s string) string {
	if len(s) <= bashMaxBytes && strings.Count(s, "\n") <= bashMaxLines {
		return s
	}

	// Byte-limit first: keep the last bashMaxBytes to avoid splitting mid-line
	// at the beginning, then line-truncate the result.
	if len(s) > bashMaxBytes {
		s = s[len(s)-bashMaxBytes:]
	}

	lines := strings.Split(s, "\n")
	if len(lines) <= bashMaxLines {
		// Byte truncation alone was enough; we lost content from the front.
		return fmt.Sprintf("[output truncated, showing last %d lines]\n%s", len(lines), s)
	}

	// Head+tail line truncation.
	omitted := len(lines) - truncHeadLines - truncTailLines
	head := strings.Join(lines[:truncHeadLines], "\n")
	tail := strings.Join(lines[len(lines)-truncTailLines:], "\n")
	return fmt.Sprintf("%s\n[... %d lines omitted ...]\n%s", head, omitted, tail)
}

// GitTool executes git commands on the host in the worktree directory.
type GitTool struct {
	workDir  string
	coAuthor bool
}

// allowedGitSubcommands is the set of git subcommands the agent may run.
var allowedGitSubcommands = map[string]bool{
	"status":   true,
	"diff":     true,
	"log":      true,
	"show":     true,
	"branch":   true,
	"checkout": true,
	"add":      true,
	"commit":   true,
	"pull":     true,
	"push":     true,
	"fetch":    true,
	"stash":    true,
	"rebase":   true,
	"merge":    true,
	"reset":    true,
	"tag":      true,
}

// NewGitTool creates a GitTool that runs in the given worktree directory.
func NewGitTool(workDir string, coAuthor bool) *GitTool {
	return &GitTool{workDir: workDir, coAuthor: coAuthor}
}

func (t *GitTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        "git",
		Description: getToolDescription("git", "Run git commands on the host in the project worktree. Required for remote operations (push/pull/fetch) since only the host has SSH keys and credentials."),
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"subcommand": {
					"type": "string",
					"description": "Git subcommand to run (e.g. status, diff, add, commit)"
				},
				"args": {
					"type": "array",
					"items": {"type": "string"},
					"description": "Arguments for the subcommand (e.g. [\"-m\", \"fix bug\"])"
				}
			},
			"required": ["subcommand"]
		}`),
	}
}

type gitInput struct {
	Subcommand string   `json:"subcommand"`
	Args       []string `json:"args,omitempty"`
}

func (t *GitTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in gitInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid git input: %w", err)
	}
	if in.Subcommand == "" {
		return "", fmt.Errorf("subcommand is required")
	}

	if !allowedGitSubcommands[in.Subcommand] {
		return "", fmt.Errorf("git subcommand %q is not allowed", in.Subcommand)
	}

	// Append co-author trailer for message-based commits.
	if in.Subcommand == "commit" && t.coAuthor {
		for _, a := range in.Args {
			if a == "-m" {
				in.Args = append(in.Args, "--trailer", "Co-authored-by: herm <herm@hermagent.com>")
				break
			}
		}
	}
	args := append([]string{in.Subcommand}, in.Args...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = t.workDir

	out, err := cmd.CombinedOutput()
	output := string(out)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			msg := fmt.Sprintf("exit code: %d\n%s", exitErr.ExitCode(), output)
			if hint := gitCredentialHint(output); hint != "" {
				msg += "\n" + hint
			}
			return msg, nil
		}
		return "", fmt.Errorf("git exec: %w", err)
	}

	return output, nil
}

func (t *GitTool) RequiresApproval(input json.RawMessage) bool {
	var in gitInput
	if err := json.Unmarshal(input, &in); err != nil {
		return false
	}
	if in.Subcommand == "push" {
		return true
	}
	// Gate on destructive force operations.
	if gitArgsContainForce(in.Args) {
		return true
	}
	// reset --hard is destructive.
	if in.Subcommand == "reset" {
		for _, arg := range in.Args {
			if arg == "--hard" {
				return true
			}
		}
	}
	return false
}

func (t *GitTool) HostTool() bool { return true }

// gitArgsContainForce returns true if args contain --force or -f.
func gitArgsContainForce(args []string) bool {
	for _, arg := range args {
		if arg == "--force" || arg == "-f" || arg == "--force-with-lease" {
			return true
		}
	}
	return false
}

// gitCredentialHint checks git output for common auth/credential error patterns
// and returns a helpful hint, or empty string if no pattern matches.
func gitCredentialHint(output string) string {
	lower := strings.ToLower(output)
	patterns := []string{
		"permission denied (publickey)",
		"could not read from remote repository",
		"authentication failed",
		"fatal: could not read username",
		"support for password authentication was removed",
		"host key verification failed",
		"connection refused",
		"connection timed out",
	}
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return "Hint: This may be a credentials/SSH issue on the host. Inform the user so they can check their SSH keys or git credentials."
		}
	}
	return ""
}

// DevEnvTool allows the agent to read, write, and build a custom Dockerfile
// in the project's .herm/ directory, then hot-swap the running container.
type DevEnvTool struct {
	container *ContainerClient
	hermDir   string // host path to .herm/ directory (contains Dockerfile)
	workspace string // host workspace path (docker build context)
	mounts    []MountSpec
	projectID string                 // first 8 chars used in image tags
	onRebuild func(imageName string) // called after successful rebuild
	onStatus  func(text string)      // called with container status updates
}

// NewDevEnvTool creates a DevEnvTool with the given container client and paths.
func NewDevEnvTool(container *ContainerClient, hermDir, workspace string, mounts []MountSpec, projectID string, onRebuild func(string), onStatus func(string)) *DevEnvTool {
	return &DevEnvTool{
		container: container,
		hermDir:   hermDir,
		workspace: workspace,
		mounts:    mounts,
		projectID: projectID,
		onRebuild: onRebuild,
		onStatus:  onStatus,
	}
}

func (t *DevEnvTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        "devenv",
		Description: getToolDescription("devenv", "Manage the single dev container Dockerfile at .herm/Dockerfile. The built image replaces the running container and persists across sessions."),
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"enum": ["read", "write", "build"],
					"description": "read: view the current Dockerfile (and note any other .herm/*.Dockerfile files), write: replace the Dockerfile content entirely, build: build the image and hot-swap the running container"
				},
				"content": {
					"type": "string",
					"description": "Full Dockerfile content (required for 'write'). Must include ALL previously installed tools — this replaces the entire file."
				}
			},
			"required": ["action"]
		}`),
	}
}

type devenvInput struct {
	Action  string `json:"action"`
	Name    string `json:"name,omitempty"` // ignored; kept for JSON compat during transition
	Content string `json:"content,omitempty"`
}

func (t *DevEnvTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in devenvInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid devenv input: %w", err)
	}

	switch in.Action {
	case "read":
		return t.readDockerfile()
	case "write":
		return t.writeDockerfile(in.Content)
	case "build":
		return t.buildAndReplace()
	default:
		return "", fmt.Errorf("unknown action %q: must be read, write, or build", in.Action)
	}
}

func (t *DevEnvTool) RequiresApproval(_ json.RawMessage) bool {
	return false
}

func (t *DevEnvTool) HostTool() bool { return false }

// dockerfilePath returns the canonical path to .herm/Dockerfile.
func (t *DevEnvTool) dockerfilePath() string {
	return filepath.Join(t.hermDir, "Dockerfile")
}

// dockerfileGuidelines is appended to devenv read output so the agent gets
// installation examples and common mistakes exactly when editing the Dockerfile.
// This content was moved out of the tool description to save tokens on every request.
const dockerfileGuidelines = `
## Dockerfile guidelines

**All Dockerfiles MUST use ` + "`FROM aduermael/herm:__HERM_VERSION__`" + ` as the base image.** The base image includes git, ripgrep, tree, python3, and the herm file tools. Do NOT use other base images or hardcode version tags.

**Dockerfile rules that prevent build failures:**
- Download official release tarballs rather than curl-pipe-to-bash setup scripts (NodeSource, rustup.sh, etc).
- Combine related RUN steps: ` + "`apt-get update && apt-get install -y ... && rm -rf /var/lib/apt/lists/*`" + `. Never split update and install across layers.
- Pin specific versions for reproducibility. Set WORKDIR /workspace.

**Installing runtimes — download tarballs, not setup scripts:**

Go:
` + "```dockerfile" + `
ENV GOLANG_VERSION=1.22.5
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget \
    && wget -qO go.tar.gz "https://go.dev/dl/go${GOLANG_VERSION}.linux-amd64.tar.gz" \
    && tar -C /usr/local -xzf go.tar.gz && rm go.tar.gz \
    && rm -rf /var/lib/apt/lists/*
ENV PATH="/usr/local/go/bin:/root/go/bin:$PATH"
` + "```" + `

Node.js:
` + "```dockerfile" + `
ENV NODE_VERSION=22.14.0
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget xz-utils \
    && wget -qO node.tar.xz "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-x64.tar.xz" \
    && tar -xJf node.tar.xz -C /usr/local --strip-components=1 && rm node.tar.xz \
    && rm -rf /var/lib/apt/lists/*
` + "```" + `

Python (already in base image — only add pip/venv if needed):
` + "```dockerfile" + `
RUN apt-get update && apt-get install -y --no-install-recommends python3-pip python3-venv \
    && rm -rf /var/lib/apt/lists/*
` + "```" + `

**Common mistakes that cause build failures:**
1. Missing ` + "`apt-get update`" + ` in same RUN layer as install — the base image has no cached package lists.
2. Wrong Debian package names — use ` + "`apt-cache search`" + ` in the running container. Common: build-essential (not gcc+make), python3-dev (not python-dev), libssl-dev (not openssl-dev).
3. Download URLs that 404 — always verify and pin exact versions.
4. Missing -y flag on apt-get install.
5. curl-pipe-to-bash setup scripts — use tarballs instead.

**When a build fails**, read the full error. Fix only the failing step — don't rewrite the whole Dockerfile.`

func (t *DevEnvTool) readDockerfile() (string, error) {
	data, err := os.ReadFile(t.dockerfilePath())
	if err != nil {
		if os.IsNotExist(err) {
			msg := "No .herm/Dockerfile exists yet. Use the 'write' action to create one."

			// Surface backed-up Dockerfile from base image migration.
			backupPath := filepath.Join(t.hermDir, "Dockerfile.old")
			if oldData, readErr := os.ReadFile(backupPath); readErr == nil {
				msg += fmt.Sprintf("\n\nA previous Dockerfile was backed up during base image migration. "+
					"Replicate its customizations on top of the herm base image (FROM aduermael/herm:__HERM_VERSION__):\n\n```\n%s```", string(oldData))
			}

			// Surface any named .herm/*.Dockerfile files so they can be consolidated.
			if entries, globErr := filepath.Glob(filepath.Join(t.hermDir, "*.Dockerfile")); globErr == nil && len(entries) > 0 {
				msg += "\n\nNote: named Dockerfiles exist that should be consolidated into .herm/Dockerfile:"
				for _, e := range entries {
					msg += "\n  " + filepath.Base(e)
					if d, readErr := os.ReadFile(e); readErr == nil {
						msg += "\n```\n" + string(d) + "```"
					}
				}
			}

			// Check for a Dockerfile in the project root.
			rootDockerfile := filepath.Join(t.workspace, "Dockerfile")
			if rootData, rootErr := os.ReadFile(rootDockerfile); rootErr == nil {
				msg += fmt.Sprintf("\n\nNote: a Dockerfile exists in the project root that you can use as a base:\n\n```\n%s```", string(rootData))
			}
			return msg + dockerfileGuidelines, nil
		}
		return "", fmt.Errorf("reading Dockerfile: %w", err)
	}

	// Regenerate environment manifest if stale (missing or older than Dockerfile).
	if t.manifestStale() {
		_ = t.generateManifest() // best-effort; don't fail the read
	}

	// Also report any stale named Dockerfiles alongside the active one.
	result := string(data)
	if entries, globErr := filepath.Glob(filepath.Join(t.hermDir, "*.Dockerfile")); globErr == nil && len(entries) > 0 {
		result += "\n\nWarning: the following named Dockerfiles also exist and are not active. Consolidate their contents into .herm/Dockerfile and remove them:\n"
		for _, e := range entries {
			result += "  " + filepath.Base(e) + "\n"
		}
	}
	return result + dockerfileGuidelines, nil
}

func (t *DevEnvTool) writeDockerfile(content string) (string, error) {
	if content == "" {
		return "", fmt.Errorf("content is required for write action")
	}

	// Validate that the Dockerfile uses the herm base image with version placeholder.
	if !dockerfileUsesHermBase(content) {
		return "", fmt.Errorf(
			"Dockerfile must use FROM aduermael/herm:__HERM_VERSION__ as the base image. " +
				"Add your custom tools on top of it.")
	}

	// Ensure .herm/ directory exists.
	if err := os.MkdirAll(t.hermDir, 0o755); err != nil {
		return "", fmt.Errorf("creating .herm directory: %w", err)
	}

	if err := os.WriteFile(t.dockerfilePath(), []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("writing Dockerfile: %w", err)
	}

	return "Dockerfile written to .herm/Dockerfile. Use the 'build' action to build and apply it.", nil
}

func (t *DevEnvTool) buildAndReplace() (string, error) {
	dfPath := t.dockerfilePath()
	if _, err := os.Stat(dfPath); os.IsNotExist(err) {
		return "", fmt.Errorf("no Dockerfile at .herm/Dockerfile — use 'write' first")
	}

	// Deterministic image name: herm-<shortProjectID>:<hash[:12]>.
	content, err := os.ReadFile(dfPath)
	if err != nil {
		return "", fmt.Errorf("reading Dockerfile: %w", err)
	}

	// Validate that the Dockerfile uses the herm base image with version placeholder.
	if !dockerfileUsesHermBase(string(content)) {
		return "", fmt.Errorf(
			"Dockerfile must use FROM aduermael/herm:__HERM_VERSION__ as the base image. " +
				"Add your custom tools on top of it.")
	}

	// Resolve __HERM_VERSION__ placeholder to actual version.
	resolved := resolveDockerfile(string(content))
	hash := sha256.Sum256([]byte(resolved))
	hashStr := hex.EncodeToString(hash[:])[:12]

	imageName := "herm-local:" + hashStr
	if len(t.projectID) >= 8 {
		imageName = "herm-" + t.projectID[:8] + ":" + hashStr
	}

	// Write resolved Dockerfile to a temp file for docker build.
	tmpFile, tmpErr := os.CreateTemp("", "herm-dockerfile-*")
	if tmpErr != nil {
		return "", fmt.Errorf("creating temp file: %w", tmpErr)
	}
	defer os.Remove(tmpFile.Name())
	if _, writeErr := tmpFile.WriteString(resolved); writeErr != nil {
		tmpFile.Close()
		return "", fmt.Errorf("writing resolved Dockerfile: %w", writeErr)
	}
	tmpFile.Close()

	if t.onStatus != nil {
		t.onStatus("rebuilding…")
	}
	if err := t.container.Rebuild(imageName, tmpFile.Name(), t.workspace, t.mounts); err != nil {
		if t.onStatus != nil {
			t.onStatus("rebuild failed")
		}
		return "", fmt.Errorf("rebuild failed: %w", err)
	}

	if t.onRebuild != nil {
		t.onRebuild(imageName)
	}
	if t.onStatus != nil {
		if cid := t.container.ContainerID(); len(cid) > 12 {
			t.onStatus(cid[:12])
		} else if cid != "" {
			t.onStatus(cid)
		}
	}

	// Generate environment manifest from the new container.
	if err := t.generateManifest(); err != nil {
		// Non-fatal: the build succeeded, manifest is a nice-to-have.
		return fmt.Sprintf("Container rebuilt successfully with image %s.\n(Warning: failed to generate environment manifest: %v)", imageName, err), nil
	}

	return fmt.Sprintf("Container rebuilt successfully with image %s.", imageName), nil
}

// WebSearchToolDef returns a server-side web search tool definition.
// The LLM provider handles the actual search — this just declares the capability.
// langdag maps the standardized name to each provider's native tool
// (Anthropic: web_search_20250305, OpenAI: web_search_preview, Gemini: google_search).
func WebSearchToolDef() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        types.ServerToolWebSearch,
		Description: "Search the web for current information",
	}
}
