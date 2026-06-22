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
  description = "Resource name prefix."
}

variable "engine_version" {
  type        = string
  description = "PostgreSQL engine version."
}

variable "instance_class" {
  type        = string
  description = "RDS instance class for primary and replica."
}

variable "allocated_storage" {
  type        = number
  description = "Initial allocated storage (GiB)."
}

variable "max_allocated_storage" {
  type        = number
  description = "Storage autoscaling ceiling (GiB)."
}

variable "multi_az" {
  type        = bool
  description = "Enable Multi-AZ standby."
  default     = true
}

variable "create_cross_region_replica" {
  type        = bool
  description = "Create a cross-region read replica in the DR region."
  default     = false
}

variable "deletion_protection" {
  type        = bool
  description = "Protect the instance from deletion."
  default     = true
}

variable "backup_retention_days" {
  type        = number
  description = "Automated backup retention (days)."
  default     = 14
}

variable "vpc_id" {
  type        = string
  description = "VPC id."
}

variable "subnet_ids" {
  type        = list(string)
  description = "Subnet ids for the DB subnet group (intra subnets)."
}

variable "allowed_security_group_ids" {
  type        = list(string)
  description = "Security groups permitted to reach Postgres (EKS nodes)."
}

variable "kms_key_arn" {
  type        = string
  description = "Primary-region CMK for storage + master secret encryption."
}

variable "dr_kms_key_arn" {
  type        = string
  description = "DR-region CMK for the cross-region replica."
  default     = ""
}

variable "tags" {
  type        = map(string)
  description = "Tags."
  default     = {}
}
