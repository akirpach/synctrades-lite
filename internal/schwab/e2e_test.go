package schwab

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// This file holds the end-to-end walk CLAUDE.md requires before any change to
// internal/schwab counts as working. It talks to the real Schwab API and needs
// a human to complete a browser login, so it is skipped unless explicitly
// enabled.
//
//	$env:SYNCTRADES_E2E      = "1"
//	$env:SCHWAB_CLIENT_ID    = "..."
//	$env:SCHWAB_CLIENT_SECRET = "..."
//	$env:SCHWAB_REDIRECT_URI = "https://127.0.0.1:5001/api/schwab/callback"
//
// The flow needs a URL pasted in mid-run, and `go test` wires the test
// binary's stdin to the null device, so it cannot be run that way. Compile the
// binary and run it directly, which gives it the real console:
//
//	go test -c -o schwab.test.exe ./internal/schwab
//	.\schwab.test.exe --% -test.run TestE2EAuthorizationFlow -test.v -test.timeout 20m
//
// The --% is PowerShell's stop-parsing token. Without it PowerShell splits
// -test.run at the dot and the binary receives a bare -test. On a POSIX shell
// drop the --%.
//
// Where even that is inconvenient (CI, a captured shell), set
// SCHWAB_CALLBACK_URL and SCHWAB_OAUTH_STATE instead and no paste is needed;
// see readCallbackURL.
//
// It is deliberately guarded at runtime rather than behind a build tag, so it
// still compiles and vets on every ordinary `go test ./...` and cannot rot
// unnoticed while nobody is running it.
//
// Stop the SaaS backend first. It owns 127.0.0.1:5001/api/schwab/callback, and
// if it is running it will consume the authorization code before you can copy
// it. This flow only works while the callback is broken.

func e2eConfig(t *testing.T) Config {
	t.Helper()

	if os.Getenv("SYNCTRADES_E2E") != "1" {
		t.Skip("set SYNCTRADES_E2E=1 to run the live Schwab flow")
	}

	cfg := Config{
		ClientID:     os.Getenv("SCHWAB_CLIENT_ID"),
		ClientSecret: os.Getenv("SCHWAB_CLIENT_SECRET"),
		RedirectURI:  os.Getenv("SCHWAB_REDIRECT_URI"),
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("%v (set SCHWAB_CLIENT_ID, SCHWAB_CLIENT_SECRET, SCHWAB_REDIRECT_URI)", err)
	}
	return cfg
}

// describe reports a credential's shape without putting the value itself into
// terminal scrollback or CI logs.
func describe(name, secret string) string {
	if secret == "" {
		return name + ": MISSING"
	}
	return fmt.Sprintf("%s: present, %d chars", name, len(secret))
}

// openBrowser launches the system browser at rawURL.
//
// This graduates to the CLI at build step 5, where opening the browser is part
// of `synctrades auth schwab`. On Windows it goes through FileProtocolHandler
// rather than `cmd /c start`, which mangles the & separators in a query string.
func openBrowser(rawURL string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL).Start()
	case "darwin":
		return exec.Command("open", rawURL).Start()
	default:
		return exec.Command("xdg-open", rawURL).Start()
	}
}

// obtainCode returns the authorization code from the URL the browser was left
// sitting on, re-prompting on an unusable paste rather than ending the run.
//
// SCHWAB_CALLBACK_URL short-circuits the interactive path. That escape hatch
// exists because `go test` points the test binary's stdin at the null device,
// so only a directly executed test binary ever gets a usable console.
func obtainCode(t *testing.T, cfg Config, state string) (string, error) {
	t.Helper()

	if fromEnv := strings.TrimSpace(os.Getenv("SCHWAB_CALLBACK_URL")); fromEnv != "" {
		t.Log("using the callback URL from SCHWAB_CALLBACK_URL")
		code, err := ParseCallbackURL(fromEnv, state)
		if err != nil {
			return "", fmt.Errorf("SCHWAB_CALLBACK_URL is not usable: %w", err)
		}
		return code, nil
	}

	authorizeURL := cfg.AuthorizeURL(state)

	t.Log("")
	t.Log("1. Nothing must be listening on the callback port. That is the design:")
	t.Log("   the redirect is MEANT to fail, and the failed page carries the code.")
	t.Log("2. Log in to Schwab in the browser window opening now, and approve")
	t.Log("   account access. If no window appears, open this by hand:")
	t.Log("")
	t.Log("   " + authorizeURL)
	t.Log("")

	if err := openBrowser(authorizeURL); err != nil {
		t.Logf("   (could not open a browser automatically: %v)", err)
	}

	t.Log("3. The callback page will FAIL TO LOAD. That is expected and required.")
	t.Log("4. Copy that failed page's ENTIRE address bar. It must begin with")
	t.Log("   " + cfg.RedirectURI)
	t.Log("   and contain code=. A URL on " + authorizeHost + " is the wrong one.")
	t.Log("5. Paste it here and press Enter.")
	t.Log("")
	t.Log("No rush: if the prompt is awkward, press Ctrl+C and re-run")
	t.Log("non-interactively with this run's state:")
	t.Log("")
	t.Logf(`   $env:SCHWAB_OAUTH_STATE  = %q`, state)
	t.Log(`   $env:SCHWAB_CALLBACK_URL = "<the failed page's URL>"`)
	t.Log("")

	const attempts = 5
	scanner := bufio.NewScanner(os.Stdin)
	for attempt := 1; attempt <= attempts; attempt++ {
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return "", fmt.Errorf("reading pasted URL: %w", err)
			}
			break // stdin closed; fall through to the guidance below
		}

		code, err := ParseCallbackURL(scanner.Text(), state)
		if err == nil {
			return code, nil
		}

		t.Logf("that paste is not usable: %v", err)
		if attempt == attempts {
			return "", fmt.Errorf("gave up after %d attempts", attempts)
		}
		t.Logf("attempt %d of %d - paste the URL again:", attempt+1, attempts)
	}

	return "", fmt.Errorf(`stdin is not readable, so there is nowhere to paste the URL.

This is what "go test" does: it points the test binary's stdin at the null
device. Either compile the binary and run it directly, which gives it the real
console:

    go test -c -o schwab.test.exe ./internal/schwab
    .\schwab.test.exe --%% -test.run TestE2EAuthorizationFlow -test.v -test.timeout 20m

PowerShell needs that --%% stop-parsing token, or it splits -test.run at the
dot and the binary sees a bare -test. On a POSIX shell, drop it.

Alternatively finish the browser step with the URL above and re-run with both:

    $env:SCHWAB_OAUTH_STATE  = %q
    $env:SCHWAB_CALLBACK_URL = "<the URL from your address bar>"`, state)
}

func TestE2EAuthorizationFlow(t *testing.T) {
	cfg := e2eConfig(t)

	// A caller supplying a callback URL out of band must supply the state that
	// produced it, otherwise ParseCallbackURL correctly rejects the pair.
	state := os.Getenv("SCHWAB_OAUTH_STATE")
	if state == "" {
		var err error
		if state, err = GenerateState(); err != nil {
			t.Fatalf("GenerateState: %v", err)
		}
	}

	// Step one: does the parser survive a real Schwab callback URL? Every other
	// test in this package uses a URL I wrote myself.
	code, err := obtainCode(t, cfg, state)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("parsed authorization code: %d chars, ends with %q",
		len(code), lastRune(code))

	// Step two: does Schwab accept our exchange request as constructed?
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tok, err := NewTokenClient(cfg).Exchange(ctx, code)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}

	t.Log("exchange succeeded")
	t.Log("  " + describe("access_token", tok.AccessToken))
	t.Log("  " + describe("refresh_token", tok.RefreshToken))
	t.Logf("  expires_at: %s (in %s)",
		tok.ExpiresAt.Format(time.RFC3339), time.Until(tok.ExpiresAt).Round(time.Second))

	if tok.AccessToken == "" {
		t.Error("no access token returned")
	}
	if tok.RefreshToken == "" {
		t.Error("no refresh token returned; refresh will be impossible")
	}
	if tok.NeedsRefresh(time.Now()) {
		t.Errorf("a freshly issued token already reports NeedsRefresh; "+
			"expires_at=%s buffer=%s", tok.ExpiresAt, expiryBuffer)
	}

	// Step three: exercise refresh now rather than discovering it is broken
	// when the first access token expires in the field.
	refreshed, err := NewTokenClient(cfg).Refresh(ctx, tok.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	t.Log("refresh succeeded")
	t.Log("  " + describe("access_token", refreshed.AccessToken))
	t.Log("  " + describe("refresh_token", refreshed.RefreshToken))
	t.Logf("  expires_at: %s", refreshed.ExpiresAt.Format(time.RFC3339))

	if refreshed.AccessToken == "" {
		t.Error("refresh returned no access token")
	}
	if refreshed.RefreshToken == "" {
		t.Error("refresh left no usable refresh token")
	}

	// Whether Schwab rotates the refresh token decides if the fallback in
	// Refresh is load-bearing or merely defensive. Worth knowing either way.
	if refreshed.RefreshToken == tok.RefreshToken {
		t.Log("note: Schwab returned the same refresh token (or omitted it)")
	} else {
		t.Log("note: Schwab rotated the refresh token; stored credentials must be updated after every refresh")
	}
}

func lastRune(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	return string(r[len(r)-1])
}

// TestE2EGuardIsOff is a cheap check that the live test really does stay out of
// the way of an ordinary `go test ./...`.
func TestE2EGuardIsOff(t *testing.T) {
	if os.Getenv("SYNCTRADES_E2E") == "1" {
		t.Skip("live E2E is enabled for this run")
	}
	if strings.TrimSpace(os.Getenv("SCHWAB_CLIENT_SECRET")) != "" {
		t.Log("warning: SCHWAB_CLIENT_SECRET is set in this environment")
	}
}
