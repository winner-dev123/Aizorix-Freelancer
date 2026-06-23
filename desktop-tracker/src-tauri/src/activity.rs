//! Activity engine (Phase 9, client side).
//!
//! Privacy-by-design: we listen to global input events ONLY to increment counters and measure
//! mouse travel. We never record which keys are pressed, clipboard, or window contents — there
//! is no keylogging. The freelancer is shown exactly what is collected (the monitoring
//! disclosure they accepted at registration).
//!
//! The engine runs a background listener thread updating atomics; `Sampler::take_sample()`
//! snapshots and resets the counters once per SAMPLE_SECONDS to build a per-sample record.

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use serde::Serialize;

#[derive(Default)]
struct Counters {
    keyboard: AtomicU64,
    mouse_clicks: AtomicU64,
    mouse_moves: AtomicU64,
    mouse_distance: AtomicU64, // accumulated pixel distance
    last_x: AtomicU64,
    last_y: AtomicU64,
    last_input_ms: AtomicU64, // monotonic-ish epoch ms of last input
}

#[derive(Clone)]
pub struct ActivityEngine {
    counters: Arc<Counters>,
    started: Instant,
}

#[derive(Clone, Debug, Serialize)]
pub struct Sample {
    pub at: chrono::DateTime<chrono::Utc>,
    pub keyboard_count: i64,
    pub mouse_count: i64,
    pub mouse_distance: i64,
}

impl ActivityEngine {
    pub fn new() -> Self {
        Self {
            counters: Arc::new(Counters::default()),
            started: Instant::now(),
        }
    }

    /// Spawn the global input listener. On platforms requiring accessibility permission
    /// (macOS), the OS will prompt; until granted, counters stay zero (handled gracefully).
    pub fn start_listener(&self) {
        let counters = self.counters.clone();
        std::thread::spawn(move || {
            // `rdev::listen` blocks; it is fine on a dedicated thread.
            #[cfg(not(feature = "mock-capture"))]
            {
                let _ = rdev::listen(move |event| handle_event(&counters, event));
            }
            #[cfg(feature = "mock-capture")]
            {
                // Deterministic fake input for CI.
                loop {
                    counters.keyboard.fetch_add(3, Ordering::Relaxed);
                    counters.mouse_moves.fetch_add(5, Ordering::Relaxed);
                    counters.mouse_distance.fetch_add(120, Ordering::Relaxed);
                    touch(&counters);
                    std::thread::sleep(Duration::from_secs(1));
                }
            }
        });
    }

    /// Snapshot + reset counters for one sample window.
    pub fn take_sample(&self) -> Sample {
        let kb = self.counters.keyboard.swap(0, Ordering::Relaxed);
        let clicks = self.counters.mouse_clicks.swap(0, Ordering::Relaxed);
        let moves = self.counters.mouse_moves.swap(0, Ordering::Relaxed);
        let dist = self.counters.mouse_distance.swap(0, Ordering::Relaxed);
        Sample {
            at: chrono::Utc::now(),
            keyboard_count: kb as i64,
            mouse_count: (clicks + moves) as i64,
            mouse_distance: dist as i64,
        }
    }

    /// True if no input for at least `threshold` (idle detection).
    pub fn is_idle(&self, threshold: Duration) -> bool {
        let last = self.counters.last_input_ms.load(Ordering::Relaxed);
        if last == 0 {
            return self.started.elapsed() >= threshold;
        }
        let now = now_ms();
        now.saturating_sub(last) >= threshold.as_millis() as u64
    }
}

#[cfg(not(feature = "mock-capture"))]
fn handle_event(counters: &Arc<Counters>, event: rdev::Event) {
    use rdev::EventType::*;
    match event.event_type {
        KeyPress(_) => {
            counters.keyboard.fetch_add(1, Ordering::Relaxed);
            touch(counters);
        }
        ButtonPress(_) => {
            counters.mouse_clicks.fetch_add(1, Ordering::Relaxed);
            touch(counters);
        }
        MouseMove { x, y } => {
            let (xi, yi) = (x as u64, y as u64);
            let lx = counters.last_x.swap(xi, Ordering::Relaxed);
            let ly = counters.last_y.swap(yi, Ordering::Relaxed);
            if lx != 0 || ly != 0 {
                let dx = (xi as i64 - lx as i64).abs();
                let dy = (yi as i64 - ly as i64).abs();
                let d = ((dx * dx + dy * dy) as f64).sqrt() as u64;
                if d > 2 {
                    // ignore micro-jitter
                    counters.mouse_moves.fetch_add(1, Ordering::Relaxed);
                    counters.mouse_distance.fetch_add(d, Ordering::Relaxed);
                    touch(counters);
                }
            }
        }
        Wheel { .. } => {
            counters.mouse_moves.fetch_add(1, Ordering::Relaxed);
            touch(counters);
        }
        _ => {}
    }
}

fn touch(counters: &Arc<Counters>) {
    counters.last_input_ms.store(now_ms(), Ordering::Relaxed);
}

fn now_ms() -> u64 {
    use std::time::{SystemTime, UNIX_EPOCH};
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
}
