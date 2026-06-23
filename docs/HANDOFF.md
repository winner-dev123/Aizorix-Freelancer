# Handoff — continuing Aizorix on another computer

This is the practical "pick up where I left off on a new machine" guide. For the day-to-day local
run flow (Docker stack → migrate → seed → smoke), see [`LOCAL_DEV.md`](./LOCAL_DEV.md); for the
proven behavior + the full bug-fix history, see [`RUN_LOG.md`](./RUN_LOG.md). This doc covers
**setup on a fresh machine, current state, what's left, and the non-obvious gotchas.**

---

## 1. Current state (as of the latest commit)

- **Repo:** `github.com/winner-dev123/Aizorix-Freelancer`, branch `main`. Everything is pushed —
  a clean `git clone` is the entire transfer. Tree is clean; single-author history.
- **CI: all 8 workflows green** — `ci-backend`, `ci-frontend`, `ci-tracker`, `ci-integration`,
  `security`, `terraform`, `build-push`, `release`. (`deploy.yml` only runs with cloud secrets.)
- **Quality:** built end-to-end, run live against real infra, hardened through **three adversarial
  review waves (54 findings, 54 fixed, 10 critical)**; the wave-3 audit is fully closed. The
  highest-risk paths are pinned by unit tests.
- **Layout:** 21 Go service modules (`services/*`, tied by `go.work`), Next.js 14 web (`web/`),
  Tauri 2 + Rust desktop tracker (`desktop-tracker/`), SQL migrations (`db/migrations/`),
  Terraform (`infra/terraform/`), k8s/Kustomize (`infra/k8s/`).

## 2. Get the code on the new machine

```bash
gh auth login                       # authenticate as the account that owns the repo (winner-dev123)
git clone https://github.com/winner-dev123/Aizorix-Freelancer.git
cd Aizorix-Freelancer               # NOTE: the working dir name may contain '&' — quote it in shells

# Keep the commit history single-author and consistent:
git config user.name  "Eduardo"
git config user.email "wisdom.lifegood0101@gmail.com"
```

## 3. Toolchain to install

| Tool | Version | Why this version |
|------|---------|------------------|
| **Go** | **1.25.0** | `go.work` pins `go 1.25.0`; modules use 1.25 language/std features. |
| **golangci-lint** | **v2.12.2** (the v2 line) | **v1.x cannot analyze Go 1.25** ("unsupported version: 2" on export data). CI uses golangci-lint-action@v8 + `GOLANGCI_LINT_VERSION=v2.12.2`; `.golangci.yml` is the v2 schema. |
| **Node** | ≥ 20 | `web/` is Next.js 14. |
| **npm** | bundled | **Use npm, not pnpm/yarn** — CI (`ci-frontend`) and the lockfile are npm. (The `package.json` still lists a `packageManager: pnpm` line, but the project is driven with npm.) |
| **Docker** (Desktop or engine) | recent | For the local stack (Postgres/Redis/Redpanda/MinIO/Elasticsearch) and `make demo`. |
| **golang-migrate** (`migrate`) | recent | Applies `db/migrations`. |
| **Rust** + Tauri prereqs | stable | Only needed to build/run the desktop tracker (`desktop-tracker/`). See the Tauri gotcha in §6. |
| **Terraform** | 1.8.x | `infra/terraform/` (`terraform.yml` pins 1.8.5). Only needed to touch IaC. |

## 4. Build / test / lint

Most `make` targets exist (`make help` lists them). Key ones:

```bash
make dev-up        # docker compose up the infra (Postgres/Redis/Redpanda/MinIO/ES)
make migrate-up    # apply db/migrations
make seed          # idempotent demo dataset (prints demo logins; pw: DemoPassw0rd!)
make test          # Go unit tests
make lint          # golangci-lint v2 across modules
make demo          # one-shot: infra + services + web + smoke + browser test
make demo-down     # tear it all down
```

**Per-module Go commands** (the workspace can interfere — disable it per module):

```bash
cd services/<name>
GOWORK=off go build ./...
GOWORK=off go test ./...
```
Integration tests are behind a build tag and need Docker: `go test -tags integration ./...`
(or `make test-integration`). Plain `go test ./...` skips them.

**Frontend:**

```bash
cd web && npm ci && npm run build && npm run typecheck
```

## 5. What's left to do

Everything achievable without external inputs is done. The remaining work is **inputs-gated** —
each needs something only you can provide:

1. **AWS OIDC + Terraform state secrets** → flips `build-push` (real ECR publish), `terraform`
   (`plan`/`apply` against the `production-infra` environment), and `deploy.yml` from
   build/validate-only to live. Set repo secrets: `AWS_TERRAFORM_ROLE_ARN`, `TF_STATE_BUCKET`,
   `TF_LOCK_TABLE` (+ the ECR/deploy equivalents). Until then these workflows stay green by design
   (offline validate / gated apply).
2. **Stripe test keys** → exercise a live charge → escrow → payout end-to-end.
3. **Docker up** → re-run `make demo` to re-validate the full live stack.

**Lower priority — remaining unit-test gaps** (intentionally not done; not unit-testable):
`messaging` (its real logic is participant authz that needs the DB → integration-test candidate),
`relay` (a polling loop), `tools` (CLI utilities). Every module with pure/guard logic already has
focused tests (search, review, proposal, user, project, wsgateway, screenshot, admin, …).

## 6. Gotchas & conventions (the non-obvious stuff)

- **golangci-lint must be the v2 line** (see §3). If you see "unsupported version: 2" you're on v1.
- **Local golangci-lint may emit phantom `undefined: pgx/chi/redis` typechecking errors** for some
  modules (a local dep-cache quirk). Trust `go vet ./...` and CI instead — both resolve deps
  correctly. It is **not** a real problem and not from the test files.
- **The desktop tracker can't be fully compiled locally without the MSVC toolchain + Tauri system
  deps.** Workflow used: install `rustfmt` (no compiler needed) to fix formatting locally, then fix
  any compile/clippy errors *from `ci-tracker`'s precise output* and push. `ci-tracker` builds it on
  Linux/macOS/Windows runners — that's the source of truth for the Rust side.
  - The tracker icons are generated, RGBA-forced PNGs: `cd desktop-tracker/src-tauri && go run tools/genicon.go` (Tauri rejects non-RGBA; Go's `png.Encode` drops alpha on opaque images, so they're rendered at alpha 254).
- **golang-migrate migrations are paired up/down.** The most recent schema-hardening migration is
  `000014` (DEFAULT partitions, append-only triggers on ledger/audit, a unique index). CI
  (`ci-integration`) applies them against real Postgres.
- **`ci-integration` can flake** on testcontainer startup ("connection reset by peer" applying
  migrations). It's transient — re-run the failed job (`gh run rerun <id> --failed`); it's not a
  code regression.
- **Terraform Deployments tab:** the workflow deliberately does **not** put a GitHub `environment:`
  on its fmt/validate/plan job (only a real `apply` targets `production-infra`), so CI runs don't
  masquerade as deployments. Keep it that way.

### Working conventions for this repo
- **Commit history must stay clean and human-authored** — no AI-assistant names anywhere in commit
  messages/author/committer, and **no `Co-Authored-By` trailers**. Verify before pushing:
  `git log -1 --format='%B%n%an %ae %cn %ce' | grep -ci <assistant-name>` should be `0`.
- **Pushes go to `winner-dev123/Aizorix-Freelancer`**; authenticate `gh` as that account.
- **Don't run global Docker/WSL resets** — the host may run other stacks. Restart only the Docker
  Desktop app if needed, and keep this project on its isolated compose project + high ports.
- **Watch out for stray gofmt temp files** (`*.go.<digits>`): if you `git add` a directory, check
  `git status` doesn't sweep them in.

## 7. Where to read more

- [`README.md`](../README.md) — top-level overview + the bug-fix tally.
- [`docs/ARCHITECTURE.md`](./ARCHITECTURE.md) — design.
- [`docs/RUN_LOG.md`](./RUN_LOG.md) — what was proven live + all three review waves.
- [`docs/SECURITY.md`](./SECURITY.md), [`SCREENSHOT_PIPELINE.md`](./SCREENSHOT_PIPELINE.md),
  [`FRAUD.md`](./FRAUD.md), [`COMPLIANCE.md`](./COMPLIANCE.md) — subsystem deep-dives.
- [`ROADMAP.md`](../ROADMAP.md) — planned phases.
