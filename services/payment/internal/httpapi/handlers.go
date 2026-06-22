// Package httpapi is the REST transport for the payment service. The gateway
// authenticates callers and injects X-User-Id / X-User-Roles / X-Account-Type; the Stripe
// webhook endpoint is the exception (authenticated by signature, not by identity headers).
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/aizorix/platform/payment/internal/service"
	"github.com/aizorix/platform/payment/internal/store"
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
	r.Route("/v1/payments", func(r chi.Router) {
		r.Post("/", a.createIntent)
		r.Post("/{id}/confirm", a.confirm)
		r.Get("/{id}", a.getPayment)
		r.Get("/", a.listForContract)
		// Stripe posts a raw body signed with the endpoint secret; no identity headers.
		r.Post("/webhooks/stripe", a.webhook)
	})
	return r
}

type createIntentReq struct {
	ContractID     string `json:"contract_id"`
	AmountCents    int64  `json:"amount_cents"`
	Currency       string `json:"currency"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (a *API) createIntent(w http.ResponseWriter, r *http.Request) {
	var req createIntentReq
	if !decode(w, r, &req) {
		return
	}
	payerID := r.Header.Get("X-User-Id")
	if payerID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHENTICATED", "missing X-User-Id")
		return
	}
	// The Idempotency-Key header takes precedence over the body field.
	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey == "" {
		idemKey = req.IdempotencyKey
	}
	var contractID *string
	if req.ContractID != "" {
		contractID = &req.ContractID
	}
	res, err := a.svc.CreatePaymentIntent(r.Context(), payerID, contractID, req.AmountCents, req.Currency, idemKey)
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (a *API) confirm(w http.ResponseWriter, r *http.Request) {
	p, err := a.svc.ConfirmPayment(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paymentDTO(p))
}

func (a *API) getPayment(w http.ResponseWriter, r *http.Request) {
	p, err := a.svc.GetPayment(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paymentDTO(p))
}

func (a *API) listForContract(w http.ResponseWriter, r *http.Request) {
	contractID := r.URL.Query().Get("contract_id")
	if contractID == "" {
		writeErr(w, http.StatusBadRequest, "MISSING_PARAM", "contract_id is required")
		return
	}
	ps, err := a.svc.ListPaymentsForContract(r.Context(), contractID)
	if err != nil {
		a.mapError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(ps))
	for _, p := range ps {
		out = append(out, paymentDTO(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"payments": out})
}

func (a *API) webhook(w http.ResponseWriter, r *http.Request) {
	// Read the RAW body BEFORE any JSON parsing: signature verification is over the exact bytes.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID_BODY", "could not read body")
		return
	}
	sig := r.Header.Get("Stripe-Signature")
	if err := a.svc.HandleWebhook(r.Context(), body, sig); err != nil {
		a.mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"received": true})
}

func paymentDTO(p store.Payment) map[string]any {
	return map[string]any{
		"payment_id":               p.ID,
		"contract_id":              p.ContractID,
		"payer_id":                 p.PayerID,
		"amount_cents":             p.AmountCents,
		"currency":                 p.Currency,
		"status":                   p.Status,
		"stripe_payment_intent_id": p.StripePaymentIntentID,
		"stripe_charge_id":         p.StripeChargeID,
		"failure_reason":           p.FailureReason,
		"created_at":               p.CreatedAt,
		"updated_at":               p.UpdatedAt,
	}
}

// metrics emits a minimal Prometheus text-format exposition. Real instrumentation is wired
// by the platform's metrics middleware in production; this keeps the endpoint contract.
func (a *API) metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = io.WriteString(w, "# HELP payment_up 1 if the payment service is serving.\n")
		_, _ = io.WriteString(w, "# TYPE payment_up gauge\n")
		_, _ = io.WriteString(w, "payment_up 1\n")
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
	case errors.Is(err, service.ErrInvalidSignature):
		writeErr(w, http.StatusBadRequest, "INVALID_SIGNATURE", "stripe signature verification failed")
	default:
		if a.logger != nil {
			a.logger.Error("request failed", "err", err)
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "internal error")
	}
}
