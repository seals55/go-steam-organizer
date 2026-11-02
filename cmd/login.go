package cmd

import (
	"fmt"
	"time"

	"github.com/csull/go-steam-organizer/internal/auth"
	"github.com/spf13/cobra"
)

var (
	flagLoginForce  bool
	flagLoginManual bool
	flagLoginStatus bool
	flagLoginLogout bool
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Log in to Steam via your web browser or check authentication status",
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagLoginLogout {
			if err := auth.ClearSession(); err != nil {
				return err
			}
			fmt.Println("Cleared cached Steam session.")
			return nil
		}

		if flagLoginStatus {
			sess, err := auth.LoadSession()
			if err != nil || !sess.IsValid() {
				fmt.Println("Status: Not logged in (or session expired). Run 'steam-organizer login' to sign in.")
				return nil
			}
			fmt.Printf("Status:       Authenticated\n")
			fmt.Printf("Persona Name: %s\n", sess.PersonaName)
			fmt.Printf("SteamID64:    %s\n", sess.SteamID64)
			fmt.Printf("AccountID:    %d\n", sess.AccountID)
			fmt.Printf("Expires At:   %s (%s remaining)\n",
				sess.ExpiresAt.Format(time.RFC3339),
				time.Until(sess.ExpiresAt).Round(time.Minute))
			return nil
		}

		sess, err := auth.EnsureAuthenticated(cmd.Context(), auth.LoginOptions{
			Force:  flagLoginForce,
			Manual: flagLoginManual,
			APIKey: flagAPIKey,
		})
		if err != nil {
			return err
		}

		fmt.Printf("\nAuthenticated as %s (SteamID64: %s, AccountID: %d)\n",
			sess.PersonaName, sess.SteamID64, sess.AccountID)
		return nil
	},
}

func init() {
	loginCmd.Flags().BoolVarP(&flagLoginForce, "force", "f", false, "Force a fresh browser login even if a valid session exists")
	loginCmd.Flags().BoolVar(&flagLoginManual, "manual", false, "Use local browser callback page instead of automated Chromium window")
	loginCmd.Flags().BoolVar(&flagLoginStatus, "status", false, "Show current Steam login status without opening browser")
	loginCmd.Flags().BoolVar(&flagLoginLogout, "logout", false, "Clear saved Steam session credentials")
	rootCmd.AddCommand(loginCmd)
}

