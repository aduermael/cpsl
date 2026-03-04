package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
)

// branchList is the UI component for the /branches screen.
type branchList struct {
	items         []string // all branch names
	filtered      []string // branches matching the current filter
	cursor        int
	scroll        int
	filterInput   textinput.Model
	currentBranch string // the branch checked out in the current worktree
	width         int
	height        int
}

// branchSelected is returned when the user presses Enter on a branch.
type branchSelected struct {
	name string
}

const branchListChrome = 12 // border + padding + title + filter input + hint

func (l branchList) visibleRows() int {
	rows := l.height - branchListChrome
	if rows < 1 {
		rows = 1
	}
	return rows
}

func newBranchList(items []string, currentBranch string, width, height int) branchList {
	ti := textinput.New()
	ti.Placeholder = "Filter branches..."
	ti.Prompt = "  / "
	ti.Focus()

	s := ti.Styles()
	s.Focused.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color("#7B3EC7"))
	s.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color("#E0E0E0"))
	s.Focused.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("#555555"))
	ti.SetStyles(s)

	bl := branchList{
		items:         items,
		filtered:      items,
		filterInput:   ti,
		currentBranch: currentBranch,
		width:         width,
		height:        height,
	}
	return bl
}

func (l *branchList) clampScroll() {
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

func (l *branchList) applyFilter() {
	query := strings.ToLower(l.filterInput.Value())
	if query == "" {
		l.filtered = l.items
	} else {
		var matches []string
		for _, b := range l.items {
			if strings.Contains(strings.ToLower(b), query) {
				matches = append(matches, b)
			}
		}
		l.filtered = matches
	}
	l.cursor = 0
	l.scroll = 0
}

func (l branchList) Update(msg tea.Msg) (branchList, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		l.width = msg.Width
		l.height = msg.Height
		l.clampScroll()

	case tea.KeyPressMsg:
		switch msg.String() {
		case "up":
			if l.cursor > 0 {
				l.cursor--
				l.clampScroll()
			}
			return l, nil
		case "down":
			if l.cursor < len(l.filtered)-1 {
				l.cursor++
				l.clampScroll()
			}
			return l, nil
		case "enter":
			if len(l.filtered) > 0 && l.cursor < len(l.filtered) {
				return l, func() tea.Msg {
					return branchSelected{name: l.filtered[l.cursor]}
				}
			}
			return l, nil
		}
	}

	// Forward to text input for filter typing
	var cmd tea.Cmd
	prevValue := l.filterInput.Value()
	l.filterInput, cmd = l.filterInput.Update(msg)
	if l.filterInput.Value() != prevValue {
		l.applyFilter()
	}
	return l, cmd
}

func (l branchList) View() string {
	titleStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#B88AFF")).
		Bold(true).
		PaddingLeft(2).
		PaddingBottom(1)

	if len(l.items) == 0 {
		var b strings.Builder
		b.WriteString(titleStyle.Render("Branches"))
		b.WriteString("\n")
		emptyStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#888888")).
			PaddingLeft(2)
		b.WriteString(emptyStyle.Render("No branches found."))
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
	b.WriteString(titleStyle.Render("Branches"))
	b.WriteString("\n")

	// Filter input
	b.WriteString(l.filterInput.View())
	b.WriteString("\n\n")

	// Empty filter results
	if len(l.filtered) == 0 {
		emptyStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#888888")).
			PaddingLeft(2)
		b.WriteString(emptyStyle.Render("No matching branches."))
		b.WriteString("\n")
	} else {
		vis := l.visibleRows()
		end := l.scroll + vis
		if end > len(l.filtered) {
			end = len(l.filtered)
		}

		if l.scroll > 0 {
			b.WriteString(scrollIndicator.Render(fmt.Sprintf("  ↑ %d more", l.scroll)))
			b.WriteString("\n")
		}

		for i := l.scroll; i < end; i++ {
			branch := l.filtered[i]
			selected := i == l.cursor

			var cursorStr string
			nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))

			if selected {
				cursorStr = lipgloss.NewStyle().Foreground(lipgloss.Color("#B88AFF")).Render("▸ ")
				nameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#E0E0E0")).Bold(true)
			} else {
				cursorStr = lipgloss.NewStyle().Foreground(lipgloss.Color("#3A0066")).Render("  ")
			}

			var row strings.Builder
			row.WriteString(nameStyle.Render(branch))

			// Current branch marker
			if branch == l.currentBranch {
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

		if end < len(l.filtered) {
			b.WriteString(scrollIndicator.Render(fmt.Sprintf("  ↓ %d more", len(l.filtered)-end)))
			b.WriteString("\n")
		}
	}

	hint := fmt.Sprintf(
		"  %s filter  %s navigate  %s checkout  %s close",
		hintKeyStyle.Render("type"),
		hintKeyStyle.Render("↑/↓"),
		hintKeyStyle.Render("enter"),
		hintKeyStyle.Render("esc"),
	)
	b.WriteString(hintStyle.Render(hint))
	b.WriteString("\n")

	return b.String()
}
