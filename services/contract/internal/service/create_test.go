package service

import (
	"errors"
	"testing"

	"github.com/aizorix/platform/contract/internal/proposallookup"
	"github.com/aizorix/platform/contract/internal/store"
)

// TestDeriveContractTerms locks in the H4 fix: contract terms are taken from the AUTHORITATIVE
// proposal, never the request body. The client cannot name an arbitrary freelancer, set a low
// platform fee, or inflate the amount, and cannot form a contract on a project they don't own.
func TestDeriveContractTerms(t *testing.T) {
	const client, freelancer, project = "client-1", "freelancer-9", "project-7"
	base := func() CreateInput {
		return CreateInput{
			ProposalID:   "prop-1",
			ProjectID:    "ATTACKER-PROJECT",   // should be overridden by the proposal's
			ClientID:     client,               // must equal the project owner
			FreelancerID: "ATTACKER-FREELANCER", // should be overridden by the proposal's
			BudgetType:   "fixed",
			PlatformFeeBps: 0,                   // attacker tries a zero fee; must be forced to 1000
			Milestones: []store.MilestoneInput{
				{Seq: 1, Title: "a", AmountCents: 6000},
				{Seq: 2, Title: "b", AmountCents: 4000},
			},
		}
	}
	prop := proposallookup.Proposal{
		ProposalID: "prop-1", ProjectID: project, ProjectClientID: client,
		FreelancerID: freelancer, Status: "shortlisted", BidAmountCents: 10000, Currency: "USD",
	}

	t.Run("fixed: derives freelancer/project/amount/fee from proposal", func(t *testing.T) {
		got, err := deriveContractTerms(base(), prop)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.FreelancerID != freelancer {
			t.Errorf("freelancer = %q, want the proposal's %q (client-supplied value ignored)", got.FreelancerID, freelancer)
		}
		if got.ProjectID != project {
			t.Errorf("project = %q, want the proposal's %q", got.ProjectID, project)
		}
		if got.PlatformFeeBps != 1000 {
			t.Errorf("platform_fee_bps = %d, want forced 1000 (client cannot set it)", got.PlatformFeeBps)
		}
		if got.TotalAmountCents == nil || *got.TotalAmountCents != 10000 {
			t.Errorf("total = %v, want the accepted bid 10000", got.TotalAmountCents)
		}
		if got.HourlyRateCents != nil {
			t.Errorf("hourly rate must be nil for a fixed contract")
		}
	})

	t.Run("hourly: bid becomes the hourly rate", func(t *testing.T) {
		in := base()
		in.BudgetType = "hourly"
		in.Milestones = nil
		got, err := deriveContractTerms(in, prop)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.HourlyRateCents == nil || *got.HourlyRateCents != 10000 {
			t.Errorf("hourly rate = %v, want the accepted bid 10000", got.HourlyRateCents)
		}
		if got.TotalAmountCents != nil {
			t.Errorf("total must be nil for an hourly contract")
		}
	})

	t.Run("rejects caller who does not own the project", func(t *testing.T) {
		in := base()
		in.ClientID = "someone-else"
		if _, err := deriveContractTerms(in, prop); !errors.Is(err, ErrForbidden) {
			t.Fatalf("err = %v, want ErrForbidden", err)
		}
	})

	t.Run("rejects withdrawn proposal", func(t *testing.T) {
		p := prop
		p.Status = "withdrawn"
		if _, err := deriveContractTerms(base(), p); !errors.Is(err, ErrProposalNotSelected) {
			t.Fatalf("err = %v, want ErrProposalNotSelected", err)
		}
	})

	t.Run("rejects milestones that don't total the accepted bid", func(t *testing.T) {
		in := base()
		in.Milestones = []store.MilestoneInput{{Seq: 1, Title: "a", AmountCents: 999999}}
		if _, err := deriveContractTerms(in, prop); !errors.Is(err, ErrProposalMismatch) {
			t.Fatalf("err = %v, want ErrProposalMismatch", err)
		}
	})
}
