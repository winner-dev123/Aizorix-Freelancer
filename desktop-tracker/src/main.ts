// Minimal tracker UI. The heavy lifting (capture, encryption, sync) is in Rust; this just
// drives the Tauri commands and shows status.
import { invoke } from "@tauri-apps/api/core";

const $ = (id: string) => document.getElementById(id)!;

let deviceId = "";

async function login() {
  const email = ($("email") as HTMLInputElement).value;
  const password = ($("password") as HTMLInputElement).value;
  try {
    const res = await invoke<{ user_id: string; device_pubkey: string; device_id: string }>("login", { email, password });
    // The Rust `login` command enrolls this device (POST /v1/users/me/devices) and returns the
    // server-assigned device_id, which start_tracking sends so the backend can verify uploads.
    deviceId = res.device_id;
    await loadContracts();
    ($("login-view") as HTMLElement).hidden = true;
    ($("track-view") as HTMLElement).hidden = false;
  } catch (e: any) {
    alert(`Login failed: ${e.message ?? e.code}`);
  }
}

async function loadContracts() {
  // Real build: fetch the freelancer's active hourly contracts from the gateway.
  const select = $("contract-select") as HTMLSelectElement;
  select.innerHTML = `<option value="demo-contract">Demo hourly contract</option>`;
}

async function start() {
  const contractId = ($("contract-select") as HTMLSelectElement).value;
  try {
    await invoke("start_tracking", { contractId, deviceId });
    ($("start-btn") as HTMLElement).hidden = true;
    ($("stop-btn") as HTMLElement).hidden = false;
    pollStatus();
  } catch (e: any) {
    alert(`Could not start: ${e.message ?? e.code}`);
  }
}

async function stop() {
  try {
    const r = await invoke<{ active_seconds: number; avg_activity_pct: number }>("stop_tracking", {});
    ($("start-btn") as HTMLElement).hidden = false;
    ($("stop-btn") as HTMLElement).hidden = true;
    ($("status").textContent = `Stopped — ${Math.round(r.active_seconds / 60)} min, ${r.avg_activity_pct}% activity`);
  } catch (e: any) {
    alert(`Could not stop: ${e.message ?? e.code}`);
  }
}

async function pollStatus() {
  const s = await invoke<{ tracking: boolean; billing_week?: string; pending_uploads: number }>("tracking_status", {});
  if (s.tracking) {
    $("status").innerHTML =
      `<span class="pill">Tracking</span> week ${s.billing_week ?? "-"} · ${s.pending_uploads} pending upload(s)`;
    setTimeout(pollStatus, 5000);
  }
}

$("login-btn").addEventListener("click", login);
$("start-btn").addEventListener("click", start);
$("stop-btn").addEventListener("click", stop);
