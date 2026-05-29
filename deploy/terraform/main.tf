# k3s cluster on Hetzner Cloud. Terraform provisions VMs, network,
# firewall, LB, and DNS; k3s install + join token are handled by Ansible.

resource "hcloud_ssh_key" "default" {
  name       = var.ssh_key_name
  public_key = file(pathexpand(var.ssh_public_key_path))
}

# Private network keeps kubelet <-> apiserver traffic off the public internet.
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

# Control plane. 1 CP = non-HA; 3/5/7 = HA via k3s embedded etcd (quorum
# needs an odd number >= 3).

resource "hcloud_server" "control_plane" {
  count = var.cp_count

  # cp_count == 1 keeps the historical name "gemline-cp" that the Ansible
  # inventory script hard-codes; >1 switches to suffixed names.
  name = var.cp_count == 1 ? "${var.server_name}-cp" : "${var.server_name}-cp${count.index + 1}"

  server_type = var.server_type
  image       = var.image
  location    = var.location
  ssh_keys    = [hcloud_ssh_key.default.id]

  # cloud-init only brings up the private NIC; k3s/ArgoCD installs are in Ansible.
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

# `moved` rewrites the state address of the former singleton CP so adding
# count doesn't destroy+recreate it.
moved {
  from = hcloud_server.control_plane
  to   = hcloud_server.control_plane[0]
}

moved {
  from = hcloud_server_network.control_plane
  to   = hcloud_server_network.control_plane[0]
}

# Workers

resource "hcloud_server" "workers" {
  count       = var.worker_count
  name        = "${var.server_name}-w${count.index + 1}"
  server_type = var.worker_server_type
  image       = var.image
  location    = var.location
  ssh_keys    = [hcloud_ssh_key.default.id]

  # Worker private IPs continue right after the CP range so bumping
  # cp_count never collides with an existing worker IP.
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

  depends_on = [hcloud_server_network.control_plane]
}

resource "hcloud_server_network" "workers" {
  count      = var.worker_count
  server_id  = hcloud_server.workers[count.index].id
  network_id = hcloud_network.internal.id
  # Must match the IP injected by cloud-init via user_data above.
  ip = "10.0.1.${10 + var.cp_count + count.index}"
}

# SSH + HTTP(S) open to the world; the kube API stays closed unless
# var.kubeapi_allowed_ips lists source IPs (defense-in-depth on top of mTLS).
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

  # Empty kubeapi_allowed_ips emits no rule, leaving :6443 closed (SSH tunnel only).
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

# Load Balancer fronting the K8s API (6443) and app ingress (80/443) over
# the private network, giving kubectl/workers/browsers one stable address
# that survives any single CP failure.

resource "hcloud_load_balancer" "main" {
  name               = "${var.server_name}-lb"
  load_balancer_type = "lb11"
  location           = var.location

  labels = {
    project = "gemline"
  }
}

# Keep LB->CP traffic on the private network (no egress charge, no public hop).
resource "hcloud_load_balancer_network" "main" {
  load_balancer_id = hcloud_load_balancer.main.id
  network_id       = hcloud_network.internal.id
}

# Label selector auto-adds any node tagged role=control-plane, so bumping
# cp_count needs no Terraform change here.
resource "hcloud_load_balancer_target" "control_plane" {
  load_balancer_id = hcloud_load_balancer.main.id
  type             = "label_selector"
  label_selector   = "role=control-plane"
  use_private_ip   = true

  # use_private_ip targets only resolve once the LB is in the private
  # network; TF can't infer this through the label_selector indirection.
  depends_on = [hcloud_load_balancer_network.main]
}

# K8s API: TCP passthrough, TLS terminated by the kube-apiserver itself.
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

# App HTTP, also carries cert-manager's HTTP-01 ACME challenge on :80.
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

# App HTTPS: TCP passthrough, TLS terminated by Traefik so cert lifecycle
# stays with cert-manager + Traefik.
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
