variable "hcloud_token" {
  description = "Hetzner Cloud API token. Generate at Console → Security → API Tokens (Read & Write)."
  type        = string
  sensitive   = true
}

variable "server_name" {
  description = "Prefix for every resource in the Hetzner dashboard (servers, network, firewall)."
  type        = string
  default     = "gemline"
}

variable "server_type" {
  description = "Hetzner type for the control-plane node. cx23 = 2 vCPU / 4 GB."
  type        = string
  default     = "cx23"
}

variable "cp_count" {
  description = <<-EOT
    Number of control-plane nodes.
      1 = non-HA (single point of failure, fine for dev/learning).
      3/5/7 = HA with k3s embedded etcd. Quorum needs an odd number
              of nodes; only odd values >= 3 give real fault tolerance.
    Even numbers are rejected to keep cluster semantics sane.
  EOT
  type        = number
  default     = 3

  validation {
    condition     = contains([1, 3, 5, 7], var.cp_count)
    error_message = "cp_count must be 1 (non-HA) or 3/5/7 (HA with embedded etcd)."
  }
}

variable "worker_count" {
  description = "How many worker nodes to attach to the control plane (0 = single-node cluster)."
  type        = number
  default     = 1
}

variable "worker_server_type" {
  description = "Hetzner type for each worker. Match server_type unless you want asymmetric nodes."
  type        = string
  default     = "cx23"
}

variable "location" {
  description = "Datacenter for every server. Workers and the CP must share this so the private network reaches them all."
  type        = string
  default     = "fsn1"
}

variable "network_zone" {
  description = "Hetzner network zone — must match the location's zone (fsn1/nbg1/hel1 → eu-central, ash → us-east, hil → us-west)."
  type        = string
  default     = "eu-central"
}

variable "image" {
  description = "Base OS image."
  type        = string
  default     = "ubuntu-24.04"
}

variable "ssh_public_key_path" {
  description = <<-EOT
    Path to the SSH public key uploaded to every node. Default points
    to the repo-committed key at ssh-keys/admin.pub (resolved relative
    to the Terraform module). The committed key is *public* — fine to
    track in git. Override locally with your own path if needed (the
    pathexpand() in main.tf handles `~`).
  EOT
  type        = string
  default     = "./ssh-keys/admin.pub"
}

variable "ssh_key_name" {
  description = "Name shown in the Hetzner dashboard for the uploaded SSH key."
  type        = string
  default     = "gemline"
}

variable "cloudflare_api_token" {
  description = <<-EOT
    Cloudflare API token used to manage the DNS A record pointing the
    app hostname at the control-plane public IP. Create it at
    Cloudflare Dashboard → My Profile → API Tokens → Create Token, with
    the "Edit zone DNS" template scoped to var.dns_zone.

    Pre-req: var.dns_zone must be a zone in your Cloudflare account
    (i.e. the registrar's nameservers point at Cloudflare's). If your
    domain lives elsewhere, swap the cloudflare provider in dns.tf for
    your registrar's (ovh, gandi, hetznerdns, …).
  EOT
  type        = string
  sensitive   = true
}

variable "dns_zone" {
  description = "Apex Cloudflare zone (e.g. \"werilo.fr\"). Must already exist in your Cloudflare account."
  type        = string
  default     = "werilo.fr"
}

variable "dns_subdomain" {
  description = <<-EOT
    Subdomain under var.dns_zone for the app — kept in sync with the
    Host: header in deploy/k8s/ingress.yaml. Default "gemline" produces
    "gemline.werilo.fr".
  EOT
  type        = string
  default     = "gemline"
}

# ArgoCD vars moved to deploy/ansible/group_vars/all/vars.yaml; TF no
# longer needs them.

variable "kubeapi_allowed_ips" {
  description = <<-EOT
    CIDRs allowed to reach the Kubernetes API on :6443.
    The API enforces mTLS regardless, but restricting by source IP adds
    defense-in-depth against stolen kubeconfigs and zero-day exploits in
    kube-apiserver.

    Default (empty list) keeps the port closed — admin via SSH tunnel.
    To allow direct kubectl from your laptop, set both:
      - your IPv4: result of `curl -s -4 ifconfig.me` with /32 suffix
      - your IPv6 /64 prefix (the first 4 groups, the rest replaced by ::)
    Example:
      kubeapi_allowed_ips = ["82.65.x.y/32", "2a01:cb0c:18f7:5d00::/64"]
  EOT
  type        = list(string)
  default     = []
}
