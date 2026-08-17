package schwab

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

// capturedRequest records what the client actually put on the wire.
type capturedRequest struct {
	method string
	auth   string
	ctype  string
	form   url.Values
}

// newTestClient returns a TokenClient pointed at a stub token endpoint, plus a
// pointer to the request that endpoint received.
func newTestClient(t *testing.T, status int, body string) (*TokenClient, *capturedRequest) {
	t.Helper()

	got := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parsing request form: %v", err)
		}
		got.method = r.Method
		got.auth = r.Header.Get("Authorization")
		got.ctype = r.Header.Get("Content-Type")
		got.form = r.PostForm

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := NewTokenClient(testConfig())
	c.endpoint = srv.URL
	c.now = func() time.Time { return fixedNow }
	return c, got
}

const okTokenBody = `{
	"access_token": "access-abc",
	"refresh_token": "refresh-xyz",
	"expires_in": 1800,
	"token_type": "Bearer"
}`

func TestExchangeSendsCorrectRequest(t *testing.T) {
	c, got := newTestClient(t, http.StatusOK, okTokenBody)

	tok, err := c.Exchange(context.Background(), "C0.code123@")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}

	if got.method != http.MethodPost {
		t.Errorf("method = %s, want POST", got.method)
	}
	if got.ctype != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q", got.ctype)
	}

	// Schwab authenticates the token endpoint with Basic over the app
	// credentials, not with form fields.
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString(
		[]byte("test-client-id:test-client-secret"))
	if got.auth != wantAuth {
		t.Errorf("Authorization = %q, want %q", got.auth, wantAuth)
	}
	if got.form.Get("client_id") != "" || got.form.Get("client_secret") != "" {
		t.Error("credentials leaked into the form body; they belong in the Basic header")
	}

	if got.form.Get("grant_type") != "authorization_code" {
		t.Errorf("grant_type = %q", got.form.Get("grant_type"))
	}
	if got.form.Get("code") != "C0.code123@" {
		t.Errorf("code = %q, want the decoded form", got.form.Get("code"))
	}
	if got.form.Get("redirect_uri") != testConfig().RedirectURI {
		t.Errorf("redirect_uri = %q, want %q", got.form.Get("redirect_uri"), testConfig().RedirectURI)
	}

	if tok.AccessToken != "access-abc" {
		t.Errorf("access token = %q", tok.AccessToken)
	}
	if tok.RefreshToken != "refresh-xyz" {
		t.Errorf("refresh token = %q", tok.RefreshToken)
	}
	if want := fixedNow.Add(1800 * time.Second); !tok.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", tok.ExpiresAt, want)
	}
}

func TestRefreshSendsCorrectRequest(t *testing.T) {
	c, got := newTestClient(t, http.StatusOK, okTokenBody)

	if _, err := c.Refresh(context.Background(), "old-refresh"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if got.form.Get("grant_type") != "refresh_token" {
		t.Errorf("grant_type = %q", got.form.Get("grant_type"))
	}
	if got.form.Get("refresh_token") != "old-refresh" {
		t.Errorf("refresh_token = %q", got.form.Get("refresh_token"))
	}
	// Refresh takes no redirect_uri; sending one is a way to get a 400.
	if got.form.Has("redirect_uri") {
		t.Error("refresh sent a redirect_uri; it takes none")
	}
}

func TestRefreshKeepsOldRefreshTokenWhenOmitted(t *testing.T) {
	// Schwab may return no refresh_token when it is unchanged. Overwriting the
	// stored one with "" would force a needless browser round trip.
	body := `{"access_token":"access-new","expires_in":1800,"token_type":"Bearer"}`
	c, _ := newTestClient(t, http.StatusOK, body)

	tok, err := c.Refresh(context.Background(), "still-good-refresh")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tok.RefreshToken != "still-good-refresh" {
		t.Errorf("refresh token = %q, want the previous one preserved", tok.RefreshToken)
	}
}

func TestRefreshRotatesRefreshTokenWhenReturned(t *testing.T) {
	c, _ := newTestClient(t, http.StatusOK, okTokenBody)

	tok, err := c.Refresh(context.Background(), "old-refresh")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tok.RefreshToken != "refresh-xyz" {
		t.Errorf("refresh token = %q, want the rotated value", tok.RefreshToken)
	}
}

func TestRefreshRequiresReauthOnDeadToken(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c, _ := newTestClient(t, status, `{"error":"invalid_grant"}`)

			_, err := c.Refresh(context.Background(), "dead-refresh")
			if !errors.Is(err, ErrReauthRequired) {
				t.Fatalf("error = %v, want ErrReauthRequired", err)
			}
		})
	}
}

func TestRefreshDoesNotRequireReauthOnServerError(t *testing.T) {
	// A 500 is transient. Treating it as re-auth would throw away a working
	// refresh token over a Schwab outage.
	c, _ := newTestClient(t, http.StatusInternalServerError, `{"error":"server_error"}`)

	_, err := c.Refresh(context.Background(), "good-refresh")
	if errors.Is(err, ErrReauthRequired) {
		t.Fatal("a 500 was treated as re-auth required")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d", apiErr.StatusCode)
	}
}

func TestRefreshWithNoStoredTokenRequiresReauth(t *testing.T) {
	c, _ := newTestClient(t, http.StatusOK, okTokenBody)

	if _, err := c.Refresh(context.Background(), ""); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("error = %v, want ErrReauthRequired", err)
	}
}

func TestExchangeFailureIsNotReauth(t *testing.T) {
	// A bad code means re-run authorize, which is a different remedy from a
	// dead refresh token.
	c, _ := newTestClient(t, http.StatusBadRequest, `{"error":"invalid_grant"}`)

	_, err := c.Exchange(context.Background(), "expired-code")
	if errors.Is(err, ErrReauthRequired) {
		t.Error("exchange failure reported as ErrReauthRequired")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
}

func TestPostRejectsSuccessWithoutAccessToken(t *testing.T) {
	c, _ := newTestClient(t, http.StatusOK, `{"expires_in":1800}`)

	if _, err := c.Exchange(context.Background(), "code"); err == nil {
		t.Fatal("a 200 with no access token was accepted")
	}
}

func TestExchangeRejectsEmptyInputs(t *testing.T) {
	c, _ := newTestClient(t, http.StatusOK, okTokenBody)

	if _, err := c.Exchange(context.Background(), ""); err == nil {
		t.Error("empty authorization code accepted")
	}

	incomplete := NewTokenClient(Config{ClientID: "only-an-id"})
	if _, err := incomplete.Exchange(context.Background(), "code"); err == nil {
		t.Error("incomplete config accepted")
	}
}

func TestNeedsRefresh(t *testing.T) {
	tests := []struct {
		name  string
		token Token
		want  bool
	}{
		{
			name:  "fresh",
			token: Token{AccessToken: "a", ExpiresAt: fixedNow.Add(30 * time.Minute)},
			want:  false,
		},
		{
			name:  "just outside the buffer",
			token: Token{AccessToken: "a", ExpiresAt: fixedNow.Add(expiryBuffer + time.Second)},
			want:  false,
		},
		{
			name:  "exactly at the buffer",
			token: Token{AccessToken: "a", ExpiresAt: fixedNow.Add(expiryBuffer)},
			want:  true,
		},
		{
			name:  "inside the buffer but not yet expired",
			token: Token{AccessToken: "a", ExpiresAt: fixedNow.Add(time.Minute)},
			want:  true,
		},
		{
			name:  "expired",
			token: Token{AccessToken: "a", ExpiresAt: fixedNow.Add(-time.Minute)},
			want:  true,
		},
		{
			name:  "no access token",
			token: Token{ExpiresAt: fixedNow.Add(time.Hour)},
			want:  true,
		},
		{
			name:  "zero value",
			token: Token{},
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.token.NeedsRefresh(fixedNow); got != tt.want {
				t.Errorf("NeedsRefresh = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExchangeHonorsContextCancellation(t *testing.T) {
	c, _ := newTestClient(t, http.StatusOK, okTokenBody)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.Exchange(ctx, "code"); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}
