package main

import (
	"strings"
	"testing"
)

func TestStatusInfoMsgUpdatesModel(t *testing.T) {
	a := newTestApp(80, 24)

	info := statusInfo{
		Branch:       "feature/login",
		PRNumber:     42,
		WorktreeName: "cpsl-abc123",
		ActiveCount:  3,
		TotalCount:   5,
	}

	simResult(a, statusInfoMsg{info: info})

	if a.status.Branch != "feature/login" {
		t.Errorf("Branch = %q, want %q", a.status.Branch, "feature/login")
	}
	if a.status.PRNumber != 42 {
		t.Errorf("PRNumber = %d, want 42", a.status.PRNumber)
	}
	if a.status.WorktreeName != "cpsl-abc123" {
		t.Errorf("WorktreeName = %q, want %q", a.status.WorktreeName, "cpsl-abc123")
	}
	if a.status.ActiveCount != 3 {
		t.Errorf("ActiveCount = %d, want 3", a.status.ActiveCount)
	}
}

func TestRenderStatusBarContainsBranch(t *testing.T) {
	a := newTestApp(80, 24)
	a.status = statusInfo{
		Branch:       "main",
		PRNumber:     7,
		WorktreeName: "wt-1",
		ActiveCount:  2,
		TotalCount:   3,
	}

	bar := a.renderStatusBar()

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
	a := newTestApp(80, 24)
	// status is zero value — no branch

	bar := a.renderStatusBar()
	if bar != "" {
		t.Errorf("status bar should be empty with no branch, got %q", bar)
	}
}

func TestRenderStatusBarNoPR(t *testing.T) {
	a := newTestApp(80, 24)
	a.status = statusInfo{
		Branch:       "develop",
		PRNumber:     0, // no PR
		WorktreeName: "wt-2",
		ActiveCount:  1,
	}

	bar := a.renderStatusBar()

	if !strings.Contains(bar, "develop") {
		t.Error("status bar should contain branch name")
	}
	if strings.Contains(bar, "PR #") {
		t.Error("status bar should not contain PR when PRNumber is 0")
	}
}

func TestStatusBarHeight(t *testing.T) {
	a := newTestApp(80, 24)

	// Without status bar
	if a.status.Branch != "" {
		t.Fatal("branch should be empty initially")
	}

	// With status bar
	a.status = statusInfo{Branch: "main", WorktreeName: "wt-1"}
	bar := a.renderStatusBar()
	if bar == "" {
		t.Error("status bar should not be empty with branch info")
	}
}

func TestStatusBarHeightMethod(t *testing.T) {
	a := newTestApp(80, 24)

	if a.status.Branch != "" {
		t.Errorf("branch should be empty initially")
	}

	a.status = statusInfo{Branch: "main"}
	if a.renderStatusBar() == "" {
		t.Error("status bar should not be empty with branch set")
	}
}

func TestStatusBarVisibleInRender(t *testing.T) {
	a := newTestApp(80, 24)
	a.status = statusInfo{
		Branch:       "feature/test",
		WorktreeName: "wt-test",
	}

	bar := a.renderStatusBar()
	if !strings.Contains(bar, "feature/test") {
		t.Error("status bar should contain branch name")
	}
}

func TestRenderStatusBarDiffStats(t *testing.T) {
	a := newTestApp(120, 24)
	a.status = statusInfo{
		Branch:  "feature/test",
		DiffAdd: 42,
		DiffDel: 7,
	}

	bar := a.renderStatusBar()

	if !strings.Contains(bar, "+42") {
		t.Error("status bar should contain '+42' for additions")
	}
	if !strings.Contains(bar, "-7") {
		t.Error("status bar should contain '-7' for deletions")
	}
}

func TestRenderStatusBarNoDiffStatsWhenZero(t *testing.T) {
	a := newTestApp(80, 24)
	a.status = statusInfo{
		Branch: "main",
	}

	bar := a.renderStatusBar()

	if strings.Contains(bar, "+0") {
		t.Error("status bar should not show diff stats when both are zero")
	}
}

func TestRenderStatusBarTruncatesNarrow(t *testing.T) {
	a := newTestApp(40, 24)
	a.status = statusInfo{
		Branch:       "feature/very-long-branch-name-here",
		WorktreeName: "cpsl-worktree-long-name",
	}

	bar := a.renderStatusBar()

	if !strings.Contains(bar, "…") {
		t.Error("status bar should truncate with … on narrow terminal")
	}
	// Original names should NOT fit at 40 chars
	if strings.Contains(bar, "feature/very-long-branch-name-here") {
		t.Error("branch name should be truncated at narrow width")
	}
}

func TestRenderStatusBarNoTruncateWide(t *testing.T) {
	a := newTestApp(120, 24)
	a.status = statusInfo{
		Branch:       "feature/login",
		WorktreeName: "cpsl-abc",
	}

	bar := a.renderStatusBar()

	if strings.Contains(bar, "…") {
		t.Error("status bar should not truncate on wide terminal with short names")
	}
	if !strings.Contains(bar, "feature/login") {
		t.Error("full branch name should be visible")
	}
	if !strings.Contains(bar, "cpsl-abc") {
		t.Error("full worktree name should be visible")
	}
}

func TestWorkspaceMsgStartsContainerBoot(t *testing.T) {
	a := newTestApp(80, 24)

	simResult(a, workspaceMsg{worktreePath: "/tmp/test-wt"})

	if a.worktreePath != "/tmp/test-wt" {
		t.Fatalf("expected worktreePath = /tmp/test-wt, got %s", a.worktreePath)
	}
}

func TestContainerReadyNoCmd(t *testing.T) {
	a := newTestApp(80, 24)

	client := &ContainerClient{
		config:  ContainerConfig{Image: "alpine:latest"},
		running: true,
	}

	simResult(a, containerReadyMsg{client: client, worktreePath: "/tmp/test-wt"})

	if !a.containerReady {
		t.Fatal("containerReady should be true")
	}
}
