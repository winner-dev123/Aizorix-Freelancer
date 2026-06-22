# CI/CD Pipelines

This directory holds every GitHub Actions workflow for the Aizorix monorepo.
Pipelines are **path-filtered** so a change in one area (Go service, web app,
desktop tracker, Terraform) only runs the relevant jobs.

## Pipeline graph

```
                     ┌─────────────────── Pull Request ───────────────────┐
                     │                                                     │
   ci-backend.yml ───┤  (changed Go services: build/vet/lint/test/vuln)   │
   ci-frontend.yml ──┤  (web: lint/typecheck/test/build)                  │  required
   ci-tracker.yml ───┤  (tracker: fmt/clippy/test on win/mac/linux)       │  status
   terraform.yml ────┤  (fmt/validate/plan → PR comment)                  │  checks
   security.yml ─────┘  (CodeQL · dependency-review · gitleaks)           │
                     └─────────────────────────────────────────────────────┘
                                          │  merge to main
                                          ▼
   ┌──────────────────────────────────────────────────────────────────────┐
   │ build-push.yml   per-service Docker build → ECR                        │
   │                  · tags: <sha>, latest (+ semver on tags)              │
   │                  · cosign keyless sign · syft SBOM attestation         │
   │                  · trivy scan → SARIF                                   │
   │                                                                        │
   │ terraform.yml    apply  (env: production-infra → required reviewers)   │
   │                                                                        │
   │ release.yml      semantic-release → tag vX.Y.Z + CHANGELOG             │
   │                    ├─ tauri-bundle  (win/mac/linux desktop artifacts)  │
   │                    └─ calls deploy.yml ▼                               │
   └──────────────────────────────────────────────────────────────────────┘
                                          │
                                          ▼
   ┌──────────────────────── deploy.yml (reusable) ────────────────────────┐
   │  1. migrate   golang-migrate up (expand-phase, backward compatible)    │
   │  2. rollout   kustomize edit set image  → commit to GitOps overlay     │
   │  3. sync      argocd app sync + wait (Healthy & Synced)                │
   │                                                                        │
   │  Called twice:  staging (auto)  →  production (env gate: reviewers)    │
   └────────────────────────────────────────────────────────────────────────┘
```

## Workflows

| File              | Trigger                       | Purpose |
|-------------------|-------------------------------|---------|
| `ci-backend.yml`  | PR / push on `services/**`    | Matrix over **changed** Go modules: build, vet, golangci-lint, `go test -race`, govulncheck. A `services/pkg` change rebuilds all. |
| `ci-frontend.yml` | PR / push on `web/**`         | pnpm lint, typecheck, test, `next build`. |
| `ci-tracker.yml`  | PR / push on `desktop-tracker/**` | Rust fmt + clippy `-D warnings` + test on windows/macos/linux. |
| `build-push.yml`  | push `main`, tag `v*`         | Build each service image, push to ECR (SHA + semver tags), cosign sign, syft SBOM, trivy scan. |
| `deploy.yml`      | `workflow_call` (reusable)    | migrate → kustomize image bump → Argo CD sync; per-environment. |
| `terraform.yml`   | PR / push on `infra/terraform/**` | fmt/validate/plan on PR; apply on main (gated). |
| `release.yml`     | push `main`, manual           | semantic-release tag + changelog, Tauri bundles, calls `deploy.yml`. |
| `security.yml`    | PR / push / weekly cron       | CodeQL (Go + JS/TS), dependency review, gitleaks secret scan. |
| `dependabot.yml`  | schedule (weekly)             | Updates for actions, gomod, npm, cargo, docker. |

## Required secrets

| Secret                        | Used by                       | Notes |
|-------------------------------|-------------------------------|-------|
| `AWS_ECR_PUSH_ROLE_ARN`       | build-push                    | IAM role with ECR push, OIDC-assumed. |
| `AWS_DEPLOY_ROLE_ARN`         | deploy                        | IAM role with EKS/migrate access. |
| `AWS_TERRAFORM_ROLE_ARN`      | terraform                     | IAM role for plan/apply. |
| `TF_STATE_BUCKET`             | terraform                     | S3 backend bucket. |
| `TF_LOCK_TABLE`               | terraform                     | DynamoDB lock table. |
| `ARGOCD_SERVER`               | deploy                        | Argo CD API endpoint. |
| `ARGOCD_AUTH_TOKEN`           | deploy                        | Argo CD account token (sync rights). |
| `STAGING_DATABASE_URL`        | deploy (staging)              | Migration DSN. |
| `PRODUCTION_DATABASE_URL`     | deploy (production)           | Migration DSN. |
| `TAURI_SIGNING_PRIVATE_KEY` (+ password) | release            | Updater bundle signing. |
| `APPLE_CERTIFICATE` / `APPLE_SIGNING_IDENTITY` / `APPLE_CERTIFICATE_PASSWORD` | release | macOS code signing (optional). |

`GITHUB_TOKEN` is provided automatically; no static value needed.

## OIDC / AWS authentication

No long-lived AWS keys are stored in GitHub. Workflows that touch AWS request an
OIDC token (`permissions: id-token: write`) and assume an IAM role via
`aws-actions/configure-aws-credentials`. Provision a GitHub OIDC provider
(`token.actions.githubusercontent.com`) in IAM and a role per purpose
(ECR push, deploy, terraform) whose trust policy restricts `sub` to this repo and
the appropriate ref/environment, for example:

```
"token.actions.githubusercontent.com:sub": "repo:aizorix/marketplace:ref:refs/heads/main"
"token.actions.githubusercontent.com:sub": "repo:aizorix/marketplace:environment:production"
```

## GitHub Environments

| Environment        | Gate |
|--------------------|------|
| `staging`          | auto-deploy. |
| `production`       | required reviewers + wait timer (app rollout). |
| `staging-infra`    | terraform plan (no approval). |
| `production-infra` | terraform apply — required reviewers. |

## Branch protection (recommended required checks)

`backend-ci`, `ci-frontend / lint • typecheck • test • build`,
`ci-tracker / clippy • test (*)`, `terraform / fmt • validate • plan`,
`security / codeql (*)`, and `security / dependency-review`.
