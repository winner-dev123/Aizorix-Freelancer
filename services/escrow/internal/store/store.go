// Package store is the escrow data layer: per-contract escrow accounts with held/released/
// refunded balances, per-milestone/week allocations, and the append-only double-entry
// ledger (transactions). All money is BIGINT minor units (cents). Balance mutations lock
// the escrow_accounts row with SELECT ... FOR UPDATE to serialize concurrent movements.
package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("store: not found")
	// ErrInsufficientFunds is returned when a release/refund/allocation exceeds held funds.
	ErrInsufficientFunds = errors.New("store: insufficient escrow funds")
	// ErrDuplicateAllocation is returned when an allocation collides with the partial unique
	// index on (escrow_id, milestone_id) or (escrow_id, billing_week) for non-refunded rows
	// (migration 000011) — the natural-key idempotency guard.
	ErrDuplicateAllocation = errors.New("store: duplicate escrow allocation")
)

// isUniqueViolation reports whether err is a Postgres unique-constraint violation (23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// newUUID returns a canonical RFC 4122 version-4 UUID string (no external dependency):
// 16 random bytes with the version (4) and variant (10xx) bits set.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		ns := uint64(time.Now().UnixNano())
		for i := 0; i < 8; i++ {
			b[i] = byte(ns >> (8 * i))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10xx
	h := hex.EncodeToString(b[:])
	return strings.Join([]string{h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]}, "-")
}

// NewUUID lets the service mint txn_group ids without re-implementing the helper.
func (s *Store) NewUUID() string { return newUUID() }

// ---------------------------------------------------------------------------
// escrow_accounts
// ---------------------------------------------------------------------------

// Escrow mirrors the escrow_accounts table.
type Escrow struct {
	ID            string
	ContractID    string
	Currency      string
	HeldCents     int64
	ReleasedCents int64
	RefundedCents int64
	Status        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

const escrowCols = `id, contract_id, currency, held_cents, released_cents, refunded_cents, status, created_at, updated_at`

func scanEscrow(row pgx.Row) (Escrow, error) {
	var e Escrow
	err := row.Scan(&e.ID, &e.ContractID, &e.Currency, &e.HeldCents, &e.ReleasedCents, &e.RefundedCents, &e.Status, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

func (s *Store) GetEscrow(ctx context.Context, id string) (Escrow, error) {
	return scanEscrow(s.pool.QueryRow(ctx, `SELECT `+escrowCols+` FROM escrow_accounts WHERE id=$1`, id))
}

func (s *Store) GetEscrowByContract(ctx context.Context, contractID string) (Escrow, error) {
	return scanEscrow(s.pool.QueryRow(ctx, `SELECT `+escrowCols+` FROM escrow_accounts WHERE contract_id=$1`, contractID))
}

// UpsertEscrowForContract returns the escrow account for the contract, creating an empty
// 'held' account if none exists. It does NOT lock; call LockEscrow before mutating balances.
func (s *Store) UpsertEscrowForContract(ctx context.Context, tx pgx.Tx, contractID, currency string) (Escrow, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO escrow_accounts (contract_id, currency)
		VALUES ($1,$2)
		ON CONFLICT (contract_id) DO UPDATE SET updated_at = now()
		RETURNING `+escrowCols, contractID, currency)
	return scanEscrow(row)
}

// LockEscrow selects the account FOR UPDATE so concurrent balance mutations serialize.
func (s *Store) LockEscrow(ctx context.Context, tx pgx.Tx, id string) (Escrow, error) {
	return scanEscrow(tx.QueryRow(ctx, `SELECT `+escrowCols+` FROM escrow_accounts WHERE id=$1 FOR UPDATE`, id))
}

// AddHeld increases held_cents (used on funding).
func (s *Store) AddHeld(ctx context.Context, tx pgx.Tx, id string, amountCents int64) (Escrow, error) {
	row := tx.QueryRow(ctx, `
		UPDATE escrow_accounts
		SET held_cents = held_cents + $2, updated_at = now()
		WHERE id=$1
		RETURNING `+escrowCols, id, amountCents)
	return scanEscrow(row)
}

// MoveHeldToReleased decrements held and increments released, updating status.
func (s *Store) MoveHeldToReleased(ctx context.Context, tx pgx.Tx, id string, amountCents int64, status string) (Escrow, error) {
	row := tx.QueryRow(ctx, `
		UPDATE escrow_accounts
		SET held_cents = held_cents - $2, released_cents = released_cents + $2, status = $3, updated_at = now()
		WHERE id=$1
		RETURNING `+escrowCols, id, amountCents, status)
	return scanEscrow(row)
}

// MoveHeldToRefunded decrements held and increments refunded, updating status.
func (s *Store) MoveHeldToRefunded(ctx context.Context, tx pgx.Tx, id string, amountCents int64, status string) (Escrow, error) {
	row := tx.QueryRow(ctx, `
		UPDATE escrow_accounts
		SET held_cents = held_cents - $2, refunded_cents = refunded_cents + $2, status = $3, updated_at = now()
		WHERE id=$1
		RETURNING `+escrowCols, id, amountCents, status)
	return scanEscrow(row)
}

// ---------------------------------------------------------------------------
// escrow_allocations (per-milestone / per-week ledger)
// ---------------------------------------------------------------------------

// Allocation mirrors the escrow_allocations table. MilestoneID and BillingWeek are
// nullable (exactly one is set per the table CHECK); ReleasedAt is nullable.
type Allocation struct {
	ID          string
	EscrowID    string
	MilestoneID *string
	BillingWeek *string
	AmountCents int64
	Status      string
	CreatedAt   time.Time
	ReleasedAt  *time.Time
}

const allocCols = `id, escrow_id, milestone_id, billing_week, amount_cents, status, created_at, released_at`

func scanAllocation(row pgx.Row) (Allocation, error) {
	var a Allocation
	err := row.Scan(&a.ID, &a.EscrowID, &a.MilestoneID, &a.BillingWeek, &a.AmountCents, &a.Status, &a.CreatedAt, &a.ReleasedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// InsertAllocation records a new held allocation keyed by milestone or billing week. A
// collision with the non-refunded partial unique index (migration 000011) is mapped to
// ErrDuplicateAllocation so the service can return the prior allocation idempotently.
func (s *Store) InsertAllocation(ctx context.Context, tx pgx.Tx, escrowID string, milestoneID, billingWeek *string, amountCents int64) (Allocation, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO escrow_allocations (escrow_id, milestone_id, billing_week, amount_cents, status)
		VALUES ($1,$2,$3,$4,'held')
		RETURNING `+allocCols, escrowID, milestoneID, billingWeek, amountCents)
	a, err := scanAllocation(row)
	if err != nil && isUniqueViolation(err) {
		return a, ErrDuplicateAllocation
	}
	return a, err
}

// HeldAllocationByMilestone finds a held allocation for an escrow + milestone.
func (s *Store) HeldAllocationByMilestone(ctx context.Context, tx pgx.Tx, escrowID, milestoneID string) (Allocation, error) {
	return scanAllocation(tx.QueryRow(ctx, `
		SELECT `+allocCols+` FROM escrow_allocations
		WHERE escrow_id=$1 AND milestone_id=$2 AND status='held'
		ORDER BY created_at LIMIT 1`, escrowID, milestoneID))
}

// AllocationByMilestone finds a NON-refunded allocation (held or released) for an escrow +
// milestone — the natural idempotency key. Returns ErrNotFound if none exists.
func (s *Store) AllocationByMilestone(ctx context.Context, tx pgx.Tx, escrowID, milestoneID string) (Allocation, error) {
	return scanAllocation(tx.QueryRow(ctx, `
		SELECT `+allocCols+` FROM escrow_allocations
		WHERE escrow_id=$1 AND milestone_id=$2 AND status <> 'refunded'
		ORDER BY created_at LIMIT 1`, escrowID, milestoneID))
}

// AllocationByBillingWeek finds a NON-refunded allocation for an escrow + billing week — the
// natural idempotency key for hourly releases. Returns ErrNotFound if none exists.
func (s *Store) AllocationByBillingWeek(ctx context.Context, tx pgx.Tx, escrowID, billingWeek string) (Allocation, error) {
	return scanAllocation(tx.QueryRow(ctx, `
		SELECT `+allocCols+` FROM escrow_allocations
		WHERE escrow_id=$1 AND billing_week=$2 AND status <> 'refunded'
		ORDER BY created_at LIMIT 1`, escrowID, billingWeek))
}

// MarkAllocationReleased flips a held allocation to released and stamps released_at.
func (s *Store) MarkAllocationReleased(ctx context.Context, tx pgx.Tx, id string) (Allocation, error) {
	row := tx.QueryRow(ctx, `
		UPDATE escrow_allocations
		SET status='released', released_at=now()
		WHERE id=$1 AND status='held'
		RETURNING `+allocCols, id)
	return scanAllocation(row)
}

// SumHeldAllocations returns the total amount currently allocated-and-held for an escrow.
func (s *Store) SumHeldAllocations(ctx context.Context, tx pgx.Tx, escrowID string) (int64, error) {
	var sum int64
	err := tx.QueryRow(ctx, `
		SELECT coalesce(sum(amount_cents),0) FROM escrow_allocations WHERE escrow_id=$1 AND status='held'`, escrowID).Scan(&sum)
	return sum, err
}

func (s *Store) ListAllocations(ctx context.Context, escrowID string) ([]Allocation, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+allocCols+` FROM escrow_allocations WHERE escrow_id=$1 ORDER BY created_at`, escrowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Allocation
	for rows.Next() {
		a, err := scanAllocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// transactions (append-only double-entry ledger, shared schema with payment)
// ---------------------------------------------------------------------------

// Leg is one signed ledger entry. AmountCents is signed: debits negative, credits positive;
// the legs of a txn_group must sum to zero.
type Leg struct {
	Type        string
	AccountKind string
	AccountRef  *string
	ContractID  *string
	AmountCents int64
	Currency    string
	Memo        *string
}

// WriteLegs appends a balanced set of ledger legs sharing one txn_group, validating the
// zero-sum invariant first.
func (s *Store) WriteLegs(ctx context.Context, tx pgx.Tx, txnGroup string, legs []Leg) error {
	var sum int64
	for _, l := range legs {
		sum += l.AmountCents
	}
	if sum != 0 {
		return fmt.Errorf("store: unbalanced ledger txn_group=%s sum=%d", txnGroup, sum)
	}
	for _, l := range legs {
		_, err := tx.Exec(ctx, `
			INSERT INTO transactions
			  (txn_group, type, account_kind, account_ref, contract_id, amount_cents, currency, memo)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			txnGroup, l.Type, l.AccountKind, l.AccountRef, l.ContractID, l.AmountCents, l.Currency, l.Memo)
		if err != nil {
			return err
		}
	}
	return nil
}
