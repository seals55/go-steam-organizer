package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/csull/go-steam-organizer/internal/auth"
	"github.com/csull/go-steam-organizer/internal/collections"
	"github.com/csull/go-steam-organizer/internal/steam"
	"github.com/spf13/cobra"
)

var (
	flagSteamPath        string
	flagCloudStorageFile string
	flagAPIKey           string
	flagLaunchTUI        bool
	flagConfigFile       string
)

var rootCmd = &cobra.Command{
	Use:   "steam-organizer",
	Short: "Organize your Steam Library collections and query purchases by criteria",
	Long: `go-steam-organizer is a CLI and interactive TUI utility that authenticates with Steam
via your web browser, queries your purchase history, licenses, playtime, and store metadata,
and organizes library items into Steam collections.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagLaunchTUI {
			return runInteractiveTUI(cmd.Context())
		}
		return cmd.Help()
	},
}

// Execute runs the root CLI command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&flagSteamPath, "steam-path", "", "Path to local Steam installation directory (auto-detected if omitted)")
	rootCmd.PersistentFlags().StringVar(&flagCloudStorageFile, "cloudstorage-file", "", "Explicit path to cloud-storage-namespace-1.json")
	rootCmd.PersistentFlags().StringVar(&flagAPIKey, "api-key", "", "Optional Steam Web API key (can also be set via STEAM_API_KEY)")
	rootCmd.PersistentFlags().BoolVar(&flagLaunchTUI, "tui", false, "Launch the interactive Terminal UI (TUI)")
	rootCmd.PersistentFlags().StringVarP(&flagConfigFile, "config", "c", "", "Path to YAML/JSON organization rules file")
}

// requireSession ensures the user is authenticated with Steam, prompting for browser login if necessary.
func requireSession(ctx context.Context) (*auth.Session, error) {
	return auth.EnsureAuthenticated(ctx, auth.LoginOptions{
		APIKey: flagAPIKey,
	})
}

// requireSessionOrCached returns a valid session, or an existing cached session if forceRefresh is false
// and a complete local library cache already exists.
func requireSessionOrCached(ctx context.Context, forceRefresh bool) (*auth.Session, error) {
	if !forceRefresh {
		if sess, err := auth.LoadSession(); err == nil && sess != nil && sess.SteamID64 != "" {
			if sess.IsValid() {
				return sess, nil
			}
			if cached, err := steam.LoadLibraryCache(sess.SteamID64); err == nil && len(cached.Items) > 0 && (len(cached.Purchases) > 0 || len(cached.Licenses) > 0) {
				return sess, nil
			}
		}
		if cached, err := steam.LoadLibraryCache(""); err == nil && len(cached.Items) > 0 && cached.SteamID64 != "" && (len(cached.Purchases) > 0 || len(cached.Licenses) > 0) {
			acctID, _ := auth.SteamID64ToAccountID(cached.SteamID64)
			return &auth.Session{
				SteamID64:   cached.SteamID64,
				AccountID:   acctID,
				PersonaName: "Cached Session",
			}, nil
		}
	}
	return requireSession(ctx)
}

// loadCollectionManager locates and loads the user's cloud-storage-namespace-1.json.
func loadCollectionManager(sess *auth.Session) (*collections.Manager, error) {
	var accountID uint32
	if sess != nil {
		accountID = sess.AccountID
	} else if cached, err := auth.LoadSession(); err == nil {
		accountID = cached.AccountID
	}

	path, err := collections.LocateCloudStorageFile(flagCloudStorageFile, flagSteamPath, accountID)
	if err != nil {
		return nil, err
	}
	return collections.LoadManager(path)
}

