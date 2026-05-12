output "control_plane_ipv4" {
  description = "Public IPv4 of the control-plane node. Point your DNS A record at this."
  value       = hcloud_server.control_plane.ipv4_address
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

    1. Point your domain at the CONTROL-PLANE IP (where Traefik listens):
         gemline.<your-domain>.   A   ${hcloud_server.control_plane.ipv4_address}

    2. Wait ~3 minutes for cloud-init to finish on every node, then copy
       the kubeconfig:
         ssh root@${hcloud_server.control_plane.ipv4_address} cat /etc/rancher/k3s/k3s.yaml > ~/.kube/gemline.yaml
         sed -i '' "s/127.0.0.1/${hcloud_server.control_plane.ipv4_address}/" ~/.kube/gemline.yaml
         export KUBECONFIG=~/.kube/gemline.yaml
         kubectl get nodes
       You should see ${1 + var.worker_count} Ready nodes.

    3. Continue with DEPLOY.md from "Edit hostnames in the manifests".
  EOT
}
