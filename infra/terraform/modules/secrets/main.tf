# Secrets Manager entries.
#
#   * One JSON secret per service ("<prefix>/<service>/config") holding that service's
#     connection strings + per-service generated secrets (e.g. an app-level signing salt).
#     Services read ONLY their own secret (enforced by the iam module) via External Secrets.
#   * Shared infra secrets (DB master creds, Redis AUTH) are referenced/embedded so each
#     service gets a ready-to-use config blob.
#
# Rotation: the RDS master password is already rotated by RDS (manage_master_user_password).
# For app-managed credentials we attach a Lambda rotation schedule placeholder (rotation
# Lambda ARN supplied per-env; left optional so the module applies cleanly without it).

variable "name_prefix" {
  type        = string
  description = "Resource name prefix (and secret path base)."
}
variable "services" {
  type        = list(string)
  description = "Services that each get a config secret."
}
variable "kms_key_arn" {
  type        = string
  description = "CMK to encrypt the secrets."
}
variable "rds_endpoint" {
  type = string
}
variable "rds_port" {
  type = number
}
variable "rds_master_secret_arn" {
  type        = string
  description = "ARN of the RDS-managed master credentials secret."
}
variable "redis_endpoint" {
  type = string
}
variable "msk_bootstrap_iam" {
  type        = string
  description = "MSK SASL/IAM bootstrap broker string."
}
variable "rotation_lambda_arn" {
  type        = string
  description = "Optional rotation Lambda ARN for app DB users."
  default     = ""
}
variable "tags" {
  type    = map(string)
  default = {}
}

# A small per-service random salt so each service has at least one unique generated secret.
resource "random_password" "app_salt" {
  for_each = toset(var.services)
  length   = 48
  special  = true
}

# Per-service config secret. Each service connects to its OWN logical database
# (<service> schema/db) using credentials provisioned out-of-band; here we ship the
# non-credential connection facts + a generated app salt. The DB password is read by the
# app from the RDS master secret reference (or a per-service user secret in a fuller setup).
resource "aws_secretsmanager_secret" "service" {
  for_each    = toset(var.services)
  name        = "${var.name_prefix}/${each.value}/config"
  description = "Config + generated secrets for the ${each.value} service"
  kms_key_id  = var.kms_key_arn
  tags        = merge(var.tags, { Service = each.value })
}

resource "aws_secretsmanager_secret_version" "service" {
  for_each  = toset(var.services)
  secret_id = aws_secretsmanager_secret.service[each.value].id
  secret_string = jsonencode({
    POSTGRES_HOST         = var.rds_endpoint
    POSTGRES_PORT         = tostring(var.rds_port)
    POSTGRES_DB           = "aizorix"
    POSTGRES_SCHEMA       = each.value
    RDS_MASTER_SECRET_ARN = var.rds_master_secret_arn
    REDIS_ADDR            = var.redis_endpoint
    KAFKA_BROKERS         = var.msk_bootstrap_iam
    KAFKA_SASL_MECHANISM  = "AWS_MSK_IAM"
    APP_SALT              = random_password.app_salt[each.value].result
    SERVICE_NAME          = each.value
  })
  lifecycle {
    # The app salt is generated once; don't churn it on every plan.
    ignore_changes = [secret_string]
  }
}

# Optional rotation schedule (only when a rotation Lambda is provided).
resource "aws_secretsmanager_secret_rotation" "service" {
  for_each            = var.rotation_lambda_arn == "" ? toset([]) : toset(var.services)
  secret_id           = aws_secretsmanager_secret.service[each.value].id
  rotation_lambda_arn = var.rotation_lambda_arn
  rotation_rules {
    automatically_after_days = 30
  }
}

output "service_secret_arns" {
  description = "Map of service -> config secret ARN (consumed by the iam module)."
  value       = { for s in var.services : s => aws_secretsmanager_secret.service[s].arn }
}
