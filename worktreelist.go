package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// worktreeList is the UI component for the /worktrees screen.
type worktreeList struct {
	items       []WorktreeInfo
	cursor      int
	scroll      int
	currentPath string // path of the current session's worktree (for marking)
	width       int
	height      int
}

const worktreeListChrome = 10 // border + padding + title + hint

func (l worktreeList) visibleRows() int {
	rows := l.height - worktreeListChrome
	if rows < 1 {
		rows = 1
	}
	return rows
}

func newWorktreeList(items []WorktreeInfo, currentPath string, width, height int) worktreeList {
	return worktreeList{
		items:       items,
		currentPath: currentPath,
		width:       width,
		height:      height,
	}
}

func (l *worktreeList) clampScroll() {
	vis := l.visibleRows()
	if l.cursor < l.scroll {
		l.scroll = l.cursor
	}
	if l.cursor >= l.scroll+vis {
		l.scroll = l.cursor - vis + 1
	}
	if l.scroll < 0 {
		l.scroll = 0
	}
}

func (l worktreeList) Update(msg tea.Msg) (worktreeList, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		l.width = msg.Width
		l.height = msg.Height
		l.clampScroll()

	case tea.KeyPressMsg:
		switch msg.String() {
		case "up", "k":
			if l.cursor > 0 {
				l.cursor--
				l.clampScroll()
			}
		case "down", "j":
			if l.cursor < len(l.items)-1 {
				l.cursor++
				l.clampScroll()
			}
		}
	}
	return l, nil
}

func (l worktreeList) View() string {
	titleStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#B88AFF")).
		Bold(true).
		PaddingLeft(2).
		PaddingBottom(1)

	if len(l.items) == 0 {
		var b strings.Builder
		b.WriteString(titleStyle.Render("Worktrees"))
		b.WriteString("\n")
		emptyStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#888888")).
			PaddingLeft(2)
		b.WriteString(emptyStyle.Render("No worktrees found."))
		b.WriteString("\n")
		return b.String()
	}

	scrollIndicator := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#555555")).
		PaddingLeft(2)
	hintStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#555555")).
		PaddingLeft(2).
		PaddingTop(1)
	hintKeyStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#7B3EC7"))

	selectedRowStyle := lipgloss.NewStyle().Background(lipgloss.Color("#2A1545"))

	var b strings.Builder
	b.WriteString(titleStyle.Render("Worktrees"))
	b.WriteString("\n")

	vis := l.visibleRows()
	end := l.scroll + vis
	if end > len(l.items) {
		end = len(l.items)
	}

	if l.scroll > 0 {
		b.WriteString(scrollIndicator.Render(fmt.Sprintf("  ↑ %d more", l.scroll)))
		b.WriteString("\n")
	}

	for i := l.scroll; i < end; i++ {
		wt := l.items[i]
		selected := i == l.cursor

		var cursorStr string
		nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))

		if selected {
			cursorStr = lipgloss.NewStyle().Foreground(lipgloss.Color("#B88AFF")).Render("▸ ")
			nameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#E0E0E0")).Bold(true)
		} else {
			cursorStr = lipgloss.NewStyle().Foreground(lipgloss.Color("#3A0066")).Render("  ")
		}

		// Build row content: branch name + badges
		var row strings.Builder
		row.WriteString(nameStyle.Render(wt.Branch))

		// Clean/dirty badge
		if wt.Clean {
			badge := lipgloss.NewStyle().Foreground(lipgloss.Color("#6FE7B8")).Render(" ✓")
			row.WriteString(badge)
		} else {
			badge := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF6B6B")).Render(" ●")
			row.WriteString(badge)
		}

		// Active badge
		if wt.Active {
			badge := lipgloss.NewStyle().Foreground(lipgloss.Color("#B88AFF")).Render(" [active]")
			row.WriteString(badge)
		}

		// Current session marker
		if wt.Path == l.currentPath {
			marker := lipgloss.NewStyle().Foreground(lipgloss.Color("#9B6ADE")).Bold(true).Render(" ← current")
			row.WriteString(marker)
		}

		b.WriteString(cursorStr)
		if selected {
			b.WriteString(selectedRowStyle.Render(row.String()))
		} else {
			b.WriteString(row.String())
		}
		b.WriteString("\n")
	}

	if end < len(l.items) {
		b.WriteString(scrollIndicator.Render(fmt.Sprintf("  ↓ %d more", len(l.items)-end)))
		b.WriteString("\n")
	}

	hint := fmt.Sprintf(
		"  %s navigate  %s close",
		hintKeyStyle.Render("↑/↓"),
		hintKeyStyle.Render("esc"),
	)
	b.WriteString(hintStyle.Render(hint))
	b.WriteString("\n")

	return b.String()
}
