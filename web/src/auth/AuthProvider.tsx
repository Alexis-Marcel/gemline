import {
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import type { Session } from "@supabase/supabase-js";
import { supabase } from "../api/supabase";
import { userSocket } from "../api/userSocket";
import { AuthContext, type AuthContextValue } from "./AuthContext";

export function AuthProvider({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<Session | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    // Initial session from localStorage, then subscribe to auth changes.
    supabase.auth
      .getSession()
      .then(({ data }) => setSession(data.session))
      .finally(() => setLoading(false));

    const { data } = supabase.auth.onAuthStateChange((_event, s) => {
      setSession(s);
    });
    return () => {
      data.subscription.unsubscribe();
    };
  }, []);

  // Mirror the session into the user socket: open while authed, close on
  // sign-out so a logged-out viewer doesn't keep an authed WS open.
  useEffect(() => {
    const token = session?.access_token;
    if (token) {
      userSocket.open(token);
    } else {
      userSocket.close();
    }
  }, [session?.access_token]);

  // Let the socket refresh the access_token on reconnect failure — covers
  // the browser sleeping past the auto-refresh, which would otherwise loop
  // forever on the expired JWT. refreshSession() no-ops a still-valid token.
  useEffect(() => {
    userSocket.setAuthRefresher(async () => {
      const { data, error } = await supabase.auth.refreshSession();
      if (error) return null;
      return data.session?.access_token ?? null;
    });
    return () => {
      userSocket.setAuthRefresher(null);
    };
  }, []);

  const value = useMemo<AuthContextValue>(
    () => ({
      user: session?.user ?? null,
      session,
      loading,
      jwt: session?.access_token ?? null,
      signOut: async () => {
        await supabase.auth.signOut();
      },
    }),
    [session, loading],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}
