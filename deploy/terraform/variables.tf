variable "hcloud_token" {
  description = "Hetzner Cloud API token. Generate at Console → Security → API Tokens (Read & Write)."
  type        = string
  sensitive   = true
}

variable "k3s_token" {
  description = <<-EOT
    Shared secret used by every node to authenticate cluster membership.
    Generate one once and pin it here: openssl rand -hex 32
    Anyone who has this token can register themselves as a node.
  EOT
  type        = string
  sensitive   = true
}

variable "server_name" {
  description = "Prefix for every resource in the Hetzner dashboard (servers, network, firewall)."
  type        = string
  default     = "gemline"
}

variable "server_type" {
  description = "Hetzner type for the control-plane node. cx22 = 2 vCPU / 4 GB."
  type        = string
  default     = "cx22"
}

variable "worker_count" {
  description = "How many worker nodes to attach to the control plane (0 = single-node cluster)."
  type        = number
  default     = 1
}

variable "worker_server_type" {
  description = "Hetzner type for each worker. Match server_type unless you want asymmetric nodes."
  type        = string
  default     = "cx22"
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
  description = "Local path to the SSH public key uploaded to every node."
  type        = string
  default     = "~/.ssh/id_ed25519.pub"
}

variable "ssh_key_name" {
  description = "Name shown in the Hetzner dashboard for the uploaded SSH key."
  type        = string
  default     = "gemline"
}

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
