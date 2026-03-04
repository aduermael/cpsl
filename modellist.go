package main

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// sortColumn identifies which column the model list is sorted by.
type sortColumn int

const (
	colName     sortColumn = iota // Model name
	colProvider                   // Provider
	colPrice                      // Prompt price
)

const numSortColumns = 3

// modelList is the UI component for the /model selection screen.
type modelList struct {
	models      []ModelDef
	cursor      int
	scroll      int                      // index of first visible model row
	activeModel string                   // currently active model ID (for highlighting)
	width       int
	height      int
	loading     bool
	sortCol     sortColumn               // active sort column
	sortDirs    [numSortColumns]bool      // per-column sort direction (true = ascending)
}

// visibleRows returns how many model rows fit in the available height.
// Accounts for border (2), padding (2), title+blank (2), header (1), separator (1), hint (2).
const modelListChrome = 12 // border + padding + title + header + separator + hint

func (l modelList) visibleRows() int {
	rows := l.height - modelListChrome
	if rows < 1 {
		rows = 1
	}
	return rows
}

func newModelList(models []ModelDef, activeModel string, width, height int, savedDirs map[string]bool) modelList {
	ml := modelList{
		models:      models,
		activeModel: activeModel,
		width:       width,
		height:      height,
		sortCol:     colPrice,
	}
	// Initialize per-column defaults, then overlay any saved preferences
	for c := sortColumn(0); int(c) < numSortColumns; c++ {
		ml.sortDirs[c] = defaultAscending(c)
	}
	for c := sortColumn(0); int(c) < numSortColumns; c++ {
		if dir, ok := savedDirs[columnKey(c)]; ok {
			ml.sortDirs[c] = dir
		}
	}
	ml.sortModels()

	// Find cursor position for active model after sort
	for i, m := range ml.models {
		if m.ID == activeModel {
			ml.cursor = i
			break
		}
	}
	ml.clampScroll()
	return ml
}

// defaultAscending returns the natural sort direction for a column.
func defaultAscending(col sortColumn) bool {
	switch col {
	case colPrice:
		return false // most expensive first
	default:
		return true // alphabetical
	}
}

// columnKey returns a stable string key for persisting a column's sort direction.
func columnKey(col sortColumn) string {
	switch col {
	case colName:
		return "name"
	case colProvider:
		return "provider"
	case colPrice:
		return "price"
	}
	return ""
}

// sortModels sorts the model slice by the current sort column and direction.
func (l *modelList) sortModels() {
	asc := l.sortDirs[l.sortCol]
	sort.SliceStable(l.models, func(i, j int) bool {
		a, b := l.models[i], l.models[j]
		switch l.sortCol {
		case colName:
			an, bn := strings.ToLower(a.DisplayName), strings.ToLower(b.DisplayName)
			if an == bn {
				return false
			}
			if asc {
				return an < bn
			}
			return an > bn
		case colProvider:
			ap, bp := strings.ToLower(a.Provider), strings.ToLower(b.Provider)
			if ap == bp {
				return false
			}
			if asc {
				return ap < bp
			}
			return ap > bp
		case colPrice:
			if a.PromptPrice == b.PromptPrice {
				return false
			}
			if asc {
				return a.PromptPrice < b.PromptPrice
			}
			return a.PromptPrice > b.PromptPrice
		}
		return false
	})
}

// clampScroll ensures the cursor is visible within the scroll window.
func (l *modelList) clampScroll() {
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

func (l modelList) Update(msg tea.Msg) (modelList, tea.Cmd) {
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
			if l.cursor < len(l.models)-1 {
				l.cursor++
				l.clampScroll()
			}
		case "left", "h":
			selectedID := l.models[l.cursor].ID
			l.sortCol = sortColumn((int(l.sortCol) - 1 + numSortColumns) % numSortColumns)
			l.sortModels()
			l.restoreCursor(selectedID)
		case "right", "l":
			selectedID := l.models[l.cursor].ID
			l.sortCol = sortColumn((int(l.sortCol) + 1) % numSortColumns)
			l.sortModels()
			l.restoreCursor(selectedID)
		case "tab":
			selectedID := l.models[l.cursor].ID
			l.sortDirs[l.sortCol] = !l.sortDirs[l.sortCol]
			l.sortModels()
			l.restoreCursor(selectedID)
		}
	}
	return l, nil
}

// restoreCursor finds the model with the given ID and moves the cursor to it.
func (l *modelList) restoreCursor(id string) {
	for i, m := range l.models {
		if m.ID == id {
			l.cursor = i
			l.clampScroll()
			return
		}
	}
}

// selected returns the model under the cursor.
func (l modelList) selected() ModelDef {
	return l.models[l.cursor]
}

// sortDirsMap returns the current per-column sort directions as a map for config persistence.
func (l modelList) sortDirsMap() map[string]bool {
	m := make(map[string]bool, numSortColumns)
	for c := sortColumn(0); int(c) < numSortColumns; c++ {
		m[columnKey(c)] = l.sortDirs[c]
	}
	return m
}

// columnLabel returns the header label for a sort column.
func columnLabel(col sortColumn) string {
	switch col {
	case colName:
		return "MODEL"
	case colProvider:
		return "PROVIDER"
	case colPrice:
		return "PRICE"
	}
	return ""
}

// runeLen returns the number of runes in a string.
func runeLen(s string) int {
	return len([]rune(s))
}

// padRight pads s with spaces to width w (in runes). If s is longer, it's truncated.
func padRight(s string, w int) string {
	n := runeLen(s)
	if n >= w {
		return string([]rune(s)[:w])
	}
	return s + strings.Repeat(" ", w-n)
}

func (l modelList) View() string {
	titleStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#B88AFF")).
		Bold(true).
		PaddingLeft(2).
		PaddingBottom(1)

	if l.loading {
		var b strings.Builder
		b.WriteString(titleStyle.Render("⚡ Select Model"))
		b.WriteString("\n")
		loadingStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#888888")).
			PaddingLeft(2)
		b.WriteString(loadingStyle.Render("Loading models..."))
		b.WriteString("\n")
		return b.String()
	}

	// Compute column widths from data (in runes, not bytes)
	// Headers include space for sort arrow " ▼" (2 extra chars)
	const arrowLen = 2
	nameW := runeLen("MODEL") + arrowLen
	provW := runeLen("PROVIDER") + arrowLen
	priceW := runeLen("PRICE") + arrowLen

	for _, m := range l.models {
		if runeLen(m.DisplayName) > nameW {
			nameW = runeLen(m.DisplayName)
		}
		if runeLen(m.Provider) > provW {
			provW = runeLen(m.Provider)
		}
		p := fmt.Sprintf("%s / %s", formatPrice(m.PromptPrice), formatPrice(m.CompletionPrice))
		if runeLen(p) > priceW {
			priceW = runeLen(p)
		}
	}

	// Add padding between columns
	const colGap = 2

	// Styles
	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#888888")).
		Bold(true)
	activeHeaderStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#B88AFF")).
		Bold(true)
	sepStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#3A0066"))
	providerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#666666"))
	priceStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#555555"))
	activeMarkerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6FE7B8")).
		Bold(true)
	scrollIndicator := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#555555")).
		PaddingLeft(2)
	hintStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#555555")).
		PaddingLeft(2).
		PaddingTop(1)
	hintKeyStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#7B3EC7"))

	var b strings.Builder
	b.WriteString(titleStyle.Render("⚡ Select Model"))
	b.WriteString("\n")

	// Render header row
	sortArrow := func(col sortColumn) string {
		if l.sortCol != col {
			return ""
		}
		if l.sortDirs[col] {
			return " ▲"
		}
		return " ▼"
	}

	renderHeader := func(col sortColumn, width int) string {
		label := columnLabel(col) + sortArrow(col)
		padded := padRight(label, width)
		if l.sortCol == col {
			return activeHeaderStyle.Render(padded)
		}
		return headerStyle.Render(padded)
	}

	gap := strings.Repeat(" ", colGap)
	b.WriteString("  ") // cursor column placeholder
	b.WriteString(renderHeader(colName, nameW))
	b.WriteString(gap)
	b.WriteString(renderHeader(colProvider, provW))
	b.WriteString(gap)
	b.WriteString(renderHeader(colPrice, priceW))
	b.WriteString("\n")

	// Separator line
	totalW := 2 + nameW + colGap + provW + colGap + priceW + 3
	b.WriteString(sepStyle.Render("  " + strings.Repeat("─", totalW-2)))
	b.WriteString("\n")

	vis := l.visibleRows()
	end := l.scroll + vis
	if end > len(l.models) {
		end = len(l.models)
	}

	// Show scroll-up indicator
	if l.scroll > 0 {
		b.WriteString(scrollIndicator.Render(fmt.Sprintf("  ↑ %d more", l.scroll)))
		b.WriteString("\n")
	}

	selectedRowStyle := lipgloss.NewStyle().Background(lipgloss.Color("#2A1545"))

	for i := l.scroll; i < end; i++ {
		m := l.models[i]
		selected := i == l.cursor

		var cursorStr string
		nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
		rowProvStyle := providerStyle
		rowPriceStyle := priceStyle

		if selected {
			cursorStr = lipgloss.NewStyle().Foreground(lipgloss.Color("#B88AFF")).Render("▸ ")
			nameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#E0E0E0")).Bold(true)
			rowProvStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#AAAAAA"))
			rowPriceStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#999999"))
		} else {
			cursorStr = lipgloss.NewStyle().Foreground(lipgloss.Color("#3A0066")).Render("  ")
		}

		price := fmt.Sprintf("%s / %s", formatPrice(m.PromptPrice), formatPrice(m.CompletionPrice))

		var row strings.Builder
		row.WriteString(nameStyle.Render(padRight(m.DisplayName, nameW)))
		row.WriteString(gap)
		row.WriteString(rowProvStyle.Render(padRight(m.Provider, provW)))
		row.WriteString(gap)
		row.WriteString(rowPriceStyle.Render(padRight(price, priceW)))

		if m.ID == l.activeModel {
			row.WriteString(" ")
			row.WriteString(activeMarkerStyle.Render("●"))
		}

		b.WriteString(cursorStr)
		if selected {
			b.WriteString(selectedRowStyle.Render(row.String()))
		} else {
			b.WriteString(row.String())
		}
		b.WriteString("\n")
	}

	// Show scroll-down indicator
	if end < len(l.models) {
		b.WriteString(scrollIndicator.Render(fmt.Sprintf("  ↓ %d more", len(l.models)-end)))
		b.WriteString("\n")
	}

	hint := fmt.Sprintf(
		"  %s select  %s cancel  %s sort  %s reverse  %s in/out per 1M tokens",
		hintKeyStyle.Render("enter"),
		hintKeyStyle.Render("esc"),
		hintKeyStyle.Render("←/→"),
		hintKeyStyle.Render("tab"),
		hintKeyStyle.Render("$"),
	)
	b.WriteString(hintStyle.Render(hint))
	b.WriteString("\n")

	return b.String()
}
