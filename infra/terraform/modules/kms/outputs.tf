output "rds_key_arn" {
  description = "CMK ARN for RDS/ElastiCache/MSK storage encryption."
  value       = aws_kms_key.rds.arn
}

output "rds_dr_key_arn" {
  description = "DR-region CMK ARN for the cross-region RDS replica."
  value       = aws_kms_key.rds_dr.arn
}

output "screenshots_key_arn" {
  description = "CMK ARN for S3 screenshot encryption."
  value       = aws_kms_key.screenshots.arn
}

output "screenshots_dr_key_arn" {
  description = "DR-region CMK ARN for screenshot S3 CRR."
  value       = aws_kms_key.screenshots_dr.arn
}

output "token_signing_key_arn" {
  description = "Asymmetric CMK ARN used by auth for JWT signing."
  value       = aws_kms_key.token_signing.arn
}

output "secrets_key_arn" {
  description = "CMK ARN for Secrets Manager encryption."
  value       = aws_kms_key.secrets.arn
}

output "ebs_key_arn" {
  description = "CMK ARN for EBS volume encryption on EKS nodes."
  value       = aws_kms_key.ebs.arn
}
