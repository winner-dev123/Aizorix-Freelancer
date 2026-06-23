//! Capture scheduler. While a tracking session is active it runs two cadences:
//!   * every SAMPLE_SECONDS  → snapshot the activity counters into the current slice
//!   * every capture_interval → take a screenshot, encrypt it offline-first, and flush the
//!                              accumulated samples as one billable slice (both queued locally)
//!
//! Nothing here talks to the network directly — it only writes to the local store. The sync
//! engine (sync.rs) uploads later, so capture continues uninterrupted while offline.

use std::sync::Arc;
use std::time::Duration;

use rand::RngCore;
use serde_json::json;

use crate::activity::{ActivityEngine, Sample};
use crate::config::{CAPTURE_JITTER, IDLE_THRESHOLD, SAMPLE_SECONDS};
use crate::db::{PendingScreenshot, PendingSlice};
use crate::state::{AppState, TrackingSession};
use crate::{crypto, screenshot};

pub fn spawn(state: Arc<AppState>) {
    tokio::spawn(async move {
        run(state).await;
    });
}

async fn run(state: Arc<AppState>) {
    let mut samples: Vec<Sample> = Vec::new();
    let mut slice_start = chrono::Utc::now();
    let mut elapsed_in_slice = Duration::ZERO;

    loop {
        // Stop cleanly when tracking ends.
        let sess = { state.session.lock().await.clone() };
        let Some(session) = sess else {
            return;
        };

        tokio::time::sleep(Duration::from_secs(SAMPLE_SECONDS)).await;

        // 1) Sample the activity counters for this minute.
        let sample = state.activity.take_sample();
        samples.push(sample);
        elapsed_in_slice += Duration::from_secs(SAMPLE_SECONDS);

        // 2) At the capture boundary, screenshot + flush the slice.
        let interval = Duration::from_secs(session.capture_interval_secs.max(SAMPLE_SECONDS));
        if elapsed_in_slice >= interval {
            let slice_end = chrono::Utc::now();
            if let Err(e) = capture_and_enqueue(
                &state,
                &session,
                &samples,
                slice_start,
                slice_end,
                &state.activity,
            )
            .await
            {
                tracing::error!("capture failed: {e}");
            }
            samples.clear();
            slice_start = slice_end;
            elapsed_in_slice = Duration::ZERO;

            // Small jitter so many trackers don't hit the backend at the same quarter-hour.
            let mut b = [0u8; 1];
            rand::rngs::OsRng.fill_bytes(&mut b);
            let jitter =
                Duration::from_millis((b[0] as u64) * CAPTURE_JITTER.as_millis() as u64 / 255);
            tokio::time::sleep(jitter).await;
        }
    }
}

async fn capture_and_enqueue(
    state: &Arc<AppState>,
    session: &TrackingSession,
    samples: &[Sample],
    slice_start: chrono::DateTime<chrono::Utc>,
    slice_end: chrono::DateTime<chrono::Utc>,
    activity: &ActivityEngine,
) -> crate::error::Result<()> {
    let captured_at = slice_end;
    // Canonical UTC RFC3339 with a 'Z' designator and NO fractional seconds, byte-for-byte
    // identical to Go's time.RFC3339 of a UTC instant. The backend re-derives the signed
    // message with that exact layout, so the Ed25519 signature must be produced over this same
    // string (and the same value is sent as the JSON captured_at).
    let captured_at_str = captured_at.to_rfc3339_opts(chrono::SecondsFormat::Secs, true);

    // Skip the screenshot blob entirely for a fully idle slice (cost + privacy); the slice is
    // still recorded with zero activity so the server/fraud sees the gap.
    let idle = activity.is_idle(IDLE_THRESHOLD)
        && samples
            .iter()
            .all(|s| s.keyboard_count == 0 && s.mouse_count == 0);

    let app = crate::apps::active_app();
    let local_id = uuid::Uuid::new_v4().to_string();
    let slice_id = uuid::Uuid::new_v4().to_string();

    // --- Screenshot path (skipped when idle) ---
    if !idle {
        // Capture + downscale + WebP encode + perceptual hash is heavy CPU work; run it on the
        // blocking pool so it never stalls the async runtime (sync loop, Tauri commands).
        let cap = tokio::task::spawn_blocking(screenshot::capture_primary)
            .await
            .map_err(|e| crate::error::AppError::Capture(format!("join: {e}")))??;

        // Offline-first: generate a local DEK, encrypt immediately so plaintext pixels never
        // touch disk. The server KMS-wraps this DEK when the slot is requested at sync time.
        let mut dek = vec![0u8; 32];
        rand::rngs::OsRng.fill_bytes(&mut dek);
        // Seal the DEK under the device-local keychain key before it touches disk (#15): the queued
        // row stores the WRAPPED dek, never plaintext, so a stolen SQLite file can't decrypt the
        // ciphertext. The sync path unwraps it transiently when requesting the upload slot.
        let sealed_dek = crypto::seal_dek(&dek)?;

        let aad = format!("{}|{}", session.contract_id, captured_at_str);
        let enc = crypto::encrypt_screenshot(dek, &cap.webp, aad.as_bytes())?;

        let sig = state.device.sign_metadata(
            &enc.sha256_cipher,
            &enc.nonce,
            &captured_at_str,
            &session.contract_id,
        );
        let b64 = |b: &[u8]| base64::Engine::encode(&base64::engine::general_purpose::STANDARD, b);

        // Persist ciphertext to disk; the queue row references it.
        let blob_path = {
            let store = state.store.lock().await;
            store.blob_dir().join(format!("{local_id}.enc"))
        };
        tokio::fs::write(&blob_path, &enc.ciphertext)
            .await
            .map_err(|e| crate::error::AppError::Storage(e.to_string()))?;

        let pending = PendingScreenshot {
            id: local_id.clone(),
            server_id: None,
            contract_id: session.contract_id.clone(),
            session_id: session.session_id.clone(),
            slice_id: slice_id.clone(),
            captured_at: captured_at_str.clone(),
            blob_path: blob_path.to_string_lossy().to_string(),
            nonce_b64: b64(&enc.nonce),
            sha256_b64: b64(&enc.sha256_cipher),
            signature_b64: b64(&sig),
            phash_b64: cap.phash_b64,
            width: cap.width as i64,
            height: cap.height as i64,
            size_bytes: enc.ciphertext.len() as i64,
            activity_pct: 0, // server computes the authoritative %; this is a placeholder
            retries: 0,
            // At-rest-wrapped DEK (#15): base64(nonce || AES-256-GCM(dek)) under the keychain key,
            // so the SQLite file alone yields neither the key nor a decryptable screenshot.
            client_dek_b64: sealed_dek,
        };
        state.store.lock().await.enqueue_screenshot(&pending)?;
    }

    // --- Slice path (always recorded) ---
    let slice_payload = json!({
        "idempotency_key": local_id,
        "slices": [{
            "contract_id": session.contract_id,
            "slice_start": slice_start.to_rfc3339(),
            "slice_end": slice_end.to_rfc3339(),
            "active_app": app.app_name,
            "active_app_title": app.window_title,
            "browser_url_host": app.browser_url_host,
            "screenshot_id": if idle { String::new() } else { slice_id.clone() },
            "samples": samples.iter().map(|s| json!({
                "at": s.at.to_rfc3339(),
                "keyboard_count": s.keyboard_count,
                "mouse_count": s.mouse_count,
                "mouse_distance": s.mouse_distance,
            })).collect::<Vec<_>>(),
        }]
    });
    state.store.lock().await.enqueue_slice(&PendingSlice {
        id: slice_id,
        session_id: session.session_id.clone(),
        contract_id: session.contract_id.clone(),
        payload_json: slice_payload.to_string(),
    })?;
    Ok(())
}
