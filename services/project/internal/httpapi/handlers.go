// Package httpapi is the REST transport for the project service. The gateway verifies the
// JWT and injects identity headers (X-User-Id, X-User-Roles, X-Account-Type); the owner of
// a project is taken from X-User-Id and project creation requires a client account.
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

	"github.com/aizorix/platform/pkg/rbac"
	"github.com/aizorix/platform/project/internal/service"
	"github.com/aizorix/platform/project/internal/store"
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
	r.Route("/v1/projects", func(r chi.Router) {
		r.Post("/", a.createProject)
		r.Get("/", a.listProjects)
		r.Get("/{id}", a.getProject)
		r.Post("/{id}/publish", a.publishProject)
		r.Post("/{id}/close", a.closeProject)
		r.Post("/{id}/attachments", a.addAttachment)
	})
	return r
}

// ── Create ──────────────────────────────────────────────────────────────────

type createProjectReq struct {
	Title               string   `json:"title"`
	Description         string   `json:"description"`
	BudgetType          string   `json:"budget_type"`
	BudgetMinCents      *int64   `json:"budget_min_cents"`
	BudgetMaxCents      *int64   `json:"budget_max_cents"`
	Currency            string   `json:"currency"`
	WeeklyHourLimit     *int     `json:"weekly_hour_limit"`
	ExperienceRequired  *string  `json:"experience_required"`
	EstimatedDurationDs *int     `json:"estimated_duration_days"`
	CategoryID          *string  `json:"category_id"`
	SkillIDs            []string `json:"skill_ids"`
}

func (a *API) createProject(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	if p.AccountType != "client" {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "only client accounts may post projects")
		return
	}
	var req createProjectReq
	if !decode(w, r, &req) {
		return
	}
	proj, err := a.svc.CreateProject(r.Context(), service.CreateProjectInput{
		ClientID:            p.UserID,
		Title:               req.Title,
		Description:         req.Description,
		BudgetType:          req.BudgetType,
		BudgetMinCents:      req.BudgetMinCents,
		BudgetMaxCents:      req.BudgetMaxCents,
		Currency:            req.Currency,
		WeeklyHourLimit:     req.WeeklyHourLimit,
		ExperienceRequired:  req.ExperienceRequired,
		EstimatedDurationDs: req.EstimatedDurationDs,
		CategoryID:          req.CategoryID,
		SkillIDs:            req.SkillIDs,
	})
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, projectJSON(proj))
}

// ── List & get ──────────────────────────────────────────────────────────────

func (a *API) listProjects(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	projects, err := a.svc.ListProjects(r.Context(), store.ListFilter{
		Status:     q.Get("status"),
		CategoryID: q.Get("category"),
		Search:     q.Get("q"),
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		mapError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(projects))
	for i := range projects {
		out = append(out, projectJSON(&projects[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

func (a *API) getProject(w http.ResponseWriter, r *http.Request) {
	proj, err := a.svc.GetProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectJSON(proj))
}

// ── State transitions ───────────────────────────────────────────────────────

func (a *API) publishProject(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	proj, err := a.svc.PublishProject(r.Context(), chi.URLParam(r, "id"), p.UserID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectJSON(proj))
}

func (a *API) closeProject(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	proj, err := a.svc.CloseProject(r.Context(), chi.URLParam(r, "id"), p.UserID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectJSON(proj))
}

// ── Attachments ─────────────────────────────────────────────────────────────

type attachmentReq struct {
	S3Key       string `json:"s3_key"`
	Filename    string `json:"filename"`
	SizeBytes   int64  `json:"size_bytes"`
	ContentType string `json:"content_type"`
}

func (a *API) addAttachment(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	var req attachmentReq
	if !decode(w, r, &req) {
		return
	}
	att, err := a.svc.AddAttachment(r.Context(), chi.URLParam(r, "id"), p.UserID,
		req.S3Key, req.Filename, req.SizeBytes, req.ContentType)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": att.ID, "project_id": att.ProjectID, "s3_key": att.S3Key,
		"filename": att.Filename, "size_bytes": att.SizeBytes, "content_type": att.ContentType,
		"created_at": att.CreatedAt.Format(time.RFC3339),
	})
}

// ── JSON mappers ────────────────────────────────────────────────────────────

func projectJSON(p *store.Project) map[string]any {
	out := map[string]any{
		"id":                      p.ID,
		"client_id":               p.ClientID,
		"category_id":             p.CategoryID,
		"title":                   p.Title,
		"description":             p.Description,
		"budget_type":             p.BudgetType,
		"budget_min_cents":        p.BudgetMinCents,
		"budget_max_cents":        p.BudgetMaxCents,
		"currency":                p.Currency,
		"weekly_hour_limit":       p.WeeklyHourLimit,
		"experience_required":     p.ExperienceRequired,
		"estimated_duration_days": p.EstimatedDurationDs,
		"status":                  p.Status,
		"visibility":              p.Visibility,
		"proposals_count":         p.ProposalsCount,
		"hired_count":             p.HiredCount,
		"created_at":              p.CreatedAt.Format(time.RFC3339),
		"updated_at":              p.UpdatedAt.Format(time.RFC3339),
	}
	if p.PublishedAt != nil {
		out["published_at"] = p.PublishedAt.Format(time.RFC3339)
	}
	if p.ClosedAt != nil {
		out["closed_at"] = p.ClosedAt.Format(time.RFC3339)
	}
	return out
}

// ── helpers ─────────────────────────────────────────────────────────────────

func (a *API) metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# HELP project_up Service liveness.\n# TYPE project_up gauge\nproject_up 1\n"))
	})
}

func principal(r *http.Request) rbac.Principal {
	var roles []string
	if raw := r.Header.Get("X-User-Roles"); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			if t := strings.TrimSpace(part); t != "" {
				roles = append(roles, t)
			}
		}
	}
	return rbac.Principal{
		UserID:      r.Header.Get("X-User-Id"),
		Roles:       roles,
		AccountType: r.Header.Get("X-Account-Type"),
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
	case errors.Is(err, service.ErrNotFound):
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "resource not found")
	case errors.Is(err, service.ErrInvalidBudget):
		writeErr(w, http.StatusBadRequest, "INVALID_BUDGET", "budget values are invalid for this budget type")
	case errors.Is(err, service.ErrValidation):
		writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "request failed validation")
	case errors.Is(err, service.ErrInvalidState):
		writeErr(w, http.StatusConflict, "INVALID_STATE", "project is not in a valid state for this action")
	case errors.Is(err, service.ErrForbidden), errors.Is(err, rbac.ErrForbidden):
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
