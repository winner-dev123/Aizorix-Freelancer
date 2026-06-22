# Screenshot Pipeline (Phases 8 & 9)

End-to-end design for capturing, encrypting, uploading, verifying, viewing, and retiring
screenshots — the technical core of verified hourly work.

## 1. Capture → encrypt (on device)

```
 capture all monitors ─► pick largest ─► downscale (long edge ≤1920) ─► WebP encode ─► pHash
                                                                            │
   generate random 32-byte DEK ──► AES-256-GCM( webp, aad = contract_id|captured_at ) ─► ciphertext
                                                                            │
   sha256(ciphertext) ──► Ed25519 device-sign( sha256 || captured_at || contract_id )
                                                                            │
   write ciphertext to local SQLite-referenced file + enqueue (offline-safe)
```

- **Encrypt-on-capture** means plaintext pixels never persist, even offline. WebP at quality ~60
  with downscaling keeps captures ~100–300 KB while remaining legible for audits.
- **AAD binds context:** the ciphertext can only be decrypted with the same `contract_id|captured_at`,
  preventing replay under another identity.
- **Perceptual hash** (8×8 gradient pHash) is computed pre-compression for the fraud service's
  duplicate/static-screen detection.

## 2. Upload (device ↔ S3 direct, control plane = screenshot service)

```
 device ── RequestUploadSlot(contract,session,slice,captured_at) ─► screenshot-svc
        ◄── { screenshot_id, presigned PUT url, wrapped_dek (KMS), headers } ──
 device ── PUT ciphertext ───────────────────────────────────────► S3 (SSE-KMS, versioned, private)
 device ── ConfirmUpload(sha256, nonce, device_sig, pubkey, dims, phash, activity) ─► screenshot-svc
        ◄── { accepted } ──   (writes screenshot_metadata, outbox: screenshot.ingested)
```

- The multi-MB blob travels **device → S3 directly**; the service handles only small metadata.
- The DEK is **KMS-wrapped server-side**; only the wrapped form is persisted in
  `screenshot_metadata.wrapped_dek`. (Online mode: server issues the DEK. Offline mode: device
  generates the DEK, which the server wraps at sync time — see `desktop-tracker`.)
- **Idempotent:** `ConfirmUpload` only transitions `pending_upload → stored` once; retries no-op.

## 3. Integrity & anti-tampering

| Check | Where | Catches |
|-------|-------|---------|
| Device Ed25519 signature over metadata | `ConfirmUpload` (sync) | altered metadata / non-enrolled device |
| Server re-hash of the S3 object vs `sha256_cipher` | async job | corrupted/swapped blob, `integrity_failed` |
| Perceptual-hash dedupe across a session | `fraud` (stream) | static screen / looping the same image |
| AAD mismatch on decrypt | any read | blob replayed under a different contract |
| GCM auth tag | any decrypt | any bit-level tampering of ciphertext |

A failed integrity re-hash sets `screenshots.status = 'integrity_failed'` and emits
`screenshot.integrity_failed` → `admin` + `fraud`.

## 4. Authorized, audited viewing

```
 viewer ── GET /v1/screenshots/{id} ─► screenshot-svc
        authz: contract party OR permission screenshot:audit
        write audit_logs(actor, action=screenshot.view, contract)
        ◄── { presigned GET (2-min TTL), wrapped_dek, gcm_nonce } ──
 viewer ── GET ciphertext from S3 ─► decrypt locally (unwrap dek via KMS, AES-GCM open)
```

- Only contract parties (client/freelancer) and admins with `screenshot:audit` can view.
- **Every view is audited** (who, when, which screenshot/contract) — essential for monitoring
  trust and dispute investigations.
- Thumbnails for the review grid are server-decrypted, re-encrypted for transit, and served via
  short-TTL signed URLs so the grid is fast without exposing the bucket.

## 5. Storage & retention

- Bucket: private, **SSE-KMS**, **versioned**, **Object Lock** (for legal holds), CRR to the DR
  region. Keys partitioned `contracts/{id}/{yyyy}/{mm}/{dd}/...` for lifecycle + locality.
- Lifecycle: Standard → IA (30d) → Glacier IR (60d) → shred at `retain_until` (default 90d)
  unless `legal_hold`.
- **Crypto-shred** on erasure: destroy the wrapped DEK; the blob becomes unrecoverable without
  bulk delete (which the lifecycle then performs).

## 6. Failure modes & guarantees

- **Offline capture:** everything is queued locally first; the sync engine is resumable and
  idempotent, so no work is lost or double-billed across crashes/disconnects.
- **Partial upload:** S3 PUT either fully succeeds (then `ConfirmUpload`) or is retried; an
  unconfirmed slot is garbage-collected by a sweep job.
- **Cost guard:** idle slices skip the blob; identical perceptual hashes are flagged (not billed
  as fresh work) by `fraud`.
