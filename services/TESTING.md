# Testing

The backend is a Go workspace of independently-versioned modules (see `go.work`).
Tests come in two flavors with very different runtime requirements.

## Unit tests (no external dependencies — always run)

Pure, hermetic, deterministic tests. No Docker, no Postgres, no network.

```sh
# From a module directory (e.g. services/escrow), bypass the workspace so each
# module resolves against its own go.mod exactly as CI would:
GOWORK=off go test ./...

# Or run everything from the repo root via the workspace:
go test ./...            # default build excludes integration-tagged files
make test                # all modules, with the race detector
```

On PowerShell:

```powershell
$env:GOWORK="off"; go test ./...
```

What is covered today:

| Module    | Package                       | What it pins |
|-----------|-------------------------------|--------------|
| `pkg`     | `token`                       | JWKS round-trip, expired/`nbf`/`exp` rejection, `WithExpirationRequired` |
| `fraud`   | `internal/service`            | weighted-score clamp, band thresholds (low/medium/high/critical), case-opens-past-threshold |
| `escrow`  | `internal/service`            | `available = held − Σ(held allocations)`, reservation guard, `statusAfterRelease` |
| `payment` | `internal/store`              | ledger `WriteLegs` zero-sum validation (balanced accepted, unbalanced rejected before any write) |

## Integration tests (need Docker — opt in with a build tag)

Integration tests spin up a throwaway PostgreSQL container via
[`testcontainers-go`](https://golang.testcontainers.org/), apply **all**
`db/migrations/*.up.sql` in order, and exercise the real service + store against it.
They live behind the `//go:build integration` tag so the **default** `go test` never
requires Docker. Each test calls `t.Skip` if a container runtime is unavailable.

```sh
# Requires a running Docker daemon:
GOWORK=off go test -tags=integration ./...
```

If Docker is not running you can still confirm they **compile**:

```sh
GOWORK=off go vet -tags=integration ./...
```

PowerShell equivalents:

```powershell
$env:GOWORK="off"; go test -tags=integration ./...   # needs Docker
$env:GOWORK="off"; go vet  -tags=integration ./...    # compile-only check
```

What is covered today:

| Module     | Scenario |
|------------|----------|
| `auth`     | register → login → refresh (rotation issues a new refresh token) → reuse the old token (`ErrTokenReuse`, family revoked) → subsequent refresh fails; duplicate-email guard |
| `escrow`   | fund(100) → allocate(m1,100) → reserved funds protected from refund/hourly over-release → release once → idempotent re-release moves no money; idempotent re-allocation |
| `contract` | create-from-proposal → activate → fund/submit/approve milestone state machine happy path; non-party caller rejected with `ErrForbidden`; invalid state-machine transition rejected |

The shared container/migration helper lives in each service's
`internal/itest/pg.go` (also under the `integration` tag): `itest.NewPostgres(t)`
returns a migrated `*pgxpool.Pool` and registers teardown via `t.Cleanup`.

## Notes

- All money is BIGINT minor units (cents); tests assert exact integer balances.
- Integration tests are hermetic: every test gets a fresh database and tears the
  container down afterward, so they are order-independent and repeatable.
- The `integration` build tag also pulls extra test-only dependencies
  (`testcontainers-go` and friends) into the affected modules' `go.mod`.
