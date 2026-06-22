// Package httpapi is the REST transport for the messaging service. The gateway
// authenticates callers and injects X-User-Id / X-User-Roles / X-Account-Type.
//
// NOTE: this service is REST-only; real-time WebSocket delivery lives in a separate gateway
// that consumes the message.sent events this service emits.
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/aizorix/platform/messaging/internal/service"
	"github.com/aizorix/platform/messaging/internal/store"
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
	r.Route("/v1/messaging", func(r chi.Router) {
		r.Post("/conversations", a.createConversation)
		r.Get("/conversations", a.listConversations)
		r.Get("/conversations/{id}/membership", a.conversationMembership)
		r.Get("/conversations/{id}/messages", a.listMessages)
		r.Post("/conversations/{id}/messages", a.postMessage)
		r.Post("/conversations/{id}/read", a.markRead)
		r.Post("/messages/{id}/attachments", a.addAttachment)
	})
	return r
}

type createConvReq struct {
	ContractID   *string  `json:"contract_id"`
	ProjectID    *string  `json:"project_id"`
	Subject      *string  `json:"subject"`
	Participants []string `json:"participants"`
}

func (a *API) createConversation(w http.ResponseWriter, r *http.Request) {
	var req createConvReq
	if !decode(w, r, &req) {
		return
	}
	creator := r.Header.Get("X-User-Id")
	conv, err := a.svc.CreateConversation(r.Context(), creator, req.ContractID, req.ProjectID, req.Subject, req.Participants)
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, conversationDTO(conv))
}

func (a *API) listConversations(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-Id")
	list, err := a.svc.ListConversations(r.Context(), userID)
	if err != nil {
		a.mapError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for i := range list {
		out = append(out, conversationDTO(&list[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": out})
}

// conversationMembership reports whether the X-User-Id caller is a participant in the
// conversation. It is an internal authorization probe used by the wsgateway to gate a
// WebSocket "join" before subscribing the connection to live conversation events.
func (a *API) conversationMembership(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-Id")
	if userID == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "X-User-Id required")
		return
	}
	member, err := a.svc.IsParticipant(r.Context(), chi.URLParam(r, "id"), userID)
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"member": member})
}

func (a *API) listMessages(w http.ResponseWriter, r *http.Request) {
	requester := r.Header.Get("X-User-Id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	var before *time.Time
	if v := r.URL.Query().Get("before"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			before = &t
		}
	}
	list, err := a.svc.ListMessages(r.Context(), chi.URLParam(r, "id"), requester, limit, before)
	if err != nil {
		a.mapError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for i := range list {
		out = append(out, messageDTO(&list[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": out})
}

type postMsgReq struct {
	Body *string `json:"body"`
	Kind string  `json:"kind"`
}

func (a *API) postMessage(w http.ResponseWriter, r *http.Request) {
	var req postMsgReq
	if !decode(w, r, &req) {
		return
	}
	sender := r.Header.Get("X-User-Id")
	msg, err := a.svc.PostMessage(r.Context(), chi.URLParam(r, "id"), sender, req.Body, req.Kind)
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, messageDTO(msg))
}

func (a *API) markRead(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-Id")
	if err := a.svc.MarkRead(r.Context(), chi.URLParam(r, "id"), userID); err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

type attachmentReq struct {
	S3Key       string `json:"s3_key"`
	Filename    string `json:"filename"`
	SizeBytes   int64  `json:"size_bytes"`
	ContentType string `json:"content_type"`
}

func (a *API) addAttachment(w http.ResponseWriter, r *http.Request) {
	var req attachmentReq
	if !decode(w, r, &req) {
		return
	}
	id, err := a.svc.AddAttachment(r.Context(), chi.URLParam(r, "id"), req.S3Key, req.Filename, req.SizeBytes, req.ContentType)
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func conversationDTO(c *store.Conversation) map[string]any {
	return map[string]any{
		"id":              c.ID,
		"contract_id":     c.ContractID,
		"project_id":      c.ProjectID,
		"subject":         c.Subject,
		"last_message_at": c.LastMessageAt,
		"created_at":      c.CreatedAt,
	}
}

func messageDTO(m *store.Message) map[string]any {
	return map[string]any{
		"id":              m.ID,
		"conversation_id": m.ConversationID,
		"sender_id":       m.SenderID,
		"body":            m.Body,
		"kind":            m.Kind,
		"created_at":      m.CreatedAt,
		"edited_at":       m.EditedAt,
	}
}

func (a *API) metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# HELP messaging_up\n# TYPE messaging_up gauge\nmessaging_up 1\n"))
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
	case errors.Is(err, service.ErrNotParticipant):
		writeErr(w, http.StatusForbidden, "NOT_PARTICIPANT", "user is not a participant in this conversation")
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "resource not found")
	default:
		a.logger.Error("request failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "internal error")
	}
}
