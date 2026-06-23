package contractparties

import "testing"

// TestParties covers the authorization primitives the review service relies on.
func TestParties(t *testing.T) {
	p := Parties{ClientID: "c1", FreelancerID: "f1"}

	if !p.IsParty("c1") || !p.IsParty("f1") {
		t.Fatal("both the client and the freelancer must be parties")
	}
	if p.IsParty("stranger") || p.IsParty("") {
		t.Fatal("a non-party (and the empty id) must not be a party")
	}
	if got := p.Opposite("c1"); got != "f1" {
		t.Fatalf("Opposite(client) = %q, want freelancer", got)
	}
	if got := p.Opposite("f1"); got != "c1" {
		t.Fatalf("Opposite(freelancer) = %q, want client", got)
	}
	if got := p.Opposite("stranger"); got != "" {
		t.Fatalf("Opposite(non-party) = %q, want empty", got)
	}
}
