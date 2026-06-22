# Local development — end to end

This is the fastest path from a fresh clone to a running, seeded, smoke-tested
Aizorix stack. It assumes Docker, Go 1.22+, and `golang-migrate` (`migrate`) are
installed.

```
clone ─▶ infra up ─▶ migrate ─▶ seed ─▶ services up ─▶ smoke
```

## 0. Clone & configure

```bash
git clone <repo> && cd "Aizorix&Freelancer"
cp .env.example .env          # dev defaults are fine for local
export DATABASE_URL="postgres://aizorix:aizorix_dev@localhost:5432/aizorix?sslmode=disable"
```

## 1. Infra up

Starts Postgres, Redis, Redpanda, MinIO and Elasticsearch (compose project
`aizorix-dev`, network `aizorix-dev_default`):

```bash
docker compose up -d
# or: make dev-up
```

## 2. Migrate

Apply the full schema (`db/migrations`):

```bash
make migrate-up
# (equivalently)  migrate -path db/migrations -database "$DATABASE_URL" up
```

## 3. Seed demo data

Inserts a coherent demo dataset — 1 admin, 2 clients, 3 freelancers (with
hashed, login-ready passwords), skills, 2 projects (fixed + hourly), proposals,
2 contracts (one hourly + active, one fixed with milestones), a funded escrow
account, and a few notifications. **Idempotent** — safe to re-run.

```bash
make seed
# (equivalently)  cd services/tools && GOWORK=off go run ./cmd/seed
```

It prints the created IDs and the demo login credentials. They are also listed
below.

### Demo login credentials

All accounts share one password (dev only):

```
password: DemoPassw0rd!
```

| Role       | Email                |
|------------|----------------------|
| admin      | admin@aizorix.dev    |
| client     | acme@aizorix.dev     |
| client     | globex@aizorix.dev   |
| freelancer | ada@aizorix.dev      |
| freelancer | linus@aizorix.dev    |
| freelancer | grace@aizorix.dev    |

## 4. Services up

Builds and runs the app tier (gateway on **:8080**, backing services on
`8081`–`8090`) on the infra network:

```bash
make services-up
# or:  docker compose -f deploy/docker-compose.services.yml up --build
```

See [`deploy/README.md`](../deploy/README.md) for the full port map and how to
enable the commented-out services (review, messaging, notification, search,
fraud, admin, analytics).

## 5. Smoke test

Hits the gateway: health → register → login → authenticated `/v1/auth/me`,
printing PASS/FAIL per step (health is retried while the stack warms up).

```bash
make smoke                       # bash
# or directly:
bash scripts/smoke.sh            # GATEWAY_URL overridable
pwsh ./scripts/smoke.ps1         # PowerShell variant
```

## Tear down

```bash
make services-down               # stop the app tier
make dev-down                    # stop infra (make dev-reset wipes volumes)
```

## Notes

- **`go.work` is owned by the orchestrator.** The seed tool (`services/tools`)
  and the gateway build with `GOWORK=off` via their own `replace` directives, so
  they compile standalone without being listed in the workspace.
- The seed tool only needs `DATABASE_URL`; it does not require any service or
  Docker container to be running — just a migrated database.
- Dev ergonomics: `.golangci.yml` (lint config) and `sqlc.yaml` (+ example
  queries in `db/queries/`) live at the repo root. Run `make lint` / `make sqlc`.
```
