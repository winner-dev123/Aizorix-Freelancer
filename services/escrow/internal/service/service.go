// Package service implements escrow business logic: funding a contract's escrow, allocating
// held funds to milestones/weeks, releasing them to the freelancer, and refunding the
// client. Every money movement updates the escrow_accounts balances, writes balanced
// double-entry ledger legs, and emits an event via the transactional outbox — all in one
// database transaction. The escrow_accounts row is locked FOR UPDATE while mutated.
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/aizorix/platform/escrow/internal/store"
	"github.com/aizorix/platform/pkg/outbox"
)

// ErrInsufficientFunds is re-exported from the store for the transport layer to map.
var ErrInsufficientFunds = store.ErrInsufficientFunds

type Service struct {
	store *store.Store
}

func New(st *store.Store) *Service { return &Service{store: st} }

// FundEscrow upserts the contract's escrow account and adds the amount to held_cents.
//
// LEDGER OWNERSHIP: the payment service owns the cash-in posting for a real deposit. Its
// capture posts stripe_clearing -> client_funding -> escrow once (see payment
// writeDepositLedger), which is the single authoritative credit to the 'escrow' ledger
// account. FundEscrow must therefore NOT re-post client_funding -> escrow, or the shared
// `transactions` ledger would double-credit 'escrow'. We only adjust the held_cents balance
// here and record a NON-monetary 'escrow_hold' marker on the contract account that moves
// money between two ledger views WITHOUT crediting the 'escrow' account a second time.
func (s *Service) FundEscrow(ctx context.Context, contractID string, amountCents int64, currency, idempotencyKey string) (store.Escrow, error) {
	if amountCents <= 0 {
		return store.Escrow{}, fmt.Errorf("service: amount_cents must be > 0")
	}
	if currency == "" {
		currency = "USD"
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return store.Escrow{}, err
	}
	defer tx.Rollback(ctx)

	e, err := s.store.UpsertEscrowForContract(ctx, tx, contractID, currency)
	if err != nil {
		return store.Escrow{}, err
	}
	// Lock the row before mutating the balance.
	if _, err := s.store.LockEscrow(ctx, tx, e.ID); err != nil {
		return store.Escrow{}, err
	}
	// Idempotency: claim the (contract_id, idempotency_key) pair before crediting held_cents.
	// A replayed key collides, so we roll back the credit and return the existing escrow as a
	// no-op — a duplicate fund request never inflates held_cents without a real deposit. An
	// empty key is allowed (legacy callers); only non-empty keys are deduplicated.
	if idempotencyKey != "" {
		if err := s.store.RecordFundIdempotency(ctx, tx, contractID, idempotencyKey, e.ID); err != nil {
			if errors.Is(err, store.ErrDuplicateFund) {
				// Already funded under this key: return the current escrow without re-crediting.
				_ = tx.Rollback(ctx)
				return s.store.GetEscrow(ctx, e.ID)
			}
			return store.Escrow{}, err
		}
	}
	e, err = s.store.AddHeld(ctx, tx, e.ID, amountCents)
	if err != nil {
		return store.Escrow{}, err
	}

	cid := contractID
	ref := e.ID
	// escrow_hold marker: reclassify funds the payment deposit already credited to the
	// top-level 'escrow' account into this contract's escrow sub-account. We DEBIT 'escrow'
	// and CREDIT 'escrow_contract' (the per-contract sub-account), so the NET credit to the
	// 'escrow' account kind from a real deposit stays exactly +amount (from the payment
	// capture). FundEscrow deliberately does NOT credit 'escrow' again and does NOT touch
	// client_funding, avoiding the double-credit of the shared ledger.
	if err := s.store.WriteLegs(ctx, tx, s.store.NewUUID(), []store.Leg{
		{Type: "escrow_hold", AccountKind: "escrow", AmountCents: -amountCents, Currency: e.Currency},
		{Type: "escrow_hold", AccountKind: "escrow_contract", AccountRef: &ref, ContractID: &cid, AmountCents: amountCents, Currency: e.Currency},
	}); err != nil {
		return store.Escrow{}, err
	}
	if err := outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "escrow", AggregateID: e.ID, EventType: "escrow.funded",
		Topic: "escrow.events", PartitionKey: contractID,
		Payload: map[string]any{
			"escrow_id": e.ID, "contract_id": contractID, "amount_cents": amountCents, "held_cents": e.HeldCents,
		},
	}); err != nil {
		return store.Escrow{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Escrow{}, err
	}
	return e, nil
}

// AllocateToMilestone reserves held funds for a milestone. The allocation truly reserves
// funds: the amount must not exceed the UNALLOCATED held balance, i.e.
// available = held_cents - SUM(currently-held allocations). This prevents the same dollars
// from being reserved (and later released/refunded) twice via another path.
//
// Idempotency (migration 000011): a partial UNIQUE index on (escrow_id, milestone_id) WHERE
// status <> 'refunded' makes a re-allocation of the same milestone collide; we surface the
// prior held allocation instead of reserving the funds again.
func (s *Service) AllocateToMilestone(ctx context.Context, escrowID, milestoneID string, amountCents int64) (store.Allocation, error) {
	if amountCents <= 0 {
		return store.Allocation{}, fmt.Errorf("service: amount_cents must be > 0")
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return store.Allocation{}, err
	}
	defer tx.Rollback(ctx)

	e, err := s.store.LockEscrow(ctx, tx, escrowID)
	if err != nil {
		return store.Allocation{}, err
	}
	// Idempotency: if a non-refunded allocation already exists for this milestone, return it
	// rather than reserving funds again.
	if existing, err := s.store.AllocationByMilestone(ctx, tx, escrowID, milestoneID); err == nil {
		if err := tx.Commit(ctx); err != nil {
			return store.Allocation{}, err
		}
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Allocation{}, err
	}

	allocated, err := s.store.SumHeldAllocations(ctx, tx, escrowID)
	if err != nil {
		return store.Allocation{}, err
	}
	available := e.HeldCents - allocated
	if amountCents > available {
		return store.Allocation{}, ErrInsufficientFunds
	}
	mid := milestoneID
	a, err := s.store.InsertAllocation(ctx, tx, escrowID, &mid, nil, amountCents)
	if errors.Is(err, store.ErrDuplicateAllocation) {
		// Lost an idempotency race: read back the existing allocation.
		existing, gErr := s.store.AllocationByMilestone(ctx, tx, escrowID, milestoneID)
		if gErr != nil {
			return store.Allocation{}, gErr
		}
		if err := tx.Commit(ctx); err != nil {
			return store.Allocation{}, err
		}
		return existing, nil
	}
	if err != nil {
		return store.Allocation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Allocation{}, err
	}
	return a, nil
}

// ReleaseMilestone releases a milestone's held funds to the freelancer: it flips the
// allocation to released, moves money held->released, writes escrow_release ledger legs
// (debit escrow, credit freelancer_balance), and emits escrow.released.
//
// A held allocation MUST already exist (created via AllocateToMilestone): we never
// synthesize an allocation from a caller-supplied amount, which previously allowed repeated
// releases. MarkAllocationReleased's RowsAffected==1 is the SOLE gate that the release
// happens at most once — a second call finds the allocation already 'released' (RowsAffected
// 0 -> ErrNotFound) and is rejected. The released amount is the allocation's amount, never a
// caller value, so cumulative released for the milestone can never exceed what was allocated.
func (s *Service) ReleaseMilestone(ctx context.Context, escrowID, milestoneID string, amountCents int64) (store.Escrow, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return store.Escrow{}, err
	}
	defer tx.Rollback(ctx)

	e, err := s.store.LockEscrow(ctx, tx, escrowID)
	if err != nil {
		return store.Escrow{}, err
	}

	// Require an existing held allocation for this milestone.
	alloc, err := s.store.HeldAllocationByMilestone(ctx, tx, escrowID, milestoneID)
	if err != nil {
		return store.Escrow{}, err
	}

	// Available excludes this allocation itself; the allocation's own reserved amount is what
	// we release. Guard against the held balance having drifted below the reservation.
	allocated, err := s.store.SumHeldAllocations(ctx, tx, escrowID)
	if err != nil {
		return store.Escrow{}, err
	}
	if alloc.AmountCents > e.HeldCents || allocated > e.HeldCents {
		return store.Escrow{}, ErrInsufficientFunds
	}
	// MarkAllocationReleased is the sole idempotency gate: it only matches a 'held' row, so a
	// duplicate release returns ErrNotFound and moves no money.
	if _, err := s.store.MarkAllocationReleased(ctx, tx, alloc.ID); err != nil {
		return store.Escrow{}, err
	}
	e, err = s.store.MoveHeldToReleased(ctx, tx, escrowID, alloc.AmountCents, statusAfterRelease(e.HeldCents-alloc.AmountCents))
	if err != nil {
		return store.Escrow{}, err
	}

	cid := e.ContractID
	ref := e.ID
	if err := s.store.WriteLegs(ctx, tx, s.store.NewUUID(), []store.Leg{
		{Type: "escrow_release", AccountKind: "escrow", AccountRef: &ref, ContractID: &cid, AmountCents: -alloc.AmountCents, Currency: e.Currency},
		{Type: "escrow_release", AccountKind: "freelancer_balance", ContractID: &cid, AmountCents: alloc.AmountCents, Currency: e.Currency},
	}); err != nil {
		return store.Escrow{}, err
	}
	if err := s.emitReleased(ctx, tx, e, &milestoneID, nil, alloc.AmountCents); err != nil {
		return store.Escrow{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Escrow{}, err
	}
	return e, nil
}

// ReleaseHours releases hourly-billed funds for a billing week. It creates and releases an
// allocation keyed by billing_week in one step, mirroring ReleaseMilestone.
func (s *Service) ReleaseHours(ctx context.Context, escrowID, billingWeek string, amountCents int64) (store.Escrow, error) {
	if amountCents <= 0 {
		return store.Escrow{}, fmt.Errorf("service: amount_cents must be > 0")
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return store.Escrow{}, err
	}
	defer tx.Rollback(ctx)

	e, err := s.store.LockEscrow(ctx, tx, escrowID)
	if err != nil {
		return store.Escrow{}, err
	}
	// Idempotency: a non-refunded allocation for this billing week means we already released
	// (or reserved) it; return the current escrow without moving money again.
	if _, err := s.store.AllocationByBillingWeek(ctx, tx, escrowID, billingWeek); err == nil {
		if err := tx.Commit(ctx); err != nil {
			return store.Escrow{}, err
		}
		return e, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Escrow{}, err
	}
	// available excludes funds already reserved by other held allocations so the same dollars
	// cannot be released twice via different paths.
	allocated, err := s.store.SumHeldAllocations(ctx, tx, escrowID)
	if err != nil {
		return store.Escrow{}, err
	}
	if amountCents > e.HeldCents-allocated {
		return store.Escrow{}, ErrInsufficientFunds
	}
	bw := billingWeek
	alloc, err := s.store.InsertAllocation(ctx, tx, escrowID, nil, &bw, amountCents)
	if errors.Is(err, store.ErrDuplicateAllocation) {
		// Lost an idempotency race for this billing week: do not move money again.
		if err := tx.Commit(ctx); err != nil {
			return store.Escrow{}, err
		}
		return e, nil
	}
	if err != nil {
		return store.Escrow{}, err
	}
	if _, err := s.store.MarkAllocationReleased(ctx, tx, alloc.ID); err != nil {
		return store.Escrow{}, err
	}
	e, err = s.store.MoveHeldToReleased(ctx, tx, escrowID, amountCents, statusAfterRelease(e.HeldCents-amountCents))
	if err != nil {
		return store.Escrow{}, err
	}

	cid := e.ContractID
	ref := e.ID
	if err := s.store.WriteLegs(ctx, tx, s.store.NewUUID(), []store.Leg{
		{Type: "escrow_release", AccountKind: "escrow", AccountRef: &ref, ContractID: &cid, AmountCents: -amountCents, Currency: e.Currency},
		{Type: "escrow_release", AccountKind: "freelancer_balance", ContractID: &cid, AmountCents: amountCents, Currency: e.Currency},
	}); err != nil {
		return store.Escrow{}, err
	}
	if err := s.emitReleased(ctx, tx, e, nil, &billingWeek, amountCents); err != nil {
		return store.Escrow{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Escrow{}, err
	}
	return e, nil
}

// RefundEscrow returns held funds to the client: money moves held->refunded, refund ledger
// legs are written (debit escrow, credit refunds), and escrow.refunded is emitted.
func (s *Service) RefundEscrow(ctx context.Context, escrowID string, amountCents int64, reason string) (store.Escrow, error) {
	if amountCents <= 0 {
		return store.Escrow{}, fmt.Errorf("service: amount_cents must be > 0")
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return store.Escrow{}, err
	}
	defer tx.Rollback(ctx)

	e, err := s.store.LockEscrow(ctx, tx, escrowID)
	if err != nil {
		return store.Escrow{}, err
	}
	// Only UNALLOCATED held funds may be refunded: funds reserved by a held allocation belong
	// to a milestone/week and must not be clawed back via the refund path.
	allocated, err := s.store.SumHeldAllocations(ctx, tx, escrowID)
	if err != nil {
		return store.Escrow{}, err
	}
	if amountCents > e.HeldCents-allocated {
		return store.Escrow{}, ErrInsufficientFunds
	}
	status := "held"
	if e.HeldCents-amountCents == 0 {
		status = "refunded"
	}
	e, err = s.store.MoveHeldToRefunded(ctx, tx, escrowID, amountCents, status)
	if err != nil {
		return store.Escrow{}, err
	}

	cid := e.ContractID
	ref := e.ID
	memo := reason
	var memoPtr *string
	if reason != "" {
		memoPtr = &memo
	}
	if err := s.store.WriteLegs(ctx, tx, s.store.NewUUID(), []store.Leg{
		{Type: "refund", AccountKind: "escrow", AccountRef: &ref, ContractID: &cid, AmountCents: -amountCents, Currency: e.Currency, Memo: memoPtr},
		{Type: "refund", AccountKind: "refunds", ContractID: &cid, AmountCents: amountCents, Currency: e.Currency, Memo: memoPtr},
	}); err != nil {
		return store.Escrow{}, err
	}
	if err := outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "escrow", AggregateID: e.ID, EventType: "escrow.refunded",
		Topic: "escrow.events", PartitionKey: e.ContractID,
		Payload: map[string]any{
			"escrow_id": e.ID, "contract_id": e.ContractID, "amount_cents": amountCents, "reason": reason,
		},
	}); err != nil {
		return store.Escrow{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Escrow{}, err
	}
	return e, nil
}

func (s *Service) GetEscrow(ctx context.Context, id string) (store.Escrow, error) {
	return s.store.GetEscrow(ctx, id)
}

func (s *Service) GetEscrowByContract(ctx context.Context, contractID string) (store.Escrow, error) {
	return s.store.GetEscrowByContract(ctx, contractID)
}

func (s *Service) ListAllocations(ctx context.Context, escrowID string) ([]store.Allocation, error) {
	return s.store.ListAllocations(ctx, escrowID)
}

// emitReleased writes the escrow.released event. milestoneID/billingWeek are optional.
func (s *Service) emitReleased(ctx context.Context, tx pgx.Tx, e store.Escrow, milestoneID, billingWeek *string, amountCents int64) error {
	payload := map[string]any{
		"escrow_id": e.ID, "contract_id": e.ContractID, "amount_cents": amountCents,
	}
	if milestoneID != nil {
		payload["milestone_id"] = *milestoneID
	}
	if billingWeek != nil {
		payload["billing_week"] = *billingWeek
	}
	return outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "escrow", AggregateID: e.ID, EventType: "escrow.released",
		Topic: "escrow.events", PartitionKey: e.ContractID,
		Payload: payload,
	})
}

// statusAfterRelease returns 'released' once nothing remains held, else 'partially_released'.
func statusAfterRelease(remainingHeld int64) string {
	if remainingHeld <= 0 {
		return "released"
	}
	return "partially_released"
}
