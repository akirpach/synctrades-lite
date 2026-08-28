package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/akirpach/synctrades-lite/internal/license"
	"github.com/akirpach/synctrades-lite/internal/store"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show what's configured and whether you're signed in",
		Long: `Reports where your credentials are stored, whether Schwab is signed in, and
which sheet is configured. This is a local, offline check: it does not call
Schwab or Google, so a token it reports as valid can still fail on the next
sync if it was revoked elsewhere.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus()
		},
	}
}

func runStatus() error {
	path, origin, err := store.ResolvePath()
	if err != nil {
		return fmt.Errorf("resolving the credential store location: %w", err)
	}
	fmt.Printf("Credential store: %s (%s)\n", path, origin)

	s, err := store.Default()
	if err != nil {
		return fmt.Errorf("opening the local credential store: %w", err)
	}

	creds, err := s.Load()
	if err != nil {
		if !errors.Is(err, store.ErrNotConfigured) {
			return err
		}
		fmt.Println()
		fmt.Println("Nothing configured yet. Run `synctrades auth schwab` and `synctrades auth sheets` to get started.")
		return nil
	}

	fmt.Println()
	printLicenseStatus(creds.License)
	fmt.Println()
	printSchwabStatus(creds.Schwab)
	fmt.Println()
	printSheetsStatus(creds.Sheets)
	return nil
}

func printLicenseStatus(l store.License) {
	if !l.HasToken() {
		fmt.Println("License: not activated. Run `synctrades license activate <key>`.")
		return
	}
	claims, err := license.Verify(l.Token)
	if err != nil {
		fmt.Printf("License: %v. Run `synctrades license activate <key>` with a current key.\n", err)
		return
	}
	fmt.Printf("License: active (%s).\n", claims.Email)
}

func printSchwabStatus(sc store.Schwab) {
	if sc.ClientID == "" {
		fmt.Println("Schwab: not configured. Run `synctrades auth schwab`.")
		return
	}
	if !sc.HasToken() {
		fmt.Println("Schwab: app registered, but not signed in. Run `synctrades auth schwab`.")
		return
	}

	tok := sc.Token()
	if tok.NeedsRefresh(time.Now()) {
		fmt.Printf("Schwab: signed in, access token expired %s. It will refresh automatically on the next sync.\n",
			tok.ExpiresAt.Local().Format(time.RFC3339))
		return
	}
	fmt.Printf("Schwab: signed in, access token valid until %s.\n", tok.ExpiresAt.Local().Format(time.RFC3339))
}

func printSheetsStatus(sh store.Sheets) {
	if sh.SpreadsheetID == "" {
		fmt.Println("Sheets: not configured. Run `synctrades auth sheets`.")
		return
	}
	fmt.Printf("Sheets: https://docs.google.com/spreadsheets/d/%s (tab %q)\n", sh.SpreadsheetID, sh.SheetName)
	fmt.Printf("  service account key: %s\n", sh.ServiceAccountKeyPath)
}
