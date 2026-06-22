# Legal & Compliance (Phase 20)

Aizorix processes PII, financial data, and — uniquely — **workplace monitoring data**
(screenshots, activity). That makes consent, transparency, data residency, and retention
first-class architectural concerns, not afterthoughts.

## 1. Regulatory scope

| Regime | Applies to | Key obligations honored |
|--------|-----------|--------------------------|
| GDPR (EU/EEA) | EU users + anyone processing their data | lawful basis, consent, access/erasure/portability, DPIA for monitoring, residency. |
| CCPA/CPRA (California) | CA residents | notice, opt-out of "sale/share" (we don't sell), access/delete. |
| Employee/worker monitoring laws | freelancers being tracked (varies by country/state) | explicit disclosure + consent; some jurisdictions require purpose limitation + minimization. |
| PCI-DSS (SAQ-A) | card data | minimized — Stripe hosts card data; we never store PANs. |
| SOC 2 (target) | platform trust | audit trail, access control, change management, monitoring. |

## 2. Consent & monitoring disclosure (the critical one)

Screenshot/activity monitoring is **opt-in and contract-scoped**, never silent:

1. **At registration**, freelancers must accept a plain-language *Monitoring Disclosure*
   (`accepted_monitoring_disclosure` is recorded by the auth service and audited).
2. **Per hourly contract**, the freelancer re-confirms what is captured (screenshots every
   15 min, input *counts* not keystrokes, active app, browser host) before tracking can start.
   The contract stores this acceptance.
3. **In the tracker UI**, a persistent disclosure panel states exactly what is collected, and
   tracking is always user-initiated (Start/Stop) with a visible "Tracking" indicator.
4. **Minimization by design:** no keystroke contents, no clipboard, no continuous video; browser
   capture is host-only by default; fully idle slices skip the screenshot entirely.

```
 Consent flow (freelancer, hourly contract)
 ┌──────────────┐   accept ToS +    ┌───────────────┐  per-contract   ┌────────────────┐
 │ Registration │──monitoring────►  │  Accept hourly │──monitoring ──► │ Tracker: Start │
 │              │  disclosure       │  contract terms│   re-consent    │ (visible badge)│
 └──────────────┘  (audited)        └───────────────┘   (audited)      └────────────────┘
```

## 3. Data subject rights

- **Access/portability:** `analytics`/`admin` assemble a user export (profile, contracts,
  timesheets, screenshots metadata + signed download bundle) within the statutory window.
- **Erasure ("right to be forgotten"):** `screenshot.DeleteForCompliance` **crypto-shreds**
  (destroys wrapped DEKs) + tombstones rows; PII columns are nulled; the account is anonymized
  while preserving immutable financial/audit records (legal basis: legal obligation / dispute
  defense). Erasure requests flow as a `gdpr.erasure_requested` event fanned out to every
  service that holds the subject's data.
- **Rectification & objection:** profile edits; opt-out of non-essential processing.

## 4. Data residency

- EU users' PII + screenshots are pinned to `eu-west-1` (separate RDS/S3 + region-locked KMS
  keys); routing keys off the `residency_country` claim in the JWT. Cross-region replication for
  residency-bound data is disabled or kept in-region.

## 5. Retention policy (defaults; configurable per regulation)

| Data | Retention | Then |
|------|-----------|------|
| Screenshots (blobs) | 90 days hot | crypto-shred unless on legal hold |
| Screenshot metadata | 13 months | retain (audit) then anonymize |
| Activity logs | 90 days | drop partitions (rebuildable from timesheets) |
| Messages | 18 months | export + drop |
| Financial ledger (`transactions`) | 7 years | retain (tax/audit) |
| Audit logs | 13 months hot + 7 years S3 Object-Lock | immutable archive |
| Inactive account PII | per request / 24 months dormant | anonymize |

Retention is enforced by `retain_until` + a scheduled partition/lifecycle job; legal holds
(`legal_hold = true`, S3 Object Lock) override deletion during disputes/investigations.

## 6. Compliance architecture hooks

- Consent + disclosure acceptance are **events + audit rows**, queryable for proof.
- A **Data Processing Inventory** (which service holds which category of PII) is maintained in
  `docs/dpi.md` and drives erasure fan-out.
- DPIA for the monitoring feature is tracked as an ADR; sub-processor list (AWS, Stripe, Sumsub,
  Twilio, SES) is published and contractually bound (DPAs).
