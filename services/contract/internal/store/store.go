// Package store is the contract service's data-access layer over PostgreSQL (pgx).
// It owns migration 000005: contracts, contract_events (event-sourced transitions),
// milestones, deliverables, hourly_contracts and disputes. Write methods take a pgx.Tx
// so a state change, its contract_events row, and the outbox event commit atomically.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a row lookup yields pgx.ErrNoRows.
var ErrNotFound = errors.New("store: not found")

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool exposes the pool for transactions spanning store + outbox.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// ── domain types ────────────────────────────────────────────────────────────

type Contract struct {
	ID              string
	ProjectID       string
	ProposalID      string
	ClientID        string
	FreelancerID    string
	BudgetType      string
	Currency        string
	TotalAmountCents *int64 // nullable
	HourlyRateCents  *int64 // nullable
	WeeklyHourLimit  *int   // nullable
	Status          string
	PlatformFeeBps  int
	StartedAt       *time.Time // nullable
	EndedAt         *time.Time // nullable
	EndReason       *string    // nullable
}

type Milestone struct {
	ID          string
	ContractID  string
	Seq         int
	Title       string
	Description *string // nullable
	AmountCents int64
	Status      string
	DueAt       *time.Time // nullable
	FundedAt    *time.Time // nullable
	SubmittedAt *time.Time // nullable
	ApprovedAt  *time.Time // nullable
	ReleasedAt  *time.Time // nullable
}

type MilestoneInput struct {
	Seq         int
	Title       string
	Description *string
	AmountCents int64
	DueAt       *time.Time
}

// ContractEvent is one event-sourced transition row from contract_events, used to render
// the contract activity timeline.
type ContractEvent struct {
	FromStatus *string // nullable (nil on the initial 'create' row)
	ToStatus   string
	Event      string
	ActorID    *string // nullable
	Payload    []byte  // raw JSONB
	CreatedAt  time.Time
}

type Dispute struct {
	ID             string
	ContractID     string
	MilestoneID    *string // nullable
	RaisedBy       string
	Against        string
	Reason         string
	AmountCents    *int64 // nullable
	Status         string
	ResolutionNote *string // nullable
	AssignedAdmin  *string // nullable
}

// ── contract writes ─────────────────────────────────────────────────────────

// InsertContract inserts the parent contract (status 'pending_funding') and returns its id.
func (s *Store) InsertContract(ctx context.Context, tx pgx.Tx, c Contract) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO contracts
			(project_id, proposal_id, client_id, freelancer_id, budget_type, currency,
			 total_amount_cents, hourly_rate_cents, weekly_hour_limit, status, platform_fee_bps)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending_funding',$10)
		RETURNING id`,
		c.ProjectID, c.ProposalID, c.ClientID, c.FreelancerID, c.BudgetType, c.Currency,
		c.TotalAmountCents, c.HourlyRateCents, c.WeeklyHourLimit, c.PlatformFeeBps).Scan(&id)
	return id, err
}

// InsertHourlyContract inserts the hourly_contracts settings row for an hourly contract.
func (s *Store) InsertHourlyContract(ctx context.Context, tx pgx.Tx, contractID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO hourly_contracts (contract_id) VALUES ($1)`, contractID)
	return err
}

// InsertMilestone inserts one milestones row (status defaults to 'pending') inside tx.
func (s *Store) InsertMilestone(ctx context.Context, tx pgx.Tx, contractID string, m MilestoneInput) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO milestones (contract_id, seq, title, description, amount_cents, due_at)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		contractID, m.Seq, m.Title, m.Description, m.AmountCents, m.DueAt)
	return err
}

// InsertContractEvent appends an event-sourced transition row. fromStatus is nil on create.
func (s *Store) InsertContractEvent(ctx context.Context, tx pgx.Tx, contractID string, fromStatus *string, toStatus, event string, actorID *string, payload []byte) error {
	if payload == nil {
		payload = []byte("{}")
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO contract_events (contract_id, from_status, to_status, event, actor_id, payload)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		contractID, fromStatus, toStatus, event, actorID, payload)
	return err
}

// TransitionContract updates contracts.status from a required current status to a new one.
// Returns false (no error) when the guard fails so the caller can map ErrInvalidState.
func (s *Store) TransitionContract(ctx context.Context, tx pgx.Tx, id, fromStatus, toStatus string) (bool, error) {
	ct, err := tx.Exec(ctx, `
		UPDATE contracts SET status = $3, updated_at = now()
		WHERE id = $1 AND status = $2`, id, fromStatus, toStatus)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// MarkContractStarted stamps started_at when a contract activates.
func (s *Store) MarkContractStarted(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `UPDATE contracts SET started_at = now() WHERE id = $1`, id)
	return err
}

// ── milestone state machine ─────────────────────────────────────────────────

// milestoneInfo returns the contract id, current status and amount of a milestone,
// locking the row FOR UPDATE so the transition is serialized.
func (s *Store) milestoneInfo(ctx context.Context, tx pgx.Tx, milestoneID string) (contractID, status string, amount int64, err error) {
	err = tx.QueryRow(ctx, `
		SELECT contract_id, status, amount_cents FROM milestones WHERE id = $1 FOR UPDATE`, milestoneID).
		Scan(&contractID, &status, &amount)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

// FundMilestone moves a milestone pending -> funded and stamps funded_at. Escrow funding
// itself is handled elsewhere; this only records the milestone-side state change.
func (s *Store) FundMilestone(ctx context.Context, tx pgx.Tx, milestoneID string) (contractID string, ok bool, err error) {
	contractID, status, _, err := s.milestoneInfo(ctx, tx, milestoneID)
	if err != nil {
		return "", false, err
	}
	if status != "pending" {
		return contractID, false, nil
	}
	_, err = tx.Exec(ctx, `
		UPDATE milestones SET status = 'funded', funded_at = now(), updated_at = now() WHERE id = $1`, milestoneID)
	return contractID, err == nil, err
}

// SubmitMilestone moves a milestone funded -> submitted and stamps submitted_at.
func (s *Store) SubmitMilestone(ctx context.Context, tx pgx.Tx, milestoneID string) (contractID string, ok bool, err error) {
	contractID, status, _, err := s.milestoneInfo(ctx, tx, milestoneID)
	if err != nil {
		return "", false, err
	}
	if status != "funded" {
		return contractID, false, nil
	}
	_, err = tx.Exec(ctx, `
		UPDATE milestones SET status = 'submitted', submitted_at = now(), updated_at = now() WHERE id = $1`, milestoneID)
	return contractID, err == nil, err
}

// ApproveMilestone moves a milestone submitted -> approved and stamps approved_at,
// returning the contract id and amount for the emitted event.
func (s *Store) ApproveMilestone(ctx context.Context, tx pgx.Tx, milestoneID string) (contractID string, amount int64, ok bool, err error) {
	contractID, status, amount, err := s.milestoneInfo(ctx, tx, milestoneID)
	if err != nil {
		return "", 0, false, err
	}
	if status != "submitted" {
		return contractID, amount, false, nil
	}
	_, err = tx.Exec(ctx, `
		UPDATE milestones SET status = 'approved', approved_at = now(), updated_at = now() WHERE id = $1`, milestoneID)
	return contractID, amount, err == nil, err
}

// FreelancerOfContract returns the freelancer party of a contract (used to enrich events).
func (s *Store) FreelancerOfContract(ctx context.Context, tx pgx.Tx, contractID string) (string, error) {
	var fid string
	err := tx.QueryRow(ctx, `SELECT freelancer_id FROM contracts WHERE id = $1`, contractID).Scan(&fid)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return fid, err
}

// InsertDeliverable records a submission against a milestone inside tx.
func (s *Store) InsertDeliverable(ctx context.Context, tx pgx.Tx, milestoneID, submittedBy, note string, s3Keys []string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO deliverables (milestone_id, submitted_by, note, s3_keys)
		VALUES ($1,$2,$3,$4)`, milestoneID, submittedBy, note, s3Keys)
	return err
}

// ── disputes ────────────────────────────────────────────────────────────────

// InsertDispute inserts a disputes row (status 'open') and returns its id.
func (s *Store) InsertDispute(ctx context.Context, tx pgx.Tx, d Dispute) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO disputes (contract_id, milestone_id, raised_by, against, reason, amount_cents, status)
		VALUES ($1,$2,$3,$4,$5,$6,'open')
		RETURNING id`,
		d.ContractID, d.MilestoneID, d.RaisedBy, d.Against, d.Reason, d.AmountCents).Scan(&id)
	return id, err
}

// CurrentStatus returns the contract's current status (used to record from_status on a
// dispute transition, which can originate from several states).
func (s *Store) CurrentStatus(ctx context.Context, tx pgx.Tx, contractID string) (string, error) {
	var st string
	err := tx.QueryRow(ctx, `SELECT status FROM contracts WHERE id = $1 FOR UPDATE`, contractID).Scan(&st)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return st, err
}

// ForceStatus sets contracts.status unconditionally (the dispute path); call CurrentStatus
// first to capture from_status for the event row.
func (s *Store) ForceStatus(ctx context.Context, tx pgx.Tx, contractID, toStatus string) error {
	_, err := tx.Exec(ctx, `UPDATE contracts SET status = $2, updated_at = now() WHERE id = $1`, contractID, toStatus)
	return err
}

// ── reads ───────────────────────────────────────────────────────────────────

// GetContract returns a contract by id (without milestones).
func (s *Store) GetContract(ctx context.Context, id string) (*Contract, error) {
	c := &Contract{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, project_id, proposal_id, client_id, freelancer_id, budget_type, currency,
		       total_amount_cents, hourly_rate_cents, weekly_hour_limit, status, platform_fee_bps,
		       started_at, ended_at, end_reason
		FROM contracts WHERE id = $1`, id).
		Scan(&c.ID, &c.ProjectID, &c.ProposalID, &c.ClientID, &c.FreelancerID, &c.BudgetType,
			&c.Currency, &c.TotalAmountCents, &c.HourlyRateCents, &c.WeeklyHourLimit, &c.Status,
			&c.PlatformFeeBps, &c.StartedAt, &c.EndedAt, &c.EndReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// GetContractTx reads a contract within an open transaction (used to enrich events while
// holding the same tx that performed the transition).
func (s *Store) GetContractTx(ctx context.Context, tx pgx.Tx, id string) (*Contract, error) {
	c := &Contract{}
	err := tx.QueryRow(ctx, `
		SELECT id, project_id, proposal_id, client_id, freelancer_id, budget_type, currency,
		       total_amount_cents, hourly_rate_cents, weekly_hour_limit, status, platform_fee_bps,
		       started_at, ended_at, end_reason
		FROM contracts WHERE id = $1`, id).
		Scan(&c.ID, &c.ProjectID, &c.ProposalID, &c.ClientID, &c.FreelancerID, &c.BudgetType,
			&c.Currency, &c.TotalAmountCents, &c.HourlyRateCents, &c.WeeklyHourLimit, &c.Status,
			&c.PlatformFeeBps, &c.StartedAt, &c.EndedAt, &c.EndReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ListMilestones returns the milestones of a contract ordered by seq.
func (s *Store) ListMilestones(ctx context.Context, contractID string) ([]Milestone, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, contract_id, seq, title, description, amount_cents, status,
		       due_at, funded_at, submitted_at, approved_at, released_at
		FROM milestones WHERE contract_id = $1 ORDER BY seq`, contractID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Milestone
	for rows.Next() {
		var m Milestone
		if err := rows.Scan(&m.ID, &m.ContractID, &m.Seq, &m.Title, &m.Description, &m.AmountCents,
			&m.Status, &m.DueAt, &m.FundedAt, &m.SubmittedAt, &m.ApprovedAt, &m.ReleasedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListContractEvents returns the event-sourced transition rows for a contract ordered by id
// (insertion order), powering the activity timeline.
func (s *Store) ListContractEvents(ctx context.Context, contractID string) ([]ContractEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT from_status, to_status, event, actor_id, payload, created_at
		FROM contract_events WHERE contract_id = $1 ORDER BY id`, contractID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContractEvent
	for rows.Next() {
		var e ContractEvent
		if err := rows.Scan(&e.FromStatus, &e.ToStatus, &e.Event, &e.ActorID, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListForUser lists contracts where the user is the client or freelancer per role.
func (s *Store) ListForUser(ctx context.Context, userID, role string) ([]Contract, error) {
	col := "client_id"
	if role == "freelancer" {
		col = "freelancer_id"
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, project_id, proposal_id, client_id, freelancer_id, budget_type, currency,
		       total_amount_cents, hourly_rate_cents, weekly_hour_limit, status, platform_fee_bps,
		       started_at, ended_at, end_reason
		FROM contracts WHERE `+col+` = $1
		ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Contract
	for rows.Next() {
		var c Contract
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.ProposalID, &c.ClientID, &c.FreelancerID,
			&c.BudgetType, &c.Currency, &c.TotalAmountCents, &c.HourlyRateCents, &c.WeeklyHourLimit,
			&c.Status, &c.PlatformFeeBps, &c.StartedAt, &c.EndedAt, &c.EndReason); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
