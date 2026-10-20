// Méthode de connexion choisie par l'hébergeur (gamekit/auth), lue une fois au
// chargement dans /api/config : none, oidc, proxy ou local.

export interface AuthInfo {
  mode: string;
  /** « Se connecter avec … » ; absent : « Se connecter ». */
  accountName?: string;
  guestPlay: boolean;
  oidc?: { issuer: string; clientId: string };
  proxy?: { loginUrl?: string; logoutUrl?: string };
  local?: { loginUrl: string; logoutUrl: string; accountUrl: string; signup: string };
}

export interface ServerConfig {
  auth: AuthInfo;
  /** Le compte vu par le serveur (modes proxy et local), null sinon. */
  user: { id: string; name: string } | null;
}

const fallback: ServerConfig = { auth: { mode: "none", guestPlay: true }, user: null };

let loading: Promise<ServerConfig> | null = null;

export function serverConfig(): Promise<ServerConfig> {
  loading ??= fetch("/api/config", { credentials: "same-origin" })
    .then((r) => (r.ok ? (r.json() as Promise<ServerConfig>) : fallback))
    .catch(() => fallback);
  return loading;
}
