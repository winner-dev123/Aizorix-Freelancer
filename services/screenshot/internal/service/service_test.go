package service

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"
)

// TestConfirmSignedMessage_BindsNonce locks in the wave-3 crypto fix: the GCM nonce is part of the
// Ed25519-signed confirm message, so a tampered nonce changes the signed bytes (and thus fails
// verification) instead of verifying yet leaving the blob permanently undecryptable. The exact
// byte layout must also stay in lock-step with the tracker's crypto::sign_metadata.
func TestConfirmSignedMessage_BindsNonce(t *testing.T) {
	sha := bytes.Repeat([]byte{0xAB}, 32)
	nonce := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	at := time.Date(2026, 6, 23, 10, 0, 0, 0, time.UTC)
	cid := "contract-123"

	got := confirmSignedMessage(sha, nonce, at, cid)

	// Exact layout: sha256_cipher || gcm_nonce || captured_at(RFC3339 UTC) || contract_id.
	want := bytes.Join([][]byte{sha, nonce, []byte("2026-06-23T10:00:00Z"), []byte(cid)}, nil)
	if !bytes.Equal(got, want) {
		t.Fatalf("signed-message layout mismatch:\n got %x\nwant %x", got, want)
	}

	// Changing ONLY the nonce must change the signed message (the security property).
	other := confirmSignedMessage(sha, bytes.Repeat([]byte{0x99}, 12), at, cid)
	if bytes.Equal(got, other) {
		t.Fatal("changing the gcm nonce must change the signed message — the nonce is not bound")
	}
}

// TestRandomKeySuffix_Unique locks in the wave-3 fix that S3 object keys are unguessable + random
// (not derived from a client captured_at), so two slots can't collide and overwrite a prior
// screenshot's ciphertext.
func TestRandomKeySuffix_Unique(t *testing.T) {
	a, err := randomKeySuffix()
	if err != nil {
		t.Fatalf("randomKeySuffix: %v", err)
	}
	b, err := randomKeySuffix()
	if err != nil {
		t.Fatalf("randomKeySuffix: %v", err)
	}
	if a == b {
		t.Fatal("two suffixes must differ (crypto/rand)")
	}
	if len(a) != 32 { // 16 random bytes, hex-encoded
		t.Fatalf("want 32 hex chars, got %d (%q)", len(a), a)
	}
	if _, err := hex.DecodeString(a); err != nil {
		t.Fatalf("suffix is not valid hex: %v", err)
	}
}
