package service

import "testing"

// availableForAllocation mirrors the reservation invariant enforced in
// AllocateToMilestone / ReleaseHours / RefundEscrow:
//
//	available = held_cents - SUM(currently-held allocations)
//
// and the guard `amount > available => ErrInsufficientFunds`. The DB-bound methods
// compute `allocated` via SumHeldAllocations; this exercises the pure arithmetic and
// the over-reservation guard so the money invariant is pinned without Postgres.
func availableForAllocation(held, heldAllocations int64) int64 {
	return held - heldAllocations
}

func canReserve(held, heldAllocations, amount int64) bool {
	return amount <= availableForAllocation(held, heldAllocations)
}

func TestAvailableFunds(t *testing.T) {
	cases := []struct {
		name        string
		held        int64
		allocations int64
		want        int64
	}{
		{"nothing_allocated", 10000, 0, 10000},
		{"partial_allocated", 10000, 4000, 6000},
		{"fully_allocated", 10000, 10000, 0},
		{"over_allocated_goes_negative", 10000, 12000, -2000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := availableForAllocation(tc.held, tc.allocations); got != tc.want {
				t.Fatalf("available(held=%d, alloc=%d) = %d, want %d", tc.held, tc.allocations, got, tc.want)
			}
		})
	}
}

func TestCanReserveGuard(t *testing.T) {
	cases := []struct {
		name        string
		held        int64
		allocations int64
		amount      int64
		want        bool
	}{
		{"within_unallocated", 10000, 0, 10000, true},
		{"exact_remaining", 10000, 4000, 6000, true},
		{"exceeds_remaining", 10000, 4000, 6001, false},
		{"nothing_left", 10000, 10000, 1, false},
		{"reserved_funds_protected", 10000, 9000, 2000, false}, // can't dip into the 9000 reserved
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canReserve(tc.held, tc.allocations, tc.amount); got != tc.want {
				t.Fatalf("canReserve(held=%d, alloc=%d, amt=%d) = %v, want %v",
					tc.held, tc.allocations, tc.amount, got, tc.want)
			}
		})
	}
}

func TestStatusAfterRelease(t *testing.T) {
	cases := []struct {
		remainingHeld int64
		want          string
	}{
		{0, "released"},
		{-1, "released"}, // defensive: never report a negative remainder as partial
		{1, "partially_released"},
		{5000, "partially_released"},
	}
	for _, tc := range cases {
		if got := statusAfterRelease(tc.remainingHeld); got != tc.want {
			t.Fatalf("statusAfterRelease(%d) = %q, want %q", tc.remainingHeld, got, tc.want)
		}
	}
}
