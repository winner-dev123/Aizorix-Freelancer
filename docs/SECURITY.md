# Security Architecture (Phase 19)

Defense-in-depth across edge, transport, identity, data, and operations. This document is the
control catalog; threat models per feature live alongside the feature docs.

## 1. OWASP Top-10 controls (mapping)

| Risk | Control in Aizorix |
|------|--------------------|
| A01 Broken Access Control | RBAC (`pkg/rbac`) + ABAC ownership guards on every resource (e.g. screenshot view requires contract party or `screenshot:audit`); deny-by-default; server-side authz only. |
| A02 Cryptographic Failures | TLS 1.3 edge, mTLS mesh; envelope encryption (AES-256-GCM + KMS) for PII + screenshots; Argon2id passwords; ES256 JWT. No secrets in code. |
| A03 Injection | Parameterized queries everywhere (pgx, no string SQL); input validation at the gateway + handlers; output encoding in the frontend (React escapes by default). |
| A04 Insecure Design | Threat modeling per feature; outbox prevents dual-write; double-entry ledger invariants; least-privilege IRSA. |
| A05 Security Misconfiguration | IaC-only infra (no console drift); distroless non-root containers; read-only rootfs; restricted PSA namespaces; default-deny NetworkPolicies. |
| A06 Vulnerable Components | Dependabot (gomod/npm/cargo/actions/docker); govulncheck, Trivy, CodeQL in CI; SBOM + cosign signing. |
| A07 Auth Failures | MFA (TOTP/WebAuthn), lockout + backoff, refresh-token rotation with reuse detection (family revoke), device binding, impossible-travel detection. |
| A08 Integrity Failures | Cosign-signed images; screenshot SHA-256 + Ed25519 device signatures; hash-chained audit log. |
| A09 Logging/Monitoring Failures | Structured logs → Loki, traces → Tempo, metrics → Prometheus; security events to SIEM; SLO burn alerts; every privileged action audited. |
| A10 SSRF | Egress controls via NetworkPolicy + VPC endpoints; allow-list outbound (Stripe, KMS, SES); presigned URLs validated; no user-supplied URLs fetched server-side without allow-list. |

## 2. Identity & access

- **Tokens:** ES256 access JWT (15 min) verified locally via JWKS (no hot-path introspection);
  opaque refresh tokens (only SHA-256 hash stored), rotated every use. Reuse of a rotated token
  revokes the whole session family (`sessions.family_id`).
- **MFA:** TOTP + WebAuthn; step-up MFA required for admin/finance actions and high-risk events.
- **RBAC:** roles → permissions seeded in `db/migrations/000002`; permissions are verb:resource
  (`payment:refund`, `screenshot:audit`). Admin actions need both the permission and step-up MFA.
- **Service identity:** SPIFFE/SPIRE or Istio identities for mTLS; pods assume AWS roles via
  IRSA (no static keys). Each service's IAM role is least-privilege (e.g. `screenshot` may
  `s3:PutObject`/`GetObject` only under its prefix and `kms:Decrypt`/`GenerateDataKey` only with
  the screenshots CMK).

## 3. Data protection

- **At rest:** RDS/EBS/S3 SSE-KMS with per-domain CMKs (rotation on). Application-layer envelope
  encryption for PII (`*_encrypted` columns + `wrapped_dek`) and for every screenshot blob.
- **In transit:** TLS everywhere; presigned S3 PUT/GET are HTTPS-only, short-TTL, single-object.
- **Key hierarchy:** KMS CMK (master) → per-object data keys (DEK). DEKs are generated per
  screenshot, used once, and stored only KMS-wrapped. Token-signing keys use a dedicated CMK.
- **Crypto-shredding:** GDPR/retention erasure destroys the wrapped DEK → ciphertext is
  permanently unrecoverable without bulk-deleting blobs.

## 4. API & edge security

- **WAF** (AWS managed CRS + bot control) + **Shield Advanced** (DDoS) in front of CloudFront/ALB.
- **Rate limiting:** Redis token-bucket per identity and per IP at the gateway; stricter buckets
  for auth endpoints. Progressive challenges (CAPTCHA/MFA) on anomaly.
- **Input limits:** body size caps, schema validation, content-type allow-lists; file uploads
  go to quarantine + AV scan (ClamAV/VirusTotal) before they are downloadable.
- **CORS/CSP:** strict CSP on web + tracker webview; CORS allow-list for the SPA origin only.

## 5. Secrets management

- AWS Secrets Manager + External Secrets Operator → K8s Secrets → pod env. Rotation enabled for
  DB creds and third-party keys. No secret is ever committed; CI uses OIDC to assume roles (no
  long-lived cloud keys). `gitleaks` + secret scanning block leaks in CI.

## 6. Audit & tamper-evidence

- Every privileged read/write writes an `audit_logs` row (partitioned monthly). Rows are
  **hash-chained** (`row_hash = sha256(prev_hash || canonical(row))`) and streamed to an S3
  bucket with **Object Lock (compliance mode)** for an immutable, regulator-grade trail.
- Screenshot **views** are always audited (actor, time, contract) — central to monitoring trust.

## 7. Incident response

- Runbooks in `docs/runbooks/` (KMS key compromise/rotation, token-signing-key rotation, account
  takeover sweep, data-breach disclosure, Stripe outage). On-call via PagerDuty; SLO burn-rate
  alerts (see `infra/observability/SLO.md`). Quarterly tabletop + game-day drills.
