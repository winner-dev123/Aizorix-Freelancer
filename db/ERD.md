# Entity-Relationship Overview

The schema is **one logical model split across service-owned schemas**. In production each
service owns its tables in a separate database/schema; cross-service references (shown as FKs
here for clarity) become **soft references validated via gRPC/events** at the service boundary.
The migrations in `db/migrations/` materialize the full model for local development.

## Domain clusters

```
 IDENTITY (auth)                         PROFILES (user)
 ───────────────                         ───────────────
 users ─1:N─ oauth_identities            users ─1:1─ freelancer_profiles ─1:N─ freelancer_skills ─N:1─ skills
 users ─1:N─ devices                     users ─1:1─ client_profiles
 users ─1:N─ sessions ─1:N─ refresh_tokens     freelancer_profiles ─1:N─ portfolio_items
 users ─1:N─ mfa_factors                 users ─1:N─ kyc_records
 users ─N:M─ roles (user_roles)
 roles ─N:M─ permissions (role_permissions)

 MARKETPLACE (project / proposal)        WORK LIFECYCLE (contract)
 ───────────────────────────────         ─────────────────────────
 users(client) ─1:N─ projects            proposals ─1:1─ contracts ─1:N─ milestones ─1:N─ deliverables
 projects ─N:M─ skills (project_skills)  contracts ─1:1─ hourly_contracts
 projects ─1:N─ proposals                contracts ─1:N─ contract_events  (event-sourced FSM)
 proposals ─1:N─ proposal_milestones     contracts ─1:N─ disputes
 proposals ─1:N─ proposal_answers

 TIME & SCREENSHOTS (timetracking / screenshot)        MONEY (escrow / payment)
 ─────────────────────────────────────────────         ────────────────────────
 contracts ─1:N─ work_sessions ─1:N─ time_slices       contracts ─1:1─ escrow_accounts ─1:N─ escrow_allocations
 work_sessions ─1:N─ activity_logs  (partitioned)      contracts ─1:N─ payments
 time_slices ─1:1─ screenshots ─1:1─ screenshot_metadata  users ─1:N─ withdrawals ─N:1─ payout_accounts
 contracts ─1:N─ timesheets                            (all movements) ─→ transactions  (double-entry ledger)
                                                        stripe_events (idempotency) · reconciliation_runs

 SOCIAL & PLATFORM
 ─────────────────
 contracts ─1:N─ reviews ─1:1─ review_responses      users ─1:N─ notifications ─1:N─ delivery_attempts
 conversations ─N:M─ users (participants)            users ─1:N─ notification_preferences
 conversations ─1:N─ messages (partitioned) ─1:N─ message_attachments

 CROSS-CUTTING
 ─────────────
 audit_logs (partitioned, hash-chained)   fraud_cases ─1:N─ fraud_signals   risk_scores   admin_actions
 outbox (per-service)   processed_events (consumer idempotency)
```

## Partitioning & retention strategy

| Table          | Partition by         | Granularity | Retention (hot) | Archive |
|----------------|----------------------|-------------|-----------------|---------|
| `activity_logs`| `sampled_at` RANGE   | monthly     | 90 days         | drop (rebuildable from slices) |
| `screenshots`  | `captured_at` RANGE  | monthly     | 90 days         | S3 Glacier (metadata kept) |
| `messages`     | `created_at` RANGE   | monthly     | 18 months       | S3 export, then drop |
| `audit_logs`   | `occurred_at` RANGE  | monthly     | 13 months       | S3 Object-Lock (7 yrs, compliance) |
| `transactions` | none (kept forever)  | —           | ∞               | nightly snapshot |

Partitions are pre-created `current+1` month ahead by a scheduled job (`pg_partman` in prod,
or the `ensure_month_partition()` helper). Old partitions are `DETACH`ed and archived, then
dropped — a metadata-only operation (no row-by-row delete).

## Soft delete vs. hard delete

- **Soft delete** (`deleted_at`): `users`, `projects`, `portfolio_items`, `messages` — partial
  unique indexes (`WHERE deleted_at IS NULL`) keep uniqueness while preserving history.
- **Hard delete on retention/compliance**: `screenshots`, `activity_logs` after `retain_until`;
  GDPR erasure crypto-shreds by destroying the wrapped DEK (renders ciphertext unrecoverable)
  and tombstones the row — cheaper and more certain than overwriting blobs.

## Audit strategy

Every privileged action writes an `audit_logs` row with a **hash chain**
(`row_hash = sha256(prev_hash || canonical(row))`) so any tampering breaks the chain. Rows are
streamed to an S3 bucket with **Object Lock (compliance mode)** for an immutable, regulator-
grade trail. Screenshot *views* specifically are always audited (who, when, which contract).
