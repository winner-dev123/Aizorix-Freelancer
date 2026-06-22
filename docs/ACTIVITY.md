# Activity Monitoring — Formulas & Algorithms (Phase 9)

Two implementations cooperate: the **tracker** (`desktop-tracker/src-tauri/src/activity.rs`)
collects privacy-preserving input counts; the **server** (`services/timetracking/internal/activity/activity.go`)
computes the authoritative, fraud-resistant activity percentage. The server never trusts the
client's computed percentage — it recomputes from raw samples.

## 1. Collection (client, privacy-preserving)

Per **sample** (default 60s), the tracker reports only:
- `keyboard_count` — number of keypress events (NOT which keys)
- `mouse_count` — clicks + significant moves
- `mouse_distance` — accumulated pixel travel (micro-jitter < 3px ignored)

No keystroke contents, clipboard, or window contents are ever captured.

## 2. Idle detection

```
 idle  ⇔  (now − last_input_ms) ≥ IDLE_THRESHOLD       (default 300s)
```
A continuous span with no input ≥ threshold is idle. Idle slices skip the screenshot.

## 3. Per-sample intensity (diminishing returns)

A single keystroke per minute must NOT read as 100%. Each input channel saturates:

```
 f(x, sat) = log(1+x) / log(1+sat)                     // 0..1, diminishing returns
 intensity = min(1, w_k·f(kb,sat_kb) + w_m·f(mouse,sat_mouse) + w_d·f(dist,sat_dist))
 defaults:  w_k=0.5, w_m=0.3, w_d=0.2
            sat_kb=120, sat_mouse=90, sat_dist=8000   (per-sample "fully busy" anchors)
```

## 4. Activity percentage (per slice)

```
 sample is "active"  ⇔  intensity ≥ ActiveFloor (0.08) AND not inside an idle span
 active_seconds      = Σ seconds of active samples
 excused_idle        = ExcusedIdleCap (0.25) · slice_seconds      // reading/calls, capped
 denominator         = slice_seconds − excused_idle
 activity_pct        = clamp( round(100 · active_seconds / denominator), 0, 100 )
```

Excused idle lets legitimate non-input work not tank the score, but only up to a cap so it
can't be abused; beyond the cap, idle counts against activity.

## 5. Fraud signals (computed alongside, never used for billing directly)

| Signal | Rule |
|--------|------|
| `uniform_input_pattern` | coefficient of variation of per-sample intensity < 1% over many samples (macro) |
| `implausible_full_activity` | activity ≥ 99% with zero idle over a ≥10-min slice |
| `mouse_only_no_keys` | mouse travel present, zero keystrokes for the whole slice (jiggler) |

These set `time_slices.flagged` and emit `activity.suspicious` → fraud service. Billing uses the
computed `activity_pct`; flags drive review, not automatic non-payment.

## 6. Worked example

A 10-min slice (600s), samples each minute, genuinely busy with varian­ce → ~10 active samples,
no idle → `activity_pct ≈ 100·600/(600−0) ≈ 100%`. Same slice but only 2 minutes of input then
silence → idle detected after 5 min, `active_seconds ≈ 120`, excused 150s → denominator 450s →
`activity_pct ≈ 27%`. Identical input every minute → flagged `uniform_input_pattern` even at high
percentage. These exact cases are covered by `activity_test.go`.
