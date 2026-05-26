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
    // Pull the initial session synchronously from localStorage, then
    // subscribe so we react to login / logout / token refresh.
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

  // Mirror the current session into the persistent user socket. The
  // socket opens on first authenticated render and reconnects in the
  // background if the network blips; it closes on sign-out so a logged-
  // out viewer doesn't keep an authenticated WS hanging around.
  useEffect(() => {
    const token = session?.access_token;
    if (token) {
      userSocket.open(token);
    } else {
      userSocket.close();
    }
  }, [session?.access_token]);

  // Hand the socket a way to ask Supabase for a fresh access_token on
  // reconnect failure. Covers the case where the browser slept past
  // the regular auto-refresh: by the time we wake up the WS would
  // otherwise loop forever with the expired JWT. refreshSession() is
  // idempotent — Supabase no-ops when the current token still has
  // time on it.
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
