# Deploying Gemline

This runs Gemline on a single-node **k3s** cluster on any Linux VPS,
behind TLS-terminated **Traefik** ingress, with images built and pushed
by **GitHub Actions** to **GHCR**, and rolled out via `kubectl` from
the CI. Data lives in your **Supabase** project.

## Cost

- VPS (e.g. Hetzner CX22): ~4.5 €/month
- Domain (if you don't have one): ~10 €/year
- Supabase free tier: 0 €
- GitHub Actions, GHCR: 0 € for public repos

## Architecture

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
         │ 1 pod  │  │ static   │
         └────┬───┘  └──────────┘
              │
              │ outbound HTTPS
              ▼
        ┌──────────┐
        │ Supabase │
        └──────────┘
```

## One-time setup

### 1. Provision a VPS — Terraform

```sh
cd deploy/terraform
cp terraform.tfvars.example terraform.tfvars
# fill in hcloud_token (Hetzner Console → Security → API Tokens)
terraform init
terraform apply
```

Cloud-init runs `deploy/bootstrap.sh` automatically as soon as the
server boots, so k3s + cert-manager are already installed by the time
SSH is reachable (~3 minutes after apply).

Don't want IaC? Click through Hetzner Cloud's web UI to create a
`CX22` (2 vCPU / 4 GB / 40 GB) on Ubuntu 24.04 with your SSH key,
then run `bash deploy/bootstrap.sh` on it manually.

### 2. Point your domain at it

In your DNS provider, create an `A` record:

```
gemline.<your-domain>.   A   <vps-ip>
```

The Terraform output prints the IP — `terraform output ipv4_address`.

Wait for propagation (`dig gemline.<your-domain>` should return the IP).

### 3. Get a local kubeconfig

```sh
scp root@<vps-ip>:/etc/rancher/k3s/k3s.yaml ~/.kube/gemline.yaml
# Replace the loopback IP with the public one
sed -i '' "s/127.0.0.1/<vps-ip>/" ~/.kube/gemline.yaml
export KUBECONFIG=~/.kube/gemline.yaml
kubectl get nodes
```

### 4. Edit hostnames in the manifests

`deploy/k8s/ingress.yaml` and `deploy/k8s/cluster-issuer.yaml` both
ship with placeholder strings:

- `gemline.example.com` → your real hostname (in `ingress.yaml`)
- `you@example.com`     → your real email (in `cluster-issuer.yaml`)

Commit and push these edits — ArgoCD picks them up automatically.

### 5. Apply the cluster-wide bits

These two never live in the GitOps loop (one carries secrets, the
other is cluster-scoped):

```sh
kubectl apply -f deploy/k8s/cluster-issuer.yaml
kubectl create namespace gemline
kubectl -n gemline create secret generic gemline-env \
  --from-literal=DATABASE_URL='postgresql://postgres.<project>:<password>@aws-...pooler.supabase.com:5432/postgres' \
  --from-literal=SUPABASE_URL='https://<project>.supabase.co'
```

### 6. Install ArgoCD and declare the Gemline Application

```sh
bash deploy/argocd/install.sh
```

This installs ArgoCD on the cluster and applies
`deploy/argocd/app-gemline.yaml`, an `Application` that watches the
repo's `deploy/k8s/` directory on `main` with auto-sync + self-heal.

To reach the dashboard, follow the script's printed instructions:

```sh
kubectl -n argocd port-forward svc/argocd-server 8443:443
# then open https://localhost:8443 (admin password printed by the script)
```

### 7. Configure GitHub Actions secrets

In **Settings → Secrets and variables → Actions** on the repo, add:

| Secret name                     | Value                            |
| ------------------------------- | -------------------------------- |
| `VITE_SUPABASE_URL`             | `https://<project>.supabase.co`  |
| `VITE_SUPABASE_PUBLISHABLE_KEY` | `sb_publishable_...`             |

No `KUBECONFIG` secret needed: CI doesn't talk to the cluster anymore —
it commits the new image tag back to `deploy/k8s/kustomization.yaml`,
and ArgoCD does the actual rollout from inside the cluster.

## Day-to-day

### Deploying

Push to `main`. The `deploy` workflow:

1. Builds the two images and pushes them to GHCR tagged with the
   commit SHA.
2. Updates `deploy/k8s/kustomization.yaml` to point at those SHAs
   and commits the bump back to `main` (`[skip ci]` to avoid a
   recursive workflow run).
3. ArgoCD detects the new commit within ~60 s, syncs, and rolls
   the deployments.

End-to-end time: ~3 min including image build and ArgoCD sync.

### Rolling back

`git revert <bad-sha>` on `main` — CI rebuilds the images for the
previous code, pushes them, bumps the manifest tag back, and ArgoCD
re-syncs to the older version. Pure git workflow, no kubectl needed.

If you need to roll back to a specific previously-built SHA without
revisiting the code, edit `deploy/k8s/kustomization.yaml` by hand:

```sh
cd deploy/k8s
kustomize edit set image ghcr.io/<you>/gemline-server=ghcr.io/<you>/gemline-server:<sha>
kustomize edit set image ghcr.io/<you>/gemline-web=ghcr.io/<you>/gemline-web:<sha>
git add kustomization.yaml && git commit -m "rollback to <sha>" && git push
```

### Updating a secret

```sh
kubectl -n gemline create secret generic gemline-env \
  --from-literal=DATABASE_URL='...' \
  --from-literal=SUPABASE_URL='...' \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n gemline rollout restart deployment/gemline-server
```

### Inspecting

```sh
kubectl -n gemline get pods
kubectl -n gemline logs deployment/gemline-server --tail=100 -f
kubectl -n gemline describe ingress gemline
kubectl -n gemline get certificate
```

### When TLS issuance gets stuck

```sh
kubectl -n gemline describe certificate gemline-tls
kubectl -n gemline get challenge
```

The most common cause is DNS not pointing at the VPS yet, or port 80
blocked at the firewall. Let's Encrypt's HTTP-01 challenge needs both.

## Known limits

- **Single backend replica** — the WebSocket hub is per-process. Adding
  a second replica today would mean half the players miss broadcasts
  from the other half. Adding inter-process pub/sub (Postgres
  `LISTEN/NOTIFY`, Redis, NATS) would lift this.
- **Single node** — no HA. If the VPS goes down, the app goes down.
  Acceptable for a portfolio project; for anything more, switch to a
  managed Kubernetes (DOKS, GKE Autopilot, etc.) and the same manifests
  apply.
- **No in-cluster Postgres** — we lean on Supabase. If Supabase is down
  the app can still serve the SPA and the static parts but games won't
  load.
