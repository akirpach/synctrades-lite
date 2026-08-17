package schwab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxResponseBody caps a successful response read. Transaction pages are large
// but bounded; an unbounded read on a proxy error page is not.
const maxResponseBody = 32 << 20

// TypeTrade is the only transaction type the MVP syncs. See CLAUDE.md: TRADE
// entries always carry an instrument leg, which is what one-row-per-transaction
// depends on.
const TypeTrade = "TRADE"

// transactionDateFormat is the layout Schwab's transactions endpoint expects
// for startDate and endDate.
//
// UNVERIFIED. The C# implementation passes opaque strings through, so it
// documents nothing, and the vendored PDFs are ambiguous. This is the
// conventional Schwab format and must be confirmed by a live call before this
// code is trusted. The literal Z means times have to be converted to UTC first.
const transactionDateFormat = "2006-01-02T15:04:05.000Z"

// TokenSource supplies a currently valid access token, refreshing if needed.
//
// The client deliberately does not know how tokens are stored or refreshed:
// that belongs to internal/store, which plugs in here at build step 3.
type TokenSource interface {
	AccessToken(ctx context.Context) (string, error)
}

// Client is a typed HTTP client for Schwab's trader API.
type Client struct {
	tokens  TokenSource
	http    *http.Client
	baseURL string
}

// ClientOption adjusts a Client at construction.
type ClientOption func(*Client)

// WithBaseURL overrides the API base URL. It must end in a slash. Intended for
// tests and stubs; production callers should leave the default.
func WithBaseURL(baseURL string) ClientOption {
	return func(c *Client) { c.baseURL = baseURL }
}

// WithHTTPClient supplies the HTTP client used for API calls.
func WithHTTPClient(h *http.Client) ClientOption {
	return func(c *Client) { c.http = h }
}

// NewClient returns a Client that authenticates with tokens from src.
func NewClient(src TokenSource, opts ...ClientOption) *Client {
	c := &Client{
		tokens:  src,
		http:    &http.Client{Timeout: 60 * time.Second},
		baseURL: APIBaseURL,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// AccountNumber pairs a human-readable account number with the opaque hash the
// API actually addresses accounts by.
type AccountNumber struct {
	AccountNumber string `json:"accountNumber"`
	HashValue     string `json:"hashValue"`
}

// SchwabTime handles Schwab's timestamps, which are not RFC 3339.
//
// Values arrive as "2025-07-25T16:02:06+0000": the zone offset has no colon, so
// time.Time's own UnmarshalJSON rejects it outright. Every timestamp in the
// transactions response has this shape, so decoding into a plain time.Time
// fails on the very first record.
type SchwabTime struct {
	time.Time
}

// schwabTimeLayouts are tried in order. The first is what Schwab actually
// sends; the others are accepted in case a field or endpoint differs.
var schwabTimeLayouts = []string{
	"2006-01-02T15:04:05-0700",
	time.RFC3339,
	"2006-01-02T15:04:05.000-0700",
	"2006-01-02",
}

func (t *SchwabTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}

	for _, layout := range schwabTimeLayouts {
		if parsed, err := time.Parse(layout, s); err == nil {
			t.Time = parsed
			return nil
		}
	}
	return fmt.Errorf("unrecognized schwab timestamp %q", s)
}

func (t SchwabTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.Time)
}

// Instrument is the security a transfer item moved. Fee legs carry a CURRENCY
// instrument; the real trade leg carries the actual security.
type Instrument struct {
	AssetType        string      `json:"assetType"`
	Status           string      `json:"status"`
	Symbol           string      `json:"symbol"`
	Description      string      `json:"description"`
	InstrumentID     int64       `json:"instrumentId"`
	ClosingPrice     float64     `json:"closingPrice"`
	Type             string      `json:"type"`
	PutCall          string      `json:"putCall"`
	StrikePrice      float64     `json:"strikePrice"`
	ExpirationDate   *SchwabTime `json:"expirationDate"`
	UnderlyingSymbol string      `json:"underlyingSymbol"`
}

// TransferItem is one leg of a transaction. A leg is either a fee (it carries
// feeType and a CURRENCY instrument) or the instrument itself (it carries
// positionEffect and price).
type TransferItem struct {
	Instrument     Instrument `json:"instrument"`
	Amount         float64    `json:"amount"`
	Cost           float64    `json:"cost"`
	Price          float64    `json:"price"`
	FeeType        string     `json:"feeType"`
	PositionEffect string     `json:"positionEffect"`
}

// IsFee reports whether this leg is a fee rather than the traded instrument.
func (i TransferItem) IsFee() bool { return i.FeeType != "" }

// Transaction is a single Schwab account activity record.
type Transaction struct {
	ActivityID    int64          `json:"activityId"`
	Time          SchwabTime     `json:"time"`
	AccountNumber string         `json:"accountNumber"`
	Type          string         `json:"type"`
	Status        string         `json:"status"`
	SubAccount    string         `json:"subAccount"`
	TradeDate     SchwabTime     `json:"tradeDate"`
	PositionID    int64          `json:"positionId"`
	OrderID       int64          `json:"orderId"`
	NetAmount     float64        `json:"netAmount"`
	TransferItems []TransferItem `json:"transferItems"`
}

// ErrMultipleInstrumentLegs means a transaction carries more than one non-fee
// leg, which breaks the one-row-per-transaction assumption. Assignments,
// exercises and multi-leg orders can do this. CLAUDE.md defers the rule for
// handling them, so surface it rather than silently picking a leg.
var ErrMultipleInstrumentLegs = errors.New("transaction has more than one instrument leg")

// ErrNoInstrumentLeg means every leg is a fee, so there is no instrument to
// build a row from. Expected for non-TRADE activity.
var ErrNoInstrumentLeg = errors.New("transaction has no instrument leg")

// InstrumentLeg returns the single non-fee leg the sheet row is built from.
func (t Transaction) InstrumentLeg() (TransferItem, error) {
	var found TransferItem
	count := 0
	for _, item := range t.TransferItems {
		if item.IsFee() {
			continue
		}
		found = item
		count++
	}

	switch count {
	case 1:
		return found, nil
	case 0:
		return TransferItem{}, fmt.Errorf("%w (activityId %d)", ErrNoInstrumentLeg, t.ActivityID)
	default:
		return TransferItem{}, fmt.Errorf("%w: %d legs (activityId %d, type %s)",
			ErrMultipleInstrumentLegs, count, t.ActivityID, t.Type)
	}
}

// Fees is the total charged across every fee leg, negated so a normal charge
// reads positive and a refund reads negative.
func (t Transaction) Fees() float64 {
	var total float64
	for _, item := range t.TransferItems {
		if item.IsFee() {
			total += item.Cost
		}
	}
	return -total
}

// NetAmountMatches checks the instrument leg's cost plus the fee legs against
// the reported netAmount, within a cent. A mismatch means the fee-folding rule
// has met a transaction shape it does not understand, which in a financial tool
// is worth catching rather than writing a wrong row.
func (t Transaction) NetAmountMatches() bool {
	leg, err := t.InstrumentLeg()
	if err != nil {
		return false
	}
	diff := leg.Cost - t.Fees() - t.NetAmount
	return diff < 0.01 && diff > -0.01
}

// AccountNumbers lists the caller's accounts with their hashes. Every other
// account-scoped call needs a hash from here first.
func (c *Client) AccountNumbers(ctx context.Context) ([]AccountNumber, error) {
	var accounts []AccountNumber
	if err := c.get(ctx, "account numbers", "accounts/accountNumbers", nil, &accounts); err != nil {
		return nil, err
	}
	return accounts, nil
}

// AccountHash resolves a display account number to its hash. Passing an empty
// accountNumber returns the only account's hash, and fails if there is more
// than one, since MVP syncs one account per run.
func (c *Client) AccountHash(ctx context.Context, accountNumber string) (string, error) {
	accounts, err := c.AccountNumbers(ctx)
	if err != nil {
		return "", err
	}
	if len(accounts) == 0 {
		return "", errors.New("schwab returned no accounts for these credentials")
	}

	if accountNumber == "" {
		if len(accounts) > 1 {
			known := make([]string, 0, len(accounts))
			for _, a := range accounts {
				known = append(known, a.AccountNumber)
			}
			return "", fmt.Errorf("this login has %d accounts (%s); specify which one to sync",
				len(accounts), strings.Join(known, ", "))
		}
		return accounts[0].HashValue, nil
	}

	for _, a := range accounts {
		if a.AccountNumber == accountNumber {
			return a.HashValue, nil
		}
	}
	return "", fmt.Errorf("account %s not found on this login", accountNumber)
}

// Transactions fetches account activity between start and end.
//
// types is passed through as Schwab's comma-separated filter; pass TypeTrade for
// MVP behavior. Whether Schwab caps the range per request is unverified, so a
// caller asking for a year may need windowing added here.
func (c *Client) Transactions(
	ctx context.Context,
	accountHash string,
	start, end time.Time,
	types ...string,
) ([]Transaction, error) {
	if accountHash == "" {
		return nil, errors.New("account hash is empty; resolve it with AccountHash first")
	}
	if end.Before(start) {
		return nil, fmt.Errorf("end %s is before start %s", end.Format(time.RFC3339), start.Format(time.RFC3339))
	}

	q := url.Values{
		"startDate": {start.UTC().Format(transactionDateFormat)},
		"endDate":   {end.UTC().Format(transactionDateFormat)},
	}
	if len(types) > 0 {
		q.Set("types", strings.Join(types, ","))
	}

	var transactions []Transaction
	path := "accounts/" + url.PathEscape(accountHash) + "/transactions"
	if err := c.get(ctx, "transactions", path, q, &transactions); err != nil {
		return nil, err
	}
	return transactions, nil
}

func (c *Client) get(ctx context.Context, op, path string, query url.Values, out any) error {
	token, err := c.tokens.AccessToken(ctx)
	if err != nil {
		return fmt.Errorf("obtaining access token for %s: %w", op, err)
	}

	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("building %s request: %w", op, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s request failed: %w", op, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return fmt.Errorf("reading %s response: %w", op, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return classify(op, resp.StatusCode, body)
	}

	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding %s response: %w", op, err)
	}
	return nil
}
