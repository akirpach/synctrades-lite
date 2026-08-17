package schwab

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func testConfig() Config {
	return Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		RedirectURI:  "https://127.0.0.1:5001/api/schwab/callback",
	}
}

func TestAuthorizeURLEncodesRedirectURI(t *testing.T) {
	cfg := testConfig()
	raw := cfg.AuthorizeURL("state-123")

	if !strings.HasPrefix(raw, AuthorizeEndpoint+"?") {
		t.Fatalf("authorize URL does not start with the authorize endpoint: %s", raw)
	}

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("authorize URL does not parse: %v", err)
	}

	q := u.Query()
	if got := q.Get("client_id"); got != cfg.ClientID {
		t.Errorf("client_id = %q, want %q", got, cfg.ClientID)
	}
	if got := q.Get("redirect_uri"); got != cfg.RedirectURI {
		t.Errorf("redirect_uri = %q, want %q", got, cfg.RedirectURI)
	}
	if got := q.Get("response_type"); got != "code" {
		t.Errorf("response_type = %q, want %q", got, "code")
	}
	if got := q.Get("state"); got != "state-123" {
		t.Errorf("state = %q, want %q", got, "state-123")
	}

	// Schwab matches redirect_uri exactly, so it must travel percent-encoded
	// rather than splitting the query string on its own "?" or "&".
	if !strings.Contains(raw, "redirect_uri=https%3A%2F%2F127.0.0.1%3A5001%2Fapi%2Fschwab%2Fcallback") {
		t.Errorf("redirect_uri is not percent-encoded in the raw URL: %s", raw)
	}

	// Schwab's authorize endpoint takes no scope parameter.
	if _, ok := q["scope"]; ok {
		t.Error("authorize URL carries a scope parameter; Schwab does not use one")
	}
}

func TestGenerateStateIsRandomAndSchwabSafe(t *testing.T) {
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		s, err := GenerateState()
		if err != nil {
			t.Fatalf("GenerateState: %v", err)
		}
		if seen[s] {
			t.Fatalf("GenerateState repeated a value: %q", s)
		}
		seen[s] = true

		if url.QueryEscape(s) != s {
			t.Errorf("state is not URL-safe: %q escapes to %q", s, url.QueryEscape(s))
		}

		// Kept alphanumeric and short by choice, not by a known Schwab
		// requirement. Enough entropy to be unguessable is the real constraint.
		if len(s) != 32 {
			t.Errorf("state is %d chars, want 32", len(s))
		}
	}
}

func TestParseCallbackURLRoundTrip(t *testing.T) {
	cfg := testConfig()
	state, err := GenerateState()
	if err != nil {
		t.Fatalf("GenerateState: %v", err)
	}

	// Build the authorize URL the user would open, then simulate the address
	// bar Schwab leaves behind after the callback fails to connect.
	authorize, err := url.Parse(cfg.AuthorizeURL(state))
	if err != nil {
		t.Fatalf("authorize URL does not parse: %v", err)
	}
	returnedState := authorize.Query().Get("state")

	callback := cfg.RedirectURI + "?code=C0.abc123%40&state=" + url.QueryEscape(returnedState)

	code, err := ParseCallbackURL(callback, state)
	if err != nil {
		t.Fatalf("ParseCallbackURL: %v", err)
	}
	if code != "C0.abc123@" {
		t.Errorf("code = %q, want %q", code, "C0.abc123@")
	}
}

// TestParseCallbackURLHandlesRealSchwabShape uses the actual parameter layout
// observed from a live Schwab callback: code first, then an undocumented
// session parameter, then state. The code is a placeholder of the same shape;
// the real one was single-use and is long expired.
func TestParseCallbackURLHandlesRealSchwabShape(t *testing.T) {
	pasted := "https://127.0.0.1:5001/api/schwab/callback" +
		"?code=C0.b2F1dGgyLmJkYy5zY2h3YWIuY29t.PLACEHOLDER_CODE_VALUE_HERE%40" +
		"&session=a65df551-47b8-4dcf-90e1-0a0fcdbcc62d" +
		"&state=pastebacktest"

	code, err := ParseCallbackURL(pasted, "pastebacktest")
	if err != nil {
		t.Fatalf("ParseCallbackURL: %v", err)
	}

	// The trailing %40 must arrive decoded, and the unexpected session
	// parameter must not interfere.
	if !strings.HasSuffix(code, "@") {
		t.Errorf("code does not end in a decoded @: %q", code)
	}
	if strings.Contains(code, "%40") {
		t.Errorf("code is still percent-encoded: %q", code)
	}
	if !strings.HasPrefix(code, "C0.") {
		t.Errorf("code = %q, want the C0. prefix preserved", code)
	}
}

func TestParseCallbackURLDecodesTrailingAt(t *testing.T) {
	// Schwab codes commonly end in "@", shown as "%40" in the address bar.
	// Handing the encoded form to the token endpoint yields an opaque 400.
	code, err := ParseCallbackURL(
		"https://127.0.0.1:5001/api/schwab/callback?code=abc%40&state=s1",
		"s1",
	)
	if err != nil {
		t.Fatalf("ParseCallbackURL: %v", err)
	}
	if code != "abc@" {
		t.Errorf("code = %q, want %q", code, "abc@")
	}
}

func TestParseCallbackURLTolerantOfPasteArtifacts(t *testing.T) {
	tests := []struct {
		name   string
		pasted string
	}{
		{"trailing newline", "https://127.0.0.1:5001/cb?code=abc&state=s1\n"},
		{"surrounding spaces", "  https://127.0.0.1:5001/cb?code=abc&state=s1  "},
		{"double quoted", `"https://127.0.0.1:5001/cb?code=abc&state=s1"`},
		{"single quoted", `'https://127.0.0.1:5001/cb?code=abc&state=s1'`},
		{"wrapped across lines", "https://127.0.0.1:5001/cb?code=abc\n&state=s1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, err := ParseCallbackURL(tt.pasted, "s1")
			if err != nil {
				t.Fatalf("ParseCallbackURL: %v", err)
			}
			if code != "abc" {
				t.Errorf("code = %q, want %q", code, "abc")
			}
		})
	}
}

func TestParseCallbackURLRejects(t *testing.T) {
	tests := []struct {
		name    string
		pasted  string
		state   string
		wantErr error
	}{
		{
			name:    "state mismatch",
			pasted:  "https://127.0.0.1:5001/cb?code=abc&state=other",
			state:   "s1",
			wantErr: ErrStateMismatch,
		},
		{
			name:    "missing state",
			pasted:  "https://127.0.0.1:5001/cb?code=abc",
			state:   "s1",
			wantErr: ErrStateMismatch,
		},
		{
			name:    "no code",
			pasted:  "https://127.0.0.1:5001/cb?state=s1",
			state:   "s1",
			wantErr: ErrNoCode,
		},
		{
			name:    "empty paste",
			pasted:  "   ",
			state:   "s1",
			wantErr: ErrNotAURL,
		},
		{
			name:    "bare code rather than the whole URL",
			pasted:  "C0.abc123@",
			state:   "s1",
			wantErr: ErrNotAURL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseCallbackURL(tt.pasted, tt.state)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseCallbackURLDiagnosesPastedAuthorizeURL(t *testing.T) {
	// The authorize URL carries the matching state and no code, so it reaches
	// the code check and must not read as a parser bug.
	cfg := testConfig()
	_, err := ParseCallbackURL(cfg.AuthorizeURL("s1"), "s1")

	if !errors.Is(err, ErrNoCode) {
		t.Fatalf("error = %v, want ErrNoCode", err)
	}
	if !strings.Contains(err.Error(), authorizeHost) {
		t.Errorf("error does not name the likely cause: %v", err)
	}
}

func TestParseCallbackURLListsParamsWhenCodeMissing(t *testing.T) {
	_, err := ParseCallbackURL("https://127.0.0.1:5001/cb?state=s1&session=abc", "s1")
	if !errors.Is(err, ErrNoCode) {
		t.Fatalf("error = %v, want ErrNoCode", err)
	}
	for _, want := range []string{"session", "state"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not list %q: %v", want, err)
		}
	}
}

func TestParseCallbackURLSurfacesSchwabError(t *testing.T) {
	_, err := ParseCallbackURL(
		"https://127.0.0.1:5001/cb?error=access_denied&error_description=User+declined&state=s1",
		"s1",
	)

	var denied *AuthDenied
	if !errors.As(err, &denied) {
		t.Fatalf("error = %v, want *AuthDenied", err)
	}
	if denied.Code != "access_denied" {
		t.Errorf("code = %q, want %q", denied.Code, "access_denied")
	}
	if denied.Description != "User declined" {
		t.Errorf("description = %q, want %q", denied.Description, "User declined")
	}
}

func TestParseCallbackURLPrefersSchwabErrorOverStateMismatch(t *testing.T) {
	// A denial carrying a stale state should report why Schwab refused rather
	// than complaining about the state.
	_, err := ParseCallbackURL(
		"https://127.0.0.1:5001/cb?error=access_denied&state=other",
		"s1",
	)

	var denied *AuthDenied
	if !errors.As(err, &denied) {
		t.Fatalf("error = %v, want *AuthDenied", err)
	}
}

func TestParseCallbackURLRequiresExpectedState(t *testing.T) {
	_, err := ParseCallbackURL("https://127.0.0.1:5001/cb?code=abc&state=s1", "")
	if err == nil {
		t.Fatal("expected an error when expectedState is empty")
	}
}

func TestConfigValidate(t *testing.T) {
	if err := testConfig().Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}

	err := Config{}.Validate()
	if err == nil {
		t.Fatal("empty config accepted")
	}
	for _, want := range []string{"client_id", "client_secret", "redirect_uri"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name the missing %s", err, want)
		}
	}
}
