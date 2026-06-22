// Package store is the payment data layer: payments, the append-only double-entry
// ledger (transactions), Stripe event de-duplication, withdrawals, and payout accounts.
// All money is stored in BIGINT minor units (cents). Writes that must be atomic with an
// outbox event take a pgx.Tx supplied by the service layer.
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
	// ErrDuplicateKey is returned when an idempotency_key collides with an existing row.
	ErrDuplicateKey = errors.New("store: duplicate idempotency key")
)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store  { return &Store{pool: pool} }
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// newUUID returns a canonical RFC 4122 version-4 UUID string without pulling in an
// external dependency: 16 random bytes with the version (4) and variant (10xx) bits set.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is fatal-grade; fall back to a time-seeded value so callers
		// never receive an empty id (collisions here are astronomically unlikely anyway).
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

// NewUUID is exported so the service layer can mint txn_group ids without re-implementing it.
func (s *Store) NewUUID() string { return newUUID() }

// randHex returns 2*n lowercase hex characters of cryptographic randomness, used to
// synthesize stubbed Stripe object ids (e.g. "pi_"+randHex).
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		ns := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(ns >> (8 * (i % 8)))
		}
	}
	return hex.EncodeToString(b)
}

// RandToken exposes randHex for the service layer's stub Stripe client.
func (s *Store) RandToken(n int) string { return randHex(n) }

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

// ---------------------------------------------------------------------------
// payments
// ---------------------------------------------------------------------------

// Payment mirrors the payments table. Nullable columns are pointers.
type Payment struct {
	ID                    string
	ContractID            *string
	PayerID               string
	AmountCents           int64
	Currency              string
	Status                string
	StripePaymentIntentID *string
	StripeChargeID        *string
	IdempotencyKey        *string
	FailureReason         *string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

func scanPayment(row pgx.Row) (Payment, error) {
	var p Payment
	err := row.Scan(
		&p.ID, &p.ContractID, &p.PayerID, &p.AmountCents, &p.Currency, &p.Status,
		&p.StripePaymentIntentID, &p.StripeChargeID, &p.IdempotencyKey, &p.FailureReason,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

const paymentCols = `id, contract_id, payer_id, amount_cents, currency, status,
	stripe_payment_intent_id, stripe_charge_id, idempotency_key, failure_reason,
	created_at, updated_at`

// InsertPayment creates a payment row in status 'processing'. A non-empty idempotencyKey
// is UNIQUE; on collision the error is mapped to ErrDuplicateKey so the service can read
// back the existing row.
func (s *Store) InsertPayment(ctx context.Context, tx pgx.Tx, payerID string, contractID *string, amountCents int64, currency, intentID string, idempotencyKey *string) (Payment, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO payments (payer_id, contract_id, amount_cents, currency, status, stripe_payment_intent_id, idempotency_key)
		VALUES ($1,$2,$3,$4,'processing',$5,$6)
		RETURNING `+paymentCols,
		payerID, contractID, amountCents, currency, intentID, idempotencyKey)
	p, err := scanPayment(row)
	if err != nil && isUniqueViolation(err) {
		return p, ErrDuplicateKey
	}
	return p, err
}

func (s *Store) GetPayment(ctx context.Context, id string) (Payment, error) {
	return scanPayment(s.pool.QueryRow(ctx, `SELECT `+paymentCols+` FROM payments WHERE id=$1`, id))
}

func (s *Store) GetPaymentByIdempotencyKey(ctx context.Context, key string) (Payment, error) {
	return scanPayment(s.pool.QueryRow(ctx, `SELECT `+paymentCols+` FROM payments WHERE idempotency_key=$1`, key))
}

// GetPaymentByIntent looks up a payment by its Stripe PaymentIntent id (used by webhooks).
func (s *Store) GetPaymentByIntent(ctx context.Context, tx pgx.Tx, intentID string) (Payment, error) {
	return scanPayment(tx.QueryRow(ctx, `SELECT `+paymentCols+` FROM payments WHERE stripe_payment_intent_id=$1`, intentID))
}

// MarkSucceeded transitions processing -> succeeded and records the charge id.
func (s *Store) MarkSucceeded(ctx context.Context, tx pgx.Tx, id, chargeID string) (Payment, error) {
	row := tx.QueryRow(ctx, `
		UPDATE payments
		SET status='succeeded', stripe_charge_id=$2, updated_at=now()
		WHERE id=$1 AND status='processing'
		RETURNING `+paymentCols, id, chargeID)
	return scanPayment(row)
}

func (s *Store) ListPaymentsForContract(ctx context.Context, contractID string) ([]Payment, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+paymentCols+` FROM payments WHERE contract_id=$1 ORDER BY created_at DESC`, contractID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Payment
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// transactions (append-only double-entry ledger)
// ---------------------------------------------------------------------------

// Leg is one signed entry of a balanced transaction group. AmountCents is signed:
// debits are negative, credits positive; the legs of a TxnGroup must sum to zero.
type Leg struct {
	Type        string
	AccountKind string
	AccountRef  *string
	ContractID  *string
	AmountCents int64
	Currency    string
	PaymentID   *string
	Memo        *string
}

// WriteLegs appends a balanced set of ledger legs sharing one txn_group. It validates the
// invariant (sum of signed amounts == 0) before touching the database.
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
			  (txn_group, type, account_kind, account_ref, contract_id, amount_cents, currency, payment_id, memo)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			txnGroup, l.Type, l.AccountKind, l.AccountRef, l.ContractID, l.AmountCents, l.Currency, l.PaymentID, l.Memo)
		if err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// stripe_events (webhook de-duplication)
// ---------------------------------------------------------------------------

// InsertStripeEvent records the event id ON CONFLICT DO NOTHING. It returns true when the
// row was newly inserted (i.e. this is the first time we see the event), false when the
// event was already processed and should be ignored.
func (s *Store) InsertStripeEvent(ctx context.Context, tx pgx.Tx, id, typ string, payload []byte) (bool, error) {
	ct, err := tx.Exec(ctx, `
		INSERT INTO stripe_events (id, type, payload)
		VALUES ($1,$2,$3)
		ON CONFLICT (id) DO NOTHING`, id, typ, payload)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// MarkStripeEventProcessed stamps processed_at=now() for the event.
func (s *Store) MarkStripeEventProcessed(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `UPDATE stripe_events SET processed_at=now() WHERE id=$1`, id)
	return err
}
