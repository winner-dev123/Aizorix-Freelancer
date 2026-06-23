# CloudFront distribution fronting:
#   * /         -> assets bucket (web static assets) via Origin Access Control (OAC)
#   * /screenshots/* -> screenshots bucket, with a trusted key group so only SIGNED URLs
#                       (minted by the screenshot service) can fetch evidence images.
#
# A WAFv2 web ACL (CLOUDFRONT scope) is associated for L7 protection. CloudFront, WAFv2
# (CLOUDFRONT) and their ACM cert all MUST live in us-east-1 — hence the provider alias.

terraform {
  required_providers {
    aws = {
      source                = "hashicorp/aws"
      configuration_aliases = [aws.us_east_1]
    }
  }
}

variable "name_prefix" {
  type        = string
  description = "Resource name prefix."
}
variable "web_domain" {
  type        = string
  description = "Custom domain (CNAME alias). Empty => use the default *.cloudfront.net."
  default     = ""
}
variable "acm_certificate_arn" {
  type        = string
  description = "ACM cert ARN in us-east-1 for web_domain (empty => CloudFront default cert)."
  default     = ""
}
variable "assets_bucket_id" {
  type        = string
  description = "Assets bucket name (for the OAC bucket policy)."
}
variable "assets_bucket_domain" {
  type        = string
  description = "Assets bucket regional domain (origin)."
}
variable "screenshots_bucket_id" {
  type        = string
  description = "Screenshots bucket name (for the OAC bucket policy)."
}
variable "screenshots_bucket_domain" {
  type        = string
  description = "Screenshots bucket regional domain (origin)."
}
variable "tags" {
  type    = map(string)
  default = {}
}

data "aws_caller_identity" "current" {}

############################
# WAF (CLOUDFRONT scope -> must be in us-east-1)
############################

resource "aws_wafv2_web_acl" "this" {
  provider    = aws.us_east_1
  name        = "${var.name_prefix}-cf-waf"
  description = "L7 protection for the public edge"
  scope       = "CLOUDFRONT"

  default_action {
    allow {}
  }

  # AWS managed common rule set (OWASP-ish) + known bad inputs + rate limit.
  rule {
    name     = "AWSManagedCommon"
    priority = 1
    override_action {
      none {}
    }
    statement {
      managed_rule_group_statement {
        vendor_name = "AWS"
        name        = "AWSManagedRulesCommonRuleSet"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "common"
      sampled_requests_enabled   = true
    }
  }

  rule {
    name     = "AWSManagedBadInputs"
    priority = 2
    override_action {
      none {}
    }
    statement {
      managed_rule_group_statement {
        vendor_name = "AWS"
        name        = "AWSManagedRulesKnownBadInputsRuleSet"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "badinputs"
      sampled_requests_enabled   = true
    }
  }

  # Per-IP rate limit (2000 req / 5 min) to blunt scraping / brute force at the edge.
  rule {
    name     = "RateLimit"
    priority = 3
    action {
      block {}
    }
    statement {
      rate_based_statement {
        limit              = 2000
        aggregate_key_type = "IP"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "ratelimit"
      sampled_requests_enabled   = true
    }
  }

  visibility_config {
    cloudwatch_metrics_enabled = true
    metric_name                = "${var.name_prefix}-cf-waf"
    sampled_requests_enabled   = true
  }
  tags = var.tags
}

############################
# Origin Access Control + signed-URL key group
############################

resource "aws_cloudfront_origin_access_control" "this" {
  name                              = "${var.name_prefix}-oac"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

# Public key + key group used to verify signed screenshot URLs. The matching PRIVATE key is
# generated/stored in Secrets Manager (out of band) and used by the screenshot service to
# sign time-limited download URLs. We generate a keypair here for bootstrap convenience.
resource "tls_private_key" "signing" {
  algorithm = "RSA"
  rsa_bits  = 2048
}

resource "aws_cloudfront_public_key" "screenshots" {
  name        = "${var.name_prefix}-screenshot-signing"
  encoded_key = tls_private_key.signing.public_key_pem
  comment     = "Verifies signed screenshot download URLs"
}

resource "aws_cloudfront_key_group" "screenshots" {
  name  = "${var.name_prefix}-screenshot-keys"
  items = [aws_cloudfront_public_key.screenshots.id]
}

############################
# Distribution
############################

resource "aws_cloudfront_distribution" "this" {
  enabled         = true
  is_ipv6_enabled = true
  comment         = "${var.name_prefix} edge"
  price_class     = "PriceClass_100"
  web_acl_id      = aws_wafv2_web_acl.this.arn
  aliases         = var.web_domain == "" ? [] : [var.web_domain]

  origin {
    origin_id                = "assets"
    domain_name              = var.assets_bucket_domain
    origin_access_control_id = aws_cloudfront_origin_access_control.this.id
  }

  origin {
    origin_id                = "screenshots"
    domain_name              = var.screenshots_bucket_domain
    origin_access_control_id = aws_cloudfront_origin_access_control.this.id
  }

  # Default: public web assets, cached, no signing required.
  default_cache_behavior {
    target_origin_id       = "assets"
    viewer_protocol_policy = "redirect-to-https"
    allowed_methods        = ["GET", "HEAD", "OPTIONS"]
    cached_methods         = ["GET", "HEAD"]
    compress               = true
    # CachingOptimized managed policy.
    cache_policy_id = "658327ea-f89d-4fab-a63d-7e88639e58f6"
  }

  # Screenshots: require a valid signed URL (trusted key group) → evidence is not public.
  ordered_cache_behavior {
    path_pattern           = "/screenshots/*"
    target_origin_id       = "screenshots"
    viewer_protocol_policy = "https-only"
    allowed_methods        = ["GET", "HEAD"]
    cached_methods         = ["GET", "HEAD"]
    compress               = false
    trusted_key_groups     = [aws_cloudfront_key_group.screenshots.id]
    # CachingDisabled — evidence URLs are short-lived and per-user.
    cache_policy_id = "4135ea2d-6df8-44a3-9df3-4b5a84be39ad"
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    cloudfront_default_certificate = var.acm_certificate_arn == "" ? true : false
    acm_certificate_arn            = var.acm_certificate_arn == "" ? null : var.acm_certificate_arn
    ssl_support_method             = var.acm_certificate_arn == "" ? null : "sni-only"
    # Never permit below TLS 1.2. NOTE: the CloudFront *default* certificate ignores this and
    # forces TLSv1 — provision an ACM cert (set acm_certificate_arn) for prod to enforce 1.2+.
    minimum_protocol_version = "TLSv1.2_2021"
  }

  tags = var.tags
}

############################
# Bucket policies granting CloudFront (this distribution only) read via OAC
############################

data "aws_iam_policy_document" "assets_oac" {
  statement {
    sid       = "AllowCloudFrontRead"
    effect    = "Allow"
    actions   = ["s3:GetObject"]
    resources = ["arn:aws:s3:::${var.assets_bucket_id}/*"]
    principals {
      type        = "Service"
      identifiers = ["cloudfront.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "AWS:SourceArn"
      values   = [aws_cloudfront_distribution.this.arn]
    }
  }
}

resource "aws_s3_bucket_policy" "assets" {
  bucket = var.assets_bucket_id
  policy = data.aws_iam_policy_document.assets_oac.json
}

data "aws_iam_policy_document" "screenshots_oac" {
  statement {
    sid       = "AllowCloudFrontRead"
    effect    = "Allow"
    actions   = ["s3:GetObject"]
    resources = ["arn:aws:s3:::${var.screenshots_bucket_id}/*"]
    principals {
      type        = "Service"
      identifiers = ["cloudfront.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "AWS:SourceArn"
      values   = [aws_cloudfront_distribution.this.arn]
    }
  }
}

resource "aws_s3_bucket_policy" "screenshots" {
  bucket = var.screenshots_bucket_id
  policy = data.aws_iam_policy_document.screenshots_oac.json
}

############################
# Outputs
############################

output "distribution_domain_name" {
  value       = aws_cloudfront_distribution.this.domain_name
  description = "CloudFront domain name."
}
output "distribution_id" {
  value = aws_cloudfront_distribution.this.id
}
output "signing_private_key_pem" {
  value       = tls_private_key.signing.private_key_pem
  sensitive   = true
  description = "Private key for signing screenshot URLs — store in Secrets Manager."
}
output "signing_public_key_id" {
  value       = aws_cloudfront_public_key.screenshots.id
  description = "CloudFront public key id used as the Key-Pair-Id in signed URLs."
}
