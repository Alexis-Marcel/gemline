# Deploying Gemline

Reproducible deploy of Gemline to a fresh self-hosted Kubernetes cluster
on Hetzner Cloud, with TLS, GitOps, and CI/CD wired end-to-end.

## What you get

```
              Internet (HTTPS)
                     │
                     ▼
         ┌───────────────────────┐
         │  Traefik (k3s, :443)  │
         │   TLS via             │
         │   cert-manager + LE   │
         └─────┬─────────┬───────┘
               │         │
   /api/* /ws/*│         │ /
   /healthz    ▼         ▼
         ┌────────┐  ┌──────────┐
         │ server │  │ web      │
         │ Go     │  │ Caddy +  │
         │ 1 pod  │  │ 3 pods   │
         └────┬───┘  └──────────┘
              │
              │ outbound HTTPS
              ▼
        ┌──────────┐
        │ Supabase │
        └──────────┘
```

- 2 VPS Hetzner cx23 (2 vCPU / 4 GB each): control plane + 1 worker
- k3s with klipper-lb (no external load balancer needed)
- cert-manager + Let's Encrypt for automatic TLS
- ArgoCD pulling manifests from this repo, auto-syncing on every push
- GitHub Actions building images to GHCR on every commit to `main`,
  then bumping the kustomization tag back to git for ArgoCD to roll out

## Cost

| | |
|---|---|
| 2× Hetzner cx23 | ~9 €/mo |
| Domain | ~10 €/year |
| Supabase free tier | 0 € |
| GitHub Actions + GHCR (public repo) | 0 € |

## Prerequisites

Before running anything, make sure you have:

1. **Hetzner Cloud account** with an API token (Console → Security → API
   Tokens → "Read & Write").
2. **A registered domain** with control over its DNS records.
3. **A Supabase project** with auth enabled. Note its:
   - Project URL (`https://<ref>.supabase.co`)
   - Database password
   - Region (visible in the project settings)
4. **This repo forked or cloned**, with:
   - The GitHub repository **public** (or set up ArgoCD repo credentials
     in step 6 — public is simpler).
   - GHCR packages **public** for both `gemline-server` and `gemline-web`
     (Settings → Packages → Visibility), so the cluster can pull without
     an imagePullSecret.
5. **Local tools**: `terraform`, `kubectl`, `git`, `openssl`, `curl`.

## One-time setup

### 1. Provision the cluster — Terraform

```sh
cd deploy/terraform
cp terraform.tfvars.example terraform.tfvars
```

Edit `terraform.tfvars`:

```hcl
hcloud_token = "<your Hetzner token>"
k3s_token    = "<run: openssl rand -hex 32>"

# Your IP, so kubectl can reach the API server directly.
# Closed by default (empty list). To find yours:
#   curl -s -4 ifconfig.me           # IPv4
#   curl -s -6 ifconfig.me           # IPv6 — take first 4 groups + "::/64"
kubeapi_allowed_ips = [
  "<your IPv4>/32",
  "<your IPv6 /64 prefix>",
]
```

Apply:

```sh
terraform init
terraform apply
```

This brings up 1 control-plane + 1 worker VPS in ~3 minutes. Cloud-init
installs k3s on both, joins the worker via the private network, and
deploys cert-manager on the CP.

**Verify**:

```sh
terraform output         # note the IPv4 — you'll need it everywhere
```

### 2. Point your domain at the cluster

In your DNS provider, create:

```
gemline.<your-domain>.   A   <cp-ipv4 from terraform output>
TTL: 60s (raise it later once things are stable)
```

**Verify** (wait until this returns the right IP):

```sh
dig +short gemline.<your-domain>
```

### 3. Fetch the kubeconfig

```sh
PUBLIC_IP=<your cp ipv4>
scp root@$PUBLIC_IP:/etc/rancher/k3s/k3s.yaml ~/.kube/gemline.yaml
sed -i '' "s/127.0.0.1/$PUBLIC_IP/" ~/.kube/gemline.yaml   # on macOS
# or:    sed -i "s/127.0.0.1/$PUBLIC_IP/" ~/.kube/gemline.yaml   # on Linux

export KUBECONFIG=~/.kube/gemline.yaml
# Make it persistent so future shells inherit it:
echo "export KUBECONFIG=~/.kube/gemline.yaml" >> ~/.zshrc
```

**Verify**:

```sh
kubectl get nodes        # 2 nodes, both Ready
```

### 4. Edit the hostname-bearing manifests

Both `deploy/k8s/ingress.yaml` and `deploy/k8s/cluster-issuer.yaml` ship
with the live hostnames from this repo. **If you forked**, change:

- `gemline.werilo.fr` → your hostname (in `ingress.yaml`)
- `alexismarcel55@gmail.com` → your email (in `cluster-issuer.yaml`)
- `Alexis-Marcel/gemline` → `<you>/gemline` (in `deploy/argocd/app-gemline.yaml`,
  `.github/workflows/deploy.yml`, `deploy/k8s/kustomization.yaml`)

Commit and push these edits — ArgoCD will pick them up from git in
step 6.

### 5. Apply the cluster-wide bits

These never live in the GitOps loop — the Secret carries credentials,
the ClusterIssuer is cluster-scoped:

```sh
# ClusterIssuer for Let's Encrypt production.
kubectl apply -f deploy/k8s/cluster-issuer.yaml

# Namespace (ArgoCD will also create it later, but we need it now to
# host the Secret before pods come up).
kubectl create namespace gemline

# Supabase credentials.
# IMPORTANT: use the Session pooler URL, not the direct connection.
# The direct connection (db.<ref>.supabase.co) is IPv6-only since 2024
# and most VPS providers don't give you IPv6 egress — connections will
# fail with "network is unreachable". The Session pooler is IPv4 and
# behaves the same way for long-lived backends.
# Find it in Supabase: Project → Connect → Session pooler.
kubectl -n gemline create secret generic gemline-env \
  --from-literal=DATABASE_URL='postgresql://postgres.<ref>:<password>@aws-X-<region>.pooler.supabase.com:5432/postgres' \
  --from-literal=SUPABASE_URL='https://<ref>.supabase.co'
```

**Verify**:

```sh
kubectl -n gemline get secret gemline-env       # DATA: 2
kubectl get clusterissuer letsencrypt-prod      # READY: True (within ~10s)
```

### 6. Install ArgoCD and declare the Gemline Application

```sh
bash deploy/argocd/install.sh
```

This installs ArgoCD in its own namespace and applies the
`Application` manifest watching `deploy/k8s/` on `main` with
auto-sync + self-heal.

The script uses `kubectl apply --server-side --force-conflicts`. That
matters: the ArgoCD CRDs (specifically `applicationsets.argoproj.io`)
embed an OpenAPI schema larger than the 256 KB limit Kubernetes places
on the `last-applied-configuration` annotation that client-side apply
uses. Server-side apply moves the diff to the API server and avoids
that annotation entirely.

**Verify** (give it ~2 min for the first rollout):

```sh
kubectl -n argocd get pods                # all Running
kubectl -n argocd get application gemline # SYNC: Synced, HEALTH: Healthy
kubectl -n gemline get pods               # 1 server + 3 web, all Running
kubectl -n gemline get certificate        # READY: True (within ~1 min)
kubectl -n gemline get ingress            # ADDRESS = your cp ipv4
curl -I https://gemline.<your-domain>     # HTTP/2 200 with a real LE cert
```

If `Healthy` takes a while, watch with `-w`. cert-manager runs an
HTTP-01 challenge against your domain; it needs DNS to resolve and
port 80 to reach the cluster.

### 7. Configure GitHub Actions secrets

In **Settings → Secrets and variables → Actions** on the repo, add:

| Secret name                     | Value                            |
| ------------------------------- | -------------------------------- |
| `VITE_SUPABASE_URL`             | `https://<ref>.supabase.co`      |
| `VITE_SUPABASE_PUBLISHABLE_KEY` | `sb_publishable_...`             |

CI doesn't talk to the cluster directly — it commits new image tags
back to `deploy/k8s/kustomization.yaml`, and ArgoCD does the rollout
from inside the cluster. So no `KUBECONFIG` secret needed.

## Day-to-day

### Deploying

Push to `main`. The `deploy` workflow:

1. Builds the two images and pushes them to GHCR tagged with the
   commit SHA.
2. Updates `deploy/k8s/kustomization.yaml` to point at those SHAs
   and commits the bump back to `main` with `[skip ci]`.
3. ArgoCD picks up the new commit within ~60 s, syncs, and rolls
   the deployments.

End-to-end: ~3 min from `git push` to live.

### Rolling back

`git revert <bad-sha>` on `main` — CI rebuilds the images for the
previous code, the manifest bump reverts to the older SHA, and ArgoCD
re-syncs. Pure git workflow.

If you need to roll back to a specific already-built SHA without
touching the code:

```sh
cd deploy/k8s
kustomize edit set image ghcr.io/<you>/gemline-server=ghcr.io/<you>/gemline-server:<sha>
kustomize edit set image ghcr.io/<you>/gemline-web=ghcr.io/<you>/gemline-web:<sha>
git commit -am "rollback to <sha>" && git push
```

### Updating the Secret

```sh
kubectl -n gemline create secret generic gemline-env \
  --from-literal=DATABASE_URL='...' \
  --from-literal=SUPABASE_URL='...' \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl -n gemline rollout restart deployment/gemline-server
```

The `--dry-run | apply` form upserts: it replaces the existing Secret
instead of erroring out.

### Inspecting

```sh
kubectl -n gemline get pods
kubectl -n gemline logs deployment/gemline-server --tail=100 -f
kubectl -n gemline describe ingress gemline
kubectl -n gemline get certificate
```

### The ArgoCD dashboard

```sh
kubectl -n argocd port-forward svc/argocd-server 8443:443
# open https://localhost:8443
```

Login as `admin`. The initial password is in the cluster:

```sh
kubectl -n argocd get secret argocd-initial-admin-secret \
  -o jsonpath='{.data.password}' | base64 -d; echo
```

Change it via the UI (User Info → Update Password) on first login.

## Scaling

### Adding workers

Bump `worker_count` in `deploy/terraform/terraform.tfvars`, then
`terraform apply`. New nodes provision in ~90 s and join automatically.
The web Deployment has `topologySpreadConstraints` on the hostname so
its 3 replicas spread across nodes as you add them.

```sh
kubectl -n gemline get pods -o wide       # one web pod per node, ideally
```

### Why the backend stays at replicas = 1

The WebSocket hub lives in-process. With 2+ server pods, a player whose
WS lands on pod A sees moves broadcast from pod A; a player on pod B
sees moves broadcast from pod B. Postgres stays consistent via the REST
path, but live updates diverge.

The fix is a pub/sub backplane via Postgres `LISTEN/NOTIFY` (we already
have Postgres) or Redis pub/sub. Not implemented yet — `replicas: 1`
is the honest current ceiling.

## Tearing down and rebuilding

The whole stack is reproducible by design. To validate that:

```sh
cd deploy/terraform
terraform destroy           # 5 min, nukes the cluster cleanly
terraform apply             # 3 min, brings it back

# New IP — update DNS A record (lower TTL beforehand if you want)

# Refresh kubeconfig (see step 3)

# Update terraform.tfvars kubeapi_allowed_ips if your IP changed

# Apply cluster-wide bits (step 5)
kubectl apply -f deploy/k8s/cluster-issuer.yaml
kubectl create namespace gemline
kubectl -n gemline create secret generic gemline-env --from-literal=...

# Install ArgoCD (step 6)
bash deploy/argocd/install.sh
```

**Watch out for Let's Encrypt rate limits**: 5 certificates per week
per hostname in production. If you rebuild the cluster many times in a
row and re-issue the same cert each time, you'll be blocked for 7 days.

For repeated test rebuilds, point the ClusterIssuer at the LE **staging**
endpoint instead. Edit `deploy/k8s/cluster-issuer.yaml`:

```yaml
server: https://acme-staging-v02.api.letsencrypt.org/directory
```

The cert won't be trusted by browsers (untrusted root), but the full
pipeline gets validated without quota cost. Switch back to prod once
you trust the setup.

## Known limits

- **Single backend replica** — see above.
- **Single control-plane node** — if the CP node dies, the cluster API
  is down even if workers keep running their pods. HA needs 3+ CP nodes
  with embedded etcd (`k3s server --cluster-init` + `--server` join).
  Doable but not wired into the current Terraform.
- **No in-cluster Postgres** — we lean on Supabase. If Supabase is
  down, the SPA still serves but games don't load.

## Troubleshooting

### Pods stuck `CreateContainerConfigError`

A referenced Secret or ConfigMap doesn't exist in the namespace. Check
the Events of one of the affected pods — kubelet tells you which.

### Pods stuck `CrashLoopBackOff` with `exec: operation not permitted`

The container image has a binary with file capabilities (e.g. Caddy with
`cap_net_bind_service`) and the pod's `securityContext.capabilities.drop`
removes that capability at the bounding-set level. The kernel refuses
exec with EPERM. Either keep the capability or remove the drop.

### Pods stuck on `runAsNonRoot` for distroless images

Distroless images set `USER nonroot` (symbolic). Kubelet can't verify
statically whether that's UID 0, so it refuses. Add
`runAsUser: 65532` (the nonroot UID in Google's distroless) to the
pod's `securityContext`.

### Certificate stuck `READY: False`

`kubectl -n gemline describe certificate gemline-tls` and look at the
Challenge. Common causes, in order:

1. DNS doesn't yet point at the cluster. Wait, or fix the A record.
2. Port 80 isn't reachable from the public internet. Either the
   firewall blocks it, or `kube-system/traefik` has no `EXTERNAL-IP`
   (klipper-lb missing — the Terraform cloud-init should have it
   enabled by default).
3. Let's Encrypt rate-limited you. Switch to staging and wait a week.

### ArgoCD `SYNC STATUS: Unknown`

Check `kubectl -n argocd describe application gemline | grep Message`.
The two cases we've hit:

- `authentication required` → the GitHub repo is private and ArgoCD has
  no credentials. Make the repo public, or add a Repository secret in
  the `argocd` namespace.
- `field not declared in schema: .status.terminatingReplicas` →
  ArgoCD < v3 doesn't know about K8s 1.31+ Deployment fields. Bump
  `ARGOCD_VERSION` in `deploy/argocd/install.sh` and re-run.
