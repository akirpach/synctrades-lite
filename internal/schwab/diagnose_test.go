package schwab

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// This file is a diagnostic, not a test of our behavior. It asks Schwab
// directly which credential is being rejected, because Schwab answers every
// distinct failure with the same useless {"error":"invalid_client"}.
//
//	$env:SYNCTRADES_DIAGNOSE  = "1"
//	$env:SCHWAB_CLIENT_ID     = "..."
//	$env:SCHWAB_CLIENT_SECRET = "..."
//	$env:SCHWAB_REDIRECT_URI  = "https://127.0.0.1:5001/api/schwab/callback"
//
//	go test ./internal/schwab -run TestDiagnoseSchwab -v -count=1
//
// No browser and no paste, so it runs fine under plain `go test`.
//
// It works by probing the two endpoints separately. The authorize endpoint sees
// client_id and redirect_uri but never the secret; the token endpoint sees
// client_id and secret but never redirect_uri. Crossing those two results
// isolates which of the three values is at fault.

const bogusRefreshToken = "deliberately-invalid-refresh-token"

func noRedirectClient() *http.Client {
	return &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func snippet(b []byte, n int) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// probeAuthorize reports whether Schwab accepts client_id plus redirect_uri.
func probeAuthorize(ctx context.Context, cfg Config, state string) (accepted bool, detail string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.AuthorizeURL(state), nil)
	if err != nil {
		return false, "building request: " + err.Error()
	}

	resp, err := noRedirectClient().Do(req)
	if err != nil {
		return false, "request failed: " + err.Error()
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	where := resp.Header.Get("Location")

	switch {
	case strings.Contains(string(body), "invalid_client"):
		return false, fmt.Sprintf("HTTP %d invalid_client", resp.StatusCode)
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return true, fmt.Sprintf("HTTP %d redirect to %s", resp.StatusCode, firstN(where, 60))
	case resp.StatusCode == http.StatusOK:
		return true, fmt.Sprintf("HTTP 200, %d bytes (login page)", len(body))
	default:
		return false, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, snippet(body, 200))
	}
}

// probeToken reports whether Schwab accepts the client_id and secret pair. A
// deliberately invalid refresh token is sent: rejection of the grant means the
// credentials themselves were accepted.
func probeToken(ctx context.Context, cfg Config) (accepted bool, detail string) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {bogusRefreshToken},
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false, "building request: " + err.Error()
	}
	basic := base64.StdEncoding.EncodeToString([]byte(cfg.ClientID + ":" + cfg.ClientSecret))
	req.Header.Set("Authorization", "Basic "+basic)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := noRedirectClient().Do(req)
	if err != nil {
		return false, "request failed: " + err.Error()
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	text := string(body)

	switch {
	case strings.Contains(text, "invalid_client"):
		return false, fmt.Sprintf("HTTP %d invalid_client", resp.StatusCode)
	case strings.Contains(text, "invalid_grant"),
		strings.Contains(text, "unsupported_token_type"),
		strings.Contains(text, "invalid_request"):
		return true, fmt.Sprintf("HTTP %d, grant rejected as expected: %s",
			resp.StatusCode, snippet(body, 120))
	case resp.StatusCode == http.StatusOK:
		return true, "HTTP 200 (unexpected success)"
	default:
		return false, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, snippet(body, 200))
	}
}

func firstN(s string, n int) string {
	if s == "" {
		return "(none)"
	}
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func TestDiagnoseSchwab(t *testing.T) {
	if os.Getenv("SYNCTRADES_DIAGNOSE") != "1" {
		t.Skip("set SYNCTRADES_DIAGNOSE=1 to probe Schwab directly")
	}

	cfg := Config{
		ClientID:     os.Getenv("SCHWAB_CLIENT_ID"),
		ClientSecret: os.Getenv("SCHWAB_CLIENT_SECRET"),
		RedirectURI:  os.Getenv("SCHWAB_REDIRECT_URI"),
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("%v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	t.Log("")
	t.Log("=== what this process is actually sending ===")
	t.Logf("client_id    : %s (%d chars)", cfg.ClientID, len(cfg.ClientID))
	t.Logf("client_secret: %d chars", len(cfg.ClientSecret))
	t.Logf("redirect_uri : [%s]", cfg.RedirectURI)

	// A hand-typed key losing a character produces the same invalid_client as
	// every other failure, so check the shape before trusting any probe below.
	// A hand-typed key that loses one character fails with the same
	// invalid_client as everything else, so check the shape before trusting any
	// probe below. Observed Schwab shapes: 32-char App Key, 16-char Secret.
	if n := len(cfg.ClientID); n != 32 {
		t.Logf("")
		t.Logf(">>> client_id is %d chars, expected 32. Almost certainly mistyped or", n)
		t.Logf(">>> truncated. Fix that before reading anything below: copy the App Key")
		t.Logf(">>> in the portal, then $env:SCHWAB_CLIENT_ID = (Get-Clipboard).Trim()")
	}
	if n := len(cfg.ClientSecret); n != 16 {
		t.Logf(">>> client_secret is %d chars, expected 16. Re-copy it too.", n)
	}

	t.Log("")
	t.Log("Compare client_id and redirect_uri against the Schwab portal now.")
	t.Log("If either differs, stop here: that is the bug.")

	// Vary only the state, to settle whether Schwab cares about its shape.
	states := []struct{ label, value string }{
		{"short alphanumeric", "pastebacktest"},
		{"32 hex (our current default)", strings.Repeat("ab", 16)},
		{"43 base64url with underscores", "yXjWZcIJxlbVCUg__v7iVIbKYGGRl_T4rTFjDalKIoU"},
		{"36-char GUID shape (what the SaaS app sends)", "a65df551-47b8-4dcf-90e1-0a0fcdbcc62d"},
	}

	t.Log("")
	t.Log("=== authorize endpoint: tests client_id + redirect_uri ===")
	authorizeOK := false
	for _, s := range states {
		ok, detail := probeAuthorize(ctx, cfg, s.value)
		if ok {
			authorizeOK = true
		}
		t.Logf("  %-45s %-8s %s", s.label, verdict(ok), detail)
	}

	t.Log("")
	t.Log("=== token endpoint: tests client_id + client_secret ===")
	tokenOK, tokenDetail := probeToken(ctx, cfg)
	t.Logf("  %-45s %-8s %s", "basic auth with a bogus grant", verdict(tokenOK), tokenDetail)

	// Also confirm the state generator is not emitting something odd.
	if s, err := GenerateState(); err == nil {
		if _, decErr := hex.DecodeString(s); decErr != nil {
			t.Errorf("GenerateState produced non-hex output: %q", s)
		}
	}

	t.Log("")
	t.Log("=== verdict ===")
	switch {
	case authorizeOK && tokenOK:
		t.Log("  Both endpoints accept these credentials.")
		t.Log("  The browser failure is then not about client_id, secret or redirect_uri.")
		t.Log("  Re-run the E2E flow in THIS window, where these same values are set.")
	case authorizeOK && !tokenOK:
		t.Log("  client_id and redirect_uri are CORRECT. The SECRET is wrong.")
		t.Log("  Re-copy the Secret from the Schwab portal, or regenerate it there.")
	case !authorizeOK && tokenOK:
		t.Log("  client_id and secret are CORRECT, so the REDIRECT_URI does not match")
		t.Log("  the app registration. Copy the portal's Callback URL exactly.")
	default:
		t.Log("  Neither endpoint accepts this client_id. The key itself is wrong,")
		t.Log("  belongs to a different app, or the app is not active.")
		t.Log("  Confirm the App Key on the app whose Callback URL you verified.")
	}
	t.Log("")
}

func verdict(ok bool) string {
	if ok {
		return "ACCEPTED"
	}
	return "REJECTED"
}
