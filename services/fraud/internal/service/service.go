// Package service implements fraud business logic: ingesting risk signals, recomputing a
// weighted-sum risk score per subject, opening a fraud case when the score crosses the
// threshold, and resolving cases. State changes and the events they emit are written in one
// transaction via the transactional outbox.
package service

import (
	"context"
	"errors"

	"github.com/aizorix/platform/fraud/internal/store"
	"github.com/aizorix/platform/pkg/outbox"
	"github.com/jackc/pgx/v5"
)

// Tunables for the weighted-sum-v1 model.
const (
	modelVersion      = "weighted-sum-v1"
	recentSignalCount = 20   // last N signals contribute to the score
	caseThreshold     = 0.85 // open a case at/above this score
)

var (
	ErrInvalidSubject    = errors.New("fraud: subject_type and subject_id are required")
	ErrInvalidResolution = errors.New("fraud: status must be 'confirmed' or 'dismissed'")
	ErrNotFound          = store.ErrNotFound
)

type Service struct{ store *store.Store }

func New(st *store.Store) *Service { return &Service{store: st} }

// band maps a [0,1] score onto a risk band.
func band(score float64) string {
	switch {
	case score < 0.3:
		return "low"
	case score < 0.6:
		return "medium"
	case score < 0.85:
		return "high"
	default:
		return "critical"
	}
}

// IngestResult reports the outcome of ingesting a signal.
type IngestResult struct {
	SignalID  int64
	Score     float64
	Band      string
	CaseID    *string // set when a new case was opened by this ingest
	CaseCodes []string
}

// IngestSignal records a raw signal, recomputes the subject's risk score, upserts
// risk_scores, and — if the score crosses the threshold and no open case exists — opens a
// fraud case and emits fraud.case_opened (plus screenshot.flagged for screenshot subjects).
// All writes and events happen in one transaction.
func (s *Service) IngestSignal(ctx context.Context, subjectType, subjectID, signal string, weight float64, details map[string]any) (*IngestResult, error) {
	if subjectType == "" || subjectID == "" {
		return nil, ErrInvalidSubject
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	signalID, err := s.store.InsertSignal(ctx, tx, subjectType, subjectID, signal, weight, details)
	if err != nil {
		return nil, err
	}

	score, codes, err := s.store.RecentSignalSum(ctx, tx, subjectType, subjectID, recentSignalCount)
	if err != nil {
		return nil, err
	}
	b := band(score)
	features := map[string]any{
		"contributing_signals": codes,
		"signal_count":         len(codes),
	}
	if err := s.store.UpsertRiskScore(ctx, tx, subjectType, subjectID, score, b, features, modelVersion); err != nil {
		return nil, err
	}

	res := &IngestResult{SignalID: signalID, Score: score, Band: b}

	if score >= caseThreshold {
		open, err := s.store.HasOpenCase(ctx, tx, subjectType, subjectID)
		if err != nil {
			return nil, err
		}
		if !open {
			caseID, err := s.openCaseTx(ctx, tx, subjectType, subjectID, score, codes)
			if err != nil {
				return nil, err
			}
			res.CaseID = &caseID
			res.CaseCodes = codes
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

// openCaseTx inserts a fraud case and emits fraud.case_opened within tx. For screenshot
// subjects it additionally emits screenshot.flagged so the screenshot service can flag the
// asset. It returns the new case id.
func (s *Service) openCaseTx(ctx context.Context, tx pgx.Tx, subjectType, subjectID string, riskScore float64, reasonCodes []string) (string, error) {
	if reasonCodes == nil {
		reasonCodes = []string{}
	}
	caseID, err := s.store.InsertCase(ctx, tx, subjectType, subjectID, riskScore, reasonCodes)
	if err != nil {
		return "", err
	}
	if err := outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "fraud_case", AggregateID: caseID, EventType: "fraud.case_opened",
		Topic: "fraud.events", PartitionKey: subjectID,
		Payload: map[string]any{
			"case_id":      caseID,
			"subject_type": subjectType,
			"subject_id":   subjectID,
			"risk_score":   riskScore,
			"reason_codes": reasonCodes,
		},
	}); err != nil {
		return "", err
	}
	if subjectType == "screenshot" {
		if err := outbox.Enqueue(ctx, tx, outbox.Event{
			AggregateType: "screenshot", AggregateID: subjectID, EventType: "screenshot.flagged",
			Topic: "screenshot.events", PartitionKey: subjectID,
			Payload: map[string]any{
				"screenshot_id": subjectID,
				"risk_score":    riskScore,
				"reason_codes":  reasonCodes,
			},
		}); err != nil {
			return "", err
		}
	}
	return caseID, nil
}

// OpenCase opens a fraud case directly (admin/manual path) in its own transaction. It is the
// exposed counterpart to the internal openCaseTx used during ingest.
func (s *Service) OpenCase(ctx context.Context, subjectType, subjectID string, riskScore float64, reasonCodes []string) (string, error) {
	if subjectType == "" || subjectID == "" {
		return "", ErrInvalidSubject
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	caseID, err := s.openCaseTx(ctx, tx, subjectType, subjectID, riskScore, reasonCodes)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return caseID, nil
}

// ResolveCase marks a case confirmed or dismissed, records the resolution, and emits
// fraud.case_resolved — all in one transaction.
func (s *Service) ResolveCase(ctx context.Context, caseID, resolution, status string, assignedTo *string) (store.Case, error) {
	if status != "confirmed" && status != "dismissed" {
		return store.Case{}, ErrInvalidResolution
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return store.Case{}, err
	}
	defer tx.Rollback(ctx)

	c, err := s.store.ResolveCase(ctx, tx, caseID, resolution, status, assignedTo)
	if err != nil {
		return store.Case{}, err
	}
	if err := outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "fraud_case", AggregateID: c.ID, EventType: "fraud.case_resolved",
		Topic: "fraud.events", PartitionKey: c.SubjectID,
		Payload: map[string]any{
			"case_id":      c.ID,
			"subject_type": c.SubjectTyp,
			"subject_id":   c.SubjectID,
			"status":       c.Status,
			"resolution":   c.Resolution,
		},
	}); err != nil {
		return store.Case{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Case{}, err
	}
	return c, nil
}

// ListOpenCases returns open/investigating cases ordered by risk_score descending.
func (s *Service) ListOpenCases(ctx context.Context, limit int) ([]store.Case, error) {
	return s.store.ListOpenCases(ctx, limit)
}

// CaseDetail is a case together with its contributing signals.
type CaseDetail struct {
	Case    store.Case
	Signals []store.Signal
}

// GetCase returns a case and the recent signals for its subject.
func (s *Service) GetCase(ctx context.Context, id string) (CaseDetail, error) {
	c, err := s.store.GetCase(ctx, id)
	if err != nil {
		return CaseDetail{}, err
	}
	signals, err := s.store.ListSignalsForSubject(ctx, c.SubjectTyp, c.SubjectID, 50)
	if err != nil {
		return CaseDetail{}, err
	}
	return CaseDetail{Case: c, Signals: signals}, nil
}

// GetRiskScore returns the current risk score for a subject.
func (s *Service) GetRiskScore(ctx context.Context, subjectType, subjectID string) (store.RiskScore, error) {
	return s.store.GetRiskScore(ctx, subjectType, subjectID)
}
