package main

import (
	"strings"
	"testing"
)

func TestParseVersionLine_Go(t *testing.T) {
	name, ver := parseVersionLine("go version go1.22.5 linux/amd64")
	if name != "go" || ver != "1.22.5" {
		t.Errorf("got %q %q, want go 1.22.5", name, ver)
	}
}

func TestParseVersionLine_Node(t *testing.T) {
	name, ver := parseVersionLine("v22.14.0")
	if name != "node" || ver != "22.14.0" {
		t.Errorf("got %q %q, want node 22.14.0", name, ver)
	}
}

func TestParseVersionLine_Python(t *testing.T) {
	name, ver := parseVersionLine("Python 3.11.2")
	if name != "python3" || ver != "3.11.2" {
		t.Errorf("got %q %q, want python3 3.11.2", name, ver)
	}
}

func TestParseVersionLine_Ruby(t *testing.T) {
	name, ver := parseVersionLine("ruby 3.1.2p20 (2022-04-12 revision 4491bb740a) [x86_64-linux]")
	if name != "ruby" || ver != "3.1.2" {
		t.Errorf("got %q %q, want ruby 3.1.2", name, ver)
	}
}

func TestParseVersionLine_Rustc(t *testing.T) {
	name, ver := parseVersionLine("rustc 1.75.0 (82e1608df 2023-12-21)")
	if name != "rustc" || ver != "1.75.0" {
		t.Errorf("got %q %q, want rustc 1.75.0", name, ver)
	}
}

func TestParseVersionLine_Java(t *testing.T) {
	name, ver := parseVersionLine(`openjdk version "21.0.1" 2023-10-17`)
	if name != "java" || ver != "21.0.1" {
		t.Errorf("got %q %q, want java 21.0.1", name, ver)
	}
}

func TestParseVersionLine_JavaShort(t *testing.T) {
	name, ver := parseVersionLine("openjdk 21.0.1 2023-10-17")
	if name != "java" || ver != "21.0.1" {
		t.Errorf("got %q %q, want java 21.0.1", name, ver)
	}
}

func TestParseVersionLine_Unknown(t *testing.T) {
	name, ver := parseVersionLine("some random output")
	if name != "" || ver != "" {
		t.Errorf("got %q %q, want empty", name, ver)
	}
}

func TestParseManifest_Full(t *testing.T) {
	output := `=RUNTIMES=
go version go1.22.5 linux/amd64
v22.14.0
Python 3.11.2
=TOOLS=
git rg tree curl wget make
`
	got := parseManifest(output)

	if !strings.Contains(got, "Runtimes: go 1.22.5, node 22.14.0, python3 3.11.2") {
		t.Errorf("runtimes line wrong: %s", got)
	}
	if !strings.Contains(got, "System tools: git, rg, tree, curl, wget, make") {
		t.Errorf("tools line wrong: %s", got)
	}

	// Verify compactness — should be exactly 2 lines.
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d: %q", len(lines), got)
	}
}

func TestParseManifest_RuntimesOnly(t *testing.T) {
	output := "=RUNTIMES=\ngo version go1.22.5 linux/amd64\n=TOOLS=\n\n"
	got := parseManifest(output)

	if !strings.Contains(got, "Runtimes: go 1.22.5") {
		t.Errorf("expected runtimes, got: %s", got)
	}
	if strings.Contains(got, "System tools") {
		t.Error("should not have tools line when no tools detected")
	}
}

func TestParseManifest_ToolsOnly(t *testing.T) {
	output := "=RUNTIMES=\n=TOOLS=\ngit rg tree\n"
	got := parseManifest(output)

	if strings.Contains(got, "Runtimes") {
		t.Error("should not have runtimes line when none detected")
	}
	if !strings.Contains(got, "System tools: git, rg, tree") {
		t.Errorf("expected tools, got: %s", got)
	}
}

func TestParseManifest_Empty(t *testing.T) {
	output := "=RUNTIMES=\n=TOOLS=\n"
	got := parseManifest(output)
	if got != "" {
		t.Errorf("expected empty manifest, got: %q", got)
	}
}

func TestManifestPath(t *testing.T) {
	tool := NewDevEnvTool(nil, "/tmp/.herm", "/tmp", nil, "", nil, nil)
	got := tool.manifestPath()
	if got != "/tmp/.herm/environment.md" {
		t.Errorf("manifestPath() = %q, want /tmp/.herm/environment.md", got)
	}
}
