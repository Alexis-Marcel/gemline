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
# never registers with the CP. We replicate exactly what Hetzner DHCP
# would have set:
#   - /32 on the interface (Hetzner networks are routed, not switched —
#     a /24 here makes the kernel ARP for peers that aren't on the same
#     L2 segment, so node-to-CP traffic gets "no route to host")
#   - link-scope route to 10.0.0.1 (the Hetzner gateway)
#   - route for the whole private network via that gateway, so traffic
#     to the CP (10.0.1.10) gets forwarded properly
# Idempotent: no-op if the wait above already succeeded.
if ! ip -4 addr show | grep -q "$PRIVATE_IP"; then
  echo "==> private IP still missing — configuring enp7s0 manually (Hetzner-compatible)"
  ip link set enp7s0 up || true
  ip addr add "$PRIVATE_IP/32" dev enp7s0 || true
  ip route add 10.0.0.1 dev enp7s0 scope link || true
  ip route add 10.0.0.0/16 via 10.0.0.1 dev enp7s0 || true
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
