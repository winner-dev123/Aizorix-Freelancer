//go:build integration

package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aizorix/platform/contract/internal/itest"
	"github.com/aizorix/platform/contract/internal/proposallookup"
	"github.com/aizorix/platform/contract/internal/service"
	"github.com/aizorix/platform/contract/internal/store"
)

// stubProposals is a fake ProposalLookup returning a fixed accepted proposal, so the contract
// state-machine integration test doesn't need the proposal service running. Terms are derived
// from this proposal (freelancer, amount, owning client), matching the seeded parties.
type stubProposals struct{ p proposallookup.Proposal }

func (s stubProposals) Get(context.Context, string) (proposallookup.Proposal, error) {
	return s.p, nil
}

func acceptedProposal(p itest.Parties, bidCents int64) proposallookup.Proposal {
	return proposallookup.Proposal{
		ProposalID:      p.ProposalID,
		ProjectID:       p.ProjectID,
		ProjectClientID: p.ClientID,
		FreelancerID:    p.FreelancerID,
		Status:          "accepted",
		BidAmountCents:  bidCents,
		Currency:        "USD",
	}
}

// TestContractHappyPathAndForbidden drives create-from-proposal -> activate ->
// fund/submit/approve milestone state machine (happy path), then asserts a non-party caller
// is rejected with ErrForbidden at each guarded transition.
func TestContractHappyPathAndForbidden(t *testing.T) {
	ctx := context.Background()
	pool := itest.NewPostgres(t)
	parties := itest.SeedParties(ctx, t, pool)
	svc := service.New(store.New(pool), stubProposals{acceptedProposal(parties, 50000)})

	c, err := svc.CreateFromProposal(ctx, service.CreateInput{
		ProjectID:    parties.ProjectID,
		ProposalID:   parties.ProposalID,
		ClientID:     parties.ClientID,
		FreelancerID: parties.FreelancerID,
		BudgetType:   "fixed",
		Currency:     "USD",
		Milestones: []store.MilestoneInput{
			{Seq: 1, Title: "M1", AmountCents: 50000},
		},
	})
	if err != nil {
		t.Fatalf("create from proposal: %v", err)
	}
	if c.Status != "pending_funding" {
		t.Fatalf("new contract status = %q, want pending_funding", c.Status)
	}

	// Activate: pending_funding -> active.
	if err := svc.ActivateContract(ctx, c.ID, parties.ClientID); err != nil {
		t.Fatalf("activate: %v", err)
	}
	view, err := svc.GetContract(ctx, c.ID, parties.ClientID)
	if err != nil {
		t.Fatalf("get contract: %v", err)
	}
	if view.Contract.Status != "active" {
		t.Fatalf("status after activate = %q, want active", view.Contract.Status)
	}
	if len(view.Milestones) != 1 {
		t.Fatalf("expected 1 milestone, got %d", len(view.Milestones))
	}
	milestoneID := view.Milestones[0].ID

	// ── Forbidden: an outsider cannot fund the milestone. ──
	if err := svc.FundMilestone(ctx, milestoneID, parties.OutsiderID); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("outsider funding should be ErrForbidden, got %v", err)
	}

	// Happy path: client funds (pending -> funded).
	if err := svc.FundMilestone(ctx, milestoneID, parties.ClientID); err != nil {
		t.Fatalf("fund milestone: %v", err)
	}
	assertMilestoneStatus(ctx, t, svc, c.ID, parties.ClientID, "funded")

	// ── Forbidden: an outsider cannot submit work. ──
	if err := svc.SubmitMilestone(ctx, milestoneID, parties.OutsiderID, "note", nil); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("outsider submit should be ErrForbidden, got %v", err)
	}

	// Happy path: freelancer submits (funded -> submitted).
	if err := svc.SubmitMilestone(ctx, milestoneID, parties.FreelancerID, "delivered", []string{"s3://k"}); err != nil {
		t.Fatalf("submit milestone: %v", err)
	}
	assertMilestoneStatus(ctx, t, svc, c.ID, parties.ClientID, "submitted")

	// ── Forbidden: an outsider cannot approve. ──
	if err := svc.ApproveMilestone(ctx, milestoneID, parties.OutsiderID); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("outsider approve should be ErrForbidden, got %v", err)
	}

	// Happy path: client approves (submitted -> approved).
	if err := svc.ApproveMilestone(ctx, milestoneID, parties.ClientID); err != nil {
		t.Fatalf("approve milestone: %v", err)
	}
	assertMilestoneStatus(ctx, t, svc, c.ID, parties.ClientID, "approved")

	// A non-party may not even read the contract's event timeline.
	if _, err := svc.ContractEvents(ctx, c.ID, parties.OutsiderID); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("outsider reading events should be ErrForbidden, got %v", err)
	}
	// A party can.
	if _, err := svc.ContractEvents(ctx, c.ID, parties.ClientID); err != nil {
		t.Fatalf("party reading events failed: %v", err)
	}
}

// TestInvalidStateTransitionGuards asserts the state machine rejects out-of-order moves.
func TestInvalidStateTransitionGuards(t *testing.T) {
	ctx := context.Background()
	pool := itest.NewPostgres(t)
	parties := itest.SeedParties(ctx, t, pool)
	svc := service.New(store.New(pool), stubProposals{acceptedProposal(parties, 1000)})

	c, err := svc.CreateFromProposal(ctx, service.CreateInput{
		ProjectID: parties.ProjectID, ProposalID: parties.ProposalID,
		ClientID: parties.ClientID, FreelancerID: parties.FreelancerID,
		BudgetType: "fixed", Currency: "USD",
		Milestones: []store.MilestoneInput{{Seq: 1, Title: "M1", AmountCents: 1000}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	view, err := svc.GetContract(ctx, c.ID, parties.ClientID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	milestoneID := view.Milestones[0].ID

	// Submitting a still-pending milestone (never funded) is an invalid milestone transition.
	if err := svc.SubmitMilestone(ctx, milestoneID, parties.FreelancerID, "", nil); !errors.Is(err, service.ErrInvalidMilestoneState) {
		t.Fatalf("submit before fund should be ErrInvalidMilestoneState, got %v", err)
	}
}

func assertMilestoneStatus(ctx context.Context, t *testing.T, svc *service.Service, contractID, userID, want string) {
	t.Helper()
	view, err := svc.GetContract(ctx, contractID, userID)
	if err != nil {
		t.Fatalf("get contract: %v", err)
	}
	if got := view.Milestones[0].Status; got != want {
		t.Fatalf("milestone status = %q, want %q", got, want)
	}
}
