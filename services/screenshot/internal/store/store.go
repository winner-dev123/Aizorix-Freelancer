// Package store persists screenshot control-plane rows (blobs live in S3).
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("store: not found")

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store  { return &Store{pool: pool} }
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

type NewSlot struct {
	ContractID, SessionID, SliceID, FreelancerID, DeviceID string
	CapturedAt                                             time.Time
	Bucket, Key                                            string
	WrappedDEK                                             []byte
	KMSKeyID                                               string
}

// CreateSlot inserts the pending-upload row and its metadata shell in one tx. It returns the
// screenshot id AND its s3_key, so the caller presigns for the object the row actually points to.
func (s *Store) CreateSlot(ctx context.Context, n NewSlot) (id string, key string, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback(ctx)

	// Idempotency: a retried upload-slot for the SAME slice returns the EXISTING screenshot AND
	// its already-stored s3_key (so the fresh presigned URL targets the right object), instead of
	// minting a duplicate row / a new key the row wouldn't point to -> orphaned blobs + a double
	// screenshot.ingested (double billing). The slice_id unique index (migration 000014) guards
	// the rare concurrent race.
	if n.SliceID != "" {
		err = tx.QueryRow(ctx, `SELECT id, s3_key FROM screenshots WHERE slice_id = $1::uuid LIMIT 1`, n.SliceID).Scan(&id, &key)
		if err == nil {
			return id, key, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", "", err
		}
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO screenshots
		  (contract_id, session_id, slice_id, freelancer_id, captured_at, s3_bucket, s3_key, status, retain_until)
		VALUES ($1,$2,NULLIF($3,'')::uuid,$4,$5::timestamptz,$6,$7,'pending_upload', $5::timestamptz + interval '90 days')
		RETURNING id`,
		n.ContractID, n.SessionID, n.SliceID, n.FreelancerID, n.CapturedAt, n.Bucket, n.Key).Scan(&id)
	if err != nil {
		return "", "", err
	}
	// Metadata shell — wrapped DEK is stored now; integrity fields filled on confirm.
	_, err = tx.Exec(ctx, `
		INSERT INTO screenshot_metadata (screenshot_id, captured_at, sha256_cipher, wrapped_dek, gcm_nonce, kms_key_id, device_id)
		VALUES ($1,$2, ''::bytea, $3, ''::bytea, $4, NULLIF($5,'')::uuid)`,
		id, n.CapturedAt, n.WrappedDEK, n.KMSKeyID, n.DeviceID)
	if err != nil {
		return "", "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", "", err
	}
	return id, n.Key, nil
}

type Confirm struct {
	ScreenshotID    string
	SHA256Cipher    []byte
	GCMNonce        []byte
	DeviceSignature []byte
	Width, Height   int
	SizeBytes       int64
	Format          string
	PHash           []byte
	ActivityPct     int
}

// ConfirmUpload marks the screenshot stored and records integrity material; an event is
// enqueued via the outbox in the same tx for the fraud/timetracking consumers.
func (s *Store) ConfirmUpload(ctx context.Context, tx pgx.Tx, c Confirm) (contractID, sessionID, sliceID string, err error) {
	err = tx.QueryRow(ctx, `
		UPDATE screenshots SET status='stored'
		WHERE id=$1 AND status='pending_upload'
		RETURNING contract_id, session_id, coalesce(slice_id::text,'')`, c.ScreenshotID).
		Scan(&contractID, &sessionID, &sliceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", ErrNotFound
	}
	if err != nil {
		return
	}
	_, err = tx.Exec(ctx, `
		UPDATE screenshot_metadata
		SET sha256_cipher=$2, gcm_nonce=$3, device_signature=$4, width=$5, height=$6,
		    size_bytes=$7, format=$8, phash=$9
		WHERE screenshot_id=$1`,
		c.ScreenshotID, c.SHA256Cipher, c.GCMNonce, c.DeviceSignature, c.Width, c.Height,
		c.SizeBytes, c.Format, c.PHash)
	return
}

type ViewMaterial struct {
	Bucket, Key string
	WrappedDEK  []byte
	GCMNonce    []byte
	CapturedAt  time.Time
	Status      string
	Flagged     bool
	ContractID  string
}

func (s *Store) GetForView(ctx context.Context, screenshotID string) (ViewMaterial, error) {
	var v ViewMaterial
	err := s.pool.QueryRow(ctx, `
		SELECT sc.s3_bucket, sc.s3_key, m.wrapped_dek, m.gcm_nonce, sc.captured_at,
		       sc.status, (m.is_duplicate OR m.is_tampered), sc.contract_id
		FROM screenshots sc JOIN screenshot_metadata m ON m.screenshot_id = sc.id
		WHERE sc.id = $1`, screenshotID).
		Scan(&v.Bucket, &v.Key, &v.WrappedDEK, &v.GCMNonce, &v.CapturedAt, &v.Status, &v.Flagged, &v.ContractID)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// IsContractParty enforces that only the client/freelancer on the contract (or an admin,
// checked at the transport layer via permissions) may view a screenshot.
func (s *Store) IsContractParty(ctx context.Context, contractID, userID string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM contracts WHERE id=$1 AND (client_id=$2 OR freelancer_id=$2))`,
		contractID, userID).Scan(&ok)
	return ok, err
}

// RecordView bumps the view counter (full who/when goes to audit_logs at the transport layer).
func (s *Store) RecordView(ctx context.Context, screenshotID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE screenshot_metadata SET view_count = view_count + 1 WHERE screenshot_id=$1`, screenshotID)
	return err
}

// ConfirmContext returns the values recorded at slot-creation time: the SERVER's trusted
// contract_id, captured_at, and device_id. The signature is verified against THESE — never
// against client-supplied values in the confirm request.
func (s *Store) ConfirmContext(ctx context.Context, screenshotID string) (contractID string, capturedAt time.Time, deviceID string, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT sc.contract_id, sc.captured_at, coalesce(m.device_id::text,'')
		FROM screenshots sc JOIN screenshot_metadata m ON m.screenshot_id = sc.id
		WHERE sc.id = $1`, screenshotID).Scan(&contractID, &capturedAt, &deviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, "", ErrNotFound
	}
	return
}

// DeviceAttestationKey resolves the device's ENROLLED Ed25519 public key (registered at
// login). This is the trust anchor: the confirm-time signature must verify against this key,
// not a key supplied in the request. In production this is a gRPC call to the user/auth
// service; here the devices table lives in the shared dev schema.
func (s *Store) DeviceAttestationKey(ctx context.Context, deviceID string) ([]byte, error) {
	if deviceID == "" {
		return nil, ErrNotFound
	}
	var key []byte
	err := s.pool.QueryRow(ctx, `SELECT attestation_pubkey FROM devices WHERE id = $1`, deviceID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return key, err
}

type Summary struct {
	ID         string
	CapturedAt time.Time
	Status     string
	Flagged    bool
}

// ListByContract returns stored screenshot summaries for a contract (newest first).
func (s *Store) ListByContract(ctx context.Context, contractID string, limit int) ([]Summary, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT sc.id, sc.captured_at, sc.status, (m.is_duplicate OR m.is_tampered)
		FROM screenshots sc JOIN screenshot_metadata m ON m.screenshot_id = sc.id
		WHERE sc.contract_id = $1 AND sc.status = 'stored'
		ORDER BY sc.captured_at DESC
		LIMIT $2`, contractID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		var s Summary
		if err := rows.Scan(&s.ID, &s.CapturedAt, &s.Status, &s.Flagged); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}
