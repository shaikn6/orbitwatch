# One Orbit Watch instance on AWS: a small EC2 machine that refreshes the
# public orbit data every two hours, screens it and serves the viewer.
#
# It holds no secrets and no private data. The viewer port is open only to
# the networks you list; put a CDN or load balancer with TLS in front of it
# before pointing the public at it.

terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

variable "name" {
  description = "Prefix for every resource."
  type        = string
  default     = "orbitwatch"
}

variable "image" {
  description = "Container image to run, for example ghcr.io/you/orbitwatch:0.1.0."
  type        = string
}

variable "vpc_id" {
  description = "VPC to deploy into."
  type        = string
}

variable "subnet_id" {
  description = "Subnet for the instance."
  type        = string
}

variable "allowed_cidrs" {
  description = "Networks allowed to open the viewer. There is no default on purpose."
  type        = list(string)
}

variable "instance_type" {
  description = "Arm instance type. Screening uses every core; two cores take a few minutes per cycle."
  type        = string
  default     = "c7g.large"
}

data "aws_ssm_parameter" "ami" {
  name = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64"
}

resource "aws_security_group" "this" {
  name_prefix = "${var.name}-"
  description = "Orbit Watch viewer"
  vpc_id      = var.vpc_id

  ingress {
    description = "viewer"
    from_port   = 8080
    to_port     = 8080
    protocol    = "tcp"
    cidr_blocks = var.allowed_cidrs
  }

  egress {
    description = "orbit data and image pull"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_instance" "this" {
  ami                    = data.aws_ssm_parameter.ami.value
  instance_type          = var.instance_type
  subnet_id              = var.subnet_id
  vpc_security_group_ids = [aws_security_group.this.id]

  metadata_options {
    http_tokens = "required"
  }

  root_block_device {
    encrypted   = true
    volume_size = 20
  }

  user_data = templatefile("${path.module}/cloud-init.yaml.tftpl", { image = var.image })

  tags = { Name = var.name }
}

output "viewer_url" {
  description = "Private address of the viewer."
  value       = "http://${aws_instance.this.private_ip}:8080"
}
