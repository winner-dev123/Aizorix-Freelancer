// Package service holds the project service business logic: project creation with budget
// validation, publish/close state machine, listing and attachments. Transport (HTTP) is a
// thin adapter over these methods; lifecycle events are emitted via the transactional outbox.
package service

import (
	"context"
	"errors"

	"github.com/aizorix/platform/pkg/outbox"
	"github.com/aizorix/platform/project/internal/store"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrInvalidBudget = errors.New("invalid budget")
	ErrInvalidState  = errors.New("invalid state transition")
	ErrForbidden     = errors.New("forbidden")
	ErrValidation    = errors.New("validation failed")
)

// validBudgetTypes mirrors the budget_type enum.
var validBudgetTypes = map[string]bool{"fixed": true, "hourly": true}

// validExperience mirrors the experience_level enum.
var validExperience = map[string]bool{"entry": true, "intermediate": true, "expert": true}

type Service struct{ store *store.Store }

func New(st *store.Store) *Service { return &Service{store: st} }

// CreateProjectInput is the create request after transport decoding.
type CreateProjectInput struct {
	ClientID            string
	Title               string
	Description         string
	BudgetType          string
	BudgetMinCents      *int64
	BudgetMaxCents      *int64
	Currency            string
	WeeklyHourLimit     *int
	ExperienceRequired  *string
	EstimatedDurationDs *int
	CategoryID          *string
	SkillIDs            []string
}

// CreateProject validates the budget shape and inserts a draft project with its skills.
func (s *Service) CreateProject(ctx context.Context, in CreateProjectInput) (*store.Project, error) {
	if in.ClientID == "" || in.Title == "" {
		return nil, ErrValidation
	}
	if !validBudgetTypes[in.BudgetType] {
		return nil, ErrValidation
	}
	if in.ExperienceRequired != nil && !validExperience[*in.ExperienceRequired] {
		return nil, ErrValidation
	}
	if err := validateBudget(in); err != nil {
		return nil, err
	}
	if in.Currency == "" {
		in.Currency = "USD"
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	p, err := s.store.CreateProject(ctx, tx, store.CreateProjectInput{
		ClientID:            in.ClientID,
		CategoryID:          in.CategoryID,
		Title:               in.Title,
		Description:         in.Description,
		BudgetType:          in.BudgetType,
		BudgetMinCents:      in.BudgetMinCents,
		BudgetMaxCents:      in.BudgetMaxCents,
		Currency:            in.Currency,
		WeeklyHourLimit:     in.WeeklyHourLimit,
		ExperienceRequired:  in.ExperienceRequired,
		EstimatedDurationDs: in.EstimatedDurationDs,
		SkillIDs:            in.SkillIDs,
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

// validateBudget enforces the per-type budget rules.
func validateBudget(in CreateProjectInput) error {
	switch in.BudgetType {
	case "fixed":
		if in.BudgetMinCents == nil || *in.BudgetMinCents <= 0 {
			return ErrInvalidBudget
		}
		if in.BudgetMaxCents != nil && *in.BudgetMaxCents < *in.BudgetMinCents {
			return ErrInvalidBudget
		}
	case "hourly":
		if in.BudgetMinCents == nil || *in.BudgetMinCents <= 0 {
			return ErrInvalidBudget
		}
		if in.BudgetMaxCents != nil && *in.BudgetMaxCents < *in.BudgetMinCents {
			return ErrInvalidBudget
		}
		if in.WeeklyHourLimit == nil || *in.WeeklyHourLimit <= 0 {
			return ErrInvalidBudget
		}
	default:
		return ErrInvalidBudget
	}
	return nil
}

func (s *Service) GetProject(ctx context.Context, id string) (*store.Project, error) {
	p, err := s.store.GetProject(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	return p, err
}

func (s *Service) ListProjects(ctx context.Context, f store.ListFilter) ([]store.Project, error) {
	return s.store.ListProjects(ctx, f)
}

// PublishProject moves a draft to published. Only the owning client may do so.
func (s *Service) PublishProject(ctx context.Context, id, clientID string) (*store.Project, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	p, err := s.store.GetProjectForUpdate(ctx, tx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if p.ClientID != clientID {
		return nil, ErrForbidden
	}
	if p.Status != "draft" {
		return nil, ErrInvalidState
	}
	if err := s.store.SetStatusPublished(ctx, tx, id); err != nil {
		return nil, err
	}
	if err := outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "project", AggregateID: id, EventType: "project.published",
		Topic: "project.events", PartitionKey: id,
		Payload: map[string]any{
			"project_id":  id,
			"client_id":   clientID,
			"budget_type": p.BudgetType,
			"title":       p.Title,
		},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	p.Status = "published"
	return p, nil
}

// CloseProject closes an active project. Only the owning client may do so.
func (s *Service) CloseProject(ctx context.Context, id, clientID string) (*store.Project, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	p, err := s.store.GetProjectForUpdate(ctx, tx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if p.ClientID != clientID {
		return nil, ErrForbidden
	}
	if p.Status == "closed" || p.Status == "archived" {
		return nil, ErrInvalidState
	}
	if err := s.store.SetStatusClosed(ctx, tx, id); err != nil {
		return nil, err
	}
	if err := outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "project", AggregateID: id, EventType: "project.closed",
		Topic: "project.events", PartitionKey: id,
		Payload: map[string]any{"project_id": id, "client_id": clientID, "title": p.Title},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	p.Status = "closed"
	return p, nil
}

// AddAttachment stores an uploaded file reference against a project. The caller must own
// the project (enforced at the transport layer via X-User-Id).
func (s *Service) AddAttachment(ctx context.Context, projectID, clientID, s3Key, filename string, sizeBytes int64, contentType string) (*store.Attachment, error) {
	if projectID == "" || s3Key == "" {
		return nil, ErrValidation
	}
	p, err := s.store.GetProject(ctx, projectID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if p.ClientID != clientID {
		return nil, ErrForbidden
	}
	return s.store.AddAttachment(ctx, projectID, s3Key, filename, sizeBytes, contentType)
}
