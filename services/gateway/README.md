# Gateway

The **API Gateway** is the single public entry point for the Aizorix platform. All
external traffic enters here; the gateway authenticates it, rate-limits it, and
reverse-proxies it to the appropriate internal service. Internal services are never
exposed directly and trust the identity headers the gateway injects.

Module: `github.com/aizorix/platform/gateway` · Go 1.22 · depends on
`github.com/aizorix/platform/pkg` (via `replace ... => ../pkg`).

```
cmd/server/main.go            boot, JWKS bootstrap, server lifecycle, graceful shutdown
internal/config               env-driven config + the routing table
internal/auth                 JWKS verifier (background refresh) + auth middleware
internal/ratelimit            Redis token-bucket limiter (atomic Lua, fail-open)
internal/proxy                reverse-proxy router + middleware chain assembly
internal/middleware           recover, request-id, access-log + metrics
internal/observe              Prometheus registry, counter + histogram, /metrics
```

## Request pipeline

Middleware runs outermost-first:

```
panic-recover
  -> request-id        (reuse inbound X-Request-Id or generate; echo on req + resp)
    -> access-log      (structured slog line + Prometheus counter/histogram)
      -> strip spoofable identity headers      (applied to ALL traffic)
        -> rate-limit  (Redis token bucket; fail-open on cache outage)
          -> auth      (only for protected routes; injects identity headers)
            -> reverse-proxy to the upstream service
```

`/healthz` and `/metrics` are served directly by the gateway and bypass
rate-limit, auth, and the proxy.

## Routing table

Each public path prefix maps to one internal service. The upstream base URL
defaults to `http://<service>:8080` and is overridable per service via an
`UPSTREAM_<SERVICE>` env var. Matching is longest-prefix-first on path-segment
boundaries (`/v1/users` matches `/v1/users` and `/v1/users/123`, never
`/v1/userspace`). Method/path/query/body are preserved; `X-Forwarded-For`,
`X-Forwarded-Proto`, and `X-Forwarded-Host` are set.

| Public prefix          | Upstream service | Override env        |
|------------------------|------------------|---------------------|
| `/v1/auth`             | auth             | `UPSTREAM_AUTH`         |
| `/v1/users`            | user             | `UPSTREAM_USER`         |
| `/v1/projects`         | project          | `UPSTREAM_PROJECT`      |
| `/v1/proposals`        | proposal         | `UPSTREAM_PROPOSAL`     |
| `/v1/contracts`        | contract         | `UPSTREAM_CONTRACT`     |
| `/v1/tracking`         | timetracking     | `UPSTREAM_TIMETRACKING` |
| `/v1/screenshots`      | screenshot       | `UPSTREAM_SCREENSHOT`   |
| `/v1/sessions`         | screenshot       | `UPSTREAM_SCREENSHOT`   |
| `/v1/payments`         | payment          | `UPSTREAM_PAYMENT`      |
| `/v1/escrow`           | escrow           | `UPSTREAM_ESCROW`       |
| `/v1/reviews`          | review           | `UPSTREAM_REVIEW`       |
| `/v1/messages`         | messaging        | `UPSTREAM_MESSAGING`    |
| `/v1/conversations`    | messaging        | `UPSTREAM_MESSAGING`    |
| `/v1/notifications`    | notification     | `UPSTREAM_NOTIFICATION` |
| `/v1/search`           | search           | `UPSTREAM_SEARCH`       |
| `/v1/admin`            | admin            | `UPSTREAM_ADMIN`        |
| `/v1/fraud`            | fraud            | `UPSTREAM_FRAUD`        |
| `/v1/analytics`        | analytics        | `UPSTREAM_ANALYTICS`    |

`GET /.well-known/jwks.json` is also proxied to **auth** (it serves the key set).

### Public (no JWT) routes

These bypass authentication; everything else requires a valid bearer token:

- `POST /v1/auth/register`
- `POST /v1/auth/login`
- `POST /v1/auth/refresh`
- `POST /v1/auth/logout`
- `GET  /.well-known/jwks.json`
- `POST /v1/payments/webhook/stripe`
- `GET  /healthz`

## Authentication & header-injection security model

The gateway is the **only** component trusted to assert a caller's identity.
Internal services read identity from request headers and trust them
unconditionally, which is safe *only* because of the rules below.

1. **Strip on ingress.** Before any routing, the gateway deletes every
   client-supplied trusted header (`X-User-Id`, `X-Permissions`, `X-Roles`,
   `X-Residency`) from the inbound request — on **all** traffic, public and
   protected. A client therefore can never smuggle a forged identity to an
   upstream.
2. **Verify locally.** For protected routes the gateway requires
   `Authorization: Bearer <jwt>`. Tokens are ES256, verified against the auth
   service's JWKS (`issuer=https://auth.aizorix.com`, `audience=aizorix`). The
   key set is fetched at startup and refreshed in the background every 15 min
   behind an `RWMutex`; a failed refresh keeps the last-known keys (a flaky JWKS
   endpoint never disables verification). Missing/invalid token → `401`.
3. **Inject from verified claims only.** On success the gateway sets, from the
   verified token claims:
   - `X-User-Id`        ← `uid`
   - `X-Permissions`    ← `perms` (comma-separated)
   - `X-Roles`          ← `roles` (comma-separated)
   - `X-Residency`      ← `rc`

Because step 1 always wins over step 3's inputs, the headers an upstream receives
are exactly what the gateway derived from a cryptographically verified token, or
nothing.

## Rate limiting

A distributed **token bucket** backed by Redis (`REDIS_ADDR`). The bucket
arithmetic is a single atomic Lua script so all gateway replicas share one
accurate limit per key.

- Key: `rl:user:{id}` for authenticated requests, `rl:ip:{ip}` for anonymous.
- Buckets: a **stricter** bucket on `/v1/auth/*` (default 10/min) to blunt
  credential-stuffing, a **general** bucket everywhere else (default 120/min).
- On limit: `429 Too Many Requests` with a `Retry-After` header (seconds).
- **Fail-open:** if Redis is unreachable the request is allowed and a warning is
  logged. A cache outage must never take down platform ingress.

## Observability

- `GET /metrics` — Prometheus. `gateway_requests_total` (counter) and
  `gateway_request_duration_seconds` (histogram), labelled by `route`
  (logical upstream name, bounded cardinality), `method`, and `status`
  (`2xx`..`5xx`). Go runtime and process collectors are included.
- `GET /healthz` — liveness/readiness probe, returns `{"status":"ok"}`.
- One structured JSON access-log line per request (slog) with `request_id`,
  `method`, `path`, `route`, `status`, `bytes`, `duration_ms`.

## Configuration

All config is read from the environment (defaults shown).

| Env var                     | Default                                        | Purpose                                   |
|-----------------------------|------------------------------------------------|-------------------------------------------|
| `ENVIRONMENT`               | `local`                                        | local / staging / production              |
| `LOG_LEVEL`                 | `info`                                          | debug / info / warn / error               |
| `HTTP_PORT`                 | `8080`                                          | listen port                               |
| `GATEWAY_JWKS_URL`          | `http://auth:8080/.well-known/jwks.json`        | where to fetch verification keys          |
| `GATEWAY_JWKS_REFRESH`      | `15m`                                           | background JWKS refresh interval          |
| `JWT_ISSUER`                | `https://auth.aizorix.com`                      | expected token issuer                     |
| `JWT_AUDIENCE`              | `aizorix`                                        | expected token audience                   |
| `REDIS_ADDR`                | `localhost:6379`                                | rate-limiter Redis (empty disables it)    |
| `RATE_LIMIT_GENERAL`        | `120`                                           | general bucket capacity per window        |
| `RATE_LIMIT_AUTH`           | `10`                                            | `/v1/auth/*` bucket capacity per window   |
| `RATE_LIMIT_WINDOW`         | `1m`                                            | refill window for both buckets            |
| `HTTP_READ_HEADER_TIMEOUT`  | `5s`                                            | server timeout                            |
| `HTTP_READ_TIMEOUT`         | `30s`                                           | server timeout                            |
| `HTTP_WRITE_TIMEOUT`        | `30s`                                           | server timeout                            |
| `HTTP_IDLE_TIMEOUT`         | `60s`                                           | server timeout                            |
| `HTTP_SHUTDOWN_TIMEOUT`     | `15s`                                           | graceful-shutdown grace period            |
| `UPSTREAM_<SERVICE>`        | `http://<service>:8080`                          | override a single upstream base URL       |

## Build & run

The gateway is a standalone module (not part of `go.work`, which the orchestrator
owns). Build it with the workspace disabled:

```sh
cd services/gateway
GOWORK=off go build ./...
GOWORK=off go vet ./...
GOWORK=off go run ./cmd/server
```

Container image (distroless, non-root; build context is the repo root):

```sh
docker build -f services/gateway/Dockerfile -t aizorix/gateway:dev .
```
