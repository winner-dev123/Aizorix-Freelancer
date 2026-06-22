# Composition root. This file wires the modules together and is the ONLY place that knows
# about the full topology. Modules receive explicit inputs and never reference each other.

locals {
  name = "${var.project}-${var.environment}" # e.g. aizorix-production

  # Tags applied everywhere via provider default_tags + merged into module tags.
  common_tags = merge({
    Project     = var.project
    Environment = var.environment
    ManagedBy   = "terraform"
    Repo        = "aizorix/infra"
  }, var.tags)

  # The list of services that each get their own IRSA role + Secrets Manager entries.
  services = [
    "auth", "user", "project", "proposal", "contract", "timetracking",
    "screenshot", "payment", "escrow", "review", "messaging",
    "notification", "search", "fraud", "admin", "analytics",
  ]
}

############################
# Providers
############################

provider "aws" {
  region = var.region
  default_tags {
    tags = local.common_tags
  }
}

# us-east-1 alias: CloudFront, WAFv2 (CLOUDFRONT scope) and their ACM certs must be here.
provider "aws" {
  alias  = "us_east_1"
  region = "us-east-1"
  default_tags {
    tags = local.common_tags
  }
}

# DR region for the S3 CRR destination + cross-region RDS replica.
provider "aws" {
  alias  = "dr"
  region = var.dr_region
  default_tags {
    tags = local.common_tags
  }
}

# Kubernetes/Helm providers authenticate against the cluster created below. Using exec auth
# avoids storing a long-lived token in state.
provider "kubernetes" {
  host                   = module.eks.cluster_endpoint
  cluster_ca_certificate = base64decode(module.eks.cluster_ca_data)
  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    command     = "aws"
    args        = ["eks", "get-token", "--cluster-name", module.eks.cluster_name, "--region", var.region]
  }
}

provider "helm" {
  kubernetes {
    host                   = module.eks.cluster_endpoint
    cluster_ca_certificate = base64decode(module.eks.cluster_ca_data)
    exec {
      api_version = "client.authentication.k8s.io/v1beta1"
      command     = "aws"
      args        = ["eks", "get-token", "--cluster-name", module.eks.cluster_name, "--region", var.region]
    }
  }
}

data "aws_caller_identity" "current" {}

############################
# KMS — created first; almost everything else consumes a key from here.
############################

module "kms" {
  source = "./modules/kms"
  providers = {
    aws    = aws
    aws.dr = aws.dr
  }

  name_prefix = local.name
  # Grant the EKS node/IRSA principals usage of the data keys at the account root level;
  # fine-grained per-service grants are added in the iam module.
  admin_principals = concat(var.cluster_admin_principals, [data.aws_caller_identity.current.arn])
  tags             = local.common_tags
}

############################
# Networking
############################

module "vpc" {
  source = "./modules/vpc"

  name_prefix        = local.name
  cidr               = var.vpc_cidr
  az_count           = var.az_count
  single_nat_gateway = var.single_nat_gateway
  region             = var.region
  tags               = local.common_tags
}

############################
# EKS
############################

module "eks" {
  source = "./modules/eks"

  name_prefix        = local.name
  kubernetes_version = var.kubernetes_version
  vpc_id             = module.vpc.vpc_id
  # Control plane ENIs + worker nodes live in private subnets; pods reach AWS via VPC
  # endpoints and the NAT gateways.
  subnet_ids               = module.vpc.private_subnet_ids
  control_plane_subnet_ids = module.vpc.intra_subnet_ids
  node_groups              = var.node_groups
  cluster_admin_principals = var.cluster_admin_principals
  ebs_kms_key_arn          = module.kms.ebs_key_arn
  tags                     = local.common_tags
}

############################
# Data stores
############################

module "rds" {
  source = "./modules/rds"
  providers = {
    aws    = aws
    aws.dr = aws.dr
  }

  name_prefix              = local.name
  engine_version           = var.rds_engine_version
  instance_class           = var.rds_instance_class
  allocated_storage        = var.rds_allocated_storage
  max_allocated_storage    = var.rds_max_allocated_storage
  multi_az                 = var.rds_multi_az
  create_cross_region_replica = var.rds_create_cross_region_replica
  deletion_protection      = var.rds_deletion_protection
  backup_retention_days    = var.rds_backup_retention_days
  vpc_id                   = module.vpc.vpc_id
  subnet_ids               = module.vpc.intra_subnet_ids # DB has no internet path
  allowed_security_group_ids = [module.eks.node_security_group_id]
  kms_key_arn              = module.kms.rds_key_arn
  dr_kms_key_arn           = module.kms.rds_dr_key_arn
  tags                     = local.common_tags
}

module "elasticache" {
  source = "./modules/elasticache"

  name_prefix                = local.name
  node_type                  = var.redis_node_type
  num_node_groups            = var.redis_num_node_groups
  replicas_per_node_group    = var.redis_replicas_per_node_group
  vpc_id                     = module.vpc.vpc_id
  subnet_ids                 = module.vpc.intra_subnet_ids
  allowed_security_group_ids = [module.eks.node_security_group_id]
  kms_key_arn                = module.kms.rds_key_arn # reuse the data-store CMK
  tags                       = local.common_tags
}

module "msk" {
  source = "./modules/msk"

  name_prefix                = local.name
  kafka_version              = var.msk_kafka_version
  broker_instance_type       = var.msk_broker_instance_type
  broker_count               = var.msk_broker_count
  broker_ebs_gb              = var.msk_broker_ebs_gb
  vpc_id                     = module.vpc.vpc_id
  subnet_ids                 = module.vpc.private_subnet_ids
  allowed_security_group_ids = [module.eks.node_security_group_id]
  kms_key_arn                = module.kms.rds_key_arn
  tags                       = local.common_tags
}

############################
# Object storage + CDN
############################

module "s3" {
  source = "./modules/s3"
  providers = {
    aws    = aws
    aws.dr = aws.dr
  }

  name_prefix         = local.name
  screenshots_kms_arn = module.kms.screenshots_key_arn
  dr_kms_key_arn      = module.kms.screenshots_dr_key_arn
  tags                = local.common_tags
}

module "cloudfront" {
  source = "./modules/cloudfront"
  providers = {
    aws.us_east_1 = aws.us_east_1
  }

  name_prefix         = local.name
  web_domain          = var.web_domain
  acm_certificate_arn = var.acm_certificate_arn
  assets_bucket_id            = module.s3.assets_bucket_id
  assets_bucket_domain        = module.s3.assets_bucket_regional_domain
  screenshots_bucket_id       = module.s3.screenshots_bucket_id
  screenshots_bucket_domain   = module.s3.screenshots_bucket_regional_domain
  tags                = local.common_tags
}

############################
# Secrets + per-service IRSA
############################

module "secrets" {
  source = "./modules/secrets"

  name_prefix       = local.name
  services          = local.services
  kms_key_arn       = module.kms.secrets_key_arn
  rds_endpoint      = module.rds.endpoint
  rds_port          = module.rds.port
  rds_master_secret_arn = module.rds.master_user_secret_arn
  redis_endpoint    = module.elasticache.primary_endpoint
  msk_bootstrap_iam = module.msk.bootstrap_brokers_sasl_iam
  tags              = local.common_tags
}

module "iam" {
  source = "./modules/iam"

  name_prefix           = local.name
  services              = local.services
  oidc_provider_arn     = module.eks.oidc_provider_arn
  oidc_provider_url     = module.eks.oidc_provider_url
  screenshots_bucket_arn = module.s3.screenshots_bucket_arn
  assets_bucket_arn     = module.s3.assets_bucket_arn
  backups_bucket_arn    = module.s3.backups_bucket_arn
  screenshots_kms_arn   = module.kms.screenshots_key_arn
  token_signing_kms_arn = module.kms.token_signing_key_arn
  secrets_kms_arn       = module.kms.secrets_key_arn
  msk_cluster_arn       = module.msk.cluster_arn
  secret_arns           = module.secrets.service_secret_arns
  tags                  = local.common_tags
}
