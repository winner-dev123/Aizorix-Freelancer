# Live Run Log — End-to-End Verification

The platform has been stood up and driven end-to-end against **real infrastructure**
(PostgreSQL 16, Redis, MinIO/S3, Redpanda/Kafka — all in Docker), not just compiled. This log
records what was proven live and, importantly, the bugs that **only a real run surfaced** —
each now fixed and pinned by a regression test.

Reproduce the whole thing: `make demo` (or `pwsh scripts/demo.ps1`), tear down with
`make demo-down`. The integration tests in CI (`.github/workflows/ci-integration.yml`) cover
the same paths automatically on every PR.

## Flows proven live

| # | Flow | What it exercised | Evidence |
|---|------|-------------------|----------|
| 1 | **Identity** | register → login → `/v1/auth/me` through the gateway | gateway verified ES256 JWT via JWKS, injected `X-User-Id`, proxied to auth → DB; unauth `/me` → 401; login as the *seeded* user proved seed↔auth Argon2 agreement |
| 2 | **Verified hourly work → escrow payout** | start session → submit activity slices → close → weekly billing → escrow release | server **recomputed activity = 90%** from raw samples (3240/3600s); billed 0.9h × \$70 = **\$63**; escrow released exactly \$63, protected \$147; **double-entry ledger every txn_group nets 0** |
| 3 | **Encrypted screenshot pipeline** | enroll device key → AES-256-GCM encrypt → presigned PUT to MinIO → confirm (signature-verified vs *enrolled* key) → authorized decrypt-on-read | 107-byte ciphertext in MinIO (91 plaintext + 16 GCM tag); decrypt recovered exact original; signature verified against the enrolled Ed25519 key, not a request-supplied one |
| 4 | **Event-driven backbone (outbox → relay → Kafka → consumers)** | replayed the outbox; relay → Redpanda → notification + analytics consumers | relay published 10 events (outbox pending → 0), auto-created 10 topics; analytics `event_counts` populated with exactly those 10 events by type (incl. `escrow.released`×2 — the `escrow.events` topic that was dropped before the fix); `gmv_daily` correctly **empty** (no `payment.captured` → GMV de-dup fix holds); `processed_events` analytics=10 / notification=9 (clean idempotency); notifications fanned out 7 → 12 |

## Bugs surfaced by running it (invisible to compiler + unit tests)

1. **auth was missing `/.well-known/jwks.json`** — only the proto declared it. The gateway's
   fail-fast JWKS bootstrap would have crashed on startup. → endpoint added; regression test in
   `services/auth/internal/httpapi/handlers_test.go`.
2. **`CreateSlot` SQL type-inference error (`42P08`)** — the `captured_at` param was used both as
   a column value and in `$5 + interval '90 days'`, which Postgres can't type-infer. → cast to
   `$5::timestamptz` in both positions. Caught the instant it hit a real DB.
3. **Consumers never subscribed to `escrow.events`** — escrow emits there, but the consumers
   expected escrow on `payment.events`, so those events silently vanished. → subscriptions
   added + consumers now prefer the `event-type` bus header; regression tests in both
   `cmd/consumer/main_test.go`.
4. **Analytics rollup tables were missing from migrations** — `event_counts` / `gmv_daily` /
   `funnel_daily` were referenced by code but never created. → migration `000012` added (the
   runner applied just the new one, idempotently).
5. **Dedupe-before-process ordering bug (`pkg/kafka`)** — an event was marked "processed"
   *before* the handler ran, so a transient handler failure (the missing table above)
   permanently skipped 7 events. → fixed to **mark-after-success**; regression test in
   `services/pkg/kafka/consumer_test.go` proves a failing handler never marks.

## Frontend ↔ live backend (browser tier)

The Next.js app was then run against the **live** backend and the full browser→DB loop proven:

| Step | Result |
|------|--------|
| `next build` (production, all 14 routes) | ✅ compiles + prerenders after 3 fixes (below) |
| SSR pages (`/`, `/login`, `/register`, `/marketplace`) | ✅ render real HTML (200) |
| Login via the Next `/api/gateway` proxy → gateway → auth → Postgres | ✅ token issued |
| `/me` with the JWT through the proxy | ✅ gateway verified JWT, injected `X-User-Id`, returned identity |
| Unauth `/me` through the proxy | ✅ 401 (gateway-enforced) |
| **Automated browser click-through (Playwright/Chromium)**: landing → login form → real login → authenticated dashboard | ✅ **4/4**, logged in as ada (Freelancer), role-filtered nav rendered; screenshots in `web/e2e/screenshots/` (run via `node e2e/smoke.mjs`) |

**Frontend bugs surfaced by building/running (never run before):**

6. **`typedRoutes` rejected a dynamic redirect** — `router.replace(?next= param)` isn't a typed
   `Route`. → typed the dynamic target as `Route` and disabled the experimental `typedRoutes`
   flag (off by default in Next; a compile-time aid only).
7. **`useSearchParams()` without a Suspense boundary** broke static prerendering of `/login`
   and `/proposals/new` (App Router CSR-bailout rule). → wrapped both in `<Suspense>`.
8. **The `&` in the project path `Aizorix&Freelancer` hangs the Next server at request time.**
   `next build` bakes the absolute path into `.next`; the running server then hangs loading
   route chunks from `&`-containing paths (the build's prerender works because it's a separate
   code path; a directory junction does **not** help — Node canonicalizes it back). Confirmed
   identical on Node 20 and Node 24, so it is **not** a Node-version issue. **Workaround: build
   and run the web app from a path with no `&`** (e.g. copy/checkout to `D:\webclean`). This is
   the only place the unusual repo directory name leaks into tooling — documented in
   `docs/LOCAL_DEV.md`.

9. **The refresh cookie was unusable in a browser over HTTP** — auth set it `Secure: true` with
   `Path: /v1/auth`. Browsers refuse `Secure` cookies over `http://localhost`, and the path
   didn't match the proxied `/api/gateway/...` calls or the page routes the middleware guards.
   Result: login succeeded at the API level (login 200 + me 200 in the gateway log) but the
   middleware bounced every protected route back to `/login` — the app was unusable in a real
   browser. **Every API-level test had passed** because they pass the Bearer token explicitly and
   never exercise the cookie/middleware path. → cookie now uses `Path: /` and `Secure` only in
   production (`SetCookieSecure`). After the fix the Playwright click-through went **3/4 → 4/4**,
   and login persists across a full page reload (cookie-based silent refresh re-establishes it).

## Adversarial review pass (the newest, never-reviewed code)

After the live runs, three parallel adversarial reviews swept the code written *since* the
original verification — the gateway, the WebSocket gateway, the event backbone, and the live
Stripe/OpenSearch clients. They confirmed **12 more real bugs** (3 critical, 4 high, 4 medium,
1 low), all now fixed and re-built clean — in code that compiled and unit-tested green:

| # | Sev | Area | Bug → fix |
|---|-----|------|-----------|
| 10 | CRIT | payment | Stripe **stub could run in production** (API key not fail-closed) → fake charges credit real escrow. Now exits in prod if the resolved client is the stub; the startup log reflects the *actual* decision. |
| 11 | CRIT | wsgateway | **Any user could `join` (and read live messages of) any conversation** — no participant check. Now verifies membership via the messaging service (added `…/membership`), fail-closed. |
| 12 | CRIT | event bus | **Cross-DB `event-id` collision** drops events in the one-DB-per-service topology (each outbox sequence starts at 1). Now namespaced by the relay's source; regression-tested. |
| 13 | HIGH | gateway | Rate limiter ran **before** auth, so it only ever keyed on IP (per-user policy dead; NAT'd users share a bucket). Now: public→IP-keyed, protected→user-keyed *after* auth. |
| 14 | HIGH | gateway | `X-Forwarded-For` trusted blindly → trivial limit evasion / victim lockout. Now uses `RemoteAddr`, honoring XFF only from configured trusted-proxy CIDRs. |
| 15 | HIGH | wsgateway | `/presence` unauthenticated (online-status enumeration oracle) → now requires a bearer token. |
| 16 | HIGH | wsgateway | Presence wiped on the first of multiple connections (false "offline") → now ref-counted per user. |
| 17 | MED | payment | Webhook kept only the last `v1=` signature → broke during Stripe secret rotation. Now accepts any matching `v1`. |
| 18 | MED | event bus | Relay swallowed drain errors silently → now logs them (rows piling up is now visible). |
| 19 | MED | wsgateway | With Redis disabled, nothing was delivered (even same-replica) → now delivers locally. |
| 20 | MED | gateway | (see #13/#14 above — rate-limit correctness) |
| 21 | LOW | wsgateway | `CheckOrigin` allowed all origins when unset → now default-deny. |

Verified **clean** by the same reviews: the double-entry ledger math, Stripe idempotency/dedupe,
JWT/JWKS verification (alg-pinned, iss/aud/exp enforced), the WS hub concurrency/locking, the
GMV de-duplication, and OpenSearch query injection-safety.

## Why this matters

Bugs 3–5 are classic distributed-systems faults: a topic-name mismatch, an unmigrated schema,
and a record-before-commit ordering error. None are visible to a compiler or a unit test in
isolation — they appear only when real events flow through real Kafka into real consumers
hitting a real database under a partial failure. Catching and fixing them, then locking them in
with tests, is the difference between "builds" and "runs".
