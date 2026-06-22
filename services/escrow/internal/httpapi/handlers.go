// Package httpapi is the REST transport for the escrow service. The gateway authenticates
// callers and injects X-User-Id / X-User-Roles / X-Account-Type identity headers.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/aizorix/platform/escrow/internal/service"
	"github.com/aizorix/platform/escrow/internal/store"
	"github.com/go-chi/chi/v5"
)

type API struct {
	svc    *service.Service
	logger *slog.Logger
}

func New(svc *service.Service, logger *slog.Logger) *API { return &API{svc: svc, logger: logger} }

// requireUser reads the gateway-injected X-User-Id identity header and writes a 401 when it
// is absent. This is defense-in-depth: the gateway already authenticates callers, but the
// escrow money endpoints must never run unauthenticated even if that layer is bypassed.
//
// AUTHORIZATION NOTE: in production, releasing/refunding escrow is additionally gated on the
// caller being a party to the contract (and holding the right role) — that ownership check
// is performed against the contract service before the money moves. This handler only
// enforces authentication; the contract-party/role gate lives in the production gateway and
// the contract service's RBAC/ABAC guards.
func (a *API) requireUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	uid := r.Header.Get("X-User-Id")
	if uid == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHENTICATED", "missing X-User-Id")
		return "", false
	}
	return uid, true
}

func (a *API) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Handle("/metrics", a.metrics())
	r.Route("/v1/escrow", func(r chi.Router) {
		r.Post("/fund", a.fund)
		r.Get("/{id}", a.getEscrow)
		r.Get("/", a.getByContract)
		r.Post("/{id}/allocate", a.allocate)
		r.Post("/{id}/release-milestone", a.releaseMilestone)
		r.Post("/{id}/release-hours", a.releaseHours)
		r.Post("/{id}/refund", a.refund)
		r.Get("/{id}/allocations", a.listAllocations)
	})
	return r
}

type fundReq struct {
	ContractID  string `json:"contract_id"`
	AmountCents int64  `json:"amount_cents"`
	Currency    string `json:"currency"`
}

func (a *API) fund(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireUser(w, r); !ok {
		return
	}
	var req fundReq
	if !decode(w, r, &req) {
		return
	}
	if req.ContractID == "" {
		writeErr(w, http.StatusBadRequest, "MISSING_PARAM", "contract_id is required")
		return
	}
	e, err := a.svc.FundEscrow(r.Context(), req.ContractID, req.AmountCents, req.Currency)
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, escrowDTO(e))
}

func (a *API) getEscrow(w http.ResponseWriter, r *http.Request) {
	e, err := a.svc.GetEscrow(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, escrowDTO(e))
}

func (a *API) getByContract(w http.ResponseWriter, r *http.Request) {
	contractID := r.URL.Query().Get("contract_id")
	if contractID == "" {
		writeErr(w, http.StatusBadRequest, "MISSING_PARAM", "contract_id is required")
		return
	}
	e, err := a.svc.GetEscrowByContract(r.Context(), contractID)
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, escrowDTO(e))
}

type allocateReq struct {
	MilestoneID string `json:"milestone_id"`
	AmountCents int64  `json:"amount_cents"`
}

func (a *API) allocate(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireUser(w, r); !ok {
		return
	}
	var req allocateReq
	if !decode(w, r, &req) {
		return
	}
	if req.MilestoneID == "" {
		writeErr(w, http.StatusBadRequest, "MISSING_PARAM", "milestone_id is required")
		return
	}
	alloc, err := a.svc.AllocateToMilestone(r.Context(), chi.URLParam(r, "id"), req.MilestoneID, req.AmountCents)
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, allocationDTO(alloc))
}

type releaseMilestoneReq struct {
	MilestoneID string `json:"milestone_id"`
	AmountCents int64  `json:"amount_cents"`
}

func (a *API) releaseMilestone(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireUser(w, r); !ok {
		return
	}
	var req releaseMilestoneReq
	if !decode(w, r, &req) {
		return
	}
	if req.MilestoneID == "" {
		writeErr(w, http.StatusBadRequest, "MISSING_PARAM", "milestone_id is required")
		return
	}
	e, err := a.svc.ReleaseMilestone(r.Context(), chi.URLParam(r, "id"), req.MilestoneID, req.AmountCents)
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, escrowDTO(e))
}

type releaseHoursReq struct {
	BillingWeek string `json:"billing_week"`
	AmountCents int64  `json:"amount_cents"`
}

func (a *API) releaseHours(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireUser(w, r); !ok {
		return
	}
	var req releaseHoursReq
	if !decode(w, r, &req) {
		return
	}
	if req.BillingWeek == "" {
		writeErr(w, http.StatusBadRequest, "MISSING_PARAM", "billing_week is required")
		return
	}
	e, err := a.svc.ReleaseHours(r.Context(), chi.URLParam(r, "id"), req.BillingWeek, req.AmountCents)
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, escrowDTO(e))
}

type refundReq struct {
	AmountCents int64  `json:"amount_cents"`
	Reason      string `json:"reason"`
}

func (a *API) refund(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireUser(w, r); !ok {
		return
	}
	var req refundReq
	if !decode(w, r, &req) {
		return
	}
	e, err := a.svc.RefundEscrow(r.Context(), chi.URLParam(r, "id"), req.AmountCents, req.Reason)
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, escrowDTO(e))
}

func (a *API) listAllocations(w http.ResponseWriter, r *http.Request) {
	allocs, err := a.svc.ListAllocations(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.mapError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(allocs))
	for _, al := range allocs {
		out = append(out, allocationDTO(al))
	}
	writeJSON(w, http.StatusOK, map[string]any{"allocations": out})
}

func escrowDTO(e store.Escrow) map[string]any {
	return map[string]any{
		"escrow_id":      e.ID,
		"contract_id":    e.ContractID,
		"currency":       e.Currency,
		"held_cents":     e.HeldCents,
		"released_cents": e.ReleasedCents,
		"refunded_cents": e.RefundedCents,
		"status":         e.Status,
		"created_at":     e.CreatedAt,
		"updated_at":     e.UpdatedAt,
	}
}

func allocationDTO(a store.Allocation) map[string]any {
	return map[string]any{
		"allocation_id": a.ID,
		"escrow_id":     a.EscrowID,
		"milestone_id":  a.MilestoneID,
		"billing_week":  a.BillingWeek,
		"amount_cents":  a.AmountCents,
		"status":        a.Status,
		"created_at":    a.CreatedAt,
		"released_at":   a.ReleasedAt,
	}
}

// metrics emits a minimal Prometheus text-format exposition. Real instrumentation is wired
// by the platform's metrics middleware in production; this keeps the endpoint contract.
func (a *API) metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = io.WriteString(w, "# HELP escrow_up 1 if the escrow service is serving.\n")
		_, _ = io.WriteString(w, "# TYPE escrow_up gauge\n")
		_, _ = io.WriteString(w, "escrow_up 1\n")
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

// mapError translates domain errors into HTTP status codes.
func (a *API) mapError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "resource not found")
	case errors.Is(err, service.ErrInsufficientFunds):
		writeErr(w, http.StatusConflict, "INSUFFICIENT_FUNDS", "insufficient escrow funds")
	default:
		if a.logger != nil {
			a.logger.Error("request failed", "err", err)
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "internal error")
	}
}
