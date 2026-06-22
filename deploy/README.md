# Local full-stack deployment

This directory holds the **application tier** compose file. It builds and runs the
Go microservices and attaches them to the dev **infra** network created by the
root `docker-compose.yml` (Postgres, Redis, Redpanda, MinIO, Elasticsearch).

## Prerequisites

- Docker + Docker Compose v2 (`docker compose ...`)
- The infra stack running (it owns the database and the shared network)

## 1. Bring up the infra

From the repo root:

```bash
docker compose up -d
```

This starts Postgres, Redis, Redpanda, MinIO and Elasticsearch under the compose
project `aizorix-dev`, which creates the network **`aizorix-dev_default`**. The
services compose file (`deploy/docker-compose.services.yml`) joins that network
as `external`, so the app containers can reach `postgres:5432`, `redis:6379`,
`redpanda:9092`, `minio:9000` by DNS name.

> If your infra runs under a different compose project name, the network will be
> `<project>_default`. Update the `networks.default.name` at the bottom of
> `docker-compose.services.yml` to match.

## 2. Apply migrations

```bash
make migrate-up DATABASE_URL="postgres://aizorix:aizorix_dev@localhost:5432/aizorix?sslmode=disable"
```

## 3. Seed demo data

```bash
make seed DATABASE_URL="postgres://aizorix:aizorix_dev@localhost:5432/aizorix?sslmode=disable"
```

Prints the created entity IDs and the demo login credentials (all accounts share
the password `DemoPassw0rd!`).

## 4. Bring up the application services

```bash
docker compose -f deploy/docker-compose.services.yml up --build
# or: make services-up   (detached)
```

The **gateway** is published on host port **8080**. Each backing service is also
published on `808x` for direct debugging (auth 8081, user 8082, project 8083,
proposal 8084, contract 8085, timetracking 8086, screenshot 8087, payment 8088,
escrow 8089, relay 8090). Every service exposes `GET /healthz`.

The compose file builds eleven services explicitly (gateway, auth, user, project,
proposal, contract, timetracking, screenshot, payment, escrow, relay). The
remaining services (review, messaging, notification, search, fraud, admin,
analytics) follow the identical pattern and are included **commented out** — just
uncomment the block to enable them.

## 5. Smoke test

```bash
make smoke
# or directly:
bash   scripts/smoke.sh    GATEWAY_URL=http://localhost:8080
pwsh   scripts/smoke.ps1   # PowerShell variant
```

## Tear down

```bash
make services-down          # app tier only
docker compose down         # infra (add -v to wipe data volumes)
```
