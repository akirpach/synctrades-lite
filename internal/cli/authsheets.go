package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/akirpach/synctrades-lite/internal/sheets"
	"github.com/akirpach/synctrades-lite/internal/store"
)

func newAuthSheetsCmd() *cobra.Command {
	var keyPath, spreadsheetID, sheetName string

	cmd := &cobra.Command{
		Use:   "sheets",
		Short: "Point synctrades at your Google Sheet and confirm access",
		Long: `Validates your Google service account key and confirms it has Editor access
to the target spreadsheet, then creates the sync tab and its header row if
they don't already exist. Nothing is synced yet - this only sets up the
destination.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuthSheets(keyPath, spreadsheetID, sheetName)
		},
	}

	cmd.Flags().StringVar(&keyPath, "key-path", "", "path to your Google service account JSON key")
	cmd.Flags().StringVar(&spreadsheetID, "spreadsheet-id", "", "the spreadsheet ID, or its full URL")
	cmd.Flags().StringVar(&sheetName, "sheet-name", "", "the tab to sync into (default \""+sheets.DefaultSheetName+"\")")

	return cmd
}

func runAuthSheets(keyPath, spreadsheetID, sheetName string) error {
	s, err := store.Default()
	if err != nil {
		return fmt.Errorf("opening the local credential store: %w", err)
	}

	existing, err := s.Load()
	if err != nil && !errors.Is(err, store.ErrNotConfigured) {
		return err
	}

	cfg := sheets.Config{
		ServiceAccountKeyPath: firstNonEmpty(keyPath, existing.Sheets.ServiceAccountKeyPath),
		SpreadsheetID:         extractSpreadsheetID(firstNonEmpty(spreadsheetID, existing.Sheets.SpreadsheetID)),
		SheetName:             firstNonEmpty(sheetName, existing.Sheets.SheetName),
	}

	stdin := bufio.NewReader(os.Stdin)
	if cfg.ServiceAccountKeyPath == "" {
		v, err := prompt(stdin, "Path to your Google service account JSON key: ")
		if err != nil {
			return err
		}
		cfg.ServiceAccountKeyPath = v
	}
	if cfg.SpreadsheetID == "" {
		v, err := prompt(stdin, "Spreadsheet ID (or the full sheet URL): ")
		if err != nil {
			return err
		}
		cfg.SpreadsheetID = extractSpreadsheetID(v)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := sheets.NewClient(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connecting to Sheets: %w", err)
	}

	fmt.Printf("Checking access to %s (tab %q)...\n", client.URL(), client.SheetName())
	if err := client.EnsureSheet(ctx); err != nil {
		return err
	}
	if err := client.EnsureHeader(ctx); err != nil {
		return err
	}

	if err := s.Update(func(c *store.Credentials) error {
		c.Sheets.ServiceAccountKeyPath = cfg.ServiceAccountKeyPath
		c.Sheets.SpreadsheetID = client.SpreadsheetID()
		c.Sheets.SheetName = client.SheetName()
		return nil
	}); err != nil {
		return fmt.Errorf("saving configuration to %s: %w", s.Path(), err)
	}

	fmt.Println()
	fmt.Printf("Connected. Synced trades will go to %s (tab %q).\n", client.URL(), client.SheetName())
	return nil
}

// extractSpreadsheetID accepts either a bare ID or a full sheet URL, since
// pasting the whole URL where the ID belongs is the single most common setup
// mistake (see the ErrSpreadsheetNotFound guidance in internal/sheets).
func extractSpreadsheetID(raw string) string {
	raw = strings.TrimSpace(raw)
	const marker = "/d/"
	i := strings.Index(raw, marker)
	if i == -1 {
		return raw
	}
	rest := raw[i+len(marker):]
	if end := strings.IndexByte(rest, '/'); end != -1 {
		rest = rest[:end]
	}
	if end := strings.IndexByte(rest, '?'); end != -1 {
		rest = rest[:end]
	}
	return rest
}
