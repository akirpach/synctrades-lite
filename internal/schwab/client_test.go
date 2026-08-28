package schwab

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// optionTradeJSON is lifted from the real transactions response vendored in the
// sibling repo. It is the load-bearing fixture: an option trade whose fee legs
// and instrument leg must sum to netAmount (-81 + -0.65 + -0.01 = -81.66).
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
    {"instrument": {"assetType": "CURRENCY", "symbol": "CURRENCY_USD"}, "amount": 0, "cost": 0, "feeType": "SEC_FEE"},
    {"instrument": {"assetType": "CURRENCY", "symbol": "CURRENCY_USD"}, "amount": 0.01, "cost": -0.01, "feeType": "OPT_REG_FEE"},
    {"instrument": {"assetType": "CURRENCY", "symbol": "CURRENCY_USD"}, "amount": 0, "cost": 0, "feeType": "TAF_FEE"},
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

type staticTokenSource struct {
	token string
	err   error
}

func (s staticTokenSource) AccessToken(context.Context) (string, error) {
	return s.token, s.err
}

// newAPIClient points a Client at a stub server and captures what it sent.
func newAPIClient(t *testing.T, status int, body string) (*Client, *http.Request, *url.Values) {
	t.Helper()

	var gotReq http.Request
	gotQuery := url.Values{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = *r
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(staticTokenSource{token: "test-access-token"})
	c.baseURL = srv.URL + "/"
	return c, &gotReq, &gotQuery
}

func TestSchwabTimeParsesColonlessOffset(t *testing.T) {
	// "+0000" is not RFC 3339, so a plain time.Time field fails on the first
	// record of every real response.
	var st SchwabTime
	if err := json.Unmarshal([]byte(`"2025-07-25T16:02:06+0000"`), &st); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := time.Date(2025, 7, 25, 16, 2, 6, 0, time.UTC)
	if !st.Time.Equal(want) {
		t.Errorf("parsed %s, want %s", st.Time, want)
	}
}

func TestSchwabTimeRejectsGarbage(t *testing.T) {
	var st SchwabTime
	if err := json.Unmarshal([]byte(`"not a timestamp"`), &st); err == nil {
		t.Error("garbage timestamp accepted")
	}
}

func TestSchwabTimeToleratesNull(t *testing.T) {
	var st SchwabTime
	if err := json.Unmarshal([]byte(`null`), &st); err != nil {
		t.Errorf("null timestamp rejected: %v", err)
	}
	if !st.Time.IsZero() {
		t.Errorf("null produced %s, want zero", st.Time)
	}
}

func decodeOptionTrade(t *testing.T) Transaction {
	t.Helper()
	var tx Transaction
	if err := json.Unmarshal([]byte(optionTradeJSON), &tx); err != nil {
		t.Fatalf("decoding the real fixture: %v", err)
	}
	return tx
}

func TestTransactionDecodesRealFixture(t *testing.T) {
	tx := decodeOptionTrade(t)

	if tx.ActivityID != 100209943191 {
		t.Errorf("activityId = %d", tx.ActivityID)
	}
	if tx.Type != TypeTrade {
		t.Errorf("type = %q", tx.Type)
	}
	if tx.NetAmount != -81.66 {
		t.Errorf("netAmount = %v", tx.NetAmount)
	}
	if len(tx.TransferItems) != 5 {
		t.Fatalf("got %d transfer items, want 5", len(tx.TransferItems))
	}
	if tx.Time.Time.IsZero() {
		t.Error("time did not decode")
	}
}

func TestInstrumentLegPicksTheTradedSecurity(t *testing.T) {
	leg, err := decodeOptionTrade(t).InstrumentLeg()
	if err != nil {
		t.Fatalf("InstrumentLeg: %v", err)
	}

	if leg.Instrument.AssetType != "OPTION" {
		t.Errorf("assetType = %q, want OPTION (a fee leg was picked)", leg.Instrument.AssetType)
	}
	if leg.Instrument.UnderlyingSymbol != "HIMS" {
		t.Errorf("underlyingSymbol = %q", leg.Instrument.UnderlyingSymbol)
	}
	if leg.Price != 0.81 {
		t.Errorf("price = %v, want 0.81", leg.Price)
	}
	if leg.PositionEffect != "OPENING" {
		t.Errorf("positionEffect = %q", leg.PositionEffect)
	}
	if leg.IsFee() {
		t.Error("the traded leg reports itself as a fee")
	}
}

func TestFeesFoldToASinglePositiveCharge(t *testing.T) {
	// COMMISSION -0.65 and OPT_REG_FEE -0.01, negated: a charge reads positive.
	if got := decodeOptionTrade(t).Fees(); got < 0.6599 || got > 0.6601 {
		t.Errorf("Fees() = %v, want 0.66", got)
	}
}

func TestFeesGoNegativeOnARefund(t *testing.T) {
	tx := Transaction{TransferItems: []TransferItem{
		{FeeType: "COMMISSION", Cost: 0.65},
		{PositionEffect: "OPENING", Cost: -81},
	}}
	if got := tx.Fees(); got > -0.6499 || got < -0.6501 {
		t.Errorf("Fees() = %v, want -0.65 for a refunded fee", got)
	}
}

func TestNetAmountMatchesTheFixture(t *testing.T) {
	if !decodeOptionTrade(t).NetAmountMatches() {
		t.Error("the real fixture fails its own netAmount check")
	}
}

func TestNetAmountMismatchIsDetected(t *testing.T) {
	tx := decodeOptionTrade(t)
	tx.NetAmount = -99.99
	if tx.NetAmountMatches() {
		t.Error("a wrong netAmount passed the check")
	}
}

func TestInstrumentLegRejectsAmbiguousTransactions(t *testing.T) {
	// Assignments and multi-leg orders can carry two non-fee legs. CLAUDE.md
	// defers the rule, so this must surface rather than guess.
	tx := Transaction{ActivityID: 1, Type: "TRADE", TransferItems: []TransferItem{
		{FeeType: "COMMISSION", Cost: -0.65},
		{PositionEffect: "OPENING", Cost: -81},
		{PositionEffect: "CLOSING", Cost: 40},
	}}

	if _, err := tx.InstrumentLeg(); !errors.Is(err, ErrMultipleInstrumentLegs) {
		t.Errorf("error = %v, want ErrMultipleInstrumentLegs", err)
	}
}

func TestInstrumentLegRejectsFeeOnlyTransactions(t *testing.T) {
	tx := Transaction{ActivityID: 2, TransferItems: []TransferItem{
		{FeeType: "COMMISSION", Cost: -0.65},
	}}

	if _, err := tx.InstrumentLeg(); !errors.Is(err, ErrNoInstrumentLeg) {
		t.Errorf("error = %v, want ErrNoInstrumentLeg", err)
	}
}

func TestAccountNumbersSendsBearerToken(t *testing.T) {
	c, gotReq, _ := newAPIClient(t, http.StatusOK,
		`[{"accountNumber":"15011495","hashValue":"ABC123HASH"}]`)

	accounts, err := c.AccountNumbers(context.Background())
	if err != nil {
		t.Fatalf("AccountNumbers: %v", err)
	}

	if got := gotReq.Header.Get("Authorization"); got != "Bearer test-access-token" {
		t.Errorf("Authorization = %q", got)
	}
	if gotReq.URL.Path != "/accounts/accountNumbers" {
		t.Errorf("path = %q", gotReq.URL.Path)
	}
	if len(accounts) != 1 || accounts[0].HashValue != "ABC123HASH" {
		t.Errorf("accounts = %+v", accounts)
	}
}

func TestAccountHashResolvesSingleAccount(t *testing.T) {
	c, _, _ := newAPIClient(t, http.StatusOK,
		`[{"accountNumber":"15011495","hashValue":"ABC123HASH"}]`)

	hash, err := c.AccountHash(context.Background(), "")
	if err != nil {
		t.Fatalf("AccountHash: %v", err)
	}
	if hash != "ABC123HASH" {
		t.Errorf("hash = %q", hash)
	}
}

func TestAccountHashRefusesToGuessBetweenAccounts(t *testing.T) {
	// MVP syncs one account per run, so silently picking the first would write
	// the wrong account's trades into the sheet.
	c, _, _ := newAPIClient(t, http.StatusOK,
		`[{"accountNumber":"111","hashValue":"H1"},{"accountNumber":"222","hashValue":"H2"}]`)

	_, err := c.AccountHash(context.Background(), "")
	if err == nil {
		t.Fatal("picked an account without being told which")
	}
	for _, want := range []string{"111", "222"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not list account %s: %v", want, err)
		}
	}
}

func TestAccountHashFindsRequestedAccount(t *testing.T) {
	c, _, _ := newAPIClient(t, http.StatusOK,
		`[{"accountNumber":"111","hashValue":"H1"},{"accountNumber":"222","hashValue":"H2"}]`)

	hash, err := c.AccountHash(context.Background(), "222")
	if err != nil {
		t.Fatalf("AccountHash: %v", err)
	}
	if hash != "H2" {
		t.Errorf("hash = %q, want H2", hash)
	}
}

func TestAccountHashReportsUnknownAccount(t *testing.T) {
	c, _, _ := newAPIClient(t, http.StatusOK, `[{"accountNumber":"111","hashValue":"H1"}]`)

	if _, err := c.AccountHash(context.Background(), "999"); err == nil {
		t.Error("unknown account accepted")
	}
}

func TestTransactionsBuildsTheQuery(t *testing.T) {
	c, gotReq, gotQuery := newAPIClient(t, http.StatusOK, "["+optionTradeJSON+"]")

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC)

	txs, err := c.Transactions(context.Background(), "ABC123HASH", start, end, TypeTrade)
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}

	if gotReq.URL.Path != "/accounts/ABC123HASH/transactions" {
		t.Errorf("path = %q", gotReq.URL.Path)
	}
	if got := gotQuery.Get("types"); got != TypeTrade {
		t.Errorf("types = %q", got)
	}
	if got := gotQuery.Get("startDate"); got != "2025-01-01T00:00:00.000Z" {
		t.Errorf("startDate = %q", got)
	}
	if got := gotQuery.Get("endDate"); got != "2025-12-31T23:59:59.000Z" {
		t.Errorf("endDate = %q", got)
	}
	if len(txs) != 1 || txs[0].ActivityID != 100209943191 {
		t.Errorf("transactions = %+v", txs)
	}
}

func TestTransactionsConvertsToUTC(t *testing.T) {
	// The date layout ends in a literal Z, so a non-UTC input must be converted
	// or the timestamp is silently wrong by the offset.
	c, _, gotQuery := newAPIClient(t, http.StatusOK, "[]")

	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	start := time.Date(2025, 6, 1, 7, 0, 0, 0, chicago) // 12:00 UTC

	if _, err := c.Transactions(context.Background(), "H", start, start.Add(time.Hour)); err != nil {
		t.Fatalf("Transactions: %v", err)
	}
	if got := gotQuery.Get("startDate"); got != "2025-06-01T12:00:00.000Z" {
		t.Errorf("startDate = %q, want the UTC equivalent", got)
	}
}

func TestTransactionsValidatesArguments(t *testing.T) {
	c, _, _ := newAPIClient(t, http.StatusOK, "[]")
	now := time.Now()

	if _, err := c.Transactions(context.Background(), "", now, now); err == nil {
		t.Error("empty account hash accepted")
	}
	if _, err := c.Transactions(context.Background(), "H", now, now.Add(-time.Hour)); err == nil {
		t.Error("end before start accepted")
	}
}

func TestStatusCodesMapToSentinels(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusForbidden, ErrForbidden},
		{http.StatusNotFound, ErrNotFound},
		{http.StatusTooManyRequests, ErrRateLimited},
		{http.StatusInternalServerError, ErrSchwabUnavailable},
		{http.StatusBadGateway, ErrSchwabUnavailable},
	}

	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			c, _, _ := newAPIClient(t, tt.status, `{"error":"nope"}`)

			_, err := c.AccountNumbers(context.Background())
			if !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}

			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error is not an *APIError: %v", err)
			}
			if apiErr.StatusCode != tt.status {
				t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, tt.status)
			}
		})
	}
}

func TestBadRequestGetsNoSentinel(t *testing.T) {
	// A 400 means different things on the token and data endpoints, so it
	// deliberately maps to no sentinel.
	c, _, _ := newAPIClient(t, http.StatusBadRequest, `{"error":"malformed"}`)

	err := errors.Unwrap(func() error { _, e := c.AccountNumbers(context.Background()); return e }())
	if err != nil {
		t.Errorf("400 wrapped sentinel %v, want none", err)
	}
}

func TestTokenSourceFailurePropagates(t *testing.T) {
	c, _, _ := newAPIClient(t, http.StatusOK, "[]")
	c.tokens = staticTokenSource{err: ErrReauthRequired}

	_, err := c.AccountNumbers(context.Background())
	if !errors.Is(err, ErrReauthRequired) {
		t.Errorf("error = %v, want ErrReauthRequired", err)
	}
}
