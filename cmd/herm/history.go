package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultMaxHistory = 100
	historyFileName   = "history"
)

type historyEntry struct {
	Timestamp int64  `json:"t"`
	Prompt    string `json:"p"`
}

type History struct {
	entries  []historyEntry
	maxSize  int
	index    int    // -1 = not navigating, 0 = most recent, increasing = older
	draft    string // saved current input when navigation starts
	filePath string
}

func newHistory(projectDir string, maxSize int) *History {
	if maxSize <= 0 {
		maxSize = defaultMaxHistory
	}
	return &History{
		maxSize:  maxSize,
		index:    -1,
		filePath: filepath.Join(projectDir, ".herm", historyFileName),
	}
}

func (h *History) Load() error {
	f, err := os.Open(h.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var entries []historyEntry
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var e historyEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	// Trim to maxSize, keep newest
	if len(entries) > h.maxSize {
		entries = entries[len(entries)-h.maxSize:]
	}
	h.entries = entries

	h.compactIfNeeded()
	return nil
}

func (h *History) Add(prompt string) {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return
	}

	// Consecutive dedup
	if len(h.entries) > 0 && h.entries[len(h.entries)-1].Prompt == prompt {
		return
	}

	e := historyEntry{
		Timestamp: time.Now().Unix(),
		Prompt:    prompt,
	}
	h.entries = append(h.entries, e)

	// Trim if over maxSize, drop oldest
	if len(h.entries) > h.maxSize {
		h.entries = h.entries[len(h.entries)-h.maxSize:]
	}

	h.appendToFile(e)

	// Reset navigation
	h.index = -1
	h.draft = ""
}

func (h *History) appendToFile(e historyEntry) {
	dir := filepath.Dir(h.filePath)
	_ = os.MkdirAll(dir, 0755)

	f, err := os.OpenFile(h.filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	data = append(data, '\n')
	_, _ = f.Write(data)
}

func (h *History) compactIfNeeded() {
	f, err := os.Open(h.filePath)
	if err != nil {
		return
	}
	defer f.Close()

	lines := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		lines++
	}

	if lines > 2*h.maxSize {
		h.rewrite()
	}
}

func (h *History) rewrite() {
	dir := filepath.Dir(h.filePath)
	_ = os.MkdirAll(dir, 0755)

	tmpPath := h.filePath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return
	}

	for _, e := range h.entries {
		data, err := json.Marshal(e)
		if err != nil {
			continue
		}
		data = append(data, '\n')
		_, _ = f.Write(data)
	}
	f.Close()

	_ = os.Rename(tmpPath, h.filePath)
}

func (h *History) Up(currentInput string) (string, bool) {
	if len(h.entries) == 0 {
		return "", false
	}

	if h.index == -1 {
		h.draft = currentInput
		h.index = 0
		return h.entries[len(h.entries)-1].Prompt, true
	}

	if h.index < len(h.entries)-1 {
		h.index++
		return h.entries[len(h.entries)-1-h.index].Prompt, true
	}

	return "", false
}

func (h *History) Down(currentInput string) (string, bool) {
	if h.index == -1 {
		return "", false
	}

	if h.index > 0 {
		h.index--
		return h.entries[len(h.entries)-1-h.index].Prompt, true
	}

	// index == 0, restore draft
	h.index = -1
	return h.draft, true
}

func (h *History) Reset() {
	h.index = -1
	h.draft = ""
}

func (h *History) IsNavigating() bool {
	return h.index != -1
}

func (h *History) Len() int {
	return len(h.entries)
}
