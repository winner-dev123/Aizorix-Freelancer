output "vpc_id" {
  description = "VPC id."
  value       = aws_vpc.this.id
}

output "vpc_cidr" {
  description = "VPC CIDR block."
  value       = aws_vpc.this.cidr_block
}

output "public_subnet_ids" {
  description = "Public subnet ids (load balancers, NAT)."
  value       = aws_subnet.public[*].id
}

output "private_subnet_ids" {
  description = "Private subnet ids (EKS nodes/pods)."
  value       = aws_subnet.private[*].id
}

output "intra_subnet_ids" {
  description = "Intra subnet ids (RDS/ElastiCache/MSK, no internet route)."
  value       = aws_subnet.intra[*].id
}

output "azs" {
  description = "Availability zones in use."
  value       = local.azs
}
