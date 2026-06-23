// Package httpapi is the REST/JSON transport for the screenshot service. Binary fields
// (hashes, keys, signatures) are base64-encoded in JSON.
package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aizorix/platform/screenshot/internal/service"
	"github.com/aizorix/platform/screenshot/internal/store"
)

type API struct{ svc *service.Service }

func New(svc *service.Service) *API { return &API{svc: svc} }

func (a *API) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Route("/v1/screenshots", func(r chi.Router) {
		r.Get("/", a.list) // GET /v1/screenshots?contract_id=...
		r.Post("/upload-slot", a.requestSlot)
		r.Post("/confirm", a.confirm)
		r.Get("/{id}", a.get)
	})
	return r
}

type slotReq struct {
	ContractID string    `json:"contract_id"`
	SessionID  string    `json:"session_id"`
	SliceID    string    `json:"slice_id"`
	DeviceID   string    `json:"device_id"`
	CapturedAt time.Time `json:"captured_at"`
	// Offline-first: the device's locally-generated DEK (base64) to be KMS-wrapped. Empty for
	// the online flow, where the server mints the DEK.
	ClientDEK string `json:"client_dek"`
}

func (a *API) requestSlot(w http.ResponseWriter, r *http.Request) {
	var req slotReq
	if !decode(w, r, &req) {
		return
	}
	freelancer := r.Header.Get("X-User-Id")
	// A present client_dek is an explicit offline-first request and must decode to exactly 32
	// bytes. db64 swallows base64 errors, so validate strictly here — otherwise a malformed key
	// silently falls back to a server-minted DEK that can never decrypt the device's ciphertext.
	var clientDEK []byte
	if req.ClientDEK != "" {
		d, derr := base64.StdEncoding.DecodeString(req.ClientDEK)
		if derr != nil || len(d) != 32 {
			writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "client_dek must be base64 of exactly 32 bytes")
			return
		}
		clientDEK = d
	}
	slot, err := a.svc.RequestUploadSlot(r.Context(), req.ContractID, req.SessionID, req.SliceID, freelancer, req.DeviceID, req.CapturedAt, clientDEK)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"screenshot_id": slot.ScreenshotID, "upload_url": slot.UploadURL,
		"s3_bucket": slot.Bucket, "s3_key": slot.Key,
		"wrapped_dek": b64(slot.WrappedDEK), "plaintext_dek": b64(slot.PlaintextDEK),
		"kms_key_id": slot.KMSKeyID, "required_headers": slot.Headers,
	})
}

type confirmReq struct {
	ScreenshotID    string    `json:"screenshot_id"`
	ContractID      string    `json:"contract_id"`
	SHA256Cipher    string    `json:"sha256_cipher"`
	GCMNonce        string    `json:"gcm_nonce"`
	DeviceSignature string    `json:"device_signature"`
	DevicePubKey    string    `json:"device_pubkey"`
	CapturedAt      time.Time `json:"captured_at"`
	Width           int       `json:"width"`
	Height          int       `json:"height"`
	SizeBytes       int64     `json:"size_bytes"`
	Format          string    `json:"format"`
	PHash           string    `json:"phash"`
	ActivityPct     int       `json:"activity_pct"`
}

func (a *API) confirm(w http.ResponseWriter, r *http.Request) {
	var req confirmReq
	if !decode(w, r, &req) {
		return
	}
	err := a.svc.ConfirmUpload(r.Context(), service.ConfirmInput{
		ScreenshotID: req.ScreenshotID, ContractID: req.ContractID,
		SHA256Cipher: db64(req.SHA256Cipher), GCMNonce: db64(req.GCMNonce),
		DeviceSignature: db64(req.DeviceSignature), DevicePubKey: db64(req.DevicePubKey),
		CapturedAt: req.CapturedAt, Width: req.Width, Height: req.Height,
		SizeBytes: req.SizeBytes, Format: req.Format, PHash: db64(req.PHash), ActivityPct: req.ActivityPct,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "NOT_FOUND", "upload slot not found or already confirmed")
			return
		}
		writeErr(w, http.StatusBadRequest, "CONFIRM_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": true})
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	contractID := r.URL.Query().Get("contract_id")
	if contractID == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "contract_id is required")
		return
	}
	viewer := r.Header.Get("X-User-Id")
	isAdmin := hasPerm(r.Header.Get("X-Permissions"), "screenshot:audit")
	items, err := a.svc.ListByContract(r.Context(), contractID, viewer, isAdmin)
	if err != nil {
		if errors.Is(err, service.ErrForbidden) {
			writeErr(w, http.StatusForbidden, "FORBIDDEN", "not authorized to view this contract's screenshots")
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{
			"screenshot_id": it.ID, "captured_at": it.CapturedAt, "status": it.Status, "flagged": it.Flagged,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	viewer := r.Header.Get("X-User-Id")
	// Gateway sets X-Permissions; admins carry screenshot:audit.
	isAdmin := hasPerm(r.Header.Get("X-Permissions"), "screenshot:audit")
	v, err := a.svc.GetScreenshot(r.Context(), chi.URLParam(r, "id"), viewer, isAdmin)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeErr(w, http.StatusForbidden, "FORBIDDEN", "not authorized to view this screenshot")
		case errors.Is(err, store.ErrNotFound):
			writeErr(w, http.StatusNotFound, "NOT_FOUND", "screenshot not found")
		default:
			writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		}
		return
	}
	// NOTE: an audit_logs row (actor=viewer, action=screenshot.view) is written here in
	// production via the shared audit middleware; omitted from this snippet for brevity.
	writeJSON(w, http.StatusOK, map[string]any{
		"screenshot_id": v.ScreenshotID, "download_url": v.DownloadURL,
		"wrapped_dek": b64(v.WrappedDEK), "gcm_nonce": b64(v.GCMNonce),
		"captured_at": v.CapturedAt, "status": v.Status, "flagged": v.Flagged,
	})
}

// ── helpers ─────────────────────────────────────────────────────────────────

func b64(b []byte) string  { return base64.StdEncoding.EncodeToString(b) }
func db64(s string) []byte { b, _ := base64.StdEncoding.DecodeString(s); return b }

func hasPerm(csv, want string) bool {
	start := 0
	for i := 0; i <= len(csv); i++ {
		if i == len(csv) || csv[i] == ',' {
			if csv[start:i] == want {
				return true
			}
			start = i + 1
		}
	}
	return false
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
