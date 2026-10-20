import { useEffect, useMemo, useState, type ReactNode } from "react";
import type { User } from "oidc-client-ts";
import { serverConfig, type ServerConfig } from "../api/authConfig";
import { configureOIDC, restoreSession, signIn, signOut, toAuthUser, userManager, type AuthUser } from "../api/oidc";
import { userSocket } from "../api/userSocket";
import { AuthContext, type AuthContextValue } from "./AuthContext";

// Accounts follow the host's method (/api/config): oidc keeps a token in the
// browser; proxy and local are known by the server (reverse proxy header or
// session cookie), which tells us who is signed in.
export function AuthProvider({ children }: { children: ReactNode }) {
  const [config, setConfig] = useState<ServerConfig | null>(null);
  const [oidcUser, setOidcUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cleanup = () => {};
    void serverConfig().then(async (cfg) => {
      setConfig(cfg);
      if (cfg.auth.mode !== "oidc" || !cfg.auth.oidc) {
        setLoading(false);
        return;
      }
      configureOIDC(cfg.auth.oidc.issuer, cfg.auth.oidc.clientId);
      const m = userManager()!;
      // Initial session (stored, else silent SSO with the provider), then
      // follow sign-in / renewal / sign-out.
      setOidcUser(await restoreSession());
      setLoading(false);
      const onLoaded = (u: User) => setOidcUser(u);
      const onUnloaded = () => setOidcUser(null);
      // Renewal failed (provider session ended): drop the stale session.
      const onRenewError = () => void m.removeUser();
      m.events.addUserLoaded(onLoaded);
      m.events.addUserUnloaded(onUnloaded);
      m.events.addSilentRenewError(onRenewError);
      // Let the socket refresh the access_token on reconnect failure — covers
      // the browser sleeping past the auto-renewal.
      userSocket.setAuthRefresher(async () => {
        try {
          const u = await m.signinSilent();
          return u?.access_token ?? null;
        } catch {
          return null;
        }
      });
      cleanup = () => {
        m.events.removeUserLoaded(onLoaded);
        m.events.removeUserUnloaded(onUnloaded);
        m.events.removeSilentRenewError(onRenewError);
        userSocket.setAuthRefresher(null);
      };
    });
    return () => cleanup();
  }, []);

  const mode = config?.auth.mode ?? "none";
  const serverUser: AuthUser | null =
    (mode === "proxy" || mode === "local") && config?.user
      ? { id: config.user.id, email: null, displayName: config.user.name || null }
      : null;
  const user = mode === "oidc" ? toAuthUser(oidcUser) : serverUser;
  const token = mode === "oidc" && oidcUser && !oidcUser.expired ? oidcUser.access_token : null;

  // Mirror the session into the user socket: open while signed in (with the
  // bearer in oidc mode, the cookie or proxy header otherwise), close on sign-out.
  const signedIn = Boolean(user);
  useEffect(() => {
    if (!signedIn) userSocket.close();
    else userSocket.open(token);
  }, [signedIn, token]);

  const value = useMemo<AuthContextValue>(() => {
    const auth = config?.auth;
    const proxy = auth?.proxy;
    const local = auth?.local;
    return {
      user,
      loading,
      jwt: token,
      mode,
      accounts: mode === "oidc" || mode === "local" || (mode === "proxy" && Boolean(user || proxy?.loginUrl)),
      accountName: auth?.accountName ?? "",
      guestPlay: auth?.guestPlay ?? true,
      accountUrl: local?.accountUrl ?? "",
      canSignOut: mode === "oidc" || mode === "local" || Boolean(proxy?.logoutUrl),
      portal: config?.portal ?? null,
      signIn: (next = window.location.pathname + window.location.search) => {
        if (mode === "oidc") void signIn(next);
        else if (mode === "local" && local) window.location.href = `${local.loginUrl}?next=${encodeURIComponent(next)}`;
        else if (mode === "proxy" && proxy?.loginUrl) window.location.href = proxy.loginUrl;
      },
      // oidc: ends the provider session too (single sign-out across its apps).
      signOut: async () => {
        if (mode === "oidc") await signOut();
        else if (mode === "local" && local) {
          await fetch(local.logoutUrl, { method: "POST", credentials: "same-origin" }).catch(() => {});
          window.location.assign("/");
        } else if (mode === "proxy" && proxy?.logoutUrl) window.location.href = proxy.logoutUrl;
      },
    };
  }, [config, user, loading, token, mode]);

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}
