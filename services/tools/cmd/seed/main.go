// Command seed populates a local Aizorix database with a small, coherent demo
// dataset so the platform can be exercised end-to-end without manually clicking
// through registration, posting projects, bidding, hiring, and funding escrow.
//
// It is idempotent: re-running it will not create duplicates. Rows are matched by
// natural keys (email, skill slug, project title, etc.) and inserted with
// ON CONFLICT / existence guards. Passwords are hashed with the platform's
// pkg/crypto.HashPassword (Argon2id) so the seeded accounts can actually log in
// through the auth service.
//
// Usage:
//
//	DATABASE_URL=postgres://aizorix:aizorix_dev@localhost:5432/aizorix?sslmode=disable \
//	  go run ./cmd/seed
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aizorix/platform/pkg/crypto"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// demoPassword is shared by every seeded account. It satisfies the auth service's
// 12-character minimum so login works out of the box. Obviously dev-only.
const demoPassword = "DemoPassw0rd!"

// account describes one user to seed plus the profile/role wiring it needs.
type account struct {
	key         string // stable label used in the printed summary
	email       string
	accountType string // users.primary_type + drives the role granted
	role        string // roles.name to grant via user_roles
	status      string // users.status
}

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required (e.g. postgres://aizorix:aizorix_dev@localhost:5432/aizorix?sslmode=disable)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("ping: %v", err)
	}

	s := &seeder{ctx: ctx, pool: pool, ids: map[string]string{}}
	if err := s.run(); err != nil {
		log.Fatalf("seed failed: %v", err)
	}
	s.printSummary()
}

type seeder struct {
	ctx  context.Context
	pool *pgxpool.Pool
	ids  map[string]string // human label -> uuid, for the summary
}

func (s *seeder) run() error {
	// Hash the demo password once; every account reuses params-embedded hash.
	hash, err := crypto.HashPassword(demoPassword, crypto.DefaultArgon2Params())
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	tx, err := s.pool.Begin(s.ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(s.ctx) }()

	// ── Users + roles + profiles ──────────────────────────────────────────────
	accounts := []account{
		{"admin", "admin@aizorix.dev", "admin", "admin", "active"},
		{"client1", "acme@aizorix.dev", "client", "client", "active"},
		{"client2", "globex@aizorix.dev", "client", "client", "active"},
		{"freelancer1", "ada@aizorix.dev", "freelancer", "freelancer", "active"},
		{"freelancer2", "linus@aizorix.dev", "freelancer", "freelancer", "active"},
		{"freelancer3", "grace@aizorix.dev", "freelancer", "freelancer", "active"},
	}
	userIDs := map[string]string{}
	for _, a := range accounts {
		id, err := upsertUser(s.ctx, tx, a, hash)
		if err != nil {
			return fmt.Errorf("user %s: %w", a.email, err)
		}
		userIDs[a.key] = id
		s.ids["user:"+a.key+" ("+a.email+")"] = id
		if err := grantRole(s.ctx, tx, id, a.role); err != nil {
			return fmt.Errorf("grant role %s to %s: %w", a.role, a.email, err)
		}
	}

	// Client profiles.
	if err := upsertClientProfile(s.ctx, tx, userIDs["client1"], "Acme Corp", "https://acme.example", "Software"); err != nil {
		return fmt.Errorf("client1 profile: %w", err)
	}
	if err := upsertClientProfile(s.ctx, tx, userIDs["client2"], "Globex Inc", "https://globex.example", "Logistics"); err != nil {
		return fmt.Errorf("client2 profile: %w", err)
	}

	// Freelancer profiles (rate in cents).
	freelancers := []struct {
		key, headline, bio string
		rateCents          int64
		exp                string
	}{
		{"freelancer1", "Senior Go Backend Engineer", "Distributed systems and payments.", 9000, "expert"},
		{"freelancer2", "Full-stack React + Go Developer", "Product engineering, dashboards.", 7000, "intermediate"},
		{"freelancer3", "Cloud & DevOps Specialist", "Kubernetes, CI/CD, observability.", 8500, "expert"},
	}
	for _, f := range freelancers {
		if err := upsertFreelancerProfile(s.ctx, tx, userIDs[f.key], f.headline, f.bio, f.rateCents, f.exp); err != nil {
			return fmt.Errorf("%s profile: %w", f.key, err)
		}
	}

	// ── Skills + freelancer_skills ────────────────────────────────────────────
	skills := []struct{ slug, name, category string }{
		{"golang", "Go", "engineering"},
		{"react", "React", "engineering"},
		{"postgres", "PostgreSQL", "engineering"},
		{"kubernetes", "Kubernetes", "engineering"},
	}
	skillIDs := map[string]string{}
	for _, sk := range skills {
		id, err := upsertSkill(s.ctx, tx, sk.slug, sk.name, sk.category)
		if err != nil {
			return fmt.Errorf("skill %s: %w", sk.slug, err)
		}
		skillIDs[sk.slug] = id
	}
	s.ids["skill:golang"] = skillIDs["golang"]

	freelancerSkills := map[string][]string{
		"freelancer1": {"golang", "postgres"},
		"freelancer2": {"react", "golang"},
		"freelancer3": {"kubernetes", "postgres"},
	}
	for fk, slugs := range freelancerSkills {
		for _, slug := range slugs {
			if err := upsertFreelancerSkill(s.ctx, tx, userIDs[fk], skillIDs[slug]); err != nil {
				return fmt.Errorf("freelancer_skill %s/%s: %w", fk, slug, err)
			}
		}
	}

	// ── Projects (one fixed, one hourly) ──────────────────────────────────────
	fixedProjectID, err := upsertProject(s.ctx, tx, projectSpec{
		clientID:    userIDs["client1"],
		title:       "Build a payments reconciliation service",
		description: "Design and implement a double-entry ledger reconciliation job in Go against PostgreSQL.",
		budgetType:  "fixed",
		budgetMin:   ptr(int64(400000)),
		budgetMax:   ptr(int64(400000)),
		experience:  "expert",
		durationDays: ptr(30),
		status:      "published",
	})
	if err != nil {
		return fmt.Errorf("fixed project: %w", err)
	}
	s.ids["project:fixed"] = fixedProjectID

	hourlyProjectID, err := upsertProject(s.ctx, tx, projectSpec{
		clientID:        userIDs["client2"],
		title:           "Ongoing React dashboard development",
		description:     "Build and maintain an internal analytics dashboard. Ongoing hourly engagement.",
		budgetType:      "hourly",
		budgetMin:       ptr(int64(6000)),
		budgetMax:       ptr(int64(9000)),
		weeklyHourLimit: ptr(30),
		experience:      "intermediate",
		status:          "published",
	})
	if err != nil {
		return fmt.Errorf("hourly project: %w", err)
	}
	s.ids["project:hourly"] = hourlyProjectID

	// Attach relevant skills to projects.
	if err := upsertProjectSkill(s.ctx, tx, fixedProjectID, skillIDs["golang"]); err != nil {
		return fmt.Errorf("fixed project skill: %w", err)
	}
	if err := upsertProjectSkill(s.ctx, tx, hourlyProjectID, skillIDs["react"]); err != nil {
		return fmt.Errorf("hourly project skill: %w", err)
	}

	// ── Proposals ─────────────────────────────────────────────────────────────
	// Fixed project: freelancer1 (accepted) + freelancer3 (submitted).
	fixedProposalID, err := upsertProposal(s.ctx, tx, proposalSpec{
		projectID:    fixedProjectID,
		freelancerID: userIDs["freelancer1"],
		coverLetter:  "I have shipped several ledger systems and can deliver this in four weeks.",
		bidCents:     400000,
		durationDays: ptr(28),
		status:       "accepted",
	})
	if err != nil {
		return fmt.Errorf("fixed proposal accepted: %w", err)
	}
	s.ids["proposal:fixed(accepted)"] = fixedProposalID

	if _, err := upsertProposal(s.ctx, tx, proposalSpec{
		projectID:    fixedProjectID,
		freelancerID: userIDs["freelancer3"],
		coverLetter:  "Happy to bring DevOps rigor and reconciliation experience.",
		bidCents:     420000,
		durationDays: ptr(35),
		status:       "submitted",
	}); err != nil {
		return fmt.Errorf("fixed proposal submitted: %w", err)
	}

	// Hourly project: freelancer2 (accepted).
	hourlyProposalID, err := upsertProposal(s.ctx, tx, proposalSpec{
		projectID:    hourlyProjectID,
		freelancerID: userIDs["freelancer2"],
		coverLetter:  "React + Go specialist, available 30 hrs/week immediately.",
		bidCents:     7000, // proposed hourly rate (cents)
		status:       "accepted",
	})
	if err != nil {
		return fmt.Errorf("hourly proposal: %w", err)
	}
	s.ids["proposal:hourly(accepted)"] = hourlyProposalID

	// ── Contracts ─────────────────────────────────────────────────────────────
	// Fixed contract with milestones, pending->active funding represented as active.
	fixedTotal := int64(400000)
	fixedContractID, err := upsertContract(s.ctx, tx, contractSpec{
		projectID:    fixedProjectID,
		proposalID:   fixedProposalID,
		clientID:     userIDs["client1"],
		freelancerID: userIDs["freelancer1"],
		budgetType:   "fixed",
		totalCents:   &fixedTotal,
		status:       "active",
	})
	if err != nil {
		return fmt.Errorf("fixed contract: %w", err)
	}
	s.ids["contract:fixed"] = fixedContractID

	// Milestones for the fixed contract.
	milestones := []struct {
		seq    int
		title  string
		cents  int64
		status string
	}{
		{1, "Schema + ingestion", 150000, "released"},
		{2, "Reconciliation engine", 150000, "funded"},
		{3, "Reporting + handover", 100000, "pending"},
	}
	for _, m := range milestones {
		if err := upsertMilestone(s.ctx, tx, fixedContractID, m.seq, m.title, m.cents, m.status); err != nil {
			return fmt.Errorf("milestone %d: %w", m.seq, err)
		}
	}

	// Hourly contract (active) + hourly_contracts config row.
	hourlyRate := int64(7000)
	weeklyLimit := 30
	hourlyContractID, err := upsertContract(s.ctx, tx, contractSpec{
		projectID:       hourlyProjectID,
		proposalID:      hourlyProposalID,
		clientID:        userIDs["client2"],
		freelancerID:    userIDs["freelancer2"],
		budgetType:      "hourly",
		hourlyRate:      &hourlyRate,
		weeklyHourLimit: &weeklyLimit,
		status:          "active",
	})
	if err != nil {
		return fmt.Errorf("hourly contract: %w", err)
	}
	s.ids["contract:hourly"] = hourlyContractID
	if err := upsertHourlyConfig(s.ctx, tx, hourlyContractID); err != nil {
		return fmt.Errorf("hourly config: %w", err)
	}

	// ── Escrow: fund the fixed contract's escrow account ──────────────────────
	// held = milestone 2 (funded), released = milestone 1 (released).
	escrowID, err := upsertEscrow(s.ctx, tx, fixedContractID, 150000, 150000)
	if err != nil {
		return fmt.Errorf("escrow: %w", err)
	}
	s.ids["escrow:fixed"] = escrowID

	// ── Notifications ─────────────────────────────────────────────────────────
	notes := []struct {
		userKey, ntype, title, body string
	}{
		{"freelancer1", "proposal.accepted", "Your proposal was accepted", "Acme Corp accepted your proposal for the reconciliation service."},
		{"client1", "milestone.released", "Milestone released", "Funds for 'Schema + ingestion' were released."},
		{"freelancer2", "contract.activated", "Contract activated", "Your hourly contract with Globex Inc is now active."},
	}
	for i, n := range notes {
		if err := upsertNotification(s.ctx, tx, userIDs[n.userKey], n.ntype, n.title, n.body, i); err != nil {
			return fmt.Errorf("notification %s: %w", n.ntype, err)
		}
	}

	if err := tx.Commit(s.ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// ── Upsert helpers ────────────────────────────────────────────────────────────

func upsertUser(ctx context.Context, tx pgx.Tx, a account, hash string) (string, error) {
	var id string
	// Idempotent on the case-insensitive active-email unique index.
	err := tx.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, status, primary_type, email_verified)
		VALUES ($1, $2, $3::user_status, $4::account_type, true)
		ON CONFLICT (email) WHERE deleted_at IS NULL
		DO UPDATE SET password_hash = EXCLUDED.password_hash,
		              status = EXCLUDED.status,
		              primary_type = EXCLUDED.primary_type
		RETURNING id`,
		a.email, hash, a.status, a.accountType).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, nil
}

func grantRole(ctx context.Context, tx pgx.Tx, userID, roleName string) error {
	// roles are pre-seeded by migration 000002; scope defaults to '' for the PK.
	_, err := tx.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id, scope)
		SELECT $1, r.id, '' FROM roles r WHERE r.name = $2
		ON CONFLICT (user_id, role_id, scope) DO NOTHING`,
		userID, roleName)
	return err
}

func upsertClientProfile(ctx context.Context, tx pgx.Tx, userID, company, website, industry string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO client_profiles (user_id, company_name, website, industry, payment_verified)
		VALUES ($1, $2, $3, $4, true)
		ON CONFLICT (user_id) DO UPDATE SET company_name = EXCLUDED.company_name,
		                                    website = EXCLUDED.website,
		                                    industry = EXCLUDED.industry`,
		userID, company, website, industry)
	return err
}

func upsertFreelancerProfile(ctx context.Context, tx pgx.Tx, userID, headline, bio string, rateCents int64, exp string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO freelancer_profiles (user_id, headline, bio, hourly_rate_cents, experience, is_searchable, profile_completeness)
		VALUES ($1, $2, $3, $4, $5::experience_level, true, 80)
		ON CONFLICT (user_id) DO UPDATE SET headline = EXCLUDED.headline,
		                                    bio = EXCLUDED.bio,
		                                    hourly_rate_cents = EXCLUDED.hourly_rate_cents,
		                                    experience = EXCLUDED.experience`,
		userID, headline, bio, rateCents, exp)
	return err
}

func upsertSkill(ctx context.Context, tx pgx.Tx, slug, name, category string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO skills (slug, name, category) VALUES ($1, $2, $3)
		ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name, category = EXCLUDED.category
		RETURNING id`, slug, name, category).Scan(&id)
	return id, err
}

func upsertFreelancerSkill(ctx context.Context, tx pgx.Tx, userID, skillID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO freelancer_skills (user_id, skill_id, level) VALUES ($1, $2, 'expert')
		ON CONFLICT (user_id, skill_id) DO NOTHING`, userID, skillID)
	return err
}

type projectSpec struct {
	clientID        string
	title           string
	description     string
	budgetType      string
	budgetMin       *int64
	budgetMax       *int64
	weeklyHourLimit *int
	experience      string
	durationDays    *int
	status          string
}

func upsertProject(ctx context.Context, tx pgx.Tx, p projectSpec) (string, error) {
	// No natural unique key on projects, so guard on (client_id, title).
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM projects WHERE client_id = $1 AND title = $2 AND deleted_at IS NULL`,
		p.clientID, p.title).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != pgx.ErrNoRows {
		return "", err
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO projects (client_id, title, description, budget_type, budget_min_cents,
		                      budget_max_cents, currency, weekly_hour_limit, experience_required,
		                      estimated_duration_days, status, published_at)
		VALUES ($1, $2, $3, $4::budget_type, $5, $6, 'USD', $7, $8::experience_level, $9, $10::project_status, now())
		RETURNING id`,
		p.clientID, p.title, p.description, p.budgetType, p.budgetMin, p.budgetMax,
		p.weeklyHourLimit, p.experience, p.durationDays, p.status).Scan(&id)
	return id, err
}

func upsertProjectSkill(ctx context.Context, tx pgx.Tx, projectID, skillID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO project_skills (project_id, skill_id) VALUES ($1, $2)
		ON CONFLICT (project_id, skill_id) DO NOTHING`, projectID, skillID)
	return err
}

type proposalSpec struct {
	projectID    string
	freelancerID string
	coverLetter  string
	bidCents     int64
	durationDays *int
	status       string
}

func upsertProposal(ctx context.Context, tx pgx.Tx, p proposalSpec) (string, error) {
	var id string
	// Unique (project_id, freelancer_id).
	err := tx.QueryRow(ctx, `
		INSERT INTO proposals (project_id, freelancer_id, cover_letter, bid_amount_cents,
		                       estimated_duration_days, status, decided_at)
		VALUES ($1, $2, $3, $4, $5, $6::proposal_status,
		        CASE WHEN $6 IN ('accepted','declined') THEN now() ELSE NULL END)
		ON CONFLICT (project_id, freelancer_id) DO UPDATE SET status = EXCLUDED.status,
		                                                      cover_letter = EXCLUDED.cover_letter
		RETURNING id`,
		p.projectID, p.freelancerID, p.coverLetter, p.bidCents, p.durationDays, p.status).Scan(&id)
	return id, err
}

type contractSpec struct {
	projectID       string
	proposalID      string
	clientID        string
	freelancerID    string
	budgetType      string
	totalCents      *int64
	hourlyRate      *int64
	weeklyHourLimit *int
	status          string
}

func upsertContract(ctx context.Context, tx pgx.Tx, c contractSpec) (string, error) {
	var id string
	// One contract per proposal in this demo; guard on proposal_id.
	err := tx.QueryRow(ctx, `SELECT id FROM contracts WHERE proposal_id = $1`, c.proposalID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != pgx.ErrNoRows {
		return "", err
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO contracts (project_id, proposal_id, client_id, freelancer_id, budget_type,
		                       total_amount_cents, hourly_rate_cents, weekly_hour_limit, status, started_at)
		VALUES ($1, $2, $3, $4, $5::budget_type, $6, $7, $8, $9::contract_status,
		        CASE WHEN $9 = 'active' THEN now() ELSE NULL END)
		RETURNING id`,
		c.projectID, c.proposalID, c.clientID, c.freelancerID, c.budgetType,
		c.totalCents, c.hourlyRate, c.weeklyHourLimit, c.status).Scan(&id)
	return id, err
}

func upsertMilestone(ctx context.Context, tx pgx.Tx, contractID string, seq int, title string, cents int64, status string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO milestones (contract_id, seq, title, amount_cents, status,
		                        funded_at, submitted_at, approved_at, released_at)
		VALUES ($1, $2, $3, $4, $5::milestone_status,
		        CASE WHEN $5 IN ('funded','submitted','approved','released') THEN now() ELSE NULL END,
		        CASE WHEN $5 IN ('submitted','approved','released') THEN now() ELSE NULL END,
		        CASE WHEN $5 IN ('approved','released') THEN now() ELSE NULL END,
		        CASE WHEN $5 = 'released' THEN now() ELSE NULL END)
		ON CONFLICT (contract_id, seq) DO UPDATE SET status = EXCLUDED.status,
		                                             title = EXCLUDED.title,
		                                             amount_cents = EXCLUDED.amount_cents`,
		contractID, seq, title, cents, status)
	return err
}

func upsertHourlyConfig(ctx context.Context, tx pgx.Tx, contractID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO hourly_contracts (contract_id, allow_manual_time, require_screenshots)
		VALUES ($1, false, true)
		ON CONFLICT (contract_id) DO NOTHING`, contractID)
	return err
}

func upsertEscrow(ctx context.Context, tx pgx.Tx, contractID string, heldCents, releasedCents int64) (string, error) {
	var id string
	status := "held"
	if releasedCents > 0 {
		status = "partially_released"
	}
	err := tx.QueryRow(ctx, `
		INSERT INTO escrow_accounts (contract_id, held_cents, released_cents, status)
		VALUES ($1, $2, $3, $4::escrow_status)
		ON CONFLICT (contract_id) DO UPDATE SET held_cents = EXCLUDED.held_cents,
		                                        released_cents = EXCLUDED.released_cents,
		                                        status = EXCLUDED.status
		RETURNING id`,
		contractID, heldCents, releasedCents, status).Scan(&id)
	return id, err
}

func upsertNotification(ctx context.Context, tx pgx.Tx, userID, ntype, title, body string, idx int) error {
	// No natural key; guard on (user_id, type, title) so re-runs don't duplicate.
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM notifications WHERE user_id = $1 AND type = $2 AND title = $3)`,
		userID, ntype, title).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO notifications (user_id, type, title, body, data)
		VALUES ($1, $2, $3, $4, jsonb_build_object('seed', true, 'idx', $5::int))`,
		userID, ntype, title, body, idx)
	return err
}

// ── Summary ───────────────────────────────────────────────────────────────────

func (s *seeder) printSummary() {
	fmt.Print("\n✔ Demo seed complete.\n\n")
	fmt.Println("Created / ensured entities:")
	// Print in a stable, readable order.
	order := []string{
		"user:admin (admin@aizorix.dev)",
		"user:client1 (acme@aizorix.dev)",
		"user:client2 (globex@aizorix.dev)",
		"user:freelancer1 (ada@aizorix.dev)",
		"user:freelancer2 (linus@aizorix.dev)",
		"user:freelancer3 (grace@aizorix.dev)",
		"skill:golang",
		"project:fixed",
		"project:hourly",
		"proposal:fixed(accepted)",
		"proposal:hourly(accepted)",
		"contract:fixed",
		"contract:hourly",
		"escrow:fixed",
	}
	for _, k := range order {
		if v, ok := s.ids[k]; ok {
			fmt.Printf("  %-32s %s\n", k, v)
		}
	}

	fmt.Println("\nDemo login credentials (all share the same password):")
	fmt.Printf("  password: %s\n\n", demoPassword)
	for _, l := range []struct{ role, email string }{
		{"admin", "admin@aizorix.dev"},
		{"client", "acme@aizorix.dev"},
		{"client", "globex@aizorix.dev"},
		{"freelancer", "ada@aizorix.dev"},
		{"freelancer", "linus@aizorix.dev"},
		{"freelancer", "grace@aizorix.dev"},
	} {
		fmt.Printf("  %-11s %s\n", l.role, l.email)
	}
	fmt.Println()
}

// ptr returns a pointer to v; handy for nullable scalar columns.
func ptr[T any](v T) *T { return &v }
