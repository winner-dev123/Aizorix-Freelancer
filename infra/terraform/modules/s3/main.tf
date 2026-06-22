# Object storage buckets:
#   screenshots — encrypted desktop-tracker screenshots. Versioned, SSE-KMS, Object Lock
#                 (compliance/WORM for dispute evidence), lifecycle to Glacier, and
#                 cross-region replication to the DR region.
#   assets      — public-ish web/static assets fronted by CloudFront (OAC only, no public ACL).
#   backups     — DB dumps / exports, lifecycle to deep archive.
#
# All buckets block public access at the account-object level; access is via IAM/IRSA + CloudFront OAC.

terraform {
  required_providers {
    aws = {
      source                = "hashicorp/aws"
      configuration_aliases = [aws.dr]
    }
  }
}

variable "name_prefix" {
  type        = string
  description = "Resource name prefix (also bucket-name base)."
}
variable "screenshots_kms_arn" {
  type        = string
  description = "CMK for screenshot bucket SSE-KMS."
}
variable "dr_kms_key_arn" {
  type        = string
  description = "DR-region CMK for screenshot CRR destination."
}
variable "tags" {
  type        = map(string)
  default     = {}
}

locals {
  screenshots_bucket    = "${var.name_prefix}-screenshots"
  screenshots_dr_bucket = "${var.name_prefix}-screenshots-dr"
  assets_bucket         = "${var.name_prefix}-assets"
  backups_bucket        = "${var.name_prefix}-backups"
}

############################
# Screenshots bucket (primary)
############################

resource "aws_s3_bucket" "screenshots" {
  bucket = local.screenshots_bucket
  # Object Lock must be enabled at creation; required for WORM retention of evidence.
  object_lock_enabled = true
  tags                = merge(var.tags, { Name = local.screenshots_bucket, DataClass = "sensitive" })
}

resource "aws_s3_bucket_public_access_block" "screenshots" {
  bucket                  = aws_s3_bucket.screenshots.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "screenshots" {
  bucket = aws_s3_bucket.screenshots.id
  versioning_configuration {
    status = "Enabled" # required for both Object Lock and CRR
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "screenshots" {
  bucket = aws_s3_bucket.screenshots.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = var.screenshots_kms_arn
    }
    bucket_key_enabled = true # reduces KMS API cost on high-volume screenshot writes
  }
}

# Default WORM retention: 30 days COMPLIANCE so evidence cannot be altered/deleted, even by
# admins, during an active dispute window.
resource "aws_s3_bucket_object_lock_configuration" "screenshots" {
  bucket = aws_s3_bucket.screenshots.id
  rule {
    default_retention {
      mode = "COMPLIANCE"
      days = 30
    }
  }
}

# Lifecycle: transition to cheaper tiers as evidence ages, expire old noncurrent versions.
resource "aws_s3_bucket_lifecycle_configuration" "screenshots" {
  bucket = aws_s3_bucket.screenshots.id
  rule {
    id     = "tier-down-and-expire"
    status = "Enabled"
    filter {}
    transition {
      days          = 90
      storage_class = "STANDARD_IA"
    }
    transition {
      days          = 180
      storage_class = "GLACIER"
    }
    expiration {
      days = 730 # 2-year retention for hourly-work evidence
    }
    noncurrent_version_expiration {
      noncurrent_days = 30
    }
  }
}

############################
# Screenshots bucket (DR / replication destination)
############################

resource "aws_s3_bucket" "screenshots_dr" {
  provider            = aws.dr
  bucket              = local.screenshots_dr_bucket
  object_lock_enabled = true
  tags                = merge(var.tags, { Name = local.screenshots_dr_bucket, DataClass = "sensitive" })
}

resource "aws_s3_bucket_public_access_block" "screenshots_dr" {
  provider                = aws.dr
  bucket                  = aws_s3_bucket.screenshots_dr.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "screenshots_dr" {
  provider = aws.dr
  bucket   = aws_s3_bucket.screenshots_dr.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "screenshots_dr" {
  provider = aws.dr
  bucket   = aws_s3_bucket.screenshots_dr.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = var.dr_kms_key_arn
    }
    bucket_key_enabled = true
  }
}

############################
# CRR: primary -> DR replication role + config
############################

data "aws_iam_policy_document" "replication_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["s3.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "replication" {
  name               = "${var.name_prefix}-s3-replication"
  assume_role_policy = data.aws_iam_policy_document.replication_assume.json
  tags               = var.tags
}

data "aws_iam_policy_document" "replication" {
  statement {
    sid     = "SourceRead"
    effect  = "Allow"
    actions = ["s3:GetReplicationConfiguration", "s3:ListBucket", "s3:GetObjectVersionForReplication", "s3:GetObjectVersionAcl", "s3:GetObjectVersionTagging"]
    resources = [aws_s3_bucket.screenshots.arn, "${aws_s3_bucket.screenshots.arn}/*"]
  }
  statement {
    sid       = "DestWrite"
    effect    = "Allow"
    actions   = ["s3:ReplicateObject", "s3:ReplicateDelete", "s3:ReplicateTags", "s3:ObjectOwnerOverrideToBucketOwner"]
    resources = ["${aws_s3_bucket.screenshots_dr.arn}/*"]
  }
  statement {
    sid       = "KmsDecryptSource"
    effect    = "Allow"
    actions   = ["kms:Decrypt"]
    resources = [var.screenshots_kms_arn]
  }
  statement {
    sid       = "KmsEncryptDest"
    effect    = "Allow"
    actions   = ["kms:Encrypt", "kms:GenerateDataKey"]
    resources = [var.dr_kms_key_arn]
  }
}

resource "aws_iam_role_policy" "replication" {
  name   = "${var.name_prefix}-s3-replication"
  role   = aws_iam_role.replication.id
  policy = data.aws_iam_policy_document.replication.json
}

resource "aws_s3_bucket_replication_configuration" "screenshots" {
  # Replication requires versioning to exist first.
  depends_on = [aws_s3_bucket_versioning.screenshots, aws_s3_bucket_versioning.screenshots_dr]
  role       = aws_iam_role.replication.arn
  bucket     = aws_s3_bucket.screenshots.id

  rule {
    id     = "replicate-all-to-dr"
    status = "Enabled"
    filter {}
    delete_marker_replication {
      status = "Enabled"
    }
    source_selection_criteria {
      sse_kms_encrypted_objects {
        status = "Enabled" # replicate KMS-encrypted objects
      }
    }
    destination {
      bucket        = aws_s3_bucket.screenshots_dr.arn
      storage_class = "STANDARD_IA"
      encryption_configuration {
        replica_kms_key_id = var.dr_kms_key_arn
      }
    }
  }
}

############################
# Assets bucket (CloudFront origin via OAC)
############################

resource "aws_s3_bucket" "assets" {
  bucket = local.assets_bucket
  tags   = merge(var.tags, { Name = local.assets_bucket })
}

resource "aws_s3_bucket_public_access_block" "assets" {
  bucket                  = aws_s3_bucket.assets.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "assets" {
  bucket = aws_s3_bucket.assets.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "assets" {
  bucket = aws_s3_bucket.assets.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256" # SSE-S3 is fine for non-sensitive public assets
    }
  }
}

############################
# Backups bucket
############################

resource "aws_s3_bucket" "backups" {
  bucket = local.backups_bucket
  tags   = merge(var.tags, { Name = local.backups_bucket })
}

resource "aws_s3_bucket_public_access_block" "backups" {
  bucket                  = aws_s3_bucket.backups.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "backups" {
  bucket = aws_s3_bucket.backups.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "backups" {
  bucket = aws_s3_bucket.backups.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = var.screenshots_kms_arn
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "backups" {
  bucket = aws_s3_bucket.backups.id
  rule {
    id     = "archive-backups"
    status = "Enabled"
    filter {}
    transition {
      days          = 30
      storage_class = "GLACIER"
    }
    transition {
      days          = 180
      storage_class = "DEEP_ARCHIVE"
    }
    expiration {
      days = 2555 # ~7y for financial/audit retention
    }
  }
}

############################
# Outputs
############################

output "screenshots_bucket_id" {
  value = aws_s3_bucket.screenshots.id
}
output "screenshots_bucket_arn" {
  value = aws_s3_bucket.screenshots.arn
}
output "screenshots_bucket_regional_domain" {
  value = aws_s3_bucket.screenshots.bucket_regional_domain_name
}
output "assets_bucket_id" {
  value = aws_s3_bucket.assets.id
}
output "assets_bucket_arn" {
  value = aws_s3_bucket.assets.arn
}
output "assets_bucket_regional_domain" {
  value = aws_s3_bucket.assets.bucket_regional_domain_name
}
output "backups_bucket_arn" {
  value = aws_s3_bucket.backups.arn
}
