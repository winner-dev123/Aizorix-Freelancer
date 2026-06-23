# PostgreSQL 16 primary (Multi-AZ) with:
#   * storage encryption via a customer CMK
#   * master credentials managed by RDS-integrated Secrets Manager (manage_master_user_password)
#   * a tuned parameter group
#   * optional cross-region read replica in dr_region for DR / read locality
#
# The DB lives in intra subnets (no internet path) and only accepts traffic from the EKS
# node security group.

############################
# Networking
############################

resource "aws_db_subnet_group" "this" {
  name       = "${var.name_prefix}-pg"
  subnet_ids = var.subnet_ids
  tags       = merge(var.tags, { Name = "${var.name_prefix}-pg-subnets" })
}

resource "aws_security_group" "db" {
  name_prefix = "${var.name_prefix}-pg-"
  description = "PostgreSQL access from EKS nodes only"
  vpc_id      = var.vpc_id

  ingress {
    description     = "PostgreSQL from EKS nodes"
    from_port       = 5432
    to_port         = 5432
    protocol        = "tcp"
    security_groups = var.allowed_security_group_ids
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
  tags = merge(var.tags, { Name = "${var.name_prefix}-pg-sg" })
  lifecycle { create_before_destroy = true }
}

############################
# Parameter group — tuned for a high-connection-count microservice fleet
############################

resource "aws_db_parameter_group" "this" {
  name_prefix = "${var.name_prefix}-pg16-"
  family      = "postgres16"

  # Log slow queries (>1s) for performance triage.
  parameter {
    name  = "log_min_duration_statement"
    value = "1000"
  }
  # Force TLS for every client connection.
  parameter {
    name  = "rds.force_ssl"
    value = "1"
  }
  # pg_stat_statements for query observability (requires reboot → applied as pending-reboot).
  parameter {
    name         = "shared_preload_libraries"
    value        = "pg_stat_statements"
    apply_method = "pending-reboot"
  }
  parameter {
    name  = "log_connections"
    value = "1"
  }
  parameter {
    name  = "log_disconnections"
    value = "1"
  }

  lifecycle { create_before_destroy = true }
  tags = var.tags
}

############################
# Primary instance
############################

resource "aws_db_instance" "primary" {
  identifier     = "${var.name_prefix}-pg"
  engine         = "postgres"
  engine_version = var.engine_version
  instance_class = var.instance_class

  allocated_storage     = var.allocated_storage
  max_allocated_storage = var.max_allocated_storage # storage autoscaling
  storage_type          = "gp3"
  storage_encrypted     = true
  kms_key_id            = var.kms_key_arn

  db_name  = "aizorix"
  username = "aizorix_admin"
  # RDS generates + rotates the master password into Secrets Manager; nothing in TF state.
  manage_master_user_password   = true
  master_user_secret_kms_key_id = var.kms_key_arn

  multi_az               = var.multi_az
  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.db.id]
  parameter_group_name   = aws_db_parameter_group.this.name
  port                   = 5432

  backup_retention_period   = var.backup_retention_days
  backup_window             = "03:00-04:00"
  maintenance_window        = "sun:04:30-sun:05:30"
  copy_tags_to_snapshot     = true
  deletion_protection       = var.deletion_protection
  skip_final_snapshot       = false
  final_snapshot_identifier = "${var.name_prefix}-pg-final"

  performance_insights_enabled          = true
  performance_insights_kms_key_id       = var.kms_key_arn
  performance_insights_retention_period = 7
  monitoring_interval                   = 60
  monitoring_role_arn                   = aws_iam_role.monitoring.arn
  enabled_cloudwatch_logs_exports       = ["postgresql", "upgrade"]

  auto_minor_version_upgrade = true
  apply_immediately          = false

  tags = merge(var.tags, { Name = "${var.name_prefix}-pg" })

  lifecycle {
    # Guard against accidental replacement of the production database.
    ignore_changes = [final_snapshot_identifier]
  }
}

############################
# Enhanced monitoring role
############################

data "aws_iam_policy_document" "monitoring_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["monitoring.rds.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "monitoring" {
  name               = "${var.name_prefix}-rds-monitoring"
  assume_role_policy = data.aws_iam_policy_document.monitoring_assume.json
  tags               = var.tags
}

resource "aws_iam_role_policy_attachment" "monitoring" {
  role       = aws_iam_role.monitoring.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonRDSEnhancedMonitoringRole"
}

############################
# Cross-region read replica (DR / read locality) — optional
############################

resource "aws_db_instance" "replica" {
  count    = var.create_cross_region_replica ? 1 : 0
  provider = aws.dr

  identifier          = "${var.name_prefix}-pg-replica"
  instance_class      = var.instance_class
  replicate_source_db = aws_db_instance.primary.arn # ARN required for cross-region

  # Replica gets its own DR-region CMK; storage encryption is mandatory cross-region.
  storage_encrypted = true
  kms_key_id        = var.dr_kms_key_arn

  # No backups on the replica; it inherits data from the primary.
  backup_retention_period    = 0
  skip_final_snapshot        = true
  deletion_protection        = var.deletion_protection
  auto_minor_version_upgrade = true

  performance_insights_enabled = true
  monitoring_interval          = 60
  monitoring_role_arn          = aws_iam_role.monitoring.arn

  tags = merge(var.tags, { Name = "${var.name_prefix}-pg-replica", Role = "read-replica" })
}
