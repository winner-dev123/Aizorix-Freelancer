//! Tauri commands — the bridge the UI (webview) calls. Each returns Result<_, AppError> which
//! serializes to { code, message } for the frontend.

use std::sync::Arc;

use serde::{Deserialize, Serialize};
use tauri::State;

use crate::error::{AppError, Result};
use crate::state::{AppState, TrackingSession};
use crate::scheduler;

#[derive(Serialize)]
pub struct LoginResult {
    pub user_id: String,
    pub device_pubkey: String,
    /// The server-assigned device id, enrolled during login. The UI passes this back to
    /// `start_tracking`; the sync engine also loads it from the local kv store.
    pub device_id: String,
}

#[tauri::command]
pub async fn login(state: State<'_, Arc<AppState>>, email: String, password: String) -> Result<LoginResult> {
    let fingerprint = device_fingerprint();
    let tokens = state.api.login(&email, &password, &fingerprint).await?;
    let access = tokens.access_token.clone();
    {
        let mut auth = state.auth.lock().await;
        auth.access_token = Some(tokens.access_token);
        auth.user_id = Some(tokens.user_id.clone());
    }

    // Enroll this device's Ed25519 attestation key so the screenshot service can verify
    // upload signatures against it. This closes the security gap: without an enrolled key
    // the backend cannot confirm uploads. The endpoint is an idempotent upsert, so re-running
    // it on every login is safe. We persist the returned device_id locally for the sync engine.
    let pubkey = state.device.public_key_b64();
    let device_id = state.api.enroll_device(&access, &fingerprint, &pubkey).await?;
    {
        let store = state.store.lock().await;
        store.save_kv("device_id", &device_id)?;
    }

    Ok(LoginResult {
        user_id: tokens.user_id,
        device_pubkey: pubkey,
        device_id,
    })
}

#[derive(Serialize, Deserialize)]
pub struct StartResp {
    session_id: String,
    billing_week: String,
    capture_interval_seconds: u64,
}
#[derive(Serialize)]
struct StartReq<'a> { contract_id: &'a str, device_id: &'a str, timezone: &'a str }

#[tauri::command]
pub async fn start_tracking(state: State<'_, Arc<AppState>>, contract_id: String, device_id: String) -> Result<()> {
    {
        if state.session.lock().await.is_some() {
            return Err(AppError::AlreadyTracking(contract_id));
        }
    }
    // Always track against the enrolled device id. Prefer the one the UI passed (from login),
    // but fall back to the id persisted at enrollment so a restarted UI still resolves it.
    let device_id = if device_id.is_empty() {
        state.store.lock().await.load_kv("device_id")?.unwrap_or_default()
    } else {
        device_id
    };
    if device_id.is_empty() {
        return Err(AppError::Unauthenticated);
    }
    let access = state.access_token().await?;
    let tz = iana_timezone();
    let resp: StartResp = state.api.post_json(&access, "/v1/tracking/sessions", &StartReq {
        contract_id: &contract_id, device_id: &device_id, timezone: &tz,
    }).await?;

    let session = TrackingSession {
        contract_id,
        session_id: resp.session_id,
        device_id,
        billing_week: resp.billing_week,
        capture_interval_secs: resp.capture_interval_seconds,
    };
    {
        *state.session.lock().await = Some(session.clone());
        let store = state.store.lock().await;
        store.save_kv("active_session", &serde_json::to_string(&active_kv(&session)).unwrap())?;
    }

    // Launch the per-session capture scheduler (it self-terminates when the session ends).
    // The sync engine is NOT spawned here: it is started ONCE for the app lifetime in main.rs,
    // so starting/stopping tracking repeatedly can't leak additional sync loops.
    scheduler::spawn(Arc::clone(&state));
    Ok(())
}

#[derive(Serialize, Deserialize)]
pub struct StopResp { active_seconds: i64, idle_seconds: i64, avg_activity_pct: i64 }
#[derive(Serialize)]
struct StopReq<'a> { memo: &'a str }

#[tauri::command]
pub async fn stop_tracking(state: State<'_, Arc<AppState>>, memo: Option<String>) -> Result<StopResp> {
    let session = { state.session.lock().await.clone() }.ok_or(AppError::NotTracking)?;
    let access = state.access_token().await?;
    let path = format!("/v1/tracking/sessions/{}/stop", session.session_id);
    let resp: StopResp = state.api.post_json(&access, &path, &StopReq { memo: memo.as_deref().unwrap_or("") }).await?;

    *state.session.lock().await = None;
    state.store.lock().await.delete_kv("active_session")?;
    // The sync engine keeps running until the queue drains, then idles.
    Ok(resp)
}

#[derive(Serialize)]
pub struct Status {
    tracking: bool,
    contract_id: Option<String>,
    billing_week: Option<String>,
    pending_uploads: i64,
}

#[tauri::command]
pub async fn tracking_status(state: State<'_, Arc<AppState>>) -> Result<Status> {
    let session = state.session.lock().await.clone();
    let pending = {
        let store = state.store.lock().await;
        // Count ALL outstanding uploads (including those in backoff), not just those due now.
        store.pending_count().unwrap_or(0)
    };
    Ok(Status {
        tracking: session.is_some(),
        contract_id: session.as_ref().map(|s| s.contract_id.clone()),
        billing_week: session.as_ref().map(|s| s.billing_week.clone()),
        pending_uploads: pending,
    })
}

fn active_kv(s: &TrackingSession) -> serde_json::Value {
    serde_json::json!({
        "contract_id": s.contract_id, "session_id": s.session_id,
        "billing_week": s.billing_week, "capture_interval_secs": s.capture_interval_secs,
    })
}

/// Stable-ish per-install fingerprint (hostname + OS). Real builds add a persisted random salt.
fn device_fingerprint() -> String {
    use sha2::{Digest, Sha256};
    let mut h = Sha256::new();
    h.update(std::env::var("COMPUTERNAME").or_else(|_| std::env::var("HOSTNAME")).unwrap_or_default());
    h.update(std::env::consts::OS);
    base64::Engine::encode(&base64::engine::general_purpose::STANDARD, h.finalize())
}

fn iana_timezone() -> String {
    std::env::var("TZ").unwrap_or_else(|_| "UTC".into())
}
