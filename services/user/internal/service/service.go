// Package service holds the user service business logic: freelancer/client profile
// management, skills, portfolio, and KYC. Transport (HTTP) is a thin adapter over these
// methods. State changes that other services care about are emitted via the outbox.
package service

import (
	"context"
	"errors"

	"github.com/aizorix/platform/pkg/outbox"
	"github.com/aizorix/platform/pkg/rbac"
	"github.com/aizorix/platform/user/internal/store"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrValidation    = errors.New("validation failed")
	ErrKYCNotAllowed = errors.New("invalid kyc status")
)

// validKYCStatuses mirrors the kyc_status enum.
var validKYCStatuses = map[string]bool{
	"not_started": true, "pending": true, "verified": true, "rejected": true,
}

// validExperience mirrors the experience_level enum.
var validExperience = map[string]bool{
	"entry": true, "intermediate": true, "expert": true,
}

type Service struct{ store *store.Store }

func New(st *store.Store) *Service { return &Service{store: st} }

// FreelancerProfileInput is the upsert request after transport decoding.
type FreelancerProfileInput struct {
	UserID                string
	Headline              string
	Bio                   string
	HourlyRateCents       *int64
	Currency              string
	Experience            string
	AvailabilityHoursWeek *int
	Timezone              string
	Country               string
}

// completenessFields counts the core profile fields used for the completeness score.
const completenessFields = 6

// UpsertFreelancerProfile writes the profile, recomputes completeness + searchability,
// and emits a profile.updated event — all in one transaction (outbox pattern).
func (s *Service) UpsertFreelancerProfile(ctx context.Context, in FreelancerProfileInput) (*store.FreelancerProfile, error) {
	if in.UserID == "" {
		return nil, ErrValidation
	}
	if in.Currency == "" {
		in.Currency = "USD"
	}
	if in.Experience == "" {
		in.Experience = "intermediate"
	}
	if !validExperience[in.Experience] {
		return nil, ErrValidation
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Completeness: count of populated core fields scaled to 0..100.
	filled := 0
	if in.Headline != "" {
		filled++
	}
	if in.Bio != "" {
		filled++
	}
	if in.HourlyRateCents != nil && *in.HourlyRateCents > 0 {
		filled++
	}
	if in.AvailabilityHoursWeek != nil && *in.AvailabilityHoursWeek > 0 {
		filled++
	}
	if in.Timezone != "" {
		filled++
	}
	if in.Country != "" {
		filled++
	}
	completeness := filled * 100 / completenessFields

	// Searchable only when reasonably complete AND identity-verified.
	kyc, err := s.store.KYCStatusOf(ctx, tx, in.UserID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	searchable := completeness >= 60 && kyc == "verified"

	p, err := s.store.UpsertFreelancerProfile(ctx, tx, store.FreelancerProfileInput{
		UserID:                in.UserID,
		Headline:              in.Headline,
		Bio:                   in.Bio,
		HourlyRateCents:       in.HourlyRateCents,
		Currency:              in.Currency,
		Experience:            in.Experience,
		AvailabilityHoursWeek: in.AvailabilityHoursWeek,
		Timezone:              in.Timezone,
		Country:               in.Country,
		Completeness:          completeness,
		IsSearchable:          searchable,
	})
	if err != nil {
		return nil, err
	}

	if err := outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "freelancer_profile", AggregateID: in.UserID, EventType: "profile.updated",
		Topic: "user.events", PartitionKey: in.UserID,
		Payload: map[string]any{
			"user_id":       in.UserID,
			"completeness":  completeness,
			"is_searchable": searchable,
			"kind":          "freelancer",
		},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) GetFreelancerProfile(ctx context.Context, userID string) (*store.FreelancerProfile, error) {
	p, err := s.store.GetFreelancerProfile(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	return p, err
}

// ClientProfileInput is the upsert request after transport decoding.
type ClientProfileInput struct {
	UserID      string
	CompanyName string
	Website     string
	Industry    string
	CompanySize string
	Country     string
}

func (s *Service) UpsertClientProfile(ctx context.Context, in ClientProfileInput) (*store.ClientProfile, error) {
	if in.UserID == "" {
		return nil, ErrValidation
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	p, err := s.store.UpsertClientProfile(ctx, tx, store.ClientProfileInput{
		UserID:      in.UserID,
		CompanyName: in.CompanyName,
		Website:     in.Website,
		Industry:    in.Industry,
		CompanySize: in.CompanySize,
		Country:     in.Country,
	})
	if err != nil {
		return nil, err
	}
	if err := outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "client_profile", AggregateID: in.UserID, EventType: "profile.updated",
		Topic: "user.events", PartitionKey: in.UserID,
		Payload: map[string]any{"user_id": in.UserID, "kind": "client"},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) GetClientProfile(ctx context.Context, userID string) (*store.ClientProfile, error) {
	p, err := s.store.GetClientProfile(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	return p, err
}

// ── Skills ──────────────────────────────────────────────────────────────────

func (s *Service) AddFreelancerSkill(ctx context.Context, userID, skillID, level string, years *float64) error {
	if userID == "" || skillID == "" {
		return ErrValidation
	}
	if level == "" {
		level = "intermediate"
	}
	if !validExperience[level] {
		return ErrValidation
	}
	return s.store.AddFreelancerSkill(ctx, userID, skillID, level, years)
}

// SetFreelancerSkills replaces a freelancer's skill set with the given ids.
func (s *Service) SetFreelancerSkills(ctx context.Context, userID string, skillIDs []string, level string) error {
	if userID == "" {
		return ErrValidation
	}
	if level == "" {
		level = "intermediate"
	}
	if !validExperience[level] {
		return ErrValidation
	}
	return s.store.SetFreelancerSkills(ctx, userID, skillIDs, level)
}

func (s *Service) ListFreelancerSkills(ctx context.Context, userID string) ([]store.FreelancerSkill, error) {
	return s.store.ListFreelancerSkills(ctx, userID)
}

// ── Portfolio ───────────────────────────────────────────────────────────────

func (s *Service) AddPortfolioItem(ctx context.Context, userID, title, description, url string, imageKeys, skills []string) (*store.PortfolioItem, error) {
	if userID == "" || title == "" {
		return nil, ErrValidation
	}
	return s.store.AddPortfolioItem(ctx, userID, title, description, url, imageKeys, skills)
}

func (s *Service) ListPortfolioItems(ctx context.Context, userID string) ([]store.PortfolioItem, error) {
	return s.store.ListPortfolioItems(ctx, userID)
}

// ── Devices ─────────────────────────────────────────────────────────────────

const (
	// deviceKindTracker is the only kind enrolled through this endpoint; browser/mobile
	// devices are recorded by the auth service during normal session creation.
	deviceKindTracker = "desktop_tracker"
	// ed25519PublicKeySize is the expected length of the attestation public key.
	ed25519PublicKeySize = 32
)

// RegisterDevice enrolls (or re-enrolls) a desktop-tracker device for the user, storing the
// Ed25519 attestation public key the screenshot service verifies signatures against. It is an
// upsert keyed by (user_id, fingerprint) so repeated logins on the same machine are idempotent.
func (s *Service) RegisterDevice(ctx context.Context, userID, fingerprint, displayName string, attestationPubkey []byte) (*store.Device, error) {
	if userID == "" || fingerprint == "" {
		return nil, ErrValidation
	}
	// Enforce a well-formed Ed25519 key so we never enroll a key the screenshot
	// service would later reject — failing closed at enrollment, not at confirm time.
	if len(attestationPubkey) != ed25519PublicKeySize {
		return nil, ErrValidation
	}
	if displayName == "" {
		displayName = "Desktop Tracker"
	}
	return s.store.RegisterDevice(ctx, userID, fingerprint, deviceKindTracker, displayName, attestationPubkey)
}

// ListDevices returns the devices enrolled for the user.
func (s *Service) ListDevices(ctx context.Context, userID string) ([]store.Device, error) {
	if userID == "" {
		return nil, ErrValidation
	}
	return s.store.ListDevices(ctx, userID)
}

// ── KYC ─────────────────────────────────────────────────────────────────────

// SetKYCStatus updates a freelancer's verification status and emits user.kyc_updated.
func (s *Service) SetKYCStatus(ctx context.Context, userID, status, provider, providerRef string) error {
	if userID == "" {
		return ErrValidation
	}
	if !validKYCStatuses[status] {
		return ErrKYCNotAllowed
	}
	if provider == "" {
		provider = "sumsub"
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := s.store.SetKYCStatus(ctx, tx, userID, status, provider, providerRef); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	if err := outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "kyc", AggregateID: userID, EventType: "user.kyc_updated",
		Topic: "user.events", PartitionKey: userID,
		Payload: map[string]any{"user_id": userID, "status": status, "provider": provider},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CheckPermission is an internal authorization helper exposed to the gateway/other
// services. It delegates to the RBAC principal's permission set.
func (s *Service) CheckPermission(ctx context.Context, p rbac.Principal, perm string) bool {
	_ = ctx
	return p.Can(perm)
}
