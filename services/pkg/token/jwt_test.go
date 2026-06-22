package token

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"
)

func TestIssueVerifyViaJWKS(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	const kid = "auth-test-1"
	iss := NewIssuer(priv, kid, "https://auth.aizorix.com", "aizorix", 15*time.Minute)

	raw, err := iss.Issue(Claims{UserID: "u1", SessionID: "s1", Roles: []string{"freelancer"}, Permissions: []string{"project:create"}})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	// Publish the public key as JWKS, then parse it back the way a service/gateway would.
	jwks, err := MarshalJWKS(map[string]*ecdsa.PublicKey{kid: &priv.PublicKey})
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	doc, _ := json.Marshal(map[string]any{"keys": jwks})
	keys, err := ParseJWKS(doc)
	if err != nil {
		t.Fatalf("parse jwks: %v", err)
	}

	v := NewVerifier(keys, "https://auth.aizorix.com", "aizorix")
	claims, err := v.Verify(raw)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.UserID != "u1" || claims.SessionID != "s1" {
		t.Fatalf("unexpected claims: %+v", claims)
	}

	// Wrong audience must be rejected.
	if _, err := NewVerifier(keys, "https://auth.aizorix.com", "someone-else").Verify(raw); err == nil {
		t.Fatal("expected audience mismatch to fail")
	}
	// Tampered token must be rejected.
	if _, err := v.Verify(raw[:len(raw)-2] + "xx"); err == nil {
		t.Fatal("expected tampered token to fail")
	}
}
