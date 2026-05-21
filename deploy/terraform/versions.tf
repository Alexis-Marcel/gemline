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
  }

  # Remote state on S3, locked via DynamoDB.
  # AWS creds resolved through the standard chain — set
  # AWS_PROFILE=gemline (or use OIDC in CI) before running terraform.
  backend "s3" {
    bucket         = "gemline-tfstate-386324384913"
    key            = "infra/terraform.tfstate"
    region         = "eu-west-3"
    dynamodb_table = "gemline-tfstate-lock"
    encrypt        = true
  }
}

provider "hcloud" {
  token = var.hcloud_token
}

provider "cloudflare" {
  api_token = var.cloudflare_api_token
}
