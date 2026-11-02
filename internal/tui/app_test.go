package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/csull/go-steam-organizer/internal/auth"
	"github.com/csull/go-steam-organizer/internal/collections"
	"github.com/csull/go-steam-organizer/internal/models"
	"github.com/csull/go-steam-organizer/internal/rules"
)

func newTestTUIModel(t *testing.T) *Model {
	t.Helper()
	csFile := filepath.Join(t.TempDir(), "cloud-storage-namespace-1.json")
	colMgr, err := collections.LoadManager(csFile)
	if err != nil {
		t.Fatal(err)
	}

	items := []models.LibraryItem{
		{
			AppID:             1145350,
			Name:              "Hades II",
			PlaytimeMinutes:   0,
			PurchaseDate:      time.Date(2025, 11, 24, 0, 0, 0, 0, time.UTC),
			AcquisitionType:   models.AcquisitionPurchase,
			PriceCents:        2999,
			Genres:            []string{"Action", "RPG"},
			Tags:              []string{"Roguelike"},
			Categories:        []string{"Single-player"},
			Developers:        []string{"Supergiant Games"},
			Publishers:        []string{"Supergiant Games"},
			DeckCompatibility: models.DeckVerified,
		},
		{
			AppID:             620,
			Name:              "Portal 2",
			PlaytimeMinutes:   180,
			PurchaseDate:      time.Date(2024, 12, 20, 0, 0, 0, 0, time.UTC),
			AcquisitionType:   models.AcquisitionBundle,
			IsBundle:          true,
			PriceCents:        199,
			Genres:            []string{"Action", "Adventure"},
			Categories:        []string{"Co-op"},
			DeckCompatibility: models.DeckVerified,
		},
	}

	rulesCfg := &rules.ConfigFile{
		Rules: []rules.Rule{
			{
				Name:       "2025 Purchases",
				Collection: "Bought in 2025",
				Criteria:   rules.Criteria{PurchaseYears: []int{2025}},
			},
		},
	}

	ti := textinput.New()
	m := &Model{
		opts: Options{
			Session: &auth.Session{
				SteamID64:   "76561198000000001",
				PersonaName: "TestPlayer",
			},
			ColManager:    colMgr,
			Items:         items,
			RulesConfig:   rulesCfg,
			RulesFilePath: "rules.yaml",
		},
		tab:           tabLibrary,
		mode:          modeNormal,
		width:         120,
		height:        35,
		selectedApps:  make(map[uint32]bool),
		filteredItems: items,
		textInput:     ti,
	}
	m.applyFilter()
	return m
}

func sendKeyStr(m *Model, key string) {
	switch key {
	case "enter":
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	case "esc":
		m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	case "tab":
		m.Update(tea.KeyMsg{Type: tea.KeyTab})
	case "shift+tab":
		m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	case " ":
		m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	case "ctrl+a":
		m.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	default:
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	}
}

func TestTUIWorkflowsAndFilters(t *testing.T) {
	m := newTestTUIModel(t)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	// 1. Test inline filter syntax across all criteria tokens
	m.filterQuery = "year:2025 after:2025-01-01 before:2025-12-31 type:purchase genre:rpg tag:roguelike cat:single dev:supergiant pub:supergiant deck:verified price<50 price>10 hours<5 unplayed Hades"
	m.applyFilter()
	if len(m.filteredItems) != 1 || m.filteredItems[0].AppID != 1145350 {
		t.Fatalf("expected Hades II to match inline filter, got %d items", len(m.filteredItems))
	}

	// Clear filter with 'c'
	sendKeyStr(m, "c")
	if len(m.filteredItems) != 2 {
		t.Errorf("expected 2 items after clearing filter, got %d", len(m.filteredItems))
	}

	// Quick filter '2' (unplayed) and '1' (all)
	sendKeyStr(m, "2")
	if len(m.filteredItems) != 1 {
		t.Errorf("expected 1 unplayed item, got %d", len(m.filteredItems))
	}
	sendKeyStr(m, "1")
	if len(m.filteredItems) != 2 {
		t.Errorf("expected 2 items on quick filter '1', got %d", len(m.filteredItems))
	}

	// 2. Select item with Space and assign to collection 'My Favorites'
	sendKeyStr(m, " ")
	if !m.selectedApps[1145350] {
		t.Errorf("expected AppID 1145350 to be selected")
	}

	// Toggle select all with Ctrl+A
	sendKeyStr(m, "ctrl+a")
	if len(m.selectedApps) != 2 {
		t.Errorf("expected 2 selected apps after ctrl+a, got %d", len(m.selectedApps))
	}

	// Press 'a' to assign to collection
	sendKeyStr(m, "a")
	if m.mode != modeAssignCollection {
		t.Fatalf("expected modeAssignCollection")
	}
	m.textInput.SetValue("TUI Collection")
	sendKeyStr(m, "enter")

	col := m.opts.ColManager.FindByNameOrID("TUI Collection")
	if col == nil || len(col.Added) != 2 {
		t.Fatalf("expected TUI Collection with 2 apps, got %+v", col)
	}

	// 3. Switch to Collections tab, render view, create a new collection with 'n'
	sendKeyStr(m, "tab")
	if m.tab != tabCollections {
		t.Fatalf("expected tabCollections, got %v", m.tab)
	}
	viewCols := m.View()
	if !strings.Contains(viewCols, "TUI Collection") {
		t.Errorf("expected Collections view to contain 'TUI Collection'")
	}

	sendKeyStr(m, "n")
	m.textInput.SetValue("Created In TUI")
	sendKeyStr(m, "enter")
	if m.opts.ColManager.FindByNameOrID("Created In TUI") == nil {
		t.Errorf("expected 'Created In TUI' collection to exist")
	}

	// 4. Switch to Rules tab, render view, and apply rules with 'a'
	sendKeyStr(m, "tab")
	if m.tab != tabRules {
		t.Fatalf("expected tabRules, got %v", m.tab)
	}
	viewRules := m.View()
	if !strings.Contains(viewRules, "2025 Purchases") {
		t.Errorf("expected Rules view to list '2025 Purchases'")
	}
	sendKeyStr(m, "a")
	ruleCol := m.opts.ColManager.FindByNameOrID("Bought in 2025")
	if ruleCol == nil || !ruleCol.ContainsApp(1145350) {
		t.Errorf("expected 'Bought in 2025' collection with Hades II after applying rules")
	}

	// 5. Save collections with 's'
	sendKeyStr(m, "s")
	if m.dirtyCols {
		t.Errorf("expected dirtyCols to be false after saving")
	}
}

