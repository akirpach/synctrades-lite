// Package license verifies synctrades license tokens.
//
// A token is signed offline by the license server (a separate project, not
// part of this repo - see CLAUDE.md) at purchase time and never checked
// against a live service here: verification is a pure signature check
// against a public key compiled into this binary. There is no database, no
// revocation list, and no network call in this path at all.
package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// publicKeyHex authenticates every license token this binary will accept.
// Its matching private key lives only on the license server; it is not a
// secret itself; it's fine that this is public. Generated once - see
// CLAUDE.md for how to regenerate it if it's ever rotated.
const publicKeyHex = "895794dd5b4b27386a77b9e367fd06e23f951af85df09b37e06ed825793d2743"

// ErrInvalid means the token is malformed, was not signed by the license
// server's private key, or was tampered with after signing.
var ErrInvalid = errors.New("license key is not valid")

// Claims is what a verified token attests to.
type Claims struct {
	// SessionID is the Stripe Checkout Session ID the token was issued for.
	// Signing is deterministic from this value, so a lost key can always be
	// regenerated from the same session ID with nothing else stored.
	SessionID string
	Email     string
	IssuedAt  time.Time
}

type payload struct {
	SessionID string `json:"sid"`
	Email     string `json:"email"`
	IssuedAt  string `json:"iat"`
}

var publicKey = func() ed25519.PublicKey {
	b, err := hex.DecodeString(publicKeyHex)
	if err != nil || len(b) != ed25519.PublicKeySize {
		panic("license: publicKeyHex is not a valid ed25519 public key")
	}
	return ed25519.PublicKey(b)
}()

// Verify checks a token's signature and decodes its claims.
//
// A token is "<base64url payload json>.<base64url signature>".
func Verify(token string) (Claims, error) {
	return verify(publicKey, token)
}

// verify is Verify parameterized by public key, so tests can check the
// parsing and signature logic against a throwaway keypair without ever
// needing this package's real private key, which it never holds.
func verify(pub ed25519.PublicKey, token string) (Claims, error) {
	token = strings.TrimSpace(token)

	payloadPart, sigPart, ok := strings.Cut(token, ".")
	if !ok || payloadPart == "" || sigPart == "" {
		return Claims{}, fmt.Errorf("%w: wrong shape", ErrInvalid)
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigPart)
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	if !ed25519.Verify(pub, payloadBytes, sig) {
		return Claims{}, fmt.Errorf("%w: signature does not match", ErrInvalid)
	}

	var p payload
	if err := json.Unmarshal(payloadBytes, &p); err != nil {
		return Claims{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if p.SessionID == "" {
		return Claims{}, fmt.Errorf("%w: no session id in payload", ErrInvalid)
	}

	issuedAt, err := time.Parse(time.RFC3339, p.IssuedAt)
	if err != nil {
		return Claims{}, fmt.Errorf("%w: bad issued-at timestamp: %v", ErrInvalid, err)
	}

	return Claims{SessionID: p.SessionID, Email: p.Email, IssuedAt: issuedAt}, nil
}
