package store

import (
	"bytes"
	"testing"
)

// TestAuditRowHash locks in the wave-3 fix that audit_logs.row_hash is populated with a
// deterministic, tamper-evident stamp: the same row hashes identically, any field change changes
// the hash, and the record-separator framing makes field boundaries unambiguous (so "ab|c" and
// "a|bc" can't collide).
func TestAuditRowHash(t *testing.T) {
	s := func(v string) *string { return &v }
	base := AuditLog{
		ActorType: "user", ActorID: s("u1"), Action: "user.suspend",
		ResourceType: "user", ResourceID: s("u2"), IP: s("1.2.3.4"),
		UserAgent: s("agent"), Context: []byte(`{"reason":"abuse"}`),
	}

	h := auditRowHash(base)
	if len(h) != 32 {
		t.Fatalf("sha256 must be 32 bytes, got %d", len(h))
	}
	if !bytes.Equal(h, auditRowHash(base)) {
		t.Fatal("hash must be deterministic for the same row")
	}

	// Tamper-evidence: changing any stamped field changes the hash.
	mod := base
	mod.Action = "user.reinstate"
	if bytes.Equal(auditRowHash(mod), h) {
		t.Fatal("changing the action must change the row hash")
	}

	// Field boundaries are unambiguous (the 0x1e separator): (ActorType=ab, Action=c) must not
	// hash the same as (ActorType=a, Action=bc).
	x := AuditLog{ActorType: "ab", Action: "c"}
	y := AuditLog{ActorType: "a", Action: "bc"}
	if bytes.Equal(auditRowHash(x), auditRowHash(y)) {
		t.Fatal("field-boundary collision: separator framing is not working")
	}
}
