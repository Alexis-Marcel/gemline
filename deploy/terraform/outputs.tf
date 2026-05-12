output "ipv4_address" {
  description = "Public IPv4 of the VPS. Point your DNS A record at this."
  value       = hcloud_server.gemline.ipv4_address
}

output "ipv6_address" {
  description = "Public IPv6."
  value       = hcloud_server.gemline.ipv6_address
}

output "ssh_command" {
  description = "Ready-to-paste SSH command to reach the server."
  value       = "ssh root@${hcloud_server.gemline.ipv4_address}"
}

output "next_steps" {
  description = "What to do once the VPS is up."
  value       = <<-EOT

    1. Point your domain at this IP (A record):
         gemline.<your-domain>.   A   ${hcloud_server.gemline.ipv4_address}

    2. Wait ~3 minutes for cloud-init to finish installing k3s and
       cert-manager, then copy the kubeconfig:
         ssh root@${hcloud_server.gemline.ipv4_address} cat /etc/rancher/k3s/k3s.yaml > ~/.kube/gemline.yaml
         sed -i '' "s/127.0.0.1/${hcloud_server.gemline.ipv4_address}/" ~/.kube/gemline.yaml
         export KUBECONFIG=~/.kube/gemline.yaml
         kubectl get nodes

    3. Continue with DEPLOY.md from step 5.
  EOT
}
