package cmd

import (
	"fmt"
	"strings"

	"github.com/csull/go-steam-organizer/internal/auth"
	"github.com/csull/go-steam-organizer/internal/rules"
	"github.com/csull/go-steam-organizer/internal/steam"
	"github.com/spf13/cobra"
)

var (
	flagColName      string
	flagColApps      []uint
	flagColTitles    []string
	flagColDryRun    bool
	flagColSyncCloud bool
	flagColFromRules bool
)

var collectionsCmd = &cobra.Command{
	Use:   "collections",
	Short: "List and manage Steam Library collections and add/remove specific items",
}

var collectionsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all Steam Library collections",
	RunE: func(cmd *cobra.Command, args []string) error {
		sess, _ := auth.LoadSession()
		mgr, err := loadCollectionManager(sess)
		if err != nil {
			return err
		}

		if flagColSyncCloud {
			authSess, err := requireSession(cmd.Context())
			if err != nil {
				return err
			}
			client := steam.NewClient(authSess)
			_ = mgr.SyncWithCloud(cmd.Context(), client)
		}

		cols := mgr.ListCollections()
		fmt.Printf("Cloud Storage File: %s\n", mgr.FilePath())
		fmt.Printf("Total Collections:  %d\n\n", len(cols))
		if len(cols) == 0 {
			return nil
		}

		fmt.Printf("%-30s %-20s %-8s %-10s\n", "NAME", "ID", "APPS", "TYPE")
		fmt.Println(strings.Repeat("-", 72))
		for _, c := range cols {
			cType := "Static"
			if c.IsDynamic {
				cType = "Dynamic"
			}
			fmt.Printf("%-30s %-20s %-8d %-10s\n", c.Name, c.ID, len(c.Added), cType)
		}
		return nil
	},
}

var collectionsAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add specific library items (by AppID or title) to a Steam collection",
	Example: `  steam-organizer collections add --collection "Favorites" --app 440,620
  steam-organizer collections add --collection "Co-op Night" --title "Portal 2","Left 4 Dead 2"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(flagColName) == "" {
			return fmt.Errorf("--collection is required")
		}
		if len(flagColApps) == 0 && len(flagColTitles) == 0 {
			return fmt.Errorf("specify at least one item via --app <appid> or --title <game title>")
		}

		var targetIDs []uint32
		for _, id := range flagColApps {
			targetIDs = append(targetIDs, uint32(id))
		}

		var sess *auth.Session
		// If titles are provided, resolve them against the user's Steam library (prompting login if needed)
		if len(flagColTitles) > 0 {
			var err error
			sess, err = requireSession(cmd.Context())
			if err != nil {
				return err
			}
			client := steam.NewClient(sess)
			cache, err := client.SyncFullLibrary(cmd.Context(), false, 15, func(msg string) {
				fmt.Println(msg)
			})
			if err != nil {
				return err
			}
			for _, queryTitle := range flagColTitles {
				qLower := strings.ToLower(strings.TrimSpace(queryTitle))
				matched := false
				for _, item := range cache.Items {
					if strings.EqualFold(item.Name, qLower) || strings.Contains(strings.ToLower(item.Name), qLower) {
						fmt.Printf("Resolved %q -> %s (AppID %d)\n", queryTitle, item.Name, item.AppID)
						targetIDs = append(targetIDs, item.AppID)
						matched = true
					}
				}
				if !matched {
					fmt.Printf("Warning: no library item matched title %q\n", queryTitle)
				}
			}
		} else {
			sess, _ = auth.LoadSession()
		}

		if len(targetIDs) == 0 {
			return fmt.Errorf("no valid AppIDs resolved to add")
		}

		mgr, err := loadCollectionManager(sess)
		if err != nil {
			return err
		}

		if flagColSyncCloud {
			if sess == nil || !sess.IsValid() {
				sess, err = requireSession(cmd.Context())
				if err != nil {
					return err
				}
			}
			_ = mgr.SyncWithCloud(cmd.Context(), steam.NewClient(sess))
		}

		addedCount, col := mgr.AddAppsToCollection(flagColName, targetIDs)
		fmt.Printf("Collection %q (%s): +%d new app(s) added (%d total apps in collection).\n",
			col.Name, col.ID, addedCount, len(col.Added))

		if flagColDryRun {
			fmt.Println("[Dry-Run] No changes written to disk.")
			return nil
		}

		backup, err := mgr.Save()
		if err != nil {
			return err
		}
		fmt.Printf("Saved updated collections to %s\n", mgr.FilePath())
		if backup != "" {
			fmt.Printf("Backup created at %s\n", backup)
		}
		return nil
	},
}

var collectionsRemoveCmd = &cobra.Command{
	Use:   "remove",
	Short: "Remove specific library items from a Steam collection (or remove entire collections with --from-rules)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagColFromRules {
			cfgPath := resolveRulesConfigPath(args)
			cfg, err := rules.LoadConfigFile(cfgPath)
			if err != nil {
				return err
			}
			return removeCollectionsForRulesFile(cmd.Context(), cfg, cfgPath, flagColDryRun, flagColSyncCloud)
		}
		if strings.TrimSpace(flagColName) == "" {
			return fmt.Errorf("--collection is required (or pass --from-rules to remove all collections in the rules file)")
		}
		if len(flagColApps) == 0 {
			return fmt.Errorf("specify at least one AppID via --app (or use 'collections delete --collection %q' to delete the entire collection)", flagColName)
		}

		var targetIDs []uint32
		for _, id := range flagColApps {
			targetIDs = append(targetIDs, uint32(id))
		}

		sess, _ := auth.LoadSession()
		mgr, err := loadCollectionManager(sess)
		if err != nil {
			return err
		}

		removed, err := mgr.RemoveAppsFromCollection(flagColName, targetIDs)
		if err != nil {
			return err
		}
		fmt.Printf("Removed %d app(s) from collection %q.\n", removed, flagColName)

		if flagColDryRun {
			fmt.Println("[Dry-Run] No changes written to disk.")
			return nil
		}

		backup, err := mgr.Save()
		if err != nil {
			return err
		}
		fmt.Printf("Saved updated collections to %s\n", mgr.FilePath())
		if backup != "" {
			fmt.Printf("Backup created at %s\n", backup)
		}
		return nil
	},
}

var collectionsDeleteCmd = &cobra.Command{
	Use:     "delete",
	Aliases: []string{"del", "rm"},
	Short:   "Delete an entire Steam collection by name/ID or remove all collections specified in a rules file",
	Example: `  steam-organizer collections delete --collection "Gift"
  steam-organizer collections delete --from-rules --config rules.example.yaml`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagColFromRules || (strings.TrimSpace(flagColName) == "" && (flagConfigFile != "" || len(args) > 0)) {
			cfgPath := resolveRulesConfigPath(args)
			cfg, err := rules.LoadConfigFile(cfgPath)
			if err != nil {
				return err
			}
			return removeCollectionsForRulesFile(cmd.Context(), cfg, cfgPath, flagColDryRun, flagColSyncCloud)
		}

		if strings.TrimSpace(flagColName) == "" {
			return fmt.Errorf("specify --collection <name> or --from-rules [--config rules.yaml]")
		}

		sess, _ := auth.LoadSession()
		mgr, err := loadCollectionManager(sess)
		if err != nil {
			return err
		}

		deletedCol, ok := mgr.DeleteCollection(flagColName)
		if !ok {
			return fmt.Errorf("collection %q not found", flagColName)
		}
		fmt.Printf("Deleted collection %q (%s, %d apps).\n", deletedCol.Name, deletedCol.ID, len(deletedCol.Added))

		if flagColDryRun {
			fmt.Println("[Dry-Run] No changes written to disk.")
			return nil
		}

		backup, err := mgr.Save()
		if err != nil {
			return err
		}
		fmt.Printf("Saved updated collections to %s\n", mgr.FilePath())
		if backup != "" {
			fmt.Printf("Backup created at %s\n", backup)
		}
		return nil
	},
}

func init() {
	collectionsListCmd.Flags().BoolVar(&flagColSyncCloud, "sync-cloud", false, "Sync collection metadata with Steam Cloud Config Store")

	collectionsAddCmd.Flags().StringVarP(&flagColName, "collection", "n", "", "Target collection name or ID (created if it doesn't exist)")
	collectionsAddCmd.Flags().UintSliceVarP(&flagColApps, "app", "a", nil, "AppID(s) to add to the collection")
	collectionsAddCmd.Flags().StringSliceVarP(&flagColTitles, "title", "t", nil, "Game title(s) to resolve and add to the collection")
	collectionsAddCmd.Flags().BoolVar(&flagColDryRun, "dry-run", false, "Preview changes without modifying cloud-storage-namespace-1.json")
	collectionsAddCmd.Flags().BoolVar(&flagColSyncCloud, "sync-cloud", false, "Sync collection state with Steam Cloud Config Store")

	collectionsRemoveCmd.Flags().StringVarP(&flagColName, "collection", "n", "", "Target collection name or ID")
	collectionsRemoveCmd.Flags().UintSliceVarP(&flagColApps, "app", "a", nil, "AppID(s) to remove from the collection")
	collectionsRemoveCmd.Flags().BoolVar(&flagColFromRules, "from-rules", false, "Remove all collections specified in the rules file")
	collectionsRemoveCmd.Flags().BoolVar(&flagColDryRun, "dry-run", false, "Preview changes without modifying cloud-storage-namespace-1.json")

	collectionsDeleteCmd.Flags().StringVarP(&flagColName, "collection", "n", "", "Collection name or ID to delete")
	collectionsDeleteCmd.Flags().BoolVar(&flagColFromRules, "from-rules", false, "Remove all collections specified in the rules file")
	collectionsDeleteCmd.Flags().BoolVar(&flagColDryRun, "dry-run", false, "Preview changes without modifying cloud-storage-namespace-1.json")
	collectionsDeleteCmd.Flags().BoolVar(&flagColSyncCloud, "sync-cloud", false, "Sync collection state with Steam Cloud Config Store")

	collectionsCmd.AddCommand(collectionsListCmd, collectionsAddCmd, collectionsRemoveCmd, collectionsDeleteCmd)
	rootCmd.AddCommand(collectionsCmd)
}

