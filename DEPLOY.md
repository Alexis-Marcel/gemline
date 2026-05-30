# Deploying Gemline

Reproducible deploy of Gemline to a self-hosted Kubernetes cluster on
Hetzner Cloud, with HA control plane, TLS, GitOps, GitOps-managed
secrets, monitoring, and CI/CD wired end-to-end.

The whole stack stands up with **one command** (`make deploy`): Terraform
provisions the infrastructure, Ansible turns the VMs into a k3s cluster
and installs ArgoCD, and ArgoCD takes over from there — pulling every
workload from this repo.

## Architecture

```
                         Internet (HTTPS)
                                │
                    Cloudflare DNS (A record, Terraform-managed)
                                │
                                ▼
                   ┌─────────────────────────┐
                   │  Hetzner Load Balancer   │  lb11
                   │  :6443 (kube API)        │  → control-plane nodes
                   │  :80 / :443 (app)        │     (private network)
                   └────────────┬─────────────┘
                                │
                                ▼
                   ┌─────────────────────────┐
                   │  Traefik (k3s ingress)   │
                   │  TLS via cert-manager+LE │
                   └─────┬──────────────┬─────┘
            /api /ws     │              │   /
            /healthz     ▼              ▼
                  ┌────────────┐  ┌────────────┐
                  │  server    │  │  web       │
                  │  Go        │  │  Caddy SPA │
                  │  2 pods    │  │  3 pods    │
                  └─────┬──────┘  └────────────┘
                        │ outbound HTTPS
                        ▼
                  ┌────────────┐
                  │  Supabase  │  (Postgres + auth)
                  └────────────┘
```

Cross-pod live events (WebSocket fan-out, cache invalidation,
matchmaking) travel through a Postgres `LISTEN/NOTIFY` backplane, so the
backend runs **2+ replicas** with no sticky sessions.

## Stack at a glance

| Layer | Tool | Notes |
|---|---|---|
| Infrastructure | **Terraform** | Hetzner VMs + private network + firewall + Load Balancer, Cloudflare DNS. Remote state on **S3 + DynamoDB lock**. |
| Configuration | **Ansible** | Installs k3s (HA embedded etcd), cert-manager, ArgoCD, and the ArgoCD app-of-apps. Dynamic inventory from Terraform outputs. |
| Cluster | **k3s** | Lightweight Kubernetes, bundled Traefik ingress. |
| GitOps | **ArgoCD** v3.4.1 | App-of-apps; auto-sync + self-heal from `main`. |
| Secrets | **AWS Secrets Manager** + External Secrets Operator | IRSA-equivalent OIDC: the cluster publishes its discovery + JWKS to a public S3 bucket; AWS validates pod tokens against it and hands out short-lived role credentials. Zero long-lived AWS keys in the cluster. |
| TLS | **cert-manager** v1.16.1 + Let's Encrypt | HTTP-01 via Traefik. |
| Monitoring | **kube-prometheus-stack** 75.6.0 | Prometheus (15d retention) + Grafana + Alertmanager; app `ServiceMonitor` scrapes `/metrics`. |
| Registry | **GHCR** | Public images, no pull secret needed. |
| CI/CD | **GitHub Actions** | Tests, image build + manifest bump, Terraform plan/apply with OIDC. |

## Topology & cost

Node counts are configurable in `deploy/terraform/terraform.tfvars`:

- `cp_count` — control-plane nodes. **Default 3** (HA via k3s embedded
  etcd; quorum needs an odd number ≥ 3). Set `1` for a cheaper, non-HA
  learning cluster.
- `worker_count` — worker nodes. **Default 1**.

All nodes are `cx23` (2 vCPU / 4 GB) in `fsn1` by default.

| Config | Nodes | Approx. monthly |
|---|---|---|
| Non-HA (`cp_count=1`) | 1 CP + 1 worker + LB | ~€14 |
| HA (`cp_count=3`, default) | 3 CP + 1 worker + LB | ~€23 |

Plus: domain ~€10/yr, Supabase free tier, GHCR (public) free, AWS
(S3 state, DynamoDB lock, OIDC bucket, ~5 Secrets Manager entries):
cents/month total. Prices are approximate — check Hetzner's current
`cx23` and `lb11` rates.

## Prerequisites

1. **Hetzner Cloud** account + API token (Read & Write).
2. **Cloudflare** zone for your domain + an API token scoped to *Edit
   zone DNS* on it.
3. **AWS** account hosting the Terraform state (S3 + DynamoDB lock), the
   cluster's OIDC discovery bucket, the IAM OIDC provider + ESO role,
   and the app secrets in Secrets Manager. Local runs authenticate via
   `AWS_PROFILE`; CI uses OIDC (no long-lived keys). Two IAM roles are
   expected — `gemline-tf-apply` and `gemline-tf-plan`. The bootstrap
   policies that grant them OIDC + IAM + Secrets Manager + S3 access
   live in `deploy/iam-bootstrap/` and are attached one-time via the
   `aws iam put-role-policy` commands in that directory's README.
4. **Supabase** project (Postgres + auth). Note its Session-pooler
   `DATABASE_URL` and project URL.
5. **Local tools**: `terraform`, `ansible`, `kubectl`, `make`, `jq`,
   `aws` CLI, plus an SSH key.

> If you fork the repo, search-and-replace the hard-coded identifiers:
> hostname `gemline.werilo.fr`, email `alexismarcel55@gmail.com`, repo
> `Alexis-Marcel/gemline`, GHCR `alexis-marcel/...`, AWS account
> `386324384913`, and the OIDC bucket name `gemline-cluster-oidc-386324384913`
> (referenced in `deploy/terraform/`, `deploy/ansible/group_vars/`,
> and `deploy/Makefile`).

## One-command bootstrap

```sh
cd deploy

# 1. One-time per AWS account: attach the IAM policies that let the TF
#    roles create the OIDC provider, ESO role, S3 OIDC bucket and the
#    Secrets Manager entries. See deploy/iam-bootstrap/README.md.

# 2. Fill in your TF vars (gitignored):
cp terraform/terraform.tfvars.example terraform/terraform.tfvars
$EDITOR terraform/terraform.tfvars      # hcloud_token, cloudflare_api_token,
                                        # dns_zone/subdomain, cp_count, …

export AWS_PROFILE=gemline              # for the S3 state backend

# 3. Provision + configure everything:
make deploy                            # = terraform apply  +  ansible-playbook

# 4. Populate the app secrets in Secrets Manager (TF created empty
#    resources; values are managed out-of-band, never in TF state):
aws secretsmanager put-secret-value --secret-id gemline/env --secret-string '{
  "DATABASE_URL": "...",
  "SUPABASE_URL": "https://<ref>.supabase.co",
  "ALLOWED_ORIGINS": "https://gemline.werilo.fr"
}'
aws secretsmanager put-secret-value --secret-id gemline/grafana-admin --secret-string '{
  "admin-user": "admin", "admin-password": "..."
}'
aws secretsmanager put-secret-value --secret-id gemline/tf-vars --secret-string '{
  "HCLOUD_TOKEN": "...", "CLOUDFLARE_API_TOKEN": "...",
  "KUBEAPI_ALLOWED_IPS": ["x.y.z.t/32"]
}'

# 5. Publish the cluster's OIDC discovery + JWKS to S3, so AWS STS can
#    validate pod tokens (IRSA-equivalent):
make kubeconfig
make publish-jwks
```

`make deploy` runs two layers:

### Terraform layer (`terraform apply`)

Provisions the VMs, private network (`10.0.0.0/16`), a public firewall
(22/80/443 open; the kube API on 6443 closed unless you list your IP in
`kubeapi_allowed_ips`), a Hetzner Load Balancer fronting the control
plane, the Cloudflare A record, and the AWS side of the IRSA-equivalent
chain: the S3 OIDC bucket, IAM OIDC provider, the ESO role + policy,
and the Secrets Manager resources (no values, just empty entries).
cloud-init only brings up the private NIC — k3s and everything else is
Ansible's job. State lives remotely on S3 (locked via DynamoDB).

### Ansible layer (`ansible-playbook --ask-vault-pass`)

The inventory is **dynamic** (`inventory.tf.py` reads `terraform output
-json` — no IPs to maintain by hand). Roles run in order:

1. `networking` — base network setup on every node.
2. `k3s_server` (control plane, `serial: 1`) — the first CP runs
   `--cluster-init` to bootstrap embedded etcd; additional CPs join
   through the Load Balancer. All add the LB IP as a TLS SAN, and the
   API server is configured with `--service-account-issuer` +
   `--service-account-jwks-uri` pointing at the S3 OIDC bucket so pod
   tokens carry the right `iss` claim.
3. `k3s_agent` (workers) — join the cluster over the private network.
4. `cert_manager`, `argocd`, `argocd_apps` (first CP only) — install
   cert-manager + ArgoCD, then apply the ArgoCD Applications.

The k3s join token is stored encrypted in Ansible Vault
(`group_vars/all/vault.yaml`), so the playbook prompts for the vault
password (`--ask-vault-pass`).

Once ArgoCD is up, it owns the cluster: it reconciles everything below
straight from `main`.

### Grab the kubeconfig

```sh
make kubeconfig                        # writes ~/.kube/gemline.yaml
export KUBECONFIG=~/.kube/gemline.yaml
kubectl get nodes                      # cp_count + worker_count, all Ready
```

## GitOps — the app-of-apps

Ansible applies four ArgoCD `Application`s (listed in
`deploy/ansible/group_vars/all/vars.yaml`); ArgoCD syncs each from this
repo or an upstream Helm chart, with `prune` + `selfHeal` +
`ServerSideApply`:

| Application | Source | What it deploys |
|---|---|---|
| `external-secrets` | Helm 0.11.0 | External Secrets Operator (ESO) in `external-secrets`. The IRSA role ARN is set as `eks.amazonaws.com/role-arn` on its ServiceAccount via the Helm values. |
| `eso-config` | git `deploy/k8s/external-secrets` | The `ClusterSecretStore` pointing at AWS Secrets Manager + the `ExternalSecret`s for `gemline-env` and `grafana-admin`. |
| `monitoring` | Helm kube-prometheus-stack 75.6.0 | Prometheus + Grafana + Alertmanager in `monitoring`. |
| `gemline` | git `deploy/k8s` | The app: server (×2), web (×3), services, ingress, ClusterIssuer, ServiceMonitor. |

Day-to-day, **nobody runs `kubectl apply`** for the app — you push to
`main` and ArgoCD rolls the change forward.

## Secrets — AWS Secrets Manager via IRSA-equivalent

No long-lived AWS credentials live in the cluster, and no secret is
created by hand. The flow is the same pattern as EKS's IRSA, ported to
self-hosted k3s: the cluster publishes its OIDC discovery to a public
S3 bucket, AWS validates pod tokens against it, and ESO trades its
ServiceAccount JWT for short-lived role credentials.

```
       cluster                              AWS
 ┌──────────────────┐              ┌─────────────────────────┐
 │ ESO controller   │              │ S3 OIDC bucket          │
 │ ServiceAccount   │  ──fetches──►│  /.well-known/...       │  (public read)
 │ JWT (audience    │     ▲        │  /openid/v1/jwks        │
 │  sts.amazonaws   │     │        └─────────────────────────┘
 │  .com)           │     │ verifies issuer + signature
 └────────┬─────────┘     │
          │ AssumeRoleWithWebIdentity
          ▼               │
       ┌──────────────────┴─────────────────┐
       │  AWS STS                           │
       │  → short-lived creds for           │
       │    role/gemline-eso-secrets-reader │
       └─────────────────┬──────────────────┘
                         │ GetSecretValue
                         ▼
       ┌──────────────────────────────────┐
       │  Secrets Manager                 │
       │   gemline/env                    │
       │   gemline/grafana-admin          │
       └─────────────────┬────────────────┘
                         │ values
                         ▼
       ┌──────────────────────────────────┐
       │  K8s Secrets (gemline/, monitoring/) │
       │  consumed via envFrom / existingSecret │
       └──────────────────────────────────┘
```

Pieces, all managed in code:

- **Terraform** (`deploy/terraform/oidc.tf`) creates the S3 bucket, the
  IAM OIDC provider trusting that issuer (pinned by TLS thumbprint),
  the IAM role whose trust policy allows
  `system:serviceaccount:external-secrets:external-secrets` to assume
  it, a role policy granting `secretsmanager:GetSecretValue` on
  `gemline/*`, and the three Secrets Manager resources
  (`gemline/env`, `gemline/grafana-admin`, `gemline/tf-vars`).
- **Ansible** (`k3s_server` role) configures the k3s API server with
  `--service-account-issuer` + `--service-account-jwks-uri` pointing at
  the public S3 URL, so pod tokens carry the right `iss` claim.
- **`make publish-jwks`** (one-time per cluster install) fetches the
  cluster's `/.well-known/openid-configuration` and `/openid/v1/jwks`
  via local `kubectl` and uploads them to the S3 bucket. Sanity-checks
  for empty payloads — the previous SSH-based version silently uploaded
  zero-byte files on failure, which broke ESO non-obviously.
- **External Secrets Operator** (Helm chart `app-external-secrets.yaml`)
  is deployed with the role ARN as a `eks.amazonaws.com/role-arn`
  annotation on its ServiceAccount. ESO reads this and exchanges its
  projected SA token for AWS credentials.
- **ClusterSecretStore + ExternalSecrets** (`deploy/k8s/external-secrets/`)
  declare the AWS provider; `dataFrom.extract` flattens each JSON secret
  in Secrets Manager into K8s Secret keys, so consumers (`envFrom` or
  `existingSecret`) need no template glue.

Secret **values** are populated out-of-band via `aws secretsmanager
put-secret-value`. They live outside Terraform state and survive
cluster rebuilds (the SM resources have a 7-day recovery window).

> **Gotcha — don't set `provider.aws.role` in the ClusterSecretStore.**
> Setting both the SA annotation *and* `role:` makes ESO chain two
> `AssumeRole` calls; the second one fails because the role doesn't
> trust itself. The SA annotation alone is enough.

## TLS & ingress

Traefik (bundled with k3s) terminates TLS. cert-manager requests a
Let's Encrypt **production** certificate via the `letsencrypt-prod`
`ClusterIssuer` (HTTP-01 over Traefik). The `gemline` Ingress routes
`/api`, `/ws`, `/healthz`, `/readyz` to the server and everything else
to the web SPA, all on one hostname.

## Monitoring

kube-prometheus-stack runs Prometheus (15-day persistent retention),
Grafana, and Alertmanager. The app exposes Prometheus metrics at
`/metrics` (top-level, outside the CORS/auth/log middleware); the
`gemline-server` `ServiceMonitor` wires it up for scraping. Grafana's
admin credentials come from AWS Secrets Manager via ESO (the
`gemline/grafana-admin` secret).

```sh
kubectl -n monitoring port-forward svc/kube-prometheus-stack-grafana 3000:80
# open http://localhost:3000
```

## CI/CD

Four GitHub Actions workflows:

| Workflow | Trigger | Does |
|---|---|---|
| `test` | push / PR | `go vet`, `go test -race -cover`; `npm ci` + `npm run build`. |
| `deploy` | push to `main` (app paths) | Builds `gemline-server` + `gemline-web`, pushes to GHCR tagged with the commit SHA, then `kustomize edit set image` + commits the bump back to `main` with `[skip ci]`. |
| `terraform-plan` | PR touching `deploy/terraform/**` | AWS OIDC (read-only role); fmt / validate / tflint / Trivy / plan; posts the plan as a PR comment. |
| `terraform-apply` | push to `main` touching `deploy/terraform/**` | AWS OIDC (write role); pauses on the `production` environment approval gate, then plan-then-apply of the saved plan. |

Both Terraform workflows fetch their input vars (`HCLOUD_TOKEN`,
`CLOUDFLARE_API_TOKEN`, `KUBEAPI_ALLOWED_IPS`) from `gemline/tf-vars`
in Secrets Manager at runtime, masked via `::add-mask::` before they
become env vars. That preserves "zero long-lived credentials in
GitHub" — the only thing GitHub holds is the OIDC identity itself.

CI never talks to the cluster: it commits an image-tag bump and **ArgoCD
does the rollout from inside the cluster**. No `KUBECONFIG` secret
needed.

End-to-end on an app change: **~3 min from `git push` to live.**

## Day-to-day

### Deploying

Push to `main`. The `deploy` workflow builds the images and bumps
`deploy/k8s/kustomization.yaml`; ArgoCD picks up the commit within ~60 s
and rolls the deployments (surge-up, drain, terminate — no downtime at
`replicas: 2`).

### Rolling back

`git revert <bad-sha>` on `main` — CI rebuilds for the previous code and
ArgoCD re-syncs. Or pin an already-built SHA without touching code:

```sh
cd deploy/k8s
kustomize edit set image ghcr.io/alexis-marcel/gemline-server=ghcr.io/alexis-marcel/gemline-server:<sha>
kustomize edit set image ghcr.io/alexis-marcel/gemline-web=ghcr.io/alexis-marcel/gemline-web:<sha>
git commit -am "rollback to <sha>" && git push
```

### Inspecting

```sh
kubectl -n gemline get pods
kubectl -n gemline logs deployment/gemline-server --tail=100 -f
kubectl -n gemline get certificate
kubectl -n argocd get applications
kubectl -n argocd port-forward svc/argocd-server 8443:443   # ArgoCD UI
```

ArgoCD's initial admin password:

```sh
kubectl -n argocd get secret argocd-initial-admin-secret \
  -o jsonpath='{.data.password}' | base64 -d; echo
```

## Scaling

- **More workers**: bump `worker_count`, `make deploy`. New nodes
  provision and join automatically; the deployments' topology-spread
  constraints redistribute pods.
- **HA control plane**: set `cp_count` to `3` (or `5`/`7`) and re-run.
  The Load Balancer targets CPs by label, so new control planes are
  picked up without Terraform churn.
- **More backend throughput**: raise `replicas` on `gemline-server` —
  the Postgres backplane keeps all replicas in sync.

## Known limits

- **No in-cluster Postgres** — we lean on Supabase. If Supabase is down,
  the SPA still serves but games don't load.
- **Single region** — all nodes share one Hetzner location.
- **No rate limiting** at the app layer yet.

## Tearing down & rebuilding

```sh
cd deploy
make destroy            # terraform destroy (interactive confirmation)
make deploy             # bring it back
make kubeconfig
make publish-jwks       # re-upload JWKS (k3s regenerates signing keys)
```

App secret **values** survive the rebuild — they live in AWS Secrets
Manager, outside Terraform state, with a 7-day recovery window. Only the
JWKS needs republishing because k3s regenerates its service-account
signing keys on `--cluster-init`.

**Let's Encrypt rate limits**: 5 certificates per week per hostname in
production. For repeated test rebuilds, point the ClusterIssuer at the
LE **staging** endpoint
(`https://acme-staging-v02.api.letsencrypt.org/directory`) — untrusted
in browsers, but it validates the full pipeline without burning quota.

## Troubleshooting

### Pods stuck `CreateContainerConfigError`
A referenced Secret/ConfigMap is missing. If it's `gemline-env`, the
IRSA chain hasn't completed — check `kubectl get clustersecretstore
aws-secrets-manager` (Ready should be True; if False, the ESO controller
logs show the STS error), then `kubectl -n gemline describe externalsecret
gemline-env`. Common causes: JWKS not yet published to S3 (run
`make publish-jwks`); the `eks.amazonaws.com/role-arn` annotation
missing on the ESO ServiceAccount; secret values not populated in
Secrets Manager.

### `runAsNonRoot` errors on distroless images
The server runs distroless with a symbolic `USER nonroot`; kubelet can't
verify the UID statically. The deployment sets `runAsUser: 65532`
(Google distroless nonroot UID) to satisfy it.

### Caddy `CrashLoopBackOff` with `exec: operation not permitted`
The web image's Caddy binary carries `cap_net_bind_service`; dropping all
capabilities makes the kernel refuse exec (EPERM). The web pod keeps the
capability (only `allowPrivilegeEscalation: false`).

### Certificate stuck `READY: False`
`kubectl -n gemline describe certificate gemline-tls`. Usual causes, in
order: (1) DNS not yet pointing at the LB; (2) port 80 not reachable
from the internet (firewall, or the LB http service unhealthy);
(3) Let's Encrypt rate limit — switch to staging.

### ArgoCD `SYNC STATUS: Unknown`
`kubectl -n argocd describe application gemline | grep Message`. If
`authentication required`, the repo is private and ArgoCD has no creds —
make it public or add a Repository secret in the `argocd` namespace.
