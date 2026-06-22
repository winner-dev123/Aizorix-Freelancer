//! Thin HTTP client to the Aizorix backend. Carries the access token; on 401 it refreshes
//! using the rotating refresh token (stored in the OS keychain) and retries once.

use serde::{Deserialize, Serialize};

use crate::config::{API_BASE, KEYRING_SERVICE};
use crate::error::{AppError, Result};

#[derive(Clone)]
pub struct ApiClient {
    http: reqwest::Client,
    base: String,
}

#[derive(Debug, Deserialize)]
pub struct Tokens {
    pub access_token: String,
    pub refresh_token: String,
    #[allow(dead_code)]
    pub user_id: String,
}

#[derive(Serialize)]
struct LoginReq<'a> { email: &'a str, password: &'a str, device_fingerprint: &'a str }

#[derive(Serialize)]
struct RefreshReq<'a> { refresh_token: &'a str }

#[derive(Serialize)]
struct EnrollReq<'a> { fingerprint: &'a str, display_name: &'a str, attestation_pubkey: &'a str }

#[derive(Deserialize)]
struct EnrollResp { device_id: String }

impl ApiClient {
    pub fn new() -> Self {
        let http = reqwest::Client::builder()
            .user_agent(concat!("aizorix-tracker/", env!("CARGO_PKG_VERSION")))
            .timeout(std::time::Duration::from_secs(30))
            .build()
            .expect("http client");
        Self { http, base: API_BASE.to_string() }
    }

    pub async fn login(&self, email: &str, password: &str, fingerprint: &str) -> Result<Tokens> {
        let resp = self.http.post(format!("{}/v1/auth/login", self.base))
            .json(&LoginReq { email, password, device_fingerprint: fingerprint })
            .send().await?;
        if !resp.status().is_success() {
            return Err(AppError::Unauthenticated);
        }
        let tokens: Tokens = resp.json().await?;
        store_refresh(&tokens.refresh_token)?;
        Ok(tokens)
    }

    pub async fn refresh(&self) -> Result<Tokens> {
        let rt = load_refresh()?.ok_or(AppError::Unauthenticated)?;
        let resp = self.http.post(format!("{}/v1/auth/refresh", self.base))
            .json(&RefreshReq { refresh_token: &rt }).send().await?;
        if !resp.status().is_success() {
            return Err(AppError::Unauthenticated);
        }
        let tokens: Tokens = resp.json().await?;
        store_refresh(&tokens.refresh_token)?;
        Ok(tokens)
    }

    /// Enroll this device with the backend, registering its Ed25519 attestation public key
    /// (`pubkey_b64`) under the user's account. Returns the server-assigned `device_id`, which
    /// the tracker persists and sends on every upload-slot / confirm request so the screenshot
    /// service can verify signatures against the enrolled key. Idempotent server-side (upsert by
    /// user_id + fingerprint), so it is safe to call on every login.
    pub async fn enroll_device(&self, access: &str, fingerprint: &str, pubkey_b64: &str) -> Result<String> {
        let resp: EnrollResp = self.post_json(access, "/v1/users/me/devices", &EnrollReq {
            fingerprint,
            display_name: "Aizorix Desktop Tracker",
            attestation_pubkey: pubkey_b64,
        }).await?;
        Ok(resp.device_id)
    }

    /// POST JSON with bearer auth, returning the parsed response.
    pub async fn post_json<TReq: Serialize, TResp: for<'de> Deserialize<'de>>(
        &self, access: &str, path: &str, body: &TReq,
    ) -> Result<TResp> {
        let resp = self.http.post(format!("{}{}", self.base, path))
            .bearer_auth(access).json(body).send().await?;
        if !resp.status().is_success() {
            return Err(AppError::Network(format!("{} -> {}", path, resp.status())));
        }
        Ok(resp.json().await?)
    }

    /// Raw PUT of ciphertext to a presigned S3 URL (no auth header — the URL is the auth).
    pub async fn put_blob(&self, url: &str, headers: &std::collections::HashMap<String, String>, body: Vec<u8>) -> Result<()> {
        let mut req = self.http.put(url).body(body);
        for (k, v) in headers {
            req = req.header(k, v);
        }
        let resp = req.send().await?;
        if !resp.status().is_success() {
            return Err(AppError::Network(format!("s3 put -> {}", resp.status())));
        }
        Ok(())
    }
}

fn store_refresh(token: &str) -> Result<()> {
    keyring::Entry::new(KEYRING_SERVICE, "refresh_token")
        .and_then(|e| e.set_password(token))
        .map_err(|e| AppError::Storage(e.to_string()))
}
fn load_refresh() -> Result<Option<String>> {
    match keyring::Entry::new(KEYRING_SERVICE, "refresh_token").and_then(|e| e.get_password()) {
        Ok(t) => Ok(Some(t)),
        Err(keyring::Error::NoEntry) => Ok(None),
        Err(e) => Err(AppError::Storage(e.to_string())),
    }
}
