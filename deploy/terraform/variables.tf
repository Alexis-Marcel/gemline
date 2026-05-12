variable "hcloud_token" {
  description = "Hetzner Cloud API token. Generate at Console → Security → API Tokens (Read & Write)."
  type        = string
  sensitive   = true
}

variable "server_name" {
  description = "Name shown in the Hetzner dashboard and used as the cloud-init hostname."
  type        = string
  default     = "gemline"
}

variable "server_type" {
  description = "Hetzner Cloud server type. cx22 = 2 vCPU / 4 GB / 40 GB SSD, ~4.5 €/month."
  type        = string
  default     = "cx22"
}

variable "location" {
  description = "Datacenter location: fsn1 (Falkenstein DE), nbg1 (Nuremberg DE), hel1 (Helsinki FI), ash (Ashburn US), hil (Hillsboro US)."
  type        = string
  default     = "fsn1"
}

variable "image" {
  description = "Base OS image."
  type        = string
  default     = "ubuntu-24.04"
}

variable "ssh_public_key_path" {
  description = "Local path to the SSH public key uploaded to the server."
  type        = string
  default     = "~/.ssh/id_ed25519.pub"
}

variable "ssh_key_name" {
  description = "Name shown in the Hetzner dashboard for the uploaded SSH key."
  type        = string
  default     = "gemline"
}
