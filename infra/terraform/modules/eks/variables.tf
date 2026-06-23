variable "name_prefix" {
  description = "Prefix / cluster name base (e.g. aizorix-production)."
  type        = string
}

variable "kubernetes_version" {
  description = "EKS control plane Kubernetes version."
  type        = string
}

variable "vpc_id" {
  description = "VPC id the cluster lives in."
  type        = string
}

variable "subnet_ids" {
  description = "Private subnet ids for worker nodes / pods."
  type        = list(string)
}

variable "control_plane_subnet_ids" {
  description = "Subnet ids for control plane ENIs (intra subnets)."
  type        = list(string)
}

variable "node_groups" {
  description = "Managed node group definitions (see root variables.tf)."
  type = map(object({
    instance_types = list(string)
    capacity_type  = string
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
}

variable "cluster_admin_principals" {
  description = "IAM principal ARNs granted EKS cluster-admin via access entries."
  type        = list(string)
  default     = []
}

variable "endpoint_public_access" {
  description = "Expose the Kubernetes API server on a public endpoint. Default off (private-only). When enabling, you MUST scope public_access_cidrs."
  type        = bool
  default     = false
}

variable "public_access_cidrs" {
  description = "CIDRs allowed to reach the public API endpoint (only used when endpoint_public_access=true). NEVER set 0.0.0.0/0 in production."
  type        = list(string)
  default     = []
}

variable "ebs_kms_key_arn" {
  description = "CMK ARN for node EBS volume + secrets envelope encryption."
  type        = string
}

variable "tags" {
  description = "Tags to apply to all resources."
  type        = map(string)
  default     = {}
}
