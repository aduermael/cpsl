package main

import (
	"bufio"
	"os"
	"path/filepath"
	"testing"
)

func TestHistoryAddAndRetrieve(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(newHistoryOptions{projectDir: dir, maxSize: 100})

	h.Add("first")
	h.Add("second")
	h.Add("third")

	if h.Len() != 3 {
		t.Fatalf("expected Len()==3, got %d", h.Len())
	}

	// Up returns newest first
	s, ok := h.Up("")
	if !ok || s != "third" {
		t.Fatalf("expected (third, true), got (%q, %v)", s, ok)
	}
	s, ok = h.Up("")
	if !ok || s != "second" {
		t.Fatalf("expected (second, true), got (%q, %v)", s, ok)
	}
	s, ok = h.Up("")
	if !ok || s != "first" {
		t.Fatalf("expected (first, true), got (%q, %v)", s, ok)
	}
}

func TestHistoryDraftPreservation(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(newHistoryOptions{projectDir: dir, maxSize: 100})

	h.Add("cmd1")
	h.Add("cmd2")

	// Start navigating with a partial draft
	s, ok := h.Up("partial typing")
	if !ok || s != "cmd2" {
		t.Fatalf("expected (cmd2, true), got (%q, %v)", s, ok)
	}

	s, ok = h.Up("")
	if !ok || s != "cmd1" {
		t.Fatalf("expected (cmd1, true), got (%q, %v)", s, ok)
	}

	// Navigate back down
	s, ok = h.Down("")
	if !ok || s != "cmd2" {
		t.Fatalf("expected (cmd2, true), got (%q, %v)", s, ok)
	}

	// Down again should restore draft
	s, ok = h.Down("")
	if !ok || s != "partial typing" {
		t.Fatalf("expected (partial typing, true), got (%q, %v)", s, ok)
	}
}

func TestHistoryBoundsUp(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(newHistoryOptions{projectDir: dir, maxSize: 100})

	h.Add("a")
	h.Add("b")

	// Navigate to oldest
	h.Up("")
	h.Up("")

	// One more Up should fail
	s, ok := h.Up("")
	if ok || s != "" {
		t.Fatalf("expected ('', false) at upper bound, got (%q, %v)", s, ok)
	}
}

func TestHistoryBoundsDown(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(newHistoryOptions{projectDir: dir, maxSize: 100})

	h.Add("a")
	h.Add("b")

	// Not navigating, Down should fail
	s, ok := h.Down("")
	if ok || s != "" {
		t.Fatalf("expected ('', false) when not navigating, got (%q, %v)", s, ok)
	}
}

func TestHistoryDedup(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(newHistoryOptions{projectDir: dir, maxSize: 100})

	h.Add("hello")
	h.Add("hello") // consecutive duplicate
	if h.Len() != 1 {
		t.Fatalf("expected Len()==1 after consecutive dedup, got %d", h.Len())
	}

	h.Add("world")
	h.Add("hello") // non-consecutive, should be added
	if h.Len() != 3 {
		t.Fatalf("expected Len()==3 after non-consecutive add, got %d", h.Len())
	}
}

func TestHistoryMaxSize(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(newHistoryOptions{projectDir: dir, maxSize: 5})

	for i := 1; i <= 8; i++ {
		h.Add("cmd" + string(rune('0'+i)))
	}

	if h.Len() != 5 {
		t.Fatalf("expected Len()==5, got %d", h.Len())
	}

	// Newest should be cmd8
	s, ok := h.Up("")
	if !ok || s != "cmd8" {
		t.Fatalf("expected (cmd8, true), got (%q, %v)", s, ok)
	}

	// Navigate to oldest accessible
	h.Up("")
	h.Up("")
	h.Up("")
	s, ok = h.Up("")
	// Should be cmd4 (5th from newest: cmd8, cmd7, cmd6, cmd5, cmd4)
	// Wait -- need to verify the entry format. We used rune('0'+i) which gives
	// '1','2',...'8' as chars. Actually for i>9 this breaks, but i<=8 is fine.
	// But "cmd" + string(rune('0'+4)) = "cmd4"
	// Actually we go: cmd1, cmd2, ..., cmd8. After trim: cmd4, cmd5, cmd6, cmd7, cmd8
	if !ok || s != "cmd4" {
		t.Fatalf("expected (cmd4, true) as oldest, got (%q, %v)", s, ok)
	}

	// One more Up should fail
	s, ok = h.Up("")
	if ok || s != "" {
		t.Fatalf("expected ('', false) past oldest, got (%q, %v)", s, ok)
	}
}

func TestHistoryReset(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(newHistoryOptions{projectDir: dir, maxSize: 100})

	h.Add("a")
	h.Add("b")

	h.Up("")
	if !h.IsNavigating() {
		t.Fatal("expected IsNavigating()==true after Up()")
	}

	h.Reset()
	if h.IsNavigating() {
		t.Fatal("expected IsNavigating()==false after Reset()")
	}
}

func TestHistoryPersistence(t *testing.T) {
	dir := t.TempDir()

	h1 := newHistory(newHistoryOptions{projectDir: dir, maxSize: 100})
	h1.Add("alpha")
	h1.Add("beta")
	h1.Add("gamma")

	// Create a new history instance at the same dir
	h2 := newHistory(newHistoryOptions{projectDir: dir, maxSize: 100})
	if err := h2.Load(); err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if h2.Len() != 3 {
		t.Fatalf("expected Len()==3 after Load(), got %d", h2.Len())
	}

	s, ok := h2.Up("")
	if !ok || s != "gamma" {
		t.Fatalf("expected (gamma, true), got (%q, %v)", s, ok)
	}
	s, ok = h2.Up("")
	if !ok || s != "beta" {
		t.Fatalf("expected (beta, true), got (%q, %v)", s, ok)
	}
	s, ok = h2.Up("")
	if !ok || s != "alpha" {
		t.Fatalf("expected (alpha, true), got (%q, %v)", s, ok)
	}
}

func TestHistoryEmptyFile(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(newHistoryOptions{projectDir: dir, maxSize: 100})

	if err := h.Load(); err != nil {
		t.Fatalf("Load() on non-existent file should not error, got: %v", err)
	}
	if h.Len() != 0 {
		t.Fatalf("expected Len()==0, got %d", h.Len())
	}
}

func TestHistoryMultilineRoundTrip(t *testing.T) {
	dir := t.TempDir()

	h1 := newHistory(newHistoryOptions{projectDir: dir, maxSize: 100})
	multiline := "line1\nline2\nline3"
	h1.Add(multiline)

	h2 := newHistory(newHistoryOptions{projectDir: dir, maxSize: 100})
	if err := h2.Load(); err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	s, ok := h2.Up("")
	if !ok || s != multiline {
		t.Fatalf("expected multiline round-trip, got (%q, %v)", s, ok)
	}
}

func TestHistoryCompaction(t *testing.T) {
	dir := t.TempDir()
	h1 := newHistory(newHistoryOptions{projectDir: dir, maxSize: 5})

	// Add 15 entries one by one; each appends a line to the file
	for i := 1; i <= 15; i++ {
		h1.Add("entry" + itoa(i))
	}

	// The file should have 15 lines now (one per Add call)
	filePath := filepath.Join(dir, ".herm", "history")
	lineCount := countLines(t, filePath)
	if lineCount != 15 {
		t.Fatalf("expected 15 lines in file before compaction, got %d", lineCount)
	}

	// In-memory should already be trimmed to 5
	if h1.Len() != 5 {
		t.Fatalf("expected Len()==5 in memory, got %d", h1.Len())
	}

	// Create new history and Load -- this should trigger compaction (15 > 2*5)
	h2 := newHistory(newHistoryOptions{projectDir: dir, maxSize: 5})
	if err := h2.Load(); err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if h2.Len() != 5 {
		t.Fatalf("expected Len()==5 after compacted Load(), got %d", h2.Len())
	}

	// File should now be compacted to 5 lines
	lineCount = countLines(t, filePath)
	if lineCount != 5 {
		t.Fatalf("expected 5 lines in file after compaction, got %d", lineCount)
	}

	// Verify entries are the newest 5: entry11 through entry15
	s, ok := h2.Up("")
	if !ok || s != "entry15" {
		t.Fatalf("expected (entry15, true), got (%q, %v)", s, ok)
	}
	// Navigate to oldest
	h2.Up("")
	h2.Up("")
	h2.Up("")
	s, ok = h2.Up("")
	if !ok || s != "entry11" {
		t.Fatalf("expected (entry11, true) as oldest, got (%q, %v)", s, ok)
	}
}

// itoa converts a small int to its decimal string representation.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func TestHistoryAddWhitespace(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(newHistoryOptions{projectDir: dir, maxSize: 100})

	h.Add("")
	h.Add("   ")
	h.Add("\t\n")
	if h.Len() != 0 {
		t.Fatalf("expected Len()==0 after whitespace-only adds, got %d", h.Len())
	}
}

func TestHistoryLoadMalformedLines(t *testing.T) {
	dir := t.TempDir()
	hermDir := filepath.Join(dir, ".herm")
	if err := os.MkdirAll(hermDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a file with some valid and some invalid JSON lines
	content := `{"t":1,"p":"good1"}
not json at all
{"t":2,"p":"good2"}
{broken json
{"t":3,"p":"good3"}
`
	if err := os.WriteFile(filepath.Join(hermDir, "history"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	h := newHistory(newHistoryOptions{projectDir: dir, maxSize: 100})
	if err := h.Load(); err != nil {
		t.Fatalf("Load() should not error on malformed lines, got: %v", err)
	}

	// Only the 3 valid entries should be loaded
	if h.Len() != 3 {
		t.Fatalf("expected Len()==3, got %d", h.Len())
	}

	s, ok := h.Up("")
	if !ok || s != "good3" {
		t.Fatalf("expected (good3, true), got (%q, %v)", s, ok)
	}
}

func TestHistorySaveRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// Add entries, then verify the file has exactly those entries
	h1 := newHistory(newHistoryOptions{projectDir: dir, maxSize: 100})
	h1.Add("alpha")
	h1.Add("beta")
	h1.Add("gamma")

	// Verify file exists and has 3 lines
	filePath := filepath.Join(dir, ".herm", "history")
	lineCount := countLines(t, filePath)
	if lineCount != 3 {
		t.Fatalf("expected 3 lines in file, got %d", lineCount)
	}

	// Load into a new instance with smaller maxSize to test trim-on-load
	h2 := newHistory(newHistoryOptions{projectDir: dir, maxSize: 2})
	if err := h2.Load(); err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Should only keep newest 2
	if h2.Len() != 2 {
		t.Fatalf("expected Len()==2 after Load() with maxSize=2, got %d", h2.Len())
	}

	s, ok := h2.Up("")
	if !ok || s != "gamma" {
		t.Fatalf("expected (gamma, true), got (%q, %v)", s, ok)
	}
	s, ok = h2.Up("")
	if !ok || s != "beta" {
		t.Fatalf("expected (beta, true), got (%q, %v)", s, ok)
	}
}

func TestHistoryWriteErrorSilent(t *testing.T) {
	// Use a path that doesn't exist and can't be created
	h := newHistory(newHistoryOptions{projectDir: "/nonexistent/deep/path/that/cannot/be/created", maxSize: 100})

	// Add should not panic or error — it silently fails to write
	h.Add("test entry")

	// In-memory state should still be correct
	if h.Len() != 1 {
		t.Fatalf("expected Len()==1, got %d", h.Len())
	}
	s, ok := h.Up("")
	if !ok || s != "test entry" {
		t.Fatalf("expected (test entry, true), got (%q, %v)", s, ok)
	}
}

func TestHistoryRewritePreservesContent(t *testing.T) {
	dir := t.TempDir()

	// Add enough entries to trigger compaction on Load
	h1 := newHistory(newHistoryOptions{projectDir: dir, maxSize: 3})
	for i := 1; i <= 10; i++ {
		h1.Add("entry" + itoa(i))
	}

	// File has 10 lines, maxSize is 3 → Load triggers compaction (10 > 2*3)
	h2 := newHistory(newHistoryOptions{projectDir: dir, maxSize: 3})
	if err := h2.Load(); err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// After compaction, file should have exactly 3 lines
	filePath := filepath.Join(dir, ".herm", "history")
	lineCount := countLines(t, filePath)
	if lineCount != 3 {
		t.Fatalf("expected 3 lines after compaction, got %d", lineCount)
	}

	// A third Load should still work correctly
	h3 := newHistory(newHistoryOptions{projectDir: dir, maxSize: 3})
	if err := h3.Load(); err != nil {
		t.Fatalf("Load() error after rewrite: %v", err)
	}
	if h3.Len() != 3 {
		t.Fatalf("expected Len()==3 after second Load(), got %d", h3.Len())
	}

	// Verify entries are entry8, entry9, entry10
	s, _ := h3.Up("")
	if s != "entry10" {
		t.Fatalf("expected entry10, got %q", s)
	}
	s, _ = h3.Up("")
	if s != "entry9" {
		t.Fatalf("expected entry9, got %q", s)
	}
	s, _ = h3.Up("")
	if s != "entry8" {
		t.Fatalf("expected entry8, got %q", s)
	}
}

func TestHistoryDefaultMaxSize(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(newHistoryOptions{projectDir: dir, maxSize: 0})
	// maxSize 0 should default to defaultMaxHistory (100)
	if h.maxSize != defaultMaxHistory {
		t.Fatalf("expected maxSize=%d for 0 input, got %d", defaultMaxHistory, h.maxSize)
	}

	h2 := newHistory(newHistoryOptions{projectDir: dir, maxSize: -5})
	if h2.maxSize != defaultMaxHistory {
		t.Fatalf("expected maxSize=%d for negative input, got %d", defaultMaxHistory, h2.maxSize)
	}
}

// countLines counts the number of non-empty lines in a file.
func countLines(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("failed to open %s: %v", path, err)
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if scanner.Text() != "" {
			count++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}
	return count
}
