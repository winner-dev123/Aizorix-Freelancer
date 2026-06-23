# Amazon MSK (managed Kafka) — the event backbone for the outbox/event-driven architecture.
#
# Security posture:
#   * Encryption in transit: TLS only (broker-broker + client-broker).
#   * Encryption at rest: customer CMK.
#   * AuthN/Z: IAM (SASL/IAM). Services authenticate with their IRSA role; topic-level
#     authorization is enforced by IAM policies in the iam module. This avoids managing
#     SCRAM secrets and ties Kafka access to the same identity as everything else.

variable "name_prefix" {
  type        = string
  description = "Resource name prefix."
}
variable "kafka_version" {
  type        = string
  description = "Apache Kafka version."
}
variable "broker_instance_type" {
  type        = string
  description = "Broker instance type."
}
variable "broker_count" {
  type        = number
  description = "Number of brokers (multiple of subnet count)."
}
variable "broker_ebs_gb" {
  type        = number
  description = "EBS volume per broker (GiB)."
}
variable "vpc_id" {
  type        = string
  description = "VPC id."
}
variable "subnet_ids" {
  type        = list(string)
  description = "Subnet ids for brokers (private)."
}
variable "allowed_security_group_ids" {
  type        = list(string)
  description = "Security groups allowed to reach brokers (EKS nodes)."
}
variable "kms_key_arn" {
  type        = string
  description = "CMK for at-rest encryption."
}
variable "tags" {
  type    = map(string)
  default = {}
}

resource "aws_security_group" "msk" {
  name_prefix = "${var.name_prefix}-msk-"
  description = "MSK broker access from EKS nodes (TLS + IAM SASL)"
  vpc_id      = var.vpc_id

  ingress {
    description     = "TLS bootstrap/broker"
    from_port       = 9094
    to_port         = 9094
    protocol        = "tcp"
    security_groups = var.allowed_security_group_ids
  }
  ingress {
    description     = "SASL/IAM"
    from_port       = 9098
    to_port         = 9098
    protocol        = "tcp"
    security_groups = var.allowed_security_group_ids
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
  tags = merge(var.tags, { Name = "${var.name_prefix}-msk-sg" })
  lifecycle { create_before_destroy = true }
}

# Server-side config: disable auto topic creation (topics are provisioned explicitly) and
# set a safe default replication factor for durability across AZs.
resource "aws_msk_configuration" "this" {
  name           = "${var.name_prefix}-msk-config"
  kafka_versions = [var.kafka_version]

  server_properties = <<-PROPERTIES
    auto.create.topics.enable=false
    default.replication.factor=3
    min.insync.replicas=2
    num.partitions=12
    log.retention.hours=168
    unclean.leader.election.enable=false
  PROPERTIES
}

resource "aws_cloudwatch_log_group" "broker" {
  name              = "/aws/msk/${var.name_prefix}"
  retention_in_days = 30
  tags              = var.tags
}

resource "aws_msk_cluster" "this" {
  cluster_name           = "${var.name_prefix}-kafka"
  kafka_version          = var.kafka_version
  number_of_broker_nodes = var.broker_count

  broker_node_group_info {
    instance_type   = var.broker_instance_type
    client_subnets  = var.subnet_ids
    security_groups = [aws_security_group.msk.id]
    storage_info {
      ebs_storage_info {
        volume_size = var.broker_ebs_gb
      }
    }
  }

  configuration_info {
    arn      = aws_msk_configuration.this.arn
    revision = aws_msk_configuration.this.latest_revision
  }

  encryption_info {
    encryption_at_rest_kms_key_arn = var.kms_key_arn
    encryption_in_transit {
      client_broker = "TLS"
      in_cluster    = true
    }
  }

  # IAM SASL only — no unauthenticated or SCRAM access.
  client_authentication {
    sasl {
      iam = true
    }
  }

  enhanced_monitoring = "PER_TOPIC_PER_BROKER"

  logging_info {
    broker_logs {
      cloudwatch_logs {
        enabled   = true
        log_group = aws_cloudwatch_log_group.broker.name
      }
    }
  }

  tags = merge(var.tags, { Name = "${var.name_prefix}-kafka" })
}

output "cluster_arn" {
  value       = aws_msk_cluster.this.arn
  description = "MSK cluster ARN (used in IAM policies)."
}

output "bootstrap_brokers_sasl_iam" {
  value       = aws_msk_cluster.this.bootstrap_brokers_sasl_iam
  description = "SASL/IAM bootstrap broker string."
}

output "security_group_id" {
  value = aws_security_group.msk.id
}
