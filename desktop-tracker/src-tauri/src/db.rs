//! Local persistence (offline-first). All work — slices and encrypted screenshots — is
//! written here FIRST, then drained by the sync engine. This is what lets the tracker run
//! with no network and reconcile later without losing or double-billing work.
//!
//! Encrypted screenshot bytes are stored as files under the app data dir; this table holds
//! only their metadata + upload state machine. SQLite runs in WAL mode for crash safety.

use rusqlite::{params, Connection, OptionalExtension};
use std::path::{Path, PathBuf};

use crate::error::Result;

pub struct LocalStore {
    conn: Connection,
    blob_dir: PathBuf,
}

#[derive(Clone, Debug)]
pub struct PendingScreenshot {
    pub id: String,             // local id
    pub server_id: Option<String>, // assigned after RequestUploadSlot
    pub contract_id: String,
    pub session_id: String,
    pub slice_id: String,
    pub captured_at: String,    // RFC3339
    pub blob_path: String,      // path to ciphertext on disk
    pub nonce_b64: String,
    pub sha256_b64: String,
    pub signature_b64: String,
    pub phash_b64: String,
    pub width: i64,
    pub height: i64,
    pub size_bytes: i64,
    pub activity_pct: i64,
    pub retries: i64,
    /// The locally-generated AES-256 data key (base64), held only until the server KMS-wraps
    /// it at sync time. Stored in the local SQLite which itself lives under the OS-protected
    /// app dir; on a hardened build this column is encrypted under the device key at rest.
    pub client_dek_b64: String,
}

#[derive(Clone, Debug)]
pub struct PendingSlice {
    pub id: String,
    pub session_id: String,
    pub contract_id: String,
    pub payload_json: String,   // the SubmitSlices DTO for this slice
}

impl LocalStore {
    pub fn open(data_dir: &Path) -> Result<Self> {
        std::fs::create_dir_all(data_dir).ok();
        let blob_dir = data_dir.join("blobs");
        std::fs::create_dir_all(&blob_dir).ok();
        let conn = Connection::open(data_dir.join("tracker.db"))?;
        conn.pragma_update(None, "journal_mode", "WAL")?;
        conn.pragma_update(None, "synchronous", "NORMAL")?;
        let store = Self { conn, blob_dir };
        store.migrate()?;
        Ok(store)
    }

    fn migrate(&self) -> Result<()> {
        self.conn.execute_batch(
            r#"
            CREATE TABLE IF NOT EXISTS pending_screenshots (
                id           TEXT PRIMARY KEY,
                server_id    TEXT,
                contract_id  TEXT NOT NULL,
                session_id   TEXT NOT NULL,
                slice_id     TEXT NOT NULL,
                captured_at  TEXT NOT NULL,
                blob_path    TEXT NOT NULL,
                nonce_b64    TEXT NOT NULL,
                sha256_b64   TEXT NOT NULL,
                signature_b64 TEXT NOT NULL,
                phash_b64    TEXT NOT NULL,
                width        INTEGER, height INTEGER, size_bytes INTEGER,
                activity_pct INTEGER NOT NULL DEFAULT 0,
                -- upload_state: queued -> slotted -> uploaded -> confirmed
                upload_state TEXT NOT NULL DEFAULT 'queued',
                upload_url   TEXT, wrapped_dek_b64 TEXT,
                -- The device's locally-generated DEK, held until the server KMS-wraps it at
                -- sync. Kept in its OWN column so set_slot (which writes wrapped_dek_b64) can
                -- never clobber it.
                client_dek_b64 TEXT,
                retries      INTEGER NOT NULL DEFAULT 0,
                next_attempt INTEGER NOT NULL DEFAULT 0,
                created_at   INTEGER NOT NULL
            );
            CREATE INDEX IF NOT EXISTS idx_ss_state ON pending_screenshots(upload_state, next_attempt);

            CREATE TABLE IF NOT EXISTS pending_slices (
                id          TEXT PRIMARY KEY,
                session_id  TEXT NOT NULL,
                contract_id TEXT NOT NULL,
                payload_json TEXT NOT NULL,
                synced      INTEGER NOT NULL DEFAULT 0,
                retries     INTEGER NOT NULL DEFAULT 0,
                created_at  INTEGER NOT NULL
            );

            CREATE TABLE IF NOT EXISTS session_state (
                k TEXT PRIMARY KEY, v TEXT NOT NULL
            );
            "#,
        )?;
        Ok(())
    }

    pub fn blob_dir(&self) -> &Path { &self.blob_dir }

    /// Persist a freshly captured + encrypted screenshot to the queue.
    pub fn enqueue_screenshot(&self, s: &PendingScreenshot) -> Result<()> {
        self.conn.execute(
            r#"INSERT INTO pending_screenshots
               (id, contract_id, session_id, slice_id, captured_at, blob_path, nonce_b64,
                sha256_b64, signature_b64, phash_b64, width, height, size_bytes, activity_pct,
                client_dek_b64, upload_state, created_at)
               VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,?12,?13,?14,?15,'queued', strftime('%s','now'))"#,
            params![s.id, s.contract_id, s.session_id, s.slice_id, s.captured_at, s.blob_path,
                s.nonce_b64, s.sha256_b64, s.signature_b64, s.phash_b64, s.width, s.height,
                s.size_bytes, s.activity_pct, s.client_dek_b64],
        )?;
        Ok(())
    }

    /// Screenshots ready for an upload attempt (any non-confirmed, attempt time elapsed).
    pub fn due_screenshots(&self, now_unix: i64, limit: i64) -> Result<Vec<(PendingScreenshot, String)>> {
        let mut stmt = self.conn.prepare(
            r#"SELECT id, server_id, contract_id, session_id, slice_id, captured_at, blob_path,
                      nonce_b64, sha256_b64, signature_b64, phash_b64, width, height, size_bytes,
                      activity_pct, retries, upload_state, coalesce(client_dek_b64,'')
               FROM pending_screenshots
               WHERE upload_state != 'confirmed' AND next_attempt <= ?1
               ORDER BY created_at LIMIT ?2"#)?;
        let rows = stmt.query_map(params![now_unix, limit], |r| {
            Ok((
                PendingScreenshot {
                    id: r.get(0)?, server_id: r.get(1)?, contract_id: r.get(2)?,
                    session_id: r.get(3)?, slice_id: r.get(4)?, captured_at: r.get(5)?,
                    blob_path: r.get(6)?, nonce_b64: r.get(7)?, sha256_b64: r.get(8)?,
                    signature_b64: r.get(9)?, phash_b64: r.get(10)?, width: r.get(11)?,
                    height: r.get(12)?, size_bytes: r.get(13)?, activity_pct: r.get(14)?,
                    retries: r.get(15)?, client_dek_b64: r.get(17)?,
                },
                r.get::<_, String>(16)?,
            ))
        })?;
        Ok(rows.filter_map(|x| x.ok()).collect())
    }

    /// Total uploads still outstanding, INCLUDING rows currently in backoff (next_attempt in the
    /// future). For the status display, due_screenshots' `next_attempt <= now` filter would hide
    /// retrying captures and could show 0 while billable screenshots are still queued.
    pub fn pending_count(&self) -> Result<i64> {
        let n: i64 = self.conn.query_row(
            "SELECT count(*) FROM pending_screenshots WHERE upload_state != 'confirmed'",
            [],
            |r| r.get(0),
        )?;
        Ok(n)
    }

    pub fn set_slot(&self, id: &str, server_id: &str, upload_url: &str, wrapped_dek_b64: &str) -> Result<()> {
        self.conn.execute(
            "UPDATE pending_screenshots SET server_id=?2, upload_url=?3, wrapped_dek_b64=?4, upload_state='slotted' WHERE id=?1",
            params![id, server_id, upload_url, wrapped_dek_b64])?;
        Ok(())
    }
    pub fn set_state(&self, id: &str, state: &str) -> Result<()> {
        self.conn.execute("UPDATE pending_screenshots SET upload_state=?2 WHERE id=?1", params![id, state])?;
        Ok(())
    }
    pub fn backoff_screenshot(&self, id: &str, next_attempt_unix: i64) -> Result<()> {
        self.conn.execute(
            "UPDATE pending_screenshots SET retries=retries+1, next_attempt=?2 WHERE id=?1",
            params![id, next_attempt_unix])?;
        Ok(())
    }
    pub fn delete_screenshot(&self, id: &str) -> Result<()> {
        self.conn.execute("DELETE FROM pending_screenshots WHERE id=?1", params![id])?;
        Ok(())
    }

    pub fn enqueue_slice(&self, s: &PendingSlice) -> Result<()> {
        self.conn.execute(
            "INSERT OR REPLACE INTO pending_slices (id, session_id, contract_id, payload_json, created_at)
             VALUES (?1,?2,?3,?4, strftime('%s','now'))",
            params![s.id, s.session_id, s.contract_id, s.payload_json])?;
        Ok(())
    }
    pub fn unsynced_slices(&self, limit: i64) -> Result<Vec<PendingSlice>> {
        let mut stmt = self.conn.prepare(
            "SELECT id, session_id, contract_id, payload_json FROM pending_slices WHERE synced=0 ORDER BY created_at LIMIT ?1")?;
        let rows = stmt.query_map(params![limit], |r| Ok(PendingSlice {
            id: r.get(0)?, session_id: r.get(1)?, contract_id: r.get(2)?, payload_json: r.get(3)?,
        }))?;
        Ok(rows.filter_map(|x| x.ok()).collect())
    }
    pub fn mark_slice_synced(&self, id: &str) -> Result<()> {
        self.conn.execute("UPDATE pending_slices SET synced=1 WHERE id=?1", params![id])?;
        Ok(())
    }

    pub fn save_kv(&self, k: &str, v: &str) -> Result<()> {
        self.conn.execute("INSERT OR REPLACE INTO session_state (k,v) VALUES (?1,?2)", params![k, v])?;
        Ok(())
    }
    pub fn load_kv(&self, k: &str) -> Result<Option<String>> {
        Ok(self.conn.query_row("SELECT v FROM session_state WHERE k=?1", params![k], |r| r.get(0)).optional()?)
    }
    pub fn delete_kv(&self, k: &str) -> Result<()> {
        self.conn.execute("DELETE FROM session_state WHERE k=?1", params![k])?;
        Ok(())
    }
}
