// tooldesc.go loads tool descriptions from embedded markdown files in
// prompts/tools/. Each file has frontmatter (name, description) plus a body
// of guidance; combined, they form a tool's Definition().Description.
package main

import (
	"encoding/json"
	"fmt"
	"herm/prompts"
	"sort"
	"strings"
)

// ToolDesc holds a parsed tool description from a markdown file.
type ToolDesc struct {
	Name        string // from frontmatter "name:" field
	Brief       string // from frontmatter "description:" field (1-line)
	Full        string // brief + "\n\n" + body (the complete description for the tool)
}

// toolDescriptions is the package-level cache of loaded tool descriptions.
// Initialized by loadToolDescriptions() at startup.
var toolDescriptions map[string]ToolDesc

// loadToolDescriptionsOptions is the parameter bundle for loadToolDescriptions.
type loadToolDescriptionsOptions struct {
	containerImage  string
	workDir         string
	exploreMaxTurns int
	generalMaxTurns int
}

// loadToolDescriptions reads all markdown files from the embedded prompts/tools/
// directory and returns a map keyed by tool name. Dynamic placeholders
// (__CONTAINER_IMAGE__, __WORK_DIR__, __EXPLORE_MAX_TURNS__, __GENERAL_MAX_TURNS__)
// are replaced with the provided values.
func loadToolDescriptions(opts loadToolDescriptionsOptions) map[string]ToolDesc {
	exploreMaxTurns := opts.exploreMaxTurns
	generalMaxTurns := opts.generalMaxTurns
	if exploreMaxTurns <= 0 {
		exploreMaxTurns = defaultExploreMaxTurns
	}
	if generalMaxTurns <= 0 {
		generalMaxTurns = defaultGeneralMaxTurns
	}

	entries, err := prompts.ToolDescFS.ReadDir("tools")
	if err != nil {
		return nil
	}

	descs := make(map[string]ToolDesc, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}

		data, err := prompts.ToolDescFS.ReadFile("tools/" + e.Name())
		if err != nil {
			continue
		}

		td, ok := parseToolDesc(string(data))
		if !ok {
			continue
		}

		// Replace dynamic placeholders.
		if opts.containerImage != "" {
			td.Full = strings.ReplaceAll(td.Full, "__CONTAINER_IMAGE__", opts.containerImage)
			td.Brief = strings.ReplaceAll(td.Brief, "__CONTAINER_IMAGE__", opts.containerImage)
		}
		if opts.workDir != "" {
			td.Full = strings.ReplaceAll(td.Full, "__WORK_DIR__", opts.workDir)
			td.Brief = strings.ReplaceAll(td.Brief, "__WORK_DIR__", opts.workDir)
		}
		// Per-mode budget placeholders.
		exploreStr := fmt.Sprintf("%d", exploreMaxTurns)
		generalStr := fmt.Sprintf("%d", generalMaxTurns)
		td.Full = strings.ReplaceAll(td.Full, "__EXPLORE_MAX_TURNS__", exploreStr)
		td.Brief = strings.ReplaceAll(td.Brief, "__EXPLORE_MAX_TURNS__", exploreStr)
		td.Full = strings.ReplaceAll(td.Full, "__GENERAL_MAX_TURNS__", generalStr)
		td.Brief = strings.ReplaceAll(td.Brief, "__GENERAL_MAX_TURNS__", generalStr)

		descs[td.Name] = td
	}
	return descs
}

// parseToolDesc extracts a ToolDesc from a markdown file with frontmatter.
// Uses the same frontmatter format as skills.go: --- delimited block with
// name: and description: fields, followed by body content.
// Returns ok=false if frontmatter is missing or lacks a name.
func parseToolDesc(raw string) (ToolDesc, bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "---") {
		return ToolDesc{}, false
	}

	rest := raw[3:]
	rest = strings.TrimLeft(rest, "\r\n")
	idx := strings.Index(rest, "---")
	if idx < 0 {
		return ToolDesc{}, false
	}

	frontMatter := rest[:idx]
	body := strings.TrimSpace(rest[idx+3:])

	var td ToolDesc
	for _, line := range strings.Split(frontMatter, "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, ":"); ok {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			switch k {
			case "name":
				td.Name = v
			case "description":
				td.Brief = v
			}
		}
	}

	if td.Name == "" {
		return ToolDesc{}, false
	}

	// Full description: brief line + body (if present).
	if body != "" {
		td.Full = body
	} else {
		td.Full = td.Brief
	}

	return td, true
}

// toolParamNames extracts the property names from a JSON Schema InputSchema.
// Returns a sorted list of parameter names. If the schema is nil or cannot be
// parsed, returns nil.
func toolParamNames(schema json.RawMessage) []string {
	if len(schema) == 0 {
		return nil
	}
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &s); err != nil || len(s.Properties) == 0 {
		return nil
	}
	names := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// getToolDescriptionOptions is the parameter bundle for getToolDescription.
type getToolDescriptionOptions struct {
	name     string
	fallback string
}

// getToolDescription returns the full description for a named tool,
// falling back to the provided default if not found in the loaded descriptions.
func getToolDescription(opts getToolDescriptionOptions) string {
	if toolDescriptions == nil {
		return opts.fallback
	}
	if td, ok := toolDescriptions[opts.name]; ok {
		return td.Full
	}
	return opts.fallback
}
