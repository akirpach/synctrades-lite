package schwab

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// expiryBuffer is how long before actual expiry a token is treated as expired.
// Ported from the SaaS backend: it stops a token from expiring mid-request.
const expiryBuffer = 5 * time.Minute

// maxErrorBody caps how much of a failed response we read back into an error.
const maxErrorBody = 8 << 10

// ErrReauthRequired means the refresh token is no longer usable and the user
// must walk the browser paste-back flow again. Callers holding stored
// credentials should delete them when they see this, matching the SaaS
// backend's behavior of clearing tokens on a 400 or 401 from refresh.
var ErrReauthRequired = errors.New("schwab refresh token is no longer valid; re-authorization required")

// Token is a Schwab credential set with expiry resolved to an absolute time.
//
// Schwab reports lifetime as a relative expires_in, which is useless once
// persisted; storing the deadline instead means a token read back from disk
// can be judged without knowing when it was written.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// NeedsRefresh reports whether the access token is expired or close enough to
// expiry that it should be refreshed before use.
func (t Token) NeedsRefresh(now time.Time) bool {
	if t.AccessToken == "" {
		return true
	}
	return !now.Before(t.ExpiresAt.Add(-expiryBuffer))
}

// tokenResponse mirrors the JSON returned by Schwab's token endpoint.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// TokenClient performs the OAuth token-endpoint calls: exchanging an
// authorization code, and refreshing an expiring access token.
type TokenClient struct {
	cfg      Config
	http     *http.Client
	endpoint string
	now      func() time.Time
}

// NewTokenClient returns a TokenClient for the given user credentials.
func NewTokenClient(cfg Config) *TokenClient {
	return &TokenClient{
		cfg:      cfg,
		http:     &http.Client{Timeout: 30 * time.Second},
		endpoint: TokenEndpoint,
		now:      time.Now,
	}
}

// Exchange trades an authorization code for a token pair.
//
// redirect_uri is sent again here and must byte-match both the app
// registration and the value used to build the authorize URL. A failure is
// reported as an APIError rather than ErrReauthRequired: the remedy is to
// re-run the authorize step, since codes are single-use and short-lived.
func (c *TokenClient) Exchange(ctx context.Context, code string) (Token, error) {
	if err := c.cfg.Validate(); err != nil {
		return Token{}, err
	}
	if code == "" {
		return Token{}, errors.New("authorization code is empty")
	}

	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {c.cfg.RedirectURI},
	}

	resp, err := c.post(ctx, "token exchange", form)
	if err != nil {
		return Token{}, err
	}
	return c.toToken(resp, ""), nil
}

// Refresh obtains a new access token from a refresh token.
//
// A 400 or 401 here means the refresh token itself is dead, so the error wraps
// ErrReauthRequired and the caller should discard stored credentials. Other
// failures are transient and leave stored credentials alone.
func (c *TokenClient) Refresh(ctx context.Context, refreshToken string) (Token, error) {
	if err := c.cfg.Validate(); err != nil {
		return Token{}, err
	}
	if refreshToken == "" {
		return Token{}, fmt.Errorf("%w: no refresh token stored", ErrReauthRequired)
	}

	// Unlike the exchange, refresh takes no redirect_uri.
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}

	resp, err := c.post(ctx, "token refresh", form)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) &&
			(apiErr.StatusCode == http.StatusBadRequest || apiErr.StatusCode == http.StatusUnauthorized) {
			return Token{}, fmt.Errorf("%w (%s)", ErrReauthRequired, apiErr)
		}
		return Token{}, err
	}

	// Schwab may omit refresh_token when it is unchanged. Falling back to the
	// token we already hold avoids discarding a working credential and forcing
	// an unnecessary browser round trip.
	return c.toToken(resp, refreshToken), nil
}

func (c *TokenClient) toToken(r tokenResponse, fallbackRefresh string) Token {
	refresh := r.RefreshToken
	if refresh == "" {
		refresh = fallbackRefresh
	}
	return Token{
		AccessToken:  r.AccessToken,
		RefreshToken: refresh,
		ExpiresAt:    c.now().Add(time.Duration(r.ExpiresIn) * time.Second),
	}
}

func (c *TokenClient) post(ctx context.Context, op string, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("building %s request: %w", op, err)
	}

	// Schwab authenticates the token endpoint with HTTP Basic over the app
	// credentials, not with client_id/client_secret form fields.
	basic := base64.StdEncoding.EncodeToString(
		[]byte(c.cfg.ClientID + ":" + c.cfg.ClientSecret))
	req.Header.Set("Authorization", "Basic "+basic)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%s request failed: %w", op, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("reading %s response: %w", op, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return tokenResponse{}, classify(op, resp.StatusCode, body)
	}

	var parsed tokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return tokenResponse{}, fmt.Errorf("decoding %s response: %w", op, err)
	}
	if parsed.AccessToken == "" {
		return tokenResponse{}, fmt.Errorf("%s succeeded but returned no access token", op)
	}
	return parsed, nil
}
