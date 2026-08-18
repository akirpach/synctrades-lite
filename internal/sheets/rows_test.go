package sheets

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/akirpach/synctrades-lite/internal/schwab"
)

// optionTradeJSON is the same load-bearing fixture the schwab package tests use:
// a real option trade whose fee legs and instrument leg sum to netAmount
// (-81 + -0.65 + -0.01 = -81.66).
const optionTradeJSON = `{
  "activityId": 100209943191,
  "time": "2025-07-25T16:02:06+0000",
  "accountNumber": "15011495",
  "type": "TRADE",
  "status": "VALID",
  "subAccount": "CASH",
  "tradeDate": "2025-07-25T16:02:06+0000",
  "positionId": 2932154488,
  "orderId": 1003807445992,
  "netAmount": -81.66,
  "transferItems": [
    {"instrument": {"assetType": "CURRENCY", "symbol": "CURRENCY_USD"}, "amount": 0.65, "cost": -0.65, "feeType": "COMMISSION"},
    {"instrument": {"assetType": "CURRENCY", "symbol": "CURRENCY_USD"}, "amount": 0.01, "cost": -0.01, "feeType": "OPT_REG_FEE"},
    {
      "instrument": {
        "assetType": "OPTION",
        "symbol": "HIMS  250801P00046000",
        "description": "HIMS & HERS HEALTH INC 08/01/2025 $46 Put",
        "instrumentId": 235810637,
        "expirationDate": "2025-08-01T04:00:00+0000",
        "putCall": "PUT",
        "strikePrice": 46,
        "underlyingSymbol": "HIMS"
      },
      "amount": 1,
      "cost": -81,
      "price": 0.81,
      "positionEffect": "OPENING"
    }
  ]
}`

// equityTradeJSON has no option fields, so strike and expiration must come out
// blank rather than zero.
const equityTradeJSON = `{
  "activityId": 100209943192,
  "time": "2025-07-24T14:30:00+0000",
  "accountNumber": "15011495",
  "type": "TRADE",
  "status": "VALID",
  "tradeDate": "2025-07-24T14:30:00+0000",
  "netAmount": -1000.03,
  "transferItems": [
    {"instrument": {"assetType": "CURRENCY", "symbol": "CURRENCY_USD"}, "amount": 0.03, "cost": -0.03, "feeType": "SEC_FEE"},
    {
      "instrument": {"assetType": "EQUITY", "symbol": "AAPL", "description": "APPLE INC"},
      "amount": 10,
      "cost": -1000,
      "price": 100,
      "positionEffect": "OPENING"
    }
  ]
}`

func mustTransaction(t *testing.T, raw string) schwab.Transaction {
	t.Helper()
	var tx schwab.Transaction
	if err := json.Unmarshal([]byte(raw), &tx); err != nil {
		t.Fatalf("decoding fixture: %v", err)
	}
	return tx
}

func TestHeadersAndValuesStayInStep(t *testing.T) {
	row := Row{}
	if got, want := len(row.Values()), len(Headers); got != want {
		t.Fatalf("Values has %d entries, Headers has %d; they must match column for column", got, want)
	}
	if lastColumn != "S" {
		t.Errorf("lastColumn = %q, want S for %d headers", lastColumn, len(Headers))
	}
}

func TestBuildRowOptionTrade(t *testing.T) {
	row, err := BuildRow(mustTransaction(t, optionTradeJSON))
	if err != nil {
		t.Fatalf("BuildRow: %v", err)
	}

	if row.ActivityID != "100209943191" {
		t.Errorf("ActivityID = %q, want 100209943191", row.ActivityID)
	}
	if row.TradeDate != "2025-07-25" {
		t.Errorf("TradeDate = %q, want 2025-07-25", row.TradeDate)
	}
	if row.Symbol != "HIMS  250801P00046000" {
		t.Errorf("Symbol = %q", row.Symbol)
	}
	if row.PutCall != "PUT" || row.Underlying != "HIMS" {
		t.Errorf("PutCall/Underlying = %q/%q, want PUT/HIMS", row.PutCall, row.Underlying)
	}
	if row.Strike == nil || *row.Strike != 46 {
		t.Errorf("Strike = %v, want 46", row.Strike)
	}
	if row.Expiration != "2025-08-01" {
		t.Errorf("Expiration = %q, want 2025-08-01", row.Expiration)
	}
	if row.PositionEffect != "OPENING" {
		t.Errorf("PositionEffect = %q, want OPENING", row.PositionEffect)
	}
	if row.Quantity != 1 || row.Price != 0.81 || row.Amount != -81 {
		t.Errorf("Quantity/Price/Amount = %v/%v/%v, want 1/0.81/-81", row.Quantity, row.Price, row.Amount)
	}
	// Fees is negated so a charge reads positive: 0.65 + 0.01.
	if row.Fees < 0.6599 || row.Fees > 0.6601 {
		t.Errorf("Fees = %v, want 0.66", row.Fees)
	}
	if row.NetAmount != -81.66 {
		t.Errorf("NetAmount = %v, want -81.66", row.NetAmount)
	}
}

func TestBuildRowEquityLeavesOptionColumnsBlank(t *testing.T) {
	row, err := BuildRow(mustTransaction(t, equityTradeJSON))
	if err != nil {
		t.Fatalf("BuildRow: %v", err)
	}

	if row.Strike != nil {
		t.Errorf("Strike = %v, want nil for an equity", *row.Strike)
	}
	if row.Expiration != "" {
		t.Errorf("Expiration = %q, want empty for an equity", row.Expiration)
	}

	values := row.Values()
	strikeIdx, expiryIdx := headerIndex(t, "Strike"), headerIndex(t, "Expiration")
	if values[strikeIdx] != "" {
		t.Errorf("Strike cell = %#v, want an empty cell rather than a zero", values[strikeIdx])
	}
	if values[expiryIdx] != "" {
		t.Errorf("Expiration cell = %#v, want an empty cell", values[expiryIdx])
	}
}

func TestValuesAreOrderedLikeHeaders(t *testing.T) {
	row, err := BuildRow(mustTransaction(t, optionTradeJSON))
	if err != nil {
		t.Fatalf("BuildRow: %v", err)
	}

	values := row.Values()
	if got := values[headerIndex(t, "Activity ID")]; got != "100209943191" {
		t.Errorf("Activity ID column holds %#v", got)
	}
	if got := values[headerIndex(t, "Net Amount")]; got != -81.66 {
		t.Errorf("Net Amount column holds %#v", got)
	}
	// The key must be text, not a number, or Sheets can hand it back in a
	// different representation and dedup stops matching.
	if _, ok := values[headerIndex(t, "Activity ID")].(string); !ok {
		t.Errorf("Activity ID must be written as a string, got %T", values[0])
	}
	if _, ok := values[headerIndex(t, "Price")].(float64); !ok {
		t.Errorf("Price must be written as a number, got %T", values[headerIndex(t, "Price")])
	}
}

func headerIndex(t *testing.T, name string) int {
	t.Helper()
	for i, h := range Headers {
		if h == name {
			return i
		}
	}
	t.Fatalf("no %q column", name)
	return -1
}

func TestBuildRowRejectsMultipleInstrumentLegs(t *testing.T) {
	tx := mustTransaction(t, optionTradeJSON)
	extra := tx.TransferItems[len(tx.TransferItems)-1]
	tx.TransferItems = append(tx.TransferItems, extra)

	if _, err := BuildRow(tx); !errors.Is(err, schwab.ErrMultipleInstrumentLegs) {
		t.Fatalf("err = %v, want ErrMultipleInstrumentLegs", err)
	}
}

func TestBuildRowRejectsUnreconciledTransaction(t *testing.T) {
	tx := mustTransaction(t, optionTradeJSON)
	tx.NetAmount = -99.99

	_, err := BuildRow(tx)
	if !errors.Is(err, ErrNetAmountMismatch) {
		t.Fatalf("err = %v, want ErrNetAmountMismatch", err)
	}
	if !strings.Contains(err.Error(), "100209943191") {
		t.Errorf("error should name the activityId, got %v", err)
	}
}

func TestBuildRowsIsAllOrNothing(t *testing.T) {
	good := mustTransaction(t, optionTradeJSON)
	bad := mustTransaction(t, equityTradeJSON)
	bad.NetAmount = 12345

	rows, err := BuildRows([]schwab.Transaction{good, bad})
	if err == nil {
		t.Fatal("expected an error when one transaction cannot be mapped")
	}
	if rows != nil {
		t.Errorf("rows = %v, want nil so nothing partial is written", rows)
	}
	if !strings.Contains(err.Error(), "1 of 2") {
		t.Errorf("error should count the failures, got %v", err)
	}
}

func TestBuildRowsSucceedsForEveryTransaction(t *testing.T) {
	txs := []schwab.Transaction{
		mustTransaction(t, optionTradeJSON),
		mustTransaction(t, equityTradeJSON),
	}
	rows, err := BuildRows(txs)
	if err != nil {
		t.Fatalf("BuildRows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
}

func TestColumnLetter(t *testing.T) {
	cases := map[int]string{1: "A", 19: "S", 26: "Z", 27: "AA", 52: "AZ", 53: "BA", 0: ""}
	for n, want := range cases {
		if got := columnLetter(n); got != want {
			t.Errorf("columnLetter(%d) = %q, want %q", n, got, want)
		}
	}
}
