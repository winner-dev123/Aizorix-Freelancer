# The DR-region provider is required because some keys live in dr_region.
terraform {
  required_providers {
    aws = {
      source                = "hashicorp/aws"
      configuration_aliases = [aws.dr]
    }
  }
}

variable "name_prefix" {
  description = "Prefix for key descriptions and aliases."
  type        = string
}

variable "admin_principals" {
  description = "IAM principal ARNs granted key administration rights."
  type        = list(string)
  default     = []
}

variable "tags" {
  description = "Tags to apply to all keys."
  type        = map(string)
  default     = {}
}
