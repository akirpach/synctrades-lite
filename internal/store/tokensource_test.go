package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/akirpach/synctrades-lite/internal/schwab"
)

var tokenNow = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

// tokenSourceFixture returns a TokenSource whose refresh calls hit a stub
// Schwab token endpoint, along with the store behind it.
func tokenSourceFixture(t *testing.T, status int, body string, tok schwab.Token) (*TokenSource, *Store) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	s := testStore(t)
	creds := sampleCredentials()
	creds.Schwab.SetToken(tok)
	if err := s.Save(creds); err != nil {
		t.Fatalf("Save: %v", err)
	}

	ts := NewTokenSource(s, schwab.WithTokenEndpoint(srv.URL))
	ts.now = func() time.Time { return tokenNow }
	return ts, s
}

const refreshedBody = `{"access_token":"fresh-access","refresh_token":"fresh-refresh","expires_in":1800,"token_type":"Bearer"}`

func TestAccessTokenReusesAValidToken(t *testing.T) {
	// Well inside the buffer, so no network call and no rewrite.
	ts, s := tokenSourceFixture(t, http.StatusOK, refreshedBody, schwab.Token{
		AccessToken:  "still-good",
		RefreshToken: "refresh-value",
		ExpiresAt:    tokenNow.Add(25 * time.Minute),
	})

	before, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got, err := ts.AccessToken(context.Background())
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if got != "still-good" {
		t.Errorf("token = %q, want the stored one", got)
	}

	after, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if after.Schwab != before.Schwab {
		t.Error("a valid token was rewritten unnecessarily")
	}
}

func TestAccessTokenRefreshesInsideTheBufferAndPersists(t *testing.T) {
	// Not yet expired, but inside the 5-minute buffer.
	ts, s := tokenSourceFixture(t, http.StatusOK, refreshedBody, schwab.Token{
		AccessToken:  "nearly-expired",
		RefreshToken: "refresh-value",
		ExpiresAt:    tokenNow.Add(2 * time.Minute),
	})

	got, err := ts.AccessToken(context.Background())
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if got != "fresh-access" {
		t.Errorf("token = %q, want the refreshed one", got)
	}

	saved, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if saved.Schwab.AccessToken != "fresh-access" {
		t.Errorf("saved access token = %q", saved.Schwab.AccessToken)
	}
	if saved.Schwab.RefreshToken != "fresh-refresh" {
		t.Errorf("saved refresh token = %q, want the rotated value persisted", saved.Schwab.RefreshToken)
	}
	if saved.Schwab.ExpiresAt.IsZero() {
		t.Error("expiry was not persisted")
	}
}

func TestAccessTokenClearsDeadTokensButKeepsTheRegistration(t *testing.T) {
	ts, s := tokenSourceFixture(t, http.StatusBadRequest, `{"error":"invalid_grant"}`, schwab.Token{
		AccessToken:  "expired",
		RefreshToken: "dead-refresh",
		ExpiresAt:    tokenNow.Add(-time.Hour),
	})

	_, err := ts.AccessToken(context.Background())
	if !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("error = %v, want ErrReauthRequired", err)
	}

	saved, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if saved.Schwab.HasToken() {
		t.Error("a rejected refresh token was left on disk")
	}
	if saved.Schwab.ClientID == "" || saved.Schwab.ClientSecret == "" {
		t.Error("the app registration was discarded along with the session")
	}
	if saved.Sheets.SpreadsheetID == "" {
		t.Error("the sheets configuration was discarded")
	}
}

func TestAccessTokenKeepsTokensOnATransientFailure(t *testing.T) {
	// A Schwab outage must not cost the user their refresh token.
	ts, s := tokenSourceFixture(t, http.StatusInternalServerError, `{"error":"server_error"}`, schwab.Token{
		AccessToken:  "expired",
		RefreshToken: "still-valid-refresh",
		ExpiresAt:    tokenNow.Add(-time.Hour),
	})

	_, err := ts.AccessToken(context.Background())
	if err == nil {
		t.Fatal("a 500 was treated as success")
	}
	if errors.Is(err, ErrReauthRequired) {
		t.Error("a 500 was treated as requiring re-auth")
	}

	saved, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if saved.Schwab.RefreshToken != "still-valid-refresh" {
		t.Errorf("refresh token = %q, want it preserved through an outage", saved.Schwab.RefreshToken)
	}
}

func TestAccessTokenWithNothingSaved(t *testing.T) {
	s := testStore(t)
	ts := NewTokenSource(s)

	_, err := ts.AccessToken(context.Background())
	if !errors.Is(err, ErrReauthRequired) {
		t.Errorf("error = %v, want ErrReauthRequired", err)
	}
}

func TestAccessTokenWithConfigButNoToken(t *testing.T) {
	s := testStore(t)
	creds := sampleCredentials()
	creds.Schwab.ClearToken()
	if err := s.Save(creds); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err := NewTokenSource(s).AccessToken(context.Background())
	if !errors.Is(err, ErrReauthRequired) {
		t.Errorf("error = %v, want ErrReauthRequired", err)
	}
}

func TestTokenSourceSatisfiesTheClientInterface(t *testing.T) {
	// Compile-time proof that the store can drive the API client.
	var _ interface {
		AccessToken(context.Context) (string, error)
	} = NewTokenSource(testStore(t))
}

func TestRefreshedTokenIsUsableByTheAPIClient(t *testing.T) {
	// End to end within the package boundary: store to token source to client.
	ts, _ := tokenSourceFixture(t, http.StatusOK, refreshedBody, schwab.Token{
		RefreshToken: "refresh-value",
		ExpiresAt:    tokenNow.Add(-time.Hour),
	})

	var gotAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"accountNumber": "15011495", "hashValue": "HASH"},
		})
	}))
	t.Cleanup(api.Close)

	client := schwab.NewClient(ts, schwab.WithBaseURL(api.URL+"/"))

	accounts, err := client.AccountNumbers(context.Background())
	if err != nil {
		t.Fatalf("AccountNumbers: %v", err)
	}
	if len(accounts) != 1 || accounts[0].HashValue != "HASH" {
		t.Errorf("accounts = %+v", accounts)
	}
	if gotAuth != "Bearer fresh-access" {
		t.Errorf("Authorization = %q, want the refreshed token", gotAuth)
	}
}
