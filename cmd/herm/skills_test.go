package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSkillsValidDir(t *testing.T) {
	dir := t.TempDir()
	skillContent := `---
name: Testing
description: How to write tests
---

Always write table-driven tests.
`
	if err := os.WriteFile(filepath.Join(dir, "testing.md"), []byte(skillContent), 0644); err != nil {
		t.Fatal(err)
	}

	skills, err := loadSkills(dir)
	if err != nil {
		t.Fatalf("loadSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("got %d skills, want 1", len(skills))
	}
	if skills[0].Name != "Testing" {
		t.Errorf("Name = %q, want %q", skills[0].Name, "Testing")
	}
	if skills[0].Description != "How to write tests" {
		t.Errorf("Description = %q, want %q", skills[0].Description, "How to write tests")
	}
	if skills[0].Content != "Always write table-driven tests." {
		t.Errorf("Content = %q, want %q", skills[0].Content, "Always write table-driven tests.")
	}
}

func TestLoadSkillsMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.md", "b.md"} {
		content := "---\nname: " + name + "\ndescription: desc\n---\nbody"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	skills, err := loadSkills(dir)
	if err != nil {
		t.Fatalf("loadSkills: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("got %d skills, want 2", len(skills))
	}
}

func TestLoadSkillsEmptyDir(t *testing.T) {
	dir := t.TempDir()

	skills, err := loadSkills(dir)
	if err != nil {
		t.Fatalf("loadSkills: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("got %d skills, want 0", len(skills))
	}
}

func TestLoadSkillsMissingDir(t *testing.T) {
	skills, err := loadSkills("/nonexistent/path/skills")
	if err != nil {
		t.Fatalf("loadSkills: %v", err)
	}
	if skills != nil {
		t.Errorf("got %v, want nil", skills)
	}
}

func TestLoadSkillsMalformedFrontMatter(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name    string
		content string
	}{
		{"no_frontmatter.md", "just content, no front matter"},
		{"no_closing.md", "---\nname: Test\nmore content without closing"},
		{"no_name.md", "---\ndescription: missing name\n---\nbody"},
	}

	for _, tc := range cases {
		if err := os.WriteFile(filepath.Join(dir, tc.name), []byte(tc.content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	skills, err := loadSkills(dir)
	if err != nil {
		t.Fatalf("loadSkills: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("got %d skills, want 0 (all malformed)", len(skills))
	}
}

func TestLoadSkillsSkipsNonMarkdown(t *testing.T) {
	dir := t.TempDir()

	// Valid skill
	if err := os.WriteFile(filepath.Join(dir, "good.md"), []byte("---\nname: Good\ndescription: d\n---\nbody"), 0644); err != nil {
		t.Fatal(err)
	}
	// Non-markdown file
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not a skill"), 0644); err != nil {
		t.Fatal(err)
	}

	skills, err := loadSkills(dir)
	if err != nil {
		t.Fatalf("loadSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("got %d skills, want 1", len(skills))
	}
}

func TestParseSkillEmptyContent(t *testing.T) {
	raw := "---\nname: Empty\ndescription: no body\n---\n"
	s, ok := parseSkill(raw)
	if !ok {
		t.Fatal("parseSkill returned ok=false")
	}
	if s.Name != "Empty" {
		t.Errorf("Name = %q, want %q", s.Name, "Empty")
	}
	if s.Content != "" {
		t.Errorf("Content = %q, want empty", s.Content)
	}
}
