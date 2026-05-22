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
# Control plane (count = var.cp_count)
# ---------------------------------------------------------------------------
#
# 1 CP   = non-HA (current default).
# 3+ CPs = HA via k3s embedded etcd. Quorum requires odd numbers >= 3.
#
# Phase 1 of the HA migration: the resources are now counted but
# cp_count stays at its default (1). Apply produces zero infra
# change thanks to the `moved` blocks below.

resource "hcloud_server" "control_plane" {
  count = var.cp_count

  # When cp_count == 1, keep the historical name "gemline-cp" so the
  # Ansible inventory script (which hard-codes that name) keeps
  # working. When cp_count > 1, switch to suffixed names; the Ansible
  # side will need updating in Phase 4 to match.
  name = var.cp_count == 1 ? "${var.server_name}-cp" : "${var.server_name}-cp${count.index + 1}"

  server_type = var.server_type
  image       = var.image
  location    = var.location
  ssh_keys    = [hcloud_ssh_key.default.id]

  # Minimal cloud-init: just brings up the private NIC. k3s/ArgoCD/etc.
  # installs moved to deploy/ansible/ (run via `make deploy`).
  user_data = templatefile("${path.module}/cloud-init/control-plane.sh.tpl", {
    private_ip = "10.0.1.${10 + count.index}"
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
  count      = var.cp_count
  server_id  = hcloud_server.control_plane[count.index].id
  network_id = hcloud_network.internal.id
  ip         = "10.0.1.${10 + count.index}"
}

# State migration: the singleton CP resources are now counted, which
# would normally trigger destroy+create. The `moved` block (TF >= 1.1)
# tells Terraform to rewrite the addresses in state instead — zero
# infra change on apply.
moved {
  from = hcloud_server.control_plane
  to   = hcloud_server.control_plane[0]
}

moved {
  from = hcloud_server_network.control_plane
  to   = hcloud_server_network.control_plane[0]
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

  # Worker private IPs start right after the CP range. CPs take
  # 10.0.1.10..10+cp_count-1, workers continue from there. Keeps the
  # allocation collision-free when we bump cp_count from 1 to 3.
  user_data = templatefile("${path.module}/cloud-init/agent.sh.tpl", {
    private_ip = "10.0.1.${10 + var.cp_count + count.index}"
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
  # Must match the IP injected by cloud-init via user_data above.
  ip = "10.0.1.${10 + var.cp_count + count.index}"
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
    hcloud_server.control_plane[*].id,
    hcloud_server.workers[*].id,
  )
}

# ---------------------------------------------------------------------------
# Load Balancer (HA phase 2)
# ---------------------------------------------------------------------------
#
# Fronts the K8s API (6443) and the app HTTP/HTTPS ingress (80/443).
# Routes to control-plane nodes via the private network. Necessary for
# HA: once cp_count > 1, kubectl + workers + browsers all share one
# stable address, and a CP failure becomes transparent.
#
# In phase 2 the LB simply sits in front of the existing single CP —
# adds one hop but proves the routing layer end-to-end before we add
# more CPs in phase 4.

resource "hcloud_load_balancer" "main" {
  name               = "${var.server_name}-lb"
  load_balancer_type = "lb11" # 1 vCPU, 50 GbE pipe, 20k concurrent — ample for our scale
  location           = var.location

  labels = {
    project = "gemline"
  }
}

# Attach to the private network so LB->CP traffic stays on Hetzner's
# private fabric (free, no egress charge, no public hop).
resource "hcloud_load_balancer_network" "main" {
  load_balancer_id = hcloud_load_balancer.main.id
  network_id       = hcloud_network.internal.id
}

# Targets are chosen by Hetzner labels, not by static server IDs. When
# we bump cp_count to 3 in phase 4, the two new CPs inherit
# role=control-plane and are auto-added — no Terraform churn here.
resource "hcloud_load_balancer_target" "control_plane" {
  load_balancer_id = hcloud_load_balancer.main.id
  type             = "label_selector"
  label_selector   = "role=control-plane"
  use_private_ip   = true

  # The LB must be in the private network before targets using
  # use_private_ip can resolve; Terraform can't infer this through the
  # label_selector indirection.
  depends_on = [hcloud_load_balancer_network.main]
}

# K8s API. Workers and kubectl talk to <lb-ip>:6443.
# TCP passthrough — TLS is terminated by the kube-apiserver itself.
resource "hcloud_load_balancer_service" "api" {
  load_balancer_id = hcloud_load_balancer.main.id
  protocol         = "tcp"
  listen_port      = 6443
  destination_port = 6443

  health_check {
    protocol = "tcp"
    port     = 6443
    interval = 15
    timeout  = 10
    retries  = 3
  }
}

# App HTTP — used both by Traefik and by cert-manager's HTTP-01 ACME
# challenges (Let's Encrypt hits gemline.werilo.fr:80/.well-known/...).
resource "hcloud_load_balancer_service" "http" {
  load_balancer_id = hcloud_load_balancer.main.id
  protocol         = "tcp"
  listen_port      = 80
  destination_port = 80

  health_check {
    protocol = "tcp"
    port     = 80
    interval = 15
    timeout  = 10
    retries  = 3
  }
}

# App HTTPS — TCP passthrough, TLS terminated by Traefik on the
# destination. Keeps cert lifecycle (cert-manager + Traefik) in one
# place; the LB is just a dumb pipe.
resource "hcloud_load_balancer_service" "https" {
  load_balancer_id = hcloud_load_balancer.main.id
  protocol         = "tcp"
  listen_port      = 443
  destination_port = 443

  health_check {
    protocol = "tcp"
    port     = 443
    interval = 15
    timeout  = 10
    retries  = 3
  }
}
