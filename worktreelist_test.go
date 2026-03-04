package main

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestWorktreeListNavigation(t *testing.T) {
	items := []WorktreeInfo{
		{Path: "/wt/a", Branch: "branch-a", Clean: true, Active: false},
		{Path: "/wt/b", Branch: "branch-b", Clean: false, Active: true},
		{Path: "/wt/c", Branch: "branch-c", Clean: true, Active: false},
	}
	wl := newWorktreeList(items, "/wt/a", 80, 24)

	if wl.cursor != 0 {
		t.Fatalf("initial cursor = %d, want 0", wl.cursor)
	}

	// Move down
	wl, _ = wl.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if wl.cursor != 1 {
		t.Errorf("after down: cursor = %d, want 1", wl.cursor)
	}

	// Move down again
	wl, _ = wl.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if wl.cursor != 2 {
		t.Errorf("after second down: cursor = %d, want 2", wl.cursor)
	}

	// Down at bottom stays at bottom
	wl, _ = wl.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if wl.cursor != 2 {
		t.Errorf("down at bottom: cursor = %d, want 2", wl.cursor)
	}

	// Move up
	wl, _ = wl.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if wl.cursor != 1 {
		t.Errorf("after up: cursor = %d, want 1", wl.cursor)
	}

	// j/k navigation
	wl, _ = wl.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	if wl.cursor != 0 {
		t.Errorf("after k: cursor = %d, want 0", wl.cursor)
	}

	wl, _ = wl.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if wl.cursor != 1 {
		t.Errorf("after j: cursor = %d, want 1", wl.cursor)
	}
}

func TestWorktreeListEscReturnsToChat(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	m.mode = modeWorktrees
	m.worktreeListC = newWorktreeList([]WorktreeInfo{
		{Path: "/wt/a", Branch: "main", Clean: true},
	}, "", 80, 24)

	result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = result.(model)

	if m.mode != modeChat {
		t.Errorf("mode = %d, want modeChat (%d)", m.mode, modeChat)
	}
}

func TestWorktreeListCurrentMarked(t *testing.T) {
	items := []WorktreeInfo{
		{Path: "/wt/a", Branch: "branch-a", Clean: true},
		{Path: "/wt/b", Branch: "branch-b", Clean: true},
	}
	wl := newWorktreeList(items, "/wt/a", 80, 24)
	view := wl.View()

	if !strings.Contains(view, "current") {
		t.Error("view should contain 'current' marker for the current worktree")
	}
}

func TestWorktreeListCleanDirtyDisplay(t *testing.T) {
	items := []WorktreeInfo{
		{Path: "/wt/clean", Branch: "clean-branch", Clean: true},
		{Path: "/wt/dirty", Branch: "dirty-branch", Clean: false},
	}
	wl := newWorktreeList(items, "", 80, 24)
	view := wl.View()

	// Clean worktree should show checkmark
	if !strings.Contains(view, "✓") {
		t.Error("view should contain ✓ for clean worktree")
	}
	// Dirty worktree should show dot indicator
	if !strings.Contains(view, "●") {
		t.Error("view should contain ● for dirty worktree")
	}
}

func TestWorktreeListActiveDisplay(t *testing.T) {
	items := []WorktreeInfo{
		{Path: "/wt/a", Branch: "branch-a", Clean: true, Active: true},
		{Path: "/wt/b", Branch: "branch-b", Clean: true, Active: false},
	}
	wl := newWorktreeList(items, "", 80, 24)
	view := wl.View()

	if !strings.Contains(view, "[active]") {
		t.Error("view should contain [active] for active worktree")
	}
}

func TestWorktreeListEmpty(t *testing.T) {
	wl := newWorktreeList(nil, "", 80, 24)
	view := wl.View()

	if !strings.Contains(view, "No worktrees found") {
		t.Error("empty list should show 'No worktrees found'")
	}
}

func TestWorktreeListMsgPopulatesList(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)
	m.mode = modeWorktrees
	m.worktreePath = "/wt/current"

	items := []WorktreeInfo{
		{Path: "/wt/current", Branch: "main", Clean: true, Active: true},
		{Path: "/wt/other", Branch: "feature", Clean: false, Active: false},
	}
	result, _ := m.Update(worktreeListMsg{items: items})
	m = result.(model)

	if len(m.worktreeListC.items) != 2 {
		t.Errorf("worktreeList items = %d, want 2", len(m.worktreeListC.items))
	}
	if m.worktreeListC.currentPath != "/wt/current" {
		t.Errorf("currentPath = %q, want %q", m.worktreeListC.currentPath, "/wt/current")
	}
}

func TestWorktreeListMsgError(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)
	m.mode = modeWorktrees

	result, _ := m.Update(worktreeListMsg{err: fmt.Errorf("not in git repo")})
	m = result.(model)

	if m.mode != modeChat {
		t.Errorf("mode = %d, want modeChat after error", m.mode)
	}
	if len(m.messages) == 0 {
		t.Fatal("should have error message")
	}
	last := m.messages[len(m.messages)-1]
	if last.kind != msgError {
		t.Errorf("last message kind = %d, want msgError", last.kind)
	}
}

func TestWorktreeCommandAutocomplete(t *testing.T) {
	matches := filterCommands("/wor")
	if len(matches) != 1 || matches[0] != "/worktrees" {
		t.Errorf("filterCommands('/wor') = %v, want [/worktrees]", matches)
	}
}
