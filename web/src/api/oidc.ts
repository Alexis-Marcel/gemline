import { InMemoryWebStorage, UserManager, WebStorageStateStore, type User } from "oidc-client-ts";

// Mode oidc : n'importe quel fournisseur OIDC, configuré par le serveur
// (/api/config, voir authConfig.ts) et pas au build : la même image sert
// chaque hébergeur. Client public avec PKCE : rien de secret dans le bundle.

let manager: UserManager | null = null;

export function configureOIDC(issuer: string, clientId: string) {
  if (manager || !issuer || !clientId) return;
  manager = new UserManager({
    authority: issuer,
    client_id: clientId,
    redirect_uri: `${window.location.origin}/auth/callback`,
    // Hidden iframe for SSO: already signed in to the provider (on another app
    // of the same site) means signed in here too, without a click. Works when
    // the provider and the app share a site, so its session cookie reaches the iframe.
    silent_redirect_uri: `${window.location.origin}/auth/silent`,
    silentRequestTimeoutInSeconds: 5,
    post_logout_redirect_uri: `${window.location.origin}/`,
    // Tokens live in memory only, with no refresh token: each page load and each
    // renewal asks the provider again through the silent iframe. An XSS could at
    // worst grab a 1 h access token, never a long-lived refresh token.
    scope: "openid email profile",
    automaticSilentRenew: true,
    userStore: new WebStorageStateStore({ store: new InMemoryWebStorage() }),
  });
}

/** Le client OIDC, null hors du mode oidc. */
export function userManager(): UserManager | null {
  return manager;
}

// Tokens persisted by an older (localStorage) version: drop them.
for (const key of Object.keys(window.localStorage)) {
  if (key.startsWith("oidc.user:")) window.localStorage.removeItem(key);
}

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

/** Jeton d'accès valide pour le serveur, sinon null (hors mode oidc, ou sans session). */
export async function accessToken(): Promise<string | null> {
  if (!manager) return null;
  await restoreSession();
  const u = await manager.getUser();
  return u && !u.expired ? u.access_token : null;
}

let restoring: Promise<User | null> | null = null;

/**
 * Session au chargement : une tentative silencieuse (prompt=none dans une iframe)
 * auprès du fournisseur, les jetons n'étant qu'en mémoire. Jamais d'erreur : sans
 * session chez le fournisseur, on reste simplement invité. Mémoïsé (StrictMode).
 */
export function restoreSession(): Promise<User | null> {
  const m = manager;
  if (!m) return Promise.resolve(null);
  restoring ??= (async () => {
    const stored = await m.getUser();
    if (stored && !stored.expired) return stored;
    // Not in an iframe, and not on /auth/callback where a sign-in is already finishing.
    if (window.top !== window.self || window.location.pathname.startsWith("/auth/")) return null;
    try {
      return await m.signinSilent();
    } catch {
      return null;
    }
  })();
  return restoring;
}

// Déconnexion en cours : la session locale tombe avant que le navigateur quitte la page, et une
// page qui exige un compte (Profil…) relancerait aussitôt une connexion, plus rapide que la fin de
// la session chez le fournisseur — on se retrouverait reconnecté. Plus aucune connexion d'ici là.
let signingOut = false;

/** Envoie vers la page de connexion du fournisseur ; on revient sur `next`. */
export function signIn(next = "/") {
  if (signingOut || !manager) return Promise.resolve();
  return manager.signinRedirect({ state: { next } });
}

/** Ferme la session ici et chez le fournisseur (donc dans ses autres applications). */
export function signOut() {
  signingOut = true;
  return manager?.signoutRedirect() ?? Promise.resolve();
}
