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

# Fallback: on Ubuntu 24.04 Hetzner images the netplan config that
# carries the private NIC sometimes isn't applied (interface stays
# DOWN with no IPv4). Without this, flannel can't bind and the agent
# never registers with the CP. Idempotent — no-op if the wait above
# already succeeded.
if ! ip -4 addr show | grep -q "$PRIVATE_IP"; then
  echo "==> private IP still missing — bringing enp7s0 up manually"
  ip link set enp7s0 up || true
  ip addr add "$PRIVATE_IP/24" dev enp7s0 || true
fi

echo "==> waiting for control plane API at $CP_PRIVATE_IP:6443"
# Use a TCP probe rather than HTTP: kube-apiserver answers / with 401
# (no auth provided), and curl -f rejects that as a failure, so the
# previous curl-based check looped forever and the agent install never
# ran. nc -z just checks the port is open, which is what we want.
for i in $(seq 1 120); do
  if nc -zw 5 "$CP_PRIVATE_IP" 6443 2>/dev/null; then
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
