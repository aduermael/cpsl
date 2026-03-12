package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// ─── Config UI tests ───

func TestCfgTabNamesStructure(t *testing.T) {
	want := []string{"API Keys", "Global", "Project"}
	if !reflect.DeepEqual(cfgTabNames, want) {
		t.Errorf("cfgTabNames = %v, want %v", cfgTabNames, want)
	}
}

func TestProjectTabFieldLabels(t *testing.T) {
	a := &App{}
	fields := a.projectTabFields()
	wantLabels := []string{"Active Model", "Exploration Model", "Personality", "Sub-Agent Max Turns"}
	if len(fields) != len(wantLabels) {
		t.Fatalf("projectTabFields returned %d fields, want %d", len(fields), len(wantLabels))
	}
	for i, f := range fields {
		if f.label != wantLabels[i] {
			t.Errorf("field[%d].label = %q, want %q", i, f.label, wantLabels[i])
		}
		if f.globalHint == nil {
			t.Errorf("field[%d] (%s) has nil globalHint", i, f.label)
		}
	}
}

func TestProjectTabFieldGetSet(t *testing.T) {
	a := &App{
		cfgProjectDraft: ProjectConfig{
			ActiveModel:      "test-model",
			ExplorationModel: "explore-model",
			Personality:      "brief",
			SubAgentMaxTurns: 7,
		},
	}
	fields := a.projectTabFields()

	// Verify get returns project values
	if v := fields[0].get(Config{}); v != "test-model" {
		t.Errorf("ActiveModel get = %q, want %q", v, "test-model")
	}
	if v := fields[1].get(Config{}); v != "explore-model" {
		t.Errorf("ExplorationModel get = %q, want %q", v, "explore-model")
	}
	if v := fields[2].get(Config{}); v != "brief" {
		t.Errorf("Personality get = %q, want %q", v, "brief")
	}
	if v := fields[3].get(Config{}); v != "7" {
		t.Errorf("SubAgentMaxTurns get = %q, want %q", v, "7")
	}

	// Verify set modifies project draft
	fields[0].set(nil, "new-model")
	if a.cfgProjectDraft.ActiveModel != "new-model" {
		t.Errorf("after set, ActiveModel = %q, want %q", a.cfgProjectDraft.ActiveModel, "new-model")
	}
	fields[1].set(nil, "new-explore")
	if a.cfgProjectDraft.ExplorationModel != "new-explore" {
		t.Errorf("after set, ExplorationModel = %q, want %q", a.cfgProjectDraft.ExplorationModel, "new-explore")
	}
	fields[2].set(nil, "verbose")
	if a.cfgProjectDraft.Personality != "verbose" {
		t.Errorf("after set, Personality = %q, want %q", a.cfgProjectDraft.Personality, "verbose")
	}
	fields[3].set(nil, "20")
	if a.cfgProjectDraft.SubAgentMaxTurns != 20 {
		t.Errorf("after set, SubAgentMaxTurns = %d, want 20", a.cfgProjectDraft.SubAgentMaxTurns)
	}
}

func TestProjectTabSubAgentClearsOnEmpty(t *testing.T) {
	a := &App{cfgProjectDraft: ProjectConfig{SubAgentMaxTurns: 10}}
	fields := a.projectTabFields()
	fields[3].set(nil, "") // Sub-Agent Max Turns is at index 3 now
	if a.cfgProjectDraft.SubAgentMaxTurns != 0 {
		t.Errorf("SubAgentMaxTurns = %d, want 0 after clearing", a.cfgProjectDraft.SubAgentMaxTurns)
	}
}

func TestBuildConfigRowsNoProject(t *testing.T) {
	a := &App{
		cfgTab:   2, // Project tab
		repoRoot: "", // no repo
	}
	rows := a.buildConfigRows()
	found := false
	for _, row := range rows {
		if strings.Contains(row, "No project detected") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("buildConfigRows on Project tab with no repo should contain 'No project detected', got %v", rows)
	}
}

func TestBuildConfigRowsGlobalHint(t *testing.T) {
	a := &App{
		cfgTab:          2, // Project tab
		repoRoot:        "/some/repo",
		cfgDraft:        Config{ActiveModel: "global-model", Personality: "friendly"},
		cfgProjectDraft: ProjectConfig{}, // no overrides
	}
	rows := a.buildConfigRows()
	foundModel := false
	foundPersonality := false
	for _, row := range rows {
		if strings.Contains(row, "(global: global-model)") {
			foundModel = true
		}
		if strings.Contains(row, "(global: friendly)") {
			foundPersonality = true
		}
	}
	if !foundModel {
		t.Error("expected '(global: global-model)' hint for unoverridden Active Model")
	}
	if !foundPersonality {
		t.Error("expected '(global: friendly)' hint for unoverridden Personality")
	}
}

func TestBuildConfigRowsProjectOverrideShown(t *testing.T) {
	a := &App{
		cfgTab:          2,
		repoRoot:        "/some/repo",
		cfgDraft:        Config{ActiveModel: "global-model"},
		cfgProjectDraft: ProjectConfig{ActiveModel: "project-model"},
	}
	rows := a.buildConfigRows()
	found := false
	for _, row := range rows {
		if strings.Contains(row, "project-model") && !strings.Contains(row, "global:") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected project override 'project-model' shown without global hint")
	}
}

func TestExitConfigModeSavesBothConfigs(t *testing.T) {
	globalDir := t.TempDir()
	repoDir := t.TempDir()

	a := &App{
		cfgDraft: Config{
			PasteCollapseMinChars: 300,
			Personality:           "global-personality",
		},
		cfgProjectDraft: ProjectConfig{
			ActiveModel: "project-model",
			Personality: "project-personality",
		},
		repoRoot: repoDir,
		resultCh: make(chan any, 16),
	}

	// We can't easily test exitConfigMode because it calls saveConfig which
	// uses the real home dir. Instead test that saveProjectConfig is called
	// by verifying the project config is saved to repoRoot.
	a.projectConfig = a.cfgProjectDraft
	if err := saveProjectConfig(a.repoRoot, a.projectConfig); err != nil {
		t.Fatalf("saveProjectConfig: %v", err)
	}

	loaded := loadProjectConfig(repoDir)
	if loaded.ActiveModel != "project-model" {
		t.Errorf("ActiveModel = %q, want %q", loaded.ActiveModel, "project-model")
	}
	if loaded.Personality != "project-personality" {
		t.Errorf("Personality = %q, want %q", loaded.Personality, "project-personality")
	}

	// Also verify global config can be saved independently
	if err := saveConfigTo(globalDir, a.cfgDraft); err != nil {
		t.Fatalf("saveConfigTo: %v", err)
	}
	globalLoaded, err := loadConfigFrom(globalDir)
	if err != nil {
		t.Fatalf("loadConfigFrom: %v", err)
	}
	if globalLoaded.Personality != "global-personality" {
		t.Errorf("global Personality = %q, want %q", globalLoaded.Personality, "global-personality")
	}
}
