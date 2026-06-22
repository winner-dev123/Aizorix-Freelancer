package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestJWKSEndpoint locks in the /.well-known/jwks.json endpoint. It was missing originally,
// which would have crashed the gateway's fail-fast JWKS bootstrap at startup.
func TestJWKSEndpoint(t *testing.T) {
	api := New(nil) // serveJWKS does not touch the service
	api.SetJWKS(`{"keys":[{"kty":"EC","crv":"P-256","kid":"k1","alg":"ES256"}]}`)
	srv := httptest.NewServer(api.Routes())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/.well-known/jwks.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"kid":"k1"`) {
		t.Fatalf("jwks not served correctly: %s", body)
	}
}

// TestJWKSEmptyWhenUnset ensures the endpoint always returns valid JSON (never 404/500) even
// before a key is configured, so a verifier polling it degrades gracefully.
func TestJWKSEmptyWhenUnset(t *testing.T) {
	srv := httptest.NewServer(New(nil).Routes())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/.well-known/jwks.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.TrimSpace(string(body)) != `{"keys":[]}` {
		t.Fatalf("expected empty keyset, got %q", body)
	}
}
