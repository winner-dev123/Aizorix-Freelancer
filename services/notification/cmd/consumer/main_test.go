package main

import "testing"

// TestSubscribesToEscrowEvents guards the topic-subscription mismatch found at runtime:
// escrow.events must be in the notification consumer's subscription list (escrow.funded /
// escrow.released drive client-facing payout notifications).
func TestSubscribesToEscrowEvents(t *testing.T) {
	required := []string{"escrow.events", "contract.events", "worksession.events", "user.events"}
	have := map[string]bool{}
	for _, tp := range topics {
		have[tp] = true
	}
	for _, r := range required {
		if !have[r] {
			t.Fatalf("notification consumer must subscribe to %q", r)
		}
	}
}
