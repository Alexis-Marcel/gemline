#!/usr/bin/env bash
# cloud-init for the k3s control plane: bring up the Hetzner private NIC
# with the right IP + routes, then hand off to Ansible (which installs
# k3s, cert-manager, ArgoCD, and the Applications).

set -euo pipefail
exec > >(tee /var/log/gemline-cloud-init.log) 2>&1

PRIVATE_IP="${private_ip}"

echo "==> waiting for private IP $PRIVATE_IP on enp7s0"
for i in $(seq 1 60); do
  ip -4 addr show | grep -q "$PRIVATE_IP" && { echo "got it"; break; }
  sleep 2
done

# Fallback when Hetzner's netplan doesn't land. Hetzner Cloud Networks
# are routed, not switched, so a /32 + explicit routes via gateway
# 10.0.0.1 are required or node-to-node traffic gets "no route to host".
if ! ip -4 addr show | grep -q "$PRIVATE_IP"; then
  echo "==> private IP still missing — configuring enp7s0 manually (Hetzner-compatible)"
  ip link set enp7s0 up || true
  ip addr add "$PRIVATE_IP/32" dev enp7s0 || true
  ip route add 10.0.0.1 dev enp7s0 scope link || true
  ip route add 10.0.0.0/16 via 10.0.0.1 dev enp7s0 || true
fi

echo "==> cloud-init done — VM ready for Ansible"
