//! Shared, thread-safe application state held by Tauri and the background workers.

use std::sync::Arc;
use tokio::sync::Mutex;

use crate::activity::ActivityEngine;
use crate::api::ApiClient;
use crate::crypto::DeviceKey;
use crate::db::LocalStore;

/// What we're currently tracking. `None` => stopped.
#[derive(Clone, Debug, Default)]
pub struct TrackingSession {
    pub contract_id: String,
    pub session_id: String,
    pub device_id: String,
    pub billing_week: String,
    pub capture_interval_secs: u64,
}

pub struct Auth {
    pub access_token: Option<String>,
    pub user_id: Option<String>,
}

pub struct AppState {
    pub api: ApiClient,
    pub store: Arc<Mutex<LocalStore>>,
    pub device: Arc<DeviceKey>,
    pub activity: ActivityEngine,
    pub auth: Mutex<Auth>,
    pub session: Mutex<Option<TrackingSession>>,
}

impl AppState {
    pub fn new(store: LocalStore, device: DeviceKey) -> Self {
        let activity = ActivityEngine::new();
        activity.start_listener();
        Self {
            api: ApiClient::new(),
            store: Arc::new(Mutex::new(store)),
            device: Arc::new(device),
            activity,
            auth: Mutex::new(Auth {
                access_token: None,
                user_id: None,
            }),
            session: Mutex::new(None),
        }
    }

    /// Returns a usable access token, refreshing if absent/expired.
    pub async fn access_token(&self) -> crate::error::Result<String> {
        {
            let auth = self.auth.lock().await;
            if let Some(tok) = &auth.access_token {
                return Ok(tok.clone());
            }
        }
        let toks = self.api.refresh().await?;
        let mut auth = self.auth.lock().await;
        auth.access_token = Some(toks.access_token.clone());
        Ok(toks.access_token)
    }

    pub async fn invalidate_access(&self) {
        self.auth.lock().await.access_token = None;
    }
}
