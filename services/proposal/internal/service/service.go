// Package service holds the proposal business logic: submitting bids (with the
// one-active-proposal-per-project-freelancer rule), withdrawal, and client-side
// shortlisting. Every state change and the event it emits commit in one transaction
// via the outbox pattern. Transport (HTTP) is a thin adapter over these methods.
package service

import (
	"context"
	"errors"

	"github.com/aizorix/platform/proposal/internal/store"
	"github.com/aizorix/platform/pkg/outbox"
	"github.com/aizorix/platform/pkg/rbac"
)

var (
	// ErrInvalidBid is returned when bid_amount_cents is not strictly positive.
	ErrInvalidBid = errors.New("proposal: bid amount must be positive")
	// ErrDuplicateProposal maps a UNIQUE(project_id,freelancer_id) violation.
	ErrDuplicateProposal = errors.New("proposal: already submitted for this project")
	// ErrInvalidState is returned when a transition is not allowed from the current status.
	ErrInvalidState = errors.New("proposal: invalid state for this operation")
	// ErrNotFound re-exports the store sentinel for transport mapping.
	ErrNotFound = store.ErrNotFound
)

type Service struct{ store *store.Store }

func New(st *store.Store) *Service { return &Service{store: st} }

// ── input DTOs ──────────────────────────────────────────────────────────────

type MilestoneInput struct {
	Seq         int
	Title       string
	AmountCents int64
	DueDays     *int
}

type AnswerInput struct {
	Question string
	Answer   string
}

type SubmitInput struct {
	ProjectID             string
	FreelancerID          string
	CoverLetter           string
	BidAmountCents        int64
	Currency              string
	EstimatedDurationDays *int
	ConnectsSpent         int
	Milestones            []MilestoneInput
	Answers               []AnswerInput
}

// ── output views ────────────────────────────────────────────────────────────

// ProposalView bundles a proposal with its milestones for GetProposal.
type ProposalView struct {
	Proposal   store.Proposal
	Milestones []store.Milestone
}

// SubmitProposal inserts a proposal plus its milestones and answers in one transaction,
// then enqueues a proposal.submitted event. The one-active-proposal rule is enforced by
// the UNIQUE(project_id,freelancer_id) constraint, surfaced here as ErrDuplicateProposal.
func (s *Service) SubmitProposal(ctx context.Context, in SubmitInput) (*store.Proposal, error) {
	if in.BidAmountCents <= 0 {
		return nil, ErrInvalidBid
	}
	if in.Currency == "" {
		in.Currency = "USD"
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	id, err := s.store.InsertProposal(ctx, tx, store.Proposal{
		ProjectID:             in.ProjectID,
		FreelancerID:          in.FreelancerID,
		CoverLetter:           in.CoverLetter,
		BidAmountCents:        in.BidAmountCents,
		Currency:              in.Currency,
		EstimatedDurationDays: in.EstimatedDurationDays,
		ConnectsSpent:         in.ConnectsSpent,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicateProposal
		}
		return nil, err
	}
	for _, m := range in.Milestones {
		if err := s.store.InsertMilestone(ctx, tx, id, store.Milestone{
			Seq: m.Seq, Title: m.Title, AmountCents: m.AmountCents, DueDays: m.DueDays,
		}); err != nil {
			return nil, err
		}
	}
	for _, a := range in.Answers {
		if err := s.store.InsertAnswer(ctx, tx, id, store.Answer{
			Question: a.Question, Answer: a.Answer,
		}); err != nil {
			return nil, err
		}
	}
	if err := outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "proposal", AggregateID: id, EventType: "proposal.submitted",
		Topic: "proposal.events", PartitionKey: in.ProjectID,
		Payload: map[string]any{
			"proposal_id": id, "project_id": in.ProjectID,
			"freelancer_id": in.FreelancerID, "bid_amount_cents": in.BidAmountCents,
		},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &store.Proposal{
		ID: id, ProjectID: in.ProjectID, FreelancerID: in.FreelancerID,
		CoverLetter: in.CoverLetter, BidAmountCents: in.BidAmountCents, Currency: in.Currency,
		EstimatedDurationDays: in.EstimatedDurationDays, Status: "submitted", ConnectsSpent: in.ConnectsSpent,
	}, nil
}

// WithdrawProposal lets the owning freelancer withdraw while still submitted/shortlisted.
func (s *Service) WithdrawProposal(ctx context.Context, id, freelancerID string) error {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	ok, err := s.store.WithdrawProposal(ctx, tx, id, freelancerID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidState
	}
	if err := outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "proposal", AggregateID: id, EventType: "proposal.withdrawn",
		Topic: "proposal.events", PartitionKey: id,
		Payload: map[string]any{"proposal_id": id, "freelancer_id": freelancerID},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ShortlistProposal moves a submitted proposal to shortlisted. Only the client who owns
// the project the proposal was submitted to may shortlist it; we verify ownership against
// the projects table (shared dev schema) and return rbac.ErrForbidden otherwise.
func (s *Service) ShortlistProposal(ctx context.Context, id, actorClientID string) error {
	if actorClientID == "" {
		return rbac.ErrForbidden
	}
	ownerClientID, err := s.store.ProjectClientOfProposal(ctx, id)
	if err != nil {
		return err // ErrNotFound surfaces as 404 in transport
	}
	if ownerClientID != actorClientID {
		return rbac.ErrForbidden
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	ok, err := s.store.ShortlistProposal(ctx, tx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidState
	}
	if err := outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "proposal", AggregateID: id, EventType: "proposal.shortlisted",
		Topic: "proposal.events", PartitionKey: id,
		Payload: map[string]any{"proposal_id": id, "client_id": actorClientID},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GetProposal returns a proposal with its milestones.
func (s *Service) GetProposal(ctx context.Context, id string) (*ProposalView, error) {
	p, err := s.store.GetProposal(ctx, id)
	if err != nil {
		return nil, err
	}
	ms, err := s.store.ListMilestones(ctx, id)
	if err != nil {
		return nil, err
	}
	return &ProposalView{Proposal: *p, Milestones: ms}, nil
}

// ListProposalsForProject lists proposals on a project, optionally filtered by status.
// Only the client who owns the project may view its proposals (otherwise competitors'
// bids would leak); ownership is verified against the projects table exactly as
// ShortlistProposal does, returning rbac.ErrForbidden for non-owners.
func (s *Service) ListProposalsForProject(ctx context.Context, projectID, status, actorClientID string) ([]store.Proposal, error) {
	if actorClientID == "" {
		return nil, rbac.ErrForbidden
	}
	ownerClientID, err := s.store.ProjectClientOf(ctx, projectID)
	if err != nil {
		return nil, err // ErrNotFound surfaces as 404 in transport
	}
	if ownerClientID != actorClientID {
		return nil, rbac.ErrForbidden
	}
	return s.store.ListForProject(ctx, projectID, status)
}

// ListProposalsForFreelancer lists every proposal authored by a freelancer.
func (s *Service) ListProposalsForFreelancer(ctx context.Context, freelancerID string) ([]store.Proposal, error) {
	return s.store.ListForFreelancer(ctx, freelancerID)
}

// ── helpers ─────────────────────────────────────────────────────────────────

// isUniqueViolation detects SQLSTATE 23505. Checked via error string here to avoid
// importing pgconn; production code uses errors.As(*pgconn.PgError).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return contains(s, "23505") || contains(s, "proposals_project_id_freelancer_id_key")
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
