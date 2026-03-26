// manifest.go generates .herm/environment.md — a compact manifest of what's
// installed in the dev container, injected into the system prompt so the agent
// knows available runtimes and tools without running discovery commands.
//
// Two modes:
//   - Base image (no .herm/Dockerfile): use prompts.BaseEnvironment.
//   - Custom image (after devenv build): parse the Dockerfile to describe
//     what was added on top of the base image.
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"herm/prompts"
)

const manifestFile = "environment.md"

// manifestPath returns the path to .herm/environment.md.
func (t *DevEnvTool) manifestPath() string {
	return filepath.Join(t.hermDir, manifestFile)
}

// generateManifest writes .herm/environment.md based on the current state:
//   - If no Dockerfile exists, writes the base image manifest.
//   - If a Dockerfile exists, parses it to describe the environment.
func (t *DevEnvTool) generateManifest() error {
	if err := os.MkdirAll(t.hermDir, 0o755); err != nil {
		return err
	}

	dockerfile, err := os.ReadFile(t.dockerfilePath())
	if err != nil {
		if os.IsNotExist(err) {
			return os.WriteFile(t.manifestPath(), []byte(strings.TrimSpace(prompts.BaseEnvironment)+"\n"), 0o644)
		}
		return err
	}

	manifest := manifestFromDockerfile(string(dockerfile))
	return os.WriteFile(t.manifestPath(), []byte(manifest+"\n"), 0o644)
}

// manifestStale returns true if the manifest is missing or older than the Dockerfile.
// Also returns true if neither exists (so the base manifest gets written).
func (t *DevEnvTool) manifestStale() bool {
	mInfo, err := os.Stat(t.manifestPath())
	if err != nil {
		return true // missing
	}

	dfInfo, err := os.Stat(t.dockerfilePath())
	if err != nil {
		return false // no Dockerfile, base manifest is current
	}

	return dfInfo.ModTime().After(mInfo.ModTime())
}

// manifestFromDockerfile parses a Dockerfile and generates a compact environment
// description. Extracts ENV variables (especially *_VERSION), apt-get packages,
// and PATH additions — everything the agent needs to know what's available.
//
// The output format mirrors prompts.BaseEnvironment so the system prompt reads consistently.
func manifestFromDockerfile(dockerfile string) string {
	var sections []string

	// Always start with base image info.
	sections = append(sections, "Pre-installed: git, ripgrep (rg), tree, python3")
	sections = append(sections, "Herm tools: edit-file, write-file, outline")

	// Extract ENV declarations — these often declare versions and paths.
	if envs := extractEnvVars(dockerfile); len(envs) > 0 {
		sections = append(sections, "Environment: "+strings.Join(envs, ", "))
	}

	// Extract apt-get installed packages (beyond what's in the base).
	if pkgs := extractAptPackages(dockerfile); len(pkgs) > 0 {
		sections = append(sections, "Packages: "+strings.Join(pkgs, ", "))
	}

	return strings.Join(sections, "\n")
}

// envVarRe matches ENV KEY=VALUE or ENV KEY VALUE lines.
var envVarRe = regexp.MustCompile(`(?m)^\s*ENV\s+(\S+?)=(\S+)`)

// extractEnvVars pulls ENV KEY=VALUE declarations from a Dockerfile.
// Returns compact "KEY=VALUE" pairs, useful for version and PATH info.
func extractEnvVars(dockerfile string) []string {
	matches := envVarRe.FindAllStringSubmatch(dockerfile, -1)
	var result []string
	for _, m := range matches {
		result = append(result, m[1]+"="+m[2])
	}
	return result
}

// aptInstallRe matches apt-get install lines and captures the package list.
var aptInstallRe = regexp.MustCompile(`apt-get\s+install\s+(?:-\S+\s+)*(.+?)(?:\s*&&|\s*\\?\s*$)`)

// basePackages are packages already in the base image — filter them out.
var basePackages = map[string]bool{
	"git": true, "tree": true, "ca-certificates": true, "ripgrep": true, "python3": true,
}

// extractAptPackages finds packages installed via apt-get in the Dockerfile.
// Filters out packages already in the base image.
func extractAptPackages(dockerfile string) []string {
	seen := make(map[string]bool)
	var result []string

	// Normalize line continuations so multi-line RUN commands are on one line.
	normalized := strings.ReplaceAll(dockerfile, "\\\n", " ")

	for _, match := range aptInstallRe.FindAllStringSubmatch(normalized, -1) {
		pkgStr := match[1]
		for _, pkg := range strings.Fields(pkgStr) {
			// Skip flags and cleanup commands.
			if strings.HasPrefix(pkg, "-") || pkg == "&&" || pkg == "rm" || strings.HasPrefix(pkg, "/") {
				break // rest of line is likely cleanup
			}
			if !basePackages[pkg] && !seen[pkg] {
				seen[pkg] = true
				result = append(result, pkg)
			}
		}
	}
	return result
}
