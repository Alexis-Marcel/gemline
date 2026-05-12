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

### 1. Provision a VPS

Create a `CX22` (2 vCPU, 4 GB RAM, 40 GB SSD) on Hetzner Cloud, Ubuntu
24.04. Note its IPv4 address.

### 2. Point your domain at it

In your DNS provider, create an `A` record:

```
gemline.<your-domain>.   A   <vps-ip>
```

Wait for propagation (`dig gemline.<your-domain>` should return the IP).

### 3. Install k3s + cert-manager

```sh
scp deploy/bootstrap.sh root@<vps-ip>:/tmp/
ssh root@<vps-ip> "bash /tmp/bootstrap.sh"
```

This runs ~2 minutes. Follow the printed next steps from the script.

### 4. Get a local kubeconfig

```sh
scp root@<vps-ip>:/etc/rancher/k3s/k3s.yaml ~/.kube/gemline.yaml
# Replace the loopback IP with the public one
sed -i '' "s/127.0.0.1/<vps-ip>/" ~/.kube/gemline.yaml
export KUBECONFIG=~/.kube/gemline.yaml
kubectl get nodes
```

### 5. Edit hostnames in the manifests

`deploy/k8s/ingress.yaml` and `deploy/k8s/cluster-issuer.yaml` both
ship with placeholder strings:

- `gemline.example.com` → your real hostname (in `ingress.yaml`)
- `you@example.com`     → your real email (in `cluster-issuer.yaml`)

### 6. Apply the cluster-wide bits

```sh
kubectl apply -f deploy/k8s/cluster-issuer.yaml
kubectl create namespace gemline
kubectl -n gemline create secret generic gemline-env \
  --from-literal=DATABASE_URL='postgresql://postgres.<project>:<password>@aws-...pooler.supabase.com:5432/postgres' \
  --from-literal=SUPABASE_URL='https://<project>.supabase.co'
```

### 7. First deploy (manual)

Before wiring CI/CD, validate the manifests by deploying once by hand
with images you build locally and push to GHCR:

```sh
# Build + push the images
docker build -t ghcr.io/<you>/gemline-server:bootstrap -f Dockerfile .
docker build -t ghcr.io/<you>/gemline-web:bootstrap \
  --build-arg VITE_SUPABASE_URL=... \
  --build-arg VITE_SUPABASE_PUBLISHABLE_KEY=... \
  -f web/Dockerfile web/

# (login to GHCR if needed:
#   echo "$GHCR_PAT" | docker login ghcr.io -u <you> --password-stdin)
docker push ghcr.io/<you>/gemline-server:bootstrap
docker push ghcr.io/<you>/gemline-web:bootstrap

# Apply manifests, then pin to the bootstrap tag
kubectl apply -k deploy/k8s/
kubectl -n gemline set image deployment/gemline-server server=ghcr.io/<you>/gemline-server:bootstrap
kubectl -n gemline set image deployment/gemline-web web=ghcr.io/<you>/gemline-web:bootstrap

# Watch
kubectl -n gemline get pods -w
kubectl -n gemline get certificate     # should show READY=True within ~1 min
```

Visit `https://gemline.<your-domain>` — Gemline should be live.

### 8. Configure GitHub Actions secrets

In **Settings → Secrets and variables → Actions** on the repo, add:

| Secret name                       | Value                                                                                  |
| --------------------------------- | -------------------------------------------------------------------------------------- |
| `VITE_SUPABASE_URL`               | `https://<project>.supabase.co`                                                        |
| `VITE_SUPABASE_PUBLISHABLE_KEY`   | `sb_publishable_...`                                                                   |
| `KUBECONFIG`                      | `base64 -i ~/.kube/gemline.yaml \| pbcopy` — paste the base64                          |

The `deploy.yml` workflow already references those names. Push any
change touching backend or frontend code and CI will build + push +
roll out. Watch the run from the Actions tab.

## Day-to-day

### Deploying

Push to `main`. The `deploy` workflow builds new images tagged with the
commit SHA, pushes them to GHCR, then `kubectl set image` rolls the
deployments. Total time: ~3 min including image build.

### Rolling back

Find a working SHA in the GHCR registry and:

```sh
kubectl -n gemline set image deployment/gemline-server server=ghcr.io/<you>/gemline-server:<sha>
kubectl -n gemline set image deployment/gemline-web web=ghcr.io/<you>/gemline-web:<sha>
```

Or just revert the offending commit on `main` — CI will redeploy.

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
