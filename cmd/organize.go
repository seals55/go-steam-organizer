package cmd

import (
	"fmt"
	"sort"

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
)

var organizeCmd = &cobra.Command{
	Use:     "organize",
	Aliases: []string{"apply"},
	Short:   "Evaluate a YAML/JSON rules file and organize matching Steam library items into collections",
	Example: `  steam-organizer organize --config rules.example.yaml --dry-run
  steam-organizer organize --config rules.yaml --enrich-metadata`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfgPath := flagConfigFile
		if cfgPath == "" && len(args) > 0 {
			cfgPath = args[0]
		}
		if cfgPath == "" {
			cfgPath = "rules.yaml"
		}

		cfg, err := rules.LoadConfigFile(cfgPath)
		if err != nil {
			return err
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

func init() {
	organizeCmd.Flags().BoolVar(&flagOrgDryRun, "dry-run", false, "Preview rule matches and collection changes without saving")
	organizeCmd.Flags().BoolVarP(&flagOrgRefresh, "refresh", "r", false, "Force refresh of library and purchase history from Steam")
	organizeCmd.Flags().BoolVar(&flagOrgEnrich, "enrich-metadata", false, "Fetch missing store metadata from Steam Store")
	organizeCmd.Flags().IntVar(&flagOrgMaxFetch, "max-metadata-fetch", 80, "Maximum uncached apps to fetch store metadata for")
	organizeCmd.Flags().BoolVar(&flagOrgSyncCloud, "sync-cloud", false, "Sync collections with Steam Cloud Config Store")

	rootCmd.AddCommand(organizeCmd)
}

