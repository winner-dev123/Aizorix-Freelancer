# Customer-managed CMKs, one per data-protection domain so blast radius and key policies
# are isolated. All have automatic annual rotation enabled. Aliases make them discoverable.
#
# Keys:
#   rds            — RDS/ElastiCache/MSK storage encryption (primary region)
#   rds_dr         — same, in the DR region for the cross-region read replica
#   screenshots    — S3 SSE-KMS for the encrypted-screenshot bucket (envelope encryption)
#   screenshots_dr — DR-region key for S3 cross-region replication of screenshots
#   token_signing  — JWT/PASETO token-signing material (used via KMS Sign/Verify, no export)
#   secrets        — Secrets Manager encryption

data "aws_caller_identity" "current" {}

locals {
  account_root = "arn:aws:iam::${data.aws_caller_identity.current.account_id}:root"
}

# Base key policy: account root retains full admin (so you can never lock yourself out),
# plus explicit admin principals. Service-level grants are added by consumers / the iam module.
data "aws_iam_policy_document" "key_policy" {
  statement {
    sid       = "EnableRootAccountAdmin"
    effect    = "Allow"
    actions   = ["kms:*"]
    resources = ["*"]
    principals {
      type        = "AWS"
      identifiers = [local.account_root]
    }
  }

  dynamic "statement" {
    for_each = length(var.admin_principals) > 0 ? [1] : []
    content {
      sid    = "AllowKeyAdministration"
      effect = "Allow"
      actions = [
        "kms:Create*", "kms:Describe*", "kms:Enable*", "kms:List*", "kms:Put*",
        "kms:Update*", "kms:Revoke*", "kms:Disable*", "kms:Get*", "kms:Delete*",
        "kms:TagResource", "kms:UntagResource", "kms:ScheduleKeyDeletion", "kms:CancelKeyDeletion",
      ]
      resources = ["*"]
      principals {
        type        = "AWS"
        identifiers = var.admin_principals
      }
    }
  }
}

resource "aws_kms_key" "rds" {
  description             = "${var.name_prefix} RDS/ElastiCache/MSK storage encryption"
  deletion_window_in_days = 30
  enable_key_rotation     = true
  policy                  = data.aws_iam_policy_document.key_policy.json
  tags                    = merge(var.tags, { Name = "${var.name_prefix}-rds" })
}

resource "aws_kms_alias" "rds" {
  name          = "alias/${var.name_prefix}-rds"
  target_key_id = aws_kms_key.rds.key_id
}

# DR-region key. Provider alias is passed in by the caller via configuration_aliases.
resource "aws_kms_key" "rds_dr" {
  provider                = aws.dr
  description             = "${var.name_prefix} RDS storage encryption (DR region)"
  deletion_window_in_days = 30
  enable_key_rotation     = true
  policy                  = data.aws_iam_policy_document.key_policy.json
  tags                    = merge(var.tags, { Name = "${var.name_prefix}-rds-dr" })
}

resource "aws_kms_alias" "rds_dr" {
  provider      = aws.dr
  name          = "alias/${var.name_prefix}-rds-dr"
  target_key_id = aws_kms_key.rds_dr.key_id
}

resource "aws_kms_key" "screenshots" {
  description             = "${var.name_prefix} S3 screenshot envelope encryption"
  deletion_window_in_days = 30
  enable_key_rotation     = true
  policy                  = data.aws_iam_policy_document.key_policy.json
  tags                    = merge(var.tags, { Name = "${var.name_prefix}-screenshots" })
}

resource "aws_kms_alias" "screenshots" {
  name          = "alias/${var.name_prefix}-screenshots"
  target_key_id = aws_kms_key.screenshots.key_id
}

resource "aws_kms_key" "screenshots_dr" {
  provider                = aws.dr
  description             = "${var.name_prefix} S3 screenshot encryption (DR region, CRR)"
  deletion_window_in_days = 30
  enable_key_rotation     = true
  policy                  = data.aws_iam_policy_document.key_policy.json
  tags                    = merge(var.tags, { Name = "${var.name_prefix}-screenshots-dr" })
}

resource "aws_kms_alias" "screenshots_dr" {
  provider      = aws.dr
  name          = "alias/${var.name_prefix}-screenshots-dr"
  target_key_id = aws_kms_key.screenshots_dr.key_id
}

# Token-signing key. ASYMMETRIC + SIGN_VERIFY so private material never leaves KMS; the auth
# service calls kms:Sign / kms:GetPublicKey. Rotation is not supported for asymmetric keys,
# so we rotate by issuing a new key + alias swap (operational runbook, not in-place).
resource "aws_kms_key" "token_signing" {
  description              = "${var.name_prefix} JWT token signing (asymmetric, sign/verify)"
  deletion_window_in_days  = 30
  key_usage                = "SIGN_VERIFY"
  customer_master_key_spec = "ECC_NIST_P256"
  policy                   = data.aws_iam_policy_document.key_policy.json
  tags                     = merge(var.tags, { Name = "${var.name_prefix}-token-signing" })
}

resource "aws_kms_alias" "token_signing" {
  name          = "alias/${var.name_prefix}-token-signing"
  target_key_id = aws_kms_key.token_signing.key_id
}

resource "aws_kms_key" "secrets" {
  description             = "${var.name_prefix} Secrets Manager encryption"
  deletion_window_in_days = 30
  enable_key_rotation     = true
  policy                  = data.aws_iam_policy_document.key_policy.json
  tags                    = merge(var.tags, { Name = "${var.name_prefix}-secrets" })
}

resource "aws_kms_alias" "secrets" {
  name          = "alias/${var.name_prefix}-secrets"
  target_key_id = aws_kms_key.secrets.key_id
}

# EBS encryption key for EKS node root/data volumes.
resource "aws_kms_key" "ebs" {
  description             = "${var.name_prefix} EBS volume encryption (EKS nodes)"
  deletion_window_in_days = 30
  enable_key_rotation     = true
  policy                  = data.aws_iam_policy_document.key_policy.json
  tags                    = merge(var.tags, { Name = "${var.name_prefix}-ebs" })
}

resource "aws_kms_alias" "ebs" {
  name          = "alias/${var.name_prefix}-ebs"
  target_key_id = aws_kms_key.ebs.key_id
}
