// instructions.go loads and parses the project instructions file
// (.herm/instructions.md) which provides persistent project-level guidance
// to the agent across sessions.
package main

import (
	"os"
	"path/filepath"
	"strings"
)

// maxInstructionsSize is the maximum allowed size for project instructions content.
// Files exceeding this are truncated with a warning suffix.
const maxInstructionsSize = 16 * 1024 // 16 KB

// truncationSuffix is appended when instructions content exceeds maxInstructionsSize.
const truncationSuffix = "\n\n[truncated — .herm/instructions.md exceeds 16KB limit]"

// ProjectInstructions holds parsed content from .herm/instructions.md.
type ProjectInstructions struct {
	Scope   string // "all" (default), "implement", or "main"
	Content string // markdown body after front-matter, trimmed
}

// importCLAUDEmd copies CLAUDE.md into .herm/instructions.md when the latter
// does not exist. This lets projects that already have a CLAUDE.md work with
// herm without requiring the user to manually create .herm/instructions.md.
// It is a no-op when .herm/instructions.md already exists or CLAUDE.md is absent.
func importCLAUDEmd(workDir string) {
	if workDir == "" {
		return
	}

	instrPath := filepath.Join(workDir, ".herm", "instructions.md")

	// If .herm/instructions.md already exists, nothing to do.
	if _, err := os.Stat(instrPath); err == nil {
		return
	}

	claudePath := filepath.Join(workDir, "CLAUDE.md")
	data, err := os.ReadFile(claudePath)
	if err != nil {
		return
	}

	if strings.TrimSpace(string(data)) == "" {
		return
	}

	// Ensure .herm/ directory exists.
	hermDir := filepath.Join(workDir, ".herm")
	if err := os.MkdirAll(hermDir, 0o755); err != nil {
		return
	}

	// Copy content verbatim.
	_ = os.WriteFile(instrPath, data, 0o644)
}

// loadProjectInstructions reads .herm/instructions.md from the given workDir,
// parses optional front-matter to extract scope, and returns the result.
// Returns a zero value if the file is absent, empty, or whitespace-only.
func loadProjectInstructions(workDir string) ProjectInstructions {
	if workDir == "" {
		return ProjectInstructions{}
	}

	data, err := os.ReadFile(filepath.Join(workDir, ".herm", "instructions.md"))
	if err != nil {
		return ProjectInstructions{}
	}

	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return ProjectInstructions{}
	}

	scope, body := parseInstructionsFrontMatter(raw)

	if body == "" {
		return ProjectInstructions{}
	}

	// Truncate if too large.
	if len(body) > maxInstructionsSize {
		body = body[:maxInstructionsSize] + truncationSuffix
	}

	return ProjectInstructions{
		Scope:   scope,
		Content: body,
	}
}

// parseInstructionsFrontMatter extracts the scope field from optional front-matter
// and returns the scope and remaining body. If no front-matter is present, scope
// defaults to "all" and the entire input is returned as body.
func parseInstructionsFrontMatter(raw string) (scope string, body string) {
	scope = "all"

	if !strings.HasPrefix(raw, "---") {
		return scope, raw
	}

	rest := raw[3:]
	rest = strings.TrimLeft(rest, "\r\n")
	idx := strings.Index(rest, "---")
	if idx < 0 {
		// Malformed front-matter — treat entire content as body.
		return scope, raw
	}

	frontMatter := rest[:idx]
	body = strings.TrimSpace(rest[idx+3:])

	for _, line := range strings.Split(frontMatter, "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, ":"); ok {
			if strings.TrimSpace(k) == "scope" {
				v = strings.TrimSpace(v)
				switch v {
				case "all", "implement", "main":
					scope = v
				default:
					// Unknown scope — warn and fall back to "all".
					if body != "" {
						body = "[warning: unknown scope \"" + v + "\" in .herm/instructions.md front-matter, defaulting to \"all\"]\n\n" + body
					}
				}
			}
		}
	}

	return scope, body
}

// ContentForMode returns the instructions content if the scope allows it for the
// given sub-agent mode. An empty mode means the main agent (always receives content).
//
// Scope rules:
//   - "all":       content for every mode (main, "implement", "explore")
//   - "implement": content for main and "implement" only
//   - "main":      content for main only (empty mode)
func (pi ProjectInstructions) ContentForMode(mode string) string {
	if mode == "" {
		// Main agent always gets instructions.
		return pi.Content
	}
	switch pi.Scope {
	case "all":
		return pi.Content
	case "implement":
		if mode == "implement" {
			return pi.Content
		}
		return ""
	case "main":
		return ""
	default:
		// Shouldn't happen (loadProjectInstructions normalizes), but be safe.
		return pi.Content
	}
}
