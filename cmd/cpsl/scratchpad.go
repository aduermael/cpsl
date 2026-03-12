package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"langdag.com/langdag/types"
)

// Scratchpad is a shared in-memory notepad for inter-agent communication.
// Concurrent-safe; lives for the duration of the session (cleared on /clear).
type Scratchpad struct {
	mu      sync.Mutex
	entries []string
}

// Read returns a copy of all entries.
func (s *Scratchpad) Read() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.entries))
	copy(out, s.entries)
	return out
}

// Write appends a single entry.
func (s *Scratchpad) Write(entry string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
}

// Clear removes all entries.
func (s *Scratchpad) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
}

// Replace replaces all entries with the given set.
func (s *Scratchpad) Replace(entries []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = make([]string, len(entries))
	copy(s.entries, entries)
}

// ScratchpadTool exposes the shared scratchpad to agents.
type ScratchpadTool struct {
	pad *Scratchpad
}

func NewScratchpadTool(pad *Scratchpad) *ScratchpadTool {
	return &ScratchpadTool{pad: pad}
}

func (t *ScratchpadTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{
		Name:        "scratchpad",
		Description: "Shared memory between you and sub-agents. Use to store key findings, decisions, or context that other agents need. Actions: 'read' returns all entries, 'write' appends a new entry, 'clear' replaces all entries with a single summary you provide.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"enum": ["read", "write", "clear"],
					"description": "read: view all entries, write: append an entry, clear: replace all entries with a summary"
				},
				"content": {
					"type": "string",
					"description": "The entry to write (for 'write') or summary to replace all entries with (for 'clear')"
				}
			},
			"required": ["action"]
		}`),
	}
}

func (t *ScratchpadTool) RequiresApproval(_ json.RawMessage) bool {
	return false
}

type scratchpadInput struct {
	Action  string `json:"action"`
	Content string `json:"content,omitempty"`
}

func (t *ScratchpadTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in scratchpadInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid scratchpad input: %w", err)
	}

	switch in.Action {
	case "read":
		entries := t.pad.Read()
		if len(entries) == 0 {
			return "(scratchpad is empty)", nil
		}
		var b strings.Builder
		for i, e := range entries {
			fmt.Fprintf(&b, "%d. %s\n", i+1, e)
		}
		return b.String(), nil

	case "write":
		if in.Content == "" {
			return "", fmt.Errorf("content is required for write")
		}
		t.pad.Write(in.Content)
		count := len(t.pad.Read())
		return fmt.Sprintf("Entry added (%d total).", count), nil

	case "clear":
		if in.Content == "" {
			t.pad.Clear()
			return "Scratchpad cleared.", nil
		}
		t.pad.Replace([]string{in.Content})
		return "Scratchpad replaced with summary (1 entry).", nil

	default:
		return "", fmt.Errorf("unknown action %q: must be read, write, or clear", in.Action)
	}
}
