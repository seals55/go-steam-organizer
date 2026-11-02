package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/csull/go-steam-organizer/internal/auth"
	"github.com/csull/go-steam-organizer/internal/models"
	"github.com/csull/go-steam-organizer/internal/rules"
	"github.com/csull/go-steam-organizer/internal/steam"
	"github.com/csull/go-steam-organizer/internal/tui"
	"github.com/spf13/cobra"
)

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Launch the interactive Terminal UI (TUI) for browsing purchases and managing collections",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInteractiveTUI(cmd.Context())
	},
}

func runInteractiveTUI(ctx context.Context) error {
	sess, err := auth.LoadSession()
	hasCachedLibrary := false
	if err == nil && sess != nil && sess.SteamID64 != "" {
		if cached, cErr := steam.LoadLibraryCache(sess.SteamID64); cErr == nil && len(cached.Items) > 0 {
			hasCachedLibrary = true
		}
	} else if cached, cErr := steam.LoadLibraryCache(""); cErr == nil && len(cached.Items) > 0 && cached.SteamID64 != "" {
		hasCachedLibrary = true
		acctID, _ := auth.SteamID64ToAccountID(cached.SteamID64)
		sess = &auth.Session{
			SteamID64:   cached.SteamID64,
			AccountID:   acctID,
			PersonaName: "Cached Session",
		}
		err = nil
	}
	if (err != nil || !sess.IsValid()) && !hasCachedLibrary {
		fmt.Println("No active Steam session found. Prompting browser login...")
		sess, err = auth.EnsureAuthenticated(ctx, auth.LoginOptions{
			APIKey: flagAPIKey,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: starting TUI without active login (%v)\n", err)
		}
	}

	var client *steam.Client
	var items []models.LibraryItem
	var purchases []models.PurchaseRecord
	var licenses []models.LicenseRecord
	if sess != nil && (sess.IsValid() || hasCachedLibrary) {
		client = steam.NewClient(sess)
		if cache, err := client.SyncFullLibrary(ctx, false, 20, func(msg string) {
			fmt.Println(msg)
		}); err == nil {
			items = client.EnrichItems(ctx, cache.Items, false, 0, nil)
			purchases = cache.Purchases
			licenses = cache.Licenses
		}
	}

	colMgr, _ := loadCollectionManager(sess)

	rulesPath := flagConfigFile
	if rulesPath == "" {
		if _, err := os.Stat("rules.yaml"); err == nil {
			rulesPath = "rules.yaml"
		} else if _, err := os.Stat("rules.example.yaml"); err == nil {
			rulesPath = "rules.example.yaml"
		}
	}
	var rulesCfg *rules.ConfigFile
	if rulesPath != "" {
		rulesCfg, _ = rules.LoadConfigFile(rulesPath)
	}

	return tui.Run(tui.Options{
		Session:       sess,
		Client:        client,
		ColManager:    colMgr,
		Items:         items,
		Purchases:     purchases,
		Licenses:      licenses,
		RulesConfig:   rulesCfg,
		RulesFilePath: rulesPath,
	})
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}

