# Aizorix Service Level Objectives

This document defines the SLIs, SLOs, and error-budget policy for Aizorix. The
alerting rules in `prometheus/rules/alerts.rules.yml` implement the burn-rate
policy described here. SLOs are measured over a **rolling 30-day window**.

## Conventions

- **SLI**: a ratio `good events / valid events` in `[0, 1]`.
- **Error budget**: `1 - SLO`. A 99.9% SLO yields a 0.1% budget — roughly
  **43m 12s** of full outage per 30 days.
- **Burn rate**: how fast the budget is consumed relative to "even" consumption.
  A burn rate of `1` exhausts the budget in exactly 30 days; `14.4` exhausts it
  in ~2 days; `6` in ~5 days.

---

## SLO 1 — API Availability (per service)

| | |
|---|---|
| **SLI** | `sum(rate(http_requests_total{code!~"5.."}[w])) / sum(rate(http_requests_total[w]))` |
| **SLO** | **99.9%** of requests succeed (non-5xx). 4xx are excluded — they are client errors, not service failures. |
| **Budget** | 0.1% (~43m/30d) |

Tier note: payment, escrow, and auth are **tier-1** and are held to 99.95%
(budget ~21m/30d) in their per-service overrides; all others to 99.9%.

## SLO 2 — API Latency (per service)

| | |
|---|---|
| **SLI** | fraction of requests served in < **500ms**: `rate(http_request_duration_seconds_bucket{le="0.5"}[w]) / rate(http_request_duration_seconds_count[w])` |
| **SLO** | **99%** of requests faster than 500ms; **p99 < 1s** hard ceiling. |
| **Budget** | 1% slower-than-500ms |

## SLO 3 — Screenshot Ingest Success

The verified-hourly-work differentiator depends on this pipeline; failed ingests
directly threaten billing integrity.

| | |
|---|---|
| **SLI** | `sum(rate(screenshot_ingest_total{result="success"}[w])) / sum(rate(screenshot_ingest_total[w]))` |
| **SLO** | **99.5%** of screenshot uploads ingested successfully. |
| **Budget** | 0.5% |

## SLO 4 — Payment Success

| | |
|---|---|
| **SLI** | `sum(rate(payment_transactions_total{type="capture",status="succeeded"}[w])) / sum(rate(payment_transactions_total{type="capture"}[w]))` |
| **SLO** | **99.9%** of payment captures succeed (excluding legitimate card declines, which are labelled `status="declined"` and are not counted as failures). |
| **Budget** | 0.1% |

## Hard invariant — Escrow reconciliation

Not an error-budget SLO but a **correctness invariant**: the escrow ledger and
the payment provider balance must agree to within **$1.00**
(`abs(escrow_reconciliation_mismatch_cents) <= 100`). Any breach pages
immediately and freezes releases — money correctness is non-negotiable.

---

## Burn-rate alerting policy (multi-window, multi-burn-rate)

Two alerts per budgeted SLO, following the Google SRE workbook. Each pairs a
long window (detects sustained burn) with a short window (confirms the problem
is still happening), which removes flapping while keeping detection fast.

| Severity | Long window | Short window | Burn rate | Budget consumed before fire | Action |
|----------|-------------|--------------|-----------|------------------------------|--------|
| **Page** (fast) | 1h | 5m | **14.4** | ~2% in 1h | PagerDuty |
| **Ticket** (slow) | 6h | 30m | **6** | ~5% in 6h | Slack |

For the 99.9% availability SLO (budget = 0.001):

- Fast page fires when `error_ratio[1h] > 14.4 * 0.001` **and** `error_ratio[5m] > 14.4 * 0.001`.
- Slow ticket fires when `error_ratio[6h] > 6 * 0.001` **and** `error_ratio[30m] > 6 * 0.001`.

These map to `HighErrorRateFastBurn` / `HighErrorRateSlowBurn`
(availability) and `LatencySLOFastBurn` / `P99LatencySLOBreach` (latency) in the
alert rules.

## Error-budget policy (governance)

- **Budget remaining > 50%** — ship freely.
- **Budget remaining 10–50%** — proceed, but prioritize reliability fixes in the
  next sprint; risky changes require a second reviewer.
- **Budget exhausted (≤ 0)** — **feature freeze** for that service: only
  reliability, security, and rollback changes merge until the trailing 30-day
  SLO recovers above target. Escrow/payment freezes also halt automated payouts.

## Review cadence

SLOs are reviewed monthly. If a service consistently overshoots (e.g. 99.99%
actual vs 99.9% target) the target is tightened; if it is chronically infeasible,
the target is renegotiated with stakeholders rather than left perpetually red.
