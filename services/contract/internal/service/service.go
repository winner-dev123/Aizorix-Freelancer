// Package service holds the contract business logic. The fixed-price milestone state
// machine is the core: pending -> funded -> submitted -> approved (-> released elsewhere).
// Every contract-level transition updates contracts.status AND appends a contract_events
// row atomically (event sourcing), and outward-facing transitions enqueue an outbox event
// in the same transaction. Transport (HTTP) is a thin adapter over these methods.
package service

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/aizorix/platform/contract/internal/proposallookup"
	"github.com/aizorix/platform/contract/internal/store"
	"github.com/aizorix/platform/pkg/outbox"
	"github.com/aizorix/platform/pkg/rbac"
)

var (
	// ErrInvalidParties is returned when the client and freelancer are the same user.
	ErrInvalidParties = errors.New("contract: client and freelancer must differ")
	// ErrNoMilestones is returned when a fixed-price contract has no milestones.
	ErrNoMilestones = errors.New("contract: fixed-price contract requires at least one milestone")
	// ErrInvalidState is returned when a contract-level transition is not allowed.
	ErrInvalidState = errors.New("contract: invalid state for this operation")
	// ErrInvalidMilestoneState is returned when a milestone transition is not allowed.
	ErrInvalidMilestoneState = errors.New("contract: invalid milestone state for this operation")
	// ErrNotFound re-exports the store sentinel for transport mapping.
	ErrNotFound = store.ErrNotFound
	// ErrForbidden re-exports the rbac sentinel so the transport maps a non-party caller to 403.
	ErrForbidden = rbac.ErrForbidden
	// ErrProposalRequired is returned when a contract is created without a proposal_id.
	ErrProposalRequired = errors.New("contract: proposal_id is required")
	// ErrProposalNotSelected is returned when the proposal is withdrawn/declined (not contractable).
	ErrProposalNotSelected = errors.New("contract: proposal is not in a contractable state")
	// ErrProposalMismatch is returned when the requested terms contradict the accepted proposal.
	ErrProposalMismatch = errors.New("contract: terms do not match the accepted proposal")
)

// ProposalLookup resolves the AUTHORITATIVE proposal a contract is formed from (its freelancer,
// bid amount, owning client, and status), so the request body cannot fabricate contract terms.
type ProposalLookup interface {
	Get(ctx context.Context, proposalID string) (proposallookup.Proposal, error)
}

type Service struct {
	store     *store.Store
	proposals ProposalLookup
}

func New(st *store.Store, proposals ProposalLookup) *Service {
	return &Service{store: st, proposals: proposals}
}

// ── input DTOs ──────────────────────────────────────────────────────────────

type CreateInput struct {
	ProjectID        string
	ProposalID       string
	ClientID         string
	FreelancerID     string
	BudgetType       string // 'fixed' | 'hourly'
	Currency         string
	TotalAmountCents *int64
	HourlyRateCents  *int64
	WeeklyHourLimit  *int
	PlatformFeeBps   int
	Milestones       []store.MilestoneInput
}

// ── output views ────────────────────────────────────────────────────────────

// ContractView bundles a contract with its milestones for GetContract.
type ContractView struct {
	Contract   store.Contract
	Milestones []store.Milestone
}

// CreateFromProposal inserts a contract (pending_funding), the hourly settings row or the
// fixed-price milestones, and the initial 'create' contract_events row — all in one tx.
func (s *Service) CreateFromProposal(ctx context.Context, in CreateInput) (*store.Contract, error) {
	if in.ProposalID == "" {
		return nil, ErrProposalRequired
	}
	if in.ClientID == "" {
		return nil, ErrInvalidParties
	}
	// Resolve the AUTHORITATIVE proposal. The request body is NOT trusted for who the freelancer
	// is, what the contract is worth, or the platform fee — all are derived from the proposal the
	// client accepted. Fail CLOSED: an unreachable proposal service or non-2xx denies the contract.
	prop, err := s.proposals.Get(ctx, in.ProposalID)
	if err != nil {
		if errors.Is(err, proposallookup.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, ErrForbidden
	}
	in, err = deriveContractTerms(in, prop)
	if err != nil {
		return nil, err
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	id, err := s.store.InsertContract(ctx, tx, store.Contract{
		ProjectID:        in.ProjectID,
		ProposalID:       in.ProposalID,
		ClientID:         in.ClientID,
		FreelancerID:     in.FreelancerID,
		BudgetType:       in.BudgetType,
		Currency:         in.Currency,
		TotalAmountCents: in.TotalAmountCents,
		HourlyRateCents:  in.HourlyRateCents,
		WeeklyHourLimit:  in.WeeklyHourLimit,
		PlatformFeeBps:   in.PlatformFeeBps,
	})
	if err != nil {
		return nil, err
	}
	if in.BudgetType == "hourly" {
		if err := s.store.InsertHourlyContract(ctx, tx, id); err != nil {
			return nil, err
		}
	} else {
		for _, m := range in.Milestones {
			if err := s.store.InsertMilestone(ctx, tx, id, m); err != nil {
				return nil, err
			}
		}
	}
	actor := in.ClientID
	if err := s.store.InsertContractEvent(ctx, tx, id, nil, "pending_funding", "create", &actor, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &store.Contract{
		ID: id, ProjectID: in.ProjectID, ProposalID: in.ProposalID, ClientID: in.ClientID,
		FreelancerID: in.FreelancerID, BudgetType: in.BudgetType, Currency: in.Currency,
		TotalAmountCents: in.TotalAmountCents, HourlyRateCents: in.HourlyRateCents,
		WeeklyHourLimit: in.WeeklyHourLimit, Status: "pending_funding", PlatformFeeBps: in.PlatformFeeBps,
	}, nil
}

// deriveContractTerms validates a contract request against the AUTHORITATIVE proposal and
// overrides every term the client must not control — project, freelancer, currency, platform
// fee, and amount. It is pure (no I/O) so the authorization/derivation rules are unit-testable.
func deriveContractTerms(in CreateInput, prop proposallookup.Proposal) (CreateInput, error) {
	// The caller must own the project the proposal was submitted to.
	if prop.ProjectClientID == "" || prop.ProjectClientID != in.ClientID {
		return in, ErrForbidden
	}
	// The proposal must still be contractable (not withdrawn or declined).
	if prop.Status == "withdrawn" || prop.Status == "declined" {
		return in, ErrProposalNotSelected
	}
	// Authoritative overrides — the request body cannot fabricate these.
	in.ProjectID = prop.ProjectID
	in.FreelancerID = prop.FreelancerID
	in.PlatformFeeBps = 1000 // platform fee is set by the platform, never the client
	if prop.Currency != "" {
		in.Currency = prop.Currency
	}
	if in.ClientID == in.FreelancerID || in.FreelancerID == "" {
		return in, ErrInvalidParties
	}
	if in.Currency == "" {
		in.Currency = "USD"
	}
	// The contract value must equal the freelancer's accepted bid; the client cannot inflate it.
	bid := prop.BidAmountCents
	switch in.BudgetType {
	case "fixed":
		if len(in.Milestones) == 0 {
			return in, ErrNoMilestones
		}
		var sum int64
		for _, m := range in.Milestones {
			sum += m.AmountCents
		}
		if sum != bid {
			return in, ErrProposalMismatch // milestone breakdown must total the accepted bid
		}
		in.TotalAmountCents = &bid
		in.HourlyRateCents = nil
	case "hourly":
		in.HourlyRateCents = &bid // the accepted bid is the hourly rate
		in.TotalAmountCents = nil
	}
	return in, nil
}

// ActivateContract moves pending_funding -> active, stamps started_at, appends the
// 'activate' event and emits contract.activated. Guards the current status atomically.
func (s *Service) ActivateContract(ctx context.Context, id, actor string) error {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	ok, err := s.store.TransitionContract(ctx, tx, id, "pending_funding", "active")
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidState
	}
	if err := s.store.MarkContractStarted(ctx, tx, id); err != nil {
		return err
	}
	c, err := s.store.GetContractTx(ctx, tx, id)
	if err != nil {
		return err
	}
	from := "pending_funding"
	if err := s.store.InsertContractEvent(ctx, tx, id, &from, "active", "activate", &actor, nil); err != nil {
		return err
	}
	if err := outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "contract", AggregateID: id, EventType: "contract.activated",
		Topic: "contract.events", PartitionKey: id,
		Payload: map[string]any{
			"contract_id": id, "client_id": c.ClientID,
			"freelancer_id": c.FreelancerID, "budget_type": c.BudgetType,
		},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FundMilestone moves a milestone pending -> funded (escrow funding is handled elsewhere).
// Only the contract's client may fund a milestone. milestoneInfo locks the milestone row
// FOR UPDATE, so the party check below observes a consistent contract within the same tx.
func (s *Service) FundMilestone(ctx context.Context, milestoneID, caller string) error {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	contractID, ok, err := s.store.FundMilestone(ctx, tx, milestoneID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidMilestoneState
	}
	c, err := s.store.GetContractTx(ctx, tx, contractID)
	if err != nil {
		return err
	}
	if caller != c.ClientID {
		return rbac.ErrForbidden
	}
	return tx.Commit(ctx)
}

// SubmitMilestone moves a milestone funded -> submitted and, when a note or s3 keys are
// supplied, records a deliverable in the same transaction.
func (s *Service) SubmitMilestone(ctx context.Context, milestoneID, actorFreelancerID, note string, s3Keys []string) error {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	contractID, ok, err := s.store.SubmitMilestone(ctx, tx, milestoneID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidMilestoneState
	}
	// Only the contract's freelancer may submit work for a milestone.
	freelancerID, err := s.store.FreelancerOfContract(ctx, tx, contractID)
	if err != nil {
		return err
	}
	if actorFreelancerID != freelancerID {
		return rbac.ErrForbidden
	}
	if note != "" || len(s3Keys) > 0 {
		if err := s.store.InsertDeliverable(ctx, tx, milestoneID, actorFreelancerID, note, s3Keys); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ApproveMilestone moves a milestone submitted -> approved and emits milestone.approved.
// Billing/ledger effects are handled elsewhere off the event.
func (s *Service) ApproveMilestone(ctx context.Context, milestoneID, actorClientID string) error {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	contractID, amount, ok, err := s.store.ApproveMilestone(ctx, tx, milestoneID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidMilestoneState
	}
	// Only the contract's client may approve a milestone.
	c, err := s.store.GetContractTx(ctx, tx, contractID)
	if err != nil {
		return err
	}
	if actorClientID != c.ClientID {
		return rbac.ErrForbidden
	}
	freelancerID := c.FreelancerID
	if err := outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "contract", AggregateID: contractID, EventType: "milestone.approved",
		Topic: "contract.events", PartitionKey: contractID,
		Payload: map[string]any{
			"milestone_id": milestoneID, "contract_id": contractID,
			"amount_cents": amount, "freelancer_id": freelancerID,
		},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RaiseDispute inserts a disputes row, forces the contract into 'disputed', appends the
// 'dispute' event capturing the prior status, and emits contract.disputed.
func (s *Service) RaiseDispute(ctx context.Context, contractID, raisedBy, against string, milestoneID *string, reason string, amountCents *int64) (string, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	from, err := s.store.CurrentStatus(ctx, tx, contractID)
	if err != nil {
		return "", err
	}
	// Only a party to the contract (client or freelancer) may raise a dispute. CurrentStatus
	// locked the contract row FOR UPDATE, so these parties are read consistently.
	c, err := s.store.GetContractTx(ctx, tx, contractID)
	if err != nil {
		return "", err
	}
	if err := rbac.RequireOneOf(raisedBy, c.ClientID, c.FreelancerID); err != nil {
		return "", err
	}
	disputeID, err := s.store.InsertDispute(ctx, tx, store.Dispute{
		ContractID: contractID, MilestoneID: milestoneID, RaisedBy: raisedBy,
		Against: against, Reason: reason, AmountCents: amountCents,
	})
	if err != nil {
		return "", err
	}
	if err := s.store.ForceStatus(ctx, tx, contractID, "disputed"); err != nil {
		return "", err
	}
	payload, _ := json.Marshal(map[string]any{"dispute_id": disputeID, "reason": reason})
	if err := s.store.InsertContractEvent(ctx, tx, contractID, &from, "disputed", "dispute", &raisedBy, payload); err != nil {
		return "", err
	}
	if err := outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "contract", AggregateID: contractID, EventType: "contract.disputed",
		Topic: "contract.events", PartitionKey: contractID,
		Payload: map[string]any{
			"contract_id": contractID, "dispute_id": disputeID,
			"raised_by": raisedBy, "against": against,
		},
	}); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return disputeID, nil
}

// GetContract returns a contract with its milestones after verifying the caller is a party
// to it (returns rbac.ErrForbidden otherwise), mirroring ContractEvents' ownership guard.
func (s *Service) GetContract(ctx context.Context, id, userID string) (*ContractView, error) {
	c, err := s.requireParty(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	ms, err := s.store.ListMilestones(ctx, id)
	if err != nil {
		return nil, err
	}
	return &ContractView{Contract: *c, Milestones: ms}, nil
}

// ContractParties returns a contract's parties and status WITHOUT a caller check. It backs the
// internal, server-to-server endpoint that other services (escrow, timetracking, review) use to
// authorize an action against a contract. It MUST NOT be exposed on the public gateway.
func (s *Service) ContractParties(ctx context.Context, contractID string) (clientID, freelancerID, status string, err error) {
	c, err := s.store.GetContract(ctx, contractID)
	if err != nil {
		return "", "", "", err
	}
	return c.ClientID, c.FreelancerID, c.Status, nil
}

// ListContractsForUser lists contracts where the user is the given party (client/freelancer).
func (s *Service) ListContractsForUser(ctx context.Context, userID, role string) ([]store.Contract, error) {
	if role != "freelancer" {
		role = "client"
	}
	return s.store.ListForUser(ctx, userID, role)
}

// requireParty loads the contract and returns rbac.ErrForbidden unless userID is one of its
// parties (client or freelancer). Shared by the read endpoints and milestone handlers so the
// ownership guard stays consistent.
func (s *Service) requireParty(ctx context.Context, contractID, userID string) (*store.Contract, error) {
	c, err := s.store.GetContract(ctx, contractID)
	if err != nil {
		return nil, err
	}
	if err := rbac.RequireOneOf(userID, c.ClientID, c.FreelancerID); err != nil {
		return nil, err
	}
	return c, nil
}

// ContractEvents returns the activity timeline for a contract after verifying the caller is a
// party to it (returns rbac.ErrForbidden otherwise).
func (s *Service) ContractEvents(ctx context.Context, contractID, userID string) ([]store.ContractEvent, error) {
	if _, err := s.requireParty(ctx, contractID, userID); err != nil {
		return nil, err
	}
	return s.store.ListContractEvents(ctx, contractID)
}
