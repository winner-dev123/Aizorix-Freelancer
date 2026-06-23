//! Synchronization engine. Runs forever in the background draining two local queues:
//!   1. pending_screenshots — through the upload state machine:
//!        queued → (RequestUploadSlot) slotted → (PUT to S3) uploaded → (ConfirmUpload) confirmed
//!   2. pending_slices — POSTed to the time-tracking service (idempotent server-side).
//!
//! Every step is resumable: a crash or network loss leaves rows in their last state, and the
//! next tick picks up where it left off. Failures back off exponentially up to MAX_UPLOAD_RETRIES.

use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use serde::{Deserialize, Serialize};

use crate::config::{MAX_UPLOAD_RETRIES, SYNC_INTERVAL};
use crate::db::PendingScreenshot;
use crate::state::AppState;

pub fn spawn(state: Arc<AppState>) {
    tokio::spawn(async move {
        loop {
            if let Err(e) = drain_once(&state).await {
                tracing::warn!("sync tick error: {e}");
            }
            tokio::time::sleep(SYNC_INTERVAL).await;
        }
    });
}

async fn drain_once(state: &Arc<AppState>) -> crate::error::Result<()> {
    let now = chrono::Utc::now().timestamp();
    let due: Vec<(PendingScreenshot, String)> = {
        let store = state.store.lock().await;
        store.due_screenshots(now, 20)?
    };
    for (ss, upload_state) in due {
        if let Err(e) = process_screenshot(state, &ss, &upload_state).await {
            tracing::warn!("screenshot {} failed: {e}", ss.id);
            backoff(state, &ss).await?;
        }
    }
    drain_slices(state).await?;
    Ok(())
}

#[derive(Serialize)]
struct SlotReq<'a> {
    contract_id: &'a str,
    session_id: &'a str,
    slice_id: &'a str,
    device_id: &'a str,
    captured_at: &'a str,
    // The DEK the blob was already encrypted with (offline-first). The server KMS-wraps THIS
    // key, so the stored wrapped DEK decrypts the ciphertext we are about to upload.
    client_dek: &'a str,
}
#[derive(Deserialize)]
struct SlotResp {
    screenshot_id: String,
    upload_url: String,
    wrapped_dek: String,
    required_headers: HashMap<String, String>,
    #[serde(default)]
    #[allow(dead_code)]
    s3_key: String,
}
#[derive(Serialize)]
struct ConfirmReq<'a> {
    screenshot_id: &'a str,
    contract_id: &'a str,
    sha256_cipher: &'a str,
    gcm_nonce: &'a str,
    device_signature: &'a str,
    device_pubkey: &'a str,
    captured_at: &'a str,
    width: i64,
    height: i64,
    size_bytes: i64,
    format: &'a str,
    phash: &'a str,
    activity_pct: i64,
}
#[derive(Deserialize)]
struct ConfirmResp {
    #[allow(dead_code)]
    accepted: bool,
}

async fn process_screenshot(
    state: &Arc<AppState>,
    ss: &PendingScreenshot,
    upload_state: &str,
) -> crate::error::Result<()> {
    let access = state.access_token().await?;
    // The enrolled device id (persisted at login). Bound here so the &str borrow in SlotReq
    // outlives the request build below.
    let device_id = device_id(state).await;
    // Unwrap the at-rest-sealed DEK (it's stored wrapped under the keychain key); the server then
    // KMS-wraps this plaintext value. Bound here so the &str borrow in SlotReq outlives the build.
    let client_dek = crate::crypto::unseal_dek_b64(&ss.client_dek_b64)?;

    // Step 1: obtain a presigned slot if we don't have one.
    let (server_id, upload_url, headers) = if upload_state == "queued" {
        let resp: SlotResp = state
            .api
            .post_json(
                &access,
                "/v1/screenshots/upload-slot",
                &SlotReq {
                    contract_id: &ss.contract_id,
                    session_id: &ss.session_id,
                    slice_id: &ss.slice_id,
                    device_id: &device_id,
                    captured_at: &ss.captured_at,
                    client_dek: &client_dek,
                },
            )
            .await?;
        {
            let store = state.store.lock().await;
            store.set_slot(
                &ss.id,
                &resp.screenshot_id,
                &resp.upload_url,
                &resp.wrapped_dek,
            )?;
        }
        (resp.screenshot_id, resp.upload_url, resp.required_headers)
    } else {
        // Slot already exists. Re-requesting is now safe + idempotent: the server keys on slice_id
        // and returns the SAME screenshot_id and its existing s3_key (with a fresh presigned URL),
        // so a retry whose first PUT/confirm failed (or whose 5-min presign expired) no longer
        // mints a duplicate screenshot row / billing event.
        let resp: SlotResp = state
            .api
            .post_json(
                &access,
                "/v1/screenshots/upload-slot",
                &SlotReq {
                    contract_id: &ss.contract_id,
                    session_id: &ss.session_id,
                    slice_id: &ss.slice_id,
                    device_id: &device_id,
                    captured_at: &ss.captured_at,
                    client_dek: &client_dek,
                },
            )
            .await?;
        (resp.screenshot_id, resp.upload_url, resp.required_headers)
    };

    // Step 2: PUT the original ciphertext (encrypted on-device with client_dek, which the
    // server just KMS-wrapped) to S3 — byte-for-byte unchanged, so the stored wrapped DEK and
    // recorded sha256/signature stay valid.
    let ciphertext = tokio::fs::read(&ss.blob_path)
        .await
        .map_err(|e| crate::error::AppError::Storage(e.to_string()))?;
    state
        .api
        .put_blob(&upload_url, &headers, ciphertext)
        .await?;
    {
        let store = state.store.lock().await;
        store.set_state(&ss.id, "uploaded")?;
    }

    // Step 3: confirm with integrity material (server re-derives + verifies the signature).
    let pubkey = state.device.public_key_b64();
    let _: ConfirmResp = state
        .api
        .post_json(
            &access,
            "/v1/screenshots/confirm",
            &ConfirmReq {
                screenshot_id: &server_id,
                contract_id: &ss.contract_id,
                sha256_cipher: &ss.sha256_b64,
                gcm_nonce: &ss.nonce_b64,
                device_signature: &ss.signature_b64,
                device_pubkey: &pubkey,
                captured_at: &ss.captured_at,
                width: ss.width,
                height: ss.height,
                size_bytes: ss.size_bytes,
                format: "webp",
                phash: &ss.phash_b64,
                activity_pct: ss.activity_pct,
            },
        )
        .await?;

    // Done: mark confirmed and delete the on-disk ciphertext.
    {
        let store = state.store.lock().await;
        store.set_state(&ss.id, "confirmed")?;
        store.delete_screenshot(&ss.id)?;
    }
    let _ = std::fs::remove_file(&ss.blob_path);
    Ok(())
}

async fn drain_slices(state: &Arc<AppState>) -> crate::error::Result<()> {
    let access = state.access_token().await?;
    let slices = { state.store.lock().await.unsynced_slices(50)? };
    for s in slices {
        // payload_json is the per-session SubmitSlices body; the endpoint is idempotent.
        let path = format!("/v1/tracking/sessions/{}/slices", s.session_id);
        let body: serde_json::Value =
            serde_json::from_str(&s.payload_json).unwrap_or(serde_json::json!({}));
        match state
            .api
            .post_json::<_, serde_json::Value>(&access, &path, &body)
            .await
        {
            Ok(_) => {
                state.store.lock().await.mark_slice_synced(&s.id)?;
            }
            Err(e) => {
                tracing::warn!("slice {} sync failed: {e}", s.id);
            }
        }
    }
    Ok(())
}

async fn backoff(state: &Arc<AppState>, ss: &PendingScreenshot) -> crate::error::Result<()> {
    if ss.retries as u32 >= MAX_UPLOAD_RETRIES {
        tracing::error!(
            "screenshot {} exceeded retries; leaving for manual sync",
            ss.id
        );
        return Ok(());
    }
    let delay = Duration::from_secs(2u64.saturating_pow(ss.retries.min(8) as u32) * 30);
    let next = chrono::Utc::now().timestamp() + delay.as_secs() as i64;
    state.store.lock().await.backoff_screenshot(&ss.id, next)
}

/// The device id assigned by the backend at enrollment (login) and persisted in the local kv
/// store under "device_id". The screenshot service uses it to resolve the enrolled attestation
/// key for signature verification, so a missing id means uploads cannot be confirmed.
async fn device_id(state: &Arc<AppState>) -> String {
    state
        .store
        .lock()
        .await
        .load_kv("device_id")
        .ok()
        .flatten()
        .unwrap_or_default()
}
