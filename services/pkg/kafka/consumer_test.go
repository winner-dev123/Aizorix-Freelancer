package kafka

import (
	"context"
	"errors"
	"testing"
)

// fakeDeduper is an in-memory Deduplicator for testing the mark-after-success ordering.
type fakeDeduper struct {
	processed map[string]bool
	marks     int
}

func newFakeDeduper() *fakeDeduper { return &fakeDeduper{processed: map[string]bool{}} }
func (f *fakeDeduper) AlreadyProcessed(_ context.Context, _, eventID string) (bool, error) {
	return f.processed[eventID], nil
}
func (f *fakeDeduper) MarkProcessed(_ context.Context, _, eventID string) error {
	f.processed[eventID] = true
	f.marks++
	return nil
}

// TestProcess_MarksOnlyAfterSuccess locks in the fix for the dedupe-before-process bug: an
// event must be recorded as processed ONLY after its handler succeeds, never on the check —
// otherwise a transient handler failure permanently skips the event on redelivery.
func TestProcess_MarksOnlyAfterSuccess(t *testing.T) {
	dd := newFakeDeduper()
	c := &Consumer{group: "g", dedupe: dd, maxRetries: 1} // maxRetries=1 => no backoff sleep

	// 1) Failing handler must NOT mark and must NOT commit (so it gets redelivered).
	failing := func(context.Context, Message) error { return errors.New("boom") }
	if c.process(context.Background(), Message{EventID: "e1"}, failing) {
		t.Fatal("failing handler should not commit the offset")
	}
	if dd.marks != 0 || dd.processed["e1"] {
		t.Fatalf("failing handler must NOT mark processed (the original bug); marks=%d", dd.marks)
	}

	// 2) The same event then succeeds: must mark exactly once and commit.
	calls := 0
	ok := func(context.Context, Message) error { calls++; return nil }
	if !c.process(context.Background(), Message{EventID: "e1"}, ok) {
		t.Fatal("successful handler should commit the offset")
	}
	if dd.marks != 1 || !dd.processed["e1"] {
		t.Fatalf("successful handler must mark exactly once; marks=%d", dd.marks)
	}
	if calls != 1 {
		t.Fatalf("handler should have run exactly once; ran %d", calls)
	}

	// 3) Replay of an already-processed event: handler must NOT run, but offset still commits.
	if !c.process(context.Background(), Message{EventID: "e1"}, ok) {
		t.Fatal("already-processed event should commit (advance offset)")
	}
	if calls != 1 {
		t.Fatalf("handler must not re-run for an already-processed event; ran %d", calls)
	}
}

// TestProcess_NoDedupeAlwaysCommitsOnSuccess covers the path with dedupe disabled.
func TestProcess_NoDedupeAlwaysCommitsOnSuccess(t *testing.T) {
	c := &Consumer{group: "g", maxRetries: 1}
	if !c.process(context.Background(), Message{EventID: ""}, func(context.Context, Message) error { return nil }) {
		t.Fatal("success without dedupe should commit")
	}
	if c.process(context.Background(), Message{EventID: ""}, func(context.Context, Message) error { return errors.New("x") }) {
		t.Fatal("failure without dedupe/DLQ should not commit")
	}
}
