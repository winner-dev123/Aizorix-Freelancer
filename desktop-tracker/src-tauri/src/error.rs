//! Unified error type for the tracker. Tauri commands return `Result<T, AppError>` and the
//! error serializes to a stable shape the UI can branch on.

use serde::Serialize;

#[derive(Debug, thiserror::Error)]
pub enum AppError {
    #[error("network error: {0}")]
    Network(String),
    #[error("authentication required")]
    Unauthenticated,
    #[error("crypto error: {0}")]
    Crypto(String),
    #[error("capture error: {0}")]
    Capture(String),
    #[error("storage error: {0}")]
    Storage(String),
    #[error("not tracking")]
    NotTracking,
    #[error("already tracking contract {0}")]
    AlreadyTracking(String),
    #[error("internal: {0}")]
    Internal(String),
}

// Serialize as { code, message } so the frontend can map codes to UX.
impl Serialize for AppError {
    // NB: fully-qualified std Result — the crate's `Result<T>` alias (below) is single-arg and
    // would shadow this to a compile error.
    fn serialize<S: serde::Serializer>(&self, s: S) -> std::result::Result<S::Ok, S::Error> {
        use serde::ser::SerializeStruct;
        let code = match self {
            AppError::Network(_) => "NETWORK",
            AppError::Unauthenticated => "UNAUTHENTICATED",
            AppError::Crypto(_) => "CRYPTO",
            AppError::Capture(_) => "CAPTURE",
            AppError::Storage(_) => "STORAGE",
            AppError::NotTracking => "NOT_TRACKING",
            AppError::AlreadyTracking(_) => "ALREADY_TRACKING",
            AppError::Internal(_) => "INTERNAL",
        };
        let mut st = s.serialize_struct("AppError", 2)?;
        st.serialize_field("code", code)?;
        st.serialize_field("message", &self.to_string())?;
        st.end()
    }
}

impl From<rusqlite::Error> for AppError {
    fn from(e: rusqlite::Error) -> Self {
        AppError::Storage(e.to_string())
    }
}
impl From<reqwest::Error> for AppError {
    fn from(e: reqwest::Error) -> Self {
        AppError::Network(e.to_string())
    }
}
impl From<anyhow::Error> for AppError {
    fn from(e: anyhow::Error) -> Self {
        AppError::Internal(e.to_string())
    }
}

pub type Result<T> = std::result::Result<T, AppError>;
