#!/usr/bin/env bash
# k3s control-plane bootstrap, run by cloud-init on first boot.
# Variables ${private_ip}, ${k3s_token}, ${argocd_version},
# ${argocd_apps_repo_raw} are templated in by Terraform.

set -euo pipefail
exec > >(tee /var/log/gemline-cloud-init.log) 2>&1

PRIVATE_IP="${private_ip}"
TOKEN="${k3s_token}"

# Wait for the Hetzner private NIC to come up (it's attached separately
# from the boot disk, sometimes a few seconds after cloud-init starts).
echo "==> waiting for private IP $PRIVATE_IP"
for i in $(seq 1 60); do
  ip -4 addr show | grep -q "$PRIVATE_IP" && { echo "got it"; break; }
  sleep 2
done

# Fallback: on Ubuntu 24.04 Hetzner images the netplan config that
# carries the private NIC sometimes isn't applied (interface stays
# DOWN with no IPv4). Without this, flannel exits in a tight loop with
# "failed to find IPv4 address for interface enp7s0" and k3s never
# stabilises. Idempotent — no-op if the wait above already succeeded.
if ! ip -4 addr show | grep -q "$PRIVATE_IP"; then
  echo "==> private IP still missing — bringing enp7s0 up manually"
  ip link set enp7s0 up || true
  ip addr add "$PRIVATE_IP/24" dev enp7s0 || true
fi

# Fetch our own public IPv4 so it can go into the kube-apiserver
# certificate SAN list — without it, `kubectl` from outside the cluster
# fails the TLS check.
PUBLIC_IP=$(curl -fsS4 https://ifconfig.me || echo "")
TLS_SAN_OPT=""
[ -n "$PUBLIC_IP" ] && TLS_SAN_OPT="--tls-san=$PUBLIC_IP"

echo "==> installing k3s server (private=$PRIVATE_IP, public=$PUBLIC_IP)"
# klipper-lb (servicelb) is kept enabled: with type: LoadBalancer services
# on bare-metal k3s, it spawns svclb-* DaemonSet pods that bind the host
# ports (80/443 for Traefik) and route to the service endpoints. Without
# it the LoadBalancer stays <pending> forever and there's no path from the
# public IP to the ingress controller.
curl -sfL https://get.k3s.io | \
  INSTALL_K3S_EXEC="server --node-ip=$PRIVATE_IP --advertise-address=$PRIVATE_IP --flannel-iface=enp7s0 $TLS_SAN_OPT" \
  K3S_TOKEN="$TOKEN" \
  sh -

echo "==> installing cert-manager"
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.16.1/cert-manager.yaml
# 8m headroom: on a cx23 first boot the image pulls + apiserver warm-up
# can chew 3+ min before any cert-manager pod is even Pending → Ready.
# Cloud-init runs once; if we time out here, set -e bails before
# ArgoCD installs and the whole bootstrap is dead. Cheap to be generous.
for d in cert-manager cert-manager-webhook cert-manager-cainjector; do
  kubectl -n cert-manager rollout status deployment/$d --timeout=8m
done

echo "==> installing ArgoCD ${argocd_version}"
kubectl get namespace argocd >/dev/null 2>&1 || kubectl create namespace argocd
# --server-side + --force-conflicts: the applicationsets.argoproj.io CRD
# has an OpenAPI schema larger than the 256KB last-applied-configuration
# annotation limit that client-side apply uses. SSA bypasses that and
# takes ownership cleanly across re-installs / upgrades.
kubectl apply -n argocd --server-side --force-conflicts \
  -f "https://raw.githubusercontent.com/argoproj/argo-cd/${argocd_version}/manifests/install.yaml"

echo "==> waiting for ArgoCD components"
for d in argocd-server argocd-repo-server argocd-applicationset-controller argocd-notifications-controller; do
  kubectl -n argocd rollout status deployment/$d --timeout=5m
done
kubectl -n argocd rollout status statefulset/argocd-application-controller --timeout=5m

echo "==> applying ArgoCD Applications (sealed-secrets + monitoring + gemline)"
# Pulled straight from the repo's main branch so the cluster bootstraps
# against the latest committed manifests. Updates to these files after
# this point are reconciled by ArgoCD itself — cloud-init only runs at
# first boot.
#
# sealed-secrets goes first so the controller is up by the time the
# gemline kustomize tries to apply a SealedSecret. ArgoCD doesn't block
# on apply order across Applications, but the controller's CRD must
# exist before the SealedSecret CR is parseable — apply order here
# helps cover the race on first boot.
APPS_URL="${argocd_apps_repo_raw}"
kubectl apply -f "$APPS_URL/app-sealed-secrets.yaml"
kubectl apply -f "$APPS_URL/app-monitoring.yaml"
kubectl apply -f "$APPS_URL/app-gemline.yaml"

echo "==> ArgoCD initial admin password (rotate after first login):"
# Echoing into the cloud-init log so the operator can recover it via
# `ssh root@<cp-ip> grep -A1 'admin password' /var/log/gemline-cloud-init.log`
# without needing kubectl access. The Secret is deleted by ArgoCD on
# first password change, so this log line is the only persistent copy.
kubectl -n argocd get secret argocd-initial-admin-secret \
  -o jsonpath='{.data.password}' | base64 -d
echo

echo "==> done"
