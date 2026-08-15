// Package schwab implements the Schwab API integration: the OAuth paste-back
// flow, the token lifecycle, and the typed HTTP client for account and
// transaction data.
package schwab

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Schwab endpoints. These are fixed for every user; only the app credentials
// vary. Ported from the SaaS backend's SchwabApiClient.
const (
	AuthorizeEndpoint = "https://api.schwabapi.com/v1/oauth/authorize"
	TokenEndpoint     = "https://api.schwabapi.com/v1/oauth/token"
	APIBaseURL        = "https://api.schwabapi.com/trader/v1/"
)

var (
	// ErrNotAURL means the user pasted something that isn't a URL at all.
	ErrNotAURL = errors.New("pasted text is not a URL")
	// ErrNoCode means the URL parsed but carried no authorization code.
	ErrNoCode = errors.New("no authorization code in the pasted URL")
	// ErrStateMismatch means the URL is from a different authorization attempt.
	ErrStateMismatch = errors.New("state parameter does not match this session")
)

// AuthDenied reports that Schwab returned an error instead of an authorization
// code, which usually means the user declined consent or the app registration
// is misconfigured.
type AuthDenied struct {
	Code        string
	Description string
}

func (e *AuthDenied) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("schwab denied authorization: %s (%s)", e.Code, e.Description)
	}
	return fmt.Sprintf("schwab denied authorization: %s", e.Code)
}

// Config holds the Schwab app credentials. All three are supplied by the user
// from their own Schwab developer app registration and are never compiled into
// the binary.
type Config struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
}

// Validate reports whether the config is complete enough to start an
// authorization flow.
func (c Config) Validate() error {
	var missing []string
	if c.ClientID == "" {
		missing = append(missing, "client_id")
	}
	if c.ClientSecret == "" {
		missing = append(missing, "client_secret")
	}
	if c.RedirectURI == "" {
		missing = append(missing, "redirect_uri")
	}
	if len(missing) > 0 {
		return fmt.Errorf("schwab config incomplete: missing %s", strings.Join(missing, ", "))
	}
	return nil
}

// GenerateState returns a random value used to tie a pasted callback URL back
// to the authorization attempt that produced it.
func GenerateState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating oauth state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// AuthorizeURL builds the URL the user opens in their browser to grant access.
//
// Schwab exact-matches redirect_uri against the app registration here and again
// at code exchange, so the same value must be sent in both requests.
func (c Config) AuthorizeURL(state string) string {
	q := url.Values{
		"client_id":     {c.ClientID},
		"redirect_uri":  {c.RedirectURI},
		"response_type": {"code"},
		"state":         {state},
	}
	return AuthorizeEndpoint + "?" + q.Encode()
}

// ParseCallbackURL extracts the authorization code from the URL the user copies
// out of their browser's address bar after the callback fails to load.
//
// Nothing listens on the callback address, so the browser shows a connection
// error while retaining the full URL. Parsing it here is what replaces the
// redirect a server-based flow would receive.
//
// Percent-decoding is handled by net/url: Schwab codes commonly end in an "@"
// that appears as "%40" in the address bar, and decoding it by hand is a known
// way to produce a confusing 400 at exchange.
func ParseCallbackURL(pasted, expectedState string) (string, error) {
	if expectedState == "" {
		return "", errors.New("expected state is empty; generate one with GenerateState before authorizing")
	}

	// Terminals wrap long pastes and shells often leave surrounding quotes. A
	// valid URL contains no literal whitespace, so stripping it is safe.
	cleaned := strings.Join(strings.Fields(pasted), "")
	cleaned = strings.Trim(cleaned, `"'`)
	if cleaned == "" {
		return "", ErrNotAURL
	}

	u, err := url.Parse(cleaned)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotAURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", ErrNotAURL
	}

	q := u.Query()

	// Check this before state: when Schwab rejects the request its own error is
	// far more useful than a mismatch complaint.
	if errCode := q.Get("error"); errCode != "" {
		return "", &AuthDenied{Code: errCode, Description: q.Get("error_description")}
	}

	if got := q.Get("state"); got != expectedState {
		return "", fmt.Errorf("%w: this URL is from a different authorization attempt", ErrStateMismatch)
	}

	code := q.Get("code")
	if code == "" {
		return "", ErrNoCode
	}
	return code, nil
}
