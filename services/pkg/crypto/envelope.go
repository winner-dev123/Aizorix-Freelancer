package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

// KeyProvider abstracts the master-key custodian. In production this is AWS KMS
// (GenerateDataKey / Decrypt); in dev a LocalKeyProvider wraps with a static master key.
// The plaintext data key (DEK) never leaves memory unencrypted; only the wrapped DEK is
// persisted alongside the ciphertext.
type KeyProvider interface {
	// GenerateDEK returns a fresh 256-bit plaintext DEK and its wrapped form.
	GenerateDEK() (plaintext, wrapped []byte, keyID string, err error)
	// WrapDEK wraps a caller-supplied 256-bit DEK. Used for offline-first capture: the device
	// generates the DEK locally and encrypts immediately, then the server wraps that same key
	// at sync time so the stored wrapped DEK decrypts the already-uploaded ciphertext.
	WrapDEK(plaintext []byte) (wrapped []byte, keyID string, err error)
	// UnwrapDEK recovers the plaintext DEK from its wrapped form.
	UnwrapDEK(wrapped []byte) (plaintext []byte, err error)
}

// Envelope holds the result of encrypting a payload with a per-object DEK.
type Envelope struct {
	WrappedDEK []byte
	Nonce      []byte
	Ciphertext []byte
	KeyID      string
}

// EncryptEnvelope generates a DEK via the provider, AES-256-GCM encrypts the plaintext,
// and returns the wrapped DEK + nonce + ciphertext. `aad` (additional authenticated data,
// e.g. contract_id||captured_at) binds the ciphertext to its context.
func EncryptEnvelope(kp KeyProvider, plaintext, aad []byte) (*Envelope, error) {
	dek, wrapped, keyID, err := kp.GenerateDEK()
	if err != nil {
		return nil, fmt.Errorf("generate dek: %w", err)
	}
	defer zero(dek)

	gcm, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, plaintext, aad)
	return &Envelope{WrappedDEK: wrapped, Nonce: nonce, Ciphertext: ct, KeyID: keyID}, nil
}

// DecryptEnvelope unwraps the DEK and AES-256-GCM decrypts. `aad` must match what was
// supplied at encryption time or authentication fails.
func DecryptEnvelope(kp KeyProvider, env *Envelope, aad []byte) ([]byte, error) {
	dek, err := kp.UnwrapDEK(env.WrappedDEK)
	if err != nil {
		return nil, fmt.Errorf("unwrap dek: %w", err)
	}
	defer zero(dek)

	gcm, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	pt, err := gcm.Open(nil, env.Nonce, env.Ciphertext, aad)
	if err != nil {
		return nil, errors.New("crypto: decryption failed (tampered or wrong context)")
	}
	return pt, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// ── LocalKeyProvider: dev/test only. Wraps DEKs with AES-256-GCM under a static master key. ──
type LocalKeyProvider struct {
	master []byte // 32 bytes
	keyID  string
}

func NewLocalKeyProvider(master []byte) (*LocalKeyProvider, error) {
	if len(master) != 32 {
		return nil, errors.New("crypto: local master key must be 32 bytes")
	}
	sum := sha256.Sum256(master)
	return &LocalKeyProvider{master: master, keyID: fmt.Sprintf("local:%x", sum[:4])}, nil
}

func (l *LocalKeyProvider) GenerateDEK() ([]byte, []byte, string, error) {
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return nil, nil, "", err
	}
	wrapped, err := l.wrap(dek)
	if err != nil {
		return nil, nil, "", err
	}
	return dek, wrapped, l.keyID, nil
}

func (l *LocalKeyProvider) WrapDEK(plaintext []byte) ([]byte, string, error) {
	if len(plaintext) != 32 {
		return nil, "", errors.New("crypto: DEK must be 32 bytes")
	}
	wrapped, err := l.wrap(plaintext)
	if err != nil {
		return nil, "", err
	}
	return wrapped, l.keyID, nil
}

func (l *LocalKeyProvider) UnwrapDEK(wrapped []byte) ([]byte, error) {
	gcm, err := newGCM(l.master)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(wrapped) < ns {
		return nil, errors.New("crypto: wrapped dek too short")
	}
	return gcm.Open(nil, wrapped[:ns], wrapped[ns:], []byte(l.keyID))
}

func (l *LocalKeyProvider) wrap(dek []byte) ([]byte, error) {
	gcm, err := newGCM(l.master)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(nonce, gcm.Seal(nil, nonce, dek, []byte(l.keyID))...), nil
}

// NOTE: KMSKeyProvider (production) implements the same interface using
// kms.GenerateDataKey + kms.Decrypt. It lives in pkg/crypto/kms_aws.go behind a build
// tag so the AWS SDK is not pulled into builds/tests that only need the local provider.
