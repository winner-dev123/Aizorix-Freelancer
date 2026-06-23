package service

import (
	"context"
	"errors"
	"testing"

	"github.com/aizorix/platform/review/internal/contractparties"
)

// mockParties is an in-memory partiesClient: it returns a fixed Parties/error so the review
// authorization path can be exercised without the contract service or a database.
type mockParties struct {
	p   contractparties.Parties
	err error
}

func (m mockParties) Get(_ context.Context, _ string) (contractparties.Parties, error) {
	return m.p, m.err
}

// TestCreateReview_GuardsFailClosed pins the review-creation authorization. Every one of these
// checks runs BEFORE any store access, so they exercise the real guard logic with a nil store:
// invalid ratings are rejected, and a review is never created on an unverifiable contract — a
// failed parties lookup denies (fail closed), as does a non-party reviewer, a reviewee that is not
// the opposite party, or a contract that has not reached a completed state.
func TestCreateReview_GuardsFailClosed(t *testing.T) {
	// A valid, completed contract between client c1 and freelancer f1.
	ok := contractparties.Parties{ContractID: "c1", ClientID: "c1", FreelancerID: "f1", Status: "completed"}
	active := contractparties.Parties{ContractID: "c1", ClientID: "c1", FreelancerID: "f1", Status: "active"}

	cases := []struct {
		name                         string
		parties                      mockParties
		contractID, reviewer, revwee string
		rating                       int
		wantErr                      error
	}{
		{"rating below 1", mockParties{p: ok}, "c1", "c1", "f1", 0, ErrInvalidRating},
		{"rating above 5", mockParties{p: ok}, "c1", "c1", "f1", 6, ErrInvalidRating},
		{"empty contract id", mockParties{p: ok}, "", "c1", "f1", 5, ErrInvalidContract},
		{"parties lookup fails → deny (fail closed)", mockParties{err: errors.New("contract svc down")}, "c1", "c1", "f1", 5, ErrForbidden},
		{"reviewer is not a party → deny", mockParties{p: ok}, "c1", "stranger", "f1", 5, ErrForbidden},
		{"reviewee is not the opposite party", mockParties{p: ok}, "c1", "c1", "someone-else", 5, ErrInvalidContract},
		{"contract not completed → too early", mockParties{p: active}, "c1", "c1", "f1", 5, ErrContractNotComplete},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := New(nil, c.parties) // guards return before any store access
			_, err := svc.CreateReview(context.Background(), c.contractID, c.reviewer, c.revwee, c.rating, nil, nil)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("CreateReview err = %v, want %v", err, c.wantErr)
			}
		})
	}
}
