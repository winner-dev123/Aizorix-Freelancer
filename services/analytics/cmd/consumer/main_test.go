package main

import "testing"

// TestSubscribesToAllDomainTopics guards the topic-subscription mismatch found at runtime:
// the escrow service emits to "escrow.events", which the consumer originally never subscribed
// to, so escrow events were silently never ingested.
func TestSubscribesToAllDomainTopics(t *testing.T) {
	required := []string{
		"escrow.events", // regression: was missing
		"user.events", "contract.events", "worksession.events", "payment.events",
		"screenshot.ingested",
	}
	have := map[string]bool{}
	for _, tp := range topics {
		have[tp] = true
	}
	for _, r := range required {
		if !have[r] {
			t.Fatalf("analytics consumer must subscribe to %q", r)
		}
	}
}
