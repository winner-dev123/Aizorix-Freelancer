// Package service implements the screenshot control plane: issuing encrypted upload slots,
// confirming uploads with integrity material, and serving authorized, audited views.
//
// Security model (Phase 8):
//   - A fresh AES-256 data key (DEK) is generated per screenshot via the KeyProvider (KMS in
//     prod). The plaintext DEK is returned to the device ONCE over mTLS so it can encrypt the
//     image locally; only the KMS-wrapped DEK is persisted.
//   - The device uploads ciphertext directly to S3 via a presigned PUT (blob never touches us).
//   - On confirm, the device supplies sha256(ciphertext) + an Ed25519 signature; an async job
//     re-hashes the S3 object and flags mismatches (tamper detection).
//   - Views are authorized (contract party or admin) and every view is audited.
package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/aizorix/platform/pkg/crypto"
	"github.com/aizorix/platform/pkg/outbox"
	"github.com/aizorix/platform/screenshot/internal/storage"
	"github.com/aizorix/platform/screenshot/internal/store"
)

var ErrForbidden = errors.New("forbidden: not a party to this contract")

type Service struct {
	store     *store.Store
	keys      crypto.KeyProvider
	presigner storage.Presigner
	bucket    string
}

func New(st *store.Store, kp crypto.KeyProvider, ps storage.Presigner, bucket string) *Service {
	return &Service{store: st, keys: kp, presigner: ps, bucket: bucket}
}

type UploadSlot struct {
	ScreenshotID string
	UploadURL    string
	Bucket, Key  string
	WrappedDEK   []byte
	PlaintextDEK []byte // returned once, over mTLS; never stored
	KMSKeyID     string
	Headers      map[string]string
}

// RequestUploadSlot generates the per-screenshot DEK, records the pending row, and presigns
// a PUT. The device encrypts with PlaintextDEK and uploads to UploadURL.
func (s *Service) RequestUploadSlot(ctx context.Context, contractID, sessionID, sliceID, freelancerID, deviceID string, capturedAt time.Time, clientDEK []byte) (*UploadSlot, error) {
	// A non-empty client DEK means the device chose the offline-first path and already encrypted
	// with it; reject a present-but-wrong-length key instead of silently minting a SERVER DEK
	// (which would never decrypt the device's ciphertext — a permanent, silent view failure).
	if len(clientDEK) != 0 && len(clientDEK) != 32 {
		return nil, fmt.Errorf("client_dek must be exactly 32 bytes when provided")
	}
	var dek, wrapped []byte
	var keyID string
	var err error
	if len(clientDEK) == 32 {
		// Offline-first: the device already AES-256-GCM encrypted with this DEK before any
		// network was available. We wrap THAT key (never returning a plaintext DEK), so the
		// stored wrapped DEK decrypts the already-uploaded ciphertext at view time.
		wrapped, keyID, err = s.keys.WrapDEK(clientDEK)
	} else {
		// Online path: the server mints the DEK and returns plaintext_dek (over mTLS) for the
		// device to encrypt with before uploading.
		dek, wrapped, keyID, err = s.keys.GenerateDEK()
	}
	if err != nil {
		return nil, fmt.Errorf("dek: %w", err)
	}
	// Partition the key space by contract + day for lifecycle/retention; the filename itself is
	// random (NOT derived from captured_at) so two slots can never collide on one S3 object.
	suffix, err := randomKeySuffix()
	if err != nil {
		return nil, fmt.Errorf("key: %w", err)
	}
	candidateKey := fmt.Sprintf("contracts/%s/%s/%s.webp.enc", contractID, capturedAt.Format("2006/01/02"), suffix)

	// CreateSlot returns the ACTUAL stored key — the candidate on a fresh insert, or the existing
	// row's key on an idempotent retry — so the presign below always targets the right object.
	ssID, key, err := s.store.CreateSlot(ctx, store.NewSlot{
		ContractID: contractID, SessionID: sessionID, SliceID: sliceID, FreelancerID: freelancerID,
		DeviceID: deviceID, CapturedAt: capturedAt, Bucket: s.bucket, Key: candidateKey,
		WrappedDEK: wrapped, KMSKeyID: keyID,
	})
	if err != nil {
		return nil, err
	}
	url, headers, err := s.presigner.PresignPut(ctx, s.bucket, key, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	return &UploadSlot{
		ScreenshotID: ssID, UploadURL: url, Bucket: s.bucket, Key: key,
		WrappedDEK: wrapped, PlaintextDEK: dek, KMSKeyID: keyID, Headers: headers,
	}, nil
}

type ConfirmInput struct {
	ScreenshotID    string
	SHA256Cipher    []byte
	GCMNonce        []byte
	DeviceSignature []byte
	DevicePubKey    []byte // Ed25519 public key registered for the device
	CapturedAt      time.Time
	ContractID      string
	Width, Height   int
	SizeBytes       int64
	Format          string
	PHash           []byte
	ActivityPct     int
}

// ConfirmUpload validates the device signature, marks the screenshot stored, and emits
// screenshot.ingested (outbox) for the timetracking + fraud consumers.
func (s *Service) ConfirmUpload(ctx context.Context, in ConfirmInput) error {
	// Resolve the SERVER-trusted context recorded when the slot was issued, and the device's
	// ENROLLED public key. Verification uses these — never the contract_id / captured_at /
	// pubkey supplied in the (attacker-controllable) confirm request. Verification is MANDATORY:
	// a missing signature or unenrolled device is rejected (no fail-open).
	contractID, capturedAt, deviceID, err := s.store.ConfirmContext(ctx, in.ScreenshotID)
	if err != nil {
		return err
	}
	pubkey, err := s.store.DeviceAttestationKey(ctx, deviceID)
	if err != nil || len(pubkey) != ed25519.PublicKeySize {
		return errors.New("no enrolled device key for this screenshot; cannot verify integrity")
	}
	if len(in.DeviceSignature) == 0 {
		return errors.New("missing device signature")
	}
	if !ed25519.Verify(pubkey, confirmSignedMessage(in.SHA256Cipher, in.GCMNonce, capturedAt, contractID), in.DeviceSignature) {
		return errors.New("device signature verification failed")
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, sessionID, sliceID, err := s.store.ConfirmUpload(ctx, tx, store.Confirm{
		ScreenshotID: in.ScreenshotID, SHA256Cipher: in.SHA256Cipher, GCMNonce: in.GCMNonce,
		DeviceSignature: in.DeviceSignature, Width: in.Width, Height: in.Height,
		SizeBytes: in.SizeBytes, Format: in.Format, PHash: in.PHash, ActivityPct: in.ActivityPct,
	})
	if err != nil {
		return err
	}
	if err := outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "screenshot", AggregateID: in.ScreenshotID, EventType: "screenshot.ingested",
		Topic: "screenshot.ingested", PartitionKey: contractID,
		Payload: map[string]any{
			"screenshot_id": in.ScreenshotID, "contract_id": contractID, "session_id": sessionID,
			"slice_id": sliceID, "activity_pct": in.ActivityPct, "phash": in.PHash,
			"captured_at": capturedAt,
		},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListByContract returns authorized screenshot summaries for a contract's review grid.
func (s *Service) ListByContract(ctx context.Context, contractID, viewerID string, isAdmin bool) ([]store.Summary, error) {
	if !isAdmin {
		party, err := s.store.IsContractParty(ctx, contractID, viewerID)
		if err != nil {
			return nil, err
		}
		if !party {
			return nil, ErrForbidden
		}
	}
	return s.store.ListByContract(ctx, contractID, 200)
}

type View struct {
	ScreenshotID string
	DownloadURL  string
	WrappedDEK   []byte
	GCMNonce     []byte
	CapturedAt   time.Time
	Status       string
	Flagged      bool
}

// GetScreenshot authorizes the viewer (contract party; admins are allowed at the transport
// layer via the screenshot:audit permission), records the view, and returns a signed GET URL
// plus the wrapped DEK + nonce so the caller can decrypt.
func (s *Service) GetScreenshot(ctx context.Context, screenshotID, viewerID string, isAdmin bool) (*View, error) {
	v, err := s.store.GetForView(ctx, screenshotID)
	if err != nil {
		return nil, err
	}
	if !isAdmin {
		party, err := s.store.IsContractParty(ctx, v.ContractID, viewerID)
		if err != nil {
			return nil, err
		}
		if !party {
			return nil, ErrForbidden
		}
	}
	url, err := s.presigner.PresignGet(ctx, v.Bucket, v.Key, 2*time.Minute)
	if err != nil {
		return nil, err
	}
	_ = s.store.RecordView(ctx, screenshotID) // counter; full audit row written by transport
	return &View{
		ScreenshotID: screenshotID, DownloadURL: url, WrappedDEK: v.WrappedDEK,
		GCMNonce: v.GCMNonce, CapturedAt: v.CapturedAt, Status: v.Status, Flagged: v.Flagged,
	}, nil
}

// confirmSignedMessage builds the Ed25519-signed confirm message byte-for-byte as the device's
// sign_metadata does: sha256_cipher || gcm_nonce || captured_at(RFC3339 UTC) || contract_id. The
// GCM nonce is bound in so a tampered nonce can't pass verification yet leave the blob
// undecryptable. The byte layout MUST stay in lock-step with the tracker's crypto::sign_metadata.
func confirmSignedMessage(sha256Cipher, gcmNonce []byte, capturedAt time.Time, contractID string) []byte {
	msg := make([]byte, 0, len(sha256Cipher)+len(gcmNonce)+32+len(contractID))
	msg = append(msg, sha256Cipher...)
	msg = append(msg, gcmNonce...)
	msg = append(msg, []byte(capturedAt.UTC().Format(time.RFC3339))...)
	msg = append(msg, []byte(contractID)...)
	return msg
}

// randomKeySuffix returns an unguessable 16-byte hex string for the S3 object key. It MUST NOT
// be derived from any client-supplied value (e.g. captured_at): a predictable/repeatable suffix
// lets a device request two slots that resolve to the SAME S3 object, so the second ciphertext
// PUT overwrites the first — and the first row's stored DEK+nonce no longer match the blob,
// silently destroying that encrypted screenshot's evidence forever.
func randomKeySuffix() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
