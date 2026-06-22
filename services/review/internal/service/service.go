// Package service implements review business logic: double-blind review creation,
// publication (both-sided or window-closed), reputation recomputation, and responses.
package service

import (
	"context"
	"errors"

	"github.com/aizorix/platform/review/internal/contractparties"
	"github.com/aizorix/platform/review/internal/store"
	"github.com/aizorix/platform/pkg/outbox"
	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidRating   = errors.New("review: rating must be between 1 and 5")
	ErrAlreadyReviewed = errors.New("review: reviewer already reviewed this contract")
	ErrForbidden       = errors.New("review: not permitted")
	ErrNotFound        = store.ErrNotFound
	// ErrInvalidContract is a 400: contract_id missing/empty or reviewee is not the opposite
	// party on the contract.
	ErrInvalidContract = errors.New("review: invalid contract or reviewee")
	// ErrContractNotComplete is a 400: the contract has not reached a completed/terminal state,
	// so it is too early to review.
	ErrContractNotComplete = errors.New("review: contract is not in a completed state")
)

// partiesClient resolves a contract's parties from the contract service. It is the
// authorization primitive for review creation (see contractparties.Client).
type partiesClient interface {
	Get(ctx context.Context, contractID string) (contractparties.Parties, error)
}

type Service struct {
	store   *store.Store
	parties partiesClient
}

func New(st *store.Store, parties partiesClient) *Service {
	return &Service{store: st, parties: parties}
}

// completedContractStatuses are the contract states in which a review may be created. Reviews
// are for finished work, so only a terminal/completed contract qualifies.
var completedContractStatuses = map[string]bool{"completed": true}

// CreateReview inserts an unpublished review. When both parties on the contract have now
// reviewed, both reviews are published, reputations recomputed, and review.published
// events emitted — all in one transaction (double-blind reveal).
//
// Before inserting it AUTHORIZES the review against the contract service: the contract must
// exist, the reviewer must be one of its parties, the reviewee must be the OPPOSITE party,
// and the contract must be in a completed state. The parties lookup FAILS CLOSED — any lookup
// error denies (ErrForbidden) so a review is never created on an unverifiable contract.
func (s *Service) CreateReview(ctx context.Context, contractID, reviewerID, revieweeID string, rating int, dimensions map[string]any, comment *string) (*store.Review, error) {
	if rating < 1 || rating > 5 {
		return nil, ErrInvalidRating
	}
	if contractID == "" {
		return nil, ErrInvalidContract
	}
	p, err := s.parties.Get(ctx, contractID)
	if err != nil {
		// Fail closed: contract missing or contract service unreachable -> deny.
		return nil, ErrForbidden
	}
	if !p.IsParty(reviewerID) {
		return nil, ErrForbidden
	}
	// The reviewee must be exactly the other party on the contract.
	if revieweeID != p.Opposite(reviewerID) {
		return nil, ErrInvalidContract
	}
	if !completedContractStatuses[p.Status] {
		return nil, ErrContractNotComplete
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	id, err := s.store.InsertReview(ctx, tx, contractID, reviewerID, revieweeID, rating, dimensions, comment)
	if err != nil {
		if errors.Is(err, store.ErrUniqueViolation) {
			return nil, ErrAlreadyReviewed
		}
		return nil, err
	}

	// Double-blind: both sides have reviewed once two reviews exist for the contract.
	count, err := s.store.CountReviews(ctx, tx, contractID)
	if err != nil {
		return nil, err
	}
	if count >= 2 {
		if _, err := s.publishContractCount(ctx, tx, contractID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	r, err := s.store.GetReview(ctx, id)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// PublishWindowClosed is the admin/cron path: publish any still-unpublished reviews for a
// contract once the publish window has elapsed.
func (s *Service) PublishWindowClosed(ctx context.Context, contractID string) (int, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	published, err := s.publishContractCount(ctx, tx, contractID)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return published, nil
}

// publishContractCount publishes unpublished reviews, recomputes each reviewee's
// reputation, emits review.published per review, and returns how many were published.
func (s *Service) publishContractCount(ctx context.Context, tx pgx.Tx, contractID string) (int, error) {
	published, err := s.store.PublishUnpublished(ctx, tx, contractID)
	if err != nil {
		return 0, err
	}
	for _, r := range published {
		if err := s.store.RecomputeReputation(ctx, tx, r.RevieweeID); err != nil {
			return 0, err
		}
		if err := outbox.Enqueue(ctx, tx, outbox.Event{
			AggregateType: "review", AggregateID: r.ID, EventType: "review.published",
			Topic: "review.events", PartitionKey: r.ID,
			Payload: map[string]any{
				"review_id":   r.ID,
				"contract_id": r.ContractID,
				"reviewer_id": r.ReviewerID,
				"reviewee_id": r.RevieweeID,
				"rating":      r.Rating,
			},
		}); err != nil {
			return 0, err
		}
	}
	return len(published), nil
}

// RecomputeReputation recomputes a single user's reputation in its own transaction.
func (s *Service) RecomputeReputation(ctx context.Context, userID string) error {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.store.RecomputeReputation(ctx, tx, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// AddResponse records (or replaces) the reviewee's response to a review. Only the reviewee
// (the subject of the review) may respond — any other caller gets ErrForbidden, so a third
// party cannot post or overwrite the official response on someone else's review.
func (s *Service) AddResponse(ctx context.Context, reviewID, responderID, response string) error {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	revieweeID, err := s.store.GetRevieweeID(ctx, tx, reviewID)
	if err != nil {
		return err
	}
	if responderID != revieweeID {
		return ErrForbidden
	}
	if err := s.store.UpsertResponse(ctx, tx, reviewID, responderID, response); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) GetReview(ctx context.Context, id string) (store.Review, error) {
	return s.store.GetReview(ctx, id)
}

func (s *Service) ListReviewsForUser(ctx context.Context, revieweeID string) ([]store.Review, error) {
	return s.store.ListPublishedForUser(ctx, revieweeID)
}

func (s *Service) GetReputation(ctx context.Context, userID string) (store.Reputation, error) {
	return s.store.GetReputation(ctx, userID)
}
