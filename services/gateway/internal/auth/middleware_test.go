package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aizorix/platform/pkg/token"
)

// TestStripTrustedHeaders_RemovesForgedIdentity locks in the gateway's core security property:
// a client can NEVER smuggle an identity to internal services (which trust these headers
// blindly). The gateway strips them from every inbound request — public and protected alike —
// before routing. This includes the X-User-* aliases, which are the real RBAC-bypass vector:
// the admin service reads X-User-Permissions, so leaving it unstripped = full admin takeover.
func TestStripTrustedHeaders_RemovesForgedIdentity(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/auth/login", nil)
	// Canonical names (screenshot reads X-Permissions)...
	r.Header.Set("X-User-Id", "attacker-spoofed-admin")
	r.Header.Set("X-Permissions", "*")
	r.Header.Set("X-Roles", "admin,finance_admin")
	r.Header.Set("X-Residency", "ZZ")
	// ...AND the aliases the other domain services read — the privilege-escalation vector.
	r.Header.Set("X-User-Permissions", "admin.dispute.resolve,admin.user.suspend,*")
	r.Header.Set("X-User-Roles", "admin")
	r.Header.Set("X-Account-Type", "client")
	r.Header.Set("Authorization", "Bearer real.jwt.here") // NOT a trusted header; must survive

	StripTrustedHeaders(r)

	for _, h := range trustedHeaders {
		if r.Header.Get(h) != "" {
			t.Fatalf("%s must be stripped (anti-spoof), got %q", h, r.Header.Get(h))
		}
	}
	// Belt-and-suspenders: explicitly assert the admin-RBAC-bypass header is gone.
	if r.Header.Get("X-User-Permissions") != "" {
		t.Fatal("X-User-Permissions must be stripped — it is the admin RBAC bypass vector")
	}
	if r.Header.Get("Authorization") == "" {
		t.Fatal("Authorization must NOT be stripped — the gateway needs it to authenticate")
	}
}

// TestInjectIdentity_SetsBothConventionsFromClaims asserts the gateway injects the verified
// identity under EVERY name a downstream service reads (canonical + X-User-* aliases +
// X-Account-Type), so no service is fed an empty identity, and that injection OVERWRITES any
// residual forged value (Set, not Add). Every name set here must also be in trustedHeaders.
func TestInjectIdentity_SetsBothConventionsFromClaims(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/contracts/x", nil)
	r.Header.Set("X-User-Permissions", "admin.*") // residual forgery; must be overwritten
	r.Header.Set("X-User-Id", "forged")

	injectIdentity(r, &token.Claims{
		UserID:           "u-real",
		Permissions:      []string{"contract:manage"},
		Roles:            []string{"client"},
		ResidencyCountry: "US",
		AccountType:      "client",
	})

	checks := map[string]string{
		"X-User-Id":          "u-real",
		"X-Permissions":      "contract:manage",
		"X-Roles":            "client",
		"X-Residency":        "US",
		"X-User-Permissions": "contract:manage", // alias the admin/contract services read
		"X-User-Roles":       "client",
		"X-Account-Type":     "client",
	}
	for h, want := range checks {
		if got := r.Header.Get(h); got != want {
			t.Fatalf("%s = %q, want %q (from verified claims)", h, got, want)
		}
		if vals := r.Header.Values(h); len(vals) != 1 {
			t.Fatalf("%s must be a single value (Set, not Add), got %v", h, vals)
		}
	}

	// Every injected name MUST be stripped on the way in, or it is spoofable.
	stripped := make(map[string]bool, len(trustedHeaders))
	for _, h := range trustedHeaders {
		stripped[http.CanonicalHeaderKey(h)] = true
	}
	for h := range checks {
		if !stripped[http.CanonicalHeaderKey(h)] {
			t.Fatalf("injected header %s is not in trustedHeaders — it would be spoofable", h)
		}
	}
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"Bearer abc", "abc", true},
		{"bearer abc", "abc", true},     // scheme is case-insensitive
		{"Bearer   abc  ", "abc", true}, // trims surrounding space
		{"abc", "", false},              // no scheme
		{"Bearer ", "", false},          // empty token
		{"", "", false},                 // no header
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if c.in != "" {
			r.Header.Set("Authorization", c.in)
		}
		got, ok := bearerToken(r)
		if ok != c.ok || got != c.want {
			t.Fatalf("bearerToken(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
