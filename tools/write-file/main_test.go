package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// execWrite runs the write logic directly (mirrors main() without stdin).
func execWrite(t *testing.T, in Input) Output {
	t.Helper()

	if in.FilePath == "" {
		return Output{OK: false, Error: "file_path is required"}
	}

	oldContent, err := os.ReadFile(in.FilePath)
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		return Output{OK: false, Error: "cannot read existing file: " + err.Error()}
	}

	dir := filepath.Dir(in.FilePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Output{OK: false, Error: "cannot create directories: " + err.Error()}
	}

	tmp, err := os.CreateTemp(dir, ".write-file-*")
	if err != nil {
		return Output{OK: false, Error: "cannot create temp file: " + err.Error()}
	}
	tmpName := tmp.Name()
	tmp.WriteString(in.Content)
	tmp.Close()

	if existed {
		if info, err := os.Stat(in.FilePath); err == nil {
			os.Chmod(tmpName, info.Mode())
		}
	}

	if err := os.Rename(tmpName, in.FilePath); err != nil {
		os.Remove(tmpName)
		return Output{OK: false, Error: "cannot write file: " + err.Error()}
	}

	lines := countLines(in.Content)
	bytes := len(in.Content)

	out := Output{OK: true, Created: !existed}
	if existed {
		out.Summary = "Overwrote " + in.FilePath
		out.Diff = unifiedDiff(unifiedDiffOptions{path: in.FilePath, a: string(oldContent), b: in.Content})
	} else {
		out.Summary = "Created " + in.FilePath
	}
	_ = lines
	_ = bytes
	return out
}

func TestWriteNewFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "new.txt")

	out := execWrite(t, Input{FilePath: p, Content: "hello\nworld\n"})
	if !out.OK {
		t.Fatalf("expected OK, got error: %s", out.Error)
	}
	if !out.Created {
		t.Error("expected Created=true")
	}
	if !strings.Contains(out.Summary, "Created") {
		t.Errorf("summary should say Created, got: %s", out.Summary)
	}

	got, _ := os.ReadFile(p)
	if string(got) != "hello\nworld\n" {
		t.Errorf("content wrong: %q", got)
	}
}

func TestOverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "existing.txt")
	os.WriteFile(p, []byte("old content\n"), 0o644)

	out := execWrite(t, Input{FilePath: p, Content: "new content\n"})
	if !out.OK {
		t.Fatalf("expected OK, got error: %s", out.Error)
	}
	if out.Created {
		t.Error("expected Created=false for overwrite")
	}
	if !strings.Contains(out.Summary, "Overwrote") {
		t.Errorf("summary should say Overwrote, got: %s", out.Summary)
	}
	if !strings.Contains(out.Diff, "-old content") {
		t.Errorf("diff should show removed old content, got:\n%s", out.Diff)
	}
	if !strings.Contains(out.Diff, "+new content") {
		t.Errorf("diff should show added new content, got:\n%s", out.Diff)
	}

	got, _ := os.ReadFile(p)
	if string(got) != "new content\n" {
		t.Errorf("content wrong: %q", got)
	}
}

func TestCreateParentDirs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a", "b", "c", "file.txt")

	out := execWrite(t, Input{FilePath: p, Content: "nested\n"})
	if !out.OK {
		t.Fatalf("expected OK, got error: %s", out.Error)
	}
	if !out.Created {
		t.Error("expected Created=true")
	}

	got, _ := os.ReadFile(p)
	if string(got) != "nested\n" {
		t.Errorf("content wrong: %q", got)
	}
}

func TestWriteEmptyContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.txt")

	out := execWrite(t, Input{FilePath: p, Content: ""})
	if !out.OK {
		t.Fatalf("expected OK, got error: %s", out.Error)
	}

	got, _ := os.ReadFile(p)
	if string(got) != "" {
		t.Errorf("expected empty file, got: %q", got)
	}
}

func TestPathWithSpaces(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "my dir")
	p := filepath.Join(subdir, "my file.txt")

	out := execWrite(t, Input{FilePath: p, Content: "spaces ok\n"})
	if !out.OK {
		t.Fatalf("expected OK, got error: %s", out.Error)
	}

	got, _ := os.ReadFile(p)
	if string(got) != "spaces ok\n" {
		t.Errorf("content wrong: %q", got)
	}
}

func TestPreservesPermissions(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "script.sh")
	os.WriteFile(p, []byte("#!/bin/bash\necho old\n"), 0o755)

	out := execWrite(t, Input{FilePath: p, Content: "#!/bin/bash\necho new\n"})
	if !out.OK {
		t.Fatalf("expected OK, got error: %s", out.Error)
	}

	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0o755 {
		t.Errorf("expected 0755 permissions, got %o", info.Mode().Perm())
	}
}

func TestMissingFilePath(t *testing.T) {
	out := execWrite(t, Input{FilePath: "", Content: "x"})
	if out.OK {
		t.Fatal("expected error")
	}
	if !strings.Contains(out.Error, "file_path is required") {
		t.Errorf("expected file_path error, got: %s", out.Error)
	}
}

func TestCountLines(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 1},
		{"a\n", 1},
		{"a\nb\n", 2},
		{"a\nb", 2},
		{"\n\n\n", 3},
	}
	for _, tt := range tests {
		got := countLines(tt.input)
		if got != tt.want {
			t.Errorf("countLines(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestOutputJSON(t *testing.T) {
	out := Output{OK: true, Created: true, Summary: "Created foo.txt (5 lines, 42 bytes)"}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var got Output
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || !got.Created || got.Summary != out.Summary {
		t.Errorf("round-trip failed: %+v", got)
	}
}
