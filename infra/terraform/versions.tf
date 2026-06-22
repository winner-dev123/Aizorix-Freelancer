# Terraform + provider version constraints and remote state backend.
#
# Pins are intentionally narrow (~>) so `terraform init` is reproducible across the team
# and CI, while still allowing automatic patch upgrades.

terraform {
  required_version = "~> 1.8"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.60"
    }
    # A second aws provider alias (us-east-1) is needed because CloudFront/WAFv2 (CLOUDFRONT
    # scope) and ACM certs for CloudFront must live in us-east-1 regardless of the app region.
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.31"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.14"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
  }

  # Remote state: versioned, encrypted S3 bucket + DynamoDB lock.
  # The concrete bucket/key/table are supplied at `init` time via -backend-config so the
  # same code serves every environment (see README). Only static, non-secret defaults here.
  backend "s3" {
    # bucket         = "aizorix-tfstate-us-east-1"   # via -backend-config
    # key            = "production/terraform.tfstate" # via -backend-config (per env)
    # dynamodb_table = "aizorix-tflock"               # via -backend-config
    region       = "us-east-1"
    encrypt      = true
    use_lockfile = false # rely on DynamoDB lock table
  }
}
