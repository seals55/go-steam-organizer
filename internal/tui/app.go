package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/csull/go-steam-organizer/internal/auth"
	"github.com/csull/go-steam-organizer/internal/collections"
	"github.com/csull/go-steam-organizer/internal/models"
	"github.com/csull/go-steam-organizer/internal/rules"
	"github.com/csull/go-steam-organizer/internal/steam"
)

type activeTab int

const (
	tabLibrary activeTab = iota
	tabCollections
	tabRules
)

type inputMode int

const (
	modeNormal inputMode = iota
	modeFilter
	modeAssignCollection
	modeNewCollection
)

// Options configures the interactive TUI startup state.
type Options struct {
	Session          *auth.Session
	Client           *steam.Client
	ColManager       *collections.Manager
	Items            []models.LibraryItem
	RulesConfig      *rules.ConfigFile
	RulesFilePath    string
	CloudStoragePath string
}

type statusMsg struct {
	text  string
	isErr bool
}

type syncCompleteMsg struct {
	items []models.LibraryItem
	err   error
}

type loginCompleteMsg struct {
	sess *auth.Session
	err  error
}

// Model implements tea.Model for go-steam-organizer.
type Model struct {
	opts          Options
	tab           activeTab
	mode          inputMode
	width         int
	height        int
	cursor        int
	colCursor     int
	ruleCursor    int
	selectedApps  map[uint32]bool
	filteredItems []models.LibraryItem
	filterQuery   string
	textInput     textinput.Model
	status        string
	statusErr     bool
	dirtyCols     bool
	busy          bool
}

// Run launches the interactive Bubble Tea TUI.
func Run(opts Options) error {
	ti := textinput.New()
	ti.CharLimit = 120
	ti.Width = 50

	m := &Model{
		opts:          opts,
		tab:           tabLibrary,
		mode:          modeNormal,
		width:         110,
		height:        32,
		selectedApps:  make(map[uint32]bool),
		filteredItems: opts.Items,
		textInput:     ti,
		status:        "Ready. Press '/' to filter, Space to select, 'a' to add to collection, 's' to save.",
	}
	m.applyFilter()

	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func (m *Model) Init() tea.Cmd {
	return nil
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case statusMsg:
		m.busy = false
		m.status = msg.text
		m.statusErr = msg.isErr
		return m, nil

	case syncCompleteMsg:
		m.busy = false
		if msg.err != nil {
			m.status = fmt.Sprintf("Sync error: %v", msg.err)
			m.statusErr = true
			return m, nil
		}
		m.opts.Items = msg.items
		m.applyFilter()
		m.status = fmt.Sprintf("Synced %d library items with purchase & license history!", len(msg.items))
		m.statusErr = false
		return m, nil

	case loginCompleteMsg:
		m.busy = false
		if msg.err != nil {
			m.status = fmt.Sprintf("Login failed: %v", msg.err)
			m.statusErr = true
			return m, nil
		}
		m.opts.Session = msg.sess
		m.opts.Client = steam.NewClient(msg.sess)
		m.status = fmt.Sprintf("Logged in as %s (%s). Press 'r' to sync library & purchases.", msg.sess.PersonaName, msg.sess.SteamID64)
		m.statusErr = false
		return m, nil

	case tea.KeyMsg:
		if m.mode != modeNormal {
			return m.handleInputModeKey(msg)
		}
		return m.handleNormalKey(msg)
	}
	return m, nil
}

func (m *Model) handleInputModeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeNormal
		m.textInput.Blur()
		m.status = "Cancelled input."
		m.statusErr = false
		return m, nil

	case "enter":
		val := strings.TrimSpace(m.textInput.Value())
		currentMode := m.mode
		m.mode = modeNormal
		m.textInput.Blur()

		switch currentMode {
		case modeFilter:
			m.filterQuery = val
			m.applyFilter()
			m.status = fmt.Sprintf("Filter applied (%d matching items).", len(m.filteredItems))
			m.statusErr = false

		case modeAssignCollection:
			if val == "" {
				m.status = "Collection name cannot be empty."
				m.statusErr = true
				return m, nil
			}
			appIDs := m.targetAppIDsForAction()
			if len(appIDs) == 0 {
				m.status = "No items selected to add."
				m.statusErr = true
				return m, nil
			}
			if m.opts.ColManager == nil {
				m.status = "No collection manager loaded."
				m.statusErr = true
				return m, nil
			}
			added, col := m.opts.ColManager.AddAppsToCollection(val, appIDs)
			m.dirtyCols = true
			m.status = fmt.Sprintf("Added %d new item(s) to collection %q (total %d). Press 's' to save.", added, col.Name, len(col.Added))
			m.statusErr = false

		case modeNewCollection:
			if val == "" {
				m.status = "Collection name cannot be empty."
				m.statusErr = true
				return m, nil
			}
			if m.opts.ColManager != nil {
				col := m.opts.ColManager.EnsureCollection(val)
				m.dirtyCols = true
				m.status = fmt.Sprintf("Created collection %q (%s). Press 's' to save.", col.Name, col.ID)
				m.statusErr = false
			}
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	if m.mode == modeFilter {
		m.filterQuery = strings.TrimSpace(m.textInput.Value())
		m.applyFilter()
	}
	return m, cmd
}

func (m *Model) handleNormalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit

	case "tab":
		m.tab = (m.tab + 1) % 3
		return m, nil

	case "shift+tab":
		m.tab = (m.tab + 2) % 3
		return m, nil

	case "up", "k":
		m.moveCursor(-1)
		return m, nil

	case "down", "j":
		m.moveCursor(1)
		return m, nil

	case "pgup":
		m.moveCursor(-10)
		return m, nil

	case "pgdown":
		m.moveCursor(10)
		return m, nil

	case "/":
		m.mode = modeFilter
		m.textInput.Placeholder = "e.g. year:2025 type:purchase genre:rpg deck:verified price<10 unplayed"
		m.textInput.SetValue(m.filterQuery)
		m.textInput.Focus()
		return m, textinput.Blink

	case "c":
		// Clear filter in Library tab
		if m.tab == tabLibrary && m.filterQuery != "" {
			m.filterQuery = ""
			m.applyFilter()
			m.status = "Cleared filter."
			m.statusErr = false
			return m, nil
		}

	case "1":
		m.setQuickFilter("")
	case "2":
		m.setQuickFilter("unplayed")
	case "3":
		m.setQuickFilter("type:purchase")
	case "4":
		m.setQuickFilter("type:free")
	case "5":
		m.setQuickFilter("type:bundle")
	case "6":
		m.setQuickFilter("deck:verified")

	case " ":
		if m.tab == tabLibrary && len(m.filteredItems) > 0 && m.cursor < len(m.filteredItems) {
			appID := m.filteredItems[m.cursor].AppID
			if m.selectedApps[appID] {
				delete(m.selectedApps, appID)
			} else {
				m.selectedApps[appID] = true
			}
		}
		return m, nil

	case "ctrl+a":
		if m.tab == tabLibrary {
			allSelected := true
			for _, it := range m.filteredItems {
				if !m.selectedApps[it.AppID] {
					allSelected = false
					break
				}
			}
			if allSelected {
				for _, it := range m.filteredItems {
					delete(m.selectedApps, it.AppID)
				}
				m.status = "Deselected filtered items."
			} else {
				for _, it := range m.filteredItems {
					m.selectedApps[it.AppID] = true
				}
				m.status = fmt.Sprintf("Selected all %d filtered items.", len(m.filteredItems))
			}
		}
		return m, nil

	case "a":
		if m.tab == tabLibrary {
			targets := m.targetAppIDsForAction()
			if len(targets) == 0 {
				m.status = "No item selected or highlighted."
				m.statusErr = true
				return m, nil
			}
			m.mode = modeAssignCollection
			m.textInput.Placeholder = "Enter target collection name (existing or new)..."
			m.textInput.SetValue("")
			m.textInput.Focus()
			m.status = fmt.Sprintf("Adding %d item(s) to collection — type collection name and press Enter:", len(targets))
			return m, textinput.Blink
		}
		if m.tab == tabRules {
			return m.applyRulesInTUI()
		}

	case "n":
		if m.tab == tabCollections {
			m.mode = modeNewCollection
			m.textInput.Placeholder = "New collection name..."
			m.textInput.SetValue("")
			m.textInput.Focus()
			return m, textinput.Blink
		}

	case "s":
		if m.opts.ColManager == nil {
			m.status = "No collection storage configured."
			m.statusErr = true
			return m, nil
		}
		backup, err := m.opts.ColManager.Save()
		if err != nil {
			m.status = fmt.Sprintf("Save failed: %v", err)
			m.statusErr = true
			return m, nil
		}
		m.dirtyCols = false
		if backup != "" {
			m.status = fmt.Sprintf("Saved collections to %s (backup created).", m.opts.ColManager.FilePath())
		} else {
			m.status = fmt.Sprintf("Saved collections to %s.", m.opts.ColManager.FilePath())
		}
		m.statusErr = false
		return m, nil

	case "r":
		if m.busy {
			return m, nil
		}
		if m.opts.Client == nil {
			m.status = "Not logged in. Press 'l' to log in via browser first."
			m.statusErr = true
			return m, nil
		}
		m.busy = true
		m.status = "Syncing owned games, purchase history, and licenses from Steam..."
		m.statusErr = false
		client := m.opts.Client
		return m, func() tea.Msg {
			cache, err := client.SyncFullLibrary(context.Background(), true, 25, nil)
			if err != nil {
				return syncCompleteMsg{err: err}
			}
			enriched := client.EnrichItems(context.Background(), cache.Items, false, 0, nil)
			return syncCompleteMsg{items: enriched}
		}

	case "m":
		if m.busy || m.opts.Client == nil {
			return m, nil
		}
		targets := m.filteredItems
		if len(targets) == 0 {
			return m, nil
		}
		m.busy = true
		m.status = "Fetching Steam Store metadata (genres, tags, Deck status) for filtered items..."
		m.statusErr = false
		client := m.opts.Client
		allItems := m.opts.Items
		return m, func() tea.Msg {
			enriched := client.EnrichItems(context.Background(), allItems, true, 30, nil)
			if cache, err := steam.LoadLibraryCache(client.Session().SteamID64); err == nil {
				cache.Items = enriched
				_ = steam.SaveLibraryCache(cache)
			}
			return syncCompleteMsg{items: enriched}
		}

	case "l":
		if m.busy {
			return m, nil
		}
		m.busy = true
		m.status = "Launching browser for Steam login..."
		m.statusErr = false
		return m, func() tea.Msg {
			sess, err := auth.LoginViaBrowser(context.Background(), auth.LoginOptions{
				Force:      true,
				LogProgress: func(string) {},
			})
			return loginCompleteMsg{sess: sess, err: err}
		}
	}

	return m, nil
}

func (m *Model) setQuickFilter(q string) {
	m.tab = tabLibrary
	m.filterQuery = q
	m.applyFilter()
	if q == "" {
		m.status = "Showing all library items."
	} else {
		m.status = fmt.Sprintf("Quick filter %q (%d items).", q, len(m.filteredItems))
	}
	m.statusErr = false
}

func (m *Model) moveCursor(delta int) {
	switch m.tab {
	case tabLibrary:
		m.cursor += delta
		if m.cursor >= len(m.filteredItems) {
			m.cursor = len(m.filteredItems) - 1
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
	case tabCollections:
		cols := m.opts.ColManager.ListCollections()
		m.colCursor += delta
		if m.colCursor >= len(cols) {
			m.colCursor = len(cols) - 1
		}
		if m.colCursor < 0 {
			m.colCursor = 0
		}
	case tabRules:
		if m.opts.RulesConfig != nil {
			m.ruleCursor += delta
			if m.ruleCursor >= len(m.opts.RulesConfig.Rules) {
				m.ruleCursor = len(m.opts.RulesConfig.Rules) - 1
			}
			if m.ruleCursor < 0 {
				m.ruleCursor = 0
			}
		}
	}
}

func (m *Model) targetAppIDsForAction() []uint32 {
	if len(m.selectedApps) > 0 {
		out := make([]uint32, 0, len(m.selectedApps))
		for id := range m.selectedApps {
			out = append(out, id)
		}
		return out
	}
	if len(m.filteredItems) > 0 && m.cursor < len(m.filteredItems) {
		return []uint32{m.filteredItems[m.cursor].AppID}
	}
	return nil
}

func (m *Model) applyRulesInTUI() (tea.Model, tea.Cmd) {
	if m.opts.RulesConfig == nil || len(m.opts.RulesConfig.Rules) == 0 {
		m.status = "No rules loaded. Pass --config rules.yaml to evaluate rules."
		m.statusErr = true
		return m, nil
	}
	if m.opts.ColManager == nil {
		m.status = "No collection manager loaded."
		m.statusErr = true
		return m, nil
	}
	results, err := rules.EvaluateRules(m.opts.Items, m.opts.RulesConfig)
	if err != nil {
		m.status = fmt.Sprintf("Rule evaluation error: %v", err)
		m.statusErr = true
		return m, nil
	}
	totalAdded := 0
	colsTouched := 0
	for _, res := range results {
		for colName, items := range res.CollectionMatches {
			ids := make([]uint32, len(items))
			for i, it := range items {
				ids[i] = it.AppID
			}
			added, _ := m.opts.ColManager.AddAppsToCollection(colName, ids)
			totalAdded += added
			colsTouched++
		}
	}
	m.dirtyCols = true
	m.status = fmt.Sprintf("Applied %d rules across %d target collections (%d new assignments). Press 's' to save.", len(results), colsTouched, totalAdded)
	m.statusErr = false
	return m, nil
}

// applyFilter parses our rich inline query syntax (e.g. "year:2025 type:purchase genre:rpg deck:verified price<10 unplayed")
// and converts it into a rules.Criteria to filter m.opts.Items.
func (m *Model) applyFilter() {
	q := strings.TrimSpace(m.filterQuery)
	if q == "" {
		m.filteredItems = m.opts.Items
		if m.cursor >= len(m.filteredItems) {
			m.cursor = 0
		}
		return
	}

	var crit rules.Criteria
	var nameTokens []string

	tokens := strings.Fields(q)
	for _, tok := range tokens {
		lower := strings.ToLower(tok)
		switch {
		case lower == "unplayed":
			t := true
			crit.Unplayed = &t
		case lower == "played":
			f := false
			crit.Unplayed = &f
		case lower == "free":
			t := true
			crit.IsFree = &t
		case lower == "refunded":
			t := true
			crit.IsRefunded = &t
		case lower == "bundle":
			t := true
			crit.IsBundle = &t
		case strings.HasPrefix(lower, "year:"):
			if yr, err := strconv.Atoi(strings.TrimPrefix(lower, "year:")); err == nil {
				crit.PurchaseYears = append(crit.PurchaseYears, yr)
			}
		case strings.HasPrefix(lower, "after:"):
			crit.PurchasedAfter = strings.TrimPrefix(lower, "after:")
		case strings.HasPrefix(lower, "before:"):
			crit.PurchasedBefore = strings.TrimPrefix(lower, "before:")
		case strings.HasPrefix(lower, "type:"):
			crit.AcquisitionTypes = append(crit.AcquisitionTypes, strings.TrimPrefix(lower, "type:"))
		case strings.HasPrefix(lower, "genre:"):
			crit.Genres = append(crit.Genres, strings.TrimPrefix(lower, "genre:"))
		case strings.HasPrefix(lower, "tag:"):
			crit.Tags = append(crit.Tags, strings.TrimPrefix(lower, "tag:"))
		case strings.HasPrefix(lower, "cat:"):
			crit.Categories = append(crit.Categories, strings.TrimPrefix(lower, "cat:"))
		case strings.HasPrefix(lower, "dev:"):
			crit.Developers = append(crit.Developers, strings.TrimPrefix(lower, "dev:"))
		case strings.HasPrefix(lower, "pub:"):
			crit.Publishers = append(crit.Publishers, strings.TrimPrefix(lower, "pub:"))
		case strings.HasPrefix(lower, "deck:"):
			crit.DeckCompatibility = append(crit.DeckCompatibility, strings.TrimPrefix(lower, "deck:"))
		case strings.HasPrefix(lower, "price<"):
			if v, err := strconv.ParseFloat(strings.TrimPrefix(lower, "price<"), 64); err == nil {
				crit.MaxPrice = &v
			}
		case strings.HasPrefix(lower, "price>"):
			if v, err := strconv.ParseFloat(strings.TrimPrefix(lower, "price>"), 64); err == nil {
				crit.MinPrice = &v
			}
		case strings.HasPrefix(lower, "hours<"):
			if v, err := strconv.ParseFloat(strings.TrimPrefix(lower, "hours<"), 64); err == nil {
				crit.MaxPlaytimeHours = &v
			}
		case strings.HasPrefix(lower, "hours>"):
			if v, err := strconv.ParseFloat(strings.TrimPrefix(lower, "hours>"), 64); err == nil {
				crit.MinPlaytimeHours = &v
			}
		default:
			nameTokens = append(nameTokens, tok)
		}
	}
	if len(nameTokens) > 0 {
		crit.NameContains = strings.Join(nameTokens, " ")
	}

	filtered, err := rules.FilterItems(m.opts.Items, crit)
	if err != nil {
		m.filteredItems = m.opts.Items
	} else {
		m.filteredItems = filtered
	}
	if m.cursor >= len(m.filteredItems) {
		m.cursor = maxInt(0, len(m.filteredItems)-1)
	}
}

var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#66C0F4")).
			Background(lipgloss.Color("#171A21")).
			Padding(0, 2)

	activeTabStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#171A21")).
			Background(lipgloss.Color("#66C0F4")).
			Padding(0, 2)

	inactiveTabStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#C7D5E0")).
				Background(lipgloss.Color("#2A475E")).
				Padding(0, 2)

	selectedRowStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#FFFFFF")).
				Background(lipgloss.Color("#2A475E"))

	normalRowStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#C7D5E0"))

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A4D007")).
			Bold(true)

	errStatusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF6B6B")).
			Bold(true)

	subtleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8F98A0"))
)

func (m *Model) View() string {
	var b strings.Builder

	// Top header bar
	acctInfo := "Not Logged In (Press 'l')"
	if m.opts.Session != nil {
		acctInfo = fmt.Sprintf("%s (%s)", m.opts.Session.PersonaName, m.opts.Session.SteamID64)
	}
	dirtyBadge := ""
	if m.dirtyCols {
		dirtyBadge = " [UNSAVED CHANGES - Press 's']"
	}
	titleBar := headerStyle.Render(fmt.Sprintf("STEAM ORGANIZER TUI  |  Account: %s  |  Selected: %d%s", acctInfo, len(m.selectedApps), dirtyBadge))
	b.WriteString(titleBar + "\n")

	// Tabs bar
	tabs := []string{
		fmt.Sprintf("1. Purchases & Library (%d/%d)", len(m.filteredItems), len(m.opts.Items)),
		fmt.Sprintf("2. Collections (%d)", m.collectionCount()),
		fmt.Sprintf("3. Rules (%d)", m.ruleCount()),
	}
	var renderedTabs []string
	for i, t := range tabs {
		if activeTab(i) == m.tab {
			renderedTabs = append(renderedTabs, activeTabStyle.Render(t))
		} else {
			renderedTabs = append(renderedTabs, inactiveTabStyle.Render(t))
		}
	}
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, renderedTabs...) + "\n\n")

	// Input prompt bar if active
	if m.mode != modeNormal {
		promptLabel := "Filter: "
		if m.mode == modeAssignCollection {
			promptLabel = "Add to Collection: "
		} else if m.mode == modeNewCollection {
			promptLabel = "New Collection Name: "
		}
		b.WriteString(fmt.Sprintf("  %s%s  (Enter to confirm, Esc to cancel)\n\n", promptLabel, m.textInput.View()))
	} else if m.tab == tabLibrary {
		qDisplay := m.filterQuery
		if qDisplay == "" {
			qDisplay = "None (Press '/' to search or 1:All 2:Unplayed 3:Purchases 4:Free 5:Bundles 6:DeckVerified)"
		}
		b.WriteString(subtleStyle.Render(fmt.Sprintf("  Active Filter: %s", qDisplay)) + "\n\n")
	}

	// Main content view
	listRows := maxInt(8, m.height-12)
	switch m.tab {
	case tabLibrary:
		b.WriteString(m.renderLibraryTab(listRows))
	case tabCollections:
		b.WriteString(m.renderCollectionsTab(listRows))
	case tabRules:
		b.WriteString(m.renderRulesTab(listRows))
	}

	// Footer status & keybindings
	b.WriteString("\n")
	if m.statusErr {
		b.WriteString("  " + errStatusStyle.Render(m.status) + "\n")
	} else {
		b.WriteString("  " + statusStyle.Render(m.status) + "\n")
	}
	help := "  Keys: [Tab] Switch View  [/] Filter  [Space] Select  [Ctrl+A] Select All  [a] Add to Collection  [s] Save  [r] Sync Steam  [m] Fetch Metadata  [l] Login  [q] Quit"
	b.WriteString(subtleStyle.Render(help))

	return b.String()
}

func (m *Model) renderLibraryTab(maxRows int) string {
	if len(m.filteredItems) == 0 {
		return "  No matching library items found. Press 'r' to sync from Steam or 'c' to clear filter.\n"
	}

	var b strings.Builder
	hdr := fmt.Sprintf("  %-3s %-8s %-34s %-11s %-11s %-8s %-7s %-8s %s",
		"Sel", "AppID", "Title", "Purchased", "Type", "Price", "Hours", "Deck", "Genres")
	b.WriteString(subtleStyle.Render(hdr) + "\n")
	b.WriteString(subtleStyle.Render("  "+strings.Repeat("-", 105)) + "\n")

	start := 0
	if m.cursor >= maxRows {
		start = m.cursor - maxRows + 1
	}
	end := minInt(len(m.filteredItems), start+maxRows)

	for i := start; i < end; i++ {
		it := m.filteredItems[i]
		sel := "[ ]"
		if m.selectedApps[it.AppID] {
			sel = "[x]"
		}
		dateStr := "-"
		if !it.PurchaseDate.IsZero() {
			dateStr = it.PurchaseDate.Format("2006-01-02")
		}
		acqStr := string(it.AcquisitionType)
		if acqStr == "" {
			acqStr = "unknown"
		}
		deckStr := string(it.DeckCompatibility)
		if deckStr == "" {
			deckStr = "-"
		}
		genresStr := truncateStr(strings.Join(it.Genres, ", "), 22)

		row := fmt.Sprintf("  %-3s %-8d %-34s %-11s %-11s %-8s %-7.1f %-8s %s",
			sel,
			it.AppID,
			truncateStr(it.Name, 33),
			dateStr,
			truncateStr(acqStr, 10),
			truncateStr(it.SummaryPrice(), 7),
			it.PlaytimeHours(),
			truncateStr(deckStr, 7),
			genresStr,
		)
		if i == m.cursor {
			b.WriteString(selectedRowStyle.Render(row) + "\n")
		} else {
			b.WriteString(normalRowStyle.Render(row) + "\n")
		}
	}
	return b.String()
}

func (m *Model) renderCollectionsTab(maxRows int) string {
	if m.opts.ColManager == nil {
		return "  No local Steam cloudstorage file loaded.\n"
	}
	cols := m.opts.ColManager.ListCollections()
	var b strings.Builder
	b.WriteString(subtleStyle.Render(fmt.Sprintf("  Storage File: %s", m.opts.ColManager.FilePath())) + "\n\n")
	if len(cols) == 0 {
		b.WriteString("  No collections found. Press 'n' to create a new collection.\n")
		return b.String()
	}

	hdr := fmt.Sprintf("  %-26s %-18s %-8s %-10s", "Collection Name", "ID", "Apps", "Type")
	b.WriteString(subtleStyle.Render(hdr) + "\n")
	b.WriteString(subtleStyle.Render("  "+strings.Repeat("-", 68)) + "\n")

	start := 0
	if m.colCursor >= maxRows {
		start = m.colCursor - maxRows + 1
	}
	end := minInt(len(cols), start+maxRows)

	for i := start; i < end; i++ {
		c := cols[i]
		cType := "Static"
		if c.IsDynamic {
			cType = "Dynamic"
		}
		row := fmt.Sprintf("  %-26s %-18s %-8d %-10s",
			truncateStr(c.Name, 25),
			truncateStr(c.ID, 17),
			len(c.Added),
			cType,
		)
		if i == m.colCursor {
			b.WriteString(selectedRowStyle.Render(row) + "\n")
		} else {
			b.WriteString(normalRowStyle.Render(row) + "\n")
		}
	}
	return b.String()
}

func (m *Model) renderRulesTab(maxRows int) string {
	if m.opts.RulesConfig == nil || len(m.opts.RulesConfig.Rules) == 0 {
		return "  No rules configuration loaded. Run with '--config rules.yaml' to preview and apply rules here.\n"
	}
	var b strings.Builder
	b.WriteString(subtleStyle.Render(fmt.Sprintf("  Rules File: %s  (Press 'a' to evaluate & apply all rules)", m.opts.RulesFilePath)) + "\n\n")

	results, _ := rules.EvaluateRules(m.opts.Items, m.opts.RulesConfig)
	hdr := fmt.Sprintf("  %-28s %-32s %-10s", "Rule Name", "Target Collection", "Matches")
	b.WriteString(subtleStyle.Render(hdr) + "\n")
	b.WriteString(subtleStyle.Render("  "+strings.Repeat("-", 74)) + "\n")

	for i, r := range m.opts.RulesConfig.Rules {
		matchCount := 0
		if i < len(results) {
			for _, list := range results[i].CollectionMatches {
				matchCount += len(list)
			}
		}
		row := fmt.Sprintf("  %-28s %-32s %-10d",
			truncateStr(r.Name, 27),
			truncateStr(r.Collection, 31),
			matchCount,
		)
		if i == m.ruleCursor {
			b.WriteString(selectedRowStyle.Render(row) + "\n")
		} else {
			b.WriteString(normalRowStyle.Render(row) + "\n")
		}
	}
	return b.String()
}

func (m *Model) collectionCount() int {
	if m.opts.ColManager == nil {
		return 0
	}
	return len(m.opts.ColManager.ListCollections())
}

func (m *Model) ruleCount() int {
	if m.opts.RulesConfig == nil {
		return 0
	}
	return len(m.opts.RulesConfig.Rules)
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

