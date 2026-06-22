// Package httpapi is the REST transport for the proposal service. Identity is injected
// by the gateway via X-User-Id / X-User-Roles / X-Account-Type headers after the JWT is
// verified; in a standalone deploy an auth middleware would populate them.
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/aizorix/platform/proposal/internal/service"
	"github.com/aizorix/platform/proposal/internal/store"
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
	r.Route("/v1/proposals", func(r chi.Router) {
		r.Post("/", a.submit)
		r.Get("/", a.listByProject) // ?project_id=&status=
		r.Get("/mine", a.listMine)
		r.Get("/{id}", a.get)
		r.Post("/{id}/withdraw", a.withdraw)
		r.Post("/{id}/shortlist", a.shortlist)
	})
	// Internal, server-to-server only — MUST NOT be routed through the public gateway. Lets the
	// contract service form a contract from the AUTHORITATIVE proposal (freelancer, amount, owning
	// client, status) instead of trusting the request body. No caller identity required.
	r.Get("/v1/internal/proposals/{id}", a.internalProposal)
	return r
}

func (a *API) internalProposal(w http.ResponseWriter, r *http.Request) {
	cp, err := a.svc.ProposalForContract(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cp)
}

// ── request DTOs ────────────────────────────────────────────────────────────

type milestoneReq struct {
	Seq         int    `json:"seq"`
	Title       string `json:"title"`
	AmountCents int64  `json:"amount_cents"`
	DueDays     *int   `json:"due_days"`
}

type answerReq struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

type submitReq struct {
	ProjectID             string         `json:"project_id"`
	CoverLetter           string         `json:"cover_letter"`
	BidAmountCents        int64          `json:"bid_amount_cents"`
	Currency              string         `json:"currency"`
	EstimatedDurationDays *int           `json:"estimated_duration_days"`
	ConnectsSpent         int            `json:"connects_spent"`
	Milestones            []milestoneReq `json:"milestones"`
	Answers               []answerReq    `json:"answers"`
}

// ── handlers ────────────────────────────────────────────────────────────────

func (a *API) submit(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	var req submitReq
	if !decode(w, r, &req) {
		return
	}
	if req.ProjectID == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "project_id is required")
		return
	}
	ms := make([]service.MilestoneInput, 0, len(req.Milestones))
	for _, m := range req.Milestones {
		ms = append(ms, service.MilestoneInput{Seq: m.Seq, Title: m.Title, AmountCents: m.AmountCents, DueDays: m.DueDays})
	}
	ans := make([]service.AnswerInput, 0, len(req.Answers))
	for _, an := range req.Answers {
		ans = append(ans, service.AnswerInput{Question: an.Question, Answer: an.Answer})
	}
	p2, err := a.svc.SubmitProposal(r.Context(), service.SubmitInput{
		ProjectID:             req.ProjectID,
		FreelancerID:          p.UserID,
		CoverLetter:           req.CoverLetter,
		BidAmountCents:        req.BidAmountCents,
		Currency:              req.Currency,
		EstimatedDurationDays: req.EstimatedDurationDays,
		ConnectsSpent:         req.ConnectsSpent,
		Milestones:            ms,
		Answers:               ans,
	})
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, proposalJSON(p2))
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	view, err := a.svc.GetProposal(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		mapError(w, err)
		return
	}
	out := proposalJSON(&view.Proposal)
	mss := make([]map[string]any, 0, len(view.Milestones))
	for _, m := range view.Milestones {
		mss = append(mss, map[string]any{
			"id": m.ID, "seq": m.Seq, "title": m.Title,
			"amount_cents": m.AmountCents, "due_days": m.DueDays,
		})
	}
	out["milestones"] = mss
	writeJSON(w, http.StatusOK, out)
}

func (a *API) withdraw(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	if err := a.svc.WithdrawProposal(r.Context(), chi.URLParam(r, "id"), p.UserID); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (a *API) shortlist(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	if err := a.svc.ShortlistProposal(r.Context(), chi.URLParam(r, "id"), p.UserID); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (a *API) listByProject(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	projectID := r.URL.Query().Get("project_id")
	if projectID == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "project_id query param is required")
		return
	}
	status := r.URL.Query().Get("status")
	ps, err := a.svc.ListProposalsForProject(r.Context(), projectID, status, p.UserID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposals": proposalsJSON(ps)})
}

func (a *API) listMine(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	ps, err := a.svc.ListProposalsForFreelancer(r.Context(), p.UserID)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposals": proposalsJSON(ps)})
}

// ── helpers ─────────────────────────────────────────────────────────────────

func proposalJSON(p *store.Proposal) map[string]any {
	return map[string]any{
		"id": p.ID, "project_id": p.ProjectID, "freelancer_id": p.FreelancerID,
		"cover_letter": p.CoverLetter, "bid_amount_cents": p.BidAmountCents,
		"currency": p.Currency, "estimated_duration_days": p.EstimatedDurationDays,
		"status": p.Status, "connects_spent": p.ConnectsSpent,
	}
}

func proposalsJSON(ps []store.Proposal) []map[string]any {
	out := make([]map[string]any, 0, len(ps))
	for i := range ps {
		out = append(out, proposalJSON(&ps[i]))
	}
	return out
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
	case errors.Is(err, service.ErrInvalidBid):
		writeErr(w, http.StatusBadRequest, "INVALID_BID", "bid amount must be positive")
	case errors.Is(err, service.ErrDuplicateProposal):
		writeErr(w, http.StatusConflict, "DUPLICATE_PROPOSAL", "you have already submitted a proposal for this project")
	case errors.Is(err, service.ErrInvalidState):
		writeErr(w, http.StatusConflict, "INVALID_STATE", "proposal is not in a valid state for this operation")
	case errors.Is(err, service.ErrNotFound):
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "proposal not found")
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
		_, _ = w.Write([]byte("# HELP proposal_up\n# TYPE proposal_up gauge\nproposal_up 1\n"))
	})
}
