package token

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testKID      = "auth-exp-1"
	testIssuer   = "https://auth.aizorix.com"
	testAudience = "aizorix"
)

// signWith mints a token directly (bypassing Issuer.Issue) so the test controls the
// temporal claims (iat/nbf/exp) that Issue always pins to time.Now().
func signWith(t *testing.T, priv *ecdsa.PrivateKey, rc jwt.RegisteredClaims) string {
	t.Helper()
	c := Claims{RegisteredClaims: rc, UserID: "u1", SessionID: "s1"}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, c)
	tok.Header["kid"] = testKID
	raw, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return raw
}

func newVerifier(t *testing.T, priv *ecdsa.PrivateKey) *Verifier {
	t.Helper()
	return NewVerifier(map[string]*ecdsa.PublicKey{testKID: &priv.PublicKey}, testIssuer, testAudience)
}

func baseClaims() jwt.RegisteredClaims {
	now := time.Now()
	return jwt.RegisteredClaims{
		Issuer:    testIssuer,
		Audience:  jwt.ClaimStrings{testAudience},
		Subject:   "u1",
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)),
		ID:        "jti-1",
	}
}

func keypair(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	return priv
}

func TestVerifyValidToken(t *testing.T) {
	priv := keypair(t)
	raw := signWith(t, priv, baseClaims())
	claims, err := newVerifier(t, priv).Verify(raw)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if claims.UserID != "u1" {
		t.Fatalf("unexpected uid %q", claims.UserID)
	}
}

func TestVerifyExpiredToken(t *testing.T) {
	priv := keypair(t)
	rc := baseClaims()
	// Issued an hour ago, expired 45 minutes ago.
	past := time.Now().Add(-time.Hour)
	rc.IssuedAt = jwt.NewNumericDate(past)
	rc.NotBefore = jwt.NewNumericDate(past)
	rc.ExpiresAt = jwt.NewNumericDate(past.Add(15 * time.Minute))
	raw := signWith(t, priv, rc)

	_, err := newVerifier(t, priv).Verify(raw)
	if err == nil {
		t.Fatal("expected expired token to be rejected")
	}
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken wrapper, got %v", err)
	}
	if !errors.Is(err, jwt.ErrTokenExpired) {
		t.Fatalf("expected jwt.ErrTokenExpired, got %v", err)
	}
}

func TestVerifyNotYetValidToken(t *testing.T) {
	priv := keypair(t)
	rc := baseClaims()
	// nbf is 10 minutes in the future: token must not be accepted yet.
	future := time.Now().Add(10 * time.Minute)
	rc.NotBefore = jwt.NewNumericDate(future)
	rc.ExpiresAt = jwt.NewNumericDate(future.Add(15 * time.Minute))
	raw := signWith(t, priv, rc)

	_, err := newVerifier(t, priv).Verify(raw)
	if err == nil {
		t.Fatal("expected not-yet-valid (nbf) token to be rejected")
	}
	if !errors.Is(err, jwt.ErrTokenNotValidYet) {
		t.Fatalf("expected jwt.ErrTokenNotValidYet, got %v", err)
	}
}

// TestVerifyMissingExpiry asserts WithExpirationRequired: a token with no exp is rejected
// even though every other claim is valid.
func TestVerifyMissingExpiry(t *testing.T) {
	priv := keypair(t)
	rc := baseClaims()
	rc.ExpiresAt = nil
	raw := signWith(t, priv, rc)

	_, err := newVerifier(t, priv).Verify(raw)
	if err == nil {
		t.Fatal("expected token without exp to be rejected")
	}
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken wrapper, got %v", err)
	}
}

// TestVerifyExpiryBoundary checks the exp boundary: a token that expired one second ago is
// rejected, while one expiring shortly in the future is accepted.
func TestVerifyExpiryBoundary(t *testing.T) {
	priv := keypair(t)
	v := newVerifier(t, priv)

	rcExpired := baseClaims()
	rcExpired.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Second))
	if _, err := v.Verify(signWith(t, priv, rcExpired)); err == nil {
		t.Fatal("token expired 1s ago must be rejected")
	}

	rcLive := baseClaims()
	rcLive.ExpiresAt = jwt.NewNumericDate(time.Now().Add(30 * time.Second))
	if _, err := v.Verify(signWith(t, priv, rcLive)); err != nil {
		t.Fatalf("token expiring in 30s must be accepted, got %v", err)
	}
}
