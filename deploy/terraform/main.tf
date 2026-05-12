# Single-node VPS for k3s. Cloud-init runs the same bootstrap.sh that we
# ship for manual installs, so a fresh `terraform apply` produces a
# server that already has k3s and cert-manager running by the time SSH
# is reachable.

resource "hcloud_ssh_key" "default" {
  name       = var.ssh_key_name
  public_key = file(pathexpand(var.ssh_public_key_path))
}

resource "hcloud_server" "gemline" {
  name        = var.server_name
  server_type = var.server_type
  image       = var.image
  location    = var.location
  ssh_keys    = [hcloud_ssh_key.default.id]

  # The bootstrap script is sourced verbatim so any change to the manual
  # install path is reflected in the IaC one without duplication.
  user_data = file("${path.module}/../bootstrap.sh")

  labels = {
    project = "gemline"
  }

  public_net {
    ipv4_enabled = true
    ipv6_enabled = true
  }
}

# An explicit firewall in front of the node: only allow SSH, HTTP and
# HTTPS. k3s listens on 6443 (Kubernetes API) which we keep closed to
# the internet — administer it via SSH tunnel or restrict the source
# range here if you need remote kubectl.
resource "hcloud_firewall" "gemline" {
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

resource "hcloud_firewall_attachment" "gemline" {
  firewall_id = hcloud_firewall.gemline.id
  server_ids  = [hcloud_server.gemline.id]
}
