package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/csull/go-steam-organizer/internal/auth"
	"github.com/csull/go-steam-organizer/internal/collections"
	"github.com/csull/go-steam-organizer/internal/models"
	"github.com/csull/go-steam-organizer/internal/steam"
)

func setupTestCLIEnv(t *testing.T) string {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("USERPROFILE", tmpHome)
	t.Setenv("HOME", tmpHome)

	csFile := filepath.Join(tmpHome, "cloud-storage-namespace-1.json")
	if err := os.WriteFile(csFile, []byte("[]"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STEAM_CLOUDSTORAGE_FILE", csFile)

	sess := &auth.Session{
		SteamID64:        "76561198000000001",
		AccountID:        39734273,
		PersonaName:      "CLITester",
		SteamLoginSecure: "76561198000000001||validtoken",
		WebAPIToken:      "validtoken",
		ExpiresAt:        time.Now().Add(4 * time.Hour),
		CreatedAt:        time.Now(),
	}
	if err := sess.Save(); err != nil {
		t.Fatal(err)
	}

	cache := &steam.LibraryCache{
		SteamID64: sess.SteamID64,
		UpdatedAt: time.Now(),
		Items: []models.LibraryItem{
			{
				AppID:             1145350,
				Name:              "Hades II",
				PlaytimeMinutes:   0,
				PurchaseDate:      time.Date(2025, 11, 24, 0, 0, 0, 0, time.UTC),
				AcquisitionType:   models.AcquisitionPurchase,
				PriceCents:        2999,
				Genres:            []string{"Action", "RPG"},
				DeckCompatibility: models.DeckVerified,
			},
			{
				AppID:             620,
				Name:              "Portal 2",
				PlaytimeMinutes:   300,
				PurchaseDate:      time.Date(2024, 12, 20, 0, 0, 0, 0, time.UTC),
				AcquisitionType:   models.AcquisitionBundle,
				IsBundle:          true,
				PriceCents:        199,
				Genres:            []string{"Action", "Adventure"},
				DeckCompatibility: models.DeckVerified,
			},
		},
		Purchases: []models.PurchaseRecord{
			{
				TransactionID:   "111",
				Date:            time.Date(2025, 11, 24, 0, 0, 0, 0, time.UTC),
				Items:           []string{"Hades II"},
				AcquisitionType: models.AcquisitionPurchase,
				PriceCents:      2999,
				PriceDisplay:    "$29.99",
			},
		},
	}
	if err := steam.SaveLibraryCache(cache); err != nil {
		t.Fatal(err)
	}
	return csFile
}

func TestCLICommandsEndToEnd(t *testing.T) {
	csFile := setupTestCLIEnv(t)

	// 1. Test `login --status`
	rootCmd.SetArgs([]string{"login", "--status"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("login --status failed: %v", err)
	}
	flagLoginStatus = false

	// 2. Test `collections add` by AppID and by Title
	rootCmd.SetArgs([]string{"collections", "add", "--collection", "Favorites", "--app", "440", "--title", "Portal 2"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("collections add failed: %v", err)
	}

	mgr, err := collections.LoadManager(csFile)
	if err != nil {
		t.Fatal(err)
	}
	fav := mgr.FindByNameOrID("Favorites")
	if fav == nil || !fav.ContainsApp(440) || !fav.ContainsApp(620) {
		t.Fatalf("expected Favorites to contain 440 and 620, got %+v", fav)
	}

	// 3. Test `collections list`
	rootCmd.SetArgs([]string{"collections", "list"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("collections list failed: %v", err)
	}

	// 4. Test `collections remove`
	rootCmd.SetArgs([]string{"collections", "remove", "--collection", "Favorites", "--app", "440"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("collections remove failed: %v", err)
	}

	// 5. Test `purchases` query + `--add-to` with template
	flagColApps = nil
	flagColTitles = nil
	rootCmd.SetArgs([]string{"purchases", "--year", "2025", "--add-to", "Bought in {{.PurchaseYear}}"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("purchases --year 2025 failed: %v", err)
	}

	mgr, _ = collections.LoadManager(csFile)
	col2025 := mgr.FindByNameOrID("Bought in 2025")
	if col2025 == nil || !col2025.ContainsApp(1145350) {
		t.Fatalf("expected 'Bought in 2025' collection to contain Hades II (1145350), got %+v", col2025)
	}

	// 6. Test `organize` with rules file
	rulesPath := filepath.Join(t.TempDir(), "test_rules.yaml")
	rulesYAML := `
rules:
  - name: "Unplayed Games"
    collection: "Backlog"
    criteria:
      unplayed: true
`
	if err := os.WriteFile(rulesPath, []byte(rulesYAML), 0644); err != nil {
		t.Fatal(err)
	}
	rootCmd.SetArgs([]string{"organize", "--config", rulesPath})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("organize failed: %v", err)
	}

	mgr, _ = collections.LoadManager(csFile)
	backlog := mgr.FindByNameOrID("Backlog")
	if backlog == nil || !backlog.ContainsApp(1145350) {
		t.Fatalf("expected 'Backlog' collection to contain Hades II (1145350), got %+v", backlog)
	}

	// 7. Test `login --logout`
	rootCmd.SetArgs([]string{"login", "--logout"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("login --logout failed: %v", err)
	}
	flagLoginLogout = false

	// 8. Test `purchases --raw-transactions` and flag combinations using cached library fallback (after logout)
	rootCmd.SetArgs([]string{"purchases", "--raw-transactions"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("purchases --raw-transactions failed: %v", err)
	}
	flagPurRawTxnsOnly = false

	rootCmd.SetArgs([]string{
		"purchases",
		"--min-price", "10",
		"--max-price", "50",
		"--free=false",
		"--refunded=false",
		"--bundle=false",
		"--unplayed=true",
		"--min-hours", "0",
		"--max-hours", "5",
		"--add-to", "DryRunCol",
		"--dry-run",
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("purchases with all flags + dry-run failed: %v", err)
	}
	flagPurDryRun = false
	flagPurAddToCol = ""

	// 9. Test `organize --dry-run`
	rootCmd.SetArgs([]string{"organize", "--config", rulesPath, "--dry-run"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("organize --dry-run failed: %v", err)
	}
	flagOrgDryRun = false

	// 10. Test truncateCLI helper
	if got := truncateCLI("abcdef", 2); got != "ab" {
		t.Errorf("expected 'ab', got %q", got)
	}
	if got := truncateCLI("abcdefghij", 6); got != "abc..." {
		t.Errorf("expected 'abc...', got %q", got)
	}
}

