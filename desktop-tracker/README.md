# Aizorix Desktop Tracker (Tauri + Rust)

Cross-platform (Windows / macOS / Linux) time tracker that powers **verified hourly work**.
It captures an encrypted screenshot every 15 minutes, measures genuine input activity, runs
fully offline, and syncs securely when a connection returns. The heavy lifting is in Rust;
the webview UI is intentionally tiny.

## Module map (`src-tauri/src/`)

| Module          | Responsibility |
|-----------------|----------------|
| `main.rs`       | Tauri 2 bootstrap: open local store, load device key, start sync, register commands. |
| `commands.rs`   | The UI bridge: `login`, `start_tracking`, `stop_tracking`, `tracking_status`. |
| `state.rs`      | Thread-safe `AppState` (API client, store, device key, activity engine, session). |
| `scheduler.rs`  | **Capture engine**: per-minute activity sampling + per-15-min screenshot/slice flush. |
| `screenshot.rs` | Capture all monitors → downscale → WebP encode → perceptual hash. |
| `activity.rs`   | **Activity engine**: global input *counters* + idle detection (no keylogging). |
| `apps.rs`       | Active application + best-effort browser host (host-only by default). |
| `crypto.rs`     | **Security engine**: AES-256-GCM per-capture encryption, Ed25519 device signing, SHA-256. |
| `db.rs`         | **Offline store**: SQLite (WAL) queues for screenshots + slices, resumable upload FSM. |
| `sync.rs`       | **Sync engine**: drains queues → request slot → PUT to S3 → confirm; exponential backoff. |
| `api.rs`        | HTTP client to the gateway; token refresh via OS keychain. |
| `config.rs`     | Tunables (intervals, jitter, WebP quality, retry bounds). |
| `error.rs`      | Unified `AppError` serialized as `{ code, message }` for the UI. |

## Data flow (one 15-minute slice)

```
 every 60s:   activity.take_sample()  ──► samples[]            (counters only)
 every 15m:   screenshot.capture_primary()  ──► WebP + pHash
              random DEK ──► crypto.encrypt_screenshot()       (offline: encrypt immediately)
              device.sign_metadata(sha256, captured_at, contract)
              db.enqueue_screenshot()  +  db.enqueue_slice(samples)   ◄── written locally first
 background:  sync.drain_once()
              POST /v1/screenshots/upload-slot   ──► presigned PUT + KMS-wrapped DEK
              PUT ciphertext ─────────────────► S3        (blob never touches our services)
              POST /v1/screenshots/confirm       ──► integrity + signature verified server-side
              POST /v1/tracking/sessions/{id}/slices  (idempotent)
```

## Security & privacy properties

- **Client-side encryption.** Pixels are AES-256-GCM encrypted before they ever hit disk or
  the network; a stolen device or bucket yields ciphertext only. The data key is KMS-wrapped
  server-side and never stored in plaintext beyond the brief sync window.
- **Tamper-evidence.** Each capture's metadata is signed with the device's Ed25519 key
  (generated on first run, held in the OS keychain, enrolled with the backend). The server
  re-hashes the uploaded object and verifies the signature.
- **No keylogging.** `activity.rs` only increments counters and accumulates mouse travel — it
  never records key identities, clipboard, or window contents. The disclosure shown in the UI
  matches exactly what is collected.
- **Anti-fakery signals** (uniform-input macros, mouse-jigglers, implausible 100% activity) are
  computed server-side from the samples; the tracker just reports raw counts.

## Prerequisites & run

```bash
# Rust toolchain + Tauri 2 system deps (see https://tauri.app/start/prerequisites)
rustup default stable
pnpm install
pnpm tauri dev        # run
pnpm tauri build      # produce msi/nsis/dmg/appimage/deb bundles

# CI / headless: build with deterministic fakes (no display/input required)
cargo test --features mock-capture
```

## Platform notes

- **Windows:** screen capture via DXGI (xcap); no special permission.
- **macOS:** requires *Screen Recording* and *Accessibility* permissions (the OS prompts on
  first capture / first input listen); entitlements are declared in `entitlements.plist`.
- **Linux:** X11 supported out of the box; Wayland screen capture uses the portal (xcap).
