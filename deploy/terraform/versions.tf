terraform {
  required_version = ">= 1.5"
  required_providers {
    hcloud = {
      source  = "hetznercloud/hcloud"
      version = "~> 1.50"
    }
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 4"
    }
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4"
    }
  }

  # Remote state on S3, locked via DynamoDB. Set AWS_PROFILE=gemline
  # (or use OIDC in CI) so the standard credential chain resolves.
  backend "s3" {
    bucket         = "gemline-tfstate-386324384913"
    key            = "infra/terraform.tfstate"
    region         = "eu-west-3"
    dynamodb_table = "gemline-tfstate-lock"
    encrypt        = true
  }
}

locals {
  # Must match backend.s3.region above (TF can't reference locals in backend).
  aws_region = "eu-west-3"
}

provider "hcloud" {
  token = var.hcloud_token
}

provider "cloudflare" {
  api_token = var.cloudflare_api_token
}

provider "aws" {
  region = local.aws_region
}
