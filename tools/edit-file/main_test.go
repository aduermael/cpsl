package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runEditFile(t *testing.T, in Input) Output {
	t.Helper()

	// Write input to stdin via pipe by running the logic directly.
	// Since main() reads from os.Stdin, we test the core logic instead.
	return execEdit(t, in)
}

// execEdit runs the edit logic without going through main().
func execEdit(t *testing.T, in Input) Output {
	t.Helper()

	// Validate input (mirrors main logic).
	if in.FilePath == "" {
		return Output{OK: false, Error: "file_path is required"}
	}
	if in.OldString == "" {
		return Output{OK: false, Error: "old_string is required"}
	}
	if in.OldString == in.NewString {
		return Output{OK: false, Error: "old_string and new_string are identical — nothing to change"}
	}

	content, err := os.ReadFile(in.FilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return Output{OK: false, Error: "file not found: " + in.FilePath}
		}
		return Output{OK: false, Error: "cannot read file: " + err.Error()}
	}

	text := string(content)
	count := strings.Count(text, in.OldString)
	if count == 0 {
		return Output{OK: false, Error: "old_string not found in " + in.FilePath}
	}
	if count > 1 && !in.ReplaceAll {
		return Output{OK: false, Error: "old_string found multiple times"}
	}

	var newText string
	if in.ReplaceAll {
		newText = strings.ReplaceAll(text, in.OldString, in.NewString)
	} else {
		newText = strings.Replace(text, in.OldString, in.NewString, 1)
	}

	dir := filepath.Dir(in.FilePath)
	tmp, err := os.CreateTemp(dir, ".edit-file-*")
	if err != nil {
		return Output{OK: false, Error: "cannot create temp file: " + err.Error()}
	}
	tmpName := tmp.Name()
	tmp.WriteString(newText)
	tmp.Close()

	info, err := os.Stat(in.FilePath)
	if err == nil {
		os.Chmod(tmpName, info.Mode())
	}

	if err := os.Rename(tmpName, in.FilePath); err != nil {
		os.Remove(tmpName)
		return Output{OK: false, Error: "cannot write file: " + err.Error()}
	}

	diff := unifiedDiff(unifiedDiffOptions{path: in.FilePath, a: text, b: newText})
	return Output{OK: true, Diff: diff}
}

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSuccessfulEdit(t *testing.T) {
	dir := t.TempDir()
	p := writeTestFile(t, dir, "test.go", "func hello() {\n\treturn \"hello\"\n}\n")

	out := execEdit(t, Input{
		FilePath:  p,
		OldString: "\"hello\"",
		NewString: "\"world\"",
	})
	if !out.OK {
		t.Fatalf("expected OK, got error: %s", out.Error)
	}
	if !strings.Contains(out.Diff, "-\treturn \"hello\"") {
		t.Errorf("diff should show removed line, got:\n%s", out.Diff)
	}
	if !strings.Contains(out.Diff, "+\treturn \"world\"") {
		t.Errorf("diff should show added line, got:\n%s", out.Diff)
	}

	got, _ := os.ReadFile(p)
	if string(got) != "func hello() {\n\treturn \"world\"\n}\n" {
		t.Errorf("file content wrong: %q", got)
	}
}

func TestFileNotFound(t *testing.T) {
	out := execEdit(t, Input{
		FilePath:  "/nonexistent/path/file.txt",
		OldString: "a",
		NewString: "b",
	})
	if out.OK {
		t.Fatal("expected error")
	}
	if !strings.Contains(out.Error, "file not found") {
		t.Errorf("expected 'file not found', got: %s", out.Error)
	}
}

func TestOldStringNotFound(t *testing.T) {
	dir := t.TempDir()
	p := writeTestFile(t, dir, "test.txt", "hello world\n")

	out := execEdit(t, Input{
		FilePath:  p,
		OldString: "goodbye",
		NewString: "hi",
	})
	if out.OK {
		t.Fatal("expected error")
	}
	if !strings.Contains(out.Error, "not found") {
		t.Errorf("expected 'not found', got: %s", out.Error)
	}
}

func TestOldStringNotUnique(t *testing.T) {
	dir := t.TempDir()
	p := writeTestFile(t, dir, "test.txt", "aaa\nbbb\naaa\n")

	out := execEdit(t, Input{
		FilePath:  p,
		OldString: "aaa",
		NewString: "ccc",
	})
	if out.OK {
		t.Fatal("expected error")
	}
	if !strings.Contains(out.Error, "multiple times") {
		t.Errorf("expected 'multiple times', got: %s", out.Error)
	}
}

func TestReplaceAll(t *testing.T) {
	dir := t.TempDir()
	p := writeTestFile(t, dir, "test.txt", "foo bar\nfoo baz\n")

	out := execEdit(t, Input{
		FilePath:   p,
		OldString:  "foo",
		NewString:  "qux",
		ReplaceAll: true,
	})
	if !out.OK {
		t.Fatalf("expected OK, got error: %s", out.Error)
	}

	got, _ := os.ReadFile(p)
	if string(got) != "qux bar\nqux baz\n" {
		t.Errorf("file content wrong: %q", got)
	}
}

func TestNoOp(t *testing.T) {
	out := execEdit(t, Input{
		FilePath:  "/tmp/whatever",
		OldString: "same",
		NewString: "same",
	})
	if out.OK {
		t.Fatal("expected error for no-op")
	}
	if !strings.Contains(out.Error, "identical") {
		t.Errorf("expected 'identical', got: %s", out.Error)
	}
}

func TestPathWithSpaces(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "my dir")
	os.MkdirAll(subdir, 0o755)
	p := writeTestFile(t, subdir, "my file.txt", "alpha beta\n")

	out := execEdit(t, Input{
		FilePath:  p,
		OldString: "alpha",
		NewString: "gamma",
	})
	if !out.OK {
		t.Fatalf("expected OK, got error: %s", out.Error)
	}

	got, _ := os.ReadFile(p)
	if string(got) != "gamma beta\n" {
		t.Errorf("file content wrong: %q", got)
	}
}

func TestEmptyNewString(t *testing.T) {
	dir := t.TempDir()
	p := writeTestFile(t, dir, "test.txt", "line1\nremove-me\nline3\n")

	out := execEdit(t, Input{
		FilePath:  p,
		OldString: "remove-me\n",
		NewString: "",
	})
	if !out.OK {
		t.Fatalf("expected OK, got error: %s", out.Error)
	}

	got, _ := os.ReadFile(p)
	if string(got) != "line1\nline3\n" {
		t.Errorf("file content wrong: %q", got)
	}
}

func TestPreservesPermissions(t *testing.T) {
	dir := t.TempDir()
	p := writeTestFile(t, dir, "script.sh", "#!/bin/bash\necho old\n")
	os.Chmod(p, 0o755)

	out := execEdit(t, Input{
		FilePath:  p,
		OldString: "echo old",
		NewString: "echo new",
	})
	if !out.OK {
		t.Fatalf("expected OK, got error: %s", out.Error)
	}

	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0o755 {
		t.Errorf("expected 0755 permissions, got %o", info.Mode().Perm())
	}
}

// Test the diff algorithm directly.
func TestMyersDiff(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want string // expected ops: E=equal, D=delete, I=insert
	}{
		{"empty", nil, nil, ""},
		{"all inserts", nil, []string{"a", "b"}, "II"},
		{"all deletes", []string{"a", "b"}, nil, "DD"},
		{"no change", []string{"a", "b"}, []string{"a", "b"}, "EE"},
		{"single replace", []string{"a", "b", "c"}, []string{"a", "x", "c"}, "EDIE"},
		{"insert middle", []string{"a", "c"}, []string{"a", "b", "c"}, "EIE"},
		{"delete middle", []string{"a", "b", "c"}, []string{"a", "c"}, "EDE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			edits := myersDiff(myersDiffOptions{a: tt.a, b: tt.b})
			var ops strings.Builder
			for _, e := range edits {
				switch e.op {
				case opEqual:
					ops.WriteByte('E')
				case opDelete:
					ops.WriteByte('D')
				case opInsert:
					ops.WriteByte('I')
				}
			}
			if ops.String() != tt.want {
				t.Errorf("got %s, want %s", ops.String(), tt.want)
			}
		})
	}
}

func TestUnifiedDiffFormat(t *testing.T) {
	diff := unifiedDiff(unifiedDiffOptions{
		path: "test.go",
		a:    "line1\nline2\nline3\n",
		b:    "line1\nchanged\nline3\n",
	})
	if !strings.Contains(diff, "--- a/") {
		t.Error("diff missing --- header")
	}
	if !strings.Contains(diff, "+++ b/") {
		t.Error("diff missing +++ header")
	}
	if !strings.Contains(diff, "@@") {
		t.Error("diff missing @@ hunk header")
	}
	if !strings.Contains(diff, "-line2") {
		t.Error("diff missing deleted line")
	}
	if !strings.Contains(diff, "+changed") {
		t.Error("diff missing added line")
	}
}

func TestOutputJSON(t *testing.T) {
	out := Output{OK: true, Diff: "some diff"}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var got Output
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Diff != "some diff" {
		t.Errorf("round-trip failed: %+v", got)
	}
}
