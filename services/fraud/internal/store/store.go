// Package store is the fraud data layer (migration 000010): fraud_signals (raw observations),
// risk_scores (the current weighted score + band per subject), and fraud_cases (an open
// investigation once a subject crosses the risk threshold). numeric columns scan into
// float64; text[] columns map to []string; jsonb is written as []byte and read back into
// map[string]any.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("store: not found")

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store  { return &Store{pool: pool} }
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Signal is the read/write model for fraud_signals.
type Signal struct {
	ID         int64
	SubjectTyp string
	SubjectID  string
	Signal     string
	Weight     float64
	Details    map[string]any
	ObservedAt time.Time
}

// RiskScore is the read/write model for risk_scores (one row per subject).
type RiskScore struct {
	SubjectTyp   string
	SubjectID    string
	Score        float64
	Band         string
	Features     map[string]any
	ModelVersion *string
	ComputedAt   time.Time
}

// Case is the read/write model for fraud_cases.
type Case struct {
	ID          string
	SubjectTyp  string
	SubjectID   string
	RiskScore   float64
	Status      string
	ReasonCodes []string
	AssignedTo  *string
	Resolution  *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ResolvedAt  *time.Time
}

// InsertSignal writes a raw fraud signal and returns its generated id.
func (s *Store) InsertSignal(ctx context.Context, tx pgx.Tx, subjectType, subjectID, signal string, weight float64, details map[string]any) (int64, error) {
	if details == nil {
		details = map[string]any{}
	}
	body, err := json.Marshal(details)
	if err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO fraud_signals (subject_type, subject_id, signal, weight, details)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id`, subjectType, subjectID, signal, weight, body).Scan(&id)
	return id, err
}

// RecentSignalSum returns the clamped weighted sum over the most recent n signals for a
// subject and the distinct signal names that contributed, ordered by observed_at desc. The
// score is capped to [0,1.000].
func (s *Store) RecentSignalSum(ctx context.Context, tx pgx.Tx, subjectType, subjectID string, n int) (score float64, codes []string, err error) {
	rows, err := tx.Query(ctx, `
		SELECT signal, weight
		FROM fraud_signals
		WHERE subject_type = $1 AND subject_id = $2
		ORDER BY observed_at DESC, id DESC
		LIMIT $3`, subjectType, subjectID, n)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	seen := map[string]struct{}{}
	for rows.Next() {
		var sig string
		var w float64
		if err := rows.Scan(&sig, &w); err != nil {
			return 0, nil, err
		}
		score += w
		if _, ok := seen[sig]; !ok {
			seen[sig] = struct{}{}
			codes = append(codes, sig)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	if score > 1.0 {
		score = 1.0
	}
	if score < 0 {
		score = 0
	}
	return score, codes, nil
}

// UpsertRiskScore inserts or updates the current risk score for a subject.
func (s *Store) UpsertRiskScore(ctx context.Context, tx pgx.Tx, subjectType, subjectID string, score float64, band string, features map[string]any, modelVersion string) error {
	if features == nil {
		features = map[string]any{}
	}
	body, err := json.Marshal(features)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO risk_scores (subject_type, subject_id, score, band, features, model_version, computed_at)
		VALUES ($1,$2,$3,$4,$5,$6,now())
		ON CONFLICT (subject_type, subject_id) DO UPDATE SET
		  score         = EXCLUDED.score,
		  band          = EXCLUDED.band,
		  features      = EXCLUDED.features,
		  model_version = EXCLUDED.model_version,
		  computed_at   = now()`, subjectType, subjectID, score, band, body, modelVersion)
	return err
}

// GetRiskScore returns a subject's current risk score, or ErrNotFound.
func (s *Store) GetRiskScore(ctx context.Context, subjectType, subjectID string) (RiskScore, error) {
	var rs RiskScore
	var features []byte
	err := s.pool.QueryRow(ctx, `
		SELECT subject_type, subject_id, score, band, features, model_version, computed_at
		FROM risk_scores
		WHERE subject_type = $1 AND subject_id = $2`, subjectType, subjectID).
		Scan(&rs.SubjectTyp, &rs.SubjectID, &rs.Score, &rs.Band, &features, &rs.ModelVersion, &rs.ComputedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return rs, ErrNotFound
	}
	if err != nil {
		return rs, err
	}
	_ = json.Unmarshal(features, &rs.Features)
	return rs, nil
}

// HasOpenCase reports whether the subject already has a case that is not resolved
// (status in 'open' or 'investigating').
func (s *Store) HasOpenCase(ctx context.Context, tx pgx.Tx, subjectType, subjectID string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM fraud_cases
			WHERE subject_type = $1 AND subject_id = $2
			  AND status IN ('open','investigating')
		)`, subjectType, subjectID).Scan(&exists)
	return exists, err
}

// InsertCase opens a new fraud case and returns its generated id.
func (s *Store) InsertCase(ctx context.Context, tx pgx.Tx, subjectType, subjectID string, riskScore float64, reasonCodes []string) (string, error) {
	if reasonCodes == nil {
		reasonCodes = []string{}
	}
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO fraud_cases (subject_type, subject_id, risk_score, status, reason_codes)
		VALUES ($1,$2,$3,'open',$4)
		RETURNING id`, subjectType, subjectID, riskScore, reasonCodes).Scan(&id)
	return id, err
}

// ResolveCase marks a case confirmed/dismissed and returns the updated row (so the service
// can emit fraud.case_resolved). status must be 'confirmed' or 'dismissed'.
func (s *Store) ResolveCase(ctx context.Context, tx pgx.Tx, caseID, resolution, status string, assignedTo *string) (Case, error) {
	var c Case
	err := tx.QueryRow(ctx, `
		UPDATE fraud_cases
		SET status      = $2,
		    resolution  = $3,
		    assigned_to = COALESCE($4, assigned_to),
		    resolved_at = now(),
		    updated_at  = now()
		WHERE id = $1
		RETURNING id, subject_type, subject_id, risk_score, status, reason_codes,
		          assigned_to, resolution, created_at, updated_at, resolved_at`,
		caseID, status, resolution, assignedTo).
		Scan(&c.ID, &c.SubjectTyp, &c.SubjectID, &c.RiskScore, &c.Status, &c.ReasonCodes,
			&c.AssignedTo, &c.Resolution, &c.CreatedAt, &c.UpdatedAt, &c.ResolvedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// GetCase fetches a single case by id.
func (s *Store) GetCase(ctx context.Context, id string) (Case, error) {
	var c Case
	err := s.pool.QueryRow(ctx, `
		SELECT id, subject_type, subject_id, risk_score, status, reason_codes,
		       assigned_to, resolution, created_at, updated_at, resolved_at
		FROM fraud_cases
		WHERE id = $1`, id).
		Scan(&c.ID, &c.SubjectTyp, &c.SubjectID, &c.RiskScore, &c.Status, &c.ReasonCodes,
			&c.AssignedTo, &c.Resolution, &c.CreatedAt, &c.UpdatedAt, &c.ResolvedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// ListSignalsForSubject returns recent signals for a subject (used by GetCase to attach the
// contributing signals).
func (s *Store) ListSignalsForSubject(ctx context.Context, subjectType, subjectID string, limit int) ([]Signal, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, subject_type, subject_id, signal, weight, details, observed_at
		FROM fraud_signals
		WHERE subject_type = $1 AND subject_id = $2
		ORDER BY observed_at DESC, id DESC
		LIMIT $3`, subjectType, subjectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Signal
	for rows.Next() {
		var sig Signal
		var details []byte
		if err := rows.Scan(&sig.ID, &sig.SubjectTyp, &sig.SubjectID, &sig.Signal, &sig.Weight, &details, &sig.ObservedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(details, &sig.Details)
		out = append(out, sig)
	}
	return out, rows.Err()
}

// ListOpenCases returns open/investigating cases ordered by risk_score descending.
func (s *Store) ListOpenCases(ctx context.Context, limit int) ([]Case, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, subject_type, subject_id, risk_score, status, reason_codes,
		       assigned_to, resolution, created_at, updated_at, resolved_at
		FROM fraud_cases
		WHERE status IN ('open','investigating')
		ORDER BY risk_score DESC, created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Case
	for rows.Next() {
		var c Case
		if err := rows.Scan(&c.ID, &c.SubjectTyp, &c.SubjectID, &c.RiskScore, &c.Status, &c.ReasonCodes,
			&c.AssignedTo, &c.Resolution, &c.CreatedAt, &c.UpdatedAt, &c.ResolvedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
