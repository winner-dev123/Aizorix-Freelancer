# VPC with three subnet tiers across `az_count` AZs:
#   public  — ALB/NLB, NAT gateways, internet-facing only
#   private — EKS worker nodes + pods (egress via NAT, ingress from ALB)
#   intra   — RDS / ElastiCache / "no internet at all" data stores (no NAT route)
#
# Subnetting (for a /16 VPC) uses cidrsubnet with a /4 split → /20 blocks, leaving headroom.

data "aws_availability_zones" "available" {
  state = "available"
  filter {
    name   = "opt-in-status"
    values = ["opt-in-not-required"]
  }
}

locals {
  azs = slice(data.aws_availability_zones.available.names, 0, var.az_count)

  # newbits=4 → /20 subnets out of a /16. Offsets keep the three tiers non-overlapping.
  public_subnets  = [for i in range(var.az_count) : cidrsubnet(var.cidr, 4, i)]
  private_subnets = [for i in range(var.az_count) : cidrsubnet(var.cidr, 4, i + var.az_count)]
  intra_subnets   = [for i in range(var.az_count) : cidrsubnet(var.cidr, 4, i + (2 * var.az_count))]
}

resource "aws_vpc" "this" {
  cidr_block           = var.cidr
  enable_dns_support   = true
  enable_dns_hostnames = true # required for private EKS endpoints + VPC endpoints
  tags                 = merge(var.tags, { Name = "${var.name_prefix}-vpc" })
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
  tags   = merge(var.tags, { Name = "${var.name_prefix}-igw" })
}

############################
# Subnets
############################

resource "aws_subnet" "public" {
  count                   = var.az_count
  vpc_id                  = aws_vpc.this.id
  cidr_block              = local.public_subnets[count.index]
  availability_zone       = local.azs[count.index]
  map_public_ip_on_launch = true
  tags = merge(var.tags, {
    Name                     = "${var.name_prefix}-public-${local.azs[count.index]}"
    "kubernetes.io/role/elb" = "1" # public ALBs
    Tier                     = "public"
  })
}

resource "aws_subnet" "private" {
  count             = var.az_count
  vpc_id            = aws_vpc.this.id
  cidr_block        = local.private_subnets[count.index]
  availability_zone = local.azs[count.index]
  tags = merge(var.tags, {
    Name                              = "${var.name_prefix}-private-${local.azs[count.index]}"
    "kubernetes.io/role/internal-elb" = "1" # internal ALBs
    Tier                              = "private"
  })
}

resource "aws_subnet" "intra" {
  count             = var.az_count
  vpc_id            = aws_vpc.this.id
  cidr_block        = local.intra_subnets[count.index]
  availability_zone = local.azs[count.index]
  tags = merge(var.tags, {
    Name = "${var.name_prefix}-intra-${local.azs[count.index]}"
    Tier = "intra"
  })
}

############################
# NAT (one per AZ in prod for HA; single when single_nat_gateway=true)
############################

locals {
  nat_count = var.single_nat_gateway ? 1 : var.az_count
}

resource "aws_eip" "nat" {
  count  = local.nat_count
  domain = "vpc"
  tags   = merge(var.tags, { Name = "${var.name_prefix}-nat-eip-${count.index}" })
}

resource "aws_nat_gateway" "this" {
  count         = local.nat_count
  allocation_id = aws_eip.nat[count.index].id
  subnet_id     = aws_subnet.public[count.index].id
  tags          = merge(var.tags, { Name = "${var.name_prefix}-nat-${count.index}" })
  depends_on    = [aws_internet_gateway.this]
}

############################
# Route tables
############################

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.this.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this.id
  }
  tags = merge(var.tags, { Name = "${var.name_prefix}-public-rt" })
}

resource "aws_route_table_association" "public" {
  count          = var.az_count
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

# Each private subnet routes egress through the NAT in its AZ (or the single NAT).
resource "aws_route_table" "private" {
  count  = var.az_count
  vpc_id = aws_vpc.this.id
  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.this[var.single_nat_gateway ? 0 : count.index].id
  }
  tags = merge(var.tags, { Name = "${var.name_prefix}-private-rt-${count.index}" })
}

resource "aws_route_table_association" "private" {
  count          = var.az_count
  subnet_id      = aws_subnet.private[count.index].id
  route_table_id = aws_route_table.private[count.index].id
}

# Intra subnets have NO default route → no internet path, reachable only inside the VPC.
resource "aws_route_table" "intra" {
  vpc_id = aws_vpc.this.id
  tags   = merge(var.tags, { Name = "${var.name_prefix}-intra-rt" })
}

resource "aws_route_table_association" "intra" {
  count          = var.az_count
  subnet_id      = aws_subnet.intra[count.index].id
  route_table_id = aws_route_table.intra.id
}

############################
# VPC endpoints (keep AWS-bound traffic off the NAT / public internet)
############################

# Gateway endpoint for S3 — free, attaches to route tables. Big NAT savings for screenshots.
resource "aws_vpc_endpoint" "s3" {
  vpc_id            = aws_vpc.this.id
  service_name      = "com.amazonaws.${var.region}.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = concat(aws_route_table.private[*].id, [aws_route_table.intra.id])
  tags              = merge(var.tags, { Name = "${var.name_prefix}-vpce-s3" })
}

# Security group for the interface endpoints: allow HTTPS from within the VPC.
resource "aws_security_group" "vpce" {
  name_prefix = "${var.name_prefix}-vpce-"
  description = "Allow HTTPS to interface VPC endpoints from within the VPC"
  vpc_id      = aws_vpc.this.id

  ingress {
    description = "HTTPS from VPC"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = [var.cidr]
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
  tags = merge(var.tags, { Name = "${var.name_prefix}-vpce-sg" })
  lifecycle { create_before_destroy = true }
}

# Interface endpoints for ECR (image pulls), KMS (envelope encryption), STS (IRSA),
# Secrets Manager, and CloudWatch Logs. Pods hit these privately instead of via NAT.
locals {
  interface_endpoints = [
    "ecr.api",
    "ecr.dkr",
    "kms",
    "sts",
    "secretsmanager",
    "logs",
  ]
}

resource "aws_vpc_endpoint" "interface" {
  for_each            = toset(local.interface_endpoints)
  vpc_id              = aws_vpc.this.id
  service_name        = "com.amazonaws.${var.region}.${each.value}"
  vpc_endpoint_type   = "Interface"
  subnet_ids          = aws_subnet.private[*].id
  security_group_ids  = [aws_security_group.vpce.id]
  private_dns_enabled = true
  tags                = merge(var.tags, { Name = "${var.name_prefix}-vpce-${each.value}" })
}
