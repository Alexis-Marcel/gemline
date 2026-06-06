# IRSA for Tempo: Tempo writes trace blocks to a private S3 bucket via
# the cluster's OIDC provider. Same federation pattern as the Loki role
# in loki.tf, scoped to the tempo ServiceAccount in the monitoring
# namespace. No long-lived AWS keys live in the cluster.

resource "aws_s3_bucket" "tempo_traces" {
  bucket = "gemline-tempo-traces-${data.aws_caller_identity.current.account_id}"
}

# Defaults stay private — Tempo reads/writes via signed STS creds. Same
# pattern as the Loki chunks bucket: nothing in here is meant to be
# publicly readable.

# Belt-and-suspenders retention: Tempo's compactor enforces the
# block_retention (30 days by default in our values); this lifecycle
# rule is a safety net at 2× if the compactor ever falls behind.
resource "aws_s3_bucket_lifecycle_configuration" "tempo_traces" {
  bucket = aws_s3_bucket.tempo_traces.id

  rule {
    id     = "expire-old-blocks"
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

resource "aws_iam_role" "tempo" {
  name = "gemline-tempo-storage"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Federated = aws_iam_openid_connect_provider.cluster.arn }
      Action    = "sts:AssumeRoleWithWebIdentity"
      Condition = {
        StringEquals = {
          "${replace(local.oidc_issuer, "https://", "")}:sub" = "system:serviceaccount:monitoring:tempo"
          "${replace(local.oidc_issuer, "https://", "")}:aud" = "sts.amazonaws.com"
        }
      }
    }]
  })
}

resource "aws_iam_role_policy" "tempo" {
  name = "tempo-traces-rw"
  role = aws_iam_role.tempo.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["s3:ListBucket"]
        Resource = [aws_s3_bucket.tempo_traces.arn]
      },
      {
        Effect = "Allow"
        Action = [
          "s3:GetObject",
          "s3:PutObject",
          "s3:DeleteObject",
        ]
        Resource = ["${aws_s3_bucket.tempo_traces.arn}/*"]
      },
    ]
  })
}
