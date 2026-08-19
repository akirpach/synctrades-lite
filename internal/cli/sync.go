package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/akirpach/synctrades-lite/internal/schwab"
	"github.com/akirpach/synctrades-lite/internal/sheets"
	"github.com/akirpach/synctrades-lite/internal/store"
)

// defaultSinceDays is the first-sync lookback per CLAUDE.md: one year covers a
// full tax year, which is what later reporting templates will want.
const defaultSinceDays = 365

func newSyncCmd() *cobra.Command {
	var sinceDays int
	var account string

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Fetch new Schwab trades and append them to your Google Sheet",
		Long: `Refreshes your Schwab access token if needed, fetches trade activity for the
requested window, and appends only the transactions your sheet doesn't
already have. Existing rows are never rewritten.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSync(sinceDays, account)
		},
	}

	cmd.Flags().IntVar(&sinceDays, "since", defaultSinceDays, "how many days of history to fetch")
	cmd.Flags().StringVar(&account, "account", "", "which Schwab account to sync, if your login has more than one")

	return cmd
}

func runSync(sinceDays int, account string) error {
	if sinceDays <= 0 {
		return fmt.Errorf("--since must be a positive number of days, got %d", sinceDays)
	}

	s, err := store.Default()
	if err != nil {
		return fmt.Errorf("opening the local credential store: %w", err)
	}

	creds, err := s.Load()
	if errors.Is(err, store.ErrNotConfigured) {
		return errors.New("nothing configured yet; run `synctrades auth schwab` and `synctrades auth sheets` first")
	}
	if err != nil {
		return err
	}
	if !creds.Schwab.HasToken() {
		return errors.New("not signed in to Schwab; run `synctrades auth schwab`")
	}
	if creds.Sheets.SpreadsheetID == "" {
		return errors.New("no sheet configured; run `synctrades auth sheets`")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	schwabClient := schwab.NewClient(store.NewTokenSource(s))

	fmt.Println("Resolving Schwab account...")
	hash, err := schwabClient.AccountHash(ctx, account)
	if err != nil {
		return err
	}

	end := time.Now()
	start := end.AddDate(0, 0, -sinceDays)
	fmt.Printf("Fetching trades from %s to %s...\n", start.Format("2006-01-02"), end.Format("2006-01-02"))
	txs, err := schwabClient.Transactions(ctx, hash, start, end, schwab.TypeTrade)
	if err != nil {
		return fmt.Errorf("fetching transactions: %w", err)
	}
	fmt.Printf("Fetched %d transaction(s).\n", len(txs))

	sheetsClient, err := sheets.NewClient(ctx, sheets.Config{
		ServiceAccountKeyPath: creds.Sheets.ServiceAccountKeyPath,
		SpreadsheetID:         creds.Sheets.SpreadsheetID,
		SheetName:             creds.Sheets.SheetName,
	})
	if err != nil {
		return fmt.Errorf("connecting to Sheets: %w", err)
	}

	if err := sheetsClient.EnsureSheet(ctx); err != nil {
		return err
	}
	if err := sheetsClient.EnsureHeader(ctx); err != nil {
		return err
	}

	existing, err := sheetsClient.ExistingActivityIDs(ctx)
	if err != nil {
		return fmt.Errorf("reading existing activity IDs: %w", err)
	}

	diff := sheets.DiffTransactions(existing, txs)
	if diff.Repeated > 0 {
		fmt.Printf("warning: Schwab returned %d duplicate transaction(s) in this fetch; only one copy of each was kept\n", diff.Repeated)
	}

	rows, err := sheets.BuildRows(diff.New)
	if err != nil {
		return fmt.Errorf("mapping transactions to rows: %w", err)
	}

	appended, err := sheetsClient.Append(ctx, rows)
	if err != nil {
		return fmt.Errorf("appending rows: %w", err)
	}

	fmt.Println()
	fmt.Printf("Synced %d new trade(s), %d already up to date.\n", appended, diff.AlreadySynced)
	fmt.Printf("%s (tab %q)\n", sheetsClient.URL(), sheetsClient.SheetName())
	return nil
}
