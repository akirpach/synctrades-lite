package sheets

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/akirpach/synctrades-lite/internal/schwab"
	"google.golang.org/api/option"
)

// newTestClient points a Client at a stub Sheets server.
func newTestClient(t *testing.T, sheetName string, handler http.HandlerFunc) *Client {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client, err := NewClient(context.Background(),
		Config{SpreadsheetID: "sheet-id", SheetName: sheetName},
		option.WithEndpoint(srv.URL),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func writeAPIError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"code": code, "message": message, "status": "ERROR"},
	})
}

func decodeBody(t *testing.T, r *http.Request, out any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		t.Fatalf("decoding request body: %v", err)
	}
}

func TestNewClientRequiresConfiguration(t *testing.T) {
	ctx := context.Background()

	if _, err := NewClient(ctx, Config{}); err == nil {
		t.Error("expected an error with no spreadsheet ID")
	}
	if _, err := NewClient(ctx, Config{SpreadsheetID: "x"}); err == nil {
		t.Error("expected an error with no service account key")
	}
	if _, err := NewClient(ctx, Config{SpreadsheetID: "x", ServiceAccountKeyPath: "no-such-key.json"},
		option.WithoutAuthentication()); err == nil {
		t.Error("expected an error when the key file is missing")
	}
}

func TestNewClientDefaultsTheSheetName(t *testing.T) {
	client := newTestClient(t, "", func(w http.ResponseWriter, r *http.Request) {})
	if client.SheetName() != DefaultSheetName {
		t.Errorf("SheetName = %q, want %q", client.SheetName(), DefaultSheetName)
	}
}

func TestEnsureSheetAcceptsAnExistingTab(t *testing.T) {
	calls := 0
	client := newTestClient(t, "Trades", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet {
			t.Errorf("unexpected %s %s; an existing tab needs no write", r.Method, r.URL.Path)
		}
		writeJSON(w, map[string]any{
			"sheets": []any{
				map[string]any{"properties": map[string]any{"title": "Sheet1"}},
				map[string]any{"properties": map[string]any{"title": "Trades"}},
			},
		})
	})

	if err := client.EnsureSheet(context.Background()); err != nil {
		t.Fatalf("EnsureSheet: %v", err)
	}
	if calls != 1 {
		t.Errorf("made %d calls, want 1", calls)
	}
}

func TestEnsureSheetCreatesAMissingTab(t *testing.T) {
	var created string
	client := newTestClient(t, "Trades", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, map[string]any{
				"sheets": []any{map[string]any{"properties": map[string]any{"title": "Sheet1"}}},
			})
			return
		}
		if !strings.HasSuffix(r.URL.Path, ":batchUpdate") {
			t.Errorf("unexpected POST to %s", r.URL.Path)
		}
		var body struct {
			Requests []struct {
				AddSheet struct {
					Properties struct {
						Title string `json:"title"`
					} `json:"properties"`
				} `json:"addSheet"`
			} `json:"requests"`
		}
		decodeBody(t, r, &body)
		if len(body.Requests) == 1 {
			created = body.Requests[0].AddSheet.Properties.Title
		}
		writeJSON(w, map[string]any{"spreadsheetId": "sheet-id"})
	})

	if err := client.EnsureSheet(context.Background()); err != nil {
		t.Fatalf("EnsureSheet: %v", err)
	}
	if created != "Trades" {
		t.Errorf("created tab %q, want Trades", created)
	}
}

func TestEnsureSheetNamesSetupFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   error
	}{
		{"not shared", http.StatusForbidden, ErrNotShared},
		{"wrong id", http.StatusNotFound, ErrSpreadsheetNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestClient(t, "Trades", func(w http.ResponseWriter, r *http.Request) {
				writeAPIError(w, tc.status, "denied")
			})

			err := client.EnsureSheet(context.Background())
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestEnsureHeaderWritesIntoAnEmptySheet(t *testing.T) {
	var wrote []any
	var inputOption string

	client := newTestClient(t, "Trades", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, map[string]any{"range": "Trades!A1:S1"})
			return
		}
		if r.Method != http.MethodPut {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		inputOption = r.URL.Query().Get("valueInputOption")
		var body struct {
			Values [][]any `json:"values"`
		}
		decodeBody(t, r, &body)
		if len(body.Values) == 1 {
			wrote = body.Values[0]
		}
		writeJSON(w, map[string]any{"updatedCells": int64(len(Headers))})
	})

	if err := client.EnsureHeader(context.Background()); err != nil {
		t.Fatalf("EnsureHeader: %v", err)
	}
	if len(wrote) != len(Headers) {
		t.Fatalf("wrote %d header cells, want %d", len(wrote), len(Headers))
	}
	for i, want := range Headers {
		if wrote[i] != want {
			t.Errorf("header %d = %#v, want %q", i, wrote[i], want)
		}
	}
	if inputOption != valueInputRAW {
		t.Errorf("valueInputOption = %q, want %q", inputOption, valueInputRAW)
	}
}

func TestEnsureHeaderLeavesAMatchingHeaderAlone(t *testing.T) {
	client := newTestClient(t, "Trades", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected %s; a matching header must not be rewritten", r.Method)
		}
		row := make([]any, len(Headers))
		for i, h := range Headers {
			row[i] = h
		}
		writeJSON(w, map[string]any{"values": [][]any{row}})
	})

	if err := client.EnsureHeader(context.Background()); err != nil {
		t.Fatalf("EnsureHeader: %v", err)
	}
}

func TestEnsureHeaderRefusesAForeignHeader(t *testing.T) {
	cases := []struct {
		name string
		row  []any
		want string
	}{
		{"someone else's sheet", []any{"Date", "Ticker", "Shares"}, "found 3 columns"},
		{"one column renamed", renamedHeader("Price", "Cost Basis"), `expected "Price"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestClient(t, "Trades", func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Fatalf("a mismatched header must not be written over (%s)", r.Method)
				}
				writeJSON(w, map[string]any{"values": [][]any{tc.row}})
			})

			err := client.EnsureHeader(context.Background())
			if !errors.Is(err, ErrHeaderMismatch) {
				t.Fatalf("err = %v, want ErrHeaderMismatch", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err, tc.want)
			}
		})
	}
}

func renamedHeader(from, to string) []any {
	row := make([]any, len(Headers))
	for i, h := range Headers {
		if h == from {
			h = to
		}
		row[i] = h
	}
	return row
}

func TestAppendSendsRawRowsAndInsertsThem(t *testing.T) {
	var gotPath, gotInput, gotInsert string
	var gotValues [][]any

	client := newTestClient(t, "Trades", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotInput = r.URL.Query().Get("valueInputOption")
		gotInsert = r.URL.Query().Get("insertDataOption")
		var body struct {
			Values [][]any `json:"values"`
		}
		decodeBody(t, r, &body)
		gotValues = body.Values
		writeJSON(w, map[string]any{"updates": map[string]any{"updatedRows": int64(len(body.Values))}})
	})

	rows, err := BuildRows(mustTransactions(t, equityTradeJSON, optionTradeJSON))
	if err != nil {
		t.Fatalf("BuildRows: %v", err)
	}

	written, err := client.Append(context.Background(), rows)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	if written != 2 {
		t.Errorf("wrote %d rows, want 2", written)
	}
	if gotInput != valueInputRAW {
		t.Errorf("valueInputOption = %q, want %q", gotInput, valueInputRAW)
	}
	if gotInsert != "INSERT_ROWS" {
		t.Errorf("insertDataOption = %q, want INSERT_ROWS", gotInsert)
	}
	if want := "/v4/spreadsheets/sheet-id/values/'Trades'!A1:append"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if len(gotValues) != 2 || len(gotValues[0]) != len(Headers) {
		t.Fatalf("sent %d rows of width %d, want 2 x %d", len(gotValues), len(gotValues[0]), len(Headers))
	}
	// The key must arrive as text; a numeric cell is what breaks dedup.
	if gotValues[0][0] != "100209943192" {
		t.Errorf("first appended key = %#v, want 100209943192 as text", gotValues[0][0])
	}
}

func TestAppendOfNothingMakesNoRequest(t *testing.T) {
	client := newTestClient(t, "Trades", func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected %s %s for an empty append", r.Method, r.URL.Path)
	})

	written, err := client.Append(context.Background(), nil)
	if err != nil || written != 0 {
		t.Fatalf("Append(nil) = %d, %v; want 0, nil", written, err)
	}
}

func TestSheetNamesWithApostrophesAreQuoted(t *testing.T) {
	client := newTestClient(t, "Al's Trades", func(w http.ResponseWriter, r *http.Request) {})
	if got, want := client.a1("A1"), "'Al''s Trades'!A1"; got != want {
		t.Errorf("a1 = %q, want %q", got, want)
	}
}

func mustTransactions(t *testing.T, raw ...string) []schwab.Transaction {
	t.Helper()
	txs := make([]schwab.Transaction, 0, len(raw))
	for _, r := range raw {
		txs = append(txs, mustTransaction(t, r))
	}
	return txs
}
