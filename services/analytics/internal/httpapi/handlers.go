// Package httpapi is the REST transport for the analytics service. The query endpoints are
// read-only rollup lookups; POST /v1/analytics/internal/ingest mirrors the Kafka consumer's
// IngestEvent entry point for testing and manual backfill. The read endpoints are not
// RBAC-gated (rollup lookups); a deployment can wrap them with a gateway permission check.
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aizorix/platform/analytics/internal/service"
)

// dateLayout is the YYYY-MM-DD format accepted on from/to query params.
const dateLayout = "2006-01-02"

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
	r.Route("/v1/analytics", func(r chi.Router) {
		r.Get("/event-counts", a.eventCounts) // ?from=&to=&type=
		r.Get("/gmv", a.gmv)                  // ?from=&to=&currency=
		r.Get("/funnel", a.funnel)            // ?from=&to=
		r.Get("/summary", a.summary)
		r.Post("/internal/ingest", a.ingest)
	})
	return r
}

// ── request DTOs ────────────────────────────────────────────────────────────

type ingestReq struct {
	EventType   string  `json:"event_type"`
	OccurredAt  *string `json:"occurred_at"` // RFC3339; defaults to now
	AmountCents int64   `json:"amount_cents"`
	Currency    string  `json:"currency"`
}

// ── handlers ────────────────────────────────────────────────────────────────

func (a *API) eventCounts(w http.ResponseWriter, r *http.Request) {
	from, to, ok := parseRange(w, r)
	if !ok {
		return
	}
	out, err := a.svc.EventCounts(r.Context(), from, to, queryPtr(r, "type"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "something went wrong")
		return
	}
	items := make([]map[string]any, 0, len(out))
	for i := range out {
		items = append(items, map[string]any{
			"day": out[i].Day.Format(dateLayout), "event_type": out[i].EventType, "count": out[i].Count,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": from.Format(dateLayout), "to": to.Format(dateLayout), "rows": items,
	})
}

func (a *API) gmv(w http.ResponseWriter, r *http.Request) {
	from, to, ok := parseRange(w, r)
	if !ok {
		return
	}
	res, err := a.svc.GMV(r.Context(), from, to, queryPtr(r, "currency"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "something went wrong")
		return
	}
	items := make([]map[string]any, 0, len(res.Rows))
	for i := range res.Rows {
		row := &res.Rows[i]
		items = append(items, map[string]any{
			"day": row.Day.Format(dateLayout), "currency": row.Currency,
			"gross_cents": row.GrossCents, "fee_cents": row.FeeCents,
			"net_cents": row.NetCents, "contracts": row.Contracts,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": from.Format(dateLayout), "to": to.Format(dateLayout), "rows": items,
		"total": map[string]any{
			"gross_cents": res.Total.GrossCents, "fee_cents": res.Total.FeeCents,
			"net_cents": res.Total.NetCents, "contracts": res.Total.Contracts,
		},
	})
}

func (a *API) funnel(w http.ResponseWriter, r *http.Request) {
	from, to, ok := parseRange(w, r)
	if !ok {
		return
	}
	res, err := a.svc.Funnel(r.Context(), from, to)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "something went wrong")
		return
	}
	items := make([]map[string]any, 0, len(res.Rows))
	for i := range res.Rows {
		row := &res.Rows[i]
		items = append(items, map[string]any{
			"day": row.Day.Format(dateLayout), "projects_published": row.ProjectsPublished,
			"proposals_submitted": row.ProposalsSubmitted, "contracts_activated": row.ContractsActivated,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": from.Format(dateLayout), "to": to.Format(dateLayout), "rows": items,
		"totals": map[string]any{
			"projects_published": res.ProjectsPublished, "proposals_submitted": res.ProposalsSubmitted,
			"contracts_activated": res.ContractsActivated,
		},
		"conversion": map[string]any{
			"proposal_rate": res.ProposalRate, "activation_rate": res.ActivationRate,
		},
	})
}

func (a *API) summary(w http.ResponseWriter, r *http.Request) {
	s, err := a.svc.Summary(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "something went wrong")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total_gmv_cents": s.TotalGMVCents, "total_contracts": s.TotalContracts, "total_users": s.TotalUsers,
	})
}

func (a *API) ingest(w http.ResponseWriter, r *http.Request) {
	var req ingestReq
	if !decode(w, r, &req) {
		return
	}
	if req.EventType == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "event_type is required")
		return
	}
	occurredAt := time.Now().UTC()
	if req.OccurredAt != nil && *req.OccurredAt != "" {
		t, err := time.Parse(time.RFC3339, *req.OccurredAt)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "occurred_at must be RFC3339")
			return
		}
		occurredAt = t
	}
	if err := a.svc.IngestEvent(r.Context(), req.EventType, occurredAt, req.AmountCents, req.Currency); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "something went wrong")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

// ── helpers ─────────────────────────────────────────────────────────────────

// parseRange reads from/to (YYYY-MM-DD) defaulting to the last 30 days when absent. The
// range is inclusive of both endpoints.
func parseRange(w http.ResponseWriter, r *http.Request) (from, to time.Time, ok bool) {
	now := time.Now().UTC().Truncate(24 * time.Hour)
	to = now
	from = now.AddDate(0, 0, -30)
	if v := r.URL.Query().Get("from"); v != "" {
		t, err := time.Parse(dateLayout, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "from must be YYYY-MM-DD")
			return time.Time{}, time.Time{}, false
		}
		from = t
	}
	if v := r.URL.Query().Get("to"); v != "" {
		t, err := time.Parse(dateLayout, v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "to must be YYYY-MM-DD")
			return time.Time{}, time.Time{}, false
		}
		to = t
	}
	if from.After(to) {
		writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "from must not be after to")
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

func queryPtr(r *http.Request, key string) *string {
	v := r.URL.Query().Get(key)
	if v == "" {
		return nil
	}
	return &v
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

// metrics serves a minimal Prometheus text exposition for liveness scraping.
func (a *API) metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# HELP analytics_up\n# TYPE analytics_up gauge\nanalytics_up 1\n"))
	})
}
