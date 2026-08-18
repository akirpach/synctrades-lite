package sheets

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/akirpach/synctrades-lite/internal/schwab"
)

// TestE2ESheetSync walks the real Sheets API: create the tab, write the header,
// append rows, and confirm a second pass appends nothing.
//
// CLAUDE.md requires dedup to be verified against a real sheet with pre-existing
// rows, and a stub cannot do that. The failure this guards against is not an API
// error, it is a key that round-trips through Google's storage as something other
// than what we wrote, which produces duplicate rows in a tax record with nothing
// in the logs to show for it.
//
// Guarded so it compiles and vets on every ordinary `go test ./...` but only runs
// on request:
//
//	SYNCTRADES_SHEETS_E2E=1
//	SHEETS_KEY_PATH=/path/to/service-account.json
//	SHEETS_SPREADSHEET_ID=<the part of the URL between /d/ and /edit>
//	SHEETS_TAB=<optional; defaults to a test-only tab>
//
// It writes real rows and does not clean up after itself, which is why it
// defaults to its own tab rather than the tab a user actually syncs into.
func TestE2ESheetSync(t *testing.T) {
	if os.Getenv("SYNCTRADES_SHEETS_E2E") != "1" {
		t.Skip("set SYNCTRADES_SHEETS_E2E=1 to run the live Sheets walk")
	}

	keyPath := requireEnv(t, "SHEETS_KEY_PATH")
	spreadsheetID := requireEnv(t, "SHEETS_SPREADSHEET_ID")

	tab := os.Getenv("SHEETS_TAB")
	if tab == "" {
		// Deliberately not DefaultSheetName: this test leaves rows behind.
		tab = "Synctrades E2E"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client, err := NewClient(ctx, Config{
		ServiceAccountKeyPath: keyPath,
		SpreadsheetID:         spreadsheetID,
		SheetName:             tab,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Logf("target: %s (tab %q)", client.URL(), client.SheetName())

	if err := client.EnsureSheet(ctx); err != nil {
		t.Fatalf("EnsureSheet: %v", err)
	}
	if err := client.EnsureHeader(ctx); err != nil {
		t.Fatalf("EnsureHeader: %v", err)
	}

	// Running twice must be safe, so the header path is exercised against a
	// sheet that already has one.
	if err := client.EnsureHeader(ctx); err != nil {
		t.Fatalf("EnsureHeader is not idempotent: %v", err)
	}

	before, err := client.ExistingActivityIDs(ctx)
	if err != nil {
		t.Fatalf("ExistingActivityIDs: %v", err)
	}
	t.Logf("sheet already holds %d activity IDs", len(before))

	// A unique ID per run, in the same 12-digit shape as Schwab's, so repeated
	// runs accumulate rather than collide.
	txs := liveFixture(t, time.Now().UnixNano()%1_000_000_000_000)
	id := ActivityID(txs[0])
	t.Logf("appending activityId %s", id)

	first := DiffTransactions(before, txs)
	if len(first.New) != 1 {
		t.Fatalf("first diff produced %d new rows, want 1", len(first.New))
	}

	rows, err := BuildRows(first.New)
	if err != nil {
		t.Fatalf("BuildRows: %v", err)
	}
	if _, err := client.Append(ctx, rows); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// The real assertion: read the key back out of Google's storage and confirm
	// it still matches the string we wrote.
	after, err := client.ExistingActivityIDs(ctx)
	if err != nil {
		t.Fatalf("ExistingActivityIDs after append: %v", err)
	}
	if _, ok := after[id]; !ok {
		t.Fatalf("activityId %s is not in the sheet after appending it; the key did not survive the round trip (read back %d ids)",
			id, len(after))
	}
	if len(after) != len(before)+1 {
		t.Errorf("sheet went from %d to %d ids, want exactly one more", len(before), len(after))
	}

	// Re-syncing the same fetch must be a no-op. This is the append-only dedup
	// contract, verified against a sheet that now has pre-existing rows.
	second := DiffTransactions(after, txs)
	if len(second.New) != 0 {
		t.Fatalf("re-syncing produced %d new rows; dedup did not recognize the row it just wrote", len(second.New))
	}
	if second.AlreadySynced != 1 {
		t.Errorf("AlreadySynced = %d, want 1", second.AlreadySynced)
	}

	t.Logf("dedup held: %d rows in the sheet, re-sync appended nothing", len(after))
	t.Logf("this run left activityId %s behind in %q; delete it if you are done", id, tab)
}

// liveFixture is the option-trade fixture with a caller-supplied activity ID.
func liveFixture(t *testing.T, activityID int64) []schwab.Transaction {
	t.Helper()
	tx := mustTransaction(t, optionTradeJSON)
	tx.ActivityID = activityID
	return []schwab.Transaction{tx}
}

func requireEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s must be set to run the live Sheets walk", name)
	}
	return v
}
