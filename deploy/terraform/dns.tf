# Cloudflare DNS record pointing <dns_subdomain>.<dns_zone> at the
# control-plane public IPv4. cert-manager's HTTP-01 challenge needs the
# A record to resolve to a host that serves on :80, so this must be in
# place before the ingress can get a real TLS cert — `terraform apply`
# creates VMs and DNS in one shot so the order is correct by the time
# cloud-init finishes installing Traefik / cert-manager.

data "cloudflare_zones" "selected" {
  filter {
    name = var.dns_zone
  }
}

resource "cloudflare_record" "app" {
  zone_id = data.cloudflare_zones.selected.zones[0].id
  name    = var.dns_subdomain
  type    = "A"
  # Pointe sur le premier CP. En Phase 2 (HA), ce record sera repointé
  # sur l'IP publique du Hetzner Load Balancer qui fronte tous les CPs.
  value = hcloud_server.control_plane[0].ipv4_address
  # 60s TTL keeps the recovery window short if we ever rebuild the CP
  # and the public IP changes — a fresh apply pushes the new value and
  # clients see it within a minute.
  ttl = 60
  # DNS-only (not proxied through Cloudflare): cert-manager HTTP-01
  # needs Let's Encrypt to reach the host directly on :80, which the
  # orange-cloud proxy intercepts. Also keeps WebSocket upgrades free
  # of any CF-side timeout quirks.
  proxied = false
  comment = "Managed by Terraform — gemline control-plane"
}
