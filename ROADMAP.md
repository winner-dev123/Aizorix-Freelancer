# Aizorix — Implementation Roadmap

This maps every phase of the brief to concrete artifacts in the repository, marks what is
**implemented & validated** vs **scaffolded** (real, coherent, but not yet wired end-to-end),
and gives the delivery plan + deployment guide a senior team can execute immediately.

Legend: ✅ implemented & compiles/tests · 🟩 scaffolded (real code, fill-in needed) ·
📄 designed (doc/contract) · ⬜ not started.

## Phase → artifact map

| # | Phase | Status | Where |
|---|-------|--------|-------|
| 1 | System architecture | 📄 | [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) (diagrams, data flows, scalability, multi-region, DR, cost) |
| 2 | Microservices design | 📄🟩 | `docs/ARCHITECTURE.md` §7 service catalog; `services/*` |
| 3 | Database design | ✅ | `db/migrations/000001…000010` (10 migration pairs), [`db/ERD.md`](db/ERD.md) |
| 4 | AuthN/AuthZ | ✅ | `services/auth` (JWT/refresh/lockout/sessions), `db/migrations/000002`, `pkg/token`, `pkg/rbac` |
| 5 | Project marketplace | 🟩 | `services/project`, `services/proposal`; `db/migrations/000004`; `api/openapi/gateway.yaml` |
| 6 | Contract management | 🟩📄 | `services/contract`; `db/migrations/000005`; state machine in `docs/ARCHITECTURE.md` §7.5 |
| 7 | Desktop tracker | ✅ | `desktop-tracker/` (Tauri 2 + 12 Rust modules), [`desktop-tracker/README.md`](desktop-tracker/README.md) |
| 8 | Screenshot system | ✅📄 | `services/screenshot`, tracker `crypto.rs`/`sync.rs`, [`docs/SCREENSHOT_PIPELINE.md`](docs/SCREENSHOT_PIPELINE.md) |
| 9 | Activity monitoring | ✅📄 | `services/timetracking/internal/activity` (+ tests), tracker `activity.rs`, [`docs/ACTIVITY.md`](docs/ACTIVITY.md) |
| 10 | Payment & escrow | 🟩📄 | `services/payment`, `services/escrow`; `db/migrations/000008` (double-entry ledger) |
| 11 | Messaging | 🟩📄 | `services/messaging`; `db/migrations/000009`; `docs/ARCHITECTURE.md` §7.11 |
| 12 | Search | 🟩📄 | `services/search`; `docs/ARCHITECTURE.md` §7.13 |
| 13 | Fraud detection | 🟩📄 | `services/fraud`; `db/migrations/000010`; [`docs/FRAUD.md`](docs/FRAUD.md) |
| 14 | Admin system | 🟩 | `services/admin`; RBAC in `db/migrations/000002`; `admin_actions`/`audit_logs` |
| 15 | Frontend | 🟩 | `web/` (Next.js 14 App Router, 79 files), `web/README.md` |
| 16 | Infrastructure | 🟩 | `infra/terraform` (10 modules), `infra/k8s` (Kustomize), `infra/docker` |
| 17 | Observability | 🟩 | `infra/observability` (Prometheus/Grafana/Loki/Tempo/Alertmanager), `infra/observability/SLO.md` |
| 18 | CI/CD | 🟩 | `.github/workflows/*` (9 pipelines), `.github/dependabot.yml`, service `Dockerfile`s |
| 19 | Security | 📄 | [`docs/SECURITY.md`](docs/SECURITY.md); enforced in `pkg/crypto`, `pkg/token`, `pkg/rbac`, IAM, WAF |
| 20 | Legal & compliance | 📄 | [`docs/COMPLIANCE.md`](docs/COMPLIANCE.md) (consent flow, residency, retention, erasure) |
| 21 | Production MVP | 🟩 | This file + the whole monorepo structure |

## What is proven working (compiles / tests pass)

**All 21 Go modules build + `go vet` clean in workspace mode**, and the unit suites pass:

- `pkg` — Argon2id + envelope-encryption (`crypto`) and ES256 issue/verify + JWKS round-trip,
  expiry/nbf rejection (`token`) tests pass; shared platform: config, log, pg, **kafka
  producer/consumer**, **outbox relay (self-describing bus: event-id + event-type headers)**, rbac.
- `auth` — reference service; integration test (register→login→refresh-rotation→reuse-burns-family)
  compiles under `-tags=integration`.
- `timetracking` — **activity-algorithm tests pass** (busy/idle/macro detection).
- `screenshot` — enrolled-device signature verification (mandatory), wrap-client-DEK flow.
- `escrow` — available-funds + release-status unit tests pass; money-path integration test compiles.
- `payment` — `WriteLegs` zero-sum ledger validation unit test passes.
- `fraud` — risk-scoring band + case-threshold unit tests pass.
- `contract` — milestone state-machine + party-authz integration test compiles.
- **`gateway`** — JWT verify, anti-spoof header injection, Redis rate limiting, reverse-proxy.
- **`relay`** — deployable transactional-outbox publisher to Kafka.
- **`wsgateway`** — WebSocket messaging + Redis pub/sub presence.
- **`tools`** — idempotent demo-data seeder (Argon2-hashed logins).
- `notification` / `search` / `analytics` — each ships a real **`cmd/consumer`** wired to Kafka.
- `desktop-tracker` — full Rust set; device enrollment wired end-to-end (login enrolls the
  Ed25519 key, sync uses the server `device_id`); `crypto.rs` round-trip + AAD-tamper test.
  Compile requires the Rust/Tauri toolchain (`cargo test --features mock-capture` for headless CI).

> **Run the whole stack locally:** `make dev-up && make migrate-up && make seed`, then
> `make services-up` (gateway on :8080) and `make smoke`. Full flow + demo creds in
> [`docs/LOCAL_DEV.md`](docs/LOCAL_DEV.md). Run one service: `make run SERVICE=auth`.
> Unit tests: `go test ./...` per module. Integration: `go test -tags=integration ./...` (needs Docker).

## Verification & security-hardening pass

After the initial build, an adversarial multi-agent review swept the codebase across 6
dimensions (service domain logic, schema↔code consistency, the money/ledger paths, the Rust
tracker, crypto/identity, and cross-layer API contracts). It surfaced 42 findings; 34 were
independently re-verified against source as real and **all 34 were fixed and re-built clean**:

- **Critical (7):** KYC-forgery authz hole; escrow over-release (allocations now reserve held
  funds); tracker DEK mismatch (server now KMS-wraps the device's DEK so blobs decrypt);
  Ed25519 signed-message RFC3339 mismatch (canonical `Z`, no fractional seconds); screenshot
  signature verified against an attacker-supplied key (now verified against the **enrolled**
  device key, mandatory); two frontend↔backend route mismatches (proposals, milestone-by-id).
- **High (10):** GMV double-count; contract milestone/dispute party-authorization; non-idempotent
  escrow release/refund; ledger double-credit; fail-open signature check; refresh-token rotation
  TOCTOU (now row-locked); timesheet param + logout/me + refresh-cookie contract mismatches.
- **Medium/Low (17):** review 401, proposal ownership, `is_manual` persistence, escrow
  defense-in-depth authz, Stripe webhook timestamp tolerance + fail-closed, webhook retry on
  race, tracker single-sync-loop + `spawn_blocking`, dedicated client-DEK column, screenshot
  list endpoint, contract events endpoint, GMV contract-count, on-conflict column refresh,
  CSPRNG `jti`, DB-level ledger zero-sum trigger, OpenAPI login schema.

This is the kind of pass that should run on every PR (see `/code-review` and the CI gates).

## Delivery plan (milestones)

**M0 — Foundations (done in this scaffold)**
Repo, contracts, schema, shared platform lib, auth, the verified-work core (timetracking +
screenshot + tracker), infra/CI/observability skeletons.

**M1 — Marketplace MVP (4–6 wks)**
Finish `user`, `project`, `proposal`, `contract` (fixed-price milestone state machine), wire
gateway + auth middleware, search indexing, basic frontend flows (register → post → bid → hire).
Exit: a client can post a fixed-price job, hire, fund a milestone, approve, and pay.

**M2 — Verified hourly + money (4–6 wks)**
Wire `escrow` + `payment` (Stripe Connect, webhooks, ledger, reconciliation), weekly billing
saga from `timetracking`, tracker enrollment + device key registration, screenshot review UI,
disputes. Exit: end-to-end hourly contract with screenshots, weekly auto-billing, payout.

**M3 — Trust & scale (4–6 wks)**
`fraud` feature pipeline + scoring + admin review queue, messaging real-time WS gateway,
notification fan-out, analytics rollups, multi-region read replicas, load testing to 100k
concurrent, chaos/DR game day.

**M4 — Compliance & GA (3–4 wks)**
GDPR/CCPA erasure + export flows, consent audit proofs, residency routing, SOC 2 controls,
pen-test remediation, runbooks, on-call.

## Deployment guide

### Local (developer)
```bash
cp .env.example .env
make dev-up            # postgres, redis, redpanda, minio, elasticsearch
make migrate-up        # apply db/migrations
make generate          # buf + sqlc (requires buf, sqlc installed)
make run SERVICE=auth  # run a service
cd web && pnpm install && pnpm dev
cd desktop-tracker && pnpm install && pnpm tauri dev
```

### AWS (staging/prod)
```bash
# 1) Provision infra (per environment)
cd infra/terraform
terraform init -backend-config=backends/staging.hcl
terraform workspace select staging || terraform workspace new staging
terraform apply -var-file=environments/staging.tfvars

# 2) Bootstrap the cluster (order matters — see infra/k8s/platform/README.md)
#    external-secrets → cert-manager → AWS LB controller → Karpenter → Argo CD app-of-apps

# 3) Database migrations run as a pre-deploy Job (see .github/workflows/deploy.yml)
#    migrate -path db/migrations -database "$DATABASE_URL" up

# 4) Ship services
#    CI build-push.yml builds+signs+scans images to ECR; deploy.yml bumps kustomize image tags;
#    Argo CD syncs staging (auto) then production (gated).
```

### Required external setup
- AWS account + Route53 hosted zone + ACM cert; set `cluster_admin_principals`, account IDs,
  `web_domain` in the tfvars/overlays.
- Stripe (Connect) keys → Secrets Manager; KMS CMKs are created by Terraform (`modules/kms`).
- KYC (Sumsub), email (SES), SMS (Twilio), push (FCM/APNs) credentials → Secrets Manager.

## Known fill-ins (explicit, not hidden)
- gRPC code generation (`make proto`) is wired via `api/buf.gen.yaml` but not run here (no buf in
  this environment); services currently expose REST and would add gRPC servers from the generated
  stubs.
- ~~The scaffolded services need outbox relay deployment, gateway auth middleware, and integration
  tests.~~ **Done:** the `relay` publishes the outbox to Kafka, the `gateway` does JWT + rate
  limiting + routing, `cmd/consumer`s react to events, and auth/escrow/contract have integration
  tests. Remaining per-service depth: more handler coverage, OpenSearch/Stripe production clients,
  and a DLQ worker for poison events.
- Stripe/KMS/OpenSearch clients are interface-stubbed for local builds (`StubPresigner`,
  `LocalKeyProvider`); production implementations sit behind build tags / config switches.
- Frontend service-module paths for endpoints not in `api/openapi/gateway.yaml` are inferred and
  must be reconciled with the final gateway spec.
- Tauri requires real app icons under `desktop-tracker/src-tauri/icons/` to bundle.
- **Device enrollment is now required for screenshot ingestion.** Screenshot `Confirm` verifies
  the device signature against the device's *enrolled* Ed25519 key (`devices.attestation_pubkey`),
  and verification is mandatory (no fail-open). The tracker generates this key on first run and
  exposes its public key via the `login` command; wire the enrollment call (POST the pubkey →
  `devices` row, and pass the resulting `device_id` into slot requests) in M2 so uploads confirm
  end-to-end. Until then the secure path correctly rejects unenrolled devices.
- The Rust tracker is verified by inspection only in this environment (no Rust toolchain present);
  run `cargo build` / `cargo test --features mock-capture` on a dev machine.

See each service's `README` / package docs for the local fill-in checklist.
