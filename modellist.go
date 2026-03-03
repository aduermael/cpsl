package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// modelList is the UI component for the /model selection screen.
type modelList struct {
	models      []ModelDef
	cursor      int
	activeModel string // currently active model ID (for highlighting)
	width       int
	height      int
}

func newModelList(models []ModelDef, activeModel string, width, height int) modelList {
	// Position cursor on the active model if it exists in the list
	cursor := 0
	for i, m := range models {
		if m.ID == activeModel {
			cursor = i
			break
		}
	}
	return modelList{
		models:      models,
		cursor:      cursor,
		activeModel: activeModel,
		width:       width,
		height:      height,
	}
}

func (l modelList) Update(msg tea.Msg) (modelList, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		l.width = msg.Width
		l.height = msg.Height

	case tea.KeyPressMsg:
		switch msg.String() {
		case "up", "k":
			if l.cursor > 0 {
				l.cursor--
			}
		case "down", "j":
			if l.cursor < len(l.models)-1 {
				l.cursor++
			}
		}
	}
	return l, nil
}

// selected returns the model under the cursor.
func (l modelList) selected() ModelDef {
	return l.models[l.cursor]
}

func (l modelList) View() string {
	titleStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#B88AFF")).
		Bold(true).
		PaddingLeft(2).
		PaddingBottom(1)

	providerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#666666")).
		Italic(true)

	activeMarker := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6FE7B8")).
		Bold(true)

	hintStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#555555")).
		PaddingLeft(2).
		PaddingTop(1)

	hintKeyStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#7B3EC7"))

	var b strings.Builder
	b.WriteString(titleStyle.Render("⚡ Select Model"))
	b.WriteString("\n")

	for i, m := range l.models {
		cursorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#3A0066"))
		labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))

		cursor := cursorStyle.Render("  ")
		if i == l.cursor {
			cursor = lipgloss.NewStyle().Foreground(lipgloss.Color("#B88AFF")).Render("▸ ")
			labelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#E0E0E0")).Bold(true)
		}

		b.WriteString(cursor)
		b.WriteString(labelStyle.Render(m.DisplayName))
		b.WriteString("  ")
		b.WriteString(providerStyle.Render(m.Provider))

		if m.ID == l.activeModel {
			b.WriteString("  ")
			b.WriteString(activeMarker.Render("●"))
		}
		b.WriteString("\n")
	}

	hint := fmt.Sprintf(
		"  %s select  %s cancel",
		hintKeyStyle.Render("enter"),
		hintKeyStyle.Render("esc"),
	)
	b.WriteString(hintStyle.Render(hint))
	b.WriteString("\n")

	return b.String()
}
