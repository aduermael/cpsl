// manifest.go generates .herm/environment.md — a compact manifest of what's
// installed in the dev container, injected into the system prompt so the agent
// knows available runtimes and tools without running discovery commands.
package main

import (
	"path/filepath"
	"strings"
)

const manifestFile = "environment.md"

// envDetectScript runs inside the container to discover installed runtimes and tools.
// Output is delimited by =RUNTIMES= and =TOOLS= markers, parsed by parseManifest.
const envDetectScript = `echo "=RUNTIMES="
command -v go >/dev/null 2>&1 && go version
command -v node >/dev/null 2>&1 && node --version
command -v python3 >/dev/null 2>&1 && python3 --version
command -v ruby >/dev/null 2>&1 && ruby --version
command -v rustc >/dev/null 2>&1 && rustc --version
command -v java >/dev/null 2>&1 && java -version 2>&1 | head -1
echo "=TOOLS="
for cmd in git rg tree curl wget make cmake gcc g++ clang; do command -v "$cmd" >/dev/null 2>&1 && printf "%s " "$cmd"; done
echo ""`

// manifestPath returns the path to .herm/environment.md.
func (t *DevEnvTool) manifestPath() string {
	return filepath.Join(t.hermDir, manifestFile)
}

// parseManifest parses the output of envDetectScript into a compact manifest string.
// Format (target <5 lines):
//
//	Runtimes: go 1.22.5, node 22.14.0, python3 3.11.2
//	System tools: git, rg, tree, curl, wget, make
func parseManifest(output string) string {
	lines := strings.Split(output, "\n")

	var runtimes []string
	var toolsRaw string
	section := ""

	for _, line := range lines {
		line = strings.TrimSpace(line)
		switch line {
		case "=RUNTIMES=":
			section = "runtimes"
			continue
		case "=TOOLS=":
			section = "tools"
			continue
		}
		if line == "" {
			continue
		}

		switch section {
		case "runtimes":
			if name, ver := parseVersionLine(line); name != "" {
				runtimes = append(runtimes, name+" "+ver)
			}
		case "tools":
			toolsRaw = line
		}
	}

	var parts []string
	if len(runtimes) > 0 {
		parts = append(parts, "Runtimes: "+strings.Join(runtimes, ", "))
	}
	if t := strings.TrimSpace(toolsRaw); t != "" {
		toolList := strings.Fields(t)
		parts = append(parts, "System tools: "+strings.Join(toolList, ", "))
	}

	return strings.Join(parts, "\n")
}

// parseVersionLine extracts a runtime name and version from common version output formats.
func parseVersionLine(line string) (name, version string) {
	// "go version go1.22.5 linux/amd64"
	if strings.HasPrefix(line, "go version go") {
		parts := strings.Fields(line)
		if len(parts) >= 3 {
			return "go", strings.TrimPrefix(parts[2], "go")
		}
	}
	// "v22.14.0" (node --version)
	if strings.HasPrefix(line, "v") && !strings.Contains(line, " ") {
		return "node", strings.TrimPrefix(line, "v")
	}
	// "Python 3.11.2"
	if strings.HasPrefix(line, "Python ") {
		return "python3", strings.TrimPrefix(line, "Python ")
	}
	// "ruby 3.1.2p20 (2022-04-12 revision ...)  ..."
	if strings.HasPrefix(line, "ruby ") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			ver := parts[1]
			if idx := strings.Index(ver, "p"); idx > 0 {
				ver = ver[:idx]
			}
			return "ruby", ver
		}
	}
	// "rustc 1.75.0 (82e1608df 2023-12-21)"
	if strings.HasPrefix(line, "rustc ") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			return "rustc", parts[1]
		}
	}
	// "openjdk 21.0.1 2023-10-17" or "openjdk version \"21.0.1\" ..."
	if strings.Contains(line, "openjdk") || strings.Contains(line, "java version") {
		parts := strings.Fields(line)
		for _, p := range parts {
			p = strings.Trim(p, `"`)
			if len(p) > 0 && p[0] >= '0' && p[0] <= '9' {
				return "java", p
			}
		}
	}
	return "", ""
}
