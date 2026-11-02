package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/csull/go-steam-organizer/internal/auth"
	"github.com/csull/go-steam-organizer/internal/rules"
	"github.com/csull/go-steam-organizer/internal/steam"
	"github.com/spf13/cobra"
)

var (
	flagOrgDryRun    bool
	flagOrgRefresh   bool
	flagOrgEnrich    bool
	flagOrgMaxFetch  int
	flagOrgSyncCloud bool
	flagOrgRemove    bool
)

var organizeCmd = &cobra.Command{
	Use:     "organize",
	Aliases: []string{"apply"},
	Short:   "Evaluate a YAML/JSON rules file and organize matching Steam library items into collections (or remove rule collections with --remove)",
	Example: `  steam-organizer organize --config rules.example.yaml --dry-run
  steam-organizer organize --config rules.yaml --enrich-metadata
  steam-organizer organize --config rules.example.yaml --remove`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfgPath := resolveRulesConfigPath(args)

		cfg, err := rules.LoadConfigFile(cfgPath)
		if err != nil {
			return err
		}

		if flagOrgRemove {
			return removeCollectionsForRulesFile(cmd.Context(), cfg, cfgPath, flagOrgDryRun, flagOrgSyncCloud)
		}

		sess, err := requireSessionOrCached(cmd.Context(), flagOrgRefresh)
		if err != nil {
			return err
		}

		client := steam.NewClient(sess)
		cache, err := client.SyncFullLibrary(cmd.Context(), flagOrgRefresh, 25, func(msg string) {
			fmt.Println(msg)
		})
		if err != nil {
			return err
		}

		needsMeta := flagOrgEnrich
		for _, r := range cfg.Rules {
			if r.Criteria.NeedsStoreMetadata() {
				needsMeta = true
				break
			}
		}

		items := client.EnrichItems(cmd.Context(), cache.Items, needsMeta, flagOrgMaxFetch, func(msg string) {
			fmt.Println(msg)
		})
		cache.Items = items
		_ = steam.SaveLibraryCache(cache)

		results, err := rules.EvaluateRules(items, cfg)
		if err != nil {
			return err
		}

		mgr, err := loadCollectionManager(sess)
		if err != nil {
			return err
		}
		if flagOrgSyncCloud {
			_ = mgr.SyncWithCloud(cmd.Context(), client)
		}

		fmt.Printf("\nEvaluated %d rule(s) from %s:\n\n", len(results), cfgPath)
		totalAdded := 0
		for _, res := range results {
			fmt.Printf("Rule: %s\n", res.Rule.Name)
			if len(res.CollectionMatches) == 0 {
				fmt.Printf("  -> No matching items.\n")
				continue
			}

			var colNames []string
			for k := range res.CollectionMatches {
				colNames = append(colNames, k)
			}
			sort.Strings(colNames)

			for _, colName := range colNames {
				colItems := res.CollectionMatches[colName]
				appIDs := make([]uint32, len(colItems))
				for i, it := range colItems {
					appIDs[i] = it.AppID
				}
				added, col := mgr.AddAppsToCollection(colName, appIDs)
				totalAdded += added
				fmt.Printf("  -> Collection %q (%s): %d matched, +%d newly added (%d total)\n",
					col.Name, col.ID, len(colItems), added, len(col.Added))
			}
		}

		if flagOrgDryRun {
			fmt.Printf("\n[Dry-Run] %d new item assignment(s) previewed. No changes written to disk.\n", totalAdded)
			return nil
		}

		backup, err := mgr.Save()
		if err != nil {
			return err
		}
		fmt.Printf("\nSaved updated collections (%d new assignments) to %s\n", totalAdded, mgr.FilePath())
		if backup != "" {
			fmt.Printf("Backup created at %s\n", backup)
		}
		return nil
	},
}

func resolveRulesConfigPath(args []string) string {
	cfgPath := flagConfigFile
	if cfgPath == "" && len(args) > 0 {
		cfgPath = args[0]
	}
	if cfgPath == "" {
		if _, err := os.Stat("rules.yaml"); err == nil {
			cfgPath = "rules.yaml"
		} else if _, err := os.Stat("rules.example.yaml"); err == nil {
			cfgPath = "rules.example.yaml"
		} else {
			cfgPath = "rules.yaml"
		}
	}
	return cfgPath
}

func removeCollectionsForRulesFile(ctx context.Context, cfg *rules.ConfigFile, cfgPath string, dryRun, syncCloud bool) error {
	sess, _ := auth.LoadSession()
	mgr, err := loadCollectionManager(sess)
	if err != nil {
		return err
	}
	if syncCloud {
		authSess, err := requireSession(ctx)
		if err != nil {
			return err
		}
		_ = mgr.SyncWithCloud(ctx, steam.NewClient(authSess))
	}

	existing := mgr.ListCollections()
	toDelete := rules.MatchingCollectionsForRules(cfg, existing)

	fmt.Printf("\nRemoving collections specified in %s (%d rules):\n\n", cfgPath, len(cfg.Rules))
	if len(toDelete) == 0 {
		fmt.Println("No matching collections from the rules file were found in your Steam Library.")
		return nil
	}

	removedCount := 0
	for _, col := range toDelete {
		if _, ok := mgr.DeleteCollection(col.ID); ok {
			removedCount++
			fmt.Printf("  - Removed collection %q (%s, %d apps)\n", col.Name, col.ID, len(col.Added))
		}
	}

	if dryRun {
		fmt.Printf("\n[Dry-Run] %d collection(s) would be removed. No changes written to disk.\n", removedCount)
		return nil
	}

	backup, err := mgr.Save()
	if err != nil {
		return err
	}
	fmt.Printf("\nRemoved %d collection(s) and saved to %s\n", removedCount, mgr.FilePath())
	if backup != "" {
		fmt.Printf("Backup created at %s\n", backup)
	}
	return nil
}

func init() {
	organizeCmd.Flags().BoolVar(&flagOrgDryRun, "dry-run", false, "Preview rule matches and collection changes without saving")
	organizeCmd.Flags().BoolVarP(&flagOrgRefresh, "refresh", "r", false, "Force refresh of library and purchase history from Steam")
	organizeCmd.Flags().BoolVar(&flagOrgEnrich, "enrich-metadata", false, "Fetch missing store metadata from Steam Store")
	organizeCmd.Flags().IntVar(&flagOrgMaxFetch, "max-metadata-fetch", 80, "Maximum uncached apps to fetch store metadata for")
	organizeCmd.Flags().BoolVar(&flagOrgSyncCloud, "sync-cloud", false, "Sync collections with Steam Cloud Config Store")
	organizeCmd.Flags().BoolVar(&flagOrgRemove, "remove", false, "Remove all collections specified in the rules file from Steam")

	rootCmd.AddCommand(organizeCmd)
}

