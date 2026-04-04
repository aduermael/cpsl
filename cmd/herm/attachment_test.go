package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExpandAttachments_NoStore(t *testing.T) {
	got := expandAttachments("hello world", nil)
	if got != "hello world" {
		t.Fatalf("expected passthrough, got %q", got)
	}
}

func TestExpandAttachments_NoPlaceholders(t *testing.T) {
	store := map[int]Attachment{1: {Data: "abc", MediaType: "image/png", IsImage: true}}
	got := expandAttachments("hello world", store)
	if got != "hello world" {
		t.Fatalf("expected passthrough, got %q", got)
	}
}

func TestExpandAttachments_SingleImage(t *testing.T) {
	store := map[int]Attachment{
		1: {Data: "AAAA", MediaType: "image/png", IsImage: true},
	}
	got := expandAttachments("Look at this: [Image #1] ok?", store)

	var blocks []map[string]string
	if err := json.Unmarshal([]byte(got), &blocks); err != nil {
		t.Fatalf("not valid JSON: %v\ngot: %s", err, got)
	}
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d: %v", len(blocks), blocks)
	}
	if blocks[0]["type"] != "text" || blocks[0]["text"] != "Look at this: " {
		t.Errorf("block 0: %v", blocks[0])
	}
	if blocks[1]["type"] != "image" || blocks[1]["media_type"] != "image/png" || blocks[1]["data"] != "AAAA" {
		t.Errorf("block 1: %v", blocks[1])
	}
	if blocks[2]["type"] != "text" || blocks[2]["text"] != " ok?" {
		t.Errorf("block 2: %v", blocks[2])
	}
}

func TestExpandAttachments_SingleFile(t *testing.T) {
	store := map[int]Attachment{
		1: {Data: "BBBB", MediaType: "application/pdf", IsImage: false},
	}
	got := expandAttachments("[File #1]", store)

	var blocks []map[string]string
	if err := json.Unmarshal([]byte(got), &blocks); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0]["type"] != "document" || blocks[0]["media_type"] != "application/pdf" {
		t.Errorf("block 0: %v", blocks[0])
	}
}

func TestExpandAttachments_Multiple(t *testing.T) {
	store := map[int]Attachment{
		1: {Data: "IMG1", MediaType: "image/png", IsImage: true},
		2: {Data: "PDF1", MediaType: "application/pdf", IsImage: false},
	}
	got := expandAttachments("[Image #1] and [File #2]", store)

	var blocks []map[string]string
	if err := json.Unmarshal([]byte(got), &blocks); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}
	if blocks[0]["type"] != "image" {
		t.Errorf("block 0 type: %s", blocks[0]["type"])
	}
	if blocks[1]["type"] != "text" || blocks[1]["text"] != " and " {
		t.Errorf("block 1: %v", blocks[1])
	}
	if blocks[2]["type"] != "document" {
		t.Errorf("block 2 type: %s", blocks[2]["type"])
	}
}

func TestExpandAttachments_MissingID(t *testing.T) {
	store := map[int]Attachment{
		1: {Data: "IMG1", MediaType: "image/png", IsImage: true},
	}
	// [Image #99] not in store — should be kept as text
	got := expandAttachments("hello [Image #99] world", store)

	var blocks []map[string]string
	if err := json.Unmarshal([]byte(got), &blocks); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}
	if blocks[0]["text"] != "hello " {
		t.Errorf("block 0: %v", blocks[0])
	}
	if blocks[1]["text"] != "[Image #99]" {
		t.Errorf("block 1: %v", blocks[1])
	}
	if blocks[2]["text"] != " world" {
		t.Errorf("block 2: %v", blocks[2])
	}
}

func TestExpandAttachments_TextOnly(t *testing.T) {
	store := map[int]Attachment{
		1: {Data: "IMG1", MediaType: "image/png", IsImage: true},
	}
	// Only text, no placeholders — but store is non-empty
	got := expandAttachments("just text", store)
	if got != "just text" {
		t.Fatalf("expected passthrough, got %q", got)
	}
}

// ─── isFilePath tests ───

func TestIsFilePath_DoubleQuoted(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "test.jpeg")
	if err := os.WriteFile(tmp, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate a terminal that wraps the dropped path in double quotes.
	quoted := `"` + tmp + `"`
	resolved, ok := isFilePath(quoted)
	if !ok {
		t.Fatalf("expected isFilePath to accept double-quoted path %q", quoted)
	}
	if resolved != tmp {
		t.Fatalf("expected resolved=%q, got %q", tmp, resolved)
	}
}

func TestIsFilePath_SingleQuoted(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "test.png")
	if err := os.WriteFile(tmp, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate a terminal that wraps the dropped path in single quotes.
	quoted := "'" + tmp + "'"
	resolved, ok := isFilePath(quoted)
	if !ok {
		t.Fatalf("expected isFilePath to accept single-quoted path %q", quoted)
	}
	if resolved != tmp {
		t.Fatalf("expected resolved=%q, got %q", tmp, resolved)
	}
}

func TestIsFilePath_Unquoted(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "test.pdf")
	if err := os.WriteFile(tmp, []byte("pdf"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, ok := isFilePath(tmp)
	if !ok {
		t.Fatalf("expected isFilePath to accept unquoted path %q", tmp)
	}
	if resolved != tmp {
		t.Fatalf("expected resolved=%q, got %q", tmp, resolved)
	}
}

func TestIsFilePath_NonExistent(t *testing.T) {
	_, ok := isFilePath("/no/such/file.txt")
	if ok {
		t.Fatal("expected isFilePath to reject non-existent path")
	}
}

func TestIsFilePath_RelativePath(t *testing.T) {
	_, ok := isFilePath("relative/path.txt")
	if ok {
		t.Fatal("expected isFilePath to reject relative path")
	}
}

func TestIsFilePath_BackslashSpaces(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "my file.txt")
	if err := os.WriteFile(tmp, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate shell-escaped spaces from drag-drop.
	escaped := filepath.Join(dir, "my\\ file.txt")
	resolved, ok := isFilePath(escaped)
	if !ok {
		t.Fatalf("expected isFilePath to accept backslash-space path %q", escaped)
	}
	if resolved != tmp {
		t.Fatalf("expected resolved=%q, got %q", tmp, resolved)
	}
}

func TestIsFilePath_DoubleQuotedMultiline(t *testing.T) {
	// When multiple quoted paths are pasted, each line is passed to
	// isFilePath individually after trimming. Verify that a single
	// double-quoted line resolves correctly (the caller splits on \n).
	tmp := filepath.Join(t.TempDir(), "photo.jpg")
	if err := os.WriteFile(tmp, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	quoted := `"` + tmp + `"`
	resolved, ok := isFilePath(quoted)
	if !ok {
		t.Fatalf("expected isFilePath to accept %q", quoted)
	}
	if resolved != tmp {
		t.Fatalf("expected resolved=%q, got %q", tmp, resolved)
	}
}

func TestIsFilePath_UnicodeEscapes(t *testing.T) {
	// Zed's terminal escapes non-ASCII characters in dropped paths as \u{XXXX}.
	// e.g. the narrow no-break space (U+202F) in macOS screenshot filenames.
	dir := t.TempDir()
	// Create a file with U+202F (narrow no-break space) in the name.
	tmp := filepath.Join(dir, "Screenshot 2026-03-31 at 8.02.54\u202fPM.png")
	if err := os.WriteFile(tmp, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate Zed's drag-drop: double-quoted path with \u{202f} escape.
	input := `"` + filepath.Join(dir, `Screenshot 2026-03-31 at 8.02.54\u{202f}PM.png`) + `"`
	resolved, ok := isFilePath(input)
	if !ok {
		t.Fatalf("expected isFilePath to accept Zed-style Unicode-escaped path %q", input)
	}
	if resolved != tmp {
		t.Fatalf("expected resolved=%q, got %q", tmp, resolved)
	}
}

func TestIsFilePath_QuotedWithSpaces(t *testing.T) {
	// Some terminals drop paths as: ` "/path/to/img.png " `
	// (double quotes AND spaces on both sides).
	tmp := filepath.Join(t.TempDir(), "img.png")
	if err := os.WriteFile(tmp, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		` "` + tmp + `" `,   // spaces outside quotes
		`" ` + tmp + ` "`,   // spaces inside quotes
		` " ` + tmp + ` " `, // spaces both inside and outside
		`  ` + tmp + `  `,   // just spaces, no quotes
		` '` + tmp + `' `,   // single quotes with spaces outside
		`' ` + tmp + ` '`,   // single quotes with spaces inside
	} {
		resolved, ok := isFilePath(input)
		if !ok {
			t.Fatalf("expected isFilePath to accept %q", input)
		}
		if resolved != tmp {
			t.Fatalf("input %q: expected resolved=%q, got %q", input, tmp, resolved)
		}
	}
}
