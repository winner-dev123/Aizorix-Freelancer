package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/aizorix/platform/pkg/token"
)

// ctxKey types the values the auth middleware stashes on the request context for
// downstream middleware (e.g. the rate limiter keys on the authenticated user id).
type ctxKey int

const (
	ctxUserID ctxKey = iota
)

// trustedHeaders are identity headers that ONLY the gateway is allowed to set.
// They are stripped from every inbound request before authentication so a client can
// never spoof an identity to internal services (which trust these blindly).
//
// This MUST be the full superset of every identity header any downstream service reads.
// Domain services (admin/user/project/proposal/contract/analytics) read the X-User-*
// aliases and X-Account-Type; if those are not stripped, a client can forge e.g.
// `X-User-Permissions: admin.*` and defeat RBAC. Both naming conventions are stripped here
// and re-injected from verified claims in injectIdentity, so the two sides cannot drift.
var trustedHeaders = []string{
	"X-User-Id",
	"X-Permissions",
	"X-Roles",
	"X-Residency",
	// Aliases the domain services read — equally spoofable if not stripped.
	"X-User-Permissions",
	"X-User-Roles",
	"X-Account-Type",
}

// StripTrustedHeaders removes any client-supplied identity headers. Call this on
// EVERY request (public and protected) before routing, so even unauthenticated
// traffic forwarded to public upstreams cannot smuggle a forged identity.
func StripTrustedHeaders(r *http.Request) {
	for _, h := range trustedHeaders {
		r.Header.Del(h)
	}
}

// Middleware authenticates protected requests. It expects a Bearer token in the
// Authorization header, verifies it, and injects trusted identity headers for the
// downstream service. Missing or invalid tokens yield 401.
//
// Client-supplied trusted headers must already have been stripped (see
// StripTrustedHeaders, applied gateway-wide) before this runs.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := bearerToken(r)
		if !ok {
			unauthorized(w, "missing bearer token")
			return
		}
		claims, err := v.Verify(raw)
		if err != nil {
			unauthorized(w, "invalid token")
			return
		}
		injectIdentity(r, claims)
		r = r.WithContext(ContextWithUserID(r.Context(), claims.UserID))
		next.ServeHTTP(w, r)
	})
}

// injectIdentity sets the trusted downstream identity headers from verified claims. It sets
// BOTH naming conventions (the canonical X-Permissions/X-Roles that screenshot reads, and the
// X-User-Permissions/X-User-Roles/X-Account-Type aliases the other domain services read) so no
// service is ever fed an empty identity — and, paired with the strip list above, so none can be
// spoofed. Every name written here must also appear in trustedHeaders.
func injectIdentity(r *http.Request, c *token.Claims) {
	perms := strings.Join(c.Permissions, ",")
	roles := strings.Join(c.Roles, ",")
	r.Header.Set("X-User-Id", c.UserID)
	r.Header.Set("X-Permissions", perms)
	r.Header.Set("X-Roles", roles)
	r.Header.Set("X-Residency", c.ResidencyCountry)
	r.Header.Set("X-User-Permissions", perms)
	r.Header.Set("X-User-Roles", roles)
	r.Header.Set("X-Account-Type", c.AccountType)
}

// ContextWithUserID returns a context carrying the authenticated user id — the value
// UserIDFromContext reads. Exported so other middleware (and tests) can establish the
// identity that the rate limiter and downstream handlers key on.
func ContextWithUserID(ctx context.Context, uid string) context.Context {
	return context.WithValue(ctx, ctxUserID, uid)
}

// UserIDFromContext returns the authenticated user id set by the middleware, if any.
func UserIDFromContext(ctx context.Context) (string, bool) {
	uid, ok := ctx.Value(ctxUserID).(string)
	return uid, ok && uid != ""
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	tok := strings.TrimSpace(h[len(prefix):])
	return tok, tok != ""
}

func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"code":"UNAUTHORIZED","message":"` + msg + `"}`))
}
