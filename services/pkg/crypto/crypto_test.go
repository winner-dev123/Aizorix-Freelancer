package crypto

import (
	"bytes"
	"testing"
)

func TestPasswordHashVerify(t *testing.T) {
	p := DefaultArgon2Params()
	// Keep the test fast.
	p.Memory = 16 * 1024
	p.Iterations = 1

	hash, err := HashPassword("correct horse battery staple", p)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	ok, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil || !ok {
		t.Fatalf("expected match, got ok=%v err=%v", ok, err)
	}
	bad, err := VerifyPassword("wrong password", hash)
	if err != nil {
		t.Fatalf("verify err: %v", err)
	}
	if bad {
		t.Fatal("expected mismatch for wrong password")
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	master := bytes.Repeat([]byte{0x42}, 32)
	kp, err := NewLocalKeyProvider(master)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	plaintext := []byte("a screenshot's worth of bytes")
	aad := []byte("contract-123|2026-06-22T10:00:00Z")

	env, err := EncryptEnvelope(kp, plaintext, aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Equal(env.Ciphertext, plaintext) {
		t.Fatal("ciphertext must differ from plaintext")
	}
	got, err := DecryptEnvelope(kp, env, aad)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round trip mismatch: %q", got)
	}
	// Wrong AAD must fail authentication.
	if _, err := DecryptEnvelope(kp, env, []byte("different-context")); err == nil {
		t.Fatal("expected auth failure with wrong AAD")
	}
}
