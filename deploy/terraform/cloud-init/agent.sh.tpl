#!/usr/bin/env bash
# k3s agent bootstrap. Joins the control plane over the Hetzner private
# network using the pre-shared token. Variables are templated in by
# Terraform.

set -euo pipefail
exec > >(tee /var/log/gemline-cloud-init.log) 2>&1

PRIVATE_IP="${private_ip}"
CP_PRIVATE_IP="${cp_private_ip}"
TOKEN="${k3s_token}"

echo "==> waiting for private IP $PRIVATE_IP"
for i in $(seq 1 60); do
  ip -4 addr show | grep -q "$PRIVATE_IP" && { echo "got it"; break; }
  sleep 2
done

echo "==> waiting for control plane API at $CP_PRIVATE_IP:6443"
for i in $(seq 1 120); do
  if curl -fsk -o /dev/null "https://$CP_PRIVATE_IP:6443/"; then
    echo "CP reachable"
    break
  fi
  sleep 5
done

echo "==> installing k3s agent"
curl -sfL https://get.k3s.io | \
  INSTALL_K3S_EXEC="agent --node-ip=$PRIVATE_IP --flannel-iface=enp7s0" \
  K3S_URL="https://$CP_PRIVATE_IP:6443" \
  K3S_TOKEN="$TOKEN" \
  sh -

echo "==> done"
