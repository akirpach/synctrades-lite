package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/akirpach/synctrades-lite/internal/schwab"
)

// TokenSource hands the Schwab client a valid access token, refreshing and
// persisting when the stored one is close to expiry.
//
// This is what closes the loop on the paste-back flow: after one browser login,
// every later run refreshes silently from disk. It satisfies
// schwab.TokenSource, so the API client stays unaware of storage entirely.
type TokenSource struct {
	store     *Store
	now       func() time.Time
	tokenOpts []schwab.TokenOption

	// One sync per run, but a mutex keeps two concurrent fetches from both
	// refreshing and racing each other's writes.
	mu sync.Mutex
}

// NewTokenSource returns a TokenSource backed by the given store. Any options
// are passed to the token client used for refreshes.
func NewTokenSource(s *Store, opts ...schwab.TokenOption) *TokenSource {
	return &TokenSource{store: s, now: time.Now, tokenOpts: opts}
}

// ErrReauthRequired is re-exported so callers can branch on it without also
// importing the schwab package.
var ErrReauthRequired = schwab.ErrReauthRequired

// AccessToken returns a usable Schwab access token.
//
// A refresh token Schwab has rejected is cleared from disk before returning,
// matching the SaaS backend's behavior. The app registration is deliberately
// kept: the user needs another browser login, not another round of configuring
// their client ID and secret.
func (t *TokenSource) AccessToken(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	creds, err := t.store.Load()
	if errors.Is(err, ErrNotConfigured) {
		return "", fmt.Errorf("%w: nothing saved yet, run `synctrades auth schwab`", ErrReauthRequired)
	}
	if err != nil {
		return "", err
	}

	if !creds.Schwab.HasToken() {
		return "", fmt.Errorf("%w: no refresh token saved, run `synctrades auth schwab`", ErrReauthRequired)
	}

	token := creds.Schwab.Token()
	if !token.NeedsRefresh(t.now()) {
		return token.AccessToken, nil
	}

	client := schwab.NewTokenClient(creds.Schwab.Config(), t.tokenOpts...)
	refreshed, err := client.Refresh(ctx, token.RefreshToken)
	if err != nil {
		if errors.Is(err, schwab.ErrReauthRequired) {
			// Discard the dead session so the next run fails fast with clear
			// advice instead of retrying a token Schwab will never accept.
			if clearErr := t.clearToken(); clearErr != nil {
				return "", errors.Join(err, clearErr)
			}
			return "", fmt.Errorf("%w: run `synctrades auth schwab` to sign in again", err)
		}
		// Transient: leave stored credentials alone. A Schwab outage must not
		// cost the user their refresh token.
		return "", err
	}

	creds.Schwab.SetToken(refreshed)
	if err := t.store.Save(creds); err != nil {
		// Fail rather than proceeding with an unsaved token. If Schwab rotated
		// the refresh token, the copy still on disk may already be void, and
		// carrying on would leave the user locked out with no warning. Refusing
		// here keeps the failure at the point it can still be understood.
		return "", fmt.Errorf("refreshed the token but could not save it to %s, "+
			"so the stored credentials may now be stale: %w", t.store.Path(), err)
	}
	return refreshed.AccessToken, nil
}

func (t *TokenSource) clearToken() error {
	return t.store.Update(func(c *Credentials) error {
		c.Schwab.ClearToken()
		return nil
	})
}
