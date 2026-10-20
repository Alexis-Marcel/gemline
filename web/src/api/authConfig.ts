// Méthode de connexion choisie par l'hébergeur (gamekit/auth), lue une fois au
// chargement dans /api/config : none, oidc, proxy ou local.

export interface AuthInfo {
  mode: string;
  /** « Se connecter avec … » ; absent : « Se connecter ». */
  accountName?: string;
  guestPlay: boolean;
  /** metadata: the provider's discovery, already done by the server (one round trip less). */
  oidc?: { issuer: string; clientId: string; metadata?: Record<string, string> };
  proxy?: { loginUrl?: string; logoutUrl?: string };
  local?: { loginUrl: string; logoutUrl: string; accountUrl: string; signup: string };
}

/** The portal Gemline belongs to (gamekit/portal), absent when it lives alone. */
export interface PortalInfo {
  url: string;
  name: string;
  icon?: string;
  profileUrl?: string;
}

export interface ServerConfig {
  auth: AuthInfo;
  /** Le compte vu par le serveur (modes proxy et local), null sinon. */
  user: { id: string; name: string } | null;
  portal: PortalInfo | null;
}

const fallback: ServerConfig = { auth: { mode: "none", guestPlay: true }, user: null, portal: null };

let loading: Promise<ServerConfig> | null = null;

export function serverConfig(): Promise<ServerConfig> {
  loading ??= fetch("/api/config", { credentials: "same-origin" })
    .then((r) => (r.ok ? (r.json() as Promise<ServerConfig>) : fallback))
    .catch(() => fallback);
  return loading;
}
