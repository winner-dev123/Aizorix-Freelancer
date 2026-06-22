// Package httpapi is the REST transport for the user service. The gateway verifies the
// JWT and injects identity headers (X-User-Id, X-User-Roles, X-Account-Type) which the
// handlers read to build an rbac.Principal.
package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aizorix/platform/pkg/rbac"
	"github.com/aizorix/platform/user/internal/service"
	"github.com/aizorix/platform/user/internal/store"
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
	r.Route("/v1/users", func(r chi.Router) {
		r.Post("/freelancer-profile", a.upsertFreelancerProfile)
		r.Get("/freelancer-profile/{userID}", a.getFreelancerProfile)
		r.Post("/client-profile", a.upsertClientProfile)
		r.Get("/client-profile/{userID}", a.getClientProfile)
		r.Get("/freelancer-profile/{userID}/skills", a.listSkills)
		r.Put("/freelancer-profile/{userID}/skills", a.setSkills)
		r.Post("/freelancer-profile/{userID}/portfolio", a.addPortfolio)
		r.Get("/freelancer-profile/{userID}/portfolio", a.listPortfolio)
		r.Post("/kyc/{userID}/status", a.setKYCStatus)
		r.Post("/me/devices", a.registerDevice)
		r.Get("/me/devices", a.listDevices)
		r.Get("/internal/check-permission", a.checkPermission)
	})
	return r
}

// ── Freelancer profile ──────────────────────────────────────────────────────

type freelancerProfileReq struct {
	Headline              string `json:"headline"`
	Bio                   string `json:"bio"`
	HourlyRateCents       *int64 `json:"hourly_rate_cents"`
	Currency              string `json:"currency"`
	Experience            string `json:"experience"`
	AvailabilityHoursWeek *int   `json:"availability_hours_per_week"`
	Timezone              string `json:"timezone"`
	Country               string `json:"country"`
}

func (a *API) upsertFreelancerProfile(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	var req freelancerProfileReq
	if !decode(w, r, &req) {
		return
	}
	prof, err := a.svc.UpsertFreelancerProfile(r.Context(), service.FreelancerProfileInput{
		UserID:                p.UserID,
		Headline:              req.Headline,
		Bio:                   req.Bio,
		HourlyRateCents:       req.HourlyRateCents,
		Currency:              req.Currency,
		Experience:            req.Experience,
		AvailabilityHoursWeek: req.AvailabilityHoursWeek,
		Timezone:              req.Timezone,
		Country:               req.Country,
	})
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, freelancerProfileJSON(prof))
}

func (a *API) getFreelancerProfile(w http.ResponseWriter, r *http.Request) {
	prof, err := a.svc.GetFreelancerProfile(r.Context(), chi.URLParam(r, "userID"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, freelancerProfileJSON(prof))
}

// ── Client profile ──────────────────────────────────────────────────────────

type clientProfileReq struct {
	CompanyName string `json:"company_name"`
	Website     string `json:"website"`
	Industry    string `json:"industry"`
	CompanySize string `json:"company_size"`
	Country     string `json:"country"`
}

func (a *API) upsertClientProfile(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	var req clientProfileReq
	if !decode(w, r, &req) {
		return
	}
	prof, err := a.svc.UpsertClientProfile(r.Context(), service.ClientProfileInput{
		UserID:      p.UserID,
		CompanyName: req.CompanyName,
		Website:     req.Website,
		Industry:    req.Industry,
		CompanySize: req.CompanySize,
		Country:     req.Country,
	})
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, clientProfileJSON(prof))
}

func (a *API) getClientProfile(w http.ResponseWriter, r *http.Request) {
	prof, err := a.svc.GetClientProfile(r.Context(), chi.URLParam(r, "userID"))
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, clientProfileJSON(prof))
}

// ── Skills ──────────────────────────────────────────────────────────────────

func (a *API) listSkills(w http.ResponseWriter, r *http.Request) {
	skills, err := a.svc.ListFreelancerSkills(r.Context(), chi.URLParam(r, "userID"))
	if err != nil {
		mapError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(skills))
	for _, s := range skills {
		out = append(out, map[string]any{
			"skill_id": s.SkillID, "slug": s.Slug, "name": s.Name,
			"category": s.Category, "level": s.Level, "years": s.Years,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": out})
}

type setSkillsReq struct {
	SkillIDs []string `json:"skill_ids"`
	Level    string   `json:"level"`
}

func (a *API) setSkills(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	userID := chi.URLParam(r, "userID")
	if p.UserID == "" || p.UserID != userID {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "can only edit your own skills")
		return
	}
	var req setSkillsReq
	if !decode(w, r, &req) {
		return
	}
	if err := a.svc.SetFreelancerSkills(r.Context(), userID, req.SkillIDs, req.Level); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "count": len(req.SkillIDs)})
}

// ── Portfolio ───────────────────────────────────────────────────────────────

type portfolioReq struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	URL         string   `json:"url"`
	ImageKeys   []string `json:"image_keys"`
	Skills      []string `json:"skills"`
}

func (a *API) addPortfolio(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	userID := chi.URLParam(r, "userID")
	if p.UserID == "" || p.UserID != userID {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "can only edit your own portfolio")
		return
	}
	var req portfolioReq
	if !decode(w, r, &req) {
		return
	}
	it, err := a.svc.AddPortfolioItem(r.Context(), userID, req.Title, req.Description, req.URL, req.ImageKeys, req.Skills)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, portfolioJSON(it))
}

func (a *API) listPortfolio(w http.ResponseWriter, r *http.Request) {
	items, err := a.svc.ListPortfolioItems(r.Context(), chi.URLParam(r, "userID"))
	if err != nil {
		mapError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for i := range items {
		out = append(out, portfolioJSON(&items[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// ── KYC ─────────────────────────────────────────────────────────────────────

type kycReq struct {
	Status      string `json:"status"`
	Provider    string `json:"provider"`
	ProviderRef string `json:"provider_ref"`
}

func (a *API) setKYCStatus(w http.ResponseWriter, r *http.Request) {
	// KYC decisions are not self-service: in production they are pushed in by the KYC
	// provider / back office (an internal service), never by the subject user. We therefore
	// require either a privileged permission (admin/internal-only) or an internal-service
	// auth header, and forbid a user from verifying their own KYC.
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	internal := r.Header.Get("X-Internal-Service") != ""
	if !internal && !p.Can("user:kyc_update") {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "not permitted to update KYC status")
		return
	}
	userID := chi.URLParam(r, "userID")
	if !internal && p.UserID == userID {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "cannot verify your own KYC")
		return
	}
	var req kycReq
	if !decode(w, r, &req) {
		return
	}
	if err := a.svc.SetKYCStatus(r.Context(), userID, req.Status, req.Provider, req.ProviderRef); err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "status": req.Status})
}

// ── Devices ─────────────────────────────────────────────────────────────────

type registerDeviceReq struct {
	Fingerprint       string `json:"fingerprint"`
	DisplayName       string `json:"display_name"`
	AttestationPubkey string `json:"attestation_pubkey"` // base64 (standard encoding)
}

// registerDevice enrolls the caller's desktop tracker, persisting its Ed25519 attestation
// public key. Identity is taken from the trusted X-User-Id header (injected by the gateway);
// the /me path means a user can only ever enroll a device against their own account.
func (a *API) registerDevice(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	var req registerDeviceReq
	if !decode(w, r, &req) {
		return
	}
	if req.Fingerprint == "" || req.AttestationPubkey == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "fingerprint and attestation_pubkey are required")
		return
	}
	pubkey, err := base64.StdEncoding.DecodeString(req.AttestationPubkey)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "attestation_pubkey must be base64")
		return
	}
	dev, err := a.svc.RegisterDevice(r.Context(), p.UserID, req.Fingerprint, req.DisplayName, pubkey)
	if err != nil {
		mapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device_id": dev.ID})
}

func (a *API) listDevices(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.UserID == "" {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}
	devices, err := a.svc.ListDevices(r.Context(), p.UserID)
	if err != nil {
		mapError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(devices))
	for i := range devices {
		out = append(out, deviceJSON(&devices[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// ── Internal permission check ───────────────────────────────────────────────

func (a *API) checkPermission(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	perm := r.URL.Query().Get("permission")
	if perm == "" {
		writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "permission query param required")
		return
	}
	allowed := a.svc.CheckPermission(r.Context(), p, perm)
	writeJSON(w, http.StatusOK, map[string]any{"allowed": allowed})
}

// ── JSON mappers ────────────────────────────────────────────────────────────

func freelancerProfileJSON(p *store.FreelancerProfile) map[string]any {
	return map[string]any{
		"user_id":                     p.UserID,
		"headline":                    p.Headline,
		"bio":                         p.Bio,
		"hourly_rate_cents":           p.HourlyRateCents,
		"currency":                    p.Currency,
		"experience":                  p.Experience,
		"availability_hours_per_week": p.AvailabilityHoursWeek,
		"timezone":                    p.Timezone,
		"country":                     p.Country,
		"kyc_status":                  p.KYCStatus,
		"rating_avg":                  p.RatingAvg,
		"rating_count":                p.RatingCount,
		"total_earned_cents":          p.TotalEarnedCents,
		"jobs_completed":              p.JobsCompleted,
		"profile_completeness":        p.ProfileCompleteness,
		"is_searchable":               p.IsSearchable,
		"created_at":                  p.CreatedAt.Format(time.RFC3339),
		"updated_at":                  p.UpdatedAt.Format(time.RFC3339),
	}
}

func clientProfileJSON(p *store.ClientProfile) map[string]any {
	return map[string]any{
		"user_id":           p.UserID,
		"company_name":      p.CompanyName,
		"website":           p.Website,
		"industry":          p.Industry,
		"company_size":      p.CompanySize,
		"country":           p.Country,
		"payment_verified":  p.PaymentVerified,
		"total_spent_cents": p.TotalSpentCents,
		"hires_count":       p.HiresCount,
		"rating_avg":        p.RatingAvg,
		"rating_count":      p.RatingCount,
		"created_at":        p.CreatedAt.Format(time.RFC3339),
		"updated_at":        p.UpdatedAt.Format(time.RFC3339),
	}
}

func deviceJSON(d *store.Device) map[string]any {
	var lastSeen any
	if d.LastSeenAt != nil {
		lastSeen = d.LastSeenAt.Format(time.RFC3339)
	}
	return map[string]any{
		"device_id":          d.ID,
		"fingerprint":        d.Fingerprint,
		"kind":               d.Kind,
		"display_name":       d.DisplayName,
		"attestation_pubkey": base64.StdEncoding.EncodeToString(d.AttestationPubkey),
		"trusted":            d.Trusted,
		"last_seen_at":       lastSeen,
		"created_at":         d.CreatedAt.Format(time.RFC3339),
	}
}

func portfolioJSON(it *store.PortfolioItem) map[string]any {
	return map[string]any{
		"id":          it.ID,
		"user_id":     it.UserID,
		"title":       it.Title,
		"description": it.Description,
		"url":         it.URL,
		"image_keys":  it.ImageKeys,
		"skills":      it.Skills,
		"created_at":  it.CreatedAt.Format(time.RFC3339),
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func (a *API) metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# HELP user_up Service liveness.\n# TYPE user_up gauge\nuser_up 1\n"))
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
	case errors.Is(err, service.ErrValidation):
		writeErr(w, http.StatusBadRequest, "VALIDATION_FAILED", "request failed validation")
	case errors.Is(err, service.ErrKYCNotAllowed):
		writeErr(w, http.StatusBadRequest, "INVALID_KYC_STATUS", "unknown kyc status")
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
