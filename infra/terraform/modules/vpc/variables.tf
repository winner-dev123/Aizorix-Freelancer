variable "name_prefix" {
  description = "Prefix for all resource names (e.g. aizorix-production)."
  type        = string
}

variable "cidr" {
  description = "VPC CIDR block (a /16 is assumed for the subnet math)."
  type        = string
}

variable "az_count" {
  description = "Number of AZs to span."
  type        = number
  default     = 3
}

variable "single_nat_gateway" {
  description = "Use a single NAT gateway instead of one per AZ."
  type        = bool
  default     = false
}

variable "region" {
  description = "AWS region (used to build VPC endpoint service names)."
  type        = string
}

variable "tags" {
  description = "Tags to apply to all resources."
  type        = map(string)
  default     = {}
}
