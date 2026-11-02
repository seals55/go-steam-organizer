package rules

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/csull/go-steam-organizer/internal/models"
)

func TestFilterItemsAndEvaluateRules(t *testing.T) {
	items := []models.LibraryItem{
		{
			AppID:             1145350,
			Name:              "Hades II",
			PlaytimeMinutes:   0,
			PurchaseDate:      time.Date(2025, 11, 24, 0, 0, 0, 0, time.UTC),
			AcquisitionType:   models.AcquisitionPurchase,
			PriceCents:        2999,
			Genres:            []string{"Action", "Indie", "RPG"},
			Tags:              []string{"Roguelike", "Mythology"},
			Categories:        []string{"Single-player", "Full controller support"},
			Developers:        []string{"Supergiant Games"},
			Publishers:        []string{"Supergiant Games"},
			DeckCompatibility: models.DeckVerified,
		},
		{
			AppID:             620,
			Name:              "Portal 2",
			PlaytimeMinutes:   90, // 1.5 hours
			PurchaseDate:      time.Date(2024, 12, 20, 0, 0, 0, 0, time.UTC),
			AcquisitionType:   models.AcquisitionBundle,
			IsBundle:          true,
			PriceCents:        199,
			Genres:            []string{"Action", "Adventure"},
			Categories:        []string{"Single-player", "Co-op"},
			Developers:        []string{"Valve"},
			Publishers:        []string{"Valve"},
			DeckCompatibility: models.DeckVerified,
		},
		{
			AppID:             1172470,
			Name:              "Apex Legends",
			PlaytimeMinutes:   1200,
			PurchaseDate:      time.Date(2023, 5, 5, 0, 0, 0, 0, time.UTC),
			AcquisitionType:   models.AcquisitionFree,
			IsFree:            true,
			PriceCents:        0,
			Genres:            []string{"Action", "Free To Play"},
			DeckCompatibility: models.DeckUnsupported,
		},
	}

	unplayedTrue := true
	maxPrice5 := 5.0
	minPrice10 := 10.0
	maxHours2 := 2.0
	minHours10 := 10.0
	isFreeTrue := true
	isBundleTrue := true

	cfg := &ConfigFile{
		Rules: []Rule{
			{
				Name:       "By Purchase Year",
				Collection: "Bought in {{.PurchaseYear}}",
				Criteria: Criteria{
					PurchaseYears: []int{2024, 2025},
				},
			},
			{
				Name:       "Unplayed Deck Verified RPGs",
				Collection: "Deck RPG Backlog",
				Criteria: Criteria{
					Unplayed:          &unplayedTrue,
					Genres:            []string{"RPG"},
					Tags:              []string{"Roguelike"},
					Developers:        []string{"Supergiant"},
					Publishers:        []string{"Supergiant"},
					MinPrice:          &minPrice10,
					DeckCompatibility: []string{"verified"},
				},
			},
			{
				Name:       "Cheap Under 2 Hours Co-op Bundle",
				Collection: "Quick Bargain Co-op",
				Criteria: Criteria{
					MaxPrice:         &maxPrice5,
					MaxPlaytimeHours: &maxHours2,
					Categories:       []string{"Co-op"},
					IsBundle:         &isBundleTrue,
					PurchasedAfter:   "2024-01-01",
					PurchasedBefore:  "2024-12-31",
				},
			},
			{
				Name:       "Free Veterans",
				Collection: "Free Veterans",
				Criteria: Criteria{
					IsFree:           &isFreeTrue,
					MinPlaytimeHours: &minHours10,
					AcquisitionTypes: []string{"free"},
					NameContains:     "Apex",
					NameRegex:        "^Apex.*",
					AppIDs:           []uint32{1172470},
				},
			},
		},
	}

	results, err := EvaluateRules(items, cfg)
	if err != nil {
		t.Fatalf("EvaluateRules failed: %v", err)
	}

	if len(results[0].CollectionMatches["Bought in 2025"]) != 1 || len(results[0].CollectionMatches["Bought in 2024"]) != 1 {
		t.Errorf("unexpected dynamic year matches: %+v", results[0].CollectionMatches)
	}

	if matched := results[1].CollectionMatches["Deck RPG Backlog"]; len(matched) != 1 || matched[0].AppID != 1145350 {
		t.Errorf("expected Hades II in Deck RPG Backlog, got: %+v", matched)
	}

	if matched := results[2].CollectionMatches["Quick Bargain Co-op"]; len(matched) != 1 || matched[0].AppID != 620 {
		t.Errorf("expected Portal 2 in Quick Bargain Co-op, got: %+v", matched)
	}

	if matched := results[3].CollectionMatches["Free Veterans"]; len(matched) != 1 || matched[0].AppID != 1172470 {
		t.Errorf("expected Apex Legends in Free Veterans, got: %+v", matched)
	}
}

func TestLoadConfigFileAndValidationErrors(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "rules.yaml")
	jsonPath := filepath.Join(tmpDir, "rules.json")

	yamlContent := `
rules:
  - name: "Test YAML Rule"
    collection: "YAML Col"
    criteria:
      genres: ["RPG"]
`
	jsonContent := `{"rules":[{"name":"Test JSON Rule","collection":"JSON Col","criteria":{"tags":["Indie"]}}]}`

	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonPath, []byte(jsonContent), 0644); err != nil {
		t.Fatal(err)
	}

	yCfg, err := LoadConfigFile(yamlPath)
	if err != nil || len(yCfg.Rules) != 1 || !yCfg.Rules[0].Criteria.NeedsStoreMetadata() {
		t.Fatalf("LoadConfigFile YAML failed: %v", err)
	}

	jCfg, err := LoadConfigFile(jsonPath)
	if err != nil || len(jCfg.Rules) != 1 || !jCfg.Rules[0].Criteria.NeedsStoreMetadata() {
		t.Fatalf("LoadConfigFile JSON failed: %v", err)
	}

	// Test invalid date & invalid regex handling
	if _, err := FilterItems(nil, Criteria{PurchasedAfter: "bad-date"}); err == nil {
		t.Errorf("expected error for invalid PurchasedAfter date")
	}
	if _, err := FilterItems(nil, Criteria{PurchasedBefore: "bad-date"}); err == nil {
		t.Errorf("expected error for invalid PurchasedBefore date")
	}
	if _, err := FilterItems(nil, Criteria{NameRegex: "[unclosed"}); err == nil {
		t.Errorf("expected error for invalid NameRegex")
	}
}
