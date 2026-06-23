package service

import (
	"context"
	"errors"
	"testing"

	"github.com/aizorix/platform/pkg/rbac"
)

// TestSubmitProposal_RejectsNonPositiveBid pins the bid-amount guard, which runs before any store
// access (so a nil store is fine): a zero or negative bid is rejected.
func TestSubmitProposal_RejectsNonPositiveBid(t *testing.T) {
	svc := New(nil)
	for _, bid := range []int64{0, -1, -100} {
		if _, err := svc.SubmitProposal(context.Background(), SubmitInput{BidAmountCents: bid}); !errors.Is(err, ErrInvalidBid) {
			t.Fatalf("bid %d: err = %v, want ErrInvalidBid", bid, err)
		}
	}
}

// TestProposalAuthz_EmptyActorForbidden pins the authorization guards that reject an unauthenticated
// actor before any store access — so competitors' bids can't leak to an empty/anonymous caller.
func TestProposalAuthz_EmptyActorForbidden(t *testing.T) {
	svc := New(nil)
	if err := svc.ShortlistProposal(context.Background(), "p1", ""); !errors.Is(err, rbac.ErrForbidden) {
		t.Fatalf("ShortlistProposal(empty actor) = %v, want ErrForbidden", err)
	}
	if _, err := svc.ListProposalsForProject(context.Background(), "proj1", "", ""); !errors.Is(err, rbac.ErrForbidden) {
		t.Fatalf("ListProposalsForProject(empty actor) = %v, want ErrForbidden", err)
	}
}

// TestIsUniqueViolation covers how the one-active-proposal-per-project rule (a UNIQUE constraint)
// is recognized and mapped to ErrDuplicateProposal.
func TestIsUniqueViolation(t *testing.T) {
	if !isUniqueViolation(errors.New(`duplicate key value violates unique constraint "x" (SQLSTATE 23505)`)) {
		t.Error("SQLSTATE 23505 must be detected")
	}
	if !isUniqueViolation(errors.New(`violates unique constraint "proposals_project_id_freelancer_id_key"`)) {
		t.Error("the named unique constraint must be detected")
	}
	if isUniqueViolation(errors.New("connection refused")) {
		t.Error("unrelated errors must not be treated as a unique violation")
	}
	if isUniqueViolation(nil) {
		t.Error("nil must be false")
	}
}
