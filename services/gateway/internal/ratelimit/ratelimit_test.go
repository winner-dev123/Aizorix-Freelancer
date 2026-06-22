package ratelimit

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aizorix/platform/gateway/internal/auth"
)

func mustCIDRs(t *testing.T, cidrs ...string) []*net.IPNet {
	t.Helper()
	var out []*net.IPNet
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatalf("bad cidr %q: %v", c, err)
		}
		out = append(out, n)
	}
	return out
}

// TestClientIP_IgnoresForgedXFFByDefault locks in the G2 fix: with no trusted proxies
// configured, a client-supplied X-Forwarded-For must be ignored and the immediate peer
// (RemoteAddr) used — otherwise any client forges XFF to dodge its limit or lock out a victim IP.
func TestClientIP_IgnoresForgedXFFByDefault(t *testing.T) {
	l := &Limiter{} // no trusted proxies
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.7:44321"
	r.Header.Set("X-Forwarded-For", "1.2.3.4") // forged
	if got := l.clientIP(r); got != "203.0.113.7" {
		t.Fatalf("clientIP = %q, want RemoteAddr host 203.0.113.7 (forged XFF must be ignored)", got)
	}
}

// TestClientIP_HonorsXFFOnlyFromTrustedProxy: XFF is trusted only when the immediate peer is
// itself a configured trusted proxy; an untrusted peer's XFF is ignored.
func TestClientIP_HonorsXFFOnlyFromTrustedProxy(t *testing.T) {
	l := &Limiter{trustedProxies: mustCIDRs(t, "10.0.0.0/8")}

	trusted := httptest.NewRequest(http.MethodGet, "/", nil)
	trusted.RemoteAddr = "10.1.2.3:5000" // a trusted proxy hop
	trusted.Header.Set("X-Forwarded-For", "198.51.100.9, 10.1.2.3")
	if got := l.clientIP(trusted); got != "198.51.100.9" {
		t.Fatalf("trusted-proxy XFF: clientIP = %q, want left-most 198.51.100.9", got)
	}

	untrusted := httptest.NewRequest(http.MethodGet, "/", nil)
	untrusted.RemoteAddr = "203.0.113.50:5000" // NOT in the trusted CIDR
	untrusted.Header.Set("X-Forwarded-For", "198.51.100.9")
	if got := l.clientIP(untrusted); got != "203.0.113.50" {
		t.Fatalf("untrusted peer XFF: clientIP = %q, want RemoteAddr 203.0.113.50 (XFF ignored)", got)
	}
}

// TestClassify_KeysByUserWhenAuthedElseIP locks in the G1 fix: once auth has populated the
// context the limiter keys per user; pre-auth it keys per IP. Also checks the stricter auth
// bucket applies to /v1/auth paths.
func TestClassify_KeysByUserWhenAuthedElseIP(t *testing.T) {
	l := &Limiter{
		general: Bucket{Capacity: 100, Window: time.Minute},
		auth:    Bucket{Capacity: 10, Window: time.Minute},
	}

	// Authenticated request to a protected path -> user-keyed, general bucket.
	authed := httptest.NewRequest(http.MethodGet, "/v1/contracts/1", nil)
	authed.RemoteAddr = "203.0.113.7:1"
	authed = authed.WithContext(auth.ContextWithUserID(authed.Context(), "user-123"))
	if b, key := l.classify(authed); key != "rl:user:user-123" {
		t.Fatalf("authed key = %q, want rl:user:user-123", key)
	} else if b.Capacity != l.general.Capacity {
		t.Fatalf("protected non-auth path should use the general bucket, got cap %d", b.Capacity)
	}

	// Unauthenticated request to an auth path -> IP-keyed, stricter auth bucket.
	anon := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
	anon.RemoteAddr = "203.0.113.7:1"
	if b, key := l.classify(anon); key != "rl:ip:203.0.113.7" {
		t.Fatalf("anon key = %q, want rl:ip:203.0.113.7", key)
	} else if b.Capacity != l.auth.Capacity {
		t.Fatalf("/v1/auth path should use the stricter auth bucket, got cap %d", b.Capacity)
	}
}
