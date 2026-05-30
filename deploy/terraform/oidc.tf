# IRSA-equivalent for k3s. Pods authenticate to AWS by presenting their
# ServiceAccount JWT; AWS validates it against the cluster's OIDC discovery
# published to a public S3 bucket. No long-lived AWS credentials in the cluster.

data "aws_caller_identity" "current" {}

locals {
  oidc_bucket = "gemline-cluster-oidc-${data.aws_caller_identity.current.account_id}"
  oidc_issuer = "https://${local.oidc_bucket}.s3.${local.aws_region}.amazonaws.com"
}

resource "aws_s3_bucket" "oidc" {
  bucket = local.oidc_bucket
}

# Anonymous read for AWS STS on the two OIDC paths only.
resource "aws_s3_bucket_public_access_block" "oidc" {
  bucket                  = aws_s3_bucket.oidc.id
  block_public_acls       = false
  block_public_policy     = false
  ignore_public_acls      = false
  restrict_public_buckets = false
}

resource "aws_s3_bucket_policy" "oidc" {
  bucket = aws_s3_bucket.oidc.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = "*"
      Action    = "s3:GetObject"
      Resource = [
        "${aws_s3_bucket.oidc.arn}/.well-known/openid-configuration",
        "${aws_s3_bucket.oidc.arn}/openid/v1/jwks",
      ]
    }]
  })

  depends_on = [aws_s3_bucket_public_access_block.oidc]
}

# Cert thumbprint of *.s3.<region>.amazonaws.com — AWS pins it on the OIDC
# provider so a MITM can't substitute a forged JWKS.
data "tls_certificate" "oidc" {
  url = local.oidc_issuer

  depends_on = [aws_s3_bucket.oidc]
}

resource "aws_iam_openid_connect_provider" "cluster" {
  url             = local.oidc_issuer
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = [data.tls_certificate.oidc.certificates[0].sha1_fingerprint]
}

# Role assumed by the ESO controller via its ServiceAccount JWT.
resource "aws_iam_role" "eso" {
  name = "gemline-eso-secrets-reader"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Federated = aws_iam_openid_connect_provider.cluster.arn }
      Action    = "sts:AssumeRoleWithWebIdentity"
      Condition = {
        StringEquals = {
          "${replace(local.oidc_issuer, "https://", "")}:sub" = "system:serviceaccount:external-secrets:external-secrets"
          "${replace(local.oidc_issuer, "https://", "")}:aud" = "sts.amazonaws.com"
        }
      }
    }]
  })
}

resource "aws_iam_role_policy" "eso" {
  name = "secrets-read"
  role = aws_iam_role.eso.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "secretsmanager:GetSecretValue",
        "secretsmanager:DescribeSecret",
      ]
      Resource = [
        aws_secretsmanager_secret.gemline_env.arn,
        aws_secretsmanager_secret.grafana_admin.arn,
      ]
    }]
  })
}

# Resources only — populate values out of band:
#   aws secretsmanager put-secret-value --secret-id gemline/env \
#     --secret-string '{"DATABASE_URL":"...","SUPABASE_URL":"...","ALLOWED_ORIGINS":"..."}'
resource "aws_secretsmanager_secret" "gemline_env" {
  name                    = "gemline/env"
  description             = "Backend env: DATABASE_URL, SUPABASE_URL, ALLOWED_ORIGINS"
  recovery_window_in_days = 7
}

resource "aws_secretsmanager_secret" "grafana_admin" {
  name                    = "gemline/grafana-admin"
  description             = "Grafana admin credentials (admin-user, admin-password)"
  recovery_window_in_days = 7
}

# Terraform input vars consumed by CI: HCLOUD_TOKEN, CLOUDFLARE_API_TOKEN,
# KUBEAPI_ALLOWED_IPS. Workflows fetch via AWS OIDC at runtime — keeps
# "zero long-lived secrets in GitHub" intact.
resource "aws_secretsmanager_secret" "tf_vars" {
  name                    = "gemline/tf-vars"
  description             = "Terraform input vars: HCLOUD_TOKEN, CLOUDFLARE_API_TOKEN, KUBEAPI_ALLOWED_IPS"
  recovery_window_in_days = 7
}
