package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProjectInstructions_WithScopeAndContent(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")
	os.MkdirAll(hermDir, 0o755)
	os.WriteFile(filepath.Join(hermDir, "instructions.md"), []byte("---\nscope: implement\n---\n\nUse gofmt."), 0o644)

	pi := loadProjectInstructions(dir)
	if pi.Scope != "implement" {
		t.Errorf("scope = %q, want %q", pi.Scope, "implement")
	}
	if pi.Content != "Use gofmt." {
		t.Errorf("content = %q, want %q", pi.Content, "Use gofmt.")
	}
}

func TestLoadProjectInstructions_NoFrontMatter(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")
	os.MkdirAll(hermDir, 0o755)
	os.WriteFile(filepath.Join(hermDir, "instructions.md"), []byte("Just plain markdown."), 0o644)

	pi := loadProjectInstructions(dir)
	if pi.Scope != "all" {
		t.Errorf("scope = %q, want %q", pi.Scope, "all")
	}
	if pi.Content != "Just plain markdown." {
		t.Errorf("content = %q, want %q", pi.Content, "Just plain markdown.")
	}
}

func TestLoadProjectInstructions_FileAbsent(t *testing.T) {
	dir := t.TempDir()
	pi := loadProjectInstructions(dir)
	if pi.Scope != "" || pi.Content != "" {
		t.Errorf("expected zero value, got scope=%q content=%q", pi.Scope, pi.Content)
	}
}

func TestLoadProjectInstructions_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")
	os.MkdirAll(hermDir, 0o755)
	os.WriteFile(filepath.Join(hermDir, "instructions.md"), []byte("   \n\n  "), 0o644)

	pi := loadProjectInstructions(dir)
	if pi.Scope != "" || pi.Content != "" {
		t.Errorf("expected zero value for whitespace-only file, got scope=%q content=%q", pi.Scope, pi.Content)
	}
}

func TestLoadProjectInstructions_EmptyWorkDir(t *testing.T) {
	pi := loadProjectInstructions("")
	if pi.Scope != "" || pi.Content != "" {
		t.Errorf("expected zero value for empty workDir, got scope=%q content=%q", pi.Scope, pi.Content)
	}
}

func TestLoadProjectInstructions_UnknownScope(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")
	os.MkdirAll(hermDir, 0o755)
	os.WriteFile(filepath.Join(hermDir, "instructions.md"), []byte("---\nscope: custom\n---\n\nSome rules."), 0o644)

	pi := loadProjectInstructions(dir)
	if pi.Scope != "all" {
		t.Errorf("scope = %q, want %q (fallback)", pi.Scope, "all")
	}
	if !strings.Contains(pi.Content, "warning: unknown scope") {
		t.Error("content should contain warning about unknown scope")
	}
	if !strings.Contains(pi.Content, "Some rules.") {
		t.Error("content should still contain the original body")
	}
}

func TestLoadProjectInstructions_SizeCap(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")
	os.MkdirAll(hermDir, 0o755)

	// Create content that exceeds 16 KB.
	bigBody := strings.Repeat("x", maxInstructionsSize+100)
	os.WriteFile(filepath.Join(hermDir, "instructions.md"), []byte(bigBody), 0o644)

	pi := loadProjectInstructions(dir)
	if !strings.HasSuffix(pi.Content, truncationSuffix) {
		t.Error("content should end with truncation suffix")
	}
	// Body before suffix should be exactly maxInstructionsSize bytes.
	bodyWithoutSuffix := strings.TrimSuffix(pi.Content, truncationSuffix)
	if len(bodyWithoutSuffix) != maxInstructionsSize {
		t.Errorf("truncated body length = %d, want %d", len(bodyWithoutSuffix), maxInstructionsSize)
	}
}

func TestLoadProjectInstructions_FrontMatterOnlyNoBody(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")
	os.MkdirAll(hermDir, 0o755)
	os.WriteFile(filepath.Join(hermDir, "instructions.md"), []byte("---\nscope: all\n---\n"), 0o644)

	pi := loadProjectInstructions(dir)
	if pi.Content != "" {
		t.Errorf("expected empty content for front-matter-only file, got %q", pi.Content)
	}
}

func TestLoadProjectInstructions_ScopeMain(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")
	os.MkdirAll(hermDir, 0o755)
	os.WriteFile(filepath.Join(hermDir, "instructions.md"), []byte("---\nscope: main\n---\n\nMain only."), 0o644)

	pi := loadProjectInstructions(dir)
	if pi.Scope != "main" {
		t.Errorf("scope = %q, want %q", pi.Scope, "main")
	}
}

// --- importCLAUDEmd tests ---

func TestImportCLAUDEmd_CreatesInstructions(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("Use tabs for indentation."), 0o644)

	importCLAUDEmd(dir)

	data, err := os.ReadFile(filepath.Join(dir, ".herm", "instructions.md"))
	if err != nil {
		t.Fatalf("expected .herm/instructions.md to be created, got error: %v", err)
	}
	if string(data) != "Use tabs for indentation." {
		t.Errorf("content = %q, want %q", string(data), "Use tabs for indentation.")
	}
}

func TestImportCLAUDEmd_NoopWhenInstructionsExist(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")
	os.MkdirAll(hermDir, 0o755)
	os.WriteFile(filepath.Join(hermDir, "instructions.md"), []byte("Existing."), 0o644)
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("From CLAUDE.md."), 0o644)

	importCLAUDEmd(dir)

	data, _ := os.ReadFile(filepath.Join(hermDir, "instructions.md"))
	if string(data) != "Existing." {
		t.Errorf("content = %q, want %q (should not overwrite)", string(data), "Existing.")
	}
}

func TestImportCLAUDEmd_NoopWhenCLAUDEmdAbsent(t *testing.T) {
	dir := t.TempDir()

	importCLAUDEmd(dir)

	_, err := os.Stat(filepath.Join(dir, ".herm", "instructions.md"))
	if err == nil {
		t.Error("expected .herm/instructions.md to not be created when CLAUDE.md is absent")
	}
}

func TestImportCLAUDEmd_NoopWhenCLAUDEmdEmpty(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("   \n  "), 0o644)

	importCLAUDEmd(dir)

	_, err := os.Stat(filepath.Join(dir, ".herm", "instructions.md"))
	if err == nil {
		t.Error("expected .herm/instructions.md to not be created when CLAUDE.md is whitespace-only")
	}
}

func TestImportCLAUDEmd_NoopWhenEmptyWorkDir(t *testing.T) {
	importCLAUDEmd("") // should not panic
}

func TestImportCLAUDEmd_CreatesHermDir(t *testing.T) {
	dir := t.TempDir()
	// .herm/ does not exist yet.
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("Build with make."), 0o644)

	importCLAUDEmd(dir)

	info, err := os.Stat(filepath.Join(dir, ".herm"))
	if err != nil {
		t.Fatalf("expected .herm/ to be created, got error: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected .herm to be a directory")
	}
}

func TestImportCLAUDEmd_IntegrationWithLoad(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("---\nscope: implement\n---\n\nRun tests before committing."), 0o644)

	importCLAUDEmd(dir)
	pi := loadProjectInstructions(dir)

	if pi.Scope != "implement" {
		t.Errorf("scope = %q, want %q", pi.Scope, "implement")
	}
	if pi.Content != "Run tests before committing." {
		t.Errorf("content = %q, want %q", pi.Content, "Run tests before committing.")
	}
}

// --- ContentForMode tests ---

func TestContentForMode_ScopeAll(t *testing.T) {
	pi := ProjectInstructions{Scope: "all", Content: "rules"}
	for _, mode := range []string{"", "implement", "explore"} {
		if got := pi.ContentForMode(mode); got != "rules" {
			t.Errorf("ContentForMode(%q) = %q, want %q", mode, got, "rules")
		}
	}
}

func TestContentForMode_ScopeImplement(t *testing.T) {
	pi := ProjectInstructions{Scope: "implement", Content: "rules"}

	// Main agent (empty mode) always gets content.
	if got := pi.ContentForMode(""); got != "rules" {
		t.Errorf("ContentForMode(\"\") = %q, want %q", got, "rules")
	}
	// Implement sub-agent gets content.
	if got := pi.ContentForMode("implement"); got != "rules" {
		t.Errorf("ContentForMode(\"implement\") = %q, want %q", got, "rules")
	}
	// Explore sub-agent does NOT get content.
	if got := pi.ContentForMode("explore"); got != "" {
		t.Errorf("ContentForMode(\"explore\") = %q, want empty", got)
	}
}

func TestContentForMode_ScopeMain(t *testing.T) {
	pi := ProjectInstructions{Scope: "main", Content: "rules"}

	// Main agent gets content.
	if got := pi.ContentForMode(""); got != "rules" {
		t.Errorf("ContentForMode(\"\") = %q, want %q", got, "rules")
	}
	// Sub-agents do NOT get content.
	for _, mode := range []string{"implement", "explore"} {
		if got := pi.ContentForMode(mode); got != "" {
			t.Errorf("ContentForMode(%q) = %q, want empty", mode, got)
		}
	}
}
