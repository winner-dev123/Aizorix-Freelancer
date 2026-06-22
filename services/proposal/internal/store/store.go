// Package store is the proposal service's data-access layer over PostgreSQL (pgx).
// It owns the proposals, proposal_milestones and proposal_answers tables (migration
// 000004 proposal part). Write methods take a pgx.Tx so a proposal and its child rows
// (and the outbox event) commit atomically.
package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a row lookup yields pgx.ErrNoRows.
var ErrNotFound = errors.New("store: not found")

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool exposes the pool for transactions spanning store + outbox.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// ── domain types ────────────────────────────────────────────────────────────

type Proposal struct {
	ID                   string
	ProjectID            string
	FreelancerID         string
	CoverLetter          string
	BidAmountCents       int64
	Currency             string
	EstimatedDurationDays *int // nullable
	Status               string
	ConnectsSpent        int
}

type Milestone struct {
	ID          string
	ProposalID  string
	Seq         int
	Title       string
	AmountCents int64
	DueDays     *int // nullable
}

type Answer struct {
	ID         string
	ProposalID string
	Question   string
	Answer     string
}

// ── writes (tx-taking) ──────────────────────────────────────────────────────

// InsertProposal inserts the parent proposal row and returns its generated id.
func (s *Store) InsertProposal(ctx context.Context, tx pgx.Tx, p Proposal) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO proposals
			(project_id, freelancer_id, cover_letter, bid_amount_cents, currency,
			 estimated_duration_days, connects_spent, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'submitted')
		RETURNING id`,
		p.ProjectID, p.FreelancerID, p.CoverLetter, p.BidAmountCents, p.Currency,
		p.EstimatedDurationDays, p.ConnectsSpent).Scan(&id)
	return id, err
}

// InsertMilestone inserts one proposal_milestones row inside tx.
func (s *Store) InsertMilestone(ctx context.Context, tx pgx.Tx, proposalID string, m Milestone) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO proposal_milestones (proposal_id, seq, title, amount_cents, due_days)
		VALUES ($1,$2,$3,$4,$5)`,
		proposalID, m.Seq, m.Title, m.AmountCents, m.DueDays)
	return err
}

// InsertAnswer inserts one proposal_answers row inside tx.
func (s *Store) InsertAnswer(ctx context.Context, tx pgx.Tx, proposalID string, a Answer) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO proposal_answers (proposal_id, question, answer)
		VALUES ($1,$2,$3)`,
		proposalID, a.Question, a.Answer)
	return err
}

// WithdrawProposal transitions an owned proposal to 'withdrawn'. The status guard is in
// the WHERE clause so the transition is atomic; RowsAffected==0 means the guard failed.
func (s *Store) WithdrawProposal(ctx context.Context, tx pgx.Tx, id, freelancerID string) (bool, error) {
	ct, err := tx.Exec(ctx, `
		UPDATE proposals
		SET status = 'withdrawn', withdrawn_at = now()
		WHERE id = $1 AND freelancer_id = $2 AND status IN ('submitted','shortlisted')`,
		id, freelancerID)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// ShortlistProposal transitions a 'submitted' proposal to 'shortlisted'.
func (s *Store) ShortlistProposal(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	ct, err := tx.Exec(ctx, `
		UPDATE proposals
		SET status = 'shortlisted'
		WHERE id = $1 AND status = 'submitted'`, id)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// ── reads ───────────────────────────────────────────────────────────────────

// ProjectClientOfProposal returns the client_id of the project the proposal belongs to.
// The projects table lives in the same shared dev schema (owned by the project service),
// so this lightweight read lets the proposal service authorize client-side actions
// (shortlisting) without a cross-service call. Returns ErrNotFound if the proposal
// (or its project) does not exist.
func (s *Store) ProjectClientOfProposal(ctx context.Context, proposalID string) (string, error) {
	var clientID string
	err := s.pool.QueryRow(ctx, `
		SELECT pr.client_id
		FROM proposals p
		JOIN projects pr ON pr.id = p.project_id
		WHERE p.id = $1`, proposalID).Scan(&clientID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return clientID, nil
}

// ProjectClientOf returns the client_id that owns the given project. Like
// ProjectClientOfProposal it reads the shared-schema projects table so the proposal
// service can authorize client-side actions without a cross-service call. Returns
// ErrNotFound if the project does not exist.
func (s *Store) ProjectClientOf(ctx context.Context, projectID string) (string, error) {
	var clientID string
	err := s.pool.QueryRow(ctx, `
		SELECT client_id FROM projects WHERE id = $1`, projectID).Scan(&clientID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return clientID, nil
}

// GetProposal returns the proposal by id (without children).
func (s *Store) GetProposal(ctx context.Context, id string) (*Proposal, error) {
	p := &Proposal{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, project_id, freelancer_id, cover_letter, bid_amount_cents, currency,
		       estimated_duration_days, status, connects_spent
		FROM proposals WHERE id = $1`, id).
		Scan(&p.ID, &p.ProjectID, &p.FreelancerID, &p.CoverLetter, &p.BidAmountCents,
			&p.Currency, &p.EstimatedDurationDays, &p.Status, &p.ConnectsSpent)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ListMilestones returns the milestones of a proposal ordered by seq.
func (s *Store) ListMilestones(ctx context.Context, proposalID string) ([]Milestone, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, proposal_id, seq, title, amount_cents, due_days
		FROM proposal_milestones WHERE proposal_id = $1 ORDER BY seq`, proposalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Milestone
	for rows.Next() {
		var m Milestone
		if err := rows.Scan(&m.ID, &m.ProposalID, &m.Seq, &m.Title, &m.AmountCents, &m.DueDays); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListForProject lists proposals on a project, optionally filtered by status.
func (s *Store) ListForProject(ctx context.Context, projectID, status string) ([]Proposal, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, project_id, freelancer_id, cover_letter, bid_amount_cents, currency,
		       estimated_duration_days, status, connects_spent
		FROM proposals
		WHERE project_id = $1 AND ($2 = '' OR status = $2)
		ORDER BY submitted_at DESC`, projectID, status)
	if err != nil {
		return nil, err
	}
	return scanProposals(rows)
}

// ListForFreelancer lists every proposal authored by a freelancer.
func (s *Store) ListForFreelancer(ctx context.Context, freelancerID string) ([]Proposal, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, project_id, freelancer_id, cover_letter, bid_amount_cents, currency,
		       estimated_duration_days, status, connects_spent
		FROM proposals
		WHERE freelancer_id = $1
		ORDER BY submitted_at DESC`, freelancerID)
	if err != nil {
		return nil, err
	}
	return scanProposals(rows)
}

func scanProposals(rows pgx.Rows) ([]Proposal, error) {
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		var p Proposal
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.FreelancerID, &p.CoverLetter,
			&p.BidAmountCents, &p.Currency, &p.EstimatedDurationDays, &p.Status, &p.ConnectsSpent); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
