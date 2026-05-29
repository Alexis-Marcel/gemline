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
| Secrets | **Sealed Secrets → Infisical → External Secrets** | One committed (encrypted) bootstrap secret; everything else pulled from Infisical at runtime. |
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

Plus: domain ~€10/yr, Supabase free tier, GHCR (public) free, Infisical
free tier, AWS S3+DynamoDB state (cents/month). Prices are approximate —
check Hetzner's current `cx23` and `lb11` rates.

## Prerequisites

1. **Hetzner Cloud** account + API token (Read & Write).
2. **Cloudflare** zone for your domain + an API token scoped to *Edit
   zone DNS* on it.
3. **AWS** account hosting the Terraform state backend (an S3 bucket +
   DynamoDB lock table). Local runs authenticate via `AWS_PROFILE`; CI
   uses OIDC (no long-lived keys).
4. **Supabase** project (Postgres + auth). Note its Session-pooler
   `DATABASE_URL` and project URL.
5. **Infisical** project `gemline` with an environment `prod` holding:
   `DATABASE_URL`, `SUPABASE_URL`, `ALLOWED_ORIGINS`, and the Grafana
   `admin-user` / `admin-password`. Plus a Universal-Auth machine
   identity (its `clientId`/`clientSecret` bootstrap External Secrets).
6. **Local tools**: `terraform`, `ansible`, `kubectl`, `kubeseal`,
   `make`, `jq`, plus an SSH key.

> If you fork the repo, search-and-replace the hard-coded identifiers:
> hostname `gemline.werilo.fr`, email `alexismarcel55@gmail.com`, repo
> `Alexis-Marcel/gemline`, GHCR `alexis-marcel/...`, and the AWS/Infisical
> IDs in `.github/workflows/` and `deploy/terraform/versions.tf`.

## One-command bootstrap

```sh
cd deploy

# 1. Fill in your secrets / vars (gitignored):
cp terraform/terraform.tfvars.example terraform/terraform.tfvars
$EDITOR terraform/terraform.tfvars      # hcloud_token, cloudflare_api_token,
                                        # dns_zone/subdomain, cp_count, …

export AWS_PROFILE=gemline              # for the S3 state backend

# 2. Provision + configure everything:
make deploy                            # = terraform apply  +  ansible-playbook
```

`make deploy` runs two layers:

### Terraform layer (`terraform apply`)

Provisions the VMs, private network (`10.0.0.0/16`), a public firewall
(22/80/443 open; the kube API on 6443 closed unless you list your IP in
`kubeapi_allowed_ips`), a Hetzner Load Balancer fronting the control
plane, and the Cloudflare A record pointing the app hostname at the LB.
cloud-init only brings up the private NIC — k3s and everything else is
Ansible's job. State lives remotely on S3 (locked via DynamoDB).

### Ansible layer (`ansible-playbook --ask-vault-pass`)

The inventory is **dynamic** (`inventory.tf.py` reads `terraform output
-json` — no IPs to maintain by hand). Roles run in order:

1. `networking` — base network setup on every node.
2. `k3s_server` (control plane, `serial: 1`) — the first CP runs
   `--cluster-init` to bootstrap embedded etcd; additional CPs join
   through the Load Balancer. All add the LB IP as a TLS SAN.
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

Ansible applies five ArgoCD `Application`s (listed in
`deploy/ansible/group_vars/all/vars.yaml`); ArgoCD syncs each from this
repo or an upstream Helm chart, with `prune` + `selfHeal` +
`ServerSideApply`:

| Application | Source | What it deploys |
|---|---|---|
| `sealed-secrets` | Helm (Bitnami) 2.16.2 | Sealed Secrets controller in `kube-system`. |
| `external-secrets` | Helm 0.11.0 | External Secrets Operator (ESO) in `external-secrets`. |
| `eso-config` | git `deploy/k8s/external-secrets` | The bootstrap SealedSecret, the `ClusterSecretStore`, and the `ExternalSecret`s. |
| `monitoring` | Helm kube-prometheus-stack 75.6.0 | Prometheus + Grafana + Alertmanager in `monitoring`. |
| `gemline` | git `deploy/k8s` | The app: server (×2), web (×3), services, ingress, ClusterIssuer, ServiceMonitor. |

Day-to-day, **nobody runs `kubectl apply`** for the app — you push to
`main` and ArgoCD rolls the change forward.

## Secrets — Sealed Secrets → Infisical → ESO

No plaintext secret is ever committed, and no secret is created by hand
on the cluster. The chain bootstraps itself:

```
  committed (encrypted)          in-cluster                 runtime
 ┌──────────────────────┐   ┌──────────────────┐   ┌────────────────────┐
 │ sealed-infisical-    │ → │ Secret           │ → │ ClusterSecretStore │
 │ auth.yaml            │   │ infisical-auth   │   │ infisical-prod     │
 │ (SealedSecret)       │   │ (clientId/secret)│   │ (talks to Infisical)│
 └──────────────────────┘   └──────────────────┘   └─────────┬──────────┘
   decrypted by the                                           │ pulls
   Sealed Secrets controller                                  ▼
                                              ┌────────────────────────────┐
                                              │ ExternalSecret → K8s Secret │
                                              │  gemline-env, grafana-admin │
                                              └────────────────────────────┘
                                                  consumed via envFrom
```

Only the Infisical machine-identity credentials are sealed into git
(encrypted, useless without the cluster's private key). Everything else
(`DATABASE_URL`, `SUPABASE_URL`, `ALLOWED_ORIGINS`, Grafana admin) is
pulled live from Infisical by ESO and refreshed hourly.

**Bootstrap gotcha:** a fresh cluster generates a *new* Sealed Secrets
key, so the committed `sealed-infisical-auth.yaml` (sealed with the old
key) won't decrypt. Either restore the backed-up key, or re-seal:

```sh
kubeseal --controller-namespace kube-system --fetch-cert > pub.pem
# craft the infisical-auth Secret locally, then:
kubeseal --cert pub.pem -o yaml \
  < infisical-auth-secret.yaml \
  > deploy/k8s/external-secrets/sealed-infisical-auth.yaml
git commit && git push     # ArgoCD applies it; the controller decrypts it
```

> **Back up the Sealed Secrets master key** (`Secret sealed-secrets-key`
> in `kube-system`) before any teardown — it's the only thing that can
> decrypt committed SealedSecrets on a rebuild.

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
admin credentials come from Infisical via ESO.

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
| `terraform-plan` | PR touching `deploy/terraform/**` | OIDC to AWS (read-only) + Infisical; fmt / validate / tflint / Trivy / plan; posts the plan as a PR comment. |
| `terraform-apply` | push to `main` touching `deploy/terraform/**` | OIDC to AWS (write) + Infisical; pauses on the `production` environment approval gate, then plan-then-apply of the saved plan. |

CI never talks to the cluster: it commits an image-tag bump and **ArgoCD
does the rollout from inside the cluster**. No `KUBECONFIG` secret
needed. There are zero long-lived cloud credentials in GitHub — both
AWS and Infisical are reached via OIDC.

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
# Back up the Sealed Secrets key first (see Secrets section)!
make destroy            # terraform destroy (interactive confirmation)
make deploy             # bring it back
```

**Let's Encrypt rate limits**: 5 certificates per week per hostname in
production. For repeated test rebuilds, point the ClusterIssuer at the
LE **staging** endpoint
(`https://acme-staging-v02.api.letsencrypt.org/directory`) — untrusted
in browsers, but it validates the full pipeline without burning quota.

## Troubleshooting

### Pods stuck `CreateContainerConfigError`
A referenced Secret/ConfigMap is missing. If it's `gemline-env`, the
secrets chain hasn't completed — check the `ExternalSecret` status
(`kubectl -n gemline describe externalsecret gemline-env`) and that
`infisical-auth` decrypted in `external-secrets`.

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
