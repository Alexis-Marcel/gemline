#!/usr/bin/env bash
# Minimal cloud-init for the k3s control plane.
#
# Single responsibility: make sure the Hetzner private NIC comes up
# with the right IP + routes, so that:
#   1. The worker can SSH-less reach the CP on 10.0.1.10:6443 later
#   2. Ansible (running from your laptop) can SSH in on the public IP
#      and then drive everything else: install k3s, cert-manager,
#      ArgoCD, apply the Applications.
#
# Everything that was previously here (k3s install, cert-manager,
# ArgoCD bootstrap, Applications) has moved to deploy/ansible/.
# Cloud-init is now boot-time bootstrap; Ansible is configuration
# management (re-runnable, idempotent, testable). See deploy/Makefile
# for the orchestrated flow.

set -euo pipefail
exec > >(tee /var/log/gemline-cloud-init.log) 2>&1

PRIVATE_IP="${private_ip}"

# Wait for Hetzner's cloud-init network config to apply (it usually
# does within ~5-10s, sometimes longer on Ubuntu 24.04 minimal).
echo "==> waiting for private IP $PRIVATE_IP on enp7s0"
for i in $(seq 1 60); do
  ip -4 addr show | grep -q "$PRIVATE_IP" && { echo "got it"; break; }
  sleep 2
done

# Fallback for when Hetzner's netplan doesn't land. We replicate
# exactly what Hetzner DHCP would set:
#   - /32 on the NIC (Hetzner Cloud Networks are routed, not switched)
#   - link route to the gateway 10.0.0.1
#   - route for the whole private network via that gateway
# Without these, node-to-node traffic gets "no route to host" because
# the kernel ARPs for peers that aren't on the same L2 segment.
if ! ip -4 addr show | grep -q "$PRIVATE_IP"; then
  echo "==> private IP still missing — configuring enp7s0 manually (Hetzner-compatible)"
  ip link set enp7s0 up || true
  ip addr add "$PRIVATE_IP/32" dev enp7s0 || true
  ip route add 10.0.0.1 dev enp7s0 scope link || true
  ip route add 10.0.0.0/16 via 10.0.0.1 dev enp7s0 || true
fi

echo "==> cloud-init done — VM ready for Ansible"
