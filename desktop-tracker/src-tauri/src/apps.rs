//! Active application + browser URL tracking (Phase 9).
//!
//! The active application name/title comes from the OS window manager. Browser URL capture is
//! best-effort and host-only by default (privacy): we report the *host* (e.g. github.com), not
//! the full path/query. Full-URL capture is an enterprise opt-in that requires the freelancer's
//! explicit consent and a browser extension/native-messaging bridge — out of scope for the MVP,
//! which derives the host from the window title where the browser exposes it.

#[derive(Clone, Debug, Default)]
pub struct ActiveApp {
    pub app_name: String,
    pub window_title: String,
    pub browser_url_host: String,
}

/// Best-effort snapshot of the foreground application.
pub fn active_app() -> ActiveApp {
    #[cfg(feature = "mock-capture")]
    {
        return ActiveApp {
            app_name: "Code".into(),
            window_title: "main.rs - aizorix".into(),
            browser_url_host: String::new(),
        };
    }
    #[cfg(not(feature = "mock-capture"))]
    {
        match active_win_pos_rs::get_active_window() {
            Ok(win) => {
                let app_name = win.app_name.clone();
                let title = win.title.clone();
                let host = if is_browser(&app_name) {
                    extract_host_from_title(&title)
                } else {
                    String::new()
                };
                ActiveApp { app_name, window_title: title, browser_url_host: host }
            }
            Err(_) => ActiveApp::default(),
        }
    }
}

#[cfg(not(feature = "mock-capture"))]
fn is_browser(app: &str) -> bool {
    let a = app.to_lowercase();
    ["chrome", "firefox", "safari", "edge", "brave", "opera", "chromium", "arc"]
        .iter()
        .any(|b| a.contains(b))
}

/// Heuristic: many browsers put "Page Title — Domain" or expose a host in the title bar.
/// We only ever keep the registrable host, never a path or query string.
#[cfg(not(feature = "mock-capture"))]
fn extract_host_from_title(title: &str) -> String {
    for token in title.split(|c: char| c.is_whitespace() || c == '—' || c == '-' || c == '|') {
        let t = token.trim().trim_start_matches("https://").trim_start_matches("http://");
        if t.contains('.') && !t.contains('/') && !t.contains(' ') && t.len() > 3 {
            return t.split('/').next().unwrap_or("").to_string();
        }
    }
    String::new()
}
