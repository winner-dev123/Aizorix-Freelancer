// Package store is the user service's data-access layer over PostgreSQL (pgx).
// It owns freelancer/client profiles, skills, portfolio items and KYC records.
// The users table itself is owned by the auth service; this service only references it.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("store: not found")

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool exposes the pool for transactions spanning store + outbox.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// ── Freelancer profiles ─────────────────────────────────────────────────────

type FreelancerProfile struct {
	UserID                string
	Headline              string
	Bio                   string
	HourlyRateCents       *int64
	Currency              string
	Experience            string
	AvailabilityHoursWeek *int
	Timezone              string
	Country               string
	KYCStatus             string
	RatingAvg             float64
	RatingCount           int
	TotalEarnedCents      int64
	JobsCompleted         int
	ProfileCompleteness   int
	IsSearchable          bool
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// FreelancerProfileInput carries the mutable core fields of an upsert.
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
	Completeness          int
	IsSearchable          bool
}

// UpsertFreelancerProfile inserts or updates the profile's core fields inside tx and
// returns the (re)computed completeness flag set. Encrypted PII columns are managed
// elsewhere and intentionally untouched here.
func (s *Store) UpsertFreelancerProfile(ctx context.Context, tx pgx.Tx, in FreelancerProfileInput) (*FreelancerProfile, error) {
	p := &FreelancerProfile{}
	err := tx.QueryRow(ctx, `
		INSERT INTO freelancer_profiles
			(user_id, headline, bio, hourly_rate_cents, currency, experience,
			 availability_hours_per_week, timezone, country, profile_completeness, is_searchable, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11, now())
		ON CONFLICT (user_id) DO UPDATE SET
			headline = EXCLUDED.headline,
			bio = EXCLUDED.bio,
			hourly_rate_cents = EXCLUDED.hourly_rate_cents,
			currency = EXCLUDED.currency,
			experience = EXCLUDED.experience,
			availability_hours_per_week = EXCLUDED.availability_hours_per_week,
			timezone = EXCLUDED.timezone,
			country = EXCLUDED.country,
			profile_completeness = EXCLUDED.profile_completeness,
			is_searchable = EXCLUDED.is_searchable,
			updated_at = now()
		RETURNING user_id, coalesce(headline,''), coalesce(bio,''), hourly_rate_cents,
			currency, experience::text, availability_hours_per_week, coalesce(timezone,''),
			coalesce(country,''), kyc_status::text, rating_avg, rating_count, total_earned_cents,
			jobs_completed, profile_completeness, is_searchable, created_at, updated_at`,
		in.UserID, in.Headline, in.Bio, in.HourlyRateCents, in.Currency, in.Experience,
		in.AvailabilityHoursWeek, in.Timezone, in.Country, in.Completeness, in.IsSearchable).
		Scan(&p.UserID, &p.Headline, &p.Bio, &p.HourlyRateCents, &p.Currency, &p.Experience,
			&p.AvailabilityHoursWeek, &p.Timezone, &p.Country, &p.KYCStatus, &p.RatingAvg,
			&p.RatingCount, &p.TotalEarnedCents, &p.JobsCompleted, &p.ProfileCompleteness,
			&p.IsSearchable, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Store) GetFreelancerProfile(ctx context.Context, userID string) (*FreelancerProfile, error) {
	p := &FreelancerProfile{}
	err := s.pool.QueryRow(ctx, `
		SELECT user_id, coalesce(headline,''), coalesce(bio,''), hourly_rate_cents,
			currency, experience::text, availability_hours_per_week, coalesce(timezone,''),
			coalesce(country,''), kyc_status::text, rating_avg, rating_count, total_earned_cents,
			jobs_completed, profile_completeness, is_searchable, created_at, updated_at
		FROM freelancer_profiles WHERE user_id = $1`, userID).
		Scan(&p.UserID, &p.Headline, &p.Bio, &p.HourlyRateCents, &p.Currency, &p.Experience,
			&p.AvailabilityHoursWeek, &p.Timezone, &p.Country, &p.KYCStatus, &p.RatingAvg,
			&p.RatingCount, &p.TotalEarnedCents, &p.JobsCompleted, &p.ProfileCompleteness,
			&p.IsSearchable, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// KYCStatusOf returns just the current kyc status, used during completeness recompute.
func (s *Store) KYCStatusOf(ctx context.Context, tx pgx.Tx, userID string) (string, error) {
	var status string
	err := tx.QueryRow(ctx, `SELECT kyc_status::text FROM freelancer_profiles WHERE user_id = $1`, userID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return status, err
}

// ── Client profiles ─────────────────────────────────────────────────────────

type ClientProfile struct {
	UserID          string
	CompanyName     string
	Website         string
	Industry        string
	CompanySize     string
	Country         string
	PaymentVerified bool
	TotalSpentCents int64
	HiresCount      int
	RatingAvg       *float64
	RatingCount     *int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type ClientProfileInput struct {
	UserID      string
	CompanyName string
	Website     string
	Industry    string
	CompanySize string
	Country     string
}

func (s *Store) UpsertClientProfile(ctx context.Context, tx pgx.Tx, in ClientProfileInput) (*ClientProfile, error) {
	p := &ClientProfile{}
	err := tx.QueryRow(ctx, `
		INSERT INTO client_profiles
			(user_id, company_name, website, industry, company_size, country, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6, now())
		ON CONFLICT (user_id) DO UPDATE SET
			company_name = EXCLUDED.company_name,
			website = EXCLUDED.website,
			industry = EXCLUDED.industry,
			company_size = EXCLUDED.company_size,
			country = EXCLUDED.country,
			updated_at = now()
		RETURNING user_id, coalesce(company_name,''), coalesce(website,''), coalesce(industry,''),
			coalesce(company_size,''), coalesce(country,''), payment_verified, total_spent_cents,
			hires_count, rating_avg, rating_count, created_at, updated_at`,
		in.UserID, in.CompanyName, in.Website, in.Industry, in.CompanySize, in.Country).
		Scan(&p.UserID, &p.CompanyName, &p.Website, &p.Industry, &p.CompanySize, &p.Country,
			&p.PaymentVerified, &p.TotalSpentCents, &p.HiresCount, &p.RatingAvg, &p.RatingCount,
			&p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Store) GetClientProfile(ctx context.Context, userID string) (*ClientProfile, error) {
	p := &ClientProfile{}
	err := s.pool.QueryRow(ctx, `
		SELECT user_id, coalesce(company_name,''), coalesce(website,''), coalesce(industry,''),
			coalesce(company_size,''), coalesce(country,''), payment_verified, total_spent_cents,
			hires_count, rating_avg, rating_count, created_at, updated_at
		FROM client_profiles WHERE user_id = $1`, userID).
		Scan(&p.UserID, &p.CompanyName, &p.Website, &p.Industry, &p.CompanySize, &p.Country,
			&p.PaymentVerified, &p.TotalSpentCents, &p.HiresCount, &p.RatingAvg, &p.RatingCount,
			&p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ── Freelancer skills ───────────────────────────────────────────────────────

type FreelancerSkill struct {
	SkillID  string
	Slug     string
	Name     string
	Category string
	Level    string
	Years    *float64
}

func (s *Store) AddFreelancerSkill(ctx context.Context, userID, skillID, level string, years *float64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO freelancer_skills (user_id, skill_id, level, years)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (user_id, skill_id) DO UPDATE SET level = EXCLUDED.level, years = EXCLUDED.years`,
		userID, skillID, level, years)
	return err
}

// SetFreelancerSkills replaces the entire skill set for a freelancer in one tx.
func (s *Store) SetFreelancerSkills(ctx context.Context, userID string, skillIDs []string, level string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM freelancer_skills WHERE user_id = $1`, userID); err != nil {
		return err
	}
	for _, sid := range skillIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO freelancer_skills (user_id, skill_id, level)
			VALUES ($1,$2,$3)
			ON CONFLICT (user_id, skill_id) DO NOTHING`, userID, sid, level); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ListFreelancerSkills(ctx context.Context, userID string) ([]FreelancerSkill, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.id, s.slug, s.name, coalesce(s.category,''), fs.level::text, fs.years
		FROM freelancer_skills fs JOIN skills s ON s.id = fs.skill_id
		WHERE fs.user_id = $1
		ORDER BY s.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FreelancerSkill
	for rows.Next() {
		var fs FreelancerSkill
		if err := rows.Scan(&fs.SkillID, &fs.Slug, &fs.Name, &fs.Category, &fs.Level, &fs.Years); err != nil {
			return nil, err
		}
		out = append(out, fs)
	}
	return out, rows.Err()
}

// ── Portfolio ───────────────────────────────────────────────────────────────

type PortfolioItem struct {
	ID          string
	UserID      string
	Title       string
	Description string
	URL         string
	ImageKeys   []string
	Skills      []string
	CreatedAt   time.Time
}

func (s *Store) AddPortfolioItem(ctx context.Context, userID, title, description, url string, imageKeys, skills []string) (*PortfolioItem, error) {
	it := &PortfolioItem{}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO portfolio_items (user_id, title, description, url, image_keys, skills)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING id, user_id, coalesce(title,''), coalesce(description,''), coalesce(url,''),
			coalesce(image_keys,'{}'), coalesce(skills,'{}'), created_at`,
		userID, title, description, url, imageKeys, skills).
		Scan(&it.ID, &it.UserID, &it.Title, &it.Description, &it.URL, &it.ImageKeys, &it.Skills, &it.CreatedAt)
	if err != nil {
		return nil, err
	}
	return it, nil
}

func (s *Store) ListPortfolioItems(ctx context.Context, userID string) ([]PortfolioItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, coalesce(title,''), coalesce(description,''), coalesce(url,''),
			coalesce(image_keys,'{}'), coalesce(skills,'{}'), created_at
		FROM portfolio_items
		WHERE user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PortfolioItem
	for rows.Next() {
		var it PortfolioItem
		if err := rows.Scan(&it.ID, &it.UserID, &it.Title, &it.Description, &it.URL,
			&it.ImageKeys, &it.Skills, &it.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ── Devices ─────────────────────────────────────────────────────────────────

// Device is an enrolled device row from the auth-owned `devices` table. The user service
// reaches into the shared dev schema to enroll the desktop tracker's signing key; in
// production this would be a call to the auth/identity service.
type Device struct {
	ID                string
	UserID            string
	Fingerprint       string
	Kind              string
	DisplayName       string
	AttestationPubkey []byte
	LastSeenAt        *time.Time
	Trusted           bool
	CreatedAt         time.Time
}

// RegisterDevice inserts or updates a device keyed by (user_id, fingerprint) and returns the
// device id. The desktop tracker calls this right after login to enroll its Ed25519 signing
// key, which the screenshot service later uses as the trust anchor for signature verification.
// last_seen_at is bumped on every (re)enrollment so the row doubles as a heartbeat.
func (s *Store) RegisterDevice(ctx context.Context, userID, fingerprint, kind, displayName string, attestationPubkey []byte) (*Device, error) {
	d := &Device{}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO devices (user_id, fingerprint, kind, display_name, attestation_pubkey, last_seen_at)
		VALUES ($1,$2,$3,$4,$5, now())
		ON CONFLICT (user_id, fingerprint) DO UPDATE SET
			kind = EXCLUDED.kind,
			display_name = EXCLUDED.display_name,
			attestation_pubkey = EXCLUDED.attestation_pubkey,
			last_seen_at = now()
		RETURNING id, user_id, fingerprint, kind, coalesce(display_name,''),
			attestation_pubkey, last_seen_at, trusted, created_at`,
		userID, fingerprint, kind, displayName, attestationPubkey).
		Scan(&d.ID, &d.UserID, &d.Fingerprint, &d.Kind, &d.DisplayName,
			&d.AttestationPubkey, &d.LastSeenAt, &d.Trusted, &d.CreatedAt)
	if err != nil {
		return nil, err
	}
	return d, nil
}

// ListDevices returns all devices enrolled for a user, most-recently-seen first.
func (s *Store) ListDevices(ctx context.Context, userID string) ([]Device, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, fingerprint, kind, coalesce(display_name,''),
			attestation_pubkey, last_seen_at, trusted, created_at
		FROM devices WHERE user_id = $1
		ORDER BY coalesce(last_seen_at, created_at) DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.UserID, &d.Fingerprint, &d.Kind, &d.DisplayName,
			&d.AttestationPubkey, &d.LastSeenAt, &d.Trusted, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ── KYC ─────────────────────────────────────────────────────────────────────

// SetKYCStatus updates the freelancer profile's status and upserts a kyc_records row,
// all inside tx so the outbox event is consistent with the state change.
func (s *Store) SetKYCStatus(ctx context.Context, tx pgx.Tx, userID, status, provider, providerRef string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE freelancer_profiles SET kyc_status = $2, updated_at = now() WHERE user_id = $1`,
		userID, status); err != nil {
		return err
	}
	reviewedAt := "now()"
	if status == "pending" || status == "not_started" {
		reviewedAt = "null"
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO kyc_records (user_id, provider, provider_ref, status, reviewed_at)
		VALUES ($1,$2,$3,$4, `+reviewedAt+`)`,
		userID, provider, providerRef, status)
	return err
}
