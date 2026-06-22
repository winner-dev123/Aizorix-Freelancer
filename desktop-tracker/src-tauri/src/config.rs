//! Static configuration + tunables. The capture interval is requested from the server on
//! session start so the platform controls cadence; these are fallbacks/bounds.

use std::time::Duration;

pub const API_BASE: &str = "https://api.aizorix.com";

/// Default capture interval (15 minutes) — overridden by the server's `capture_interval_seconds`.
pub const DEFAULT_CAPTURE_INTERVAL: Duration = Duration::from_secs(15 * 60);

/// Jitter added to each capture so quarter-hour boundaries don't stampede the backend.
pub const CAPTURE_JITTER: Duration = Duration::from_secs(45);

/// A "slice" is the billing/screenshot unit; activity samples are aggregated into it.
pub const SLICE_SECONDS: u64 = 600; // 10 min
pub const SAMPLE_SECONDS: u64 = 60; // input counters flushed each minute

/// Idle: no input for this long marks the user idle.
pub const IDLE_THRESHOLD: Duration = Duration::from_secs(5 * 60);

/// WebP quality (0-100). Lower = smaller upload; 60 keeps text legible for audits.
pub const WEBP_QUALITY: f32 = 60.0;

/// Sync engine: how often the offline queue is drained, and max retries with backoff.
pub const SYNC_INTERVAL: Duration = Duration::from_secs(30);
pub const MAX_UPLOAD_RETRIES: u32 = 8;

/// Keychain service name for storing the device signing key + refresh token.
pub const KEYRING_SERVICE: &str = "com.aizorix.tracker";
