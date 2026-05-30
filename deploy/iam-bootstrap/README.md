# IAM bootstrap policies

One-time policies attached to the Terraform OIDC roles so they can create the
new resources introduced by `deploy/terraform/oidc.tf` (S3 OIDC bucket, IAM
OIDC provider, ESO role, Secrets Manager secrets). Once attached, the rest of
the stack is managed entirely by Terraform.

The Terraform roles themselves were created out of band (manual setup once,
predates this repo). These policies extend their existing
`terraform-state-{readwrite,readonly}` inline policies.

## Apply

```sh
export AWS_PROFILE=gemline

aws iam put-role-policy \
  --role-name gemline-tf-apply \
  --policy-name gemline-oidc-migration \
  --policy-document file://deploy/iam-bootstrap/gemline-tf-apply-policy.json

aws iam put-role-policy \
  --role-name gemline-tf-plan \
  --policy-name gemline-oidc-migration \
  --policy-document file://deploy/iam-bootstrap/gemline-tf-plan-policy.json
```

## Reverse

```sh
aws iam delete-role-policy --role-name gemline-tf-apply --policy-name gemline-oidc-migration
aws iam delete-role-policy --role-name gemline-tf-plan  --policy-name gemline-oidc-migration
```

## Scope notes

- All Resource ARNs are scoped to `gemline-*` / `gemline/*` or the OIDC bucket
  by name — no account-wide wildcards on IAM or Secrets Manager.
- `s3:*` on the OIDC bucket is intentionally broad: that bucket only holds the
  cluster's public OIDC discovery doc + JWKS, nothing sensitive.
- `secretsmanager:ListSecrets` is necessarily account-wide (the AWS API
  doesn't honour resource scoping on the List action) but discloses only
  names + ARNs, not values.
