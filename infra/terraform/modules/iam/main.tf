# Per-service IRSA roles. Every service gets a baseline role that its Kubernetes
# ServiceAccount assumes (trust scoped to the exact namespace/SA via the OIDC sub claim).
# On top of the baseline, specific services receive narrowly-scoped extra policies:
#   * screenshot -> read/write its S3 prefix + use the screenshots CMK + sign CloudFront URLs
#   * auth       -> kms:Sign/Verify/GetPublicKey on the token-signing CMK
#   * every svc  -> read ONLY its own Secrets Manager secret + Kafka IAM on its topic prefix
#
# Principle: a role can never touch another service's data. Topic / secret / S3-prefix scoping
# is by naming convention "<service>.*" / "<prefix>-<service>-*".

variable "name_prefix" {
  type        = string
  description = "Resource name prefix (also the k8s namespace base + topic prefix)."
}
variable "services" {
  type        = list(string)
  description = "Service names that each get an IRSA role."
}
variable "oidc_provider_arn" {
  type        = string
  description = "EKS OIDC provider ARN."
}
variable "oidc_provider_url" {
  type        = string
  description = "EKS OIDC issuer URL (no scheme)."
}
variable "screenshots_bucket_arn" {
  type = string
}
variable "assets_bucket_arn" {
  type = string
}
variable "backups_bucket_arn" {
  type = string
}
variable "screenshots_kms_arn" {
  type = string
}
variable "token_signing_kms_arn" {
  type = string
}
variable "secrets_kms_arn" {
  type = string
}
variable "msk_cluster_arn" {
  type = string
}
variable "secret_arns" {
  type        = map(string)
  description = "Map of service name -> its Secrets Manager secret ARN."
}
variable "tags" {
  type    = map(string)
  default = {}
}

data "aws_caller_identity" "current" {}
data "aws_region" "current" {}

locals {
  # Kubernetes namespace each service runs in. Convention: namespace == "aizorix".
  namespace = "aizorix"

  # Build topic/group resource-ARN bases from the cluster ARN. MSK cluster ARNs look like:
  #   arn:aws:kafka:<region>:<acct>:cluster/<cluster-name>/<uuid>
  # The matching topic/group ARN replaces "cluster/" with "topic/"|"group/" and ends with
  # "/<cluster-name>/<topic-or-group-name>". We keep the prefix up to and including the
  # cluster name, then append the per-service wildcard in the policy below.
  msk_arn_parts   = split(":cluster/", var.msk_cluster_arn) # [arn-prefix, "name/uuid"]
  msk_arn_prefix  = local.msk_arn_parts[0]                  # arn:aws:kafka:region:acct
  msk_cluster_seg = split("/", local.msk_arn_parts[1])[0]   # cluster-name
  msk_topic_base  = "${local.msk_arn_prefix}:topic/${local.msk_cluster_seg}"
  msk_group_base  = "${local.msk_arn_prefix}:group/${local.msk_cluster_seg}"
}

# Trust policy generator: only the named namespace/serviceaccount may assume the role, and
# only with the sts.amazonaws.com audience.
data "aws_iam_policy_document" "assume" {
  for_each = toset(var.services)
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [var.oidc_provider_arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${var.oidc_provider_url}:sub"
      values   = ["system:serviceaccount:${local.namespace}:${each.value}"]
    }
    condition {
      test     = "StringEquals"
      variable = "${var.oidc_provider_url}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "service" {
  for_each           = toset(var.services)
  name               = "${var.name_prefix}-irsa-${each.value}"
  assume_role_policy = data.aws_iam_policy_document.assume[each.value].json
  tags               = merge(var.tags, { Service = each.value })
}

############################
# Baseline: read own secret + own Kafka topic prefix
############################

data "aws_iam_policy_document" "baseline" {
  for_each = toset(var.services)

  # Read only this service's secret (and decrypt it).
  statement {
    sid       = "ReadOwnSecret"
    effect    = "Allow"
    actions   = ["secretsmanager:GetSecretValue", "secretsmanager:DescribeSecret"]
    resources = [var.secret_arns[each.value]]
  }
  statement {
    sid       = "DecryptSecret"
    effect    = "Allow"
    actions   = ["kms:Decrypt"]
    resources = [var.secrets_kms_arn]
    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["secretsmanager.${data.aws_region.current.name}.amazonaws.com"]
    }
  }

  # Kafka (MSK IAM): connect to the cluster, and produce/consume only on topics/groups
  # prefixed with this service's name. Cluster ARN form:
  #   arn:aws:kafka:region:acct:cluster/<name>/<uuid>
  # Topic/group ARNs replace "cluster" with "topic"/"group" and append the resource name.
  statement {
    sid       = "KafkaConnect"
    effect    = "Allow"
    actions   = ["kafka-cluster:Connect", "kafka-cluster:DescribeCluster"]
    resources = [var.msk_cluster_arn]
  }
  statement {
    sid    = "KafkaTopicRW"
    effect = "Allow"
    actions = [
      "kafka-cluster:DescribeTopic", "kafka-cluster:ReadData", "kafka-cluster:WriteData",
      "kafka-cluster:CreateTopic", "kafka-cluster:DescribeTopicDynamicConfiguration",
    ]
    resources = ["${local.msk_topic_base}/${each.value}.*"]
  }
  statement {
    sid       = "KafkaGroup"
    effect    = "Allow"
    actions   = ["kafka-cluster:AlterGroup", "kafka-cluster:DescribeGroup"]
    resources = ["${local.msk_group_base}/${each.value}.*"]
  }
}

resource "aws_iam_role_policy" "baseline" {
  for_each = toset(var.services)
  name     = "baseline"
  role     = aws_iam_role.service[each.value].id
  policy   = data.aws_iam_policy_document.baseline[each.value].json
}

############################
# screenshot service: scoped S3 + KMS
############################

data "aws_iam_policy_document" "screenshot" {
  statement {
    sid       = "ListScreenshotsBucket"
    effect    = "Allow"
    actions   = ["s3:ListBucket"]
    resources = [var.screenshots_bucket_arn]
  }
  # Read/write objects, and manage Object Lock retention for evidence.
  statement {
    sid    = "ObjectRW"
    effect = "Allow"
    actions = [
      "s3:GetObject", "s3:PutObject", "s3:GetObjectVersion",
      "s3:PutObjectRetention", "s3:GetObjectRetention",
    ]
    resources = ["${var.screenshots_bucket_arn}/*"]
  }
  # Envelope encryption: generate/decrypt data keys under the screenshots CMK only.
  statement {
    sid       = "ScreenshotKms"
    effect    = "Allow"
    actions   = ["kms:GenerateDataKey", "kms:Decrypt", "kms:DescribeKey"]
    resources = [var.screenshots_kms_arn]
  }
}

resource "aws_iam_role_policy" "screenshot" {
  count  = contains(var.services, "screenshot") ? 1 : 0
  name   = "screenshot-s3-kms"
  role   = aws_iam_role.service["screenshot"].id
  policy = data.aws_iam_policy_document.screenshot.json
}

############################
# auth service: token signing via KMS (no key export)
############################

data "aws_iam_policy_document" "auth" {
  statement {
    sid       = "TokenSigning"
    effect    = "Allow"
    actions   = ["kms:Sign", "kms:Verify", "kms:GetPublicKey", "kms:DescribeKey"]
    resources = [var.token_signing_kms_arn]
  }
}

resource "aws_iam_role_policy" "auth" {
  count  = contains(var.services, "auth") ? 1 : 0
  name   = "auth-token-signing"
  role   = aws_iam_role.service["auth"].id
  policy = data.aws_iam_policy_document.auth.json
}

############################
# admin / analytics: read access to backups bucket (exports)
############################

data "aws_iam_policy_document" "backups_read" {
  statement {
    effect    = "Allow"
    actions   = ["s3:GetObject", "s3:ListBucket"]
    resources = [var.backups_bucket_arn, "${var.backups_bucket_arn}/*"]
  }
}

resource "aws_iam_role_policy" "analytics_backups" {
  count  = contains(var.services, "analytics") ? 1 : 0
  name   = "analytics-backups-read"
  role   = aws_iam_role.service["analytics"].id
  policy = data.aws_iam_policy_document.backups_read.json
}

############################
# Outputs
############################

output "role_arns" {
  description = "Map of service -> IRSA role ARN (used in k8s ServiceAccount annotations)."
  value       = { for s in var.services : s => aws_iam_role.service[s].arn }
}
