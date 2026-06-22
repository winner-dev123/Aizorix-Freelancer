// Package store is the project service's data-access layer over PostgreSQL (pgx).
// It owns the projects table, its skills join table and attachments. The search_tsv
// column is maintained by a database trigger and is never written from here.
package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("store: not found")

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool exposes the pool for transactions spanning store + outbox.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// ── Projects ────────────────────────────────────────────────────────────────

type Project struct {
	ID                  string
	ClientID            string
	CategoryID          *string
	Title               string
	Description         string
	BudgetType          string
	BudgetMinCents      *int64
	BudgetMaxCents      *int64
	Currency            string
	WeeklyHourLimit     *int
	ExperienceRequired  *string
	EstimatedDurationDs *int
	Status              string
	Visibility          string
	ProposalsCount      int
	HiredCount          int
	PublishedAt         *time.Time
	ClosedAt            *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// CreateProjectInput carries the fields needed to insert a draft project.
type CreateProjectInput struct {
	ClientID            string
	CategoryID          *string
	Title               string
	Description         string
	BudgetType          string
	BudgetMinCents      *int64
	BudgetMaxCents      *int64
	Currency            string
	WeeklyHourLimit     *int
	ExperienceRequired  *string
	EstimatedDurationDs *int
	SkillIDs            []string
}

// CreateProject inserts the draft project and its skills inside tx and returns the row.
func (s *Store) CreateProject(ctx context.Context, tx pgx.Tx, in CreateProjectInput) (*Project, error) {
	p := &Project{}
	err := tx.QueryRow(ctx, `
		INSERT INTO projects
			(client_id, category_id, title, description, budget_type, budget_min_cents,
			 budget_max_cents, currency, weekly_hour_limit, experience_required,
			 estimated_duration_days, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'draft')
		RETURNING id, client_id, category_id, coalesce(title,''), coalesce(description,''),
			budget_type::text, budget_min_cents, budget_max_cents, currency, weekly_hour_limit,
			experience_required::text, estimated_duration_days, status::text, visibility,
			proposals_count, hired_count, published_at, closed_at, created_at, updated_at`,
		in.ClientID, in.CategoryID, in.Title, in.Description, in.BudgetType, in.BudgetMinCents,
		in.BudgetMaxCents, in.Currency, in.WeeklyHourLimit, in.ExperienceRequired,
		in.EstimatedDurationDs).
		Scan(&p.ID, &p.ClientID, &p.CategoryID, &p.Title, &p.Description, &p.BudgetType,
			&p.BudgetMinCents, &p.BudgetMaxCents, &p.Currency, &p.WeeklyHourLimit,
			&p.ExperienceRequired, &p.EstimatedDurationDs, &p.Status, &p.Visibility,
			&p.ProposalsCount, &p.HiredCount, &p.PublishedAt, &p.ClosedAt, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	for _, sid := range in.SkillIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO project_skills (project_id, skill_id)
			VALUES ($1,$2) ON CONFLICT DO NOTHING`, p.ID, sid); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (s *Store) GetProject(ctx context.Context, id string) (*Project, error) {
	p := &Project{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, client_id, category_id, coalesce(title,''), coalesce(description,''),
			budget_type::text, budget_min_cents, budget_max_cents, currency, weekly_hour_limit,
			experience_required::text, estimated_duration_days, status::text, visibility,
			proposals_count, hired_count, published_at, closed_at, created_at, updated_at
		FROM projects WHERE id = $1 AND deleted_at IS NULL`, id).
		Scan(&p.ID, &p.ClientID, &p.CategoryID, &p.Title, &p.Description, &p.BudgetType,
			&p.BudgetMinCents, &p.BudgetMaxCents, &p.Currency, &p.WeeklyHourLimit,
			&p.ExperienceRequired, &p.EstimatedDurationDs, &p.Status, &p.Visibility,
			&p.ProposalsCount, &p.HiredCount, &p.PublishedAt, &p.ClosedAt, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// GetProjectForUpdate locks the project row inside tx for state transitions.
func (s *Store) GetProjectForUpdate(ctx context.Context, tx pgx.Tx, id string) (*Project, error) {
	p := &Project{}
	err := tx.QueryRow(ctx, `
		SELECT id, client_id, category_id, coalesce(title,''), coalesce(description,''),
			budget_type::text, budget_min_cents, budget_max_cents, currency, weekly_hour_limit,
			experience_required::text, estimated_duration_days, status::text, visibility,
			proposals_count, hired_count, published_at, closed_at, created_at, updated_at
		FROM projects WHERE id = $1 AND deleted_at IS NULL
		FOR UPDATE`, id).
		Scan(&p.ID, &p.ClientID, &p.CategoryID, &p.Title, &p.Description, &p.BudgetType,
			&p.BudgetMinCents, &p.BudgetMaxCents, &p.Currency, &p.WeeklyHourLimit,
			&p.ExperienceRequired, &p.EstimatedDurationDs, &p.Status, &p.Visibility,
			&p.ProposalsCount, &p.HiredCount, &p.PublishedAt, &p.ClosedAt, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// SetStatusPublished flips a project to published inside tx.
func (s *Store) SetStatusPublished(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `
		UPDATE projects SET status = 'published', published_at = now(), updated_at = now()
		WHERE id = $1`, id)
	return err
}

// SetStatusClosed flips a project to closed inside tx.
func (s *Store) SetStatusClosed(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `
		UPDATE projects SET status = 'closed', closed_at = now(), updated_at = now()
		WHERE id = $1`, id)
	return err
}

// ListFilter constrains a project listing.
type ListFilter struct {
	Status     string
	CategoryID string
	Search     string
	Limit      int
	Offset     int
}

// ListProjects returns published, non-deleted projects matching the filter, newest first.
func (s *Store) ListProjects(ctx context.Context, f ListFilter) ([]Project, error) {
	status := f.Status
	if status == "" {
		status = "published"
	}
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	// Build the query with positional args; only append clauses for provided filters.
	var b strings.Builder
	b.WriteString(`
		SELECT id, client_id, category_id, coalesce(title,''), coalesce(description,''),
			budget_type::text, budget_min_cents, budget_max_cents, currency, weekly_hour_limit,
			experience_required::text, estimated_duration_days, status::text, visibility,
			proposals_count, hired_count, published_at, closed_at, created_at, updated_at
		FROM projects
		WHERE deleted_at IS NULL AND status = $1`)
	args := []any{status}
	n := 1
	if f.CategoryID != "" {
		n++
		b.WriteString(" AND category_id = $")
		b.WriteString(itoa(n))
		args = append(args, f.CategoryID)
	}
	if f.Search != "" {
		n++
		b.WriteString(" AND search_tsv @@ plainto_tsquery('english', $")
		b.WriteString(itoa(n))
		b.WriteString(")")
		args = append(args, f.Search)
	}
	b.WriteString(" ORDER BY published_at DESC NULLS LAST")
	n++
	b.WriteString(" LIMIT $")
	b.WriteString(itoa(n))
	args = append(args, limit)
	n++
	b.WriteString(" OFFSET $")
	b.WriteString(itoa(n))
	args = append(args, f.Offset)

	rows, err := s.pool.Query(ctx, b.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.ClientID, &p.CategoryID, &p.Title, &p.Description, &p.BudgetType,
			&p.BudgetMinCents, &p.BudgetMaxCents, &p.Currency, &p.WeeklyHourLimit,
			&p.ExperienceRequired, &p.EstimatedDurationDs, &p.Status, &p.Visibility,
			&p.ProposalsCount, &p.HiredCount, &p.PublishedAt, &p.ClosedAt, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ── Attachments ─────────────────────────────────────────────────────────────

type Attachment struct {
	ID          string
	ProjectID   string
	S3Key       string
	Filename    string
	SizeBytes   int64
	ContentType string
	CreatedAt   time.Time
}

func (s *Store) AddAttachment(ctx context.Context, projectID, s3Key, filename string, sizeBytes int64, contentType string) (*Attachment, error) {
	a := &Attachment{}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO project_attachments (project_id, s3_key, filename, size_bytes, content_type)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id, project_id, s3_key, coalesce(filename,''), coalesce(size_bytes,0),
			coalesce(content_type,''), created_at`,
		projectID, s3Key, filename, sizeBytes, contentType).
		Scan(&a.ID, &a.ProjectID, &a.S3Key, &a.Filename, &a.SizeBytes, &a.ContentType, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// itoa is a tiny dependency-free positional-parameter index formatter.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
