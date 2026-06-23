# EKS control plane + managed node groups + IRSA (OIDC) provider.
#
# Capacity strategy:
#   * Managed node groups here provide the stable baseline: a tainted "system" pool for
#     platform add-ons and a "general" pool for services (ON_DEMAND).
#   * Bursty / batch / spot-tolerant capacity is handled by KARPENTER, installed in-cluster
#     (see infra/k8s/platform). Karpenter provisions SPOT + on-demand nodes just-in-time and
#     is the preferred autoscaler at this scale; it is intentionally NOT modelled as TF node
#     groups so capacity decisions live with the cluster, not the IaC apply cycle.
#
# Access:
#   * Cluster uses the modern EKS *access entries* API (authentication_mode=API_AND_CONFIG_MAP)
#     so admin principals are granted via aws_eks_access_entry rather than hand-edited aws-auth.

data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}

############################
# Cluster IAM role
############################

data "aws_iam_policy_document" "cluster_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["eks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "cluster" {
  name               = "${var.name_prefix}-eks-cluster"
  assume_role_policy = data.aws_iam_policy_document.cluster_assume.json
  tags               = var.tags
}

resource "aws_iam_role_policy_attachment" "cluster" {
  for_each = toset([
    "arn:${data.aws_partition.current.partition}:iam::aws:policy/AmazonEKSClusterPolicy",
  ])
  role       = aws_iam_role.cluster.name
  policy_arn = each.value
}

############################
# Cluster security group
############################

resource "aws_security_group" "cluster" {
  name_prefix = "${var.name_prefix}-eks-cluster-"
  description = "EKS control plane security group"
  vpc_id      = var.vpc_id
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
  tags       = merge(var.tags, { Name = "${var.name_prefix}-eks-cluster-sg" })
  lifecycle { create_before_destroy = true }
}

############################
# Control plane
############################

resource "aws_eks_cluster" "this" {
  name     = var.name_prefix
  role_arn = aws_iam_role.cluster.arn
  version  = var.kubernetes_version

  vpc_config {
    subnet_ids              = concat(var.subnet_ids, var.control_plane_subnet_ids)
    security_group_ids      = [aws_security_group.cluster.id]
    endpoint_private_access = true
    # Safe-by-default: API server is private-only unless an operator explicitly opts in to a
    # public endpoint AND scopes the allowed CIDRs. NEVER default to 0.0.0.0/0.
    endpoint_public_access  = var.endpoint_public_access
    public_access_cidrs     = var.public_access_cidrs
  }

  # API_AND_CONFIG_MAP keeps backward compat with tooling that still reads aws-auth while
  # enabling the access-entry API used below.
  access_config {
    authentication_mode                         = "API_AND_CONFIG_MAP"
    bootstrap_cluster_creator_admin_permissions = true
  }

  # Envelope-encrypt Kubernetes secrets with a CMK (defense-in-depth over EBS encryption).
  encryption_config {
    provider {
      key_arn = var.ebs_kms_key_arn
    }
    resources = ["secrets"]
  }

  enabled_cluster_log_types = ["api", "audit", "authenticator", "controllerManager", "scheduler"]

  tags = var.tags

  depends_on = [aws_iam_role_policy_attachment.cluster]
}

############################
# IRSA / OIDC provider
############################

data "tls_certificate" "oidc" {
  url = aws_eks_cluster.this.identity[0].oidc[0].issuer
}

resource "aws_iam_openid_connect_provider" "oidc" {
  url             = aws_eks_cluster.this.identity[0].oidc[0].issuer
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = [data.tls_certificate.oidc.certificates[0].sha1_fingerprint]
  tags            = var.tags
}

############################
# Node group IAM role (shared by all managed node groups)
############################

data "aws_iam_policy_document" "node_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "node" {
  name               = "${var.name_prefix}-eks-node"
  assume_role_policy = data.aws_iam_policy_document.node_assume.json
  tags               = var.tags
}

resource "aws_iam_role_policy_attachment" "node" {
  for_each = toset([
    "arn:${data.aws_partition.current.partition}:iam::aws:policy/AmazonEKSWorkerNodePolicy",
    "arn:${data.aws_partition.current.partition}:iam::aws:policy/AmazonEKS_CNI_Policy",
    "arn:${data.aws_partition.current.partition}:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly",
    # SSM for break-glass node access without SSH keys.
    "arn:${data.aws_partition.current.partition}:iam::aws:policy/AmazonSSMManagedInstanceCore",
  ])
  role       = aws_iam_role.node.name
  policy_arn = each.value
}

# Shared node security group. Pods/nodes use this; data stores allow it inbound.
resource "aws_security_group" "node" {
  name_prefix = "${var.name_prefix}-eks-node-"
  description = "EKS worker node shared security group"
  vpc_id      = var.vpc_id

  ingress {
    description = "Node-to-node all traffic"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    self        = true
  }
  ingress {
    description     = "Control plane to kubelet/webhooks"
    from_port       = 1025
    to_port         = 65535
    protocol        = "tcp"
    security_groups = [aws_security_group.cluster.id]
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
  tags       = merge(var.tags, { Name = "${var.name_prefix}-eks-node-sg" })
  lifecycle { create_before_destroy = true }
}

############################
# Managed node groups
############################

# Encrypted launch template so node EBS volumes use our CMK and we control IMDSv2.
resource "aws_launch_template" "node" {
  for_each      = var.node_groups
  name_prefix   = "${var.name_prefix}-${each.key}-"
  ebs_optimized = true

  block_device_mappings {
    device_name = "/dev/xvda"
    ebs {
      volume_size           = 100
      volume_type           = "gp3"
      encrypted             = true
      kms_key_id            = var.ebs_kms_key_arn
      delete_on_termination = true
    }
  }

  # Enforce IMDSv2 (block the v1 token-less metadata endpoint) — kills SSRF credential theft.
  metadata_options {
    http_tokens                 = "required"
    http_put_response_hop_limit = 2
    http_endpoint               = "enabled"
  }

  vpc_security_group_ids = [aws_security_group.node.id]

  tag_specifications {
    resource_type = "instance"
    tags          = merge(var.tags, { Name = "${var.name_prefix}-${each.key}-node" })
  }
  lifecycle { create_before_destroy = true }
}

resource "aws_eks_node_group" "this" {
  for_each        = var.node_groups
  cluster_name    = aws_eks_cluster.this.name
  node_group_name = each.key
  node_role_arn   = aws_iam_role.node.arn
  subnet_ids      = var.subnet_ids
  capacity_type   = each.value.capacity_type
  instance_types  = each.value.instance_types
  labels          = each.value.labels

  scaling_config {
    min_size     = each.value.min_size
    max_size     = each.value.max_size
    desired_size = each.value.desired_size
  }

  launch_template {
    id      = aws_launch_template.node[each.key].id
    version = "$Latest"
  }

  dynamic "taint" {
    for_each = each.value.taints
    content {
      key    = taint.value.key
      value  = taint.value.value
      effect = taint.value.effect
    }
  }

  update_config {
    max_unavailable_percentage = 33
  }

  tags = merge(var.tags, {
    # Lets the cluster-autoscaler/Karpenter discover the group.
    "k8s.io/cluster-autoscaler/enabled"               = "true"
    "k8s.io/cluster-autoscaler/${var.name_prefix}"    = "owned"
  })

  # Ignore desired_size drift so the autoscaler can manage it after creation.
  lifecycle {
    ignore_changes = [scaling_config[0].desired_size]
  }

  depends_on = [aws_iam_role_policy_attachment.node]
}

############################
# Access entries for cluster admins (modern replacement for aws-auth edits)
############################

resource "aws_eks_access_entry" "admin" {
  for_each      = toset(var.cluster_admin_principals)
  cluster_name  = aws_eks_cluster.this.name
  principal_arn = each.value
  type          = "STANDARD"
  tags          = var.tags
}

resource "aws_eks_access_policy_association" "admin" {
  for_each      = toset(var.cluster_admin_principals)
  cluster_name  = aws_eks_cluster.this.name
  principal_arn = each.value
  policy_arn    = "arn:${data.aws_partition.current.partition}:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy"
  access_scope {
    type = "cluster"
  }
  depends_on = [aws_eks_access_entry.admin]
}

############################
# Core add-ons (managed by EKS so versions track the control plane)
############################

# NOTE: the aws-ebs-csi-driver addon needs an IRSA role (AmazonEBSCSIDriverPolicy) bound to
# its `ebs-csi-controller-sa` service account to dynamically provision/attach EBS volumes.
# That role is created in the iam/controllers layer (same IRSA pattern as service roles) and
# wired via `service_account_role_arn` on the addon once available. Left unset here so the
# addon installs with node-instance-role permissions as a baseline.
resource "aws_eks_addon" "this" {
  for_each      = toset(["vpc-cni", "coredns", "kube-proxy", "aws-ebs-csi-driver"])
  cluster_name  = aws_eks_cluster.this.name
  addon_name    = each.value
  # PRESERVE keeps add-on config across upgrades; OVERWRITE resolves field conflicts on apply.
  resolve_conflicts_on_create = "OVERWRITE"
  resolve_conflicts_on_update = "PRESERVE"
  tags          = var.tags
  depends_on    = [aws_eks_node_group.this]
}
