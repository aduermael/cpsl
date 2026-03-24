package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── 6a: Test debug file creation and writing ──

func TestInitDebugLogCreatesDirectoryAndFile(t *testing.T) {
	dir := t.TempDir()

	f, path, err := initDebugLog(dir)
	if err != nil {
		t.Fatalf("initDebugLog: %v", err)
	}
	defer f.Close()

	// Directory should exist
	info, err := os.Stat(filepath.Join(dir, configDir, debugDir))
	if err != nil {
		t.Fatalf("debug dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("debug dir is not a directory")
	}

	// File should exist at the returned path
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("debug file not created at %s: %v", path, err)
	}

	// Path should be inside .herm/debug/
	if !strings.Contains(path, filepath.Join(configDir, debugDir)) {
		t.Errorf("path %q does not contain expected directory", path)
	}

	// Filename should match debug-<timestamp>.log pattern
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "debug-") || !strings.HasSuffix(base, ".log") {
		t.Errorf("filename %q does not match debug-*.log pattern", base)
	}
}

func TestDebugWriteProducesDelimitedSections(t *testing.T) {
	dir := t.TempDir()
	f, path, err := initDebugLog(dir)
	if err != nil {
		t.Fatalf("initDebugLog: %v", err)
	}
	defer f.Close()

	debugWrite(f, "System Prompt", "You are a helpful assistant.")
	debugWrite(f, "User Message", "Hello, world!")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading debug file: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "── System Prompt ──") {
		t.Error("missing System Prompt section header")
	}
	if !strings.Contains(content, "You are a helpful assistant.") {
		t.Error("missing system prompt content")
	}
	if !strings.Contains(content, "── User Message ──") {
		t.Error("missing User Message section header")
	}
	if !strings.Contains(content, "Hello, world!") {
		t.Error("missing user message content")
	}
}

func TestDebugWriteNilFileIsNoop(t *testing.T) {
	// Should not panic
	debugWrite(nil, "Test", "content")
}

func TestCloseDebugLogNilIsNoop(t *testing.T) {
	// Should not panic
	closeDebugLog(nil)
}

func TestDebugModeOffNoFileCreated(t *testing.T) {
	a := &App{
		config:   Config{DebugMode: false},
		cliDebug: false,
		repoRoot: t.TempDir(),
	}

	a.initAppDebugLog()

	if a.debugFile != nil {
		t.Error("debug file should not be created when debug mode is off")
	}
	if a.debugFilePath != "" {
		t.Error("debug file path should be empty when debug mode is off")
	}
}

func TestDebugActiveConfigFlag(t *testing.T) {
	a := &App{config: Config{DebugMode: true}}
	if !a.debugActive() {
		t.Error("debugActive should return true when config.DebugMode is true")
	}
}

func TestDebugActiveCLIFlag(t *testing.T) {
	a := &App{cliDebug: true}
	if !a.debugActive() {
		t.Error("debugActive should return true when cliDebug is true")
	}
}

func TestDebugActiveNeitherFlag(t *testing.T) {
	a := &App{}
	if a.debugActive() {
		t.Error("debugActive should return false when neither flag is set")
	}
}

func TestInitAppDebugLogCreatesFile(t *testing.T) {
	dir := t.TempDir()
	a := &App{
		config:   Config{DebugMode: true},
		repoRoot: dir,
	}

	a.initAppDebugLog()
	defer closeDebugLog(a.debugFile)

	if a.debugFile == nil {
		t.Fatal("debug file should be created when debug mode is on")
	}
	if a.debugFilePath == "" {
		t.Fatal("debug file path should be set")
	}
	if _, err := os.Stat(a.debugFilePath); err != nil {
		t.Fatalf("debug file does not exist at %s: %v", a.debugFilePath, err)
	}
}

// ── 6b: Test debug file regeneration ──

func TestRegenerateDebugFile(t *testing.T) {
	dir := t.TempDir()
	a := &App{
		config:   Config{DebugMode: true},
		repoRoot: dir,
	}
	a.initAppDebugLog()
	defer closeDebugLog(a.debugFile)

	// Populate messages
	a.messages = []chatMessage{
		{kind: msgUser, content: "What is 2+2?"},
		{kind: msgAssistant, content: "4"},
		{kind: msgToolCall, content: "bash: ls -la"},
		{kind: msgToolResult, content: "file1.txt\nfile2.txt"},
		{kind: msgToolResult, content: "command not found", isError: true},
		{kind: msgSystemPrompt, content: "You are helpful."},
		{kind: msgInfo, content: "Model: claude-3"},
		{kind: msgSuccess, content: "Done!"},
		{kind: msgError, content: "Something went wrong"},
	}

	a.regenerateDebugFile()

	data, err := os.ReadFile(a.debugFilePath)
	if err != nil {
		t.Fatalf("reading debug file: %v", err)
	}
	content := string(data)

	// Verify all message types are present
	checks := []struct {
		section string
		text    string
	}{
		{"── User Message ──", "What is 2+2?"},
		{"── Assistant Text ──", "4"},
		{"── Tool Call ──", "bash: ls -la"},
		{"── Tool Result ──", "file1.txt"},
		{"── Tool Result [ERROR] ──", "command not found"},
		{"── System Prompt ──", "You are helpful."},
		{"── Info ──", "Model: claude-3"},
		{"── Success ──", "Done!"},
		{"── Error ──", "Something went wrong"},
		{"── Session Summary ──", ""},
	}
	for _, c := range checks {
		if !strings.Contains(content, c.section) {
			t.Errorf("missing section %q", c.section)
		}
		if c.text != "" && !strings.Contains(content, c.text) {
			t.Errorf("missing content %q in section %q", c.text, c.section)
		}
	}

	// Content should NOT be word-wrapped (raw content preserved)
	if !strings.Contains(content, "file1.txt\nfile2.txt") {
		t.Error("tool result content should be preserved without word-wrapping")
	}
}

func TestRegenerateDebugFileIncludesStreamingText(t *testing.T) {
	dir := t.TempDir()
	a := &App{
		config:   Config{DebugMode: true},
		repoRoot: dir,
	}
	a.initAppDebugLog()
	defer closeDebugLog(a.debugFile)

	a.streamingText = "partial response in progress..."
	a.regenerateDebugFile()

	data, _ := os.ReadFile(a.debugFilePath)
	content := string(data)

	if !strings.Contains(content, "── Assistant Text [streaming...] ──") {
		t.Error("missing streaming text section")
	}
	if !strings.Contains(content, "partial response in progress...") {
		t.Error("missing streaming text content")
	}
}

func TestRegenerateDebugFileTruncates(t *testing.T) {
	dir := t.TempDir()
	a := &App{
		config:   Config{DebugMode: true},
		repoRoot: dir,
	}
	a.initAppDebugLog()
	defer closeDebugLog(a.debugFile)

	// Write initial content
	a.messages = []chatMessage{
		{kind: msgUser, content: "First message"},
		{kind: msgAssistant, content: "First response that is quite long to ensure file is bigger"},
	}
	a.regenerateDebugFile()

	// Read initial size
	info1, _ := os.Stat(a.debugFilePath)
	size1 := info1.Size()

	// Regenerate with fewer messages (simulates /clear + new messages)
	a.messages = []chatMessage{
		{kind: msgUser, content: "Short"},
	}
	a.regenerateDebugFile()

	data, _ := os.ReadFile(a.debugFilePath)
	content := string(data)

	// Old content should be gone
	if strings.Contains(content, "First message") {
		t.Error("old messages should be truncated on regeneration")
	}

	// New content should be present
	if !strings.Contains(content, "Short") {
		t.Error("new message should be present after regeneration")
	}

	// File should be smaller
	info2, _ := os.Stat(a.debugFilePath)
	if info2.Size() >= size1 {
		t.Error("file should be smaller after regeneration with fewer messages")
	}
}

func TestRegenerateDebugFileNilIsNoop(t *testing.T) {
	a := &App{}
	// Should not panic
	a.regenerateDebugFile()
}

// ── 6c: Test /clear creates new debug file ──

func TestClearCreatesNewDebugFile(t *testing.T) {
	dir := t.TempDir()
	a := &App{
		config:   Config{DebugMode: true},
		cliDebug: true,
		repoRoot: dir,
	}
	a.initAppDebugLog()

	oldPath := a.debugFilePath
	oldFile := a.debugFile

	if oldPath == "" || oldFile == nil {
		t.Fatal("initial debug file should be created")
	}

	// Write something to the old file so we can verify it's closed
	debugWrite(a.debugFile, "Test", "old content")

	// Simulate what /clear does to debug files
	closeDebugLog(a.debugFile)
	a.debugFile = nil
	a.debugFilePath = ""

	// Small delay to ensure different timestamp in filename
	time.Sleep(time.Second)

	a.initAppDebugLog()

	newPath := a.debugFilePath
	newFile := a.debugFile
	defer closeDebugLog(newFile)

	if newPath == "" || newFile == nil {
		t.Fatal("new debug file should be created after clear")
	}

	if newPath == oldPath {
		t.Error("new debug file should have a different path than the old one")
	}

	// Old file should still exist on disk (not deleted)
	if _, err := os.Stat(oldPath); err != nil {
		t.Errorf("old debug file should still exist: %v", err)
	}

	// Old file should contain the old content
	oldData, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatalf("reading old debug file: %v", err)
	}
	if !strings.Contains(string(oldData), "old content") {
		t.Error("old debug file should contain previously written content")
	}

	// New file should be empty (or near-empty)
	newData, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("reading new debug file: %v", err)
	}
	if strings.Contains(string(newData), "old content") {
		t.Error("new debug file should not contain old content")
	}
}

// ── 6d: Test headless mode end-to-end ──

func TestHeadlessModeDebugFileCreated(t *testing.T) {
	dir := t.TempDir()
	a := &App{
		cliDebug:  true,
		cliPrompt: "test prompt",
		headless:  true,
		repoRoot:  dir,
	}

	a.initAppDebugLog()
	defer closeDebugLog(a.debugFile)

	if a.debugFile == nil {
		t.Fatal("debug file should be created in headless mode with --debug")
	}
	if a.debugFilePath == "" {
		t.Fatal("debug file path should be set in headless mode")
	}

	// Write some content as the headless event loop would
	a.debugWriteSection("User Message", a.cliPrompt)
	a.debugWriteSection("Assistant Text", "The answer is 42.")

	data, _ := os.ReadFile(a.debugFilePath)
	content := string(data)

	if !strings.Contains(content, "test prompt") {
		t.Error("debug file should contain the user prompt")
	}
	if !strings.Contains(content, "The answer is 42.") {
		t.Error("debug file should contain the assistant response")
	}
}

func TestHeadlessModeDebugFilePath(t *testing.T) {
	dir := t.TempDir()
	a := &App{
		cliDebug:  true,
		cliPrompt: "test prompt",
		headless:  true,
		repoRoot:  dir,
	}

	a.initAppDebugLog()
	defer closeDebugLog(a.debugFile)

	// The debug file path should be printable to stderr (non-empty, valid path)
	if a.debugFilePath == "" {
		t.Fatal("debug file path should be set for stderr output")
	}

	// Path should be under the repo root
	if !strings.HasPrefix(a.debugFilePath, dir) {
		t.Errorf("debug file path %q should be under repo root %q", a.debugFilePath, dir)
	}
}

func TestHeadlessModeWithoutDebugNoFile(t *testing.T) {
	a := &App{
		cliPrompt: "test prompt",
		headless:  true,
		repoRoot:  t.TempDir(),
	}

	a.initAppDebugLog()

	if a.debugFile != nil {
		t.Error("debug file should not be created in headless mode without --debug")
	}
}

func TestDebugWriteSessionSummary(t *testing.T) {
	dir := t.TempDir()
	a := &App{
		config:              Config{DebugMode: true},
		repoRoot:            dir,
		agentElapsed:        5 * time.Second,
		sessionLLMCalls:     3,
		mainAgentLLMCalls:   2,
		sessionInputTokens:  10000,
		mainAgentInputTokens: 8000,
		sessionOutputTokens: 500,
		mainAgentOutputTokens: 400,
		sessionCostUSD:      0.05,
		sessionToolResults:  5,
		sessionToolBytes:    2048,
		sessionToolStats: map[string][2]int{
			"bash":      {3, 1500},
			"read_file": {2, 548},
		},
	}
	a.initAppDebugLog()
	defer closeDebugLog(a.debugFile)

	a.debugWriteSessionSummary()

	data, _ := os.ReadFile(a.debugFilePath)
	content := string(data)

	if !strings.Contains(content, "── Session Summary ──") {
		t.Error("missing Session Summary section")
	}
	if !strings.Contains(content, "LLM calls: 3") {
		t.Error("missing LLM calls in summary")
	}
	if !strings.Contains(content, "Tool calls: 5") {
		t.Error("missing tool calls in summary")
	}
	if !strings.Contains(content, "bash") {
		t.Error("missing per-tool breakdown")
	}
	if !strings.Contains(content, "read_file") {
		t.Error("missing read_file in per-tool breakdown")
	}
}

func TestMergeConfigsDebugMode(t *testing.T) {
	// nil pointer → no override
	global := Config{DebugMode: false}
	project := ProjectConfig{}
	merged := mergeConfigs(global, project)
	if merged.DebugMode {
		t.Error("nil DebugMode should not override global")
	}

	// Explicit true
	trueVal := true
	project.DebugMode = &trueVal
	merged = mergeConfigs(global, project)
	if !merged.DebugMode {
		t.Error("project DebugMode=true should override global false")
	}

	// Explicit false overrides global true
	global.DebugMode = true
	falseVal := false
	project.DebugMode = &falseVal
	merged = mergeConfigs(global, project)
	if merged.DebugMode {
		t.Error("project DebugMode=false should override global true")
	}
}
