# A record for the app hostname. cert-manager HTTP-01 needs it to resolve
# before the ingress can obtain a TLS cert; `terraform apply` creates VMs
# and DNS in one shot so the order is correct.

data "cloudflare_zones" "selected" {
  filter {
    name = var.dns_zone
  }
}

resource "cloudflare_record" "app" {
  zone_id = data.cloudflare_zones.selected.zones[0].id
  name    = var.dns_subdomain
  type    = "A"
  # Points at the Hetzner LB; single stable address surviving any CP failure.
  value = hcloud_load_balancer.main.ipv4
  # Short TTL so a rebuilt LB/IP propagates within a minute.
  ttl = 60
  # DNS-only, not proxied: cert-manager HTTP-01 needs Let's Encrypt to reach
  # the host directly on :80, which the Cloudflare proxy would intercept.
  proxied = false
  comment = "Managed by Terraform — gemline control-plane"
}
