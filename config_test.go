package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadConfigCreatesDefault(t *testing.T) {
	dir := t.TempDir()

	cfg, err := loadConfigFrom(dir)
	if err != nil {
		t.Fatalf("loadConfigFrom: %v", err)
	}

	if !reflect.DeepEqual(cfg, defaultConfig()) {
		t.Errorf("config = %+v, want %+v", cfg, defaultConfig())
	}

	// File should exist on disk
	data, err := os.ReadFile(filepath.Join(dir, configDir, configFile))
	if err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	var ondisk Config
	if err := json.Unmarshal(data, &ondisk); err != nil {
		t.Fatalf("unmarshal on-disk config: %v", err)
	}
	if !reflect.DeepEqual(ondisk, defaultConfig()) {
		t.Errorf("on-disk config = %+v, want %+v", ondisk, defaultConfig())
	}
}

func TestLoadConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()

	original := Config{PasteCollapseMinChars: 10}
	if err := saveConfigTo(dir, original); err != nil {
		t.Fatalf("saveConfigTo: %v", err)
	}

	loaded, err := loadConfigFrom(dir)
	if err != nil {
		t.Fatalf("loadConfigFrom: %v", err)
	}

	if !reflect.DeepEqual(loaded, original) {
		t.Errorf("loaded = %+v, want %+v", loaded, original)
	}
}

func TestLoadConfigMissingFileFallback(t *testing.T) {
	dir := t.TempDir()

	// Don't create any file — loadConfigFrom should create defaults
	cfg, err := loadConfigFrom(dir)
	if err != nil {
		t.Fatalf("loadConfigFrom: %v", err)
	}

	if !reflect.DeepEqual(cfg, defaultConfig()) {
		t.Errorf("config = %+v, want defaults %+v", cfg, defaultConfig())
	}
}

func TestLoadConfigMalformedJSON(t *testing.T) {
	dir := t.TempDir()

	cfgDir := filepath.Join(dir, configDir)
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, configFile), []byte("{bad json}"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfigFrom(dir)
	if err != nil {
		t.Fatalf("loadConfigFrom: %v", err)
	}

	if !reflect.DeepEqual(cfg, defaultConfig()) {
		t.Errorf("config = %+v, want defaults %+v on malformed JSON", cfg, defaultConfig())
	}
}

func TestLoadConfigMergesNewFields(t *testing.T) {
	dir := t.TempDir()

	// Write a config file that is missing the PasteCollapseMinChars field
	// (simulates upgrading when a new field is added)
	cfgDir := filepath.Join(dir, configDir)
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, configFile), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfigFrom(dir)
	if err != nil {
		t.Fatalf("loadConfigFrom: %v", err)
	}

	// Missing field should get its default value
	if cfg.PasteCollapseMinChars != defaultConfig().PasteCollapseMinChars {
		t.Errorf("PasteCollapseMinChars = %d, want default %d",
			cfg.PasteCollapseMinChars, defaultConfig().PasteCollapseMinChars)
	}
}

func TestSortPrefsRoundTrip(t *testing.T) {
	dir := t.TempDir()

	cfg := Config{
		PasteCollapseMinChars: 200,
		ModelSortCol:          "price",
		ModelSortDirs: map[string]bool{
			"name": true, "provider": true, "price": false, "context": true,
		},
	}
	if err := saveConfigTo(dir, cfg); err != nil {
		t.Fatalf("saveConfigTo: %v", err)
	}

	loaded, err := loadConfigFrom(dir)
	if err != nil {
		t.Fatalf("loadConfigFrom: %v", err)
	}

	if loaded.ModelSortCol != "price" {
		t.Errorf("ModelSortCol = %q, want %q", loaded.ModelSortCol, "price")
	}
	if loaded.ModelSortDirs["price"] != false {
		t.Error("ModelSortDirs[price] = true, want false (descending)")
	}
	if loaded.ModelSortDirs["name"] != true {
		t.Error("ModelSortDirs[name] = false, want true (ascending)")
	}
}

func TestSortPrefsDefaultsWhenMissing(t *testing.T) {
	dir := t.TempDir()

	cfg := Config{PasteCollapseMinChars: 200}
	if err := saveConfigTo(dir, cfg); err != nil {
		t.Fatalf("saveConfigTo: %v", err)
	}

	loaded, err := loadConfigFrom(dir)
	if err != nil {
		t.Fatalf("loadConfigFrom: %v", err)
	}

	if loaded.ModelSortCol != "" {
		t.Errorf("ModelSortCol = %q, want empty (default)", loaded.ModelSortCol)
	}
	if loaded.ModelSortDirs != nil {
		t.Errorf("ModelSortDirs = %v, want nil (default)", loaded.ModelSortDirs)
	}
}

func TestSortAscFromMapDefaults(t *testing.T) {
	// nil map → all ascending
	result := sortAscFromMap(nil)
	for i, v := range result {
		if !v {
			t.Errorf("sortAscFromMap(nil)[%d] = false, want true", i)
		}
	}
}

func TestSortAscRoundTrip(t *testing.T) {
	original := [4]bool{true, false, false, true}
	m := sortAscToMap(original)
	restored := sortAscFromMap(m)
	if restored != original {
		t.Errorf("round-trip: got %v, want %v", restored, original)
	}
}

func TestSaveConfigCreatesDir(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "nested", "path")

	cfg := Config{PasteCollapseMinChars: 3}
	if err := saveConfigTo(subdir, cfg); err != nil {
		t.Fatalf("saveConfigTo: %v", err)
	}

	// Verify file exists
	data, err := os.ReadFile(filepath.Join(subdir, configDir, configFile))
	if err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if loaded.PasteCollapseMinChars != 3 {
		t.Errorf("PasteCollapseMinChars = %d, want 3", loaded.PasteCollapseMinChars)
	}
}

// ─── Project config tests ───

func TestLoadProjectConfigMissingFile(t *testing.T) {
	dir := t.TempDir()
	pc := loadProjectConfig(dir)
	if pc != (ProjectConfig{}) {
		t.Errorf("loadProjectConfig = %+v, want empty", pc)
	}
}

func TestLoadProjectConfigEmptyRepoRoot(t *testing.T) {
	pc := loadProjectConfig("")
	if pc != (ProjectConfig{}) {
		t.Errorf("loadProjectConfig(\"\") = %+v, want empty", pc)
	}
}

func TestLoadProjectConfigMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, configDir)
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, configFile), []byte("{bad}"), 0o644); err != nil {
		t.Fatal(err)
	}
	pc := loadProjectConfig(dir)
	if pc != (ProjectConfig{}) {
		t.Errorf("loadProjectConfig = %+v, want empty on malformed JSON", pc)
	}
}

func TestProjectConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	original := ProjectConfig{
		ActiveModel:      "gpt-4",
		Personality:      "concise",
		SubAgentMaxTurns: 10,
	}
	if err := saveProjectConfig(dir, original); err != nil {
		t.Fatalf("saveProjectConfig: %v", err)
	}
	loaded := loadProjectConfig(dir)
	if loaded != original {
		t.Errorf("loaded = %+v, want %+v", loaded, original)
	}
}

func TestSaveProjectConfigCreatesDir(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "nested", "repo")
	pc := ProjectConfig{ActiveModel: "claude-3"}
	if err := saveProjectConfig(subdir, pc); err != nil {
		t.Fatalf("saveProjectConfig: %v", err)
	}
	loaded := loadProjectConfig(subdir)
	if loaded.ActiveModel != "claude-3" {
		t.Errorf("ActiveModel = %q, want %q", loaded.ActiveModel, "claude-3")
	}
}

func TestMergeConfigsProjectOverrides(t *testing.T) {
	global := Config{
		PasteCollapseMinChars: 200,
		ActiveModel:           "default-model",
		Personality:           "friendly",
		SubAgentMaxTurns:      15,
		AnthropicAPIKey:       "key123",
	}
	project := ProjectConfig{
		ActiveModel:      "project-model",
		SubAgentMaxTurns: 5,
	}
	merged := mergeConfigs(global, project)

	// Overridden fields
	if merged.ActiveModel != "project-model" {
		t.Errorf("ActiveModel = %q, want %q", merged.ActiveModel, "project-model")
	}
	if merged.SubAgentMaxTurns != 5 {
		t.Errorf("SubAgentMaxTurns = %d, want 5", merged.SubAgentMaxTurns)
	}
	// Non-overridden project field falls back to global
	if merged.Personality != "friendly" {
		t.Errorf("Personality = %q, want %q (global fallback)", merged.Personality, "friendly")
	}
	// Global-only fields unchanged
	if merged.PasteCollapseMinChars != 200 {
		t.Errorf("PasteCollapseMinChars = %d, want 200", merged.PasteCollapseMinChars)
	}
	if merged.AnthropicAPIKey != "key123" {
		t.Errorf("AnthropicAPIKey = %q, want %q", merged.AnthropicAPIKey, "key123")
	}
}

func TestMergeConfigsEmptyProject(t *testing.T) {
	global := Config{
		PasteCollapseMinChars: 200,
		ActiveModel:           "default-model",
		Personality:           "friendly",
		SubAgentMaxTurns:      15,
	}
	merged := mergeConfigs(global, ProjectConfig{})
	if !reflect.DeepEqual(merged, global) {
		t.Errorf("merged = %+v, want %+v (unchanged global)", merged, global)
	}
}

func TestMergeConfigsAllOverridden(t *testing.T) {
	global := Config{
		ActiveModel:      "global-model",
		Personality:      "verbose",
		SubAgentMaxTurns: 15,
	}
	project := ProjectConfig{
		ActiveModel:      "proj-model",
		Personality:      "terse",
		SubAgentMaxTurns: 3,
	}
	merged := mergeConfigs(global, project)
	if merged.ActiveModel != "proj-model" {
		t.Errorf("ActiveModel = %q, want %q", merged.ActiveModel, "proj-model")
	}
	if merged.Personality != "terse" {
		t.Errorf("Personality = %q, want %q", merged.Personality, "terse")
	}
	if merged.SubAgentMaxTurns != 3 {
		t.Errorf("SubAgentMaxTurns = %d, want 3", merged.SubAgentMaxTurns)
	}
}
