# Aizorix Web

The production frontend for the Aizorix marketplace — **Next.js 14 (App Router)**,
React 18, TypeScript (strict), TailwindCSS, TanStack React Query, Zustand, and
react-hook-form + zod. It talks to the API gateway documented in
[`../api/openapi/gateway.yaml`](../api/openapi/gateway.yaml).

## Quick start

```bash
pnpm install
cp .env.local.example .env.local   # adjust values
pnpm dev                           # http://localhost:3000
```

Other scripts: `pnpm build`, `pnpm start`, `pnpm lint`, `pnpm typecheck`, `pnpm format`.

In dev, browser API calls go to `/api/gateway/*`, which `next.config.mjs` rewrites
to `API_GATEWAY_URL` (default `http://localhost:8080`). Keeping calls same-origin
lets the httpOnly refresh cookie flow without CORS/SameSite friction.

## Folder structure

```
src/
├── app/                         # App Router tree (route groups in parens)
│   ├── (marketing)/             #   public landing + marketing chrome
│   │   ├── layout.tsx
│   │   └── page.tsx             #   "/"
│   ├── (auth)/                  #   split-screen auth shell
│   │   ├── layout.tsx
│   │   ├── login/page.tsx
│   │   └── register/page.tsx
│   ├── (dashboard)/             #   authenticated app (Sidebar + Topbar)
│   │   ├── layout.tsx           #   wraps children in <RequireAuth>
│   │   ├── loading.tsx          #   route-level skeleton
│   │   ├── error.tsx            #   route-level error boundary
│   │   ├── freelancer/page.tsx
│   │   ├── client/page.tsx
│   │   ├── marketplace/page.tsx
│   │   ├── projects/[id]/page.tsx
│   │   ├── proposals/new/page.tsx
│   │   ├── contracts/[id]/page.tsx
│   │   ├── contracts/[id]/time/page.tsx   # timesheet + screenshot review grid
│   │   ├── messages/page.tsx
│   │   └── payments/page.tsx
│   ├── (admin)/                 #   back-office (role-guarded, dark chrome)
│   │   ├── layout.tsx
│   │   └── admin/page.tsx
│   ├── layout.tsx              # root: fonts, <Providers>, global metadata
│   ├── providers.tsx          # QueryClientProvider (+ devtools)
│   └── globals.css
├── components/
│   ├── ui/                      # primitives: Button, Input, Card, Badge,
│   │                            #   Modal, Table, Avatar, Skeleton/Spinner
│   ├── features/                # ProjectCard, ProposalForm, ScreenshotGrid,
│   │                            #   ActivityBar, ContractTimeline, MessageThread
│   └── layout/                  # Sidebar, Topbar, RequireAuth, PageHeader
├── hooks/                       # React Query hooks + queryKeys factory
│   ├── queryKeys.ts             # single source of truth for cache keys
│   ├── useAuth.ts  useProjects.ts  useContract.ts
│   ├── useTimesheet.ts  useScreenshots.ts  useMessages.ts
├── lib/
│   ├── api/                     # axios client + typed service modules
│   │   ├── client.ts            #   instance + auth/refresh interceptors
│   │   ├── auth.ts projects.ts proposals.ts contracts.ts
│   │   ├── tracking.ts screenshots.ts payments.ts messages.ts
│   │   └── index.ts             #   barrel
│   ├── auth/session.ts          # in-memory access-token store (+ pub/sub)
│   ├── queryClient.ts           # QueryClient factory + retry policy
│   ├── types.ts                 # shared domain types (mirror the API)
│   ├── format.ts                # money / date / activity helpers
│   └── utils.ts                 # cn(), initials()
├── stores/                      # Zustand: authStore.ts, uiStore.ts
└── middleware.ts                # edge route protection by session cookie
```

## State strategy

Three layers, each with a clear job:

1. **Server state → TanStack React Query.** Anything fetched from the gateway
   (projects, contracts, timesheets, screenshots, payments, messages) lives in
   the query cache. Hooks in `src/hooks/` are the only place components touch the
   API; all keys come from the `queryKeys` factory so invalidation stays
   consistent. Mutations invalidate or optimistically patch the relevant keys.

2. **Auth/session → in-memory + Zustand.** The short-lived **access token lives
   only in JS memory** (`lib/auth/session.ts`), never in `localStorage`, to limit
   XSS blast radius. The long-lived **refresh token is an httpOnly cookie** set by
   the gateway. On a hard reload the access token is gone, so `useAuth` performs a
   **silent refresh** on boot. `stores/authStore.ts` mirrors the current user
   reactively for components and stays in sync via the session's pub/sub.

3. **Ephemeral UI state → Zustand.** Sidebar/drawer, the screenshot lightbox, and
   toasts live in `stores/uiStore.ts` — never persisted to the server cache.

### Auth & refresh flow

- `client.ts` request interceptor attaches `Authorization: Bearer <token>`.
- On a `401`, the response interceptor performs a **single** refresh (concurrent
  401s share one promise), then replays the original request. If refresh fails,
  the session is cleared and the user is routed to `/login`.
- **`middleware.ts`** is a cheap edge gate: it checks for the refresh cookie and
  bounces logged-out users away from protected routes before any dashboard JS
  loads. It cannot see the in-memory token, so fine-grained **role checks happen
  client-side in `<RequireAuth>`** and are always re-enforced by the API.

## Conventions

- **Money is integer cents** end-to-end (`*_cents`); format only at the edge via
  `lib/format.ts`. Wire shapes are snake_case to match the gateway 1:1.
- Path alias `@/*` → `src/*`. Strict TS with `noUncheckedIndexedAccess`.
- Primitives are presentational and unaware of data; feature components compose
  primitives + hooks.

## The verified-work feature

The differentiator lives in `contracts/[id]/time`:

- **`ScreenshotGrid`** renders encrypted tiles. Clicking a tile calls
  `useAuthorizeScreenshot` → `GET /v1/screenshots/{id}`, an **audited
  decrypt-on-read** that returns a short-lived signed URL plus decryption
  material; the image is revealed in a lightbox.
- **`ActivityBar`** colors each interval/screenshot by activity tier and
  fraud-flagged intervals surface a red badge for dispute review.
