# Redis (ElastiCache) replication group, cluster mode enabled (sharded) for horizontal
# scale + HA. Used for sessions, rate limiting, presence, and hot caches across services.
#
# Cluster mode = num_node_groups shards, each with replicas_per_node_group replicas and
# automatic failover. At-rest (CMK) and in-transit encryption are on; AUTH token is stored
# in Secrets Manager by the secrets module and injected via External Secrets.

variable "name_prefix" {
  type        = string
  description = "Resource name prefix."
}
variable "node_type" {
  type        = string
  description = "Cache node instance type."
}
variable "num_node_groups" {
  type        = number
  description = "Number of shards."
}
variable "replicas_per_node_group" {
  type        = number
  description = "Replicas per shard."
}
variable "engine_version" {
  type        = string
  description = "Redis engine version."
  default     = "7.1"
}
variable "vpc_id" {
  type        = string
  description = "VPC id."
}
variable "subnet_ids" {
  type        = list(string)
  description = "Subnet ids (intra)."
}
variable "allowed_security_group_ids" {
  type        = list(string)
  description = "Security groups allowed to connect (EKS nodes)."
}
variable "kms_key_arn" {
  type        = string
  description = "CMK for at-rest encryption."
}
variable "tags" {
  type        = map(string)
  default     = {}
}

resource "aws_elasticache_subnet_group" "this" {
  name       = "${var.name_prefix}-redis"
  subnet_ids = var.subnet_ids
  tags       = var.tags
}

resource "aws_security_group" "redis" {
  name_prefix = "${var.name_prefix}-redis-"
  description = "Redis access from EKS nodes only"
  vpc_id      = var.vpc_id
  ingress {
    description     = "Redis from EKS nodes"
    from_port       = 6379
    to_port         = 6379
    protocol        = "tcp"
    security_groups = var.allowed_security_group_ids
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
  tags       = merge(var.tags, { Name = "${var.name_prefix}-redis-sg" })
  lifecycle { create_before_destroy = true }
}

# Custom parameter group: cluster-mode-enabled family + sane eviction for a cache workload.
resource "aws_elasticache_parameter_group" "this" {
  name_prefix = "${var.name_prefix}-redis-"
  family      = "redis7"
  parameter {
    name  = "cluster-enabled"
    value = "yes"
  }
  parameter {
    name  = "maxmemory-policy"
    value = "allkeys-lru"
  }
  lifecycle { create_before_destroy = true }
  tags = var.tags
}

# Random AUTH token; surfaced via output and stored in Secrets Manager by the secrets module.
resource "random_password" "auth" {
  length  = 32
  special = false # ElastiCache AUTH tokens disallow several special chars
}

resource "aws_elasticache_replication_group" "this" {
  replication_group_id = "${var.name_prefix}-redis"
  description          = "${var.name_prefix} Redis (cluster mode enabled)"
  engine               = "redis"
  engine_version       = var.engine_version
  node_type            = var.node_type
  port                 = 6379

  # Cluster mode topology.
  num_node_groups         = var.num_node_groups
  replicas_per_node_group = var.replicas_per_node_group

  automatic_failover_enabled = true
  multi_az_enabled           = true

  subnet_group_name  = aws_elasticache_subnet_group.this.name
  security_group_ids = [aws_security_group.redis.id]
  parameter_group_name = aws_elasticache_parameter_group.this.name

  at_rest_encryption_enabled = true
  kms_key_id                 = var.kms_key_arn
  transit_encryption_enabled = true
  auth_token                 = random_password.auth.result

  snapshot_retention_limit = 7
  snapshot_window          = "02:00-03:00"
  maintenance_window       = "sun:05:00-sun:06:00"
  apply_immediately        = false

  tags = merge(var.tags, { Name = "${var.name_prefix}-redis" })
}

output "primary_endpoint" {
  description = "Configuration endpoint for cluster-mode clients."
  value       = aws_elasticache_replication_group.this.configuration_endpoint_address
}

output "auth_token" {
  description = "Redis AUTH token (store in Secrets Manager; do not log)."
  value       = random_password.auth.result
  sensitive   = true
}

output "security_group_id" {
  value = aws_security_group.redis.id
}
