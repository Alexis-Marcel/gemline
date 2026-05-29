output "control_plane_ipv4" {
  description = <<-EOT
    Public IPv4 of the first control-plane node. Kept as a scalar for
    backward compatibility with the Ansible inventory script. For HA
    (cp_count > 1), use `control_plane_ipv4_all` to get every CP IP.
    Already wired to the Cloudflare A record by dns.tf.
  EOT
  value       = hcloud_server.control_plane[0].ipv4_address
}

output "control_plane_ipv4_all" {
  description = "Public IPv4 of every control-plane node, in order."
  value       = hcloud_server.control_plane[*].ipv4_address
}

output "control_plane_private_ips" {
  description = <<-EOT
    Private IPv4 of every CP, indexed the same as control_plane_ipv4_all.
    The Ansible inventory consumes this to populate `private_ip` hostvars
    without having to recompute the allocation logic on its own side.
  EOT
  value       = hcloud_server_network.control_plane[*].ip
}

output "worker_private_ips" {
  description = "Private IPv4 of every worker, indexed same as worker_ipv4."
  value       = hcloud_server_network.workers[*].ip
}

output "load_balancer_ipv4" {
  description = <<-EOT
    Public IPv4 of the Hetzner Load Balancer. The Cloudflare A record
    for `var.dns_subdomain.var.dns_zone` points here. kubectl, workers,
    and browsers should target this address (or the FQDN) rather than
    any individual CP IP.
  EOT
  value       = hcloud_load_balancer.main.ipv4
}

output "app_fqdn" {
  description = "Fully-qualified hostname the cluster serves at — A record managed by dns.tf."
  value       = "${var.dns_subdomain}.${var.dns_zone}"
}

output "worker_ipv4" {
  description = "Public IPv4 of every worker node (SSH only — no traffic should reach them from outside)."
  value       = hcloud_server.workers[*].ipv4_address
}

output "node_count" {
  description = "Total nodes provisioned, including the control plane(s)."
  value       = var.cp_count + var.worker_count
}

output "ssh_control_plane" {
  description = "Ready-to-paste SSH command to reach the CP."
  value       = "ssh root@${hcloud_server.control_plane[0].ipv4_address}"
}

output "next_steps" {
  description = "What to do once the cluster is up."
  value       = <<-EOT

    Cluster provisioned with ${var.cp_count} control-plane(s) + ${var.worker_count} worker(s).
    The DNS A record (${var.dns_subdomain}.${var.dns_zone} -> Load Balancer) is managed by Terraform.

    Next: configure the cluster with Ansible, then fetch the kubeconfig.

      cd ${path.cwd}/..        # the deploy/ directory
      make ansible             # installs k3s + cert-manager + ArgoCD + apps
      make kubeconfig          # writes ~/.kube/gemline.yaml
      export KUBECONFIG=~/.kube/gemline.yaml
      kubectl get nodes        # expect ${var.cp_count + var.worker_count} Ready nodes

    (`make deploy` runs terraform apply + ansible in one shot.) See DEPLOY.md.
  EOT
}
