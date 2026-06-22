package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aizorix/platform/auth/internal/service"
)

func findCookie(res *http.Response, name string) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// TestRefreshCookieAttributes locks in the fix for the bug that made the SPA unusable in a
// browser over HTTP (login succeeded at the API level but the cookie-gated middleware bounced
// every protected route): the refresh cookie must be Path "/" (so it reaches page routes AND
// the proxied API), HttpOnly, and Secure ONLY when configured for production/HTTPS — because
// browsers silently refuse to store Secure cookies over http://.
func TestRefreshCookieAttributes(t *testing.T) {
	toks := &service.Tokens{AccessToken: "a", RefreshToken: "r-value", AccessExpiresIn: 900, UserID: "u1"}

	// Local HTTP dev: cookieSecure=false.
	api := New(nil)
	api.SetCookieSecure(false)
	rr := httptest.NewRecorder()
	api.writeTokens(rr, http.StatusOK, toks)

	c := findCookie(rr.Result(), refreshCookieName)
	if c == nil {
		t.Fatal("refresh cookie was not set on the token response")
	}
	if c.Value != "r-value" {
		t.Fatalf("cookie value = %q, want the refresh token", c.Value)
	}
	if c.Path != "/" {
		t.Fatalf("cookie path = %q, want / (the bug used /v1/auth, which never reaches page routes)", c.Path)
	}
	if !c.HttpOnly {
		t.Fatal("refresh cookie must be HttpOnly")
	}
	if c.Secure {
		t.Fatal("Secure must be FALSE for local HTTP dev (browsers drop Secure cookies over http://) — this was bug #9")
	}

	// Production/HTTPS: cookieSecure=true.
	api.SetCookieSecure(true)
	rr2 := httptest.NewRecorder()
	api.writeTokens(rr2, http.StatusOK, toks)
	c2 := findCookie(rr2.Result(), refreshCookieName)
	if c2 == nil || !c2.Secure {
		t.Fatal("Secure must be TRUE in production")
	}

	// Default must be secure-by-default (production-safe) before any SetCookieSecure call.
	if !New(nil).cookieSecure {
		t.Fatal("New() must default cookieSecure=true (secure by default)")
	}
}

// TestClearRefreshCookieMatchesPath ensures logout clears the cookie on the SAME path it was
// set, or the browser keeps a stale cookie.
func TestClearRefreshCookieMatchesPath(t *testing.T) {
	api := New(nil)
	api.SetCookieSecure(false)
	rr := httptest.NewRecorder()
	api.clearRefreshCookie(rr)
	c := findCookie(rr.Result(), refreshCookieName)
	if c == nil {
		t.Fatal("clear did not emit a cookie")
	}
	if c.Path != "/" {
		t.Fatalf("clear path = %q, want / (must match the set path)", c.Path)
	}
	if c.MaxAge >= 0 {
		t.Fatalf("clear MaxAge = %d, want negative (delete)", c.MaxAge)
	}
}
