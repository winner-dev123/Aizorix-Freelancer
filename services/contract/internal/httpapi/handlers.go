// Package httpapi is the REST transport for the contract service. Identity is injected
// by the gateway via X-User-Id / X-User-Roles / X-Account-Type headers after the JWT is
// verified; in a standalone deploy an auth middleware would populate them.
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aizorix/platform/contract/internal/service"
	"github.com/aizorix/platform/contract/internal/store"
	"github.com/aizorix/platform/pkg/rbac"
	"github.com/go-chi/chi/v5"
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
	r.Route("/v1/contracts", func(r chi.Router) {
		r.Post("/", a.create)
		r.Get("/", a.list) // ?role=client|freelancer
		r.Get("/{id}", a.get)
		r.Get("/{id}/events", a.events)
		r.Post("/{id}/activate", a.activate)
		r.Post("/{id}/disputes", a.raiseDispute)
		r.Post("/milestones/{mid}/fund", a.fundMilestone)
		r.Post("/milestones/{mid}/submit", a.submitMilestone)
		r.Post("/milestones/{mid}/approve", a.approveMilestone)
	})
	// Internal, server-to-server only — MUST NOT be routed through the public gateway. Lets
	// escrow / timetracking / review authorize an action against a contract by resolving its
	// parties. No caller identity is required; network reachability is the trust boundary.
	r.Get("/v1/internal/contracts/{id}/parties", a.internalParties)
	return r
}

func (a *API) internalParties(w http.ResponseWriter, r *http.Request) {
	clientID, freelancerID, status, err := a.svc.ContractParties(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"contract_id":   chi.URLParam(r, "id"),
		"client_id":     clientID,
		"freelancer_id": freelancerID,
		"status":        status,
	})
}

// ── request DTOs ────────────────────────────────────────────────────────────

type milestoneReq struct {
	Seq         int     `json:"seq"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	AmountCents int64   `json:"amount_cents"`
	DueAt       *string `json:"due_at"` // RFC3339
}

type createReq struct {
	ProjectID        string         `json:"project_id"`
	ProposalID       string         `json:"proposal_id"`
	ClientID         string         `json:"client_id"`
	FreelancerID     string         `json:"freelancer_id"`
	BudgetType       string         `json:"budget_type"`
	Currency         string         `json:"currency"`
	TotalAmountCents *int64         `json:"total_amount_cents"`
	HourlyRateCents  *int64         `json:"hourly_rate_cents"`
	WeeklyHourLimit  *int           `json:"weekly_hour_limit"`
	PlatformFeeBps   int            `json:"platform_fee_bps"`
	Milestones       []milestoneReq `json:"milestones"`
}

type submitMilestoneReq struct {
	Note   string   `json:"note"`
	S3Keys []string `json:"s3_keys"`
}

type disputeReq struct {
	Against     string  `json:"against"`
	MilestoneID *string `json:"milestone_id"`
	Reason      string  `json:"reason"`
	AmountCents *int64  `json:"amount_cents"`
}

// ── handlers ────────────────────────────────────────────────────────────────

func (a *API) create(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	var req createReq
	if !decode(w, r, &req) {
		return
	}
	if req.BudgetType != "fixed" && req.BudgetType != "hourly" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "budget_type must be fixed or hourly")
		return
	}
	// The caller must be the client they name — a user cannot mint a contract on behalf of
	// someone else. (Deeper hardening, tracked separately: derive freelancer_id / rate / amount /
	// fee from the accepted proposal server-side rather than trusting the request body.)
	if req.ClientID != p.UserID {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "client_id must be the authenticated caller")
		return
	}
	ms := make([]store.MilestoneInput, 0, len(req.Milestones))
	for _, m := range req.Milestones {
		due, err := parseTimePtr(m.DueAt)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "due_at must be RFC3339")
			return
		}
		ms = append(ms, store.MilestoneInput{
			Seq: m.Seq, Title: m.Title, Description: m.Description, AmountCents: m.AmountCents, DueAt: due,
		})
	}
	c, err := a.svc.CreateFromProposal(r.Context(), service.CreateInput{
		ProjectID:        req.ProjectID,
		ProposalID:       req.ProposalID,
		ClientID:         req.ClientID,
		FreelancerID:     req.FreelancerID,
		BudgetType:       req.BudgetType,
		Currency:         req.Currency,
		TotalAmountCents: req.TotalAmountCents,
		HourlyRateCents:  req.HourlyRateCents,
		WeeklyHourLimit:  req.WeeklyHourLimit,
		PlatformFeeBps:   req.PlatformFeeBps,
		Milestones:       ms,
	})
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, contractJSON(c))
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	view, err := a.svc.GetContract(r.Context(), chi.URLParam(r, "id"), p.UserID)
	if err != nil {
		mapError(w, err)
		return
	}
	out := contractJSON(&view.Contract)
	mss := make([]map[string]any, 0, len(view.Milestones))
	for i := range view.Milestones {
		mss = append(mss, milestoneJSON(&view.Milestones[i]))
	}
	out["milestones"] = mss
	writeJSON(w, http.StatusOK, out)
}

func (a *API) events(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	evs, err := a.svc.ContractEvents(r.Context(), chi.URLParam(r, "id"), p.UserID)
	if err != nil {
		mapError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(evs))
	for i := range evs {
		out = append(out, contractEventJSON(&evs[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	role := r.URL.Query().Get("role")
	cs, err := a.svc.ListContractsForUser(r.Context(), p.UserID, role)
	if err != nil {
		mapError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(cs))
	for i := range cs {
		out = append(out, contractJSON(&cs[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"contracts": out})
}

func (a *API) activate(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	if err := a.svc.ActivateContract(r.Context(), chi.URLParam(r, "id"), p.UserID); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (a *API) fundMilestone(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	if err := a.svc.FundMilestone(r.Context(), chi.URLParam(r, "mid"), p.UserID); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (a *API) submitMilestone(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	var req submitMilestoneReq
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	if err := a.svc.SubmitMilestone(r.Context(), chi.URLParam(r, "mid"), p.UserID, req.Note, req.S3Keys); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (a *API) approveMilestone(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	if err := a.svc.ApproveMilestone(r.Context(), chi.URLParam(r, "mid"), p.UserID); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (a *API) raiseDispute(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	var req disputeReq
	if !decode(w, r, &req) {
		return
	}
	if req.Against == "" || req.Reason == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "against and reason are required")
		return
	}
	id, err := a.svc.RaiseDispute(r.Context(), chi.URLParam(r, "id"), p.UserID, req.Against, req.MilestoneID, req.Reason, req.AmountCents)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"dispute_id": id})
}

// ── helpers ─────────────────────────────────────────────────────────────────

func contractJSON(c *store.Contract) map[string]any {
	return map[string]any{
		"id": c.ID, "project_id": c.ProjectID, "proposal_id": c.ProposalID,
		"client_id": c.ClientID, "freelancer_id": c.FreelancerID, "budget_type": c.BudgetType,
		"currency": c.Currency, "total_amount_cents": c.TotalAmountCents,
		"hourly_rate_cents": c.HourlyRateCents, "weekly_hour_limit": c.WeeklyHourLimit,
		"status": c.Status, "platform_fee_bps": c.PlatformFeeBps,
		"started_at": timePtrJSON(c.StartedAt), "ended_at": timePtrJSON(c.EndedAt),
		"end_reason": c.EndReason,
	}
}

func milestoneJSON(m *store.Milestone) map[string]any {
	return map[string]any{
		"id": m.ID, "contract_id": m.ContractID, "seq": m.Seq, "title": m.Title,
		"description": m.Description, "amount_cents": m.AmountCents, "status": m.Status,
		"due_at": timePtrJSON(m.DueAt), "funded_at": timePtrJSON(m.FundedAt),
		"submitted_at": timePtrJSON(m.SubmittedAt), "approved_at": timePtrJSON(m.ApprovedAt),
		"released_at": timePtrJSON(m.ReleasedAt),
	}
}

func contractEventJSON(e *store.ContractEvent) map[string]any {
	payload := e.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	return map[string]any{
		"from_status": e.FromStatus, "to_status": e.ToStatus, "event": e.Event,
		"actor_id": e.ActorID, "payload": json.RawMessage(payload),
		"created_at": e.CreatedAt.Format(time.RFC3339),
	}
}

func timePtrJSON(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339)
}

func parseTimePtr(s *string) (*time.Time, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func principal(r *http.Request) rbac.Principal {
	var roles []string
	if raw := r.Header.Get("X-User-Roles"); raw != "" {
		for _, role := range strings.Split(raw, ",") {
			if role = strings.TrimSpace(role); role != "" {
				roles = append(roles, role)
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
	case errors.Is(err, service.ErrInvalidParties):
		writeErr(w, http.StatusBadRequest, "INVALID_PARTIES", "client and freelancer must differ")
	case errors.Is(err, service.ErrNoMilestones):
		writeErr(w, http.StatusBadRequest, "NO_MILESTONES", "fixed-price contract requires at least one milestone")
	case errors.Is(err, service.ErrInvalidState):
		writeErr(w, http.StatusConflict, "INVALID_STATE", "contract is not in a valid state for this operation")
	case errors.Is(err, service.ErrInvalidMilestoneState):
		writeErr(w, http.StatusConflict, "INVALID_MILESTONE_STATE", "milestone is not in a valid state for this operation")
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
		_, _ = w.Write([]byte("# HELP contract_up\n# TYPE contract_up gauge\ncontract_up 1\n"))
	})
}
