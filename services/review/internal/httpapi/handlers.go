// Package httpapi is the REST transport for the review service. The gateway authenticates
// callers and injects the X-User-Id / X-User-Roles / X-Account-Type identity headers.
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/aizorix/platform/review/internal/service"
	"github.com/aizorix/platform/review/internal/store"
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
	r.Route("/v1/reviews", func(r chi.Router) {
		r.Post("/", a.createReview)
		r.Get("/{id}", a.getReview)
		r.Get("/", a.listForUser)
		r.Post("/{id}/response", a.addResponse)
		r.Get("/reputation/{userID}", a.getReputation)
		r.Post("/contracts/{contractID}/close-window", a.closeWindow)
	})
	return r
}

type createReq struct {
	ContractID string         `json:"contract_id"`
	RevieweeID string         `json:"reviewee_id"`
	Rating     int            `json:"rating"`
	Dimensions map[string]any `json:"dimensions"`
	Comment    *string        `json:"comment"`
}

func (a *API) createReview(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if !decode(w, r, &req) {
		return
	}
	reviewer := r.Header.Get("X-User-Id")
	if reviewer == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHENTICATED", "missing X-User-Id")
		return
	}
	if req.RevieweeID == "" {
		writeErr(w, http.StatusBadRequest, "INVALID_ARGUMENT", "reviewee_id is required")
		return
	}
	if req.RevieweeID == reviewer {
		writeErr(w, http.StatusBadRequest, "INVALID_ARGUMENT", "cannot review yourself")
		return
	}
	rv, err := a.svc.CreateReview(r.Context(), req.ContractID, reviewer, req.RevieweeID, req.Rating, req.Dimensions, req.Comment)
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, reviewDTO(rv))
}

func (a *API) getReview(w http.ResponseWriter, r *http.Request) {
	rv, err := a.svc.GetReview(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, reviewDTO(&rv))
}

func (a *API) listForUser(w http.ResponseWriter, r *http.Request) {
	revieweeID := r.URL.Query().Get("reviewee_id")
	if revieweeID == "" {
		writeErr(w, http.StatusBadRequest, "INVALID_ARGUMENT", "reviewee_id is required")
		return
	}
	list, err := a.svc.ListReviewsForUser(r.Context(), revieweeID)
	if err != nil {
		a.mapError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for i := range list {
		out = append(out, reviewDTO(&list[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"reviews": out})
}

type responseReq struct {
	Response string `json:"response"`
}

func (a *API) addResponse(w http.ResponseWriter, r *http.Request) {
	var req responseReq
	if !decode(w, r, &req) {
		return
	}
	responder := r.Header.Get("X-User-Id")
	if responder == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHENTICATED", "missing X-User-Id")
		return
	}
	if err := a.svc.AddResponse(r.Context(), chi.URLParam(r, "id"), responder, req.Response); err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (a *API) getReputation(w http.ResponseWriter, r *http.Request) {
	rep, err := a.svc.GetReputation(r.Context(), chi.URLParam(r, "userID"))
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":         rep.UserID,
		"score":           rep.Score,
		"job_success_pct": rep.JobSuccessPct,
		"recompute_at":    rep.RecomputeAt,
		"updated_at":      rep.UpdatedAt,
	})
}

func (a *API) closeWindow(w http.ResponseWriter, r *http.Request) {
	published, err := a.svc.PublishWindowClosed(r.Context(), chi.URLParam(r, "contractID"))
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"published": published})
}

func reviewDTO(r *store.Review) map[string]any {
	return map[string]any{
		"id":           r.ID,
		"contract_id":  r.ContractID,
		"reviewer_id":  r.ReviewerID,
		"reviewee_id":  r.RevieweeID,
		"rating":       r.Rating,
		"dimensions":   r.Dimensions,
		"comment":      r.Comment,
		"is_published": r.IsPublished,
		"published_at": r.PublishedAt,
		"created_at":   r.CreatedAt,
	}
}

func (a *API) metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# HELP review_up\n# TYPE review_up gauge\nreview_up 1\n"))
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
	case errors.Is(err, service.ErrInvalidRating):
		writeErr(w, http.StatusBadRequest, "INVALID_RATING", err.Error())
	case errors.Is(err, service.ErrInvalidContract):
		writeErr(w, http.StatusBadRequest, "INVALID_CONTRACT", err.Error())
	case errors.Is(err, service.ErrContractNotComplete):
		writeErr(w, http.StatusBadRequest, "CONTRACT_NOT_COMPLETE", err.Error())
	case errors.Is(err, service.ErrAlreadyReviewed):
		writeErr(w, http.StatusConflict, "ALREADY_REVIEWED", err.Error())
	case errors.Is(err, service.ErrForbidden):
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "not a party to this contract or not permitted")
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "resource not found")
	default:
		a.logger.Error("request failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "internal error")
	}
}
