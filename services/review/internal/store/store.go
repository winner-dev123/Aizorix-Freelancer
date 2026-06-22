// Package store is the review data layer: reviews, responses, and reputation scores.
// Reviews are double-blind: a row is written with is_published=false and only flipped
// to published once both contract parties have reviewed (or the publish window closes).
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("store: not found")
	// ErrUniqueViolation signals the (contract_id, reviewer_id) unique constraint fired;
	// the service maps it to ErrAlreadyReviewed.
	ErrUniqueViolation = errors.New("store: unique violation")
)

const uniqueViolationCode = "23505"

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store  { return &Store{pool: pool} }
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Review is the read/write model for the reviews table.
type Review struct {
	ID          string
	ContractID  string
	ReviewerID  string
	RevieweeID  string
	Rating      int
	Dimensions  map[string]any
	Comment     *string
	IsPublished bool
	PublishedAt *time.Time
	CreatedAt   time.Time
}

// InsertReview writes a new unpublished review. A unique violation on
// (contract_id, reviewer_id) is returned as ErrUniqueViolation.
func (s *Store) InsertReview(ctx context.Context, tx pgx.Tx, contractID, reviewerID, revieweeID string, rating int, dimensions map[string]any, comment *string) (string, error) {
	dims, err := json.Marshal(dimensions)
	if err != nil {
		return "", err
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO reviews (contract_id, reviewer_id, reviewee_id, rating, dimensions, comment, is_published)
		VALUES ($1,$2,$3,$4,$5,$6,false)
		RETURNING id`, contractID, reviewerID, revieweeID, rating, dims, comment).Scan(&id)
	if err != nil {
		if asPgUnique(err) {
			return "", ErrUniqueViolation
		}
		return "", err
	}
	return id, nil
}

// CountReviews returns how many reviews exist for a contract (used to detect both-sided
// completion for double-blind publication).
func (s *Store) CountReviews(ctx context.Context, tx pgx.Tx, contractID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM reviews WHERE contract_id=$1`, contractID).Scan(&n)
	return n, err
}

// PublishUnpublished flips all still-unpublished reviews for a contract to published and
// returns the published rows (id + reviewee_id + identity fields) so the service can
// recompute reputation and emit events.
func (s *Store) PublishUnpublished(ctx context.Context, tx pgx.Tx, contractID string) ([]Review, error) {
	rows, err := tx.Query(ctx, `
		UPDATE reviews
		SET is_published=true, published_at=now()
		WHERE contract_id=$1 AND is_published=false
		RETURNING id, contract_id, reviewer_id, reviewee_id, rating`, contractID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Review
	for rows.Next() {
		var r Review
		if err := rows.Scan(&r.ID, &r.ContractID, &r.ReviewerID, &r.RevieweeID, &r.Rating); err != nil {
			return nil, err
		}
		r.IsPublished = true
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecomputeReputation aggregates published reviews for a user and upserts the score.
// score is avg(rating)*20 (0..100); job_success_pct is the share of ratings >= 4.
func (s *Store) RecomputeReputation(ctx context.Context, tx pgx.Tx, userID string) error {
	var score float64
	var jobSuccess int
	var total int
	err := tx.QueryRow(ctx, `
		SELECT coalesce(avg(rating),0)*20,
		       CASE WHEN count(*)=0 THEN 0
		            ELSE round(100.0 * count(*) FILTER (WHERE rating>=4) / count(*)) END,
		       count(*)
		FROM reviews WHERE reviewee_id=$1 AND is_published=true`, userID).Scan(&score, &jobSuccess, &total)
	if err != nil {
		return err
	}
	var jsp *int
	if total > 0 {
		jsp = &jobSuccess
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO reputation_scores (user_id, score, job_success_pct, recompute_at, updated_at)
		VALUES ($1,$2,$3,now(),now())
		ON CONFLICT (user_id) DO UPDATE SET
		  score           = EXCLUDED.score,
		  job_success_pct = EXCLUDED.job_success_pct,
		  recompute_at    = now(),
		  updated_at      = now()`, userID, score, jsp)
	return err
}

// GetRevieweeID returns the reviewee_id for a review within tx. Used to authorize that the
// responder is the reviewee before writing a response. Returns ErrNotFound if absent.
func (s *Store) GetRevieweeID(ctx context.Context, tx pgx.Tx, reviewID string) (string, error) {
	var revieweeID string
	err := tx.QueryRow(ctx, `SELECT reviewee_id FROM reviews WHERE id=$1`, reviewID).Scan(&revieweeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return revieweeID, err
}

// UpsertResponse inserts (or replaces) a single review response.
func (s *Store) UpsertResponse(ctx context.Context, tx pgx.Tx, reviewID, responderID, response string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO review_responses (review_id, responder_id, response)
		VALUES ($1,$2,$3)
		ON CONFLICT (review_id) DO UPDATE SET
		  responder_id = EXCLUDED.responder_id,
		  response     = EXCLUDED.response`, reviewID, responderID, response)
	return err
}

// GetReview fetches one review by id (published or not).
func (s *Store) GetReview(ctx context.Context, id string) (Review, error) {
	var r Review
	var dims []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, contract_id, reviewer_id, reviewee_id, rating, dimensions, comment, is_published, published_at, created_at
		FROM reviews WHERE id=$1`, id).
		Scan(&r.ID, &r.ContractID, &r.ReviewerID, &r.RevieweeID, &r.Rating, &dims, &r.Comment, &r.IsPublished, &r.PublishedAt, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	_ = json.Unmarshal(dims, &r.Dimensions)
	return r, nil
}

// ListPublishedForUser returns published reviews where the user is the reviewee.
func (s *Store) ListPublishedForUser(ctx context.Context, revieweeID string) ([]Review, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, contract_id, reviewer_id, reviewee_id, rating, dimensions, comment, is_published, published_at, created_at
		FROM reviews
		WHERE reviewee_id=$1 AND is_published=true
		ORDER BY published_at DESC NULLS LAST, created_at DESC`, revieweeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Review
	for rows.Next() {
		var r Review
		var dims []byte
		if err := rows.Scan(&r.ID, &r.ContractID, &r.ReviewerID, &r.RevieweeID, &r.Rating, &dims, &r.Comment, &r.IsPublished, &r.PublishedAt, &r.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(dims, &r.Dimensions)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Reputation is the read model for reputation_scores.
type Reputation struct {
	UserID        string
	Score         float64
	JobSuccessPct *int
	RecomputeAt   *time.Time
	UpdatedAt     time.Time
}

// GetReputation returns a user's reputation score.
func (s *Store) GetReputation(ctx context.Context, userID string) (Reputation, error) {
	var rep Reputation
	err := s.pool.QueryRow(ctx, `
		SELECT user_id, score, job_success_pct, recompute_at, updated_at
		FROM reputation_scores WHERE user_id=$1`, userID).
		Scan(&rep.UserID, &rep.Score, &rep.JobSuccessPct, &rep.RecomputeAt, &rep.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return rep, ErrNotFound
	}
	return rep, err
}

// --- pg error helpers ---------------------------------------------------------

// asPgUnique reports whether err is a Postgres unique-violation (SQLSTATE 23505). It reads
// the SQLSTATE via the error's SQLState() method (implemented by *pgconn.PgError) so we
// avoid importing pgconn just for the code constant.
func asPgUnique(err error) bool {
	type sqlStater interface{ SQLState() string }
	var s sqlStater
	if errors.As(err, &s) {
		return s.SQLState() == uniqueViolationCode
	}
	return false
}
