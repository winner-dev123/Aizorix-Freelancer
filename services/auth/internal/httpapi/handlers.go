// Package httpapi is the REST transport for the auth service. The same service methods
// are also exposed via gRPC (see cmd/server) for internal callers; this HTTP surface is
// what the gateway routes public traffic to.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/aizorix/platform/auth/internal/service"
	"github.com/go-chi/chi/v5"
)

type API struct {
	svc          *service.Service
	jwks         string // precomputed JWKS JSON served at /.well-known/jwks.json
	cookieSecure bool   // Secure flag on the refresh cookie (true in prod / HTTPS)
}

func New(svc *service.Service) *API { return &API{svc: svc, cookieSecure: true} }

// SetCookieSecure controls the Secure flag on the refresh cookie. It MUST be true in
// production (HTTPS), but MUST be false for local HTTP development: browsers silently refuse
// to store Secure cookies over http://, which breaks cookie-gated SPA navigation entirely.
func (a *API) SetCookieSecure(secure bool) { a.cookieSecure = secure }

// SetJWKS publishes the public signing keys so the gateway and every service can verify
// tokens locally. Called once at startup with the JSON form of the issuer's public key(s).
func (a *API) SetJWKS(jwksJSON string) { a.jwks = jwksJSON }

func (a *API) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/.well-known/jwks.json", a.serveJWKS)
	r.Route("/v1/auth", func(r chi.Router) {
		r.Post("/register", a.register)
		r.Post("/login", a.login)
		r.Post("/refresh", a.refresh)
		r.Post("/logout", a.logout)
		r.Get("/me", a.me)
		// Authenticated routes (gateway injects X-User-Id after verifying the JWT;
		// in a standalone deploy an auth middleware would populate it).
		r.Get("/sessions", a.listSessions)
		r.Post("/sessions/{id}/revoke", a.revokeSession)
	})
	return r
}

type registerReq struct {
	Email           string `json:"email"`
	Password        string `json:"password"`
	AccountType     string `json:"account_type"`
	Residency       string `json:"residency_country"`
	Locale          string `json:"locale"`
	AcceptedTerms   bool   `json:"accepted_terms"`
	AcceptedMonitor bool   `json:"accepted_monitoring_disclosure"`
}

func (a *API) register(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if !decode(w, r, &req) {
		return
	}
	if req.AccountType != "client" && req.AccountType != "freelancer" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "account_type must be client or freelancer")
		return
	}
	if len(req.Password) < 12 {
		writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "password must be at least 12 characters")
		return
	}
	if !req.AcceptedTerms {
		writeErr(w, http.StatusBadRequest, "TERMS_REQUIRED", "must accept terms of service")
		return
	}
	toks, err := a.svc.Register(r.Context(), req.Email, req.Password, req.AccountType, req.Residency, req.Locale, clientIP(r), r.UserAgent())
	if err != nil {
		mapError(w, err)
		return
	}
	a.writeTokens(w, http.StatusCreated, toks)
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if !decode(w, r, &req) {
		return
	}
	toks, err := a.svc.Login(r.Context(), req.Email, req.Password, clientIP(r), r.UserAgent())
	if err != nil {
		mapError(w, err)
		return
	}
	a.writeTokens(w, http.StatusOK, toks)
}

type refreshReq struct {
	RefreshToken string `json:"refresh_token"`
}

func (a *API) refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshReq
	// Body is optional: the refresh token may instead arrive in the httpOnly cookie.
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req)
	token := refreshFromRequest(r, req.RefreshToken)
	toks, err := a.svc.Refresh(r.Context(), token, clientIP(r), r.UserAgent())
	if err != nil {
		mapError(w, err)
		return
	}
	a.writeTokens(w, http.StatusOK, toks)
}

type logoutReq struct {
	RefreshToken string `json:"refresh_token"`
	AllSessions  bool   `json:"all_sessions"`
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	var req logoutReq
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req)
	token := refreshFromRequest(r, req.RefreshToken)
	if err := a.svc.Logout(r.Context(), token, req.AllSessions); err != nil {
		mapError(w, err)
		return
	}
	a.clearRefreshCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (a *API) serveJWKS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	if a.jwks == "" {
		_, _ = w.Write([]byte(`{"keys":[]}`))
		return
	}
	_, _ = w.Write([]byte(a.jwks))
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-Id")
	if userID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	id, err := a.svc.Me(r.Context(), userID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": id.UserID, "email": id.Email, "account_type": id.AccountType,
		"roles": id.Roles, "email_verified": id.EmailVerified, "mfa_enabled": id.MFAEnabled,
	})
}

func (a *API) listSessions(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-Id")
	if userID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	sessions, err := a.svc.ListSessions(r.Context(), userID)
	if err != nil {
		mapError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, map[string]any{
			"id": s.ID, "device_name": s.DeviceName, "ip": s.IP,
			"created_at": s.CreatedAt.Format(time.RFC3339), "last_active_at": s.LastActiveAt.Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (a *API) revokeSession(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-Id")
	if userID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	if err := a.svc.RevokeSession(r.Context(), userID, chi.URLParam(r, "id")); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// ── helpers ─────────────────────────────────────────────────────────────────

const refreshCookieName = "aizorix_refresh"

// writeTokens sets the rotating refresh token as an httpOnly cookie (so the SPA never holds
// it in JS) and also returns the token bundle in the body for non-browser clients (the
// desktop tracker stores the refresh token in the OS keychain).
func (a *API) writeTokens(w http.ResponseWriter, status int, t *service.Tokens) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Value:    t.RefreshToken,
		Path:     "/", // root path: sent to page routes (middleware guard) AND the proxied API
		HttpOnly: true,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   30 * 24 * 3600,
	})
	writeJSON(w, status, tokenResponse(t))
}

func (a *API) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: refreshCookieName, Value: "", Path: "/",
		HttpOnly: true, Secure: a.cookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// refreshFromRequest prefers an explicit body token (tracker/mobile) and falls back to the
// httpOnly cookie (browser).
func refreshFromRequest(r *http.Request, bodyToken string) string {
	if bodyToken != "" {
		return bodyToken
	}
	if c, err := r.Cookie(refreshCookieName); err == nil {
		return c.Value
	}
	return ""
}

func tokenResponse(t *service.Tokens) map[string]any {
	return map[string]any{
		"access_token": t.AccessToken, "refresh_token": t.RefreshToken,
		"access_expires_in": t.AccessExpiresIn, "token_type": "Bearer", "user_id": t.UserID,
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID_JSON", "could not parse request body")
		return false
	}
	return true
}

func mapError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidCredentials):
		writeErr(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "invalid email or password")
	case errors.Is(err, service.ErrAccountLocked):
		writeErr(w, http.StatusLocked, "ACCOUNT_LOCKED", "account temporarily locked")
	case errors.Is(err, service.ErrEmailTaken):
		writeErr(w, http.StatusConflict, "EMAIL_TAKEN", "email already registered")
	case errors.Is(err, service.ErrTokenReuse), errors.Is(err, service.ErrTokenExpired):
		writeErr(w, http.StatusUnauthorized, "INVALID_REFRESH", "session is no longer valid")
	default:
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "something went wrong")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"code": code, "message": msg})
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return xff
	}
	return r.RemoteAddr
}
