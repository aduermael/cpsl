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
