package sheets

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	gsheets "google.golang.org/api/sheets/v4"
)

// DefaultSheetName is the tab synced into when the user did not name one.
const DefaultSheetName = "Trades"

// valueInputRAW stops Sheets from parsing what we send.
//
// USER_ENTERED would treat every value as if a person had typed it: the activity
// ID becomes a numeric cell, and a number that comes back in a different
// representation than it went in is a broken dedup key, which means duplicate
// rows or silently dropped trades. RAW costs us date-typed cells (dates land as
// ISO text) and buys us a key that round-trips byte for byte. It also means a
// description beginning with "=" is stored as text rather than evaluated as a
// formula.
const valueInputRAW = "RAW"

// unformattedValues asks for the underlying cell values rather than what the
// user's locale and number format happen to display.
const unformattedValues = "UNFORMATTED_VALUE"

var (
	// ErrSpreadsheetNotFound means the configured spreadsheet ID does not
	// resolve. Usually a whole URL was pasted instead of just the ID.
	ErrSpreadsheetNotFound = errors.New("spreadsheet not found")

	// ErrNotShared means the service account cannot see the spreadsheet. This
	// is the most common setup mistake: creating the key but never sharing the
	// sheet with the service account's email.
	ErrNotShared = errors.New("the service account does not have access to this spreadsheet")

	// ErrHeaderMismatch means the target tab already has a header row that is
	// not ours. Appending under it would file values under the wrong columns.
	ErrHeaderMismatch = errors.New("the sheet's header row does not match the expected columns")
)

// Config is the user's Sheets destination, as held in the credential store.
type Config struct {
	ServiceAccountKeyPath string
	SpreadsheetID         string
	SheetName             string
}

// Client is a Sheets API wrapper scoped to one spreadsheet and one tab.
type Client struct {
	svc           *gsheets.Service
	spreadsheetID string
	sheetName     string
}

// NewClient authenticates with the user's service account key and targets the
// configured spreadsheet.
//
// apiOpts are passed through to the Google client and are how tests point it at
// a stub server. When any are supplied, the service account key is optional, so
// a test does not need a credentials file on disk.
func NewClient(ctx context.Context, cfg Config, apiOpts ...option.ClientOption) (*Client, error) {
	if cfg.SpreadsheetID == "" {
		return nil, errors.New("no spreadsheet configured; run `synctrades auth sheets`")
	}

	opts := []option.ClientOption{option.WithScopes(gsheets.SpreadsheetsScope)}
	switch {
	case cfg.ServiceAccountKeyPath != "":
		// Check the file here so a typo in the path reads as a typo in the path,
		// not as an opaque credentials error from deep inside the Google client.
		if _, err := os.Stat(cfg.ServiceAccountKeyPath); err != nil {
			return nil, fmt.Errorf("reading the service account key at %s: %w", cfg.ServiceAccountKeyPath, err)
		}
		opts = append(opts, option.WithCredentialsFile(cfg.ServiceAccountKeyPath))
	case len(apiOpts) == 0:
		return nil, errors.New("no service account key configured; run `synctrades auth sheets`")
	}
	opts = append(opts, apiOpts...)

	svc, err := gsheets.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating the Sheets client: %w", err)
	}

	name := cfg.SheetName
	if name == "" {
		name = DefaultSheetName
	}

	return &Client{svc: svc, spreadsheetID: cfg.SpreadsheetID, sheetName: name}, nil
}

// SpreadsheetID reports the spreadsheet this client writes to.
func (c *Client) SpreadsheetID() string { return c.spreadsheetID }

// SheetName reports the tab this client writes to.
func (c *Client) SheetName() string { return c.sheetName }

// URL is the link to the target tab, for printing after a sync.
func (c *Client) URL() string {
	return "https://docs.google.com/spreadsheets/d/" + c.spreadsheetID
}

// EnsureSheet creates the target tab if the spreadsheet does not already have
// it, and confirms the spreadsheet is reachable at all.
//
// Creating a missing tab is safe in a way that creating a missing spreadsheet
// would not be: the user already pointed us at this file deliberately.
func (c *Client) EnsureSheet(ctx context.Context) error {
	resp, err := c.svc.Spreadsheets.Get(c.spreadsheetID).
		Fields("sheets.properties.title").
		Context(ctx).
		Do()
	if err != nil {
		return c.classify("reading the spreadsheet", err)
	}

	for _, s := range resp.Sheets {
		if s.Properties != nil && s.Properties.Title == c.sheetName {
			return nil
		}
	}

	_, err = c.svc.Spreadsheets.BatchUpdate(c.spreadsheetID, &gsheets.BatchUpdateSpreadsheetRequest{
		Requests: []*gsheets.Request{{
			AddSheet: &gsheets.AddSheetRequest{
				Properties: &gsheets.SheetProperties{Title: c.sheetName},
			},
		}},
	}).Context(ctx).Do()
	if err != nil {
		return c.classify(fmt.Sprintf("creating the %q tab", c.sheetName), err)
	}
	return nil
}

// EnsureHeader writes the header row into an empty sheet and refuses to touch a
// sheet whose header is something else.
//
// The refusal matters more than the write. Appending our column order beneath
// somebody else's header would scatter prices into date columns, and nothing
// downstream would notice.
func (c *Client) EnsureHeader(ctx context.Context) error {
	rng := c.a1(fmt.Sprintf("%s%d:%s%d", activityIDColumn, headerRow, lastColumn, headerRow))

	resp, err := c.svc.Spreadsheets.Values.Get(c.spreadsheetID, rng).Context(ctx).Do()
	if err != nil {
		return c.classify("reading the header row", err)
	}

	existing := firstRow(resp.Values)
	if len(existing) == 0 {
		return c.writeHeader(ctx, rng)
	}

	if diff := headerDiff(existing); diff != "" {
		return fmt.Errorf("%w in %q: %s; point --sheet at a different tab or fix the header by hand",
			ErrHeaderMismatch, c.sheetName, diff)
	}
	return nil
}

func (c *Client) writeHeader(ctx context.Context, rng string) error {
	values := make([]any, len(Headers))
	for i, h := range Headers {
		values[i] = h
	}

	_, err := c.svc.Spreadsheets.Values.Update(c.spreadsheetID, rng, &gsheets.ValueRange{
		Values: [][]any{values},
	}).ValueInputOption(valueInputRAW).Context(ctx).Do()
	if err != nil {
		return c.classify("writing the header row", err)
	}
	return nil
}

// headerDiff describes the first way existing departs from Headers, or "" when
// they match. Comparison is exact after trimming, because a reordered or renamed
// column is exactly the situation this check exists to catch.
func headerDiff(existing []any) string {
	if len(existing) != len(Headers) {
		return fmt.Sprintf("found %d columns, expected %d (%s)",
			len(existing), len(Headers), strings.Join(Headers, ", "))
	}
	for i, want := range Headers {
		got := strings.TrimSpace(asString(existing[i]))
		if got != want {
			return fmt.Sprintf("column %s is %q, expected %q", columnLetter(i+1), got, want)
		}
	}
	return ""
}

// Append adds rows to the bottom of the table and reports how many were written.
//
// It appends only; rows already present are never rewritten. That is the MVP
// dedup contract, and callers are expected to have filtered with Diff first.
func (c *Client) Append(ctx context.Context, rows []Row) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	values := make([][]any, len(rows))
	for i, r := range rows {
		values[i] = r.Values()
	}

	// Append locates the end of the table starting from this anchor, so the
	// range names where the table begins rather than where the new rows go.
	anchor := c.a1(fmt.Sprintf("%s%d", activityIDColumn, headerRow))

	_, err := c.svc.Spreadsheets.Values.Append(c.spreadsheetID, anchor, &gsheets.ValueRange{
		Values: values,
	}).
		ValueInputOption(valueInputRAW).
		InsertDataOption("INSERT_ROWS").
		Context(ctx).
		Do()
	if err != nil {
		return 0, c.classify(fmt.Sprintf("appending %d rows", len(rows)), err)
	}
	return len(rows), nil
}

// a1 qualifies a cell range with the target tab, quoting the name so tabs with
// spaces or apostrophes work.
func (c *Client) a1(cellRange string) string {
	return "'" + strings.ReplaceAll(c.sheetName, "'", "''") + "'!" + cellRange
}

// classify turns Google's HTTP errors into the two that actually happen during
// setup, so the CLI can say what to fix instead of echoing a status code.
func (c *Client) classify(op string, err error) error {
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case http.StatusNotFound:
			return fmt.Errorf("%s: %w (id %q); the ID is the part of the sheet URL between /d/ and /edit, not the whole URL",
				op, ErrSpreadsheetNotFound, c.spreadsheetID)
		case http.StatusForbidden:
			return fmt.Errorf("%s: %w; share the sheet with the service account's client_email, giving it Editor access",
				op, ErrNotShared)
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}

func firstRow(values [][]any) []any {
	if len(values) == 0 {
		return nil
	}
	return values[0]
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Sprint(v)
	}
	return s
}
