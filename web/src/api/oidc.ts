import { UserManager, WebStorageStateStore, type User } from "oidc-client-ts";

// Comptes : n'importe quel fournisseur OIDC (en production, le Compte Eriphe /
// Authentik). Client public avec PKCE : rien de secret dans le bundle.
const authority = import.meta.env.VITE_OIDC_ISSUER as string | undefined;
const clientId = import.meta.env.VITE_OIDC_CLIENT_ID as string | undefined;

export const authConfigured = Boolean(authority && clientId);

if (!authConfigured) {
  // Le jeu reste jouable en invité ; seule la connexion échoue.
  console.warn("OIDC env vars missing: set VITE_OIDC_ISSUER and VITE_OIDC_CLIENT_ID in web/.env.local");
}

export const userManager = new UserManager({
  authority: authority ?? "http://localhost/missing-oidc-issuer",
  client_id: clientId ?? "missing",
  redirect_uri: `${window.location.origin}/auth/callback`,
  post_logout_redirect_uri: `${window.location.origin}/`,
  // offline_access : jeton de rafraîchissement, la session survit au jeton d'accès (1 h).
  scope: "openid email profile offline_access",
  automaticSilentRenew: true,
  userStore: new WebStorageStateStore({ store: window.localStorage }),
});

export interface AuthUser {
  id: string;
  email: string | null;
  displayName: string | null;
}

export function toAuthUser(u: User | null): AuthUser | null {
  if (!u || u.expired) return null;
  const p = u.profile;
  return {
    id: p.sub,
    email: p.email ?? null,
    displayName: (p.name || p.preferred_username || "").trim() || null,
  };
}

/** Jeton d'accès valide pour le serveur, sinon null. */
export async function accessToken(): Promise<string | null> {
  const u = await userManager.getUser();
  return u && !u.expired ? u.access_token : null;
}

/** Envoie vers la page de connexion du fournisseur ; on revient sur `next`. */
export function signIn(next = "/") {
  return userManager.signinRedirect({ state: { next } });
}
