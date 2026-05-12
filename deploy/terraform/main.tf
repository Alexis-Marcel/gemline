# Multi-node k3s cluster on Hetzner Cloud.
#
# Topology:
#   - 1 control-plane node (k3s server) with the public IP
#   - N worker nodes (k3s agents), joining over a private Hetzner network
#   - Pre-shared join token (var.k3s_token) so cloud-init can do its job
#     without any extra round-trip
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

  user_data = templatefile("${path.module}/cloud-init/control-plane.sh.tpl", {
    private_ip = "10.0.1.10"
    k3s_token  = var.k3s_token
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
    private_ip    = "10.0.1.${11 + count.index}"
    cp_private_ip = "10.0.1.10"
    k3s_token     = var.k3s_token
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

# Public firewall: only SSH + HTTP(S) reach the cluster from outside.
# kube-apiserver (:6443) is NOT exposed publicly — administer via SSH
# tunnel or whitelist your IP if you really need remote kubectl.
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
}

resource "hcloud_firewall_attachment" "all_nodes" {
  firewall_id = hcloud_firewall.public.id
  server_ids = concat(
    [hcloud_server.control_plane.id],
    hcloud_server.workers[*].id,
  )
}
