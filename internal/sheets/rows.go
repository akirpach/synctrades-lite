// Package sheets writes synced Schwab trades into the user's own Google Sheet.
//
// The sheet is the product's only output. Everything here is built around two
// rules from CLAUDE.md: one row per transaction, and Schwab's activityId is the
// dedup key. Both are load-bearing, so this package refuses to write anything it
// cannot map onto them rather than guessing.
package sheets

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/akirpach/synctrades-lite/internal/schwab"
)

// Headers are the sheet's column titles, in order.
//
// The activity ID comes first so dedup can read one narrow column instead of the
// whole table. Columns are grouped identity, timing, instrument, option detail,
// economics; new columns for later transaction types belong on the right so an
// existing sheet keeps working.
var Headers = []string{
	"Activity ID",
	"Trade Date",
	"Time",
	"Type",
	"Status",
	"Account",
	"Symbol",
	"Description",
	"Asset Type",
	"Put/Call",
	"Underlying",
	"Strike",
	"Expiration",
	"Position Effect",
	"Quantity",
	"Price",
	"Amount",
	"Fees",
	"Net Amount",
}

// activityIDColumn is where Headers puts the dedup key.
const activityIDColumn = "A"

// lastColumn is the rightmost column Headers occupies, used to bound reads.
var lastColumn = columnLetter(len(Headers))

// headerRow is the first row; data starts at firstDataRow.
const (
	headerRow    = 1
	firstDataRow = 2
)

// dateLayout is how dates are written. Values go in with ValueInputOption RAW,
// so these land as text rather than date-typed cells; ISO 8601 keeps them
// sorting correctly and unambiguous across locales. See client.go for why RAW.
const dateLayout = "2006-01-02"

// ErrNetAmountMismatch means the instrument leg plus the fee legs did not add up
// to Schwab's own netAmount, so the fee-folding rule has met a transaction shape
// it does not understand. Writing the row anyway would put a quietly wrong
// number in someone's tax records.
var ErrNetAmountMismatch = errors.New("transaction does not reconcile against netAmount")

// Row is one transaction as it appears in the sheet. Field order matches Headers
// and Values depends on that, so the two move together.
type Row struct {
	ActivityID     string
	TradeDate      string
	Time           string
	Type           string
	Status         string
	Account        string
	Symbol         string
	Description    string
	AssetType      string
	PutCall        string
	Underlying     string
	Strike         *float64
	Expiration     string
	PositionEffect string
	Quantity       float64
	Price          float64
	Amount         float64
	Fees           float64
	NetAmount      float64
}

// Values renders the row for the Sheets API, in Headers order.
//
// Numbers are emitted as float64 so they arrive as numeric cells; a nil Strike
// becomes a blank cell rather than a misleading 0.
func (r Row) Values() []any {
	return []any{
		r.ActivityID,
		r.TradeDate,
		r.Time,
		r.Type,
		r.Status,
		r.Account,
		r.Symbol,
		r.Description,
		r.AssetType,
		r.PutCall,
		r.Underlying,
		optionalNumber(r.Strike),
		r.Expiration,
		r.PositionEffect,
		r.Quantity,
		r.Price,
		r.Amount,
		r.Fees,
		r.NetAmount,
	}
}

func optionalNumber(f *float64) any {
	if f == nil {
		return ""
	}
	return *f
}

// BuildRow maps one transaction onto a sheet row.
//
// It fails rather than improvising on the two shapes the one-row model cannot
// express: a transaction with more than one instrument leg, and one whose legs
// do not reconcile against netAmount.
func BuildRow(t schwab.Transaction) (Row, error) {
	leg, err := t.InstrumentLeg()
	if err != nil {
		return Row{}, err
	}

	fees := t.Fees()
	if !t.NetAmountMatches() {
		return Row{}, fmt.Errorf(
			"%w (activityId %d): instrument leg cost %.2f minus fees %.2f is not netAmount %.2f",
			ErrNetAmountMismatch, t.ActivityID, leg.Cost, fees, t.NetAmount)
	}

	row := Row{
		ActivityID:     ActivityID(t),
		TradeDate:      formatDate(t.TradeDate.Time),
		Time:           formatTimestamp(t.Time.Time),
		Type:           t.Type,
		Status:         t.Status,
		Account:        t.AccountNumber,
		Symbol:         leg.Instrument.Symbol,
		Description:    leg.Instrument.Description,
		AssetType:      leg.Instrument.AssetType,
		PutCall:        leg.Instrument.PutCall,
		Underlying:     leg.Instrument.UnderlyingSymbol,
		PositionEffect: leg.PositionEffect,
		Quantity:       leg.Amount,
		Price:          leg.Price,
		Amount:         leg.Cost,
		Fees:           fees,
		NetAmount:      t.NetAmount,
	}

	// Strike and expiration are meaningless on equities, so leave them blank
	// there instead of writing a zero strike and an epoch date.
	if leg.Instrument.PutCall != "" || leg.Instrument.StrikePrice != 0 {
		strike := leg.Instrument.StrikePrice
		row.Strike = &strike
	}
	if leg.Instrument.ExpirationDate != nil {
		row.Expiration = formatDate(leg.Instrument.ExpirationDate.Time)
	}

	return row, nil
}

// maxReportedRowErrors caps how many per-transaction failures BuildRows spells
// out. A systemic problem produces hundreds of identical lines, and a wall of
// them is less useful than a handful plus a count.
const maxReportedRowErrors = 5

// BuildRows maps every transaction, or fails without returning any.
//
// All-or-nothing is deliberate: the caller writes what it gets back, and a
// half-built batch would append some trades, abort, and leave the user to work
// out which ones landed. Failing before the first write keeps the sheet in a
// state the next run can simply retry.
func BuildRows(txs []schwab.Transaction) ([]Row, error) {
	rows := make([]Row, 0, len(txs))
	var problems []error

	for _, t := range txs {
		row, err := BuildRow(t)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		rows = append(rows, row)
	}

	if len(problems) > 0 {
		shown := problems
		if len(shown) > maxReportedRowErrors {
			shown = append(shown[:maxReportedRowErrors:maxReportedRowErrors],
				fmt.Errorf("and %d more transactions with the same kind of problem",
					len(problems)-maxReportedRowErrors))
		}
		return nil, fmt.Errorf("%d of %d transactions could not be mapped to a row, so nothing was written: %w",
			len(problems), len(txs), errors.Join(shown...))
	}

	return rows, nil
}

// ActivityID is the dedup key for a transaction, as it is written to and read
// back from the sheet. Everything comparing keys goes through this so the two
// directions cannot drift apart.
func ActivityID(t schwab.Transaction) string {
	return strconv.FormatInt(t.ActivityID, 10)
}

func formatDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(dateLayout)
}

func formatTimestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// columnLetter converts a 1-based column index to its A1 letter.
func columnLetter(n int) string {
	if n < 1 {
		return ""
	}
	var letters []byte
	for n > 0 {
		n--
		letters = append([]byte{byte('A' + n%26)}, letters...)
		n /= 26
	}
	return string(letters)
}
