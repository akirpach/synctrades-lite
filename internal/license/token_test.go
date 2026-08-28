package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// signForTest builds a token in the shape verify expects, signed with a
// throwaway keypair - never the real embedded key, whose private half this
// package never holds.
func signForTest(t *testing.T, priv ed25519.PrivateKey, p payload) string {
	t.Helper()
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	sig := ed25519.Sign(priv, body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestVerify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	issuedAt := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	p := payload{SessionID: "cs_test_abc123", Email: "buyer@example.com", IssuedAt: issuedAt.Format(time.RFC3339)}

	t.Run("valid token round-trips", func(t *testing.T) {
		token := signForTest(t, priv, p)

		claims, err := verify(pub, token)
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if claims.SessionID != p.SessionID {
			t.Errorf("SessionID = %q, want %q", claims.SessionID, p.SessionID)
		}
		if claims.Email != p.Email {
			t.Errorf("Email = %q, want %q", claims.Email, p.Email)
		}
		if !claims.IssuedAt.Equal(issuedAt) {
			t.Errorf("IssuedAt = %v, want %v", claims.IssuedAt, issuedAt)
		}
	})

	t.Run("signed with a different key is rejected", func(t *testing.T) {
		_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		token := signForTest(t, otherPriv, p)

		if _, err := verify(pub, token); !errors.Is(err, ErrInvalid) {
			t.Errorf("error = %v, want ErrInvalid", err)
		}
	})

	t.Run("tampered payload is rejected", func(t *testing.T) {
		token := signForTest(t, priv, p)
		payloadPart, sigPart, _ := strings.Cut(token, ".")

		body, err := base64.RawURLEncoding.DecodeString(payloadPart)
		if err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		var tampered payload
		if err := json.Unmarshal(body, &tampered); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		tampered.Email = "attacker@example.com"
		tamperedBody, err := json.Marshal(tampered)
		if err != nil {
			t.Fatalf("marshal tampered payload: %v", err)
		}
		tamperedToken := base64.RawURLEncoding.EncodeToString(tamperedBody) + "." + sigPart

		if _, err := verify(pub, tamperedToken); !errors.Is(err, ErrInvalid) {
			t.Errorf("error = %v, want ErrInvalid", err)
		}
	})

	t.Run("wrong shape is rejected", func(t *testing.T) {
		if _, err := verify(pub, "not-a-token"); !errors.Is(err, ErrInvalid) {
			t.Errorf("error = %v, want ErrInvalid", err)
		}
	})

	t.Run("empty token is rejected", func(t *testing.T) {
		if _, err := verify(pub, ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("error = %v, want ErrInvalid", err)
		}
	})
}
