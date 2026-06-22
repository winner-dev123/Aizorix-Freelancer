// Package httpapi is the wsgateway's HTTP surface: the /ws upgrade endpoint, a presence query
// endpoint, and the operational /healthz + /metrics endpoints. The gateway authenticates the
// WebSocket upgrade itself (it terminates the socket; there is no downstream to delegate to).
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/aizorix/platform/wsgateway/internal/auth"
	"github.com/aizorix/platform/wsgateway/internal/config"
	"github.com/aizorix/platform/wsgateway/internal/hub"
	"github.com/gorilla/websocket"
)

var errNoToken = errors.New("wsgateway: no token presented")

// API wires the verifier, hub, and config into the HTTP handlers.
type API struct {
	cfg      config.Config
	verifier *auth.Verifier
	hub      *hub.Hub
	logger   *slog.Logger
	upgrader websocket.Upgrader
}

// New builds the API, configuring the WebSocket upgrader (buffer sizes + origin policy).
func New(cfg config.Config, verifier *auth.Verifier, h *hub.Hub, logger *slog.Logger) *API {
	a := &API{
		cfg:      cfg,
		verifier: verifier,
		hub:      h,
		logger:   logger,
	}
	a.upgrader = websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     a.checkOrigin,
	}
	return a
}

// Routes returns the HTTP handler. Uses net/http's ServeMux (no chi dependency needed here).
func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/metrics", a.metrics)
	mux.HandleFunc("/presence", a.presence)
	mux.HandleFunc("/ws", a.serveWS)
	return mux
}

// serveWS authenticates the request, upgrades it to a WebSocket, and hands the connection to
// the hub. Unauthenticated upgrades are rejected before the handshake completes.
func (a *API) serveWS(w http.ResponseWriter, r *http.Request) {
	claims, err := a.authenticate(r)
	if err != nil {
		http.Error(w, `{"code":"UNAUTHORIZED","message":"invalid or missing token"}`, http.StatusUnauthorized)
		return
	}
	ws, err := a.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an error response.
		a.logger.Debug("ws upgrade failed", "err", err)
		return
	}
	a.logger.Info("ws connected", "user_id", claims.UserID, "remote", r.RemoteAddr)
	// Run the connection for the lifetime of the process; cancel on graceful shutdown is
	// handled by the server's BaseContext (see cmd/server).
	a.hub.Serve(r.Context(), ws, claims.UserID)
}

// authenticate extracts the bearer token (Authorization header or ?token=) and verifies it
// against the JWKS-backed verifier.
func (a *API) authenticate(r *http.Request) (*claims, error) {
	raw := bearerToken(r)
	if raw == "" {
		return nil, errNoToken
	}
	c, err := a.verifier.Verify(raw)
	if err != nil {
		return nil, err
	}
	return &claims{UserID: c.UserID}, nil
}

type claims struct{ UserID string }

// bearerToken reads the token from the Authorization: Bearer header, falling back to the
// ?token= query param (browsers cannot set headers on the WebSocket handshake, so the query
// param is the standard escape hatch).
func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if strings.HasPrefix(h, "Bearer ") {
			return strings.TrimSpace(h[len("Bearer "):])
		}
	}
	return r.URL.Query().Get("token")
}

// checkOrigin enforces the configured allow-list (CSWSH defense). It default-DENIES: an empty
// WS_ALLOWED_ORIGINS list rejects every cross-origin upgrade. To intentionally allow all
// origins (dev/local) configure an explicit "*" entry.
func (a *API) checkOrigin(r *http.Request) bool {
	if len(a.cfg.AllowedOrigins) == 0 {
		return false
	}
	origin := r.Header.Get("Origin")
	for _, allowed := range a.cfg.AllowedOrigins {
		if allowed == "*" || origin == allowed {
			return true
		}
	}
	return false
}

// presence answers GET /presence?user_ids=a,b,c with {"presence":{"a":true,"b":false,...}}.
// It requires a valid bearer token: presence reveals who is online, so it must not be an
// open enumeration endpoint.
func (a *API) presence(w http.ResponseWriter, r *http.Request) {
	if _, err := a.authenticate(r); err != nil {
		http.Error(w, `{"code":"UNAUTHORIZED","message":"invalid or missing token"}`, http.StatusUnauthorized)
		return
	}
	ids := splitNonEmpty(r.URL.Query().Get("user_ids"))
	if len(ids) == 0 {
		http.Error(w, `{"code":"VALIDATION_FAILED","message":"user_ids query param required"}`, http.StatusBadRequest)
		return
	}
	online, err := a.hub.Online(r.Context(), ids)
	if err != nil {
		a.logger.Warn("presence query failed", "err", err)
		http.Error(w, `{"code":"INTERNAL","message":"presence lookup failed"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"presence": online})
}

func (a *API) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte("# HELP wsgateway_up Service liveness.\n# TYPE wsgateway_up gauge\nwsgateway_up 1\n"))
}

func splitNonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
