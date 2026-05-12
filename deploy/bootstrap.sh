#!/usr/bin/env bash
# Bootstrap a fresh Linux VPS (Debian/Ubuntu) for Gemline.
#
# Run this as root on the new VPS *once*. It installs k3s with
# Traefik already enabled, then layers cert-manager on top so the
# Ingress can request Let's Encrypt certificates automatically.
#
# Usage:   sudo ./bootstrap.sh

set -euo pipefail

if [[ "$(id -u)" != "0" ]]; then
  echo "must run as root" >&2
  exit 1
fi

# --- k3s ----------------------------------------------------------------
# Single-server install. Traefik comes bundled and listens on :80 / :443.
# We disable the embedded servicelb (Klipper) — for one node it's just
# extra network plumbing; Traefik binds host ports directly.
if ! command -v k3s >/dev/null; then
  echo "==> installing k3s"
  curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="--disable=servicelb" sh -
fi

# --- cert-manager -------------------------------------------------------
if ! kubectl get ns cert-manager >/dev/null 2>&1; then
  echo "==> installing cert-manager"
  kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.16.1/cert-manager.yaml
  kubectl -n cert-manager rollout status deployment/cert-manager --timeout=2m
  kubectl -n cert-manager rollout status deployment/cert-manager-webhook --timeout=2m
  kubectl -n cert-manager rollout status deployment/cert-manager-cainjector --timeout=2m
fi

# --- kubeconfig hint ----------------------------------------------------
cat <<EOF

==> done

Next steps (run from your laptop, not the VPS):

  1. Copy the kubeconfig and edit the server URL:
       scp root@<vps-ip>:/etc/rancher/k3s/k3s.yaml ~/.kube/gemline.yaml
       sed -i '' "s/127.0.0.1/<vps-ip>/" ~/.kube/gemline.yaml
       export KUBECONFIG=~/.kube/gemline.yaml
       kubectl get nodes   # should show the VPS as Ready

  2. Edit deploy/k8s/ingress.yaml and deploy/k8s/cluster-issuer.yaml:
     replace 'gemline.example.com' with your real hostname and
     'you@example.com' with your real email.

  3. Apply the ClusterIssuer (once per cluster):
       kubectl apply -f deploy/k8s/cluster-issuer.yaml

  4. Create the gemline-env secret with your Supabase values:
       kubectl create namespace gemline
       kubectl -n gemline create secret generic gemline-env \\
         --from-literal=DATABASE_URL='postgresql://...' \\
         --from-literal=SUPABASE_URL='https://<project>.supabase.co'

  5. Apply the manifests:
       kubectl apply -k deploy/k8s/

  6. Watch the rollout + cert provisioning:
       kubectl -n gemline get pods -w
       kubectl -n gemline get certificate
EOF
