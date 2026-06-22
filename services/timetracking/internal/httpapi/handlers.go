// Package httpapi is the REST transport for the time-tracking service (the desktop tracker
// is the primary caller; the gateway authenticates and injects X-User-Id).
package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/aizorix/platform/timetracking/internal/activity"
	"github.com/aizorix/platform/timetracking/internal/service"
	"github.com/go-chi/chi/v5"
)

type API struct{ svc *service.Service }

func New(svc *service.Service) *API { return &API{svc: svc} }

func (a *API) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Route("/v1/tracking", func(r chi.Router) {
		r.Post("/sessions", a.startSession)
		r.Post("/sessions/{id}/slices", a.submitSlices)
		r.Post("/sessions/{id}/stop", a.stopSession)
		r.Get("/contracts/{contractID}/timesheet", a.getTimesheet)
	})
	return r
}

type startReq struct {
	ContractID string `json:"contract_id"`
	DeviceID   string `json:"device_id"`
	Timezone   string `json:"timezone"`
}

func (a *API) startSession(w http.ResponseWriter, r *http.Request) {
	var req startReq
	if !decode(w, r, &req) {
		return
	}
	freelancer := r.Header.Get("X-User-Id")
	res, err := a.svc.StartSession(r.Context(), req.ContractID, freelancer, req.DeviceID, req.Timezone, time.Now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"session_id": res.SessionID, "billing_week": res.BillingWeek,
		"capture_interval_seconds": res.CaptureInterval,
	})
}

type sampleDTO struct {
	At            time.Time `json:"at"`
	KeyboardCount int       `json:"keyboard_count"`
	MouseCount    int       `json:"mouse_count"`
	MouseDistance int       `json:"mouse_distance"`
}
type sliceDTO struct {
	ContractID     string      `json:"contract_id"`
	SliceStart     time.Time   `json:"slice_start"`
	SliceEnd       time.Time   `json:"slice_end"`
	Samples        []sampleDTO `json:"samples"`
	ActiveApp      string      `json:"active_app"`
	ActiveAppTitle string      `json:"active_app_title"`
	BrowserHost    string      `json:"browser_url_host"`
	ScreenshotID   string      `json:"screenshot_id"`
	IsManual       bool        `json:"is_manual"`
}
type submitReq struct {
	Slices         []sliceDTO `json:"slices"`
	IdempotencyKey string     `json:"idempotency_key"`
}

func (a *API) submitSlices(w http.ResponseWriter, r *http.Request) {
	var req submitReq
	if !decode(w, r, &req) {
		return
	}
	sessionID := chi.URLParam(r, "id")
	in := make([]service.IncomingSlice, 0, len(req.Slices))
	for _, s := range req.Slices {
		samples := make([]activity.Sample, 0, len(s.Samples))
		for _, sm := range s.Samples {
			samples = append(samples, activity.Sample{
				At: sm.At, KeyboardCount: sm.KeyboardCount,
				MouseCount: sm.MouseCount, MouseDistance: sm.MouseDistance,
			})
		}
		in = append(in, service.IncomingSlice{
			SessionID: sessionID, ContractID: s.ContractID, Start: s.SliceStart, End: s.SliceEnd,
			Samples: samples, ActiveApp: s.ActiveApp, ActiveAppTitle: s.ActiveAppTitle,
			BrowserHost: s.BrowserHost, ScreenshotID: s.ScreenshotID, IsManual: s.IsManual,
		})
	}
	contractID := ""
	if len(in) > 0 {
		contractID = in[0].ContractID
	}
	accepted, err := a.svc.IngestSlices(r.Context(), contractID, in)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": accepted})
}

type stopReq struct {
	Memo string `json:"memo"`
}

func (a *API) stopSession(w http.ResponseWriter, r *http.Request) {
	var req stopReq
	_ = decode(w, r, &req)
	res, err := a.svc.StopSession(r.Context(), chi.URLParam(r, "id"), req.Memo, time.Now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"active_seconds": res.ActiveSeconds, "idle_seconds": res.IdleSeconds,
		"avg_activity_pct": res.AvgActivityPct,
	})
}

func (a *API) getTimesheet(w http.ResponseWriter, r *http.Request) {
	v, err := a.svc.GetTimesheet(r.Context(), chi.URLParam(r, "contractID"), r.URL.Query().Get("billing_week"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "timesheet not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"billing_week": v.Week, "total_billable_seconds": v.BillableSeconds,
		"amount_cents": v.AmountCents, "status": v.Status, "avg_activity_pct": v.AvgActivityPct,
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
