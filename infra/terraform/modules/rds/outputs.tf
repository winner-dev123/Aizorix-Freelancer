output "endpoint" {
  description = "Primary instance endpoint (host)."
  value       = aws_db_instance.primary.address
}

output "port" {
  description = "Database port."
  value       = aws_db_instance.primary.port
}

output "instance_arn" {
  description = "Primary instance ARN."
  value       = aws_db_instance.primary.arn
}

output "master_user_secret_arn" {
  description = "Secrets Manager ARN holding the RDS-managed master credentials."
  value       = try(aws_db_instance.primary.master_user_secret[0].secret_arn, null)
}

output "replica_endpoint" {
  description = "Cross-region read replica endpoint, if created."
  value       = try(aws_db_instance.replica[0].address, null)
}

output "security_group_id" {
  description = "Database security group id."
  value       = aws_security_group.db.id
}
