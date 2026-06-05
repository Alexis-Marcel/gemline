# IRSA for Loki: Loki writes log chunks to a private S3 bucket via the
# cluster's OIDC provider. Same federation pattern as the ESO role in
# oidc.tf, scoped to the loki ServiceAccount in the monitoring namespace.
# No long-lived AWS keys live in the cluster.

resource "aws_s3_bucket" "loki_chunks" {
  bucket = "gemline-loki-chunks-${data.aws_caller_identity.current.account_id}"
}

# Defaults stay private — Loki reads/writes via signed STS creds. Nothing
# in this bucket is meant to be publicly readable (unlike the OIDC bucket).

# Belt-and-suspenders retention: Loki's compactor enforces the
# limits_config.retention_period (30 days), this lifecycle rule is a
# safety net at 2× if the compactor ever falls behind.
resource "aws_s3_bucket_lifecycle_configuration" "loki_chunks" {
  bucket = aws_s3_bucket.loki_chunks.id

  rule {
    id     = "expire-old-chunks"
    status = "Enabled"

    filter {}

    expiration {
      days = 60
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
}

resource "aws_iam_role" "loki" {
  name = "gemline-loki-storage"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Federated = aws_iam_openid_connect_provider.cluster.arn }
      Action    = "sts:AssumeRoleWithWebIdentity"
      Condition = {
        StringEquals = {
          "${replace(local.oidc_issuer, "https://", "")}:sub" = "system:serviceaccount:monitoring:loki"
          "${replace(local.oidc_issuer, "https://", "")}:aud" = "sts.amazonaws.com"
        }
      }
    }]
  })
}

resource "aws_iam_role_policy" "loki" {
  name = "loki-chunks-rw"
  role = aws_iam_role.loki.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["s3:ListBucket"]
        Resource = [aws_s3_bucket.loki_chunks.arn]
      },
      {
        Effect = "Allow"
        Action = [
          "s3:GetObject",
          "s3:PutObject",
          "s3:DeleteObject",
        ]
        Resource = ["${aws_s3_bucket.loki_chunks.arn}/*"]
      },
    ]
  })
}
