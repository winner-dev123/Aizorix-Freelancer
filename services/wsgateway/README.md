# wsgateway — real-time messaging WebSocket gateway

`wsgateway` terminates authenticated WebSocket connections and fans real-time messaging,
typing, read-receipt, and presence events out to clients. It is the live-delivery layer that
sits in front of the REST `messaging` service: `messaging` owns durable history and
participant authorization; `wsgateway` owns the sockets and the cross-replica fan-out.

```
browser / app ──WSS──▶  NLB (sticky)  ──▶  wsgateway replica ──▶ Redis pub/sub ──▶ other replicas
                                                  │
                                                  └── best-effort POST ──▶ messaging service (persist)
```

## Endpoints

| Method | Path                          | Purpose                                                        |
|--------|-------------------------------|---------------------------------------------------------------|
| GET    | `/ws`                         | WebSocket upgrade (authenticated). See protocol below.        |
| GET    | `/presence?user_ids=a,b,c`    | `{"presence":{"a":true,"b":false}}` from Redis presence keys. |
| GET    | `/healthz`                    | Liveness (200).                                               |
| GET    | `/metrics`                    | Prometheus-style liveness gauge.                              |

## Authentication

The `/ws` upgrade is authenticated **before** the handshake completes; unauthenticated
upgrades are rejected with `401`. The access token (ES256 JWT issued by `auth`) is read from:

1. `Authorization: Bearer <token>` header, or
2. `?token=<token>` query parameter (browsers cannot set headers on the WS handshake, so the
   query param is the standard escape hatch).

Tokens are verified **locally** against the auth service's JWKS, fetched from
`GATEWAY_JWKS_URL` (default `http://auth:8080/.well-known/jwks.json`) on startup and refreshed
periodically (`GATEWAY_JWKS_REFRESH`, default 15m). The verifier (`pkg/token.ParseJWKS` +
`NewVerifier`) keeps the last-good key set across transient JWKS outages so verification never
hard-fails on a blip. The token's `uid` claim becomes the connection's user identity.

## WebSocket protocol

All frames are JSON objects with a `type` discriminator.

### Inbound (client → gateway)

| Frame | Shape | Notes |
|-------|-------|-------|
| join   | `{"type":"join","conversation_id":"<id>"}`                 | Subscribe to a conversation's events. Replies with an `ack`. |
| leave  | `{"type":"leave","conversation_id":"<id>"}`                | Unsubscribe. |
| send   | `{"type":"send","conversation_id":"<id>","body":"...","ref":"<opt>"}` | Send a chat message: **persisted** then fanned out. `ref` is an optional client correlation id. |
| typing | `{"type":"typing","conversation_id":"<id>"}`               | Ephemeral typing indicator (pub/sub only, never stored). |
| read   | `{"type":"read","conversation_id":"<id>"}`                 | Ephemeral read receipt (pub/sub only, never stored). |

### Outbound (gateway → client)

| Frame | Shape |
|-------|-------|
| message.sent | `{"type":"message.sent","conversation_id","message_id","sender_id","body","ref","ts"}` |
| typing       | `{"type":"typing","conversation_id","sender_id","ts"}` |
| read         | `{"type":"read","conversation_id","sender_id","ts"}` |
| presence     | `{"type":"presence","user_id","online"}` |
| ack          | `{"type":"ack","conversation_id","ref"}` |
| error        | `{"type":"error","code","message"}` |

`ts` is unix milliseconds. A sender does not receive its own `typing`/`read` echoes.

### Keepalive

The gateway sends a WebSocket **ping** every `WS_PING_INTERVAL` (default 25s) and expects a
**pong** within `WS_PONG_WAIT` (default 60s); a missed pong closes the connection. Every pong
also refreshes the sender's presence key, so an idle-but-connected client stays "online" with
no application-level heartbeat frame required.

### Backpressure

Each connection has a bounded outbound buffer (`WS_SEND_BUFFER`, default 64 frames). If a
client is too slow to drain it, the connection is dropped rather than allowing one slow peer to
stall the hub's fan-out path. A single write-pump goroutine owns all socket writes (gorilla
forbids concurrent writers).

## Redis channel design

| Key / channel              | Type      | Purpose |
|----------------------------|-----------|---------|
| `conv:{conversationId}`    | pub/sub   | All events for a conversation (`message.sent`, `typing`, `read`). |
| `presence:{userId}`        | string+TTL| Presence marker, TTL `WS_PRESENCE_TTL` (default 90s), refreshed on every pong. |

- On **join**, a replica opens (or ref-counts onto) a single Redis subscription per
  conversation that has local subscribers. One reader goroutine per active conversation per
  replica decodes each published frame once and fans it to the local connections subscribed to
  it. When the last local subscriber leaves, the subscription is torn down.
- On **send**, the gateway **persists** to the messaging service
  (`POST {MESSAGING_URL}/v1/messaging/conversations/{id}/messages` with the trusted `X-User-Id`
  header) and then **publishes** a `message.sent` frame to `conv:{id}` for fan-out. Persistence
  is best-effort: a transient datastore failure logs and still fans out for live delivery — the
  messaging service remains the durable source of truth and reconciles via its own events.
- **typing**/**read** are ephemeral: published to `conv:{id}` only, never persisted.
- **presence** is read via `MGET presence:{userId}` for the `/presence` endpoint and set/refreshed
  with a TTL on connect and on every keepalive pong, so a crashed client expires naturally.

## Scaling model

- **Stateless replicas, sticky sockets.** Each replica holds only the connections it terminates.
  A network load balancer (NLB / `ip_hash`-style stickiness) pins a client to one replica for
  the life of the socket. No shared connection state is needed.
- **Redis stitches replicas together.** Because all delivery flows through `conv:{id}` pub/sub,
  a message sent on replica A reaches subscribers on replicas B and C. Add replicas to scale
  connection count horizontally; Redis pub/sub is the only shared dependency on the hot path.
- **Presence is global.** Presence lives in Redis keyspace (not replica memory), so
  `/presence` returns a correct answer regardless of which replica a user is connected to.
- **Graceful shutdown.** On `SIGTERM` the HTTP server stops accepting upgrades and the shared
  request context is cancelled, which signals every connection's write pump to send a close
  frame and exit. The NLB then routes reconnecting clients to healthy replicas.

## Membership authorization (scaffold note)

For this scaffold, the gateway **trusts the conversation ids a client joins**. A production
build MUST verify the connection's user is a participant of a conversation before subscribing
it — e.g. a check against the messaging service (`GET .../conversations` membership, or a
dedicated internal "is-participant" endpoint) — and reject the join with an `error` frame
otherwise. The exact insertion point is marked in `internal/hub/hub.go` (`(*Hub).join`).

## Configuration

| Env var | Default | Meaning |
|---------|---------|---------|
| `HTTP_PORT`            | `8080` | Listen port. |
| `GATEWAY_JWKS_URL`     | `http://auth:8080/.well-known/jwks.json` | JWKS endpoint for token verification. |
| `GATEWAY_JWKS_REFRESH` | `15m`  | JWKS refresh interval. |
| `JWT_ISSUER`           | `https://auth.aizorix.com` | Expected token issuer. |
| `JWT_AUDIENCE`         | `aizorix` | Expected token audience. |
| `REDIS_ADDR`           | `localhost:6379` | Redis for pub/sub + presence. Empty → single-replica local-only mode. |
| `MESSAGING_URL`        | `http://messaging:8080` | Messaging service base URL for persistence. |
| `WS_PING_INTERVAL`     | `25s`  | Keepalive ping cadence. |
| `WS_PONG_WAIT`         | `60s`  | Max wait for a pong before closing. |
| `WS_WRITE_WAIT`        | `10s`  | Per-write deadline. |
| `WS_SEND_BUFFER`       | `64`   | Per-connection outbound queue depth. |
| `WS_MAX_MESSAGE_SIZE`  | `65536`| Max inbound frame size (bytes). |
| `WS_PRESENCE_TTL`      | `90s`  | Presence key TTL (refreshed on pong). |
| `WS_ALLOWED_ORIGINS`   | (empty)| Comma-separated Origin allow-list (CSWSH defense). Empty disables the check (dev). |
| `HTTP_SHUTDOWN_TIMEOUT`| `15s`  | Graceful shutdown budget. |

## Build

```sh
GOWORK=off go build ./...
GOWORK=off go vet  ./...
docker build -f services/wsgateway/Dockerfile -t aizorix/wsgateway:dev .
```
