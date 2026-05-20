output "control_plane_ipv4" {
  description = "Public IPv4 of the control-plane node. Already wired to the Cloudflare A record by dns.tf."
  value       = hcloud_server.control_plane.ipv4_address
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
  description = "Total nodes provisioned, including the control plane."
  value       = 1 + var.worker_count
}

output "ssh_control_plane" {
  description = "Ready-to-paste SSH command to reach the CP."
  value       = "ssh root@${hcloud_server.control_plane.ipv4_address}"
}

output "next_steps" {
  description = "What to do once the cluster is up."
  value       = <<-EOT

    Cluster provisioned with 1 control-plane + ${var.worker_count} worker(s).

    1. The DNS A record is already created by Terraform:
         ${var.dns_subdomain}.${var.dns_zone}.   A   ${hcloud_server.control_plane.ipv4_address}
       Propagation is near-instant (TTL 60s).

    2. Wait ~10 minutes for cloud-init to finish on the control plane
       (k3s + cert-manager + ArgoCD + Applications), then copy the
       kubeconfig:
         ssh root@${hcloud_server.control_plane.ipv4_address} cat /etc/rancher/k3s/k3s.yaml > ~/.kube/gemline.yaml
         sed -i '' "s/127.0.0.1/${hcloud_server.control_plane.ipv4_address}/" ~/.kube/gemline.yaml
         export KUBECONFIG=~/.kube/gemline.yaml
         kubectl get nodes
       You should see ${1 + var.worker_count} Ready nodes.

    3. Continue with DEPLOY.md from "Edit hostnames in the manifests".
  EOT
}
