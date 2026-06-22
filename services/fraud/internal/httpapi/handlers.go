// Package httpapi is the REST transport for the fraud service. The gateway authenticates
// callers and injects the X-User-Id / X-User-Roles / X-User-Permissions / X-Account-Type
// identity headers; these endpoints are for trust-and-safety operators and internal signal
// producers.
//
// This service is RBAC-GATED: every /v1/fraud route first builds the caller's rbac.Principal
// from the headers and calls authorize(...) before reading or mutating state. authorize
// returns 401 when no identity is present and 403 (via rbac.ErrForbidden) when the permission
// is missing — authorization is a hard gate, not advisory.
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/aizorix/platform/fraud/internal/service"
	"github.com/aizorix/platform/fraud/internal/store"
	"github.com/aizorix/platform/pkg/rbac"
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
	r.Route("/v1/fraud", func(r chi.Router) {
		r.Post("/signals", a.ingestSignal)
		r.Get("/cases", a.listOpenCases)
		r.Get("/cases/{id}", a.getCase)
		r.Post("/cases/{id}/resolve", a.resolveCase)
		r.Get("/risk", a.getRisk)
	})
	return r
}

type ingestReq struct {
	SubjectType string         `json:"subject_type"`
	SubjectID   string         `json:"subject_id"`
	Signal      string         `json:"signal"`
	Weight      float64        `json:"weight"`
	Details     map[string]any `json:"details"`
}

func (a *API) ingestSignal(w http.ResponseWriter, r *http.Request) {
	if !authorize(w, principal(r), "fraud.signal.ingest") {
		return
	}
	var req ingestReq
	if !decode(w, r, &req) {
		return
	}
	// Bound the weight so a subject cannot drag their own score down with large negative
	// (or inflate it with out-of-range) weights. The model expects a [0,1] contribution.
	if req.Weight < 0 || req.Weight > 1.0 {
		writeErr(w, http.StatusBadRequest, "INVALID_ARGUMENT", "weight must be between 0 and 1")
		return
	}
	res, err := a.svc.IngestSignal(r.Context(), req.SubjectType, req.SubjectID, req.Signal, req.Weight, req.Details)
	if err != nil {
		a.mapError(w, err)
		return
	}
	out := map[string]any{
		"signal_id": res.SignalID,
		"score":     res.Score,
		"band":      res.Band,
	}
	if res.CaseID != nil {
		out["case_id"] = *res.CaseID
		out["reason_codes"] = res.CaseCodes
	}
	writeJSON(w, http.StatusCreated, out)
}

func (a *API) listOpenCases(w http.ResponseWriter, r *http.Request) {
	if !authorize(w, principal(r), "fraud.case.read") {
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	cases, err := a.svc.ListOpenCases(r.Context(), limit)
	if err != nil {
		a.mapError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(cases))
	for i := range cases {
		out = append(out, caseDTO(&cases[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"cases": out})
}

func (a *API) getCase(w http.ResponseWriter, r *http.Request) {
	if !authorize(w, principal(r), "fraud.case.read") {
		return
	}
	detail, err := a.svc.GetCase(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.mapError(w, err)
		return
	}
	signals := make([]map[string]any, 0, len(detail.Signals))
	for i := range detail.Signals {
		signals = append(signals, signalDTO(&detail.Signals[i]))
	}
	body := caseDTO(&detail.Case)
	body["signals"] = signals
	writeJSON(w, http.StatusOK, body)
}

type resolveReq struct {
	Status     string  `json:"status"`
	Resolution string  `json:"resolution"`
	AssignedTo *string `json:"assigned_to"`
}

func (a *API) resolveCase(w http.ResponseWriter, r *http.Request) {
	if !authorize(w, principal(r), "fraud.case.resolve") {
		return
	}
	var req resolveReq
	if !decode(w, r, &req) {
		return
	}
	c, err := a.svc.ResolveCase(r.Context(), chi.URLParam(r, "id"), req.Resolution, req.Status, req.AssignedTo)
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, caseDTO(&c))
}

func (a *API) getRisk(w http.ResponseWriter, r *http.Request) {
	if !authorize(w, principal(r), "fraud.case.read") {
		return
	}
	q := r.URL.Query()
	subjectType := q.Get("subject_type")
	subjectID := q.Get("subject_id")
	if subjectType == "" || subjectID == "" {
		writeErr(w, http.StatusBadRequest, "INVALID_ARGUMENT", "subject_type and subject_id are required")
		return
	}
	rs, err := a.svc.GetRiskScore(r.Context(), subjectType, subjectID)
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, riskDTO(&rs))
}

func caseDTO(c *store.Case) map[string]any {
	return map[string]any{
		"id":           c.ID,
		"subject_type": c.SubjectTyp,
		"subject_id":   c.SubjectID,
		"risk_score":   c.RiskScore,
		"status":       c.Status,
		"reason_codes": c.ReasonCodes,
		"assigned_to":  c.AssignedTo,
		"resolution":   c.Resolution,
		"created_at":   c.CreatedAt,
		"updated_at":   c.UpdatedAt,
		"resolved_at":  c.ResolvedAt,
	}
}

func signalDTO(s *store.Signal) map[string]any {
	return map[string]any{
		"id":           s.ID,
		"subject_type": s.SubjectTyp,
		"subject_id":   s.SubjectID,
		"signal":       s.Signal,
		"weight":       s.Weight,
		"details":      s.Details,
		"observed_at":  s.ObservedAt,
	}
}

func riskDTO(rs *store.RiskScore) map[string]any {
	return map[string]any{
		"subject_type":  rs.SubjectTyp,
		"subject_id":    rs.SubjectID,
		"score":         rs.Score,
		"band":          rs.Band,
		"features":      rs.Features,
		"model_version": rs.ModelVersion,
		"computed_at":   rs.ComputedAt,
	}
}

func (a *API) metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# HELP fraud_up\n# TYPE fraud_up gauge\nfraud_up 1\n"))
	})
}

// authorize enforces the fraud permission, writing 401 when no identity is present and
// 403 (via rbac.ErrForbidden -> mapError) when the permission is missing.
func authorize(w http.ResponseWriter, p rbac.Principal, perm string) bool {
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return false
	}
	if err := p.Require(perm); err != nil {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "not permitted")
		return false
	}
	return true
}

// principal builds the caller's rbac.Principal from the gateway-injected identity headers.
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
	case errors.Is(err, service.ErrInvalidSubject):
		writeErr(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
	case errors.Is(err, service.ErrInvalidResolution):
		writeErr(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "resource not found")
	case errors.Is(err, rbac.ErrForbidden):
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "not permitted")
	default:
		a.logger.Error("request failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "internal error")
	}
}
