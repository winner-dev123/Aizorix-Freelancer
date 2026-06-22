# Aizorix — Terraform Infrastructure

Infrastructure-as-Code for the Aizorix freelancer marketplace on AWS EKS.
Primary region is `us-east-1`; the topology is multi-region capable (RDS cross-region
read replica + S3 cross-region replication are wired in for DR / read-locality).

## Layout

```
infra/terraform/
├── versions.tf            # Terraform + provider pins, S3 remote backend
├── variables.tf           # Root input variables (region, env, sizing, ...)
├── main.tf                # Wires the modules together (the composition root)
├── environments/
│   ├── production.tfvars  # Production sizing / flags
│   └── staging.tfvars     # Staging sizing / flags
└── modules/
    ├── vpc/               # VPC, 3-AZ public/private/intra subnets, NAT, endpoints
    ├── eks/               # EKS control plane, managed node groups, IRSA/OIDC
    ├── rds/               # PostgreSQL 16 Multi-AZ + cross-region replica option
    ├── elasticache/       # Redis (cluster mode enabled)
    ├── msk/               # MSK / Kafka with IAM auth + TLS
    ├── s3/                # screenshots / assets / backups buckets
    ├── cloudfront/        # CDN + signed screenshot URLs + WAF
    ├── iam/               # Per-service IRSA roles (least privilege)
    ├── kms/               # CMKs (RDS, S3/screenshots, token-signing) with rotation
    └── secrets/           # Secrets Manager entries + rotation
```

Modules are intentionally small and single-purpose so they can be unit-tested and reused.
`main.tf` is the only place that composes them; modules never reach across to each other —
they receive everything they need as explicit inputs and expose results via `outputs.tf`.

## Remote state

State lives in a versioned, SSE-encrypted S3 bucket with a DynamoDB lock table.
These must exist **before** the first `init` (bootstrap them once, manually or with a
tiny separate config). Suggested names:

| Resource            | Name                                   |
|---------------------|----------------------------------------|
| State bucket        | `aizorix-tfstate-us-east-1`            |
| Lock table (Dynamo) | `aizorix-tflock`  (PK: `LockID`)       |

The backend is declared in `versions.tf`. The `key` is parameterized per environment via
`-backend-config` so prod and staging never share a state file.

## Workspaces / environments

We use **separate state keys per environment** (driven by `terraform.workspace` and the
`-backend-config` key) plus a per-env `*.tfvars`. This keeps blast radius small and lets
prod and staging diverge in sizing without code forks.

```bash
# one-time per env: select/create the workspace
terraform workspace new production    # or: terraform workspace select production
```

## Applying

```bash
# 1. Initialise with the env-specific state key
terraform init \
  -backend-config="bucket=aizorix-tfstate-us-east-1" \
  -backend-config="key=production/terraform.tfstate" \
  -backend-config="region=us-east-1" \
  -backend-config="dynamodb_table=aizorix-tflock" \
  -backend-config="encrypt=true"

# 2. Select the matching workspace
terraform workspace select production

# 3. Plan with the env var-file
terraform plan  -var-file=environments/production.tfvars -out=tfplan

# 4. Apply the reviewed plan (never apply without a saved plan in prod)
terraform apply tfplan
```

Staging is identical with `key=staging/terraform.tfstate`, workspace `staging`, and
`-var-file=environments/staging.tfvars`.

## Conventions

- All resources are tagged via `default_tags` on the provider (see `versions.tf` /
  `main.tf`); module-specific tags are merged on top.
- Names are prefixed `aizorix-<env>-...` so prod and staging are unambiguous in the console.
- Secrets are **never** stored in `.tfvars` or state in plaintext — they are generated into
  Secrets Manager (see `modules/secrets`) and read by workloads via IRSA + External Secrets.
- Destructive changes (RDS, KMS) have `prevent_destroy` / deletion protection on in prod.
