package sheets

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/akirpach/synctrades-lite/internal/schwab"
)

func TestNormalizeActivityID(t *testing.T) {
	cases := []struct {
		name string
		cell any
		want string
	}{
		{"text as written", "100209943191", "100209943191"},
		{"text with stray spaces", "  100209943191 ", "100209943191"},
		{"numeric cell", float64(100209943191), "100209943191"},
		{"numeric cell with a decimal tail", 100209943191.5, "100209943191.5"},
		{"json number", json.Number("100209943191"), "100209943191"},
		{"int64", int64(100209943191), "100209943191"},
		{"blank", "", ""},
		{"nil", nil, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeActivityID(tc.cell); got != tc.want {
				t.Errorf("normalizeActivityID(%#v) = %q, want %q", tc.cell, got, tc.want)
			}
		})
	}
}

// A numeric activity ID must round-trip to the same string ActivityID writes,
// or a sheet whose key column ended up numeric silently duplicates every row.
func TestNumericCellsMatchWrittenKeys(t *testing.T) {
	tx := mustTransaction(t, optionTradeJSON)
	written := ActivityID(tx)
	readBack := normalizeActivityID(float64(tx.ActivityID))
	if readBack != written {
		t.Fatalf("numeric cell normalizes to %q but we write %q", readBack, written)
	}
}

func transactionAt(id int64, ts string) schwab.Transaction {
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		panic(err)
	}
	return schwab.Transaction{ActivityID: id, Time: schwab.SchwabTime{Time: parsed}}
}

func TestDiffTransactionsSkipsWhatIsAlreadySynced(t *testing.T) {
	existing := map[string]struct{}{"2": {}}
	txs := []schwab.Transaction{
		transactionAt(3, "2025-07-03T10:00:00Z"),
		transactionAt(2, "2025-07-02T10:00:00Z"),
		transactionAt(1, "2025-07-01T10:00:00Z"),
	}

	diff := DiffTransactions(existing, txs)

	if diff.AlreadySynced != 1 {
		t.Errorf("AlreadySynced = %d, want 1", diff.AlreadySynced)
	}
	if len(diff.New) != 2 {
		t.Fatalf("New has %d entries, want 2", len(diff.New))
	}
	// Schwab returns newest first; the sheet should read oldest first.
	if diff.New[0].ActivityID != 1 || diff.New[1].ActivityID != 3 {
		t.Errorf("New order = %d, %d; want 1, 3 (oldest first)",
			diff.New[0].ActivityID, diff.New[1].ActivityID)
	}
}

func TestDiffTransactionsCollapsesRepeats(t *testing.T) {
	txs := []schwab.Transaction{
		transactionAt(7, "2025-07-01T10:00:00Z"),
		transactionAt(7, "2025-07-01T10:00:00Z"),
	}

	diff := DiffTransactions(nil, txs)

	if len(diff.New) != 1 {
		t.Errorf("New has %d entries, want 1", len(diff.New))
	}
	if diff.Repeated != 1 {
		t.Errorf("Repeated = %d, want 1", diff.Repeated)
	}
}

func TestDiffTransactionsOrdersTiesByID(t *testing.T) {
	txs := []schwab.Transaction{
		transactionAt(9, "2025-07-01T10:00:00Z"),
		transactionAt(8, "2025-07-01T10:00:00Z"),
	}

	diff := DiffTransactions(nil, txs)

	if diff.New[0].ActivityID != 8 || diff.New[1].ActivityID != 9 {
		t.Errorf("tie order = %d, %d; want 8, 9", diff.New[0].ActivityID, diff.New[1].ActivityID)
	}
}

func TestExistingActivityIDsReadsOneColumn(t *testing.T) {
	var gotPath, gotRender, gotDimension string

	client := newTestClient(t, "Trades", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRender = r.URL.Query().Get("valueRenderOption")
		gotDimension = r.URL.Query().Get("majorDimension")
		writeJSON(w, map[string]any{
			"majorDimension": "COLUMNS",
			"values":         [][]any{{"100209943191", float64(100209943192), "", "  100209943193 "}},
		})
	})

	ids, err := client.ExistingActivityIDs(context.Background())
	if err != nil {
		t.Fatalf("ExistingActivityIDs: %v", err)
	}

	for _, want := range []string{"100209943191", "100209943192", "100209943193"} {
		if _, ok := ids[want]; !ok {
			t.Errorf("missing %s from %v", want, ids)
		}
	}
	if len(ids) != 3 {
		t.Errorf("got %d ids, want 3 (blanks dropped): %v", len(ids), ids)
	}
	if gotRender != unformattedValues {
		t.Errorf("valueRenderOption = %q, want %q", gotRender, unformattedValues)
	}
	if gotDimension != "COLUMNS" {
		t.Errorf("majorDimension = %q, want COLUMNS", gotDimension)
	}
	// Only the key column, and only from the first data row down.
	if want := "/v4/spreadsheets/sheet-id/values/'Trades'!A2:A"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
}

func TestExistingActivityIDsOnEmptySheet(t *testing.T) {
	client := newTestClient(t, "Trades", func(w http.ResponseWriter, r *http.Request) {
		// Sheets omits "values" entirely for an empty range.
		writeJSON(w, map[string]any{"majorDimension": "COLUMNS"})
	})

	ids, err := client.ExistingActivityIDs(context.Background())
	if err != nil {
		t.Fatalf("ExistingActivityIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("got %v, want an empty set", ids)
	}
}
