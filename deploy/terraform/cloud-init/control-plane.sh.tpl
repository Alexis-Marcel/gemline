#!/usr/bin/env bash
# k3s control-plane bootstrap, run by cloud-init on first boot.
# Variables ${private_ip}, ${k3s_token} are templated in by Terraform.

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

# Fetch our own public IPv4 so it can go into the kube-apiserver
# certificate SAN list — without it, `kubectl` from outside the cluster
# fails the TLS check.
PUBLIC_IP=$(curl -fsS4 https://ifconfig.me || echo "")
TLS_SAN_OPT=""
[ -n "$PUBLIC_IP" ] && TLS_SAN_OPT="--tls-san=$PUBLIC_IP"

echo "==> installing k3s server (private=$PRIVATE_IP, public=$PUBLIC_IP)"
curl -sfL https://get.k3s.io | \
  INSTALL_K3S_EXEC="server --disable=servicelb --node-ip=$PRIVATE_IP --advertise-address=$PRIVATE_IP --flannel-iface=enp7s0 $TLS_SAN_OPT" \
  K3S_TOKEN="$TOKEN" \
  sh -

echo "==> installing cert-manager"
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.16.1/cert-manager.yaml
for d in cert-manager cert-manager-webhook cert-manager-cainjector; do
  kubectl -n cert-manager rollout status deployment/$d --timeout=3m
done

echo "==> done"
