package main

import (
	"os"
	"path/filepath"
	"strings"
)

// Skill represents a project-local skill loaded from a .md file.
type Skill struct {
	Name        string
	Description string
	Content     string
}

// loadSkills reads all .md files from the given directory and parses them
// into skills. Each file should have a simple front matter block delimited
// by "---" lines containing "name:" and "description:" fields, followed by
// the skill content. Missing or empty directories return an empty slice.
func loadSkills(dir string) ([]Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var skills []Skill
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}

		s, ok := parseSkill(string(data))
		if !ok {
			continue
		}
		skills = append(skills, s)
	}
	return skills, nil
}

// parseSkill extracts a Skill from a markdown file with front matter.
// Returns ok=false if the front matter is missing or lacks a name.
func parseSkill(raw string) (Skill, bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "---") {
		return Skill{}, false
	}

	// Find closing "---"
	rest := raw[3:]
	rest = strings.TrimLeft(rest, "\r\n")
	idx := strings.Index(rest, "---")
	if idx < 0 {
		return Skill{}, false
	}

	frontMatter := rest[:idx]
	content := strings.TrimSpace(rest[idx+3:])

	var s Skill
	for _, line := range strings.Split(frontMatter, "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, ":"); ok {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			switch k {
			case "name":
				s.Name = v
			case "description":
				s.Description = v
			}
		}
	}

	if s.Name == "" {
		return Skill{}, false
	}

	s.Content = content
	return s, true
}
