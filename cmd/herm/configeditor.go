// configeditor.go implements the interactive config editor UI, including
// tab navigation, field editing, model picker integration, and key handling.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// isOllamaOffline reports whether modelID is an Ollama model that is not
// present in the current live model list (i.e. Ollama is configured but down).
// Returns false if no Ollama URL is configured.
func (a *App) isOllamaOffline(modelID string) bool {
	if modelID == "" {
		return false
	}
	// Check if it's in the live list as an Ollama model.
	for _, m := range a.models {
		if m.ID == modelID && m.Provider == ProviderOllama {
			return false // online and present
		}
	}
	// Not in live list — treat as offline if it's not a known catalog model either.
	for _, m := range a.models {
		if m.ID == modelID {
			return false // it's a different provider's model
		}
	}
	return true // unknown to catalog → assume offline Ollama model
}

func maskKey(key string) string {
	if key == "" {
		return "(not set)"
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// ─── Config editor ───

var cfgTabNames = []string{"API Keys", "Global", "Project"}

type cfgField struct {
	label      string
	get        func(Config) string
	display    func(Config) string    // masked display; nil means use get
	set        func(*Config, string)
	toggle     func(*Config)          // if non-nil, Enter toggles instead of opening editor
	globalHint func(Config) string    // if set, shows "(global: X)" when field value is empty
	picker     func(*App)             // if non-nil, Enter opens a picker (e.g. model selector) instead of editor
}

var cfgAPIKeyFields = []cfgField{
	{label: "Anthropic", get: func(c Config) string { return c.AnthropicAPIKey }, display: func(c Config) string { return maskKey(c.AnthropicAPIKey) }, set: func(c *Config, v string) { c.AnthropicAPIKey = v }},
	{label: "OpenAI", get: func(c Config) string { return c.OpenAIAPIKey }, display: func(c Config) string { return maskKey(c.OpenAIAPIKey) }, set: func(c *Config, v string) { c.OpenAIAPIKey = v }},
	{label: "Grok", get: func(c Config) string { return c.GrokAPIKey }, display: func(c Config) string { return maskKey(c.GrokAPIKey) }, set: func(c *Config, v string) { c.GrokAPIKey = v }},
	{label: "OpenRouter", get: func(c Config) string { return c.OpenRouterAPIKey }, display: func(c Config) string { return maskKey(c.OpenRouterAPIKey) }, set: func(c *Config, v string) { c.OpenRouterAPIKey = v }},
	{label: "Gemini", get: func(c Config) string { return c.GeminiAPIKey }, display: func(c Config) string { return maskKey(c.GeminiAPIKey) }, set: func(c *Config, v string) { c.GeminiAPIKey = v }},
	{label: "Ollama URL", get: func(c Config) string { return c.OllamaBaseURL }, set: func(c *Config, v string) {
		v = strings.TrimSpace(v)
		if v != "" && !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
			v = "http://" + v
		}
		c.OllamaBaseURL = v
	}},
}

func apiKeyRowForProvider(provider string) int {
	switch provider {
	case ProviderAnthropic:
		return 0
	case ProviderOpenAI:
		return 1
	case ProviderGrok:
		return 2
	case ProviderOpenRouter:
		return 3
	case ProviderGemini:
		return 4
	case ProviderOllama:
		return 5
	default:
		return 0
	}
}

// effectiveProviderForConfig returns the provider implied by the effective
// active model. Falls back to the default configured provider when no active
// model can be resolved.
func (a *App) effectiveProviderForConfig(cfg Config) (provider string, modelID string) {
	modelID = cfg.resolveActiveModel(a.models)
	if modelID != "" {
		if model := findModelByID(a.models, modelID); model != nil {
			return model.Provider, modelID
		}
		// For unknown model IDs, keep the existing offline-Ollama assumption.
		return ollamaModelProvider(modelID, a.models, cfg.OllamaBaseURL), modelID
	}
	return cfg.defaultLangdagProvider(), ""
}

// preferredAPIKeyCursor chooses the initial cursor row in the API Keys tab:
// 1) active model provider, 2) first configured provider, 3) Anthropic.
func (a *App) preferredAPIKeyCursor(cfg Config) int {
	if p, _ := a.effectiveProviderForConfig(cfg); p != "" {
		return apiKeyRowForProvider(p)
	}
	ordered := []string{ProviderAnthropic, ProviderOpenAI, ProviderGrok, ProviderOpenRouter, ProviderGemini, ProviderOllama}
	configured := cfg.configuredProviders()
	for _, p := range ordered {
		if configured[p] {
			return apiKeyRowForProvider(p)
		}
	}
	return 0
}

func (a *App) enterConfigMode() {
	a.cfgActive = true
	a.cfgTab = 0
	a.cfgTabCursor = [3]int{a.preferredAPIKeyCursor(a.config), 0, 0}
	a.cfgCursor = a.cfgTabCursor[a.cfgTab]
	a.cfgEditing = false
	a.cfgEditBuf = nil
	a.cfgEditCursor = 0
	a.cfgDraft = a.globalConfig
	a.cfgProjectDraft = a.projectConfig
	a.renderInput()
}

func (a *App) exitConfigMode(save bool) {
	if save {
		a.globalConfig = a.cfgDraft
		a.projectConfig = a.cfgProjectDraft
		a.config = mergeConfigs(a.globalConfig, a.projectConfig)
		// Re-initialize debug log if debug mode changed
		if a.debugActive() && a.traceCollector == nil {
			a.initAppDebugLog()
		} else if !a.debugActive() && a.traceCollector != nil {
			a.traceCollector.Finalize()
			if err := a.traceCollector.FlushToFile(a.traceFilePath); err != nil {
				fmt.Fprintf(os.Stderr, "debug: failed to write trace: %v\n", err)
			}
			a.traceCollector = nil
			a.traceFilePath = ""
		}
		var saveErr bool
		if err := saveConfig(a.globalConfig); err != nil {
			a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Error saving global config: %v", err)})
			saveErr = true
		}
		if a.repoRoot != "" {
			if err := saveProjectConfig(a.repoRoot, a.projectConfig); err != nil {
				a.messages = append(a.messages, chatMessage{kind: msgError, content: fmt.Sprintf("Error saving project config: %v", err)})
				saveErr = true
			}
		}
		if !saveErr {
			a.messages = append(a.messages, chatMessage{kind: msgSuccess, content: "Config saved."})
		}
		// Refresh models including Ollama and OpenRouter if configured
		if a.config.OllamaBaseURL != "" {
			go func() { a.resultCh <- fetchOllamaModelsCmd(a.config.OllamaBaseURL) }()
		}
		if a.config.OpenRouterAPIKey != "" {
			a.openRouterFetched = false // allow re-fetch with new key
			go func() { a.resultCh <- fetchOpenRouterModelsCmd(a.config.OpenRouterAPIKey) }()
		}
		// Show updated model if it changed
		if a.models != nil {
			a.showModelChange(a.config.resolveActiveModel(a.models))
		}
		// Reinitialize langdag client with updated config
		go func() {
			client, err := newLangdagClient(a.config)
			a.resultCh <- langdagReadyMsg{client: client, provider: a.config.defaultLangdagProvider(), err: err}
		}()
	}
	a.cfgActive = false
	a.cfgEditing = false
	a.cfgEditBuf = nil
	a.render()
}

// openConfigModelPicker opens an inline model menu within the config editor.
// getCurrentID returns the currently selected model ID (for highlighting).
// onSelect is called with the chosen model ID when the user makes a selection.
// If the draft Ollama URL differs from the saved URL, models are fetched
// asynchronously and the picker opens once results arrive.
func (a *App) openConfigModelPicker(getCurrentID func() string, onSelect func(string)) {
	if a.models == nil {
		return
	}
	// If the draft URL differs from the saved URL, fetch Ollama models async
	// before opening the picker so we don't block the UI.
	if a.cfgDraft.OllamaBaseURL != "" && a.config.OllamaBaseURL != a.cfgDraft.OllamaBaseURL {
		go func() {
			msg := fetchOllamaModelsCmd(a.cfgDraft.OllamaBaseURL)
			a.resultCh <- msg
			// Open the picker after the result is handled; send a follow-up
			// signal via a dedicated picker-open message.
			a.resultCh <- openPickerMsg{getCurrentID: getCurrentID, onSelect: onSelect}
		}()
		return
	}
	// If the draft OpenRouter key differs from the saved key, fetch fresh models
	// async before opening the picker.
	if a.cfgDraft.OpenRouterAPIKey != "" && a.config.OpenRouterAPIKey != a.cfgDraft.OpenRouterAPIKey {
		go func() {
			msg := fetchOpenRouterModelsCmd(a.cfgDraft.OpenRouterAPIKey)
			a.resultCh <- msg
			a.resultCh <- openPickerMsg{getCurrentID: getCurrentID, onSelect: onSelect}
		}()
		return
	}
	a.doOpenConfigModelPicker(a.models, getCurrentID, onSelect)
}

// doOpenConfigModelPicker builds and displays the model picker menu.
func (a *App) doOpenConfigModelPicker(models []ModelDef, getCurrentID func() string, onSelect func(string)) {
	available := a.cfgDraft.availableModels(models)

	// If Ollama is configured but offline, inject a stub for the saved model
	// so the picker still opens and the user can see their current selection.
	if a.cfgDraft.OllamaBaseURL != "" {
		ollamaInList := false
		for _, m := range available {
			if m.Provider == ProviderOllama {
				ollamaInList = true
				break
			}
		}
		if !ollamaInList {
			savedID := getCurrentID()
			if savedID == "" {
				savedID = a.cfgDraft.ActiveModel
			}
			if savedID != "" {
				available = append(available, ModelDef{
					Provider: ProviderOllama,
					ID:       savedID,
					Label:    savedID + " \033[33m(offline)\033[0m",
				})
			}
		}
	}

	// If OpenRouter is configured but models haven't loaded yet (e.g. bad key or
	// network error), inject a stub so the picker still shows the saved selection.
	if a.cfgDraft.OpenRouterAPIKey != "" {
		orInList := false
		for _, m := range available {
			if m.Provider == ProviderOpenRouter {
				orInList = true
				break
			}
		}
		if !orInList {
			savedID := getCurrentID()
			if savedID == "" {
				savedID = a.cfgDraft.ActiveModel
			}
			if savedID != "" {
				available = append(available, ModelDef{
					Provider: ProviderOpenRouter,
					ID:       savedID,
					Label:    savedID + " \033[33m(unavailable)\033[0m",
				})
			}
		}
	}

	if len(available) == 0 {
		return
	}

	activeID := getCurrentID()
	a.menuModels = available
	a.menuActiveID = activeID
	a.menuSortCol = sortColFromName(a.cfgDraft.ModelSortCol)
	a.menuSortAsc = sortAscFromMap(a.cfgDraft.ModelSortDirs)
	asc := a.menuSortAsc[a.menuSortCol]
	sortModelsByCol(a.menuModels, a.menuSortCol, asc)
	header, lines := formatModelMenuLines(a.menuModels, activeID, a.menuSortCol, asc)

	activeIdx := 0
	for i, m := range a.menuModels {
		if m.ID == activeID {
			activeIdx = i
			break
		}
	}

	a.menuHeader = header
	a.menuLines = lines
	a.menuCursor = activeIdx
	maxVisible := getTerminalHeight() * 60 / 100
	if maxVisible < 1 {
		maxVisible = 1
	}
	if activeIdx >= maxVisible {
		a.menuScrollOffset = activeIdx - maxVisible + 1
	} else {
		a.menuScrollOffset = 0
	}
	a.menuActive = true
	a.menuAction = func(idx int) {
		if idx >= 0 && idx < len(a.menuModels) {
			onSelect(a.menuModels[idx].ID)
		}
		a.menuLines = nil
		a.menuHeader = ""
		a.menuActive = false
		a.menuAction = nil
		a.menuScrollOffset = 0
		a.menuModels = nil
		a.menuActiveID = ""
		// Config mode stays active — renderInput will show config fields again.
	}
	a.renderInput()
}

func (a *App) cfgCurrentFields() []cfgField {
	switch a.cfgTab {
	case 0:
		return cfgAPIKeyFields
	case 1:
		return a.settingsTabFields()
	case 2:
		return a.projectTabFields()
	}
	return nil
}

func (a *App) resolvedExplorationDisplay(c Config) string {
	if c.ExplorationModel != "" {
		return c.ExplorationModel
	}
	if len(c.configuredProviders()) == 0 {
		return ""
	}
	return c.resolveExplorationModel(a.models)
}

func (a *App) settingsTabFields() []cfgField {
	return []cfgField{
		{label: "Active Model", get: func(c Config) string { return c.ActiveModel }, set: func(c *Config, v string) { c.ActiveModel = v }, picker: func(a *App) { a.openConfigModelPicker(func() string { return a.cfgDraft.ActiveModel }, func(id string) { a.cfgDraft.ActiveModel = id }) }},
		{label: "Exploration Model", get: func(c Config) string { return c.ExplorationModel }, display: func(c Config) string { return a.resolvedExplorationDisplay(c) }, set: func(c *Config, v string) { c.ExplorationModel = v }, picker: func(a *App) { a.openConfigModelPicker(func() string { return a.cfgDraft.ExplorationModel }, func(id string) { a.cfgDraft.ExplorationModel = id }) }},
		{label: "Paste Collapse", get: func(c Config) string { return strconv.Itoa(c.PasteCollapseMinChars) }, set: func(c *Config, v string) { if n, err := strconv.Atoi(v); err == nil { c.PasteCollapseMinChars = n } }},
		{label: "Debug Mode", get: func(c Config) string { if c.DebugMode { return "on" }; return "off" }, toggle: func(c *Config) { c.DebugMode = !c.DebugMode }},
		{label: "Thinking", get: func(c Config) string { if c.effectiveThinking() { return "on" }; return "off" }, toggle: func(c *Config) { if c.Thinking == nil { t := true; c.Thinking = &t } else { v := !*c.Thinking; c.Thinking = &v } }},
		{label: "Sub-Agent Max Turns", get: func(c Config) string { n := c.SubAgentMaxTurns; if n <= 0 { n = defaultSubAgentMaxTurns }; return strconv.Itoa(n) }, set: func(c *Config, v string) { if n, err := strconv.Atoi(v); err == nil && n > 0 { c.SubAgentMaxTurns = n } }},
		{label: "Personality", get: func(c Config) string { return c.Personality }, set: func(c *Config, v string) { c.Personality = v }},
		{label: "Git Co-Author", get: func(c Config) string { if c.effectiveGitCoAuthor() { return "on" }; return "off" }, toggle: func(c *Config) { if c.GitCoAuthor == nil { f := false; c.GitCoAuthor = &f } else { v := !*c.GitCoAuthor; c.GitCoAuthor = &v } }},
	}
}

func (a *App) projectTabFields() []cfgField {
	return []cfgField{
		{
			label:      "Active Model",
			get:        func(_ Config) string { return a.cfgProjectDraft.ActiveModel },
			set:        func(_ *Config, v string) { a.cfgProjectDraft.ActiveModel = v },
			globalHint: func(c Config) string { return c.ActiveModel },
			picker:     func(a *App) { a.openConfigModelPicker(func() string { return a.cfgProjectDraft.ActiveModel }, func(id string) { a.cfgProjectDraft.ActiveModel = id }) },
		},
		{
			label:      "Exploration Model",
			get:        func(_ Config) string { return a.cfgProjectDraft.ExplorationModel },
			set:        func(_ *Config, v string) { a.cfgProjectDraft.ExplorationModel = v },
			globalHint: func(c Config) string { return a.resolvedExplorationDisplay(c) },
			picker:     func(a *App) { a.openConfigModelPicker(func() string { return a.cfgProjectDraft.ExplorationModel }, func(id string) { a.cfgProjectDraft.ExplorationModel = id }) },
		},
		{
			label:      "Personality",
			get:        func(_ Config) string { return a.cfgProjectDraft.Personality },
			set:        func(_ *Config, v string) { a.cfgProjectDraft.Personality = v },
			globalHint: func(c Config) string { return c.Personality },
		},
		{
			label: "Sub-Agent Max Turns",
			get: func(_ Config) string {
				if a.cfgProjectDraft.SubAgentMaxTurns == 0 {
					return ""
				}
				return strconv.Itoa(a.cfgProjectDraft.SubAgentMaxTurns)
			},
			set: func(_ *Config, v string) {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					a.cfgProjectDraft.SubAgentMaxTurns = n
				} else {
					a.cfgProjectDraft.SubAgentMaxTurns = 0
				}
			},
			globalHint: func(c Config) string {
				n := c.SubAgentMaxTurns
				if n <= 0 {
					n = defaultSubAgentMaxTurns
				}
				return strconv.Itoa(n)
			},
		},
		{
			label: "Thinking",
			get: func(_ Config) string {
				if a.cfgProjectDraft.Thinking == nil {
					return ""
				}
				if *a.cfgProjectDraft.Thinking {
					return "on"
				}
				return "off"
			},
			toggle: func(_ *Config) {
				if a.cfgProjectDraft.Thinking == nil {
					t := true
					a.cfgProjectDraft.Thinking = &t
				} else {
					v := !*a.cfgProjectDraft.Thinking
					a.cfgProjectDraft.Thinking = &v
				}
			},
			globalHint: func(c Config) string {
				if c.effectiveThinking() {
					return "on"
				}
				return "off"
			},
		},
	}
}

func (a *App) buildConfigRows() []string {
	var rows []string
	configured := a.cfgDraft.configuredProviders()
	hasProvider := len(configured) > 0
	isProjectTab := a.cfgTab == 2

	// Tab bar
	var tabParts []string
	for i, name := range cfgTabNames {
		if i == a.cfgTab {
			tabParts = append(tabParts, fmt.Sprintf("\033[36;1m[%s]\033[0m", name))
		} else {
			tabParts = append(tabParts, fmt.Sprintf("\033[2m %s \033[0m", name))
		}
	}
	rows = append(rows, strings.Join(tabParts, " "))

	if a.cfgTab == 0 {
		effective := mergeConfigs(a.cfgDraft, a.cfgProjectDraft)
		provider, modelID := a.effectiveProviderForConfig(effective)
		if provider == "" {
			rows = append(rows, "\033[2mEffective provider: (none)\033[0m")
		} else if modelID != "" {
			rows = append(rows, fmt.Sprintf("\033[2mEffective provider: %s  (active model: %s)\033[0m", provider, modelID))
		} else {
			rows = append(rows, fmt.Sprintf("\033[2mEffective provider: %s\033[0m", provider))
		}
	}

	// No-project message for Project tab
	if a.cfgTab == 2 && a.repoRoot == "" {
		rows = append(rows, "\033[2mNo project detected (not in a git repository)\033[0m")
		rows = append(rows, "\033[2m←/→=tab  Esc=close  Ctrl+S=save & close\033[0m")
		return rows
	}

	// When a model picker menu is active, render it inline below the tab bar
	if a.menuActive && len(a.menuLines) > 0 {
		w := a.width
		if a.menuHeader != "" {
			rows = append(rows, fmt.Sprintf("\033[1m%s\033[0m", truncateWithEllipsis(a.menuHeader, w)))
		}
		maxVisible := getTerminalHeight() * 60 / 100
		if maxVisible < 1 {
			maxVisible = 1
		}
		total := len(a.menuLines)
		end := a.menuScrollOffset + maxVisible
		if end > total {
			end = total
		}
		for i := a.menuScrollOffset; i < end; i++ {
			line := a.menuLines[i]
			if i == a.menuCursor {
				rows = append(rows, fmt.Sprintf("\033[36;1m%s ◆\033[0m", truncateWithEllipsis(line, w-2)))
			} else {
				rows = append(rows, truncateWithEllipsis(line, w))
			}
		}
		first := a.menuScrollOffset + 1
		last := end
		rows = append(rows, fmt.Sprintf("\033[2m(%d->%d / %d)\033[0m", first, last, total))
		rows = append(rows, "\033[2m←/→ sort column  Tab flip order  Enter select  Esc close\033[0m")
		return rows
	}

	// Fields
	fields := a.cfgCurrentFields()
	for i, f := range fields {
		if a.cfgEditing && i == a.cfgCursor {
			// Show editable text input with underline
			editStr := string(a.cfgEditBuf)
			rows = append(rows, fmt.Sprintf("\033[36;1m%s: \033[4m%s\033[0m \033[36;1m◆\033[0m", f.label, editStr))
		} else {
			val := ""
			if f.display != nil {
				val = f.display(a.cfgDraft)
			} else {
				val = f.get(a.cfgDraft)
			}
			if f.picker != nil && val != "" {
				p := ollamaModelProvider(val, a.models, a.cfgDraft.OllamaBaseURL)
				// Hide model values when no providers are configured, or when this
				// model's provider is not currently configured.
				if !isProjectTab && (!hasProvider || p == "" || !configured[p]) {
					val = ""
				}
			}
			// If the value is an Ollama model and Ollama is offline, show indicator.
			// Only applies to model picker fields, not API key or other fields.
			if val != "" && f.picker != nil && a.cfgDraft.OllamaBaseURL != "" && a.isOllamaOffline(val) {
				val = val + " \033[33m(offline)\033[0m"
			}
			if val == "" {
				if f.picker != nil && !hasProvider && !isProjectTab {
					val = "(not set)"
				} else if f.globalHint != nil {
					hint := f.globalHint(a.cfgDraft)
					if f.picker != nil && !isProjectTab {
						p := ollamaModelProvider(hint, a.models, a.cfgDraft.OllamaBaseURL)
						if hint == "" || p == "" || !configured[p] {
							hint = "not set"
						}
					}
					if hint == "" {
						hint = "not set"
					}
					val = fmt.Sprintf("(global: %s)", hint)
				} else {
					val = "(not set)"
				}
			}
			if i == a.cfgCursor {
				rows = append(rows, fmt.Sprintf("\033[36;1m%s: %s ◆\033[0m", f.label, val))
			} else {
				rows = append(rows, fmt.Sprintf("%s: %s", f.label, val))
			}
		}
	}

	// Help line
	if a.cfgEditing {
		rows = append(rows, "\033[2mEnter=confirm  Esc=cancel\033[0m")
	} else {
		rows = append(rows, "\033[2m←/→=tab  ↑/↓=select  Enter=edit  Esc=close  Ctrl+S=save & close\033[0m")
	}

	return rows
}

func (a *App) handleConfigByte(ch byte, stdinCh chan byte, readByte func() (byte, bool)) {
	if a.cfgEditing {
		a.handleConfigEditByte(ch, stdinCh, readByte)
		return
	}

	switch {
	case ch == '\033': // Escape sequence
		var b byte
		var ok bool
		select {
		case b, ok = <-stdinCh:
			if !ok {
				return
			}
		case <-time.After(50 * time.Millisecond):
			a.exitConfigMode(false)
			return
		}
		if b != '[' {
			a.exitConfigMode(false)
			return
		}
		b, ok = readByte()
		if !ok {
			return
		}
		switch b {
		case 'A': // Up
			if a.cfgCursor > 0 {
				a.cfgCursor--
			} else {
				fields := a.cfgCurrentFields()
				if len(fields) > 0 {
					a.cfgCursor = len(fields) - 1
				}
			}
			a.cfgTabCursor[a.cfgTab] = a.cfgCursor
			a.renderInput()
		case 'B': // Down
			fields := a.cfgCurrentFields()
			if a.cfgCursor < len(fields)-1 {
				a.cfgCursor++
			} else {
				a.cfgCursor = 0
			}
			a.cfgTabCursor[a.cfgTab] = a.cfgCursor
			a.renderInput()
		case 'C': // Right - next tab
			a.cfgTabCursor[a.cfgTab] = a.cfgCursor
			a.cfgTab++
			if a.cfgTab >= len(cfgTabNames) {
				a.cfgTab = 0
			}
			fields := a.cfgCurrentFields()
			if len(fields) == 0 {
				a.cfgCursor = 0
			} else if a.cfgTabCursor[a.cfgTab] < len(fields) {
				a.cfgCursor = a.cfgTabCursor[a.cfgTab]
			} else {
				a.cfgCursor = len(fields) - 1
			}
			a.renderInput()
		case 'D': // Left - prev tab
			a.cfgTabCursor[a.cfgTab] = a.cfgCursor
			a.cfgTab--
			if a.cfgTab < 0 {
				a.cfgTab = len(cfgTabNames) - 1
			}
			fields := a.cfgCurrentFields()
			if len(fields) == 0 {
				a.cfgCursor = 0
			} else if a.cfgTabCursor[a.cfgTab] < len(fields) {
				a.cfgCursor = a.cfgTabCursor[a.cfgTab]
			} else {
				a.cfgCursor = len(fields) - 1
			}
			a.renderInput()
		case '2': // modifyOtherKeys (Ctrl+S, Ctrl+C, etc.)
			a.handleCSIDigit2(readByte, func(string) {})
		default:
			// Consume modified key sequences (ESC [ 1 ; mod letter)
			if b == '1' {
				readByte() // ;
				readByte() // mod
				readByte() // letter
			}
		}

	case ch == '\r': // Enter - toggle, picker, or start editing current field
		if a.cfgTab == 2 && a.repoRoot == "" {
			break // Project tab non-editable without a repo
		}
		fields := a.cfgCurrentFields()
		if len(fields) > 0 && a.cfgCursor < len(fields) {
			f := fields[a.cfgCursor]
			if f.picker != nil {
				f.picker(a)
			} else if f.toggle != nil {
				f.toggle(&a.cfgDraft)
			} else {
				a.cfgEditing = true
				val := f.get(a.cfgDraft)
				a.cfgEditBuf = []rune(val)
				a.cfgEditCursor = len(a.cfgEditBuf)
			}
		}
		a.renderInput()

	case ch == 0x13: // Ctrl+S - save and close
		a.exitConfigMode(true)

	case ch == 3 || ch == 4: // Ctrl+C/D - exit without saving
		a.exitConfigMode(false)
	}
}

func (a *App) handleConfigEditByte(ch byte, stdinCh chan byte, readByte func() (byte, bool)) {
	switch {
	case ch == '\033': // Escape
		var b byte
		var ok bool
		select {
		case b, ok = <-stdinCh:
			if !ok {
				return
			}
		case <-time.After(50 * time.Millisecond):
			// Plain Escape - cancel edit
			a.cfgEditing = false
			a.cfgEditBuf = nil
			a.renderInput()
			return
		}
		if b != '[' {
			a.cfgEditing = false
			a.cfgEditBuf = nil
			a.renderInput()
			return
		}
		b, ok = readByte()
		if !ok {
			return
		}
		switch b {
		case 'C': // Right
			if a.cfgEditCursor < len(a.cfgEditBuf) {
				a.cfgEditCursor++
				a.renderInput()
			}
		case 'D': // Left
			if a.cfgEditCursor > 0 {
				a.cfgEditCursor--
				a.renderInput()
			}
		case 'H': // Home
			a.cfgEditCursor = 0
			a.renderInput()
		case 'F': // End
			a.cfgEditCursor = len(a.cfgEditBuf)
			a.renderInput()
		case '2': // Bracketed paste or modifyOtherKeys
			a.handleCSIDigit2(readByte, func(s string) {
				pasted := []rune(s)
				tail := make([]rune, len(a.cfgEditBuf[a.cfgEditCursor:]))
				copy(tail, a.cfgEditBuf[a.cfgEditCursor:])
				a.cfgEditBuf = append(a.cfgEditBuf[:a.cfgEditCursor], append(pasted, tail...)...)
				a.cfgEditCursor += len(pasted)
				a.renderInput()
			})
		case '3': // Delete
			if t, ok := readByte(); ok && t == '~' {
				if a.cfgEditCursor < len(a.cfgEditBuf) {
					a.cfgEditBuf = append(a.cfgEditBuf[:a.cfgEditCursor], a.cfgEditBuf[a.cfgEditCursor+1:]...)
					a.renderInput()
				}
			}
		default:
			// consume remaining bytes of sequences
			if b == '1' {
				readByte()
				readByte()
				readByte()
			}
		}

	case ch == '\r': // Enter - confirm edit
		fields := a.cfgCurrentFields()
		if a.cfgCursor < len(fields) {
			fields[a.cfgCursor].set(&a.cfgDraft, string(a.cfgEditBuf))
		}
		a.cfgEditing = false
		a.cfgEditBuf = nil
		a.renderInput()

	case ch == 127 || ch == 0x08: // Backspace
		if a.cfgEditCursor > 0 {
			a.cfgEditCursor--
			a.cfgEditBuf = append(a.cfgEditBuf[:a.cfgEditCursor], a.cfgEditBuf[a.cfgEditCursor+1:]...)
			a.renderInput()
		}

	case ch == 0x01: // Ctrl+A
		a.cfgEditCursor = 0
		a.renderInput()

	case ch == 0x05: // Ctrl+E
		a.cfgEditCursor = len(a.cfgEditBuf)
		a.renderInput()

	case ch == 0x15: // Ctrl+U - kill to start
		a.cfgEditBuf = a.cfgEditBuf[a.cfgEditCursor:]
		a.cfgEditCursor = 0
		a.renderInput()

	case ch == 0x0b: // Ctrl+K - kill to end
		a.cfgEditBuf = a.cfgEditBuf[:a.cfgEditCursor]
		a.renderInput()

	case ch == 0x17: // Ctrl+W - delete word backward
		if a.cfgEditCursor > 0 {
			i := a.cfgEditCursor - 1
			for i > 0 && a.cfgEditBuf[i] == ' ' {
				i--
			}
			for i > 0 && a.cfgEditBuf[i-1] != ' ' {
				i--
			}
			a.cfgEditBuf = append(a.cfgEditBuf[:i], a.cfgEditBuf[a.cfgEditCursor:]...)
			a.cfgEditCursor = i
			a.renderInput()
		}

	case ch >= 0x20: // Printable character
		r := rune(ch)
		if ch >= 0x80 {
			b := []byte{ch}
			n := utf8ByteLen(ch)
			for i := 1; i < n; i++ {
				next, ok := readByte()
				if !ok {
					return
				}
				b = append(b, next)
			}
			r, _ = utf8.DecodeRune(b)
		}
		a.cfgEditBuf = append(a.cfgEditBuf, 0)
		copy(a.cfgEditBuf[a.cfgEditCursor+1:], a.cfgEditBuf[a.cfgEditCursor:])
		a.cfgEditBuf[a.cfgEditCursor] = r
		a.cfgEditCursor++
		a.renderInput()
	}
}
