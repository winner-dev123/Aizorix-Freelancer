# Aizorix — System Architecture (Phases 1 & 2)

This document is the authoritative design for the platform. It covers the high-level
architecture, service boundaries, data + event flows, security architecture, scalability
and multi-region strategy, cost optimization, and disaster recovery. Per-service deep dives
follow in the service catalog (§7).

**Design targets:** 1M registered users · 100k concurrent · ~25k req/s peak public API ·
global (multi-region) · screenshots: ~4 captures/hr/active-hourly-contract, ≤2 MB each,
encrypted at rest, retained 90 days hot + archived.

---

## 1. Architectural principles

1. **Bounded contexts over a shared monolith.** Each service owns its data and exposes it
   only through contracts (gRPC/REST) or events. No cross-service SQL.
2. **Event-driven by default, synchronous where correctness demands it.** Money movement
   (escrow/payment) and auth use synchronous, idempotent RPCs; everything else (search
   indexing, notifications, analytics, fraud signals) is asynchronous via Kafka.
3. **Outbox, never dual-write.** A state change and its event are committed in one DB
   transaction (transactional outbox); a relay publishes to Kafka. Consumers are idempotent.
4. **The contract is the source of truth.** `api/proto` + `api/openapi` are versioned and
   reviewed before code. Breaking changes require a new version.
5. **Secure & private by construction.** PII and all screenshots use envelope encryption
   (KMS-wrapped data keys). Every privileged read/write is audited.
6. **Cell-based, horizontally scalable.** Stateless services scale on HPA; stateful stores
   scale by read replicas, sharding, and partitioning.

---

## 2. High-level architecture (C4 container view)

```
                            ┌────────────────────────────────────────────┐
   End users (browsers)     │                 AWS edge                    │
   ──────────────────────►  │  Route53 (latency/geo)  →  CloudFront (CDN) │
   Desktop tracker (Tauri)  │            │                    │ WAF + Shield│
   ──────────────────────►  │            ▼                    ▼            │
                            │      ┌───────────┐        ┌──────────────┐   │
                            │      │  Next.js  │        │  ALB / NLB   │   │
                            │      │  (SSR/BFF)│        │ (Ingress)    │   │
                            │      └─────┬─────┘        └──────┬───────┘   │
                            └────────────┼─────────────────────┼──────────┘
                                         │   public REST/WS     │ gRPC (mTLS, mesh)
                                         ▼                      ▼
                            ┌───────────────────────────────────────────────────┐
                            │            EKS cluster (per region)                │
                            │  ┌──────────────┐   API Gateway / Envoy mesh       │
                            │  │ api-gateway  │── routing, authN, rate-limit ────│
                            │  └──────┬───────┘                                  │
                            │         │ gRPC                                     │
                            │  ┌──────┴───────────────────────────────────────┐ │
                            │  │  auth  user  project  proposal  contract      │ │
                            │  │  timetracking  screenshot  payment  escrow    │ │
                            │  │  review  messaging  notification  search      │ │
                            │  │  fraud  admin  analytics                      │ │
                            │  └──────┬───────────────┬──────────────┬────────┘ │
                            └─────────┼───────────────┼──────────────┼──────────┘
                                      │               │              │
                 ┌────────────────────┼───────────────┼──────────────┼─────────────────┐
                 ▼                    ▼               ▼              ▼                 ▼
          ┌────────────┐      ┌──────────────┐  ┌──────────┐  ┌────────────┐   ┌────────────┐
          │ PostgreSQL │      │   Kafka/MSK  │  │  Redis   │  │ OpenSearch │   │     S3     │
          │ (RDS, 1 DB │      │ (event bus,  │  │ (cache,  │  │ (search,   │   │ (screens,  │
          │ per service│      │  outbox sink)│  │ presence,│  │  freelancer│   │  exports,  │
          │  schema)   │      │              │  │  rate)   │  │  ranking)  │   │  backups)  │
          └────────────┘      └──────────────┘  └──────────┘  └────────────┘   └────────────┘
                                      │
                                      ▼
                          ┌───────────────────────────┐
                          │  External: Stripe Connect, │
                          │  KMS, SES/SNS, Twilio,     │
                          │  Sumsub (KYC), VirusTotal  │
                          └───────────────────────────┘
```

**Why a BFF + API Gateway:** Next.js renders SSR and acts as a thin BFF for browser
sessions (cookie ↔ token exchange, CSRF). The desktop tracker and mobile clients hit the
API Gateway directly with bearer tokens. The gateway terminates TLS, validates JWT, applies
per-identity rate limits, and routes to services over the mesh (mTLS, retries, circuit
breaking handled by Envoy/Istio sidecars).

---

## 3. Service boundaries & ownership

The decomposition follows the **commerce lifecycle**, not the UI:

```
  Identity & Trust         Marketplace             Work & Money              Platform
  ────────────────         ───────────             ────────────              ────────
  auth                     project                 contract                  notification
  user (profiles, KYC)     proposal                timetracking              search
                           review                  screenshot                fraud
                                                    payment                   admin
                                                    escrow                    analytics
                                                    messaging
```

**Boundary rules**
- `auth` owns *only* credentials, tokens, sessions, MFA, OAuth links. Profile data lives in
  `user`. This keeps the high-security blast radius small.
- `contract` is the **orchestrator** of the work lifecycle. It does not own money or
  screenshots; it coordinates `escrow`, `timetracking`, and `payment` via events + RPC.
- `screenshot` is split from `timetracking` because its storage, encryption, retention, and
  compliance profile differ sharply (large encrypted blobs vs. small structured rows).
- `fraud` is read-mostly: it consumes events from everywhere and emits risk signals; it never
  blocks the hot path synchronously (it can *recommend* holds, which `payment`/`admin` enforce).

---

## 4. Data flow — the differentiator (hourly verified work)

This is the highest-value, most novel flow. End to end:

```
 Desktop Tracker (Tauri/Rust)                 Backend                         Consumers
 ─────────────────────────────                ───────                         ─────────
 [10:00] capture screen ────┐
   compress (WebP)          │
   AES-256-GCM encrypt      │  (a) request presigned PUT (gRPC, mTLS, JWT)
   with per-capture DEK ────┼───────────────► screenshot-svc ──► issues S3 presigned URL
                            │                       │            + DEK wrapped by KMS
 [10:00] activity sample    │  (b) PUT ciphertext directly to S3 (client → S3, bypasses svc)
   keys/mouse counters      │───────────────────────────────────► S3 (SSE-KMS, versioned)
   active app + URL         │
   idle detection           │  (c) confirm upload + metadata (sha256, dims, activity%)
   ──► build 10-min slice   │───────────────► screenshot-svc ──► writes screenshot_metadata
                            │                       │            └─► outbox: screenshot.ingested
 offline? buffer in SQLite ─┘                       │
   + WAL, sync when online                          ▼
                                          Kafka topic: screenshot.ingested
                                          ┌──────────┬──────────┬─────────────┐
                                          ▼          ▼          ▼             ▼
                                     timetracking  fraud     analytics   notification
                                     (builds       (dup hash, (rollups)   (client: "new
                                      work_session) VM/macro              screenshots")
                                      activity%     scoring)
                                                    │
                                          weekly cron / billing.week_ready
                                                    ▼
                                          contract-svc → escrow-svc.releaseHours()
                                                    ▼
                                          payment-svc → Stripe transfer to freelancer
```

**Key properties**
- **Client-to-S3 direct upload** with presigned URLs keeps multi-MB blobs off the service
  fabric; the service only handles small metadata.
- **Encryption happens on the device** before upload (zero-trust: even a compromised bucket
  yields ciphertext). The DEK is generated client-side, wrapped by KMS, and the wrapped DEK
  is stored in `screenshot_metadata`. Decryption-on-read happens only for authorized viewers.
- **Integrity:** the device computes `sha256(ciphertext)` and signs the metadata with a
  device key; the service re-hashes the stored object asynchronously and flags mismatches.
- **Tamper/fraud signals** (perceptual-hash duplicates, impossible activity, VM/macro
  fingerprints) are computed by `fraud` off the event stream — never on the capture path.

See [`SCREENSHOT_PIPELINE.md`](./SCREENSHOT_PIPELINE.md) and `desktop-tracker/` for detail.

---

## 5. Event-driven architecture

**Backbone:** Kafka (MSK in prod, Redpanda in dev). Topics are versioned, keyed for ordering,
and consumed by idempotent handlers.

### 5.1 Topic taxonomy

| Topic                        | Key            | Partitions (prod) | Retention | Producers → Consumers |
|------------------------------|----------------|-------------------|-----------|------------------------|
| `user.events`                | `user_id`      | 24                | 7d        | auth,user → search,notification,analytics |
| `project.events`             | `project_id`   | 48                | 7d        | project → search,notification,fraud |
| `proposal.events`            | `project_id`   | 48                | 7d        | proposal → notification,analytics |
| `contract.events`            | `contract_id`  | 48                | 30d       | contract → escrow,timetracking,notification |
| `worksession.events`         | `contract_id`  | 96                | 30d       | timetracking → contract,analytics,fraud |
| `screenshot.ingested`        | `contract_id`  | 96                | 14d       | screenshot → timetracking,fraud,analytics |
| `screenshot.flagged`         | `contract_id`  | 24                | 90d       | fraud → admin,notification |
| `payment.events`             | `contract_id`  | 24                | 90d       | payment,escrow → contract,analytics,notification |
| `dispute.events`             | `dispute_id`   | 12                | 365d      | contract,admin → notification,analytics |
| `audit.events`               | `actor_id`     | 24                | 365d→Glacier | all → analytics (audit sink) |
| `dlq.<topic>`                | original key   | =source           | 14d       | failed handlers → on-call tooling |

### 5.2 Delivery guarantees & patterns

- **Transactional outbox:** producers write `(aggregate, event)` to an `outbox` table in the
  same transaction as the state change. A Debezium/CDC or polling relay publishes to Kafka and
  marks the row sent. This guarantees *at-least-once* with no lost events.
- **Idempotent consumers:** every consumer records processed `event_id`s (Redis set or a
  `processed_events` table with a unique constraint) and no-ops on replays.
- **Ordering:** per-aggregate ordering via partition key (e.g., all events for one contract
  land on one partition). Cross-aggregate ordering is not assumed.
- **Saga orchestration** for multi-service workflows (hiring, milestone release, weekly
  billing). `contract` runs the saga as a state machine; compensating actions on failure
  (e.g., `escrow.refund` if `payment.capture` fails). See `docs/SAGAS.md`.
- **Dead-letter queues** per topic; poison messages are parked after N retries with backoff.

---

## 6. Security architecture

Full detail in [`SECURITY.md`](./SECURITY.md); summary:

```
 Layer            Control
 ───────          ───────
 Edge             CloudFront + AWS WAF (OWASP CRS, bot control) + Shield Advanced (DDoS)
 Transport        TLS 1.3 public; mTLS service-to-service (Istio/SPIFFE identities)
 AuthN            Short-lived ES256 JWT (15m) + rotating refresh tokens (opaque, hashed)
 AuthZ            RBAC (role→permission) + ABAC guards (owns-resource, contract-party)
 Secrets          AWS Secrets Manager + IRSA (no static creds in pods); rotation enabled
 Data at rest     RDS/EBS/S3 SSE-KMS; app-level envelope encryption for PII + screenshots
 Data in transit  All hops TLS; presigned S3 uploads are HTTPS-only
 Tenant isolation Row-level auth checks; screenshots scoped to contract parties + admins
 Audit            Append-only partitioned audit_logs + immutable S3 audit sink (Object Lock)
 Abuse            Per-identity + per-IP rate limits (Redis token bucket); progressive MFA
```

**Threat-model highlights**
- *Screenshot exfiltration:* mitigated by client-side encryption + KMS-wrapped DEKs +
  least-privilege IAM + per-view audit + signed, short-TTL download URLs.
- *Tracker tampering / fake activity:* device attestation key, signed metadata, server-side
  re-hash, perceptual-hash dedupe, VM/macro fingerprints, ML risk scoring in `fraud`.
- *Account takeover:* MFA, device fingerprinting, impossible-travel detection, refresh-token
  rotation with reuse detection (auto-revoke session family).
- *Payment fraud / collusion:* velocity checks, escrow holds, Stripe Radar + internal `fraud`
  scoring, manual review queue in `admin`.

---

## 7. Service catalog (Phase 2 deep dive)

Each service spec lists: responsibilities · sync API · DB ownership · events produced ·
events consumed · scaling · security notes. (Full proto in `api/proto/<svc>/v1`.)

### 7.1 auth
- **Responsibilities:** register/login, password (Argon2id), email/phone verify, ES256 JWT
  issuance, refresh-token rotation, MFA (TOTP + WebAuthn), OAuth (Google/GitHub/LinkedIn),
  session & device management, account recovery.
- **API (gRPC + REST via gateway):** `Register`, `Login`, `RefreshToken`, `Logout`,
  `VerifyEmail`, `EnrollMFA`, `VerifyMFA`, `OAuthCallback`, `ListSessions`, `RevokeSession`,
  `IntrospectToken` (internal), `GetJWKS`.
- **DB:** `users(credentials only)`, `refresh_tokens`, `sessions`, `devices`, `mfa_factors`,
  `oauth_identities`, `email_verifications`, `password_resets`.
- **Produces:** `user.registered`, `session.created`, `session.revoked`, `mfa.enrolled`.
- **Consumes:** `user.profile_deleted` (cascade session revoke).
- **Scaling:** stateless, HPA on CPU + RPS; JWKS cached at gateway; token introspection is
  local (asymmetric verify) — no per-request call to auth. Redis for refresh-token family
  reuse detection.
- **Security:** highest tier. Separate KMS key for token signing keys; key rotation via JWKS
  with overlapping `kid`s; brute-force lockout; secrets via IRSA.

### 7.2 user
- **Responsibilities:** client & freelancer profiles, skills, portfolio, rates, availability,
  roles/permissions assignment, KYC status (via Sumsub), public profile projections.
- **API:** `GetProfile`, `UpdateFreelancerProfile`, `UpdateClientProfile`, `AddSkill`,
  `SubmitKYC`, `AssignRole`, `CheckPermission` (internal).
- **DB:** `freelancer_profiles`, `client_profiles`, `skills`, `freelancer_skills`,
  `portfolio_items`, `roles`, `permissions`, `role_permissions`, `user_roles`, `kyc_records`.
- **Produces:** `user.profile_updated`, `user.kyc_verified`, `user.role_changed`.
- **Consumes:** `user.registered` (create empty profile shell).
- **Scaling:** read-heavy; Redis cache for hot profiles; OpenSearch projection for discovery.
- **Security:** PII envelope-encrypted (legal name, tax id, address); field-level access control.

### 7.3 project
- **Responsibilities:** job posting, edit/close, categories, required skills, budget type
  (fixed/hourly), visibility, moderation.
- **API:** `CreateProject`, `UpdateProject`, `PublishProject`, `CloseProject`, `GetProject`,
  `ListClientProjects`.
- **DB:** `projects`, `project_skills`, `project_categories`, `project_attachments`.
- **Produces:** `project.published`, `project.updated`, `project.closed`.
- **Consumes:** `fraud.project_flagged` (auto-hide).
- **Scaling:** writes modest; reads served from `search`/cache. HPA on RPS.

### 7.4 proposal
- **Responsibilities:** freelancer bids, cover letters, proposed rate/milestones, connects/
  screening questions, withdrawal, client shortlisting.
- **API:** `SubmitProposal`, `WithdrawProposal`, `ListProjectProposals`, `ShortlistProposal`.
- **DB:** `proposals`, `proposal_milestones`, `proposal_answers`.
- **Produces:** `proposal.submitted`, `proposal.shortlisted`, `proposal.withdrawn`.
- **Consumes:** `project.closed` (auto-decline open proposals).
- **Scaling:** write spikes on popular jobs; partition by `project_id`.

### 7.5 contract  *(lifecycle orchestrator)*
- **Responsibilities:** create from accepted proposal, fixed-price milestones & hourly terms,
  state machine, deliverable approval, weekly billing trigger, dispute initiation. Runs sagas.
- **API:** `CreateContract`, `ActivateContract`, `SubmitMilestone`, `ApproveMilestone`,
  `RequestRevision`, `EndContract`, `RaiseDispute`, `GetContract`.
- **DB:** `contracts`, `milestones`, `hourly_contracts`, `deliverables`, `contract_events`
  (sourced state machine), `disputes`.
- **Produces:** `contract.activated`, `milestone.approved`, `billing.week_ready`,
  `dispute.opened`, `contract.ended`.
- **Consumes:** `proposal.accepted`, `worksession.closed`, `escrow.released`,
  `payment.captured`.
- **Scaling:** moderate; the saga state lives in DB; idempotent handlers.
- **Security:** authorization that both parties match the contract; immutable event log.

### 7.6 timetracking  *(differentiator core)*
- **Responsibilities:** ingest activity samples, assemble 10-minute slices into `work_sessions`,
  compute activity %, idle handling, weekly timesheet aggregation, manual-time requests.
- **API:** `StartSession`, `Heartbeat`, `SubmitActivitySlice`, `StopSession`,
  `GetTimesheet`, `RequestManualTime`, `ApproveManualTime`.
- **DB:** `work_sessions`, `activity_logs` (partitioned by week), `time_slices`,
  `timesheets`, `manual_time_requests`.
- **Produces:** `worksession.closed`, `billing.week_ready`, `activity.recorded`.
- **Consumes:** `screenshot.ingested` (link screenshot to slice), `contract.activated`.
- **Scaling:** highest write volume (per active freelancer per ~10 min). Partition by time;
  batch inserts; Kafka buffering; HPA on consumer lag.
- **Security:** only contract parties + admin read; activity raw counts not exposed verbatim.

### 7.7 screenshot
- **Responsibilities:** issue presigned PUT URLs + wrapped DEKs, confirm ingestion, store
  metadata, decrypt-on-read for authorized viewers (issue short-TTL GET), enforce retention,
  re-hash for integrity.
- **API:** `RequestUploadSlot`, `ConfirmUpload`, `GetScreenshot` (authorized, returns
  signed URL + decryption material), `ListSessionScreenshots`, `DeleteForCompliance`.
- **DB:** `screenshots`, `screenshot_metadata` (sha256, perceptual hash, dims, DEK wrapped,
  flags). Blobs in S3.
- **Produces:** `screenshot.ingested`, `screenshot.integrity_failed`.
- **Consumes:** `contract.ended` (start retention clock), `gdpr.erasure_requested`.
- **Scaling:** thin control plane; data plane is S3. HPA on RPS; presign is CPU-cheap.
- **Security:** envelope encryption; per-view audit; bucket is private, KMS-gated, Object-Lock
  for legal holds.

### 7.8 payment
- **Responsibilities:** Stripe integration (Connect for freelancer payouts), charge clients,
  capture/refund, payout scheduling, webhook processing, ledger + reconciliation,
  chargeback handling.
- **API (REST + webhook):** `CreatePaymentIntent`, `ConfirmDeposit`, `RequestWithdrawal`,
  `Refund`, `StripeWebhook`, `GetLedger`.
- **DB:** `payments`, `transactions` (double-entry ledger), `withdrawals`, `payout_accounts`,
  `stripe_events` (idempotency), `reconciliation_runs`.
- **Produces:** `payment.captured`, `payout.paid`, `payment.failed`, `chargeback.received`.
- **Consumes:** `escrow.released`, `milestone.approved`, `billing.week_ready`.
- **Scaling:** modest RPS, strict correctness. Idempotency keys everywhere; webhooks queued.
- **Security:** PCI scope minimized (Stripe holds card data); secrets in Secrets Manager;
  signed-webhook verification; ledger is append-only.

### 7.9 escrow
- **Responsibilities:** hold client funds, allocate to milestones/weekly hours, release on
  approval, refund on dispute resolution, maintain escrow balances.
- **API:** `FundEscrow`, `AllocateMilestone`, `ReleaseMilestone`, `ReleaseHours`, `Refund`,
  `GetBalance`.
- **DB:** `escrow_accounts`, `escrow_allocations`, `escrow_ledger`.
- **Produces:** `escrow.funded`, `escrow.released`, `escrow.refunded`.
- **Consumes:** `payment.captured`, `milestone.approved`, `dispute.resolved`.
- **Scaling:** low volume, high correctness; single-writer per escrow account (row lock /
  serializable).
- **Security:** double-entry invariants enforced in DB constraints + reconciled nightly.

### 7.10 review
- Ratings (1–5 multi-dimension), written feedback, reputation score, mutual-review windows.
- **DB:** `reviews`, `review_responses`, `reputation_scores`.
- **Produces:** `review.published`. **Consumes:** `contract.ended` (open review window).
- Scaling: read-heavy; reputation recomputed async.

### 7.11 messaging
- Real-time chat (WebSocket), conversations, attachments (S3), typing indicators, read
  receipts, presence. Persistence in Postgres + hot path in Redis pub/sub.
- **DB:** `conversations`, `conversation_participants`, `messages`, `message_attachments`,
  `message_receipts`.
- **Produces:** `message.sent`. **Consumes:** `contract.activated` (open conversation).
- Scaling: sticky WS via NLB; horizontal WS gateways; Redis pub/sub fan-out; presence TTL keys.
- See [`MESSAGING.md`](./MESSAGING.md).

### 7.12 notification
- Multi-channel fan-out: email (SES), push (FCM/APNs), SMS (Twilio), in-app. Templating,
  user preferences, digest batching, delivery tracking.
- **DB:** `notifications`, `notification_preferences`, `delivery_attempts`.
- **Consumes:** nearly all event topics. **Produces:** none (terminal consumer).
- Scaling: consumer groups per channel; rate-limited per provider; retry + DLQ.

### 7.13 search
- Elasticsearch/OpenSearch projection of projects, freelancers, skills. Query API with
  filters, facets, geo, ranking, recommendations.
- **Stores:** OpenSearch indices (no owned Postgres). **Consumes:** `project.events`,
  `user.events`, `review.published`.
- Scaling: index per type with rollover; query nodes scale on QPS. See [`SEARCH.md`](./SEARCH.md).

### 7.14 fraud
- Risk scoring & anomaly detection across screenshots, activity, payments, accounts.
  Feature store + rules engine + ML model serving. Opens investigation cases.
- **DB:** `fraud_cases`, `fraud_signals`, `risk_scores`, `feature_snapshots`.
- **Consumes:** virtually all events. **Produces:** `fraud.case_opened`, `screenshot.flagged`,
  `account.risk_changed`.
- Scaling: stream processing (Kafka Streams/Flink optional); model inference autoscaled.
  See [`FRAUD.md`](./FRAUD.md).

### 7.15 admin
- Back-office: user/contract/payment/dispute management, screenshot audit, fraud review,
  RBAC-gated actions, audit trail viewer.
- **DB:** owns `admin_actions`; reads others via gRPC (never direct SQL).
- **Produces:** `admin.action_logged`, `dispute.resolved`, `account.suspended`.
- Security: strict RBAC + step-up MFA for sensitive actions; every action audited.

### 7.16 analytics
- OLAP rollups, business + ops metrics, scheduled reports, data exports. Consumes the firehose,
  writes to a columnar store (Redshift/ClickHouse) + serves dashboards.
- **Consumes:** all topics (analytics sink). **Produces:** none.
- Scaling: batch + streaming; isolated from OLTP.

---

## 8. Scalability strategy

```
 Tier              Technique
 ────              ─────────
 Stateless svcs    K8s HPA (CPU + custom metrics: RPS, Kafka consumer lag) 3→N replicas;
                   PodDisruptionBudgets; Cluster Autoscaler / Karpenter for nodes.
 PostgreSQL        Vertical first; then read replicas for read-heavy svcs (user, project);
                   partitioning for high-volume tables (activity_logs, screenshots, audit_logs,
                   messages) by time; Citus/sharding path documented for timetracking if a
                   single primary saturates. PgBouncer for connection pooling.
 Redis             Cluster mode (sharded) for cache + presence + rate limits; separate
                   instances per concern to isolate eviction.
 Kafka             Partition counts sized for peak consumer parallelism; key by aggregate;
                   tiered storage for long-retention topics.
 Search            OpenSearch with index rollover, dedicated master/data/coordinating nodes.
 Object storage    S3 scales infinitely; CloudFront for read distribution; lifecycle to IA/Glacier.
 Hot paths         Screenshot blobs never traverse services (client↔S3 direct).
```

**Capacity sketch (peak):** 100k concurrent users, assume 10% generate API load at ~2.5 req/s
each → ~25k req/s. At ~1.5k req/s/replica that's ~17 gateway replicas + service fan-out.
Active hourly contracts ≈ 50k → ~3.3 screenshot ingests/s and ~3.3 activity slices/s steady,
spiking at quarter-hour boundaries (handled by Kafka buffering + jittered capture scheduling).

---

## 9. Multi-region deployment

```
        Route53 (latency-based + health checks + geo for data residency)
                 │
        ┌────────┴─────────┬───────────────────┐
        ▼                  ▼                   ▼
   us-east-1 (PRIMARY)  eu-west-1 (ACTIVE)  ap-southeast-1 (ACTIVE)
   ───────────────────  ─────────────────   ────────────────────
   EKS + services       EKS + services       EKS + services
   RDS primary  ───────►RDS read replica     RDS read replica
   (writes)     async   (+ regional writer    (...)
                replica  for EU residency)
   MSK            ◄────► MirrorMaker2 ◄─────► MSK
   ElastiCache (regional)                     ElastiCache (regional)
   S3 (CRR replication of screenshots + backups across regions)
```

**Strategy**
- **Active-active for stateless reads**, **primary-region writes** for globally-consistent
  data (payments, escrow, auth tokens) to avoid multi-master money bugs. Money mutations route
  to the primary region; reads served locally.
- **Data residency:** EU users' PII + screenshots pinned to `eu-west-1` (separate RDS/S3 with
  region-locked KMS keys) to satisfy GDPR; routing by user residency claim in JWT.
- **Event replication:** MSK MirrorMaker2 replicates cross-region topics where global views are
  needed (search, analytics).
- **CDN:** CloudFront fronts web + static + signed screenshot downloads globally.
- **Failover:** Route53 health checks shift traffic; RDS cross-region replica promoted on
  primary loss (see §11).

---

## 10. Cost optimization

| Lever | Action |
|-------|--------|
| Compute | Karpenter + Spot for stateless/batch (fraud ML, analytics, async consumers); On-Demand/Reserved for stateful & latency-critical. Right-size via VPA recommendations. |
| Storage | S3 lifecycle: screenshots → Standard 30d → IA 60d → Glacier IR/Deep Archive after retention; Intelligent-Tiering for unknown access. |
| Screenshots | WebP compression (≈70% smaller than PNG); dedupe identical captures via perceptual hash; only store metadata when activity is 0 (idle slices skip blob). |
| Data transfer | Client↔S3 direct upload avoids service egress; CloudFront caching; VPC endpoints for S3/Kafka to avoid NAT cost. |
| Databases | Read replicas only where measured; Graviton (ARM) RDS/EKS nodes; pause non-prod off-hours. |
| Kafka | Tiered storage to S3 for long-retention topics instead of large broker disks. |
| Observability | Loki (cheap log storage) over hot indexing; sampled tracing (1–10%); metric cardinality budgets. |
| Egress for payouts | Batch Stripe payouts weekly to reduce per-transaction overhead. |

Target unit economics tracked in `analytics`: cost per active contract-hour, cost per stored
screenshot-GB-month, infra cost per 1k API requests.

---

## 11. Disaster recovery

| Tier | RPO | RTO | Mechanism |
|------|-----|-----|-----------|
| Auth/Payment/Escrow (Tier-0) | ≤ 1 min | ≤ 15 min | RDS Multi-AZ + cross-region read replica (promotable); PITR; idempotent replays from Kafka. |
| Core marketplace (Tier-1) | ≤ 5 min | ≤ 1 hr | Multi-AZ RDS, automated snapshots, GitOps redeploy to standby region. |
| Screenshots/blobs (Tier-2) | ≤ 15 min | ≤ 4 hr | S3 CRR (cross-region replication) + versioning + Object Lock. |
| Analytics (Tier-3) | ≤ 24 hr | ≤ 24 hr | Rebuildable from event log; daily snapshots. |

**Practices**
- **Everything is code:** Terraform + GitOps (Argo CD) means a region can be rebuilt from the
  repo + data restore.
- **Backups:** RDS automated backups + PITR (35d), cross-region copy; S3 versioning + CRR;
  Kafka topics with sufficient retention to replay derived state.
- **Game days:** quarterly region-failover and restore drills; backup-restore verification
  automated weekly.
- **Runbooks:** in `docs/runbooks/` (DB failover, region failover, Kafka lag storm, Stripe
  outage, KMS key compromise/rotation).

---

## 12. Technology choices — rationale (ADR summary)

| Decision | Choice | Why |
|----------|--------|-----|
| Backend language | Go | Concurrency, low-latency, small images, strong gRPC ecosystem. |
| Service comms | gRPC internal, REST/WS edge | Typed contracts + perf inside; ergonomics + caching outside. |
| Event bus | Kafka (MSK) | Durable, ordered, replayable; mature ecosystem; tiered storage. |
| Primary DB | PostgreSQL | Relational integrity for money + partitioning + JSONB flexibility. |
| Cache/presence | Redis | Sub-ms cache, pub/sub, token-bucket rate limiting, presence TTLs. |
| Search | OpenSearch | Rich querying, facets, ranking, geo for discovery. |
| Desktop tracker | Tauri + Rust | Tiny footprint, native OS APIs (screen/input), memory safety, hard-to-tamper. |
| Frontend | Next.js (App Router) | SSR/SEO for marketplace, BFF for sessions, React ecosystem. |
| Infra | EKS + Terraform | Portable orchestration, IaC reproducibility, mature AWS integration. |
| Payments | Stripe Connect | Marketplace payouts, escrow-friendly, Radar fraud, global coverage. |

Individual ADRs live in `docs/adr/NNNN-title.md`.
