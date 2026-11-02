package cmd

import (
	"fmt"
	"strings"

	"github.com/csull/go-steam-organizer/internal/rules"
	"github.com/csull/go-steam-organizer/internal/steam"
	"github.com/spf13/cobra"
)

var (
	flagPurYears       []int
	flagPurAfter       string
	flagPurBefore      string
	flagPurTypes       []string
	flagPurMinPrice    float64
	flagPurMaxPrice    float64
	flagPurFree        bool
	flagPurRefunded    bool
	flagPurBundle      bool
	flagPurGenres      []string
	flagPurTags        []string
	flagPurCategories  []string
	flagPurDevelopers  []string
	flagPurPublishers  []string
	flagPurDeck        []string
	flagPurUnplayed    bool
	flagPurMinHours    float64
	flagPurMaxHours    float64
	flagPurTitle       string
	flagPurRefresh     bool
	flagPurEnrich      bool
	flagPurMaxFetch    int
	flagPurAddToCol    string
	flagPurDryRun      bool
	flagPurSyncCloud   bool
	flagPurRawTxnsOnly bool
)

var purchasesCmd = &cobra.Command{
	Use:   "purchases",
	Short: "Query Steam purchases & library items by criteria and optionally add matches to a collection",
	Example: `  # List all games purchased in 2025
  steam-organizer purchases --year 2025

  # Find unplayed RPGs bought for under $10 and add them to "Backlog RPGs"
  steam-organizer purchases --genre RPG --unplayed --max-price 10 --enrich-metadata --add-to "Backlog RPGs"

  # Group all purchases by purchase year using a template collection name
  steam-organizer purchases --type purchase --add-to "Bought in {{.PurchaseYear}}"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		sess, err := requireSession(cmd.Context())
		if err != nil {
			return err
		}

		client := steam.NewClient(sess)
		cache, err := client.SyncFullLibrary(cmd.Context(), flagPurRefresh, 25, func(msg string) {
			fmt.Println(msg)
		})
		if err != nil {
			return err
		}

		if flagPurRawTxnsOnly {
			fmt.Printf("Total Raw Account Purchase Transactions: %d\n\n", len(cache.Purchases))
			fmt.Printf("%-12s %-14s %-10s %-10s %s\n", "DATE", "TYPE", "PRICE", "REFUNDED", "ITEMS")
			fmt.Println(strings.Repeat("-", 90))
			for _, p := range cache.Purchases {
				d := "-"
				if !p.Date.IsZero() {
					d = p.Date.Format("2006-01-02")
				}
				fmt.Printf("%-12s %-14s %-10s %-10v %s\n",
					d, p.AcquisitionType, p.PriceDisplay, p.IsRefunded, strings.Join(p.Items, "; "))
			}
			return nil
		}

		crit := buildCriteriaFromFlags(cmd)
		items := client.EnrichItems(cmd.Context(), cache.Items, flagPurEnrich || crit.NeedsStoreMetadata(), flagPurMaxFetch, func(msg string) {
			fmt.Println(msg)
		})
		cache.Items = items
		_ = steam.SaveLibraryCache(cache)

		matched, err := rules.FilterItems(items, crit)
		if err != nil {
			return err
		}

		fmt.Printf("\nMatched %d of %d library items:\n\n", len(matched), len(items))
		if len(matched) == 0 {
			return nil
		}

		fmt.Printf("%-8s %-36s %-11s %-11s %-9s %-8s %-9s %s\n",
			"APPID", "TITLE", "PURCHASED", "TYPE", "PRICE", "HOURS", "DECK", "GENRES")
		fmt.Println(strings.Repeat("-", 110))
		for _, it := range matched {
			d := "-"
			if !it.PurchaseDate.IsZero() {
				d = it.PurchaseDate.Format("2006-01-02")
			}
			deck := string(it.DeckCompatibility)
			if deck == "" {
				deck = "-"
			}
			fmt.Printf("%-8d %-36s %-11s %-11s %-9s %-8.1f %-9s %s\n",
				it.AppID,
				truncateCLI(it.Name, 35),
				d,
				it.AcquisitionType,
				it.SummaryPrice(),
				it.PlaytimeHours(),
				deck,
				truncateCLI(strings.Join(it.Genres, ", "), 25),
			)
		}

		if strings.TrimSpace(flagPurAddToCol) == "" {
			return nil
		}

		// Organize matched items into target collection(s)
		cfg := &rules.ConfigFile{
			Rules: []rules.Rule{
				{
					Name:       "CLI Purchases Filter",
					Collection: flagPurAddToCol,
					Criteria:   crit,
				},
			},
		}
		evalResults, err := rules.EvaluateRules(matched, cfg)
		if err != nil {
			return err
		}

		mgr, err := loadCollectionManager(sess)
		if err != nil {
			return err
		}
		if flagPurSyncCloud {
			_ = mgr.SyncWithCloud(cmd.Context(), client)
		}

		fmt.Println()
		for _, res := range evalResults {
			for colName, colItems := range res.CollectionMatches {
				appIDs := make([]uint32, len(colItems))
				for i, it := range colItems {
					appIDs[i] = it.AppID
				}
				added, col := mgr.AddAppsToCollection(colName, appIDs)
				fmt.Printf("Collection %q (%s): +%d new app(s) added (%d total apps).\n",
					col.Name, col.ID, added, len(col.Added))
			}
		}

		if flagPurDryRun {
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

func buildCriteriaFromFlags(cmd *cobra.Command) rules.Criteria {
	var c rules.Criteria
	c.PurchaseYears = flagPurYears
	c.PurchasedAfter = flagPurAfter
	c.PurchasedBefore = flagPurBefore
	c.AcquisitionTypes = flagPurTypes
	if cmd.Flags().Changed("min-price") {
		v := flagPurMinPrice
		c.MinPrice = &v
	}
	if cmd.Flags().Changed("max-price") {
		v := flagPurMaxPrice
		c.MaxPrice = &v
	}
	if cmd.Flags().Changed("free") {
		v := flagPurFree
		c.IsFree = &v
	}
	if cmd.Flags().Changed("refunded") {
		v := flagPurRefunded
		c.IsRefunded = &v
	}
	if cmd.Flags().Changed("bundle") {
		v := flagPurBundle
		c.IsBundle = &v
	}
	c.Genres = flagPurGenres
	c.Tags = flagPurTags
	c.Categories = flagPurCategories
	c.Developers = flagPurDevelopers
	c.Publishers = flagPurPublishers
	c.DeckCompatibility = flagPurDeck
	if cmd.Flags().Changed("unplayed") {
		v := flagPurUnplayed
		c.Unplayed = &v
	}
	if cmd.Flags().Changed("min-hours") {
		v := flagPurMinHours
		c.MinPlaytimeHours = &v
	}
	if cmd.Flags().Changed("max-hours") {
		v := flagPurMaxHours
		c.MaxPlaytimeHours = &v
	}
	c.NameContains = flagPurTitle
	return c
}

func truncateCLI(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func init() {
	// 1. Purchase date / year flags
	purchasesCmd.Flags().IntSliceVarP(&flagPurYears, "year", "y", nil, "Filter by purchase year(s) (e.g. --year 2025,2026)")
	purchasesCmd.Flags().StringVar(&flagPurAfter, "after", "", "Filter items purchased on or after YYYY-MM-DD")
	purchasesCmd.Flags().StringVar(&flagPurBefore, "before", "", "Filter items purchased on or before YYYY-MM-DD")

	// 2. Price / acquisition type flags
	purchasesCmd.Flags().StringSliceVar(&flagPurTypes, "type", nil, "Filter by acquisition type: purchase, free, retail_key, gift, bundle, refunded")
	purchasesCmd.Flags().Float64Var(&flagPurMinPrice, "min-price", 0, "Minimum purchase price in dollars")
	purchasesCmd.Flags().Float64Var(&flagPurMaxPrice, "max-price", 0, "Maximum purchase price in dollars")
	purchasesCmd.Flags().BoolVar(&flagPurFree, "free", false, "Filter for free / complimentary acquisitions")
	purchasesCmd.Flags().BoolVar(&flagPurRefunded, "refunded", false, "Filter by refunded status")
	purchasesCmd.Flags().BoolVar(&flagPurBundle, "bundle", false, "Filter by bundle status")

	// 3. Store metadata flags
	purchasesCmd.Flags().StringSliceVar(&flagPurGenres, "genre", nil, "Filter by store genre(s) (e.g. RPG, Action, Indie)")
	purchasesCmd.Flags().StringSliceVar(&flagPurTags, "tag", nil, "Filter by store user tag(s) (e.g. Roguelike, Co-op)")
	purchasesCmd.Flags().StringSliceVar(&flagPurCategories, "category", nil, "Filter by store category (e.g. 'Single-player', 'Controller')")
	purchasesCmd.Flags().StringSliceVar(&flagPurDevelopers, "developer", nil, "Filter by developer name")
	purchasesCmd.Flags().StringSliceVar(&flagPurPublishers, "publisher", nil, "Filter by publisher name")
	purchasesCmd.Flags().StringSliceVar(&flagPurDeck, "deck", nil, "Filter by Steam Deck status: verified, playable, unsupported, unknown")

	// 4. Playtime / played status flags
	purchasesCmd.Flags().BoolVar(&flagPurUnplayed, "unplayed", false, "Filter for unplayed games (0 minutes played)")
	purchasesCmd.Flags().Float64Var(&flagPurMinHours, "min-hours", 0, "Minimum playtime in hours")
	purchasesCmd.Flags().Float64Var(&flagPurMaxHours, "max-hours", 0, "Maximum playtime in hours (e.g. 2.0 for <2 hours)")

	// General & action flags
	purchasesCmd.Flags().StringVar(&flagPurTitle, "title", "", "Filter by substring in game title")
	purchasesCmd.Flags().BoolVarP(&flagPurRefresh, "refresh", "r", false, "Force refresh of library, purchase history, and licenses from Steam")
	purchasesCmd.Flags().BoolVar(&flagPurEnrich, "enrich-metadata", false, "Fetch missing store metadata (genres, tags, Deck status) from Steam Store")
	purchasesCmd.Flags().IntVar(&flagPurMaxFetch, "max-metadata-fetch", 60, "Maximum uncached apps to query for store metadata in one run")
	purchasesCmd.Flags().StringVar(&flagPurAddToCol, "add-to", "", "Add matching items to the specified collection name (supports {{.PurchaseYear}})")
	purchasesCmd.Flags().BoolVar(&flagPurDryRun, "dry-run", false, "Preview collection assignments without saving to disk")
	purchasesCmd.Flags().BoolVar(&flagPurSyncCloud, "sync-cloud", false, "Sync collections with Steam Cloud Config Store")
	purchasesCmd.Flags().BoolVar(&flagPurRawTxnsOnly, "raw-transactions", false, "Display raw account purchase transaction rows")

	rootCmd.AddCommand(purchasesCmd)
}

