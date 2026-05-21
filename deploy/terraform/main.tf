# Multi-node k3s cluster on Hetzner Cloud.
#
# Topology:
#   - 1 control-plane node (k3s server) with the public IP
#   - N worker nodes (k3s agents), joining over a private Hetzner network
#
# k3s install + join token are handled by Ansible (see deploy/ansible/).
# Terraform only provisions the VMs, network, firewall, and DNS.
#
# Set worker_count = 0 to fall back to a single-node cluster.

resource "hcloud_ssh_key" "default" {
  name       = var.ssh_key_name
  public_key = file(pathexpand(var.ssh_public_key_path))
}

# Private network shared by all nodes. Free on Hetzner Cloud and keeps
# kubelet ↔ apiserver traffic off the public internet.
resource "hcloud_network" "internal" {
  name     = "${var.server_name}-net"
  ip_range = "10.0.0.0/16"
}

resource "hcloud_network_subnet" "internal" {
  network_id   = hcloud_network.internal.id
  type         = "cloud"
  network_zone = var.network_zone
  ip_range     = "10.0.1.0/24"
}

# ---------------------------------------------------------------------------
# Control plane
# ---------------------------------------------------------------------------

resource "hcloud_server" "control_plane" {
  name        = "${var.server_name}-cp"
  server_type = var.server_type
  image       = var.image
  location    = var.location
  ssh_keys    = [hcloud_ssh_key.default.id]

  # Minimal cloud-init: just brings up the private NIC. k3s/ArgoCD/etc.
  # installs moved to deploy/ansible/ (run via `make deploy`).
  user_data = templatefile("${path.module}/cloud-init/control-plane.sh.tpl", {
    private_ip = "10.0.1.10"
  })

  labels = {
    project = "gemline"
    role    = "control-plane"
  }

  public_net {
    ipv4_enabled = true
    ipv6_enabled = true
  }
}

resource "hcloud_server_network" "control_plane" {
  server_id  = hcloud_server.control_plane.id
  network_id = hcloud_network.internal.id
  ip         = "10.0.1.10"
}

# ---------------------------------------------------------------------------
# Workers
# ---------------------------------------------------------------------------

resource "hcloud_server" "workers" {
  count       = var.worker_count
  name        = "${var.server_name}-w${count.index + 1}"
  server_type = var.worker_server_type
  image       = var.image
  location    = var.location
  ssh_keys    = [hcloud_ssh_key.default.id]

  user_data = templatefile("${path.module}/cloud-init/agent.sh.tpl", {
    private_ip = "10.0.1.${11 + count.index}"
  })

  labels = {
    project = "gemline"
    role    = "worker"
  }

  public_net {
    ipv4_enabled = true
    ipv6_enabled = true
  }

  # Wait for the CP to exist (and ideally its cloud-init to finish) so
  # the agents have an API to join.
  depends_on = [hcloud_server_network.control_plane]
}

resource "hcloud_server_network" "workers" {
  count      = var.worker_count
  server_id  = hcloud_server.workers[count.index].id
  network_id = hcloud_network.internal.id
  ip         = "10.0.1.${11 + count.index}"
}

# ---------------------------------------------------------------------------
# Firewalls
# ---------------------------------------------------------------------------

# Public firewall: SSH + HTTP(S) are open to the world; the Kubernetes
# API is closed by default and opens only to source IPs listed in
# var.kubeapi_allowed_ips (defense-in-depth on top of mTLS).
resource "hcloud_firewall" "public" {
  name = "${var.server_name}-public"

  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "22"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "80"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "443"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  # Conditional kube-apiserver rule. When kubeapi_allowed_ips is empty,
  # no rule is emitted and the port stays closed (SSH tunnel only).
  dynamic "rule" {
    for_each = length(var.kubeapi_allowed_ips) > 0 ? [1] : []
    content {
      direction  = "in"
      protocol   = "tcp"
      port       = "6443"
      source_ips = var.kubeapi_allowed_ips
    }
  }
}

resource "hcloud_firewall_attachment" "all_nodes" {
  firewall_id = hcloud_firewall.public.id
  server_ids = concat(
    [hcloud_server.control_plane.id],
    hcloud_server.workers[*].id,
  )
}
