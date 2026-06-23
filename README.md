# Aizorix — Global Freelancer Marketplace

[![ci-backend](https://github.com/winner-dev123/Aizorix-Freelancer/actions/workflows/ci-backend.yml/badge.svg)](https://github.com/winner-dev123/Aizorix-Freelancer/actions/workflows/ci-backend.yml)
[![ci-frontend](https://github.com/winner-dev123/Aizorix-Freelancer/actions/workflows/ci-frontend.yml/badge.svg)](https://github.com/winner-dev123/Aizorix-Freelancer/actions/workflows/ci-frontend.yml)
[![ci-tracker](https://github.com/winner-dev123/Aizorix-Freelancer/actions/workflows/ci-tracker.yml/badge.svg)](https://github.com/winner-dev123/Aizorix-Freelancer/actions/workflows/ci-tracker.yml)
[![ci-integration](https://github.com/winner-dev123/Aizorix-Freelancer/actions/workflows/ci-integration.yml/badge.svg)](https://github.com/winner-dev123/Aizorix-Freelancer/actions/workflows/ci-integration.yml)
[![security](https://github.com/winner-dev123/Aizorix-Freelancer/actions/workflows/security.yml/badge.svg)](https://github.com/winner-dev123/Aizorix-Freelancer/actions/workflows/security.yml)
[![terraform](https://github.com/winner-dev123/Aizorix-Freelancer/actions/workflows/terraform.yml/badge.svg)](https://github.com/winner-dev123/Aizorix-Freelancer/actions/workflows/terraform.yml)

A production-grade, event-driven freelancer marketplace (Upwork-class) with a unique
**verified hourly work** differentiator: a Tauri/Rust desktop tracker that captures
encrypted screenshots every 15 minutes, measures real activity, and feeds an escrow-backed
billing pipeline with fraud detection.

> **Status:** Runs end-to-end. 21 Go modules build/vet/test clean; the full stack has been
> driven live against real infrastructure (Postgres, Redis, MinIO/S3, Redpanda/Kafka) — identity,
> verified-hourly-work → escrow payout, the encrypted screenshot pipeline, the event backbone,
> and the browser UI clicking through to the database. Running it surfaced **9 real bugs**, and
> **three adversarial review waves** (the HTTP services' authorization, then the under-reviewed
> surface — desktop tracker, web, cryptography, IaC, event-correctness, schema/ledger) found **45
> more**: **54 found / 53 fixed** (1 documented), **10 critical** (money-safety, privilege
> escalation, cross-tenant data access, auth, data-integrity). The highest-stakes ones are pinned by
> regression tests; each was in code that compiled and passed its unit tests.
>
> **All 8 CI workflows are green** — build/vet/golangci-lint v2/test-race/govulncheck across 21 Go
> modules, CodeQL, gitleaks, integration tests against real Postgres, the Next.js build, all 16
> service container images, `terraform validate`, and the Tauri tracker (clippy+test on
> Linux/macOS/Windows).
>
> - **See it:** [`DEMO.md`](./DEMO.md) — visual walkthrough with real screenshots.
> - **Run it:** `make demo` (one command; brings up infra + services + web + smoke + browser test),
>   `make demo-down` to tear down. Demo login: `ada@aizorix.dev` / `DemoPassw0rd!`.
> - **What was proven + the bugs found:** [`docs/RUN_LOG.md`](./docs/RUN_LOG.md).
> - **Design & plan:** [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md) · [`ROADMAP.md`](./ROADMAP.md).

## Repository layout

```
.
├── api/                 # Versioned contracts — single source of truth
│   ├── proto/           #   gRPC service + event schemas (buf-managed)
│   └── openapi/         #   Public REST surface (API Gateway / BFF)
├── db/migrations/       # PostgreSQL DDL (golang-migrate, forward + down)
├── desktop-tracker/     # Tauri + Rust cross-platform time tracker
├── docs/                # Architecture, security, compliance, ADRs
├── infra/
│   ├── terraform/       # AWS: VPC, EKS, RDS, ElastiCache, MSK, S3, CloudFront, IAM
│   ├── k8s/             # Helm/Kustomize manifests per service
│   └── observability/   # Prometheus, Grafana, Loki, Alertmanager, SLOs
├── services/
│   ├── pkg/             # Shared Go platform libraries (the "paved road")
│   └── <service>/       # One Go module per bounded context
├── web/                 # Next.js 14 (App Router) + TS + Tailwind + React Query
└── .github/workflows/   # CI/CD pipelines
```

## Bounded contexts (services)

| Service        | Owns                                   | Sync API | Key events produced |
|----------------|----------------------------------------|----------|---------------------|
| `auth`         | credentials, JWT, MFA, sessions, OAuth | gRPC+REST| `user.registered`, `session.created` |
| `user`         | profiles, roles, permissions, KYC      | gRPC     | `user.profile_updated` |
| `project`      | job posts, categories, skills          | gRPC     | `project.published` |
| `proposal`     | bids, cover letters, screening         | gRPC     | `proposal.submitted` |
| `contract`     | fixed-price + hourly lifecycle, milestones | gRPC | `contract.activated`, `milestone.approved` |
| `timetracking` | work sessions, activity, weekly billing| gRPC     | `worksession.closed`, `billing.week_ready` |
| `screenshot`   | ingest, decrypt-on-read, retention     | gRPC     | `screenshot.ingested`, `screenshot.flagged` |
| `payment`      | Stripe, charges, payouts, reconciliation | REST   | `payment.captured`, `payout.paid` |
| `escrow`       | fund holds, milestone/hour releases    | gRPC     | `escrow.funded`, `escrow.released` |
| `review`       | ratings, feedback, reputation          | gRPC     | `review.published` |
| `messaging`    | real-time chat, presence, files        | WS+gRPC  | `message.sent` |
| `notification` | fan-out email/push/in-app              | gRPC     | — (consumer) |
| `search`       | Elasticsearch projection + query       | REST     | — (consumer) |
| `fraud`        | risk scoring, anomaly detection        | gRPC     | `fraud.case_opened` |
| `admin`        | back-office, audits, dispute ops       | REST     | `admin.action_logged` |
| `analytics`    | OLAP rollups, reporting                 | gRPC     | — (consumer) |

## Platform & edge services

Beyond the bounded contexts, the platform runs these supporting services (the keystones that
make the 17 services behave as one system):

| Service           | Role |
|-------------------|------|
| `gateway`         | Single public entry point: JWT verification (local, via JWKS — no per-request auth call), identity-header injection (`X-User-Id`/`X-Permissions`/`X-Roles`, with client spoofing stripped), Redis rate limiting, reverse-proxy routing, Stripe webhook passthrough. |
| `relay`           | Transactional-outbox publisher: polls each service DB's `outbox` (`FOR UPDATE SKIP LOCKED`) and publishes to Kafka with an `event-id` dedupe header. One deployment per service database. |
| `wsgateway`       | Real-time messaging + presence over WebSocket, fanned out across replicas via Redis pub/sub. |
| `<svc>/cmd/consumer` | Per-service Kafka consumers (notification fan-out, search indexing, analytics ingest) — idempotent via the `processed_events` table. |

The event backbone is wired end to end: a state change and its event commit together via the
**outbox** (`pkg/outbox`); the **relay** publishes through the **Kafka producer** (`pkg/kafka`);
**idempotent consumers** (`pkg/kafka` consumer group + dedupe) react. No dual-write, at-least-once
delivery, exactly-once *effect*.

## Quick start (local development)

```bash
# 1. Bring up the backing services (Postgres, Redis, Redpanda/Kafka, MinIO/S3, Elasticsearch)
make dev-up

# 2. Apply database migrations
make migrate-up

# 3. Generate code from contracts (protobuf, sqlc)
make generate

# 4. Run a service (example: auth)
make run SERVICE=auth

# 5. Run the web app
cd web && pnpm install && pnpm dev

# 6. Run the desktop tracker (requires Rust + Tauri prerequisites)
cd desktop-tracker && pnpm install && pnpm tauri dev
```

## Engineering standards

- **Contracts first.** Nothing ships without a proto/OpenAPI change reviewed in `api/`.
- **One database per service.** No cross-service table access; integrate via gRPC or events.
- **Outbox pattern** for every state change that emits an event (no dual-write).
- **Migrations are forward-only in prod**, expand/contract for zero-downtime deploys.
- **All PII and screenshots are encrypted** (envelope encryption via KMS); see `docs/SECURITY.md`.
- **Every privileged action is audited** to an append-only `audit_logs` partitioned table.

See [`ROADMAP.md`](./ROADMAP.md) for what is implemented vs. stubbed.
