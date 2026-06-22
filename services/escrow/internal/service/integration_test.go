//go:build integration

package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aizorix/platform/escrow/internal/itest"
	"github.com/aizorix/platform/escrow/internal/service"
	"github.com/aizorix/platform/escrow/internal/store"
)

// TestEscrowReserveProtectsFunds funds 100, allocates the whole 100 to milestone m1, then
// asserts no OTHER path (refund / hourly release) can spend the reserved dollars, and that
// releasing the allocation is idempotent (a second release moves no money, held never goes
// negative).
func TestEscrowReserveProtectsFunds(t *testing.T) {
	ctx := context.Background()
	pool := itest.NewPostgres(t)
	st := store.New(pool)
	svc := service.New(st)

	const amount int64 = 100
	seed := itest.SeedFixedContract(ctx, t, pool, amount)

	// Fund 100 into the contract's escrow.
	e, err := svc.FundEscrow(ctx, seed.ContractID, amount, "USD", "")
	if err != nil {
		t.Fatalf("fund: %v", err)
	}
	if e.HeldCents != amount {
		t.Fatalf("held after fund = %d, want %d", e.HeldCents, amount)
	}

	// Allocate the entire held balance to milestone m1 (reserves it).
	alloc, err := svc.AllocateToMilestone(ctx, e.ID, seed.MilestoneID, amount)
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if alloc.AmountCents != amount {
		t.Fatalf("allocation amount = %d, want %d", alloc.AmountCents, amount)
	}

	// A refund cannot claw back reserved funds: available = held - reserved = 0.
	if _, err := svc.RefundEscrow(ctx, e.ID, 1, "should-be-blocked"); !errors.Is(err, service.ErrInsufficientFunds) {
		t.Fatalf("refund of reserved funds should fail with ErrInsufficientFunds, got %v", err)
	}

	// An hourly release (a DIFFERENT path) also cannot dip into the reserved dollars.
	if _, err := svc.ReleaseHours(ctx, e.ID, "2026-W01", 1); !errors.Is(err, service.ErrInsufficientFunds) {
		t.Fatalf("hourly over-release should fail with ErrInsufficientFunds, got %v", err)
	}

	// Release the milestone allocation once: money moves held -> released.
	after, err := svc.ReleaseMilestone(ctx, e.ID, seed.MilestoneID, amount)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if after.HeldCents != 0 || after.ReleasedCents != amount {
		t.Fatalf("after release held=%d released=%d, want held=0 released=%d", after.HeldCents, after.ReleasedCents, amount)
	}

	// Idempotent re-release: the allocation is already 'released', so the second call moves
	// no money (the held/released balances are unchanged) and is rejected as not-found.
	_, err = svc.ReleaseMilestone(ctx, e.ID, seed.MilestoneID, amount)
	if err == nil {
		t.Fatal("expected second release to be rejected (no held allocation remains)")
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected store.ErrNotFound on re-release, got %v", err)
	}

	final, err := svc.GetEscrow(ctx, e.ID)
	if err != nil {
		t.Fatalf("get escrow: %v", err)
	}
	if final.HeldCents != 0 || final.ReleasedCents != amount {
		t.Fatalf("balances drifted after idempotent re-release: held=%d released=%d", final.HeldCents, final.ReleasedCents)
	}
}

// TestAllocateIsIdempotent asserts re-allocating the same milestone returns the prior
// allocation instead of reserving the funds a second time.
func TestAllocateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := itest.NewPostgres(t)
	st := store.New(pool)
	svc := service.New(st)

	const amount int64 = 5000
	seed := itest.SeedFixedContract(ctx, t, pool, amount)

	e, err := svc.FundEscrow(ctx, seed.ContractID, amount, "USD", "")
	if err != nil {
		t.Fatalf("fund: %v", err)
	}
	a1, err := svc.AllocateToMilestone(ctx, e.ID, seed.MilestoneID, amount)
	if err != nil {
		t.Fatalf("allocate #1: %v", err)
	}
	a2, err := svc.AllocateToMilestone(ctx, e.ID, seed.MilestoneID, amount)
	if err != nil {
		t.Fatalf("allocate #2 (idempotent) should succeed, got %v", err)
	}
	if a1.ID != a2.ID {
		t.Fatalf("re-allocation must return the SAME allocation, got %s vs %s", a1.ID, a2.ID)
	}
	allocs, err := svc.ListAllocations(ctx, e.ID)
	if err != nil {
		t.Fatalf("list allocations: %v", err)
	}
	if len(allocs) != 1 {
		t.Fatalf("expected exactly 1 allocation after idempotent re-allocate, got %d", len(allocs))
	}
}
