package main

import (
	"strings"
	"testing"
)

func TestStatusInfoMsgUpdatesModel(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	info := statusInfo{
		Branch:       "feature/login",
		PRNumber:     42,
		WorktreeName: "cpsl-abc123",
		ActiveCount:  3,
		TotalCount:   5,
	}

	result, _ := m.Update(statusInfoMsg{info: info})
	m = result.(model)

	if m.status.Branch != "feature/login" {
		t.Errorf("Branch = %q, want %q", m.status.Branch, "feature/login")
	}
	if m.status.PRNumber != 42 {
		t.Errorf("PRNumber = %d, want 42", m.status.PRNumber)
	}
	if m.status.WorktreeName != "cpsl-abc123" {
		t.Errorf("WorktreeName = %q, want %q", m.status.WorktreeName, "cpsl-abc123")
	}
	if m.status.ActiveCount != 3 {
		t.Errorf("ActiveCount = %d, want 3", m.status.ActiveCount)
	}
}

func TestRenderStatusBarContainsBranch(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)
	m.status = statusInfo{
		Branch:       "main",
		PRNumber:     7,
		WorktreeName: "wt-1",
		ActiveCount:  2,
		TotalCount:   3,
	}

	bar := m.renderStatusBar()

	if !strings.Contains(bar, "main") {
		t.Error("status bar should contain branch name 'main'")
	}
	if !strings.Contains(bar, "PR #7") {
		t.Error("status bar should contain 'PR #7'")
	}
	if !strings.Contains(bar, "wt-1") {
		t.Error("status bar should contain worktree name")
	}
	if !strings.Contains(bar, "2/3") {
		t.Error("status bar should contain worktree count '2/3'")
	}
}

func TestRenderStatusBarEmptyWhenNoBranch(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)
	// status is zero value — no branch

	bar := m.renderStatusBar()
	if bar != "" {
		t.Errorf("status bar should be empty with no branch, got %q", bar)
	}
}

func TestRenderStatusBarNoPR(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)
	m.status = statusInfo{
		Branch:       "develop",
		PRNumber:     0, // no PR
		WorktreeName: "wt-2",
		ActiveCount:  1,
	}

	bar := m.renderStatusBar()

	if !strings.Contains(bar, "develop") {
		t.Error("status bar should contain branch name")
	}
	if strings.Contains(bar, "PR #") {
		t.Error("status bar should not contain PR when PRNumber is 0")
	}
}

func TestViewportHeightReducedByStatusBar(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	heightWithout := m.viewportHeight()

	// Set status info so status bar appears
	m.status = statusInfo{Branch: "main", WorktreeName: "wt-1"}

	heightWith := m.viewportHeight()

	if heightWith != heightWithout-1 {
		t.Errorf("viewport height with status bar = %d, want %d (one less than %d)",
			heightWith, heightWithout-1, heightWithout)
	}
}

func TestStatusBarHeightMethod(t *testing.T) {
	m := initialModel()

	if h := m.statusBarHeight(); h != 0 {
		t.Errorf("statusBarHeight with no branch = %d, want 0", h)
	}

	m.status = statusInfo{Branch: "main"}
	if h := m.statusBarHeight(); h != 1 {
		t.Errorf("statusBarHeight with branch = %d, want 1", h)
	}
}

func TestStatusBarVisibleInView(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)
	m.status = statusInfo{
		Branch:       "feature/test",
		WorktreeName: "wt-test",
	}

	v := m.View()
	if !strings.Contains(v.Content, "feature/test") {
		t.Error("View should contain status bar with branch name")
	}
}

func TestContainerReadyChainsFetchStatus(t *testing.T) {
	m := initialModel()
	m = resize(m, 80, 24)

	client := &ContainerClient{
		config:  ContainerConfig{Image: "alpine:latest"},
		running: true,
	}

	_, cmd := m.Update(containerReadyMsg{client: client, worktreePath: "/tmp/test-wt"})

	if cmd == nil {
		t.Fatal("containerReadyMsg should return a cmd to fetch status")
	}
}
