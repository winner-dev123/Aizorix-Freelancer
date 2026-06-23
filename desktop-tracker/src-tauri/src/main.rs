// Aizorix desktop tracker — Tauri 2 entrypoint.
// Prevents a console window on Windows release builds.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]
// WIP desktop app: some fields/constants/methods are scaffolding for not-yet-wired features, and
// a couple of pedantic clippy style lints (a cfg-gated early return; doc-list indentation) are
// relaxed crate-wide rather than contorting the code.
#![allow(dead_code)]
#![allow(clippy::needless_return)]
#![allow(clippy::doc_overindented_list_items)]

mod activity;
mod api;
mod apps;
mod commands;
mod config;
mod crypto;
mod db;
mod error;
mod scheduler;
mod screenshot;
mod state;
mod sync;

use std::sync::Arc;

use crate::crypto::DeviceKey;
use crate::db::LocalStore;
use crate::state::AppState;

fn main() {
    tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::from_default_env())
        .init();

    tauri::Builder::default()
        .plugin(tauri_plugin_single_instance::init(|_app, _argv, _cwd| {}))
        .setup(|app| {
            // App data dir: per-user, OS-appropriate (e.g. %APPDATA%/com.aizorix.tracker).
            let data_dir = app
                .path()
                .app_data_dir()
                .unwrap_or_else(|_| std::env::temp_dir().join("aizorix-tracker"));

            let store = LocalStore::open(&data_dir).expect("open local store");
            let device = DeviceKey::load_or_create().expect("device key");
            let state = Arc::new(AppState::new(store, device));

            // Resume an interrupted session's pending uploads at startup (offline → online).
            sync::spawn(Arc::clone(&state));

            app.manage(state);
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            commands::login,
            commands::start_tracking,
            commands::stop_tracking,
            commands::tracking_status,
        ])
        .run(tauri::generate_context!())
        .expect("error while running aizorix tracker");
}

// Bring the Tauri path-resolver trait into scope for `app.path()`.
use tauri::Manager;
