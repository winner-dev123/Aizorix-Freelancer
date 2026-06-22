# Root input variables. Defaults target production scale; per-env overrides live in
# environments/*.tfvars. Anything with no sensible default is left required.

############################
# Global / identity
############################

variable "project" {
  description = "Project name used as a prefix for all resource names and tags."
  type        = string
  default     = "aizorix"
}

variable "environment" {
  description = "Deployment environment (production, staging). Drives names and sizing."
  type        = string
  validation {
    condition     = contains(["production", "staging", "dev"], var.environment)
    error_message = "environment must be one of: production, staging, dev."
  }
}

variable "region" {
  description = "Primary AWS region."
  type        = string
  default     = "us-east-1"
}

variable "dr_region" {
  description = "Disaster-recovery / read-replica region (cross-region RDS + S3 CRR)."
  type        = string
  default     = "us-west-2"
}

variable "tags" {
  description = "Additional tags merged into provider default_tags."
  type        = map(string)
  default     = {}
}

############################
# Networking
############################

variable "vpc_cidr" {
  description = "CIDR block for the VPC. /16 gives room for 3 AZs x 3 tiers + growth."
  type        = string
  default     = "10.0.0.0/16"
}

variable "az_count" {
  description = "Number of Availability Zones to span (3 for prod HA)."
  type        = number
  default     = 3
}

variable "single_nat_gateway" {
  description = "Use one NAT gateway (cheaper, non-HA) instead of one per AZ. False in prod."
  type        = bool
  default     = false
}

############################
# EKS
############################

variable "kubernetes_version" {
  description = "EKS control plane Kubernetes version."
  type        = string
  default     = "1.30"
}

variable "node_groups" {
  description = <<-EOT
    Managed node group definitions. The "system" group runs platform add-ons on ON_DEMAND;
    the "general" group runs services. Bursty/batch capacity is handled by Karpenter +
    SPOT (provisioned in-cluster, not here) — see modules/eks for the note.
  EOT
  type = map(object({
    instance_types = list(string)
    capacity_type  = string # ON_DEMAND | SPOT
    min_size       = number
    max_size       = number
    desired_size   = number
    labels         = optional(map(string), {})
    taints = optional(list(object({
      key    = string
      value  = string
      effect = string
    })), [])
  }))
  default = {
    system = {
      instance_types = ["m6i.large"]
      capacity_type  = "ON_DEMAND"
      min_size       = 3
      max_size       = 6
      desired_size   = 3
      labels         = { role = "system" }
      taints = [{
        key    = "CriticalAddonsOnly"
        value  = "true"
        effect = "NO_SCHEDULE"
      }]
    }
    general = {
      instance_types = ["m6i.xlarge", "m6a.xlarge", "m5.xlarge"]
      capacity_type  = "ON_DEMAND"
      min_size       = 3
      max_size       = 20
      desired_size   = 4
      labels         = { role = "general" }
    }
  }
}

variable "cluster_admin_principals" {
  description = "IAM role/user ARNs granted cluster-admin via EKS access entries / aws-auth."
  type        = list(string)
  default     = []
}

############################
# RDS (PostgreSQL)
############################

variable "rds_engine_version" {
  description = "PostgreSQL engine version."
  type        = string
  default     = "16.4"
}

variable "rds_instance_class" {
  description = "RDS primary instance class."
  type        = string
  default     = "db.r6g.2xlarge"
}

variable "rds_allocated_storage" {
  description = "Initial allocated storage (GiB)."
  type        = number
  default     = 200
}

variable "rds_max_allocated_storage" {
  description = "Upper bound for storage autoscaling (GiB)."
  type        = number
  default     = 2000
}

variable "rds_multi_az" {
  description = "Enable Multi-AZ standby for the primary."
  type        = bool
  default     = true
}

variable "rds_create_cross_region_replica" {
  description = "Create a cross-region read replica in dr_region for DR / read locality."
  type        = bool
  default     = true
}

variable "rds_deletion_protection" {
  description = "Block accidental deletion of the database."
  type        = bool
  default     = true
}

variable "rds_backup_retention_days" {
  description = "Automated backup retention in days."
  type        = number
  default     = 14
}

############################
# ElastiCache (Redis)
############################

variable "redis_node_type" {
  description = "ElastiCache node type."
  type        = string
  default     = "cache.r6g.large"
}

variable "redis_num_node_groups" {
  description = "Number of shards (cluster mode)."
  type        = number
  default     = 3
}

variable "redis_replicas_per_node_group" {
  description = "Read replicas per shard (for HA + read scale)."
  type        = number
  default     = 1
}

############################
# MSK (Kafka)
############################

variable "msk_kafka_version" {
  description = "Apache Kafka version for MSK."
  type        = string
  default     = "3.6.0"
}

variable "msk_broker_instance_type" {
  description = "MSK broker instance type."
  type        = string
  default     = "kafka.m5.large"
}

variable "msk_broker_count" {
  description = "Number of MSK brokers (multiple of az_count)."
  type        = number
  default     = 3
}

variable "msk_broker_ebs_gb" {
  description = "EBS volume per broker (GiB)."
  type        = number
  default     = 500
}

############################
# CloudFront / WAF
############################

variable "web_domain" {
  description = "Primary web domain served via CloudFront (e.g. app.aizorix.com)."
  type        = string
  default     = ""
}

variable "acm_certificate_arn" {
  description = "ACM cert ARN in us-east-1 for the CloudFront distribution (optional)."
  type        = string
  default     = ""
}
