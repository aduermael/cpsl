// content.go handles content and attachment processing: paste expansion,
// file-path detection, MIME type mapping, attachment placeholder expansion,
// tool call summarisation, and diff/tool-result collapsing.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ─── Attachment types ───

// Attachment holds metadata and encoded content for a file or image attached to a message.
type Attachment struct {
	Path      string // original file path (may be temp path for clipboard images)
	MediaType string // MIME type (e.g. "image/png", "application/pdf")
	Data      string // base64-encoded file content
	IsImage   bool   // whether this is an image attachment
}

// ─── Paste expansion ───

var pasteplaceholderRe = regexp.MustCompile(`\[pasted #(\d+) \| \d+ chars\]`)

func expandPastes(s string, store map[int]string) string {
	return pasteplaceholderRe.ReplaceAllStringFunc(s, func(match string) string {
		sub := pasteplaceholderRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		id, err := strconv.Atoi(sub[1])
		if err != nil {
			return match
		}
		if content, ok := store[id]; ok {
			return content
		}
		return match
	})
}

// ─── Attachment helpers ───

// isFilePath reports whether s looks like an absolute file path that exists on disk.
// It handles surrounding quotes (some terminals wrap dropped paths in double or
// single quotes), shell-escaped paths (backslash-spaces from terminal drag-drop),
// and tilde-prefixed home-dir paths.
func isFilePath(s string) (string, bool) {
	// Trim surrounding whitespace first — some terminals pad dropped paths with spaces.
	s = strings.TrimSpace(s)
	// Strip surrounding double-quotes (some terminals wrap dropped paths in quotes).
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	// Strip surrounding single-quotes (some terminals wrap dropped paths in single quotes).
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		s = s[1 : len(s)-1]
	}
	// Trim again — spaces may appear inside the quotes (e.g. `" /path/to/file "`)
	s = strings.TrimSpace(s)
	// Unescape backslash-spaces (common in terminal drag-drop).
	p := strings.ReplaceAll(s, "\\ ", " ")
	// Expand tilde.
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	if !filepath.IsAbs(p) {
		return "", false
	}
	info, err := os.Stat(p)
	if err != nil || info.IsDir() {
		return "", false
	}
	return p, true
}

// isImageExt reports whether the file extension indicates an image format.
func isImageExt(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tiff", ".svg":
		return true
	}
	return false
}

// mimeForExt returns the MIME type for a file based on its extension.
func mimeForExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".tiff":
		return "image/tiff"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".txt":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv"
	default:
		return "application/octet-stream"
	}
}

// attachmentPlaceholderRe matches [Image #N] and [File #N] placeholders.
var attachmentPlaceholderRe = regexp.MustCompile(`\[(Image|File) #(\d+)\]`)

// expandAttachments takes a message string (already paste-expanded) and the
// attachment store. If there are no attachment placeholders it returns the
// string as-is. Otherwise it splits the text on placeholders and builds a JSON
// content-block array: text segments become {"type":"text"} blocks, image
// attachments become {"type":"image"} blocks, and file attachments become
// {"type":"document"} blocks. The returned JSON string is understood by
// langdag's contentToRawMessage() which passes arrays through as-is.
func expandAttachments(s string, store map[int]Attachment) string {
	if len(store) == 0 {
		return s
	}
	locs := attachmentPlaceholderRe.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return s
	}

	type block map[string]string
	var blocks []block

	addText := func(t string) {
		if t != "" {
			blocks = append(blocks, block{"type": "text", "text": t})
		}
	}

	prev := 0
	for _, loc := range locs {
		// loc[0..1] = full match, loc[2..3] = kind (Image/File), loc[4..5] = ID
		addText(s[prev:loc[0]])
		idStr := s[loc[4]:loc[5]]
		id, err := strconv.Atoi(idStr)
		if err != nil {
			addText(s[loc[0]:loc[1]])
			prev = loc[1]
			continue
		}
		att, ok := store[id]
		if !ok {
			addText(s[loc[0]:loc[1]])
			prev = loc[1]
			continue
		}
		if att.IsImage {
			blocks = append(blocks, block{
				"type":       "image",
				"media_type": att.MediaType,
				"data":       att.Data,
			})
		} else {
			blocks = append(blocks, block{
				"type":       "document",
				"media_type": att.MediaType,
				"data":       att.Data,
			})
		}
		prev = loc[1]
	}
	addText(s[prev:])

	out, err := json.Marshal(blocks)
	if err != nil {
		return s
	}
	return string(out)
}

// ─── Tool call suppression helpers ───

// isAgentStatusCheck returns true if the tool call is an "agent" tool
// with task:"status" — these are internal polling calls whose info is
// already shown in the sub-agent display and status line.
func isAgentStatusCheck(toolName string, input json.RawMessage) bool {
	if toolName != "agent" {
		return false
	}
	var in struct {
		Task string `json:"task"`
	}
	if json.Unmarshal(input, &in) == nil && in.Task == "status" {
		return true
	}
	return false
}

// ─── Tool result helpers ───

func toolCallSummary(toolName string, input json.RawMessage) string {
	switch toolName {
	case "bash":
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &in) == nil && in.Command != "" {
			cmd := in.Command
			if len(cmd) > 120 {
				cmd = cmd[:120] + "..."
			}
			return fmt.Sprintf("~ $ %s", cmd)
		}
	case "git":
		var in struct {
			Subcommand string   `json:"subcommand"`
			Args       []string `json:"args,omitempty"`
		}
		if json.Unmarshal(input, &in) == nil && in.Subcommand != "" {
			parts := append([]string{"git", in.Subcommand}, in.Args...)
			cmd := strings.Join(parts, " ")
			if len(cmd) > 120 {
				cmd = cmd[:120] + "..."
			}
			return fmt.Sprintf("~ %s", cmd)
		}
	case "edit_file":
		var in struct {
			FilePath string `json:"file_path"`
		}
		if json.Unmarshal(input, &in) == nil && in.FilePath != "" {
			p := in.FilePath
			if len(p) > 100 {
				p = "..." + p[len(p)-97:]
			}
			return fmt.Sprintf("~ edit %s", p)
		}
	case "write_file":
		var in struct {
			FilePath string `json:"file_path"`
		}
		if json.Unmarshal(input, &in) == nil && in.FilePath != "" {
			p := in.FilePath
			if len(p) > 100 {
				p = "..." + p[len(p)-97:]
			}
			return fmt.Sprintf("~ write %s", p)
		}
	}
	return fmt.Sprintf("~ %s", toolName)
}

// approvalCmdDesc formats a tool call as a terminal command string for the
// approval detail line. For known tools (bash, git) it reconstructs the
// command; for unknown tools it falls back to "name: {json}".
func approvalCmdDesc(toolName string, input json.RawMessage) string {
	switch toolName {
	case "bash":
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &in) == nil && in.Command != "" {
			cmd := in.Command
			if len(cmd) > 80 {
				cmd = cmd[:80] + "..."
			}
			return cmd
		}
	case "git":
		var in struct {
			Subcommand string   `json:"subcommand"`
			Args       []string `json:"args,omitempty"`
		}
		if json.Unmarshal(input, &in) == nil && in.Subcommand != "" {
			parts := append([]string{"git", in.Subcommand}, in.Args...)
			cmd := strings.Join(parts, " ")
			if len(cmd) > 80 {
				cmd = cmd[:80] + "..."
			}
			return cmd
		}
	}
	return fmt.Sprintf("%s: %s", toolName, string(input))
}

// approvalShortDesc creates a short summary of a tool call for approval prompts.
// It extracts key info from the tool name and input (similar to toolCallSummary).
func approvalShortDesc(toolName string, input json.RawMessage) string {
	switch toolName {
	case "bash":
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &in) == nil && in.Command != "" {
			cmd := in.Command
			if len(cmd) > 80 {
				cmd = cmd[:80] + "..."
			}
			return fmt.Sprintf("bash: %s", cmd)
		}
	case "git":
		var in struct {
			Subcommand string `json:"subcommand"`
		}
		if json.Unmarshal(input, &in) == nil && in.Subcommand != "" {
			return "git " + in.Subcommand
		}
	}
	return toolName
}

func collapseToolResult(result string) string {
	lines := strings.Split(result, "\n")
	if len(lines) <= 4 {
		return compactLineNumbers(result)
	}
	if len(lines) == 5 {
		// Show first 2 + last 3 (all 5, no ellipsis needed)
		return compactLineNumbers(strings.Join(lines[:2], "\n") + "\n" + strings.Join(lines[2:], "\n"))
	}
	// For unified diffs: show header + first hunk preview.
	if isDiffContent(result) {
		return collapseDiff(lines)
	}
	// >5: first 2 + ... + last 2
	head := strings.Join(lines[:2], "\n")
	tail := strings.Join(lines[len(lines)-2:], "\n")
	return compactLineNumbers(fmt.Sprintf("%s\n...\n%s", head, tail))
}

// collapseDiff collapses a unified diff while preserving enough context
// to see the actual changes. Shows up to 20 lines before truncating.
func collapseDiff(lines []string) string {
	show := 20
	if len(lines) <= show {
		return strings.Join(lines, "\n")
	}
	head := strings.Join(lines[:show], "\n")
	remaining := len(lines) - show
	return fmt.Sprintf("%s\n... (%d more lines)", head, remaining)
}

// catNPadRe matches cat-n style line numbers with leading whitespace (e.g. "     1\t").
var catNPadRe = regexp.MustCompile(`(?m)^ +(\d+)\t`)

// compactLineNumbers strips excess leading whitespace from cat-n style
// line-numbered output (e.g. "     1\tcode" → "1 code") for display.
func compactLineNumbers(s string) string {
	return catNPadRe.ReplaceAllString(s, "$1 ")
}

// isDiffContent returns true if the content appears to be a unified diff.
func isDiffContent(content string) bool {
	return strings.Contains(content, "\n@@ ") || strings.HasPrefix(content, "@@ ")
}

// diffLineStyle returns the ANSI style for a unified diff line.
// Returns empty string if the line should use the default content style.
func diffLineStyle(line string) string {
	if strings.HasPrefix(line, "@@") {
		return "\033[2;36m" // dim cyan
	}
	if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
		return "\033[2;1m" // dim bold
	}
	if strings.HasPrefix(line, "+") {
		return "\033[2;32m" // dim green
	}
	if strings.HasPrefix(line, "-") {
		return "\033[2;31m" // dim red
	}
	return ""
}
