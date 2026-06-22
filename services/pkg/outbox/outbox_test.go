package outbox

import "testing"

// TestEventIDGloballyUnique locks in the fix for the cross-database dedupe collision: with the
// one-DB-per-service topology, each service's outbox sequence starts at 1, so the dedupe key
// MUST be namespaced by the relay's source or consumers reading from multiple service DBs drop
// events as false duplicates (e.g. payment row 42 vs contract row 42).
func TestEventIDGloballyUnique(t *testing.T) {
	if got := eventID("payment", 42); got != "payment:42" {
		t.Fatalf("eventID(payment,42) = %q, want payment:42", got)
	}
	// Same numeric id from two different source DBs must NOT collide.
	if eventID("payment", 42) == eventID("contract", 42) {
		t.Fatal("event ids from different source databases must not collide")
	}
	// Single shared DB (dev/demo): no source -> bare id (already unique within one outbox).
	if got := eventID("", 7); got != "7" {
		t.Fatalf("eventID(\"\",7) = %q, want 7", got)
	}
}
