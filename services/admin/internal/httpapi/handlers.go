// Package httpapi is the REST transport for the admin service. Identity is injected by the
// gateway via X-User-Id / X-User-Roles / X-Account-Type after the JWT is verified.
//
// This service is RBAC-GATED: every route first builds the caller's rbac.Principal from the
// headers and calls principal.Require("admin.<action>"). Require returns rbac.ErrForbidden,
// which mapError renders as 403, BEFORE the service method runs — so authorization is a hard
// gate, not advisory.
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aizorix/platform/admin/internal/service"
	"github.com/aizorix/platform/admin/internal/store"
	"github.com/aizorix/platform/pkg/rbac"
)

type API struct {
	svc    *service.Service
	logger *slog.Logger
}

func New(svc *service.Service, logger *slog.Logger) *API {
	return &API{svc: svc, logger: logger}
}

func (a *API) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Handle("/metrics", a.metrics())
	r.Route("/v1/admin", func(r chi.Router) {
		r.Post("/users/{id}/suspend", a.suspendUser)     // perm admin.user.suspend
		r.Post("/users/{id}/reinstate", a.reinstateUser) // perm admin.user.suspend
		r.Post("/disputes/{id}/resolve", a.resolveDispute)
		r.Get("/screenshots", a.listScreenshots)
		r.Get("/actions", a.listActions)
		r.Get("/audit-logs", a.listAuditLogs)
	})
	return r
}

// ── request DTOs ────────────────────────────────────────────────────────────

type reasonReq struct {
	Reason string `json:"reason"`
}

type resolveReq struct {
	Resolution string `json:"resolution"`
	Note       string `json:"note"`
}

// ── handlers ────────────────────────────────────────────────────────────────

func (a *API) suspendUser(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if !authorize(w, p, "admin.user.suspend") {
		return
	}
	var req reasonReq
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	if err := a.svc.SuspendUser(r.Context(), p.UserID, chi.URLParam(r, "id"), req.Reason, auditMeta(r)); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (a *API) reinstateUser(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if !authorize(w, p, "admin.user.suspend") {
		return
	}
	var req reasonReq
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	if err := a.svc.ReinstateUser(r.Context(), p.UserID, chi.URLParam(r, "id"), req.Reason, auditMeta(r)); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (a *API) resolveDispute(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if !authorize(w, p, "admin.dispute.resolve") {
		return
	}
	var req resolveReq
	if !decode(w, r, &req) {
		return
	}
	if req.Resolution == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "resolution is required")
		return
	}
	if err := a.svc.ResolveDispute(r.Context(), p.UserID, chi.URLParam(r, "id"), req.Resolution, req.Note, auditMeta(r)); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (a *API) listScreenshots(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if !authorize(w, p, "admin.screenshot.audit") {
		return
	}
	f := service.ScreenshotAuditFilter{
		UserID: queryPtr(r, "user_id"),
		Status: queryPtr(r, "status"),
		Limit:  queryInt(r, "limit", 50),
	}
	out, err := a.svc.ListScreenshotsForAudit(r.Context(), p.UserID, f, auditMeta(r))
	if err != nil {
		mapError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(out))
	for i := range out {
		items = append(items, screenshotJSON(&out[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"screenshots": items})
}

func (a *API) listActions(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if !authorize(w, p, "admin.audit.read") {
		return
	}
	out, err := a.svc.ListAdminActions(r.Context(), queryInt(r, "limit", 50))
	if err != nil {
		mapError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(out))
	for i := range out {
		items = append(items, adminActionJSON(&out[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"actions": items})
}

func (a *API) listAuditLogs(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if !authorize(w, p, "admin.audit.read") {
		return
	}
	f := service.AuditLogFilter{
		ActorID: queryPtr(r, "actor_id"),
		Action:  queryPtr(r, "action"),
		Limit:   queryInt(r, "limit", 50),
	}
	out, err := a.svc.ListAuditLogs(r.Context(), f)
	if err != nil {
		mapError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(out))
	for i := range out {
		items = append(items, auditLogJSON(&out[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit_logs": items})
}

// ── JSON projections ──────────────────────────────────────────────────────────

func adminActionJSON(a *store.AdminAction) map[string]any {
	return map[string]any{
		"id": a.ID, "admin_id": a.AdminID, "action": a.Action,
		"target_type": a.TargetType, "target_id": a.TargetID, "reason": a.Reason,
		"metadata": rawJSON(a.Metadata), "created_at": a.CreatedAt.Format(time.RFC3339),
	}
}

func auditLogJSON(l *store.AuditLog) map[string]any {
	return map[string]any{
		"id": l.ID, "occurred_at": l.OccurredAt.Format(time.RFC3339),
		"actor_id": l.ActorID, "actor_type": l.ActorType, "action": l.Action,
		"resource_type": l.ResourceType, "resource_id": l.ResourceID,
		"ip": l.IP, "user_agent": l.UserAgent, "context": rawJSON(l.Context),
	}
}

func screenshotJSON(s *store.Screenshot) map[string]any {
	return map[string]any{
		"id": s.ID, "work_session_id": s.WorkSessionID, "user_id": s.UserID,
		"status": s.Status, "captured_at": s.CapturedAt.Format(time.RFC3339), "s3_key": s.S3Key,
	}
}

// rawJSON returns jsonb bytes as a json.RawMessage so they pass through unescaped.
func rawJSON(b []byte) any {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}

// ── auth + request helpers ────────────────────────────────────────────────────

// authorize enforces the admin permission, writing 401 when no identity is present and
// 403 (via rbac.ErrForbidden -> mapError) when the permission is missing.
func authorize(w http.ResponseWriter, p rbac.Principal, perm string) bool {
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return false
	}
	if err := p.Require(perm); err != nil {
		mapError(w, err)
		return false
	}
	return true
}

func principal(r *http.Request) rbac.Principal {
	var roles, perms []string
	if raw := r.Header.Get("X-User-Roles"); raw != "" {
		for _, role := range strings.Split(raw, ",") {
			if role = strings.TrimSpace(role); role != "" {
				roles = append(roles, role)
			}
		}
	}
	if raw := r.Header.Get("X-User-Permissions"); raw != "" {
		for _, perm := range strings.Split(raw, ",") {
			if perm = strings.TrimSpace(perm); perm != "" {
				perms = append(perms, perm)
			}
		}
	}
	return rbac.Principal{
		UserID:      r.Header.Get("X-User-Id"),
		Roles:       roles,
		Permissions: perms,
		AccountType: r.Header.Get("X-Account-Type"),
	}
}

// auditMeta extracts client IP (from X-Forwarded-For) and User-Agent for the audit trail.
func auditMeta(r *http.Request) service.AuditCtx {
	return service.AuditMeta(clientIP(r), nilIfEmpty(r.Header.Get("User-Agent")))
}

// clientIP returns the originating client IP from X-Forwarded-For (first hop) or nil.
func clientIP(r *http.Request) *string {
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return nil
	}
	ip := strings.TrimSpace(strings.Split(xff, ",")[0])
	if ip == "" {
		return nil
	}
	return &ip
}

func queryPtr(r *http.Request, key string) *string {
	v := r.URL.Query().Get(key)
	if v == "" {
		return nil
	}
	return &v
}

func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
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
	case errors.Is(err, service.ErrInvalidResolution):
		writeErr(w, http.StatusBadRequest, "INVALID_RESOLUTION", "resolution must be resolved_client, resolved_freelancer or split")
	case errors.Is(err, service.ErrNotFound):
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "resource not found")
	case errors.Is(err, rbac.ErrForbidden):
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "not permitted")
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

// metrics serves a minimal Prometheus text exposition for liveness scraping.
func (a *API) metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# HELP admin_up\n# TYPE admin_up gauge\nadmin_up 1\n"))
	})
}
