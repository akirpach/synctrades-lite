package sheets

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/akirpach/synctrades-lite/internal/schwab"
	gsheets "google.golang.org/api/sheets/v4"
)

// ExistingActivityIDs reads the activity ID column and returns everything the
// sheet already holds.
//
// Only one column is fetched. A year of trades is hundreds of rows and nineteen
// columns wide, and none of the other eighteen affect whether a transaction is
// already synced.
func (c *Client) ExistingActivityIDs(ctx context.Context) (map[string]struct{}, error) {
	rng := c.a1(fmt.Sprintf("%s%d:%s", activityIDColumn, firstDataRow, activityIDColumn))

	resp, err := c.svc.Spreadsheets.Values.Get(c.spreadsheetID, rng).
		ValueRenderOption(unformattedValues).
		MajorDimension("COLUMNS").
		Context(ctx).
		Do()
	if err != nil {
		return nil, c.classify("reading existing activity IDs", err)
	}

	return activityIDSet(resp), nil
}

func activityIDSet(resp *gsheets.ValueRange) map[string]struct{} {
	existing := make(map[string]struct{})
	if resp == nil || len(resp.Values) == 0 {
		return existing
	}
	for _, cell := range resp.Values[0] {
		if id := normalizeActivityID(cell); id != "" {
			existing[id] = struct{}{}
		}
	}
	return existing
}

// normalizeActivityID renders a cell as the same string ActivityID produces.
//
// We write IDs as text, so the common case is a string. Numeric forms are
// handled anyway: a sheet may have been produced by an earlier build, imported,
// or edited by hand, and a key that fails to match because of its cell type
// would append a duplicate row for a trade that is already there.
func normalizeActivityID(cell any) string {
	switch v := cell.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case float64:
		return formatWholeNumber(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case json.Number:
		return strings.TrimSpace(v.String())
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

// formatWholeNumber renders a numeric cell without an exponent or a decimal
// tail, so 9.1928304738e10 comes back as "91928304738".
func formatWholeNumber(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return ""
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e18 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// Diff is the outcome of comparing fetched transactions against the sheet.
type Diff struct {
	// New are the transactions to append, oldest first.
	New []schwab.Transaction
	// AlreadySynced counts transactions the sheet already holds.
	AlreadySynced int
	// Repeated counts transactions Schwab returned more than once in this
	// fetch. Not expected, but appending both copies would be a duplicate row
	// the next run could never clean up.
	Repeated int
}

// DiffTransactions splits a fetch into what needs appending and what does not.
//
// New is sorted oldest first so the sheet reads chronologically downward, which
// is both what a ledger should look like and what keeps successive syncs from
// interleaving. Schwab returns transactions newest first.
func DiffTransactions(existing map[string]struct{}, txs []schwab.Transaction) Diff {
	var diff Diff
	seen := make(map[string]struct{}, len(txs))

	for _, t := range txs {
		id := ActivityID(t)
		if _, ok := existing[id]; ok {
			diff.AlreadySynced++
			continue
		}
		if _, ok := seen[id]; ok {
			diff.Repeated++
			continue
		}
		seen[id] = struct{}{}
		diff.New = append(diff.New, t)
	}

	sort.SliceStable(diff.New, func(i, j int) bool {
		a, b := diff.New[i], diff.New[j]
		if !a.Time.Time.Equal(b.Time.Time) {
			return a.Time.Time.Before(b.Time.Time)
		}
		// Same instant: fall back to the ID so the order is deterministic
		// across runs rather than dependent on Schwab's response order.
		return a.ActivityID < b.ActivityID
	})

	return diff
}
