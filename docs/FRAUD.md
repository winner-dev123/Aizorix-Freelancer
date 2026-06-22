# Fraud Detection (Phase 13)

The fraud service is read-mostly: it consumes the event firehose, computes features and risk
scores, and opens investigation cases. It never blocks the hot path — it *recommends* holds,
which `payment`/`admin` enforce.

## 1. What we detect

| Category | Signals |
|----------|---------|
| Repeated/static screenshots | identical or near-identical perceptual hashes across a session/week |
| Screenshot manipulation | server re-hash ≠ device hash; bad device signature; impossible dimensions |
| Fake activity | uniform input intensity (macro), mouse-only-no-keys (jiggler), implausible 100% activity |
| Bots/automation | input timing regularity, no app/window changes, headless indicators |
| VM/sandbox usage | device fingerprint heuristics (MAC/OUI, GPU, timing, hypervisor artifacts reported by tracker) |
| Unusual work patterns | impossible travel (logins), 24/7 activity, sudden rate spikes, geo mismatch |
| Collusion / payment fraud | same device across "client" + "freelancer", circular payments, velocity |

## 2. Architecture

```
 Kafka topics ─┬─ screenshot.ingested ─┐
               ├─ worksession.events   │   ┌─────────────────────────────────────┐
               ├─ activity.suspicious  ├──►│ fraud-svc consumers (idempotent)     │
               ├─ payment.events       │   │  → feature extraction                │
               ├─ user.events          │   │  → feature_snapshots (per subject)   │
               └─ session.created      ┘   │  → rules engine + ML scorer          │
                                           │  → risk_scores / fraud_signals       │
                                           │  → open fraud_cases (threshold)      │
                                           └──────────┬──────────────────────────┘
                                                      ▼
                              emits: fraud.case_opened, screenshot.flagged, account.risk_changed
                                                      ▼
                                       admin review queue  +  payment hold recommendation
```

## 3. Feature engineering (examples)

Per `(subject_type, subject_id)` rolling windows (1h / 24h / 7d), stored in `feature_snapshots`:

- **Screenshot:** `dup_phash_ratio`, `phash_entropy`, `integrity_fail_count`, `unsigned_ratio`.
- **Activity:** `activity_variance`, `idle_ratio`, `keys_per_mouse_ratio`, `night_activity_pct`,
  `slice_uniformity` (coefficient of variation of intensity), `app_switch_rate`.
- **Account/session:** `distinct_geo_24h`, `impossible_travel_flag`, `device_reuse_count`,
  `vm_fingerprint_score`, `account_age_days`.
- **Payment:** `payout_velocity`, `chargeback_ratio`, `client_freelancer_same_device`.

## 4. Risk scoring

Hybrid: a transparent **weighted-rules** baseline (auditable, explainable for disputes) plus an
optional gradient-boosted model for second-order patterns.

```
 risk = sigmoid( Σ w_i * f_i )           // f_i normalized features, w_i tuned per signal
 band = critical (>0.85) | high (>0.6) | medium (>0.35) | low
 reason_codes = top contributing signals (for the investigator + the user-facing explanation)
```

Each `fraud_signals` row carries a `weight` and `details`; `risk_scores` stores the aggregate,
band, contributing `features`, and `model_version` for reproducibility. A case opens when band
≥ high or any hard rule fires (e.g. integrity failure, mouse-jiggler over a full week).

## 5. Investigation workflow

```
 fraud.case_opened ─► admin review queue (sorted by risk_score desc)
   investigator opens case → sees: timeline, flagged screenshots (audited view),
     activity charts, device/geo history, payment graph, reason codes
   actions: dismiss · request more info · pause contract · hold payout · suspend account
   every action → admin_actions + audit_logs + dispute/payment events
   outcome feeds back as a label → model retraining set
```

## 6. Guardrails

- **No silent punishment:** automated detection only *recommends*; account suspension / payout
  holds require an admin action (or a documented auto-rule with appeal).
- **Explainability:** reason codes are surfaced to the user and the investigator; the rules
  baseline guarantees a human-readable justification even when the ML model abstains.
- **Privacy:** features use input *counts* and hashes, never key contents or screenshot pixels;
  investigators viewing a screenshot is itself audited.
- **Feedback loop:** confirmed/dismissed labels retrain the model; drift monitored in `analytics`.
