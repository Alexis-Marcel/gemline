import {
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import type { User } from "oidc-client-ts";
import { toAuthUser, userManager } from "../api/oidc";
import { userSocket } from "../api/userSocket";
import { AuthContext, type AuthContextValue } from "./AuthContext";

export function AuthProvider({ children }: { children: ReactNode }) {
  const [oidcUser, setOidcUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    // Initial session from localStorage, then follow sign-in / renewal / sign-out.
    userManager
      .getUser()
      .then((u) => setOidcUser(u && !u.expired ? u : null))
      .finally(() => setLoading(false));

    const onLoaded = (u: User) => setOidcUser(u);
    const onUnloaded = () => setOidcUser(null);
    // Renewal failed (refresh token expired or revoked): drop the stale session.
    const onRenewError = () => {
      void userManager.removeUser();
    };
    userManager.events.addUserLoaded(onLoaded);
    userManager.events.addUserUnloaded(onUnloaded);
    userManager.events.addSilentRenewError(onRenewError);
    return () => {
      userManager.events.removeUserLoaded(onLoaded);
      userManager.events.removeUserUnloaded(onUnloaded);
      userManager.events.removeSilentRenewError(onRenewError);
    };
  }, []);

  // Mirror the session into the user socket: open while authed, close on
  // sign-out so a logged-out viewer doesn't keep an authed WS open.
  useEffect(() => {
    const token = oidcUser?.access_token;
    if (token) {
      userSocket.open(token);
    } else {
      userSocket.close();
    }
  }, [oidcUser?.access_token]);

  // Let the socket refresh the access_token on reconnect failure — covers
  // the browser sleeping past the auto-renewal, which would otherwise loop
  // forever on the expired JWT.
  useEffect(() => {
    userSocket.setAuthRefresher(async () => {
      try {
        const u = await userManager.signinSilent();
        return u?.access_token ?? null;
      } catch {
        return null;
      }
    });
    return () => {
      userSocket.setAuthRefresher(null);
    };
  }, []);

  const value = useMemo<AuthContextValue>(
    () => ({
      user: toAuthUser(oidcUser),
      loading,
      jwt: oidcUser && !oidcUser.expired ? oidcUser.access_token : null,
      // Ends the provider session too (single sign-out across Eriphe apps).
      signOut: async () => {
        await userManager.signoutRedirect();
      },
    }),
    [oidcUser, loading],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}
