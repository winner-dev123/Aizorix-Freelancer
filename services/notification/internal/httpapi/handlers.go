// Package httpapi is the REST transport for the notification service. The gateway
// authenticates callers and injects X-User-Id / X-User-Roles / X-Account-Type.
//
// The internal/dispatch endpoint mirrors the Kafka consumer's HandleEvent call so other
// services (and tests) can trigger fan-out synchronously; it reads the target user_id from
// the request body rather than the identity header.
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/aizorix/platform/notification/internal/service"
	"github.com/aizorix/platform/notification/internal/store"
	"github.com/go-chi/chi/v5"
)

type API struct {
	svc    *service.Service
	logger *slog.Logger
}

func New(svc *service.Service, logger *slog.Logger) *API { return &API{svc: svc, logger: logger} }

func (a *API) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Handle("/metrics", a.metrics())
	r.Route("/v1/notifications", func(r chi.Router) {
		r.Get("/", a.list)
		r.Post("/{id}/read", a.markRead)
		r.Post("/read-all", a.markAllRead)
		r.Get("/preferences", a.getPreferences)
		r.Put("/preferences", a.setPreference)
		r.Post("/internal/dispatch", a.dispatch)
	})
	return r
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-Id")
	unreadOnly := r.URL.Query().Get("unread") == "true"
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := a.svc.ListNotifications(r.Context(), userID, unreadOnly, limit)
	if err != nil {
		a.mapError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for i := range list {
		out = append(out, notificationDTO(&list[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": out})
}

func (a *API) markRead(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-Id")
	if err := a.svc.MarkRead(r.Context(), chi.URLParam(r, "id"), userID); err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (a *API) markAllRead(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-Id")
	n, err := a.svc.MarkAllRead(r.Context(), userID)
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": n})
}

func (a *API) getPreferences(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-Id")
	prefs, err := a.svc.GetPreferences(r.Context(), userID)
	if err != nil {
		a.mapError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(prefs))
	for _, p := range prefs {
		out = append(out, map[string]any{"event_type": p.EventType, "channel": p.Channel, "enabled": p.Enabled})
	}
	writeJSON(w, http.StatusOK, map[string]any{"preferences": out})
}

type setPrefReq struct {
	EventType string `json:"event_type"`
	Channel   string `json:"channel"`
	Enabled   bool   `json:"enabled"`
}

func (a *API) setPreference(w http.ResponseWriter, r *http.Request) {
	var req setPrefReq
	if !decode(w, r, &req) {
		return
	}
	userID := r.Header.Get("X-User-Id")
	if err := a.svc.SetPreference(r.Context(), userID, req.EventType, req.Channel, req.Enabled); err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

type dispatchReq struct {
	UserID    string         `json:"user_id"`
	EventType string         `json:"event_type"`
	Title     string         `json:"title"`
	Body      *string        `json:"body"`
	Data      map[string]any `json:"data"`
}

// dispatch mirrors the Kafka consumer's HandleEvent; the target user is taken from the body.
func (a *API) dispatch(w http.ResponseWriter, r *http.Request) {
	var req dispatchReq
	if !decode(w, r, &req) {
		return
	}
	if req.UserID == "" || req.EventType == "" {
		writeErr(w, http.StatusBadRequest, "INVALID_ARGUMENT", "user_id and event_type are required")
		return
	}
	id, err := a.svc.HandleEvent(r.Context(), req.UserID, req.EventType, req.Title, req.Body, req.Data)
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"notification_id": id})
}

func notificationDTO(n *store.Notification) map[string]any {
	return map[string]any{
		"id":         n.ID,
		"user_id":    n.UserID,
		"type":       n.Type,
		"title":      n.Title,
		"body":       n.Body,
		"data":       n.Data,
		"read_at":    n.ReadAt,
		"created_at": n.CreatedAt,
	}
}

func (a *API) metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# HELP notification_up\n# TYPE notification_up gauge\nnotification_up 1\n"))
	})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID_JSON", "could not parse request body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"code": code, "message": msg})
}

// mapError translates domain/store errors into HTTP responses.
func (a *API) mapError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "resource not found")
	default:
		a.logger.Error("request failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "internal error")
	}
}
